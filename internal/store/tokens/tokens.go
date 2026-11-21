// Package tokens persists API tokens (task A4, ADR-0029) in the api_tokens
// table of migration 0012_api_tokens. It implements [tokens.Store]; writes
// record their audit events in the same transaction through a
// [store.AuditWriter]. The table never holds a token, only its hash.
package tokens

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// Store implements [tokens.Store] on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
}

var _ tokens.Store = (*Store)(nil)

// New returns a Store; aw is required.
func New(db *store.DB, aw store.AuditWriter) *Store {
	if aw == nil {
		panic("tokens.New: an audit writer is required")
	}
	return &Store{db: db, audit: aw}
}

const columns = "id, owner_id, name, token_hash, display_prefix, last_chars, root, grants, created_at, expires_at, last_used_at, revoked_at"

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return store.Timestamp{Time: *t}
}

func parseNull(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := store.ParseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// parseGrants reads the stored grants. A grant that is no longer in the
// vocabulary is dropped, never widened: names are never reused (ADR-0021),
// so this can only narrow a token.
func parseGrants(s string) authz.Grants {
	var out authz.Grants
	for _, part := range strings.Split(s, ",") {
		if g, err := authz.ParseGrant(part); err == nil {
			out = append(out, g)
		}
	}
	gs, _ := authz.NewGrants(out...)
	return gs
}

func scan(row interface{ Scan(...any) error }) (tokens.Token, error) {
	var t tokens.Token
	var grants string
	var created store.Timestamp
	var expires, used, revoked sql.NullString
	if err := row.Scan(&t.ID, &t.OwnerID, &t.Name, &t.TokenHash, &t.DisplayPrefix, &t.LastChars, &t.Root, &grants,
		&created, &expires, &used, &revoked); err != nil {
		return tokens.Token{}, err
	}
	t.CreatedAt = created.Time
	t.Grants = parseGrants(grants)
	var err error
	if t.ExpiresAt, err = parseNull(expires); err != nil {
		return tokens.Token{}, err
	}
	if t.LastUsedAt, err = parseNull(used); err != nil {
		return tokens.Token{}, err
	}
	if t.RevokedAt, err = parseNull(revoked); err != nil {
		return tokens.Token{}, err
	}
	return t, nil
}

func (s *Store) writeEvents(ctx context.Context, tx *sql.Tx, events []audit.Event) error {
	for _, e := range events {
		if err := s.audit.WriteAudit(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

// Create inserts a token and its events.
func (s *Store) Create(ctx context.Context, t tokens.Token, events ...audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO api_tokens ("+columns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			t.ID, t.OwnerID, t.Name, t.TokenHash, t.DisplayPrefix, t.LastChars, t.Root, strings.Join(t.Grants.Strings(), ","),
			store.Timestamp{Time: t.CreatedAt}, nullTime(t.ExpiresAt), nullTime(t.LastUsedAt), nullTime(t.RevokedAt)); err != nil {
			return fmt.Errorf("store: create API token: %w", err)
		}
		return s.writeEvents(ctx, tx, events)
	})
}

func (s *Store) one(ctx context.Context, where, arg string) (tokens.Token, error) {
	var t tokens.Token
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		t, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM api_tokens WHERE "+where, arg))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return tokens.Token{}, resource.ErrNotFound
	}
	return t, err
}

// ByHash returns the token with this hash.
func (s *Store) ByHash(ctx context.Context, hash string) (tokens.Token, error) {
	return s.one(ctx, "token_hash = ?", hash)
}

// Get returns the token with this ID.
func (s *Store) Get(ctx context.Context, id string) (tokens.Token, error) {
	return s.one(ctx, "id = ?", id)
}

// List returns up to f.Limit+1 tokens after f.After in ID order.
func (s *Store) List(ctx context.Context, f tokens.ListFilter) ([]tokens.Token, error) {
	where := []string{"id > ?"}
	args := []any{f.After}
	if f.OwnerID != "" {
		where = append(where, "owner_id = ?")
		args = append(args, f.OwnerID)
	}
	args = append(args, f.Limit+1)
	var out []tokens.Token
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM api_tokens WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			t, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// Revoke marks the token revoked and writes the events, in one transaction.
func (s *Store) Revoke(ctx context.Context, id, ownerID string, at time.Time, events ...audit.Event) (bool, error) {
	var revoked bool
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var owner string
		var already bool
		err := tx.QueryRowContext(ctx, "SELECT owner_id, revoked_at IS NOT NULL FROM api_tokens WHERE id = ?", id).Scan(&owner, &already)
		if errors.Is(err, sql.ErrNoRows) || err == nil && ownerID != "" && owner != ownerID {
			return resource.ErrNotFound
		}
		if err != nil || already {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", store.Timestamp{Time: at}, id); err != nil {
			return err
		}
		revoked = true
		return s.writeEvents(ctx, tx, events)
	})
	return revoked, err
}

// Touch records use of the token. The value only moves forward.
func (s *Store) Touch(ctx context.Context, id string, at time.Time) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)", store.Timestamp{Time: at}, id, store.Timestamp{Time: at})
		return err
	})
}
