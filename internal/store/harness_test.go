package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The migration test harness. A fixture is a SQL script that recreates a
// database exactly as an earlier binary left it at schema version NNNN
// (file name NNNN_description.sql): its tables, its schema_migrations rows
// with the checksums of that time, and representative synthetic rows. The
// harness loads each fixture into a fresh file without the migration runner,
// opens it with the current migrations, and checks that the upgrade reaches
// the latest version with history, integrity and foreign keys intact.
//
// When a migration is added, add a fixture for the version before it (the
// coverage test below fails until there is one), and register a check in
// fixtureChecks if the migration moves data.

var fixtureName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

type fixture struct {
	version int
	name    string
	path    string
}

func loadFixtures(t *testing.T, dir string) []fixture {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []fixture
	for _, e := range entries {
		m := fixtureName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Fatalf("fixture %s is not named NNNN_description.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		out = append(out, fixture{version: v, name: e.Name(), path: filepath.Join(dir, e.Name())})
	}
	return out
}

// upgradeFixture runs one fixture through the harness and returns the opened,
// upgraded database for fixture-specific checks.
func upgradeFixture(t *testing.T, fx fixture, known []Migration) *DB {
	t.Helper()
	ctx := context.Background()
	script, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	path := tempPath(t)
	raw := rawDB(t, path)
	mustExec(t, raw, string(script))
	before, err := readApplied(ctx, raw)
	if err != nil {
		t.Fatalf("fixture %s: %v", fx.name, err)
	}
	if len(before) != fx.version {
		t.Fatalf("fixture %s records %d migrations, its name says version %d", fx.name, len(before), fx.version)
	}
	if err := checkApplied(before, known); err != nil {
		t.Fatalf("fixture %s does not match the migrations it claims: %v", fx.name, err)
	}
	var appliedAtBefore []string
	if fx.version > 0 {
		rows, err := raw.QueryContext(ctx, "SELECT applied_at FROM schema_migrations ORDER BY version")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			appliedAtBefore = append(appliedAtBefore, s)
		}
		_ = rows.Close()
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db := openTest(t, path, Options{Migrations: known})
	if got := db.SchemaVersion(); got != len(known) {
		t.Fatalf("fixture %s upgraded to version %d, want %d", fx.name, got, len(known))
	}
	err = db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		var integrity string
		if err := tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
			return err
		}
		if integrity != "ok" {
			t.Errorf("fixture %s: integrity_check = %q", fx.name, integrity)
		}
		if err := foreignKeyCheck(ctx, tx); err != nil {
			t.Errorf("fixture %s: %v", fx.name, err)
		}
		after, err := readApplied(ctx, tx)
		if err != nil {
			return err
		}
		if len(after) != len(known) {
			t.Errorf("fixture %s: %d migrations recorded, want %d", fx.name, len(after), len(known))
		}
		for i, want := range appliedAtBefore {
			var got string
			if err := tx.QueryRowContext(ctx, "SELECT applied_at FROM schema_migrations WHERE version = ?", i+1).Scan(&got); err != nil {
				return err
			}
			if got != want {
				t.Errorf("fixture %s: applied_at of version %d changed from %s to %s", fx.name, i+1, want, got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fixture %s: %v", fx.name, err)
	}
	return db
}

// fixtureChecks holds data assertions for fixtures of the real schema, by
// fixture file name. Add one whenever a migration moves or reshapes data.
var fixtureChecks = map[string]func(t *testing.T, db *DB){
	// Migration 0027 adds commitment leeway: existing commitments have
	// recorded none, which is not the same as "unknown" or "none".
	"0026_merchants.sql": func(t *testing.T, db *DB) {
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM commitments"); n == 0 {
				t.Error("the fixture has no commitments to check")
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM commitments WHERE leeway_kind IS NOT NULL OR leeway_days IS NOT NULL OR leeway_until IS NOT NULL"); n != 0 {
				t.Errorf("%d commitments have leeway after the migration, want none", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	},
	// Migration 0021 adds the tolerance a match was confirmed within:
	// existing matches were confirmed within none.
	"0020_commitment_match_tolerance.sql": func(t *testing.T, db *DB) {
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM payment_matches"); n == 0 {
				t.Error("the fixture has no payment matches to check")
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM payment_matches WHERE tolerance_amount IS NOT NULL OR tolerance_currency IS NOT NULL"); n != 0 {
				t.Errorf("%d matches have a recorded tolerance after the migration, want none", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	},
	// Migration 0018 adds the token parent: existing tokens have none.
	"0017_payment_matches.sql": func(t *testing.T, db *DB) {
		err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			if n := queryInt(t, tx, "SELECT count(*) FROM api_tokens"); n == 0 {
				t.Error("the fixture has no API tokens to check")
			}
			if n := queryInt(t, tx, "SELECT count(*) FROM api_tokens WHERE parent_token_id IS NOT NULL"); n != 0 {
				t.Errorf("%d tokens have a parent after the migration, want none", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	},
}

func TestMigrationHarness(t *testing.T) {
	known, err := EmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, fx := range loadFixtures(t, filepath.Join("testdata", "fixtures")) {
		t.Run(fx.name, func(t *testing.T) {
			db := upgradeFixture(t, fx, known)
			if check := fixtureChecks[fx.name]; check != nil {
				check(t, db)
			}
		})
	}
}

// TestFixtureCoverage requires a fixture for every schema version a released
// database can be at, below the latest one.
func TestFixtureCoverage(t *testing.T) {
	known, err := EmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	have := map[int]bool{}
	for _, fx := range loadFixtures(t, filepath.Join("testdata", "fixtures")) {
		if fx.version >= len(known) {
			t.Errorf("fixture %s is at version %d; fixtures hold prior versions only (latest is %d)", fx.name, fx.version, len(known))
		}
		have[fx.version] = true
	}
	for v := 0; v < len(known); v++ {
		if !have[v] {
			t.Errorf("no fixture for schema version %04d in testdata/fixtures; add a representative one before migration %04d", v, v+1)
		}
	}
}

// TestMigrationHarnessExample drives the harness through a synthetic
// three-step schema with data, including a table rebuild with foreign keys
// off, so the harness is exercised beyond the current one-migration schema.
func TestMigrationHarnessExample(t *testing.T) {
	dir := filepath.Join("testdata", "harness-example")
	known := dirMigrations(t, filepath.Join(dir, "migrations"))
	ctx := context.Background()
	checks := map[string]func(t *testing.T, db *DB){
		"0002_accounts_with_entries.sql": func(t *testing.T, db *DB) {
			err := db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
				if n := queryInt(t, tx, "SELECT count(*) FROM entries"); n != 3 {
					t.Errorf("entries = %d after the accounts rebuild, want 3 (a cascade deleted them?)", n)
				}
				if n := queryInt(t, tx, "SELECT count(*) FROM accounts WHERE currency = 'EUR'"); n != 2 {
					t.Errorf("accounts with the default currency = %d, want 2", n)
				}
				var created string
				if err := tx.QueryRowContext(ctx, "SELECT created_at FROM accounts WHERE id = 2").Scan(&created); err != nil {
					return err
				}
				if created != "2026-02-03T04:05:06.123456789Z" {
					t.Errorf("created_at = %s, want it carried over exactly", created)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		},
	}
	fixtures := loadFixtures(t, filepath.Join(dir, "fixtures"))
	if len(fixtures) < 2 {
		t.Fatalf("harness example has %d fixtures, want at least 2", len(fixtures))
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			db := upgradeFixture(t, fx, known)
			if check := checks[fx.name]; check != nil {
				check(t, db)
			}
		})
	}
}
