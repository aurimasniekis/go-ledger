package store

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func tempPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "ledger.db")
}

func openTest(t *testing.T, path string, opts Options) *DB {
	t.Helper()
	db, err := Open(context.Background(), path, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

// rawDB opens path with foreign keys on and nothing else, bypassing Open, to
// prepare or inspect a database the way an older binary or another process
// would.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// mapMigrations builds a migration set from file name -> SQL.
func mapMigrations(t *testing.T, files map[string]string) []Migration {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	ms, err := LoadMigrations(fsys)
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	return ms
}

func dirMigrations(t *testing.T, dir string) []Migration {
	t.Helper()
	ms, err := LoadMigrations(os.DirFS(dir))
	if err != nil {
		t.Fatalf("LoadMigrations(%s): %v", dir, err)
	}
	return ms
}

func embeddedSQL(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(embedded, "migrations/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func queryInt(t *testing.T, q querier, query string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}
