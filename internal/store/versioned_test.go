package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

func versionedDB(t *testing.T) *DB {
	t.Helper()
	db := openTest(t, tempPath(t), Options{})
	mustExec(t, db.writer, "CREATE TABLE things (id TEXT PRIMARY KEY CHECK ("+IDCheck("id")+"), name TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1 CHECK ("+VersionCheck+")) STRICT")
	return db
}

func TestUpdateVersioned(t *testing.T) {
	ctx := context.Background()
	db := versionedDB(t)
	id := resource.NewID()
	mustExec(t, db.writer, "INSERT INTO things (id, name) VALUES (?, 'a')", id)

	update := func(name string, expected int64) (int64, error) {
		var v int64
		err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			v, err = UpdateVersioned(ctx, tx, "things", "name = ?", id, expected, name)
			return err
		})
		return v, err
	}
	if v, err := update("b", 1); err != nil || v != 2 {
		t.Fatalf("first update: %d %v", v, err)
	}
	// A second writer that also read version 1 loses.
	_, err := update("c", 1)
	var vc *resource.VersionConflictError
	if !errors.As(err, &vc) || vc.Current != 2 || vc.Expected != 1 {
		t.Fatalf("stale update: %v", err)
	}
	if got := queryInt(t, db.writer, "SELECT version FROM things WHERE id = ?", id); got != 2 {
		t.Fatalf("version after conflict = %d", got)
	}
	var name string
	if err := db.writer.QueryRow("SELECT name FROM things WHERE id = ?", id).Scan(&name); err != nil || name != "b" {
		t.Fatalf("the losing write changed the row: %q %v", name, err)
	}
	err = db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := UpdateVersioned(ctx, tx, "things", "name = ?", resource.NewID(), 1, "x")
		return err
	})
	if !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing row: %v", err)
	}
	for _, table := range []string{"things; DROP TABLE things", "Things", ""} {
		err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := UpdateVersioned(ctx, tx, table, "name = 'x'", id, 2)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "invalid table") {
			t.Errorf("table %q accepted: %v", table, err)
		}
	}
}

// UpdateVersioned must not write id and expected into spare capacity of the
// caller's args slice.
func TestUpdateVersionedDoesNotAliasArgs(t *testing.T) {
	ctx := context.Background()
	db := versionedDB(t)
	id := resource.NewID()
	mustExec(t, db.writer, "INSERT INTO things (id, name) VALUES (?, 'a')", id)
	args := make([]any, 1, 4)
	args[0] = "b"
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := UpdateVersioned(ctx, tx, "things", "name = ?", id, 1, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if spare := args[1:4]; spare[0] != nil || spare[1] != nil || spare[2] != nil {
		t.Fatalf("the caller's args backing array was overwritten: %v", spare)
	}
}

func TestDeleteVersioned(t *testing.T) {
	ctx := context.Background()
	db := versionedDB(t)
	id := resource.NewID()
	mustExec(t, db.writer, "INSERT INTO things (id, name, version) VALUES (?, 'a', 3)", id)
	del := func(expected int64) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			return DeleteVersioned(ctx, tx, "things", id, expected)
		})
	}
	var vc *resource.VersionConflictError
	if err := del(2); !errors.As(err, &vc) || vc.Current != 3 {
		t.Fatalf("stale delete: %v", err)
	}
	if err := del(3); err != nil {
		t.Fatal(err)
	}
	if err := del(3); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestIDCheckMatchesValidID(t *testing.T) {
	db := versionedDB(t)
	good := resource.NewID()
	for _, id := range []string{good, strings.ToLower(good), "8" + good[1:], good[:25] + "U", good[:25], good + "0"} {
		_, err := db.writer.Exec("INSERT INTO things (id, name) VALUES (?, 'x')", id)
		if (err == nil) != resource.ValidID(id) {
			t.Errorf("id %q: insert error %v, ValidID %v", id, err, resource.ValidID(id))
		}
	}
	if _, err := db.writer.Exec("INSERT INTO things (id, name, version) VALUES (?, 'x', 0)", resource.NewID()); err == nil {
		t.Error("version 0 accepted")
	}
}

// unversionedTables are the tables that are not mutable configuration and so
// have no version column, each with the reason. A new table either follows the
// versioned convention or is added here with its reason.
var unversionedTables = map[string]string{
	"schema_migrations":              "the migration runner's own ledger",
	"settings":                       "instance bookkeeping written by the server (reporting zone), not edited through the API",
	"bank_accounts":                  "provider-owned bank facts (ADR-0013), written only by the import path and never edited through the API",
	"bank_balance_snapshots":         "provider-owned bank facts (ADR-0013), append-only, never edited through the API",
	"bank_transactions":              "provider-owned bank facts (ADR-0013), written only by the import path and never edited through the API",
	"outbox":                         "append-only event queue owned by the server (ADR-0012); rows are never edited through the API",
	"fx_rates":                       "provider-reported currency rates (ADR-0022), written only by the rate service and never edited through the API",
	"bank_refresh_runs":              "refresh history written only by the import engine (ADR-0020); finished runs are immutable, never edited through the API",
	"audit_log":                      "append-only audit log (ADR-0025); triggers refuse any update or delete, so entries are never edited through the API",
	"bank_account_sync":              "per-account refresh bookkeeping written only by the import engine (ADR-0020), never edited through the API",
	"commitment_occurrence_horizons": "materialisation bookkeeping written only by the occurrence generator (ADR-0027), never edited through the API",
}

// versionProblems checks the optimistic-concurrency convention: every table
// not in exempt has an id TEXT PRIMARY KEY with IDCheck and a version INTEGER
// NOT NULL DEFAULT 1 with VersionCheck.
func versionProblems(ctx context.Context, tx *sql.Tx, exempt map[string]string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_list WHERE schema = 'main' AND type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		tables = append(tables, n)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sort.Strings(tables)
	var problems []string
	for _, table := range tables {
		if _, ok := exempt[table]; ok {
			continue
		}
		var ddl string
		if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&ddl); err != nil {
			return nil, err
		}
		type col struct {
			typ     string
			notNull bool
			dflt    sql.NullString
			pk      int
		}
		cols := map[string]col{}
		cr, err := tx.QueryContext(ctx, "SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", table)
		if err != nil {
			return nil, err
		}
		for cr.Next() {
			var name string
			var c col
			if err := cr.Scan(&name, &c.typ, &c.notNull, &c.dflt, &c.pk); err != nil {
				return nil, err
			}
			cols[name] = c
		}
		if err := cr.Close(); err != nil {
			return nil, err
		}
		if c, ok := cols["id"]; !ok || c.typ != "TEXT" || c.pk != 1 || !strings.Contains(ddl, IDCheck("id")) {
			problems = append(problems, fmt.Sprintf("%s: needs id TEXT PRIMARY KEY CHECK (IDCheck(\"id\")), or an entry in unversionedTables", table))
		}
		if c, ok := cols["version"]; !ok || c.typ != "INTEGER" || !c.notNull || c.dflt.String != "1" || !strings.Contains(ddl, VersionCheck) {
			problems = append(problems, fmt.Sprintf("%s: needs version INTEGER NOT NULL DEFAULT 1 CHECK (%s), or an entry in unversionedTables", table, VersionCheck))
		}
	}
	return problems, nil
}

func TestMutableTablesAreVersioned(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		problems, err := versionProblems(ctx, tx, unversionedTables)
		for _, p := range problems {
			t.Error(p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVersionConventionCatchesViolations(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	mustExec(t, db.writer, "CREATE TABLE good (id TEXT PRIMARY KEY CHECK ("+IDCheck("id")+"), version INTEGER NOT NULL DEFAULT 1 CHECK ("+VersionCheck+")) STRICT")
	mustExec(t, db.writer, "CREATE TABLE no_version (id TEXT PRIMARY KEY CHECK ("+IDCheck("id")+")) STRICT")
	mustExec(t, db.writer, "CREATE TABLE int_id (id INTEGER PRIMARY KEY, version INTEGER NOT NULL DEFAULT 1 CHECK ("+VersionCheck+")) STRICT")
	mustExec(t, db.writer, "CREATE TABLE nullable_version (id TEXT PRIMARY KEY CHECK ("+IDCheck("id")+"), version INTEGER DEFAULT 1) STRICT")
	err := db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		problems, err := versionProblems(ctx, tx, unversionedTables)
		got := strings.Join(problems, "\n")
		for _, want := range []string{"no_version: needs version", "int_id: needs id", "nullable_version: needs version"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing problem %q in:\n%s", want, got)
			}
		}
		if strings.Contains(got, "good:") {
			t.Errorf("the conforming table was flagged:\n%s", got)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
