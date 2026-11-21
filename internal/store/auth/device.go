package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// Device cookies (decision D23-1b) live in device_cookies (migration
// 0016_device_cookies).

// DeviceCookieByTokenHash returns the device cookie with this token hash.
func (s *Store) DeviceCookieByTokenHash(ctx context.Context, tokenHash string) (auth.DeviceCookie, error) {
	var d auth.DeviceCookie
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var created, expires store.Timestamp
		if err := tx.QueryRowContext(ctx, "SELECT id, user_id, token_hash, created_at, expires_at FROM device_cookies WHERE token_hash = ?", tokenHash).
			Scan(&d.ID, &d.UserID, &d.TokenHash, &created, &expires); err != nil {
			return err
		}
		d.CreatedAt, d.ExpiresAt = created.Time, expires.Time
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return auth.DeviceCookie{}, resource.ErrNotFound
	}
	return d, err
}

// IssueDeviceCookie inserts d and deletes the cookie it replaces, the user's
// expired cookies and the user's oldest cookies beyond keep.
func (s *Store) IssueDeviceCookie(ctx context.Context, d auth.DeviceCookie, replace string, keep int) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if replace != "" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM device_cookies WHERE id = ? AND user_id = ?", replace, d.UserID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM device_cookies WHERE user_id = ? AND expires_at <= ?", d.UserID, store.Timestamp{Time: d.CreatedAt}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO device_cookies (id, user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
			d.ID, d.UserID, d.TokenHash, store.Timestamp{Time: d.CreatedAt}, store.Timestamp{Time: d.ExpiresAt}); err != nil {
			return fmt.Errorf("store: issue device cookie: %w", err)
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM device_cookies WHERE user_id = ? AND id NOT IN (SELECT id FROM device_cookies WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)",
			d.UserID, d.UserID, max(keep, 1))
		return err
	})
}
