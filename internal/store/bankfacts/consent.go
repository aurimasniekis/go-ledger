package bankfacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// ClaimConsent implements [banking.ConsentStore]. The read, the checks and the
// clearing of the state happen in one write transaction, so of two completions
// of one state exactly one gets the claim.
func (s *Store) ClaimConsent(ctx context.Context, state, initiator string, at time.Time, ttl time.Duration) (banking.ConsentClaim, error) {
	if state == "" {
		return banking.ConsentClaim{}, fmt.Errorf("bankfacts: connection: %w", banking.ErrNotFound)
	}
	if err := requireTime("claim", at); err != nil {
		return banking.ConsentClaim{}, err
	}
	var out banking.ConsentClaim
	timedOut := false
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		c, err := connectionTx(ctx, tx, "consent_state = ?", state)
		if err != nil {
			return err
		}
		var started sql.Null[store.Timestamp]
		var by string
		if err := tx.QueryRowContext(ctx, "SELECT consent_started_at, consent_initiator FROM bank_connections WHERE id = ?", c.ID).Scan(&started, &by); err != nil {
			return fmt.Errorf("bankfacts: read consent origin: %w", err)
		}
		if ttl > 0 && started.Valid && !started.V.Time.Add(ttl).After(at) {
			if err := expireTx(ctx, tx, c, at); err != nil {
				return err
			}
			timedOut = true // committed: the expiry is the outcome
			return nil
		}
		if by != initiator {
			return banking.ErrConsentInitiator
		}
		res, err := tx.ExecContext(ctx, "UPDATE bank_connections SET consent_state = NULL WHERE id = ? AND consent_state = ?", c.ID, state)
		if err != nil {
			return fmt.Errorf("bankfacts: claim consent: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("bankfacts: connection: %w", banking.ErrNotFound)
		}
		out = banking.ConsentClaim{Connection: c, State: state}
		return nil
	})
	if err == nil && timedOut {
		return banking.ConsentClaim{}, banking.ErrConsentTimedOut
	}
	return out, err
}

// expireTx ends the waiting consent of c: a pending connection fails with
// consent_expired, any other keeps its status.
func expireTx(ctx context.Context, tx *sql.Tx, c banking.Connection, at time.Time) error {
	var err error
	if c.Status == banking.ConnectionPending {
		_, err = tx.ExecContext(ctx, "UPDATE bank_connections SET status = 'failed', consent_state = NULL, last_error_kind = 'permanent', last_error_code = ?, updated_at = ?, version = version + 1 WHERE id = ?",
			banking.ConsentExpiredCode, ts(at), c.ID)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE bank_connections SET consent_state = NULL WHERE id = ?", c.ID)
	}
	if err != nil {
		return fmt.Errorf("bankfacts: expire consent: %w", err)
	}
	return nil
}

// ReleaseConsent implements [banking.ConsentStore].
func (s *Store) ReleaseConsent(ctx context.Context, claim banking.ConsentClaim) error {
	return s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// A disconnect after the claim must not be undone; a connection that
		// was already disconnected when claimed (a reconnect) may be.
		_, err := tx.ExecContext(ctx, "UPDATE bank_connections SET consent_state = ? WHERE id = ? AND consent_state IS NULL AND (status <> 'disconnected' OR ? = 'disconnected')", claim.State, claim.Connection.ID, string(claim.Connection.Status))
		if err != nil {
			return fmt.Errorf("bankfacts: release consent: %w", err)
		}
		return nil
	})
}

// ActivateClaimed implements [banking.ConsentStore].
func (s *Store) ActivateClaimed(ctx context.Context, claim banking.ConsentClaim, sess banking.Session, at time.Time, ev banking.AuditFunc) (banking.Connection, error) {
	if sess.Reference.IsZero() {
		return banking.Connection{}, errors.New("bankfacts: session without a reference")
	}
	if err := requireTime("consent validity", sess.ValidUntil); err != nil {
		return banking.Connection{}, err
	}
	if err := requireTime("activation", at); err != nil {
		return banking.Connection{}, err
	}
	id := claim.Connection.ID
	var out banking.Connection
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// Refused only when the connection was disconnected after the claim:
		// reconnecting a disconnected connection claims it as disconnected.
		res, err := tx.ExecContext(ctx, "UPDATE bank_connections SET status = 'active', session_reference = ?, consent_expires_at = ?, consent_granted_at = ?, consent_state = NULL, last_error_kind = '', last_error_code = '', updated_at = ?, version = version + 1 WHERE id = ? AND (status <> 'disconnected' OR ? = 'disconnected')",
			sess.Reference.Reveal(), ts(sess.ValidUntil), ts(at), ts(at), id, string(claim.Connection.Status))
		if err != nil {
			return fmt.Errorf("bankfacts: activate connection %s: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			if _, err := connectionTx(ctx, tx, "id = ?", id); err != nil {
				return err
			}
			return banking.ErrDisconnected
		}
		if out, err = connectionTx(ctx, tx, "id = ?", id); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, ev, out)
	})
	return out, err
}

// DisconnectWithAudit implements [banking.ConsentStore].
func (s *Store) DisconnectWithAudit(ctx context.Context, id banking.ConnectionID, expected int64, at time.Time, ev banking.AuditFunc) (banking.Connection, error) {
	if err := requireTime("disconnect", at); err != nil {
		return banking.Connection{}, err
	}
	var out banking.Connection
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := store.UpdateVersioned(ctx, tx, "bank_connections",
			"status = 'disconnected', consent_state = NULL, last_error_kind = '', last_error_code = '', updated_at = ?", string(id), expected, ts(at)); err != nil {
			return versionedError(id, err)
		}
		var err error
		if out, err = connectionTx(ctx, tx, "id = ?", id); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, ev, out)
	})
	return out, err
}

// ExpireConsents implements [banking.ConsentStore].
func (s *Store) ExpireConsents(ctx context.Context, at time.Time, ttl time.Duration) (int, error) {
	if ttl <= 0 {
		return 0, nil
	}
	// A read finds out whether anything is due, so the common case (nothing)
	// never takes the write lock.
	var due int
	if err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM bank_connections WHERE consent_state IS NOT NULL AND consent_started_at IS NOT NULL AND consent_started_at <= ?", ts(at.Add(-ttl))).Scan(&due)
	}); err != nil {
		return 0, fmt.Errorf("bankfacts: expire consents: %w", err)
	}
	if due == 0 {
		return 0, nil
	}
	n := 0
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+connectionColumns+" FROM bank_connections WHERE consent_state IS NOT NULL AND consent_started_at IS NOT NULL AND consent_started_at <= ?", ts(at.Add(-ttl)))
		if err != nil {
			return err
		}
		var due []banking.Connection
		for rows.Next() {
			c, err := scanConnection(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			due = append(due, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, c := range due {
			if err := expireTx(ctx, tx, c, at); err != nil {
				return err
			}
			n++
		}
		return rows.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("bankfacts: expire consents: %w", err)
	}
	return n, nil
}
