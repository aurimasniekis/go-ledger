package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// Registers the pure-Go "sqlite" database/sql driver (ADR-0003).
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const driverName = "sqlite"

// ErrNestedWriteTx is returned by [DB.WriteTx] when it is called with the
// context of a WriteTx function that is still running: the single writer
// connection is held by the outer call, so waiting for it would never end.
var ErrNestedWriteTx = errors.New("store: WriteTx called inside a WriteTx function")

type writeTxKey struct{}

// Defaults for [Options]. Retry limits and timeouts are still an open product
// decision; these values only bound how long a writer waits for a lock that
// another process holds.
const (
	DefaultBusyTimeout = 5 * time.Second
	DefaultMaxReaders  = 4
)

// Options configures [Open]. The zero value uses the defaults.
type Options struct {
	// BusyTimeout is how long a connection waits for a lock held by another
	// connection or process before failing with SQLITE_BUSY.
	BusyTimeout time.Duration
	// MaxReaders is the size of the read-only connection pool.
	MaxReaders int
	// Migrations replaces the embedded migrations. Tests use it; the
	// application leaves it nil.
	Migrations []Migration
	// Now is the clock for schema_migrations.applied_at. Nil means time.Now.
	Now func() time.Time
}

// DB is an open Ledger database: one writer connection and a pool of
// query-only readers on the same file.
type DB struct {
	path    string
	writer  *sql.DB
	readers *sql.DB
	version int
}

// Open opens (creating if needed) the SQLite database at path, applies any
// pending migrations and verifies the connection settings. path is a file
// system path; in-memory databases and URI file names are not accepted,
// because the writer and the readers must share one file.
func Open(ctx context.Context, path string, opts Options) (*DB, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = DefaultBusyTimeout
	}
	if opts.MaxReaders <= 0 {
		opts.MaxReaders = DefaultMaxReaders
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	known := opts.Migrations
	if known == nil {
		var err error
		if known, err = EmbeddedMigrations(); err != nil {
			return nil, err
		}
	}

	if err := createFile(path); err != nil {
		return nil, err
	}
	res, err := runMigrations(ctx, path, opts, known)
	if err != nil {
		return nil, err
	}

	db := &DB{path: path, version: res.To}
	db.writer, err = sql.Open(driverName, writerDSN(path, opts))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	db.writer.SetMaxOpenConns(1)
	db.writer.SetMaxIdleConns(1)
	db.writer.SetConnMaxLifetime(0)
	db.writer.SetConnMaxIdleTime(0)

	db.readers, err = sql.Open(driverName, readerDSN(path, opts))
	if err != nil {
		_ = db.writer.Close()
		return nil, fmt.Errorf("store: open readers: %w", err)
	}
	db.readers.SetMaxOpenConns(opts.MaxReaders)
	db.readers.SetMaxIdleConns(opts.MaxReaders)

	if err := db.verify(ctx, opts); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// createFile creates an empty database file with mode 0600 when it does not
// exist, so the file is owner-only whatever the umask. SQLite creates the
// -wal and -shm files itself, copying the database file's permissions on
// Unix, so they follow; the directory should still be owner-only. An existing
// file keeps its mode.
func createFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	return f.Close()
}

func checkPath(path string) error {
	switch {
	case path == "":
		return errors.New("store: database path is empty")
	case path == ":memory:" || strings.HasPrefix(path, "file:"):
		return fmt.Errorf("store: database path %q must be a file system path, not an in-memory or URI name", path)
	case strings.ContainsAny(path, "?#"):
		return fmt.Errorf("store: database path %q must not contain '?' or '#'", path)
	}
	if filepath.Base(path) == "." || strings.HasSuffix(path, string(filepath.Separator)) {
		return fmt.Errorf("store: database path %q names a directory", path)
	}
	return nil
}

// commonParams are set on every connection. modernc.org/sqlite applies
// busy_timeout first, then foreign_keys; see ADR-0003.
func commonParams(opts Options) url.Values {
	q := url.Values{}
	q.Set("_busy_timeout", strconv.FormatInt(opts.BusyTimeout.Milliseconds(), 10))
	q.Set("_foreign_keys", "on")
	q.Set("_synchronous", "FULL")
	return q
}

func writerDSN(path string, opts Options) string {
	q := commonParams(opts)
	q.Set("_journal_mode", "WAL")
	q.Set("_txlock", "immediate")
	return path + "?" + q.Encode()
}

func readerDSN(path string, opts Options) string {
	q := commonParams(opts)
	q.Set("_query_only", "on")
	return path + "?" + q.Encode()
}

// runMigrations migrates on a dedicated single connection that is closed
// afterwards, so a migration that switched foreign keys off can never leave
// a pooled connection in that state.
func runMigrations(ctx context.Context, path string, opts Options, known []Migration) (MigrationResult, error) {
	mdb, err := sql.Open(driverName, writerDSN(path, opts))
	if err != nil {
		return MigrationResult{}, fmt.Errorf("store: open for migration: %w", err)
	}
	defer func() { _ = mdb.Close() }()
	mdb.SetMaxOpenConns(1)
	conn, err := connWithBusyRetry(ctx, mdb, opts.BusyTimeout)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("store: open %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	return migrate(ctx, conn, known, opts.Now, func(ctx context.Context, from int) error {
		return backupBeforeMigrate(ctx, conn, path, from, opts.Now())
	})
}

// backupBeforeMigrate writes a consistent copy of the database to a sibling
// file, "<path>.pre-vNNNN-<UTC time>.bak", where NNNN is the schema version
// being left (decision D17). VACUUM cannot run inside a transaction, so it
// runs on the migration connection before the first migration's transaction.
// The copy holds the same financial data as the database, so it gets the same
// owner-only permissions. A failed backup fails Open: the migrations do not run.
func backupBeforeMigrate(ctx context.Context, conn *sql.Conn, path string, from int, at time.Time) error {
	dest := fmt.Sprintf("%s.pre-v%04d-%s.bak", path, from, at.UTC().Format("20060102T150405.000000000Z"))
	if err := vacuumInto(ctx, conn, dest); err != nil {
		return fmt.Errorf("store: pre-migration backup to %s: %w", dest, err)
	}
	return nil
}

// connWithBusyRetry opens a connection, retrying while SQLite reports the
// database busy, for at most timeout. Switching a new file to WAL can fail
// with SQLITE_BUSY without consulting the busy handler when another process
// is creating the same file, so busy_timeout alone does not cover it.
func connWithBusyRetry(ctx context.Context, pool *sql.DB, timeout time.Duration) (*sql.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := pool.Conn(ctx)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return conn, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func isBusy(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	switch serr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	}
	return false
}

// verify fails Open when a connection did not get the settings ADR-0003
// relies on, for example a file system where WAL is unavailable.
func (db *DB) verify(ctx context.Context, opts Options) error {
	type pragma struct{ name, want string }
	common := []pragma{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", strconv.FormatInt(opts.BusyTimeout.Milliseconds(), 10)},
		{"synchronous", "2"},
	}
	check := func(pool *sql.DB, label string, extra ...pragma) error {
		conn, err := connWithBusyRetry(ctx, pool, opts.BusyTimeout)
		if err != nil {
			return fmt.Errorf("store: open %s connection: %w", label, err)
		}
		defer func() { _ = conn.Close() }()
		for _, p := range append(common, extra...) {
			var got string
			if err := conn.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&got); err != nil {
				return fmt.Errorf("store: %s connection: read %s: %w", label, p.name, err)
			}
			if !strings.EqualFold(got, p.want) {
				return fmt.Errorf("store: %s connection: %s is %q, want %q", label, p.name, got, p.want)
			}
		}
		return nil
	}
	if err := check(db.writer, "writer", pragma{"query_only", "0"}); err != nil {
		return err
	}
	return check(db.readers, "reader", pragma{"query_only", "1"})
}

// Close closes both pools.
func (db *DB) Close() error {
	return errors.Join(db.readers.Close(), db.writer.Close())
}

// Path returns the database file path.
func (db *DB) Path() string { return db.path }

// SchemaVersion returns the schema version Open migrated the database to.
func (db *DB) SchemaVersion() int { return db.version }

// Ping checks that both pools can reach the database.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.writer.PingContext(ctx); err != nil {
		return fmt.Errorf("store: writer: %w", err)
	}
	if err := db.readers.PingContext(ctx); err != nil {
		return fmt.Errorf("store: readers: %w", err)
	}
	return nil
}

// WriteTx runs fn in a write transaction on the single writer connection,
// begun with BEGIN IMMEDIATE. It commits when fn returns nil and rolls back
// when fn returns an error or panics (the panic is re-raised). fn must not
// call WriteTx again with the context it receives (that returns
// [ErrNestedWriteTx] instead of deadlocking) and must not keep tx. A [DB.ReadTx]
// started inside fn runs on another connection and does not see fn's
// uncommitted writes.
func (db *DB) WriteTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	if ctx.Value(writeTxKey{}) != nil {
		return ErrNestedWriteTx
	}
	return runTx(context.WithValue(ctx, writeTxKey{}, struct{}{}), db.writer, nil, fn)
}

// ReadTx runs fn in a read-only transaction on a reader connection. Every
// query in fn sees the same snapshot of the database.
func (db *DB) ReadTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	return runTx(ctx, db.readers, &sql.TxOptions{ReadOnly: true}, fn)
}

func runTx(ctx context.Context, pool *sql.DB, txOpts *sql.TxOptions, fn func(ctx context.Context, tx *sql.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, txOpts)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rerr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
