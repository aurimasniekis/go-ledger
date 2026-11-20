package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestEmbeddedMigrationsLoad(t *testing.T) {
	ms, err := EmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].Version != 1 || ms[0].Name != "schema_migrations" {
		t.Fatalf("first migration is %04d_%s, want 0001_schema_migrations", ms[0].Version, ms[0].Name)
	}
}

func TestLoadMigrationsRejectsMalformedSets(t *testing.T) {
	ok := "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT;"
	tests := map[string]fstest.MapFS{
		"gap":             {"0001_a.sql": {Data: []byte(ok)}, "0003_c.sql": {Data: []byte(ok)}},
		"duplicate":       {"0001_a.sql": {Data: []byte(ok)}, "0002_b.sql": {Data: []byte(ok)}, "0002_c.sql": {Data: []byte(ok)}},
		"not from one":    {"0002_b.sql": {Data: []byte(ok)}},
		"bad name":        {"0001_a.sql": {Data: []byte(ok)}, "0002-B.sql": {Data: []byte(ok)}},
		"wrong extension": {"0001_a.sql": {Data: []byte(ok)}, "0002_b.SQL": {Data: []byte(ok)}},
		"three digits":    {"001_a.sql": {Data: []byte(ok)}},
		"empty file":      {"0001_a.sql": {Data: []byte(" \n")}},
		"directory":       {"0001_a.sql": {Data: []byte(ok)}, "0002_b/x.sql": {Data: []byte(ok)}},
		"no migrations":   {},
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadMigrations(fsys); !errors.Is(err, ErrInvalidMigrations) {
				t.Fatalf("err = %v, want ErrInvalidMigrations", err)
			}
		})
	}
}

func TestChecksumIgnoresLineEndingConversion(t *testing.T) {
	lf := mapMigrations(t, map[string]string{"0001_a.sql": "SELECT 1;\nSELECT 2;\n"})
	crlf := mapMigrations(t, map[string]string{"0001_a.sql": "SELECT 1;\r\nSELECT 2;\r\n"})
	if lf[0].Checksum != crlf[0].Checksum {
		t.Fatal("a CRLF checkout changes the checksum")
	}
}

// exampleSet returns the first n harness-example migrations.
func exampleSet(t *testing.T, n int) []Migration {
	t.Helper()
	all := dirMigrations(t, filepath.Join("testdata", "harness-example", "migrations"))
	return all[:n]
}

func TestMigrateAppliesInOrderAndRecordsUTC(t *testing.T) {
	plus3 := time.FixedZone("UTC+3", 3*60*60)
	db := openTest(t, tempPath(t), Options{
		Migrations: exampleSet(t, 3),
		Now:        func() time.Time { return time.Date(2026, 11, 20, 1, 0, 0, 0, plus3) },
	})
	if db.SchemaVersion() != 3 {
		t.Fatalf("version %d, want 3", db.SchemaVersion())
	}
	rows, err := db.readers.Query("SELECT version, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	want := 1
	for rows.Next() {
		var v int
		var at string
		if err := rows.Scan(&v, &at); err != nil {
			t.Fatal(err)
		}
		if v != want {
			t.Errorf("version %d recorded where %d was expected", v, want)
		}
		if at != "2026-11-19T22:00:00.000000000Z" {
			t.Errorf("applied_at = %s, want the UTC instant", at)
		}
		want++
	}
}

func TestMigrateUpgradesStepByStep(t *testing.T) {
	path := tempPath(t)
	db := openTest(t, path, Options{Migrations: exampleSet(t, 2)})
	mustExec(t, db.writer, "INSERT INTO accounts (id, name, created_at) VALUES (1, 'Everyday', ?)", Timestamp{time.Now()})
	mustExec(t, db.writer, "INSERT INTO entries (id, account_id, memo) VALUES (1, 1, 'rent')")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTest(t, path, Options{Migrations: exampleSet(t, 3)})
	if db.SchemaVersion() != 3 {
		t.Fatalf("version %d, want 3", db.SchemaVersion())
	}
	if n := queryInt(t, db.readers, "SELECT count(*) FROM entries"); n != 1 {
		t.Fatalf("entries = %d after the rebuild, want 1", n)
	}
}

func TestMigrateRefusesToRunBackwards(t *testing.T) {
	path := tempPath(t)
	db := openTest(t, path, Options{Migrations: exampleSet(t, 3)})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path, Options{Migrations: exampleSet(t, 2)})
	if !errors.Is(err, ErrDatabaseNewer) {
		t.Fatalf("err = %v, want ErrDatabaseNewer", err)
	}
	if n := queryInt(t, rawDB(t, path), "SELECT count(*) FROM schema_migrations"); n != 3 {
		t.Fatalf("schema_migrations has %d rows after the refusal, want 3 (untouched)", n)
	}
}

func TestMigrateRefusesOutOfOrder(t *testing.T) {
	ctx := context.Background()
	m1 := embeddedSQL(t, "0001_schema_migrations.sql")
	t.Run("gap in applied versions", func(t *testing.T) {
		path := tempPath(t)
		db := openTest(t, path, Options{Migrations: exampleSet(t, 1)})
		_ = db.Close()
		// A database that claims 0001 and 0003 but not 0002.
		known := exampleSet(t, 3)
		mustExec(t, rawDB(t, path), "INSERT INTO schema_migrations VALUES (3, ?, ?, ?)", known[2].Name, known[2].Checksum, Timestamp{time.Now()})
		_, err := Open(ctx, path, Options{Migrations: known})
		if !errors.Is(err, ErrOutOfOrder) {
			t.Fatalf("err = %v, want ErrOutOfOrder", err)
		}
	})
	t.Run("lower-numbered migration added after a higher one ran", func(t *testing.T) {
		path := tempPath(t)
		old := mapMigrations(t, map[string]string{
			"0001_schema_migrations.sql": m1,
			"0002_b.sql":                 "CREATE TABLE b (id INTEGER PRIMARY KEY) STRICT;",
		})
		db := openTest(t, path, Options{Migrations: old})
		_ = db.Close()
		renumbered := mapMigrations(t, map[string]string{
			"0001_schema_migrations.sql": m1,
			"0002_a.sql":                 "CREATE TABLE a (id INTEGER PRIMARY KEY) STRICT;",
			"0003_b.sql":                 "CREATE TABLE b (id INTEGER PRIMARY KEY) STRICT;",
		})
		_, err := Open(ctx, path, Options{Migrations: renumbered})
		if !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("err = %v, want ErrChecksumMismatch", err)
		}
	})
}

func TestMigrateRefusesEditedMigration(t *testing.T) {
	path := tempPath(t)
	m1 := embeddedSQL(t, "0001_schema_migrations.sql")
	db := openTest(t, path, Options{Migrations: mapMigrations(t, map[string]string{
		"0001_schema_migrations.sql": m1,
		"0002_a.sql":                 "CREATE TABLE a (id INTEGER PRIMARY KEY) STRICT;",
	})})
	_ = db.Close()
	_, err := Open(context.Background(), path, Options{Migrations: mapMigrations(t, map[string]string{
		"0001_schema_migrations.sql": m1,
		"0002_a.sql":                 "CREATE TABLE a (id INTEGER PRIMARY KEY, x TEXT) STRICT;",
	})})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
}

func TestFailedMigrationRollsBackCompletely(t *testing.T) {
	path := tempPath(t)
	m1 := embeddedSQL(t, "0001_schema_migrations.sql")
	_, err := Open(context.Background(), path, Options{Migrations: mapMigrations(t, map[string]string{
		"0001_schema_migrations.sql": m1,
		"0002_half.sql":              "CREATE TABLE half (id INTEGER PRIMARY KEY) STRICT;\nINSERT INTO missing_table VALUES (1);",
	})})
	if err == nil || !strings.Contains(err.Error(), "0002_half failed and was rolled back") {
		t.Fatalf("err = %v, want a rolled-back 0002_half", err)
	}
	raw := rawDB(t, path)
	if n := queryInt(t, raw, "SELECT count(*) FROM sqlite_schema WHERE name = 'half'"); n != 0 {
		t.Error("table from the failed migration's first statement survived")
	}
	if n := queryInt(t, raw, "SELECT max(version) FROM schema_migrations"); n != 1 {
		t.Errorf("version = %d, want 1", n)
	}
}

func TestForeignKeysOffMigrationMustLeaveNoViolations(t *testing.T) {
	path := tempPath(t)
	db := openTest(t, path, Options{Migrations: exampleSet(t, 2)})
	_ = db.Close()
	m := exampleSet(t, 2)
	files := map[string]string{
		"0001_schema_migrations.sql": m[0].SQL,
		"0002_accounts.sql":          m[1].SQL,
		"0003_orphan.sql":            foreignKeysOffDirective + "\nINSERT INTO entries (id, account_id, memo) VALUES (1, 42, 'orphan');",
	}
	_, err := Open(context.Background(), path, Options{Migrations: mapMigrations(t, files)})
	if !errors.Is(err, ErrForeignKeyViolation) {
		t.Fatalf("err = %v, want ErrForeignKeyViolation", err)
	}
	if n := queryInt(t, rawDB(t, path), "SELECT count(*) FROM entries"); n != 0 {
		t.Fatalf("orphan row kept: %d", n)
	}
}

func TestOpenRefusesUnversionedDatabase(t *testing.T) {
	path := tempPath(t)
	mustExec(t, rawDB(t, path), "CREATE TABLE something_else (id INTEGER PRIMARY KEY)")
	if _, err := Open(context.Background(), path, Options{}); !errors.Is(err, ErrUnversioned) {
		t.Fatalf("err = %v, want ErrUnversioned", err)
	}
}

// TestConcurrentOpenMigratesOnce opens the same fresh file from several
// goroutines, each with its own pools, as separate processes would.
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	path := tempPath(t)
	known := exampleSet(t, 3)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			db, err := Open(context.Background(), path, Options{Migrations: known})
			if err == nil {
				err = db.Close()
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := rawDB(t, path)
	if n := queryInt(t, raw, "SELECT count(*) FROM schema_migrations"); n != 3 {
		t.Fatalf("schema_migrations rows = %d, want 3", n)
	}
}
