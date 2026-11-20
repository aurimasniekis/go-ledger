package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenAppliesSettingsAndMigrates(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	known, err := EmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if db.SchemaVersion() != len(known) {
		t.Fatalf("SchemaVersion = %d, want %d", db.SchemaVersion(), len(known))
	}
	want := map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000", "synchronous": "2"}
	for label, run := range map[string]func(context.Context, func(context.Context, *sql.Tx) error) error{"writer": db.WriteTx, "reader": db.ReadTx} {
		err := run(ctx, func(_ context.Context, tx *sql.Tx) error {
			for name, w := range want {
				var got string
				if err := tx.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
					return err
				}
				if !strings.EqualFold(got, w) {
					t.Errorf("%s: PRAGMA %s = %q, want %q", label, name, got, w)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsNonFilePaths(t *testing.T) {
	for _, p := range []string{"", ":memory:", "file:ledger.db", "file:ledger.db?mode=memory", "a?b.db", "a#b.db", t.TempDir() + string(filepath.Separator)} {
		if _, err := Open(context.Background(), p, Options{}); err == nil {
			t.Errorf("Open(%q) succeeded, want an error", p)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := tempPath(t)
	first := openTest(t, path, Options{})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := openTest(t, path, Options{})
	if second.SchemaVersion() != first.SchemaVersion() {
		t.Fatalf("version changed on reopen: %d -> %d", first.SchemaVersion(), second.SchemaVersion())
	}
	err := second.ReadTx(context.Background(), func(_ context.Context, tx *sql.Tx) error {
		if n := queryInt(t, tx, "SELECT count(*) FROM schema_migrations"); n != second.SchemaVersion() {
			t.Errorf("schema_migrations has %d rows, want %d", n, second.SchemaVersion())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func createParentChild(t *testing.T, db *DB) {
	t.Helper()
	err := db.WriteTx(context.Background(), func(_ context.Context, tx *sql.Tx) error {
		mustExec(t, tx, "CREATE TABLE parent (id INTEGER PRIMARY KEY) STRICT")
		mustExec(t, tx, "CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent (id)) STRICT")
		mustExec(t, tx, "INSERT INTO parent (id) VALUES (1)")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestForeignKeyViolationIsRejected(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	createParentChild(t, db)

	err := db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO child (id, parent_id) VALUES (1, 99)")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("insert of an orphan: err = %v, want a foreign key failure", err)
	}
	// Deleting a referenced parent must fail too.
	mustExec(t, db.writer, "INSERT INTO child (id, parent_id) VALUES (3, 1)")
	err = db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM parent WHERE id = 1")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("delete of a referenced parent: err = %v, want a foreign key failure", err)
	}
	err = db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		if n := queryInt(t, tx, "SELECT count(*) FROM child WHERE parent_id = 99"); n != 0 {
			t.Errorf("orphan child rows = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadersAreQueryOnly(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	createParentChild(t, db)
	err := db.ReadTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO parent (id) VALUES (2)")
		return err
	})
	if err == nil {
		t.Fatal("a write through ReadTx succeeded")
	}
}

func TestWriteTxRollsBackOnErrorAndPanic(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	createParentChild(t, db)
	boom := errors.New("boom")

	err := db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		mustExec(t, tx, "INSERT INTO parent (id) VALUES (2)")
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic was swallowed")
			}
		}()
		_ = db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
			mustExec(t, tx, "INSERT INTO parent (id) VALUES (3)")
			panic("boom")
		})
	}()

	// The single writer connection must be usable again, and neither row kept.
	err = db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		if n := queryInt(t, tx, "SELECT count(*) FROM parent WHERE id IN (2, 3)"); n != 0 {
			t.Errorf("rolled-back rows present: %d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestWritesAreSerialised runs concurrent read-modify-write transactions. The
// single writer connection with BEGIN IMMEDIATE must neither lose an update
// nor surface SQLITE_BUSY.
func TestWritesAreSerialised(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	mustExec(t, db.writer, "CREATE TABLE counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL) STRICT")
	mustExec(t, db.writer, "INSERT INTO counter (id, n) VALUES (1, 0)")

	const workers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for range workers {
		wg.Go(func() {
			for range each {
				errs <- db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
					var n int
					if err := tx.QueryRowContext(ctx, "SELECT n FROM counter WHERE id = 1").Scan(&n); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, "UPDATE counter SET n = ? WHERE id = 1", n+1)
					return err
				})
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := queryInt(t, db.readers, "SELECT n FROM counter WHERE id = 1"); n != workers*each {
		t.Fatalf("counter = %d, want %d", n, workers*each)
	}
}

// TestReadersSeeSnapshotDuringWrite shows WAL at work: a reader is not blocked
// by an open write transaction and does not see its uncommitted rows.
func TestReadersSeeSnapshotDuringWrite(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, tempPath(t), Options{})
	createParentChild(t, db)

	inWrite, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO parent (id) VALUES (2)"); err != nil {
				return err
			}
			close(inWrite)
			<-release
			return nil
		})
	}()
	<-inWrite
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := db.ReadTx(rctx, func(_ context.Context, tx *sql.Tx) error {
		if n := queryInt(t, tx, "SELECT count(*) FROM parent"); n != 1 {
			t.Errorf("reader sees %d parents during the write, want 1", n)
		}
		return nil
	})
	close(release)
	if err != nil {
		t.Fatalf("read during write: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, db.readers, "SELECT count(*) FROM parent"); n != 2 {
		t.Fatalf("after commit the reader sees %d parents, want 2", n)
	}
}

// TestBusyTimeoutAgainstAnotherProcess holds the write lock from a separate
// connection (standing in for another process). A writer waits up to the busy
// timeout and then fails; it succeeds once the lock is released in time.
func TestBusyTimeoutAgainstAnotherProcess(t *testing.T) {
	ctx := context.Background()
	path := tempPath(t)
	db := openTest(t, path, Options{BusyTimeout: 200 * time.Millisecond})
	createParentChild(t, db)

	other := rawDB(t, path)
	conn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	mustExec(t, conn, "BEGIN IMMEDIATE")

	start := time.Now()
	err = db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error { return nil })
	if err == nil {
		t.Fatal("WriteTx succeeded while another connection held the write lock")
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("gave up after %v, before the busy timeout", waited)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "busy") && !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Errorf("err = %v, want a busy/locked error", err)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = conn.ExecContext(ctx, "COMMIT")
	}()
	if err := db.WriteTx(ctx, func(_ context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO parent (id) VALUES (2)")
		return err
	}); err != nil {
		t.Fatalf("WriteTx after the lock was released within the timeout: %v", err)
	}
}

// TestVacuumIntoMakesUsableCopy verifies the online-copy path recorded in
// ADR-0003: VACUUM INTO writes a consistent copy that opens at the same
// schema version without migrating.
func TestVacuumIntoMakesUsableCopy(t *testing.T) {
	db := openTest(t, tempPath(t), Options{})
	createParentChild(t, db)
	dest := filepath.Join(t.TempDir(), "copy.db")
	mustExec(t, db.writer, "VACUUM INTO ?", dest)
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}
	copyDB := openTest(t, dest, Options{})
	if copyDB.SchemaVersion() != db.SchemaVersion() {
		t.Fatalf("copy at version %d, original at %d", copyDB.SchemaVersion(), db.SchemaVersion())
	}
	if n := queryInt(t, copyDB.readers, "SELECT count(*) FROM parent"); n != 1 {
		t.Fatalf("copy has %d parents, want 1", n)
	}
}

func TestNestedWriteTxIsRejected(t *testing.T) {
	db := openTest(t, tempPath(t), Options{})
	// Background would wait forever on the single writer connection; the
	// guard must answer at once. The timeout only turns a regression into a
	// failure instead of a hung test.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var inner error
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		derived, cancelDerived := context.WithCancel(ctx)
		defer cancelDerived()
		inner = db.WriteTx(derived, func(context.Context, *sql.Tx) error { return nil })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(inner, ErrNestedWriteTx) {
		t.Fatalf("nested WriteTx = %v, want ErrNestedWriteTx", inner)
	}
	// The outer transaction is unaffected and the writer is free afterwards.
	if err := db.WriteTx(ctx, func(context.Context, *sql.Tx) error { return nil }); err != nil {
		t.Fatalf("WriteTx after the rejected nested call: %v", err)
	}
}
