package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Optimistic concurrency (ADR-0011, decision D17). Every mutable configurable
// table has
//
//	id      TEXT PRIMARY KEY CHECK (<IDCheck("id")>)
//	version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1)
//
// and is changed only through [UpdateVersioned] and [DeleteVersioned], which
// apply the change only when the row still has the version the caller read.
// TestSchemaConventions enforces the columns; tables that are not mutable
// configuration (append-only facts, internal bookkeeping) are listed there
// with the reason.

// VersionCheck is the CHECK expression of a version column.
const VersionCheck = "version >= 1"

// IDCheck returns the CHECK expression of a ULID identifier column: 26
// canonical Crockford base32 characters (see resource.ValidID).
func IDCheck(column string) string {
	return fmt.Sprintf("length(%[1]s) = 26 AND %[1]s GLOB '[0-7]%[2]s'", column, strings.Repeat("[0-9A-HJKMNP-TV-Z]", 25))
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// UpdateVersioned runs "UPDATE table SET set, version = version + 1 WHERE id
// = ? AND version = ?" with args (for the placeholders in set), then id and
// expected. It returns the new version. When no row changed it tells why:
// [resource.ErrNotFound] when the row does not exist, a
// [*resource.VersionConflictError] when its version differs. table and set
// are SQL written in code, never input: table must be a plain identifier.
func UpdateVersioned(ctx context.Context, tx *sql.Tx, table, set string, id string, expected int64, args ...any) (int64, error) {
	if !identifier.MatchString(table) || set == "" {
		return 0, fmt.Errorf("store: UpdateVersioned: invalid table %q or empty SET", table)
	}
	q := "UPDATE " + table + " SET " + set + ", version = version + 1 WHERE id = ? AND version = ?"
	res, err := tx.ExecContext(ctx, q, slices.Concat(args, []any{id, expected})...)
	if err != nil {
		return 0, err
	}
	if err := checkAffected(ctx, tx, res, table, id, expected); err != nil {
		return 0, err
	}
	return expected + 1, nil
}

// DeleteVersioned deletes the row with id when it still has version
// expected, with the same errors as [UpdateVersioned].
func DeleteVersioned(ctx context.Context, tx *sql.Tx, table, id string, expected int64) error {
	if !identifier.MatchString(table) {
		return fmt.Errorf("store: DeleteVersioned: invalid table %q", table)
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE id = ? AND version = ?", id, expected)
	if err != nil {
		return err
	}
	return checkAffected(ctx, tx, res, table, id, expected)
}

func checkAffected(ctx context.Context, tx *sql.Tx, res sql.Result, table, id string, expected int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	switch n {
	case 1:
		return nil
	case 0:
	default:
		return fmt.Errorf("store: %s: %d rows changed for one id", table, n)
	}
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT version FROM "+table+" WHERE id = ?", id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return resource.ErrNotFound
	}
	if err != nil {
		return err
	}
	return &resource.VersionConflictError{Expected: expected, Current: current}
}
