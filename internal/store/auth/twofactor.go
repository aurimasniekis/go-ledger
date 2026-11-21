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

// Second factors (A3, ADR-0030) in the tables of migration 0012_totp.

// TwoFactor returns the user's second-factor state.
func (s *Store) TwoFactor(ctx context.Context, userID string) (auth.TwoFactorState, error) {
	var st auth.TwoFactorState
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var secret, enabled, pending, pendingAt sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT secret, enabled_at, last_step, pending_secret, pending_at FROM user_totp WHERE user_id = ?", userID).
			Scan(&secret, &enabled, &st.LastStep, &pending, &pendingAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		st.Secret, st.PendingSecret = secret.String, pending.String
		if st.EnabledAt, err = nullTime(enabled); err != nil {
			return err
		}
		if st.PendingAt, err = nullTime(pendingAt); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM user_recovery_codes WHERE user_id = ?", userID).Scan(&st.RecoveryCodes)
	})
	return st, err
}

func nullTime(s sql.NullString) (time.Time, error) {
	if !s.Valid {
		return time.Time{}, nil
	}
	return store.ParseTime(s.String)
}

// SetPendingTOTP records a started enrolment.
func (s *Store) SetPendingTOTP(ctx context.Context, userID, sealed string, at time.Time) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO user_totp (user_id, pending_secret, pending_at) VALUES (?, ?, ?) "+
			"ON CONFLICT (user_id) DO UPDATE SET pending_secret = excluded.pending_secret, pending_at = excluded.pending_at",
			userID, sealed, store.Timestamp{Time: at})
		return err
	})
}

func (s *Store) replaceCodes(ctx context.Context, tx *sql.Tx, userID string, hashes []string, at time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_recovery_codes WHERE user_id = ?", userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)",
			userID, h, store.Timestamp{Time: at}); err != nil {
			return err
		}
	}
	return nil
}

// ConfirmTOTP promotes the pending secret, replaces the recovery codes and
// ends the user's other sessions.
func (s *Store) ConfirmTOTP(ctx context.Context, userID, pending string, lastStep int64, codeHashes []string, keep string, at time.Time, events func(int) []audit.Event) (int, error) {
	var ended int
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE user_totp SET secret = pending_secret, enabled_at = ?, last_step = ?, pending_secret = NULL, pending_at = NULL "+
			"WHERE user_id = ? AND pending_secret = ?", store.Timestamp{Time: at}, lastStep, userID, pending)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return resource.ErrNotFound
		}
		if err := s.replaceCodes(ctx, tx, userID, codeHashes, at); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", userID, keep)
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
		return s.writeEvents(ctx, tx, events(ended))
	})
	return ended, err
}

// UseTOTPStep records step as used if it is later than the last one, in one
// statement, so two requests with the same code cannot both pass.
func (s *Store) UseTOTPStep(ctx context.Context, userID, sealed string, step int64) (bool, error) {
	var ok bool
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE user_totp SET last_step = ? WHERE user_id = ? AND secret = ? AND last_step < ?", step, userID, sealed, step)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		ok = n == 1
		return err
	})
	return ok, err
}

// UseRecoveryCode deletes the code with this hash.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, codeHash string) (int, bool, error) {
	var remaining int
	var ok bool
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM user_recovery_codes WHERE user_id = ? AND code_hash = ?", userID, codeHash)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		ok = n == 1
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM user_recovery_codes WHERE user_id = ?", userID).Scan(&remaining)
	})
	return remaining, ok, err
}

// ReplaceRecoveryCodes replaces the codes of a user with TOTP enabled.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID string, codeHashes []string, at time.Time, events ...audit.Event) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM user_totp WHERE user_id = ? AND secret IS NOT NULL", userID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return resource.ErrNotFound
		}
		if err := s.replaceCodes(ctx, tx, userID, codeHashes, at); err != nil {
			return err
		}
		return s.writeEvents(ctx, tx, events)
	})
}

// DisableTwoFactor deletes the user's TOTP row and recovery codes.
// When it was enabled, it also ends the user's sessions except keep (a
// session id, "" for none), in the same transaction, and returns how many.
func (s *Store) DisableTwoFactor(ctx context.Context, userID, keep string, events func(int) []audit.Event) (was bool, ended int, err error) {
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM user_totp WHERE user_id = ? AND secret IS NOT NULL", userID).Scan(&n); err != nil {
			return err
		}
		was = n == 1
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_totp WHERE user_id = ?", userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM user_recovery_codes WHERE user_id = ?", userID); err != nil {
			return err
		}
		if !was {
			return nil
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
		return s.writeEvents(ctx, tx, events(ended))
	})
	return was, ended, err
}
