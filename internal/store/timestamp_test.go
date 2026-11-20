package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestTimestampsAreStoredInUTC(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	mustExec(t, db.writer, "CREATE TABLE events (id INTEGER PRIMARY KEY, happened_at TEXT NOT NULL CHECK ("+TimestampCheck("happened_at")+")) STRICT")

	plus3 := time.FixedZone("UTC+3", 3*60*60)
	local := time.Date(2026, 3, 29, 2, 30, 0, 123456789, plus3)
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO events (id, happened_at) VALUES (1, ?)", Timestamp{local})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	var back Timestamp
	err = db.ReadTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT happened_at FROM events WHERE id = 1").Scan(&stored); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT happened_at FROM events WHERE id = 1").Scan(&back)
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored != "2026-03-28T23:30:00.123456789Z" {
		t.Fatalf("stored %q, want the UTC instant", stored)
	}
	if !back.Equal(local) || back.Location() != time.UTC {
		t.Fatalf("read back %v (%v), want %v in UTC", back.Time, back.Location(), local)
	}

	// Anything else is refused by the column CHECK, including a bare
	// time.Time, which the driver would encode with its own offset layout.
	for name, v := range map[string]any{
		"bare time.Time":     local,
		"offset":             "2026-03-29T02:30:00.123456789+03:00",
		"no fraction":        "2026-03-28T23:30:00Z",
		"space separator":    "2026-03-28 23:30:00.123456789Z",
		"unix seconds":       int64(1774740600),
		"utc bare time.Time": local.UTC(),
	} {
		err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO events (happened_at) VALUES (?)", v)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("%s: err = %v, want a CHECK failure", name, err)
		}
	}
}

func TestParseTimeIsStrict(t *testing.T) {
	if _, err := ParseTime("2026-03-28T23:30:00.123456789Z"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"2026-03-28T23:30:00Z", "2026-03-28T23:30:00.123456789+00:00", "2026-03-28 23:30:00.123456789Z", ""} {
		if _, err := ParseTime(s); err == nil {
			t.Errorf("ParseTime(%q) succeeded", s)
		}
	}
	var ts Timestamp
	if err := ts.Scan(int64(5)); err == nil {
		t.Error("Scan(int64) succeeded")
	}
}

// TestSchemaConventions checks the migrated schema: every table is STRICT,
// and every *_at column is TEXT guarded by TimestampCheck.
func TestSchemaConventions(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	err := db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT name, strict FROM pragma_table_list WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%'")
		if err != nil {
			return err
		}
		tables := map[string]bool{}
		for rows.Next() {
			var name string
			var strict bool
			if err := rows.Scan(&name, &strict); err != nil {
				return err
			}
			tables[name] = strict
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(tables) == 0 {
			t.Fatal("no tables after migration")
		}
		for table, strict := range tables {
			if !strict {
				t.Errorf("table %s is not STRICT", table)
			}
			var ddl string
			if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&ddl); err != nil {
				return err
			}
			cols, err := tx.QueryContext(ctx, "SELECT name, type FROM pragma_table_info(?)", table)
			if err != nil {
				return err
			}
			for cols.Next() {
				var col, typ string
				if err := cols.Scan(&col, &typ); err != nil {
					return err
				}
				if !strings.HasSuffix(col, "_at") {
					continue
				}
				if typ != "TEXT" {
					t.Errorf("%s.%s is %s, timestamps are TEXT", table, col, typ)
				}
				if !strings.Contains(ddl, TimestampCheck(col)) {
					t.Errorf("%s.%s lacks CHECK (%s)", table, col, TimestampCheck(col))
				}
			}
			if err := cols.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
