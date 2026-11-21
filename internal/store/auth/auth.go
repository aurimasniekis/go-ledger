// Package auth persists passwords and sign-in sessions (task A2, ADR-0026)
// in the tables of migration 0010_auth_sessions, second factors (ADR-0030)
// single-sign-on links and recovery sign-ins (ADR-0033) and device cookies
// (decision D23-1b). It implements [auth.Store];
// writes record their audit events in the same transaction through a
// [store.AuditWriter].
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// Store implements [auth.Store] on a [store.DB].
type Store struct {
	db    *store.DB
	audit store.AuditWriter
}

var _ auth.Store = (*Store)(nil)

// New returns a Store; aw is required.
func New(db *store.DB, aw store.AuditWriter) *Store {
	if aw == nil {
		panic("auth.New: an audit writer is required")
	}
	return &Store{db: db, audit: aw}
}

func (s *Store) writeEvents(ctx context.Context, tx *sql.Tx, events []audit.Event) error {
	for _, e := range events {
		if err := s.audit.WriteAudit(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

const credentialQuery = "SELECT u.id, u.username, u.disabled, coalesce(p.hash, '') FROM users u LEFT JOIN user_passwords p ON p.user_id = u.id WHERE "

func (s *Store) credential(ctx context.Context, where string, arg string) (auth.Credential, error) {
	var c auth.Credential
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, credentialQuery+where, arg).Scan(&c.UserID, &c.Username, &c.Disabled, &c.Hash)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Credential{}, resource.ErrNotFound
	}
	return c, err
}

// CredentialByUsername returns the account with this username.
func (s *Store) CredentialByUsername(ctx context.Context, username string) (auth.Credential, error) {
	return s.credential(ctx, "u.username = ?", username)
}

// CredentialByUser returns the account with this ID.
func (s *Store) CredentialByUser(ctx context.Context, userID string) (auth.Credential, error) {
	return s.credential(ctx, "u.id = ?", userID)
}

// ReplaceHash swaps the hash when it is still old.
func (s *Store) ReplaceHash(ctx context.Context, userID, old, hash string) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE user_passwords SET hash = ? WHERE user_id = ? AND hash = ?", hash, userID, old)
		return err
	})
}

// SetPassword stores the hash, ends the user's other sessions and deletes
// the user's device cookies. events receives both counts.
func (s *Store) SetPassword(ctx context.Context, userID, hash, keep string, at time.Time, events func(ended, revokedCookies int) []audit.Event) (int, error) {
	var ended int
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id = ?", userID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return resource.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_passwords (user_id, hash, changed_at) VALUES (?, ?, ?) ON CONFLICT (user_id) DO UPDATE SET hash = excluded.hash, changed_at = excluded.changed_at",
			userID, hash, store.Timestamp{Time: at}); err != nil {
			return err
		}
		// A new password revokes every known browser (decision D23-1b).
		cookies, err := tx.ExecContext(ctx, "DELETE FROM device_cookies WHERE user_id = ?", userID)
		if err != nil {
			return err
		}
		revoked, err := cookies.RowsAffected()
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", userID, keep)
		if err != nil {
			return err
		}
		n64, err := res.RowsAffected()
		if err != nil {
			return err
		}
		ended = int(n64)
		if events == nil {
			return nil
		}
		return s.writeEvents(ctx, tx, events(ended, int(revoked)))
	})
	return ended, err
}

const sessionColumns = "id, user_id, token_hash, created_at, last_seen_at, authenticated_at, expires_at, user_agent, address, method"

func scanSession(row interface{ Scan(...any) error }) (auth.Session, error) {
	var x auth.Session
	var created, seen, authed, expires store.Timestamp
	if err := row.Scan(&x.ID, &x.UserID, &x.TokenHash, &created, &seen, &authed, &expires, &x.UserAgent, &x.Address, &x.Method); err != nil {
		return auth.Session{}, err
	}
	x.CreatedAt, x.LastSeenAt, x.AuthenticatedAt, x.ExpiresAt = created.Time, seen.Time, authed.Time, expires.Time
	return x, nil
}

// CreateSession inserts a session and its events.
func (s *Store) CreateSession(ctx context.Context, x auth.Session, events ...audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		method := x.Method
		if method == "" {
			method = auth.SessionMethodPassword
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions ("+sessionColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			x.ID, x.UserID, x.TokenHash, store.Timestamp{Time: x.CreatedAt}, store.Timestamp{Time: x.LastSeenAt},
			store.Timestamp{Time: x.AuthenticatedAt}, store.Timestamp{Time: x.ExpiresAt}, x.UserAgent, x.Address, method); err != nil {
			return fmt.Errorf("store: create session: %w", err)
		}
		return s.writeEvents(ctx, tx, events)
	})
}

// SessionByTokenHash returns the session with this token hash.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (auth.Session, error) {
	var x auth.Session
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		x, err = scanSession(tx.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM sessions WHERE token_hash = ?", tokenHash))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Session{}, resource.ErrNotFound
	}
	return x, err
}

func (s *Store) updateSession(ctx context.Context, set string, id string, at time.Time, events []audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE sessions SET "+set+" WHERE id = ?", store.Timestamp{Time: at}, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return resource.ErrNotFound
		}
		return s.writeEvents(ctx, tx, events)
	})
}

// TouchSession records use of the session. A session deleted meanwhile is
// not an error: the next request will not find it.
func (s *Store) TouchSession(ctx context.Context, id string, at time.Time) error {
	if err := s.updateSession(ctx, "last_seen_at = ?", id, at, nil); err != nil && !errors.Is(err, resource.ErrNotFound) {
		return err
	}
	return nil
}

// MarkAuthenticated records a fresh password check on the session.
func (s *Store) MarkAuthenticated(ctx context.Context, id string, at time.Time, events ...audit.Event) error {
	return s.updateSession(ctx, "authenticated_at = ?", id, at, events)
}

// ListSessions returns the user's sessions, newest first.
func (s *Store) ListSessions(ctx context.Context, userID string) ([]auth.Session, error) {
	var out []auth.Session
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+sessionColumns+" FROM sessions WHERE user_id = ? ORDER BY created_at DESC, id DESC", userID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			x, err := scanSession(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteSession deletes one session of the user.
func (s *Store) DeleteSession(ctx context.Context, userID, id string, events ...audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id = ? AND user_id = ?", id, userID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return resource.ErrNotFound
		}
		return s.writeEvents(ctx, tx, events)
	})
}

// DeleteExpired deletes sessions past either timeout.
func (s *Store) DeleteExpired(ctx context.Context, now, idleBefore time.Time) (int, error) {
	var n int64
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?", store.Timestamp{Time: now}, store.Timestamp{Time: idleBefore})
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}
