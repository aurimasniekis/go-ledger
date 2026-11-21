package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// Single-sign-on links and recovery sign-ins (A6, ADR-0033), in the tables of
// migration 0015_oidc.

const linkColumns = "id, user_id, issuer, subject, email, created_at, last_used_at"

func scanLink(row interface{ Scan(...any) error }) (auth.OIDCLink, error) {
	var l auth.OIDCLink
	var created store.Timestamp
	var used sql.Null[store.Timestamp]
	if err := row.Scan(&l.ID, &l.UserID, &l.Issuer, &l.Subject, &l.Email, &created, &used); err != nil {
		return auth.OIDCLink{}, err
	}
	l.CreatedAt = created.Time
	if used.Valid {
		t := used.V.Time
		l.LastUsedAt = &t
	}
	return l, nil
}

func (s *Store) link(ctx context.Context, where string, args ...any) (auth.OIDCLink, error) {
	var l auth.OIDCLink
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		l, err = scanLink(tx.QueryRowContext(ctx, "SELECT "+linkColumns+" FROM oidc_links WHERE "+where, args...))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return auth.OIDCLink{}, resource.ErrNotFound
	}
	return l, err
}

// OIDCLinkBySubject returns the link of an identity.
func (s *Store) OIDCLinkBySubject(ctx context.Context, issuer, subject string) (auth.OIDCLink, error) {
	return s.link(ctx, "issuer = ? AND subject = ?", issuer, subject)
}

// OIDCLinkByUser returns the link of a user.
func (s *Store) OIDCLinkByUser(ctx context.Context, userID string) (auth.OIDCLink, error) {
	return s.link(ctx, "user_id = ?", userID)
}

// CreateOIDCLink inserts a link unless the user or the identity has one.
func (s *Store) CreateOIDCLink(ctx context.Context, l auth.OIDCLink, events ...audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM oidc_links WHERE user_id = ? OR (issuer = ? AND subject = ?)",
			l.UserID, l.Issuer, l.Subject).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return auth.ErrOIDCAlreadyLinked
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO oidc_links (id, user_id, issuer, subject, email, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			l.ID, l.UserID, l.Issuer, l.Subject, l.Email, store.Timestamp{Time: l.CreatedAt}); err != nil {
			return err
		}
		return s.writeEvents(ctx, tx, events)
	})
}

// DeleteOIDCLink removes the user's link.
func (s *Store) DeleteOIDCLink(ctx context.Context, userID string, events ...audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM oidc_links WHERE user_id = ?", userID)
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

// TouchOIDCLink records a sign-in with the link.
func (s *Store) TouchOIDCLink(ctx context.Context, id string, at time.Time, email string) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE oidc_links SET last_used_at = ?, email = ? WHERE id = ?", store.Timestamp{Time: at}, email, id)
		return err
	})
}

// ReplaceRecoverySignIn stores the user's only recovery sign-in.
func (s *Store) ReplaceRecoverySignIn(ctx context.Context, r auth.RecoverySignIn, events ...audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO recovery_sign_ins (user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT (user_id) DO UPDATE SET token_hash = excluded.token_hash, created_at = excluded.created_at, expires_at = excluded.expires_at",
			r.UserID, r.TokenHash, store.Timestamp{Time: r.CreatedAt}, store.Timestamp{Time: r.ExpiresAt}); err != nil {
			return err
		}
		return s.writeEvents(ctx, tx, events)
	})
}

// TakeRecoverySignIn deletes and returns the recovery sign-in of a token hash.
func (s *Store) TakeRecoverySignIn(ctx context.Context, tokenHash string) (auth.RecoverySignIn, error) {
	var r auth.RecoverySignIn
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var created, expires store.Timestamp
		if err := tx.QueryRowContext(ctx, "DELETE FROM recovery_sign_ins WHERE token_hash = ? RETURNING user_id, token_hash, created_at, expires_at", tokenHash).
			Scan(&r.UserID, &r.TokenHash, &created, &expires); err != nil {
			return err
		}
		r.CreatedAt, r.ExpiresAt = created.Time, expires.Time
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return auth.RecoverySignIn{}, resource.ErrNotFound
	}
	return r, err
}
