package store

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Errors the migration runner returns. Each is wrapped with detail; match
// with errors.Is.
var (
	// ErrDatabaseNewer means the database records migrations this binary does
	// not have. Migrations never run backwards, so the database is refused.
	ErrDatabaseNewer = errors.New("store: database schema is newer than this binary")
	// ErrOutOfOrder means the applied migrations are not a contiguous run
	// from 0001, for example because a lower-numbered migration was added
	// after a higher one had been applied.
	ErrOutOfOrder = errors.New("store: applied migrations are out of order")
	// ErrChecksumMismatch means an applied migration's file has changed since
	// it was applied. Applied migrations are immutable; squashing them needs
	// an ADR.
	ErrChecksumMismatch = errors.New("store: applied migration has changed")
	// ErrUnversioned means the file holds tables but no migration ledger, so
	// it is not a Ledger database (or predates versioning) and is not touched.
	ErrUnversioned = errors.New("store: database has tables but no schema_migrations")
	// ErrInvalidMigrations means the migration set itself is malformed.
	ErrInvalidMigrations = errors.New("store: invalid migration set")
	// ErrForeignKeyViolation means a migration left rows that violate a
	// foreign key; the migration was rolled back.
	ErrForeignKeyViolation = errors.New("store: migration left foreign key violations")
)

// foreignKeysOffDirective, as the first line of a migration, runs it with
// foreign key enforcement off. See the package documentation.
const foreignKeysOffDirective = "-- ledger:foreign-keys-off"

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

// Migration is one numbered, forward-only schema change.
type Migration struct {
	Version  int    // 1, 2, 3, ... from the file name
	Name     string // the snake_case part of the file name
	SQL      string // the file content, executed as one script
	Checksum string // hex SHA-256 of the file content
	// ForeignKeysOff is set by the foreign-keys-off directive.
	ForeignKeysOff bool
}

// EmbeddedMigrations returns the migrations compiled into the binary.
func EmbeddedMigrations() ([]Migration, error) {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		return nil, err
	}
	return LoadMigrations(sub)
}

// LoadMigrations reads every file in the root of fsys as a migration. Every
// file must be named NNNN_snake_case_name.sql, and the versions must run
// from 0001 without gaps or duplicates.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidMigrations, err)
	}
	var out []Migration
	var problems []error
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			problems = append(problems, fmt.Errorf("%q is not named NNNN_snake_case_name.sql", e.Name()))
			continue
		}
		version, _ := strconv.Atoi(m[1]) // four digits, cannot fail
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if strings.TrimSpace(string(body)) == "" {
			problems = append(problems, fmt.Errorf("%q is empty", e.Name()))
			continue
		}
		// Hash with LF line endings so a checkout that converts them does
		// not change the checksum of an applied migration.
		sum := sha256.Sum256(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")))
		out = append(out, Migration{
			Version:        version,
			Name:           m[2],
			SQL:            string(body),
			Checksum:       hex.EncodeToString(sum[:]),
			ForeignKeysOff: firstLine(string(body)) == foreignKeysOffDirective,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			problems = append(problems, fmt.Errorf("expected version %04d, found %04d_%s (versions must run from 0001 without gaps or duplicates)", i+1, m.Version, m.Name))
			break
		}
	}
	if len(out) == 0 && len(problems) == 0 {
		problems = append(problems, errors.New("no migrations"))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalidMigrations, errors.Join(problems...))
	}
	return out, nil
}

func firstLine(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}

// appliedMigration is one row of schema_migrations.
type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readApplied returns the applied migrations in version order. A database
// with no schema_migrations table and no other tables is at version 0.
func readApplied(ctx context.Context, q querier) ([]appliedMigration, error) {
	var hasLedger, otherTables int
	err := q.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE name = 'schema_migrations'),
		count(*) FILTER (WHERE name <> 'schema_migrations')
		FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&hasLedger, &otherTables)
	if err != nil {
		return nil, fmt.Errorf("store: read schema: %w", err)
	}
	if hasLedger == 0 {
		if otherTables > 0 {
			return nil, ErrUnversioned
		}
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []appliedMigration
	for rows.Next() {
		var a appliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum); err != nil {
			return nil, fmt.Errorf("store: read schema_migrations: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	return out, nil
}

// checkApplied verifies that applied is a prefix of known: contiguous from 1,
// no version beyond the binary's, and every file unchanged.
func checkApplied(applied []appliedMigration, known []Migration) error {
	if n := len(applied); n > 0 && applied[n-1].Version > len(known) {
		return fmt.Errorf("%w: database is at version %04d, this binary knows up to %04d; use a newer binary or restore a backup",
			ErrDatabaseNewer, applied[n-1].Version, len(known))
	}
	for i, a := range applied {
		if a.Version != i+1 {
			return fmt.Errorf("%w: expected version %04d to be applied next, found %04d", ErrOutOfOrder, i+1, a.Version)
		}
		k := known[i]
		if a.Name != k.Name || a.Checksum != k.Checksum {
			return fmt.Errorf("%w: %04d_%s was applied as %04d_%s with checksum %.12s, the binary has checksum %.12s",
				ErrChecksumMismatch, k.Version, k.Name, a.Version, a.Name, a.Checksum, k.Checksum)
		}
	}
	return nil
}

// MigrationResult reports what [migrate] did.
type MigrationResult struct {
	From, To int   // schema versions before and after
	Applied  []int // versions applied in this run, in order
}

// migrate brings the database behind conn up to the last of known. conn must
// be a dedicated connection opened with BEGIN IMMEDIATE transactions; it may be
// left with foreign keys on or off, so the caller closes it afterwards.
func migrate(ctx context.Context, conn *sql.Conn, known []Migration, now func() time.Time) (MigrationResult, error) {
	applied, err := readApplied(ctx, conn)
	if err != nil {
		return MigrationResult{}, err
	}
	if err := checkApplied(applied, known); err != nil {
		return MigrationResult{}, err
	}
	res := MigrationResult{From: len(applied), To: len(applied)}
	for res.To < len(known) {
		next := known[res.To]
		done, err := applyOne(ctx, conn, known, next, now)
		if err != nil {
			return res, err
		}
		if done {
			res.Applied = append(res.Applied, next.Version)
		}
		// Re-read: another process may have migrated concurrently.
		applied, err := readApplied(ctx, conn)
		if err != nil {
			return res, err
		}
		if err := checkApplied(applied, known); err != nil {
			return res, err
		}
		res.To = len(applied)
	}
	return res, nil
}

// applyOne applies m in one transaction, together with its schema_migrations
// row. It reports false, without error, when another process applied m first.
func applyOne(ctx context.Context, conn *sql.Conn, known []Migration, m Migration, now func() time.Time) (applied bool, err error) {
	if m.ForeignKeysOff {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return false, fmt.Errorf("store: migration %04d_%s: disable foreign keys: %w", m.Version, m.Name, err)
		}
		defer func() {
			if _, rerr := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON"); rerr != nil && err == nil {
				err = fmt.Errorf("store: migration %04d_%s: re-enable foreign keys: %w", m.Version, m.Name, rerr)
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: migration %04d_%s: begin: %w", m.Version, m.Name, err)
	}
	defer func() {
		if err != nil || !applied {
			_ = tx.Rollback()
		}
	}()

	// Under the write lock, confirm that m is still the next migration.
	current, err := readApplied(ctx, tx)
	if err != nil {
		return false, err
	}
	if err := checkApplied(current, known); err != nil {
		return false, err
	}
	if len(current) != m.Version-1 {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return false, fmt.Errorf("store: migration %04d_%s failed and was rolled back: %w", m.Version, m.Name, err)
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return false, fmt.Errorf("store: migration %04d_%s was rolled back: %w", m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.Version, m.Name, m.Checksum, Timestamp{now()}); err != nil {
		return false, fmt.Errorf("store: migration %04d_%s: record version (migration 0001 must create schema_migrations): %w", m.Version, m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: migration %04d_%s: commit: %w", m.Version, m.Name, err)
	}
	return true, nil
}

// foreignKeyCheck returns ErrForeignKeyViolation, with the first few
// offending tables and rowids, when PRAGMA foreign_key_check reports rows.
func foreignKeyCheck(ctx context.Context, q querier) error {
	rows, err := q.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("store: foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var found []string
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("store: foreign_key_check: %w", err)
		}
		if len(found) < 5 {
			found = append(found, fmt.Sprintf("%s rowid %d -> %s", table, rowid.Int64, parent))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: foreign_key_check: %w", err)
	}
	if len(found) > 0 {
		return fmt.Errorf("%w: %s", ErrForeignKeyViolation, strings.Join(found, "; "))
	}
	return nil
}
