package matches

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// ConfirmReplacing confirms a suggestion "as the payment I recorded"
// (decision D45, ADR-0049). In one write transaction it marks each listed
// manual payment replaced by the match (manual_payment_replacements, migration
// 0024), then confirms the match with the checks of Confirm run against the
// evidence that remains: the replaced records no longer count, so the
// overpayment check and the tolerance snapshot (D41 2a and 3a: bank evidence
// alone) see the occurrence as paid by the bank match and whatever else is
// left.
//
// Every record must be of the match's occurrence (ErrReplaceOtherOccurrence),
// in its currency (ErrReplaceCurrency), not deleted and not already replaced
// (ErrReplaceUnavailable). Automation never may (ErrActorReplace). The
// confirmation is an ordinary one otherwise: Unconfirm inside its undo window
// takes it back and the records count again, exactly as before.
func (s *Store) ConfirmReplacing(ctx context.Context, actor audit.Actor, id string, expected int64, r matches.Replacement) (matches.Change, error) {
	if err := commitments.Validation(r.Validate()); err != nil {
		return matches.Change{}, err
	}
	if automation(actor) {
		return matches.Change{}, matches.ErrActorReplace
	}
	return s.confirmWith(ctx, actor, id, expected, r.AllowOverpayment, r.Reason, func(ctx context.Context, tx *sql.Tx, m matches.Match, eventID string, now time.Time) error {
		for _, pid := range r.ManualPaymentIDs {
			p, deleted, replaced, err := manualTx(ctx, tx, pid)
			if err != nil {
				return err
			}
			switch {
			case p.OccurrenceID != m.OccurrenceID:
				return matches.ErrReplaceOtherOccurrence
			case deleted || replaced:
				return matches.ErrReplaceUnavailable
			case p.Amount.Currency() != m.Amount.Currency():
				return matches.ErrReplaceCurrency
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO manual_payment_replacements (id, manual_payment_id, match_id, replaced_event_id, replaced_at) VALUES (?, ?, ?, ?, ?)",
				s.newID(), p.ID, m.ID, eventID, store.Timestamp{Time: now}); err != nil {
				return fmt.Errorf("matches: replace manual payment: %w", err)
			}
			if err := s.audit.WriteAudit(ctx, tx, payments.ReplacedEvent(auditActor(actor), p, m.ID, now)); err != nil {
				return err
			}
		}
		return nil
	})
}

// manualTx reads a manual payment, whether it is deleted and whether a match
// replaces it now.
func manualTx(ctx context.Context, tx *sql.Tx, id string) (p payments.ManualPayment, deleted, replaced bool, err error) {
	var amount sqlvalue.Amount
	var currency sqlvalue.Currency
	var deletedAt sql.Null[string]
	var active int
	err = tx.QueryRowContext(ctx, "SELECT p.id, p.occurrence_id, p.amount, p.currency, p.deleted_at,"+
		" (SELECT count(*) FROM manual_payment_replacements r WHERE r.manual_payment_id = p.id AND r.restored_at IS NULL)"+
		" FROM manual_payments p WHERE p.id = ?", id).Scan(&p.ID, &p.OccurrenceID, &amount, &currency, &deletedAt, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, false, matches.ErrReplaceNotFound
	}
	if err != nil {
		return p, false, false, err
	}
	if p.Amount, err = sqlvalue.Money(amount, currency); err != nil {
		return p, false, false, err
	}
	return p, deletedAt.Valid, active > 0, nil
}

// restore makes the manual payments the match replaces count again (an
// unconfirm or an unmatch, by; ADR-0049): each active replacement is stamped
// with the change's history entry eventID and audited. A record deleted while
// replaced stays deleted.
func (s *Store) restore(ctx context.Context, tx *sql.Tx, actor audit.Actor, m matches.Match, eventID string, by matches.Action, now time.Time) error {
	rows, err := tx.QueryContext(ctx, "SELECT r.id, p.id, p.occurrence_id, p.amount, p.currency FROM manual_payment_replacements r JOIN manual_payments p ON p.id = r.manual_payment_id"+
		" WHERE r.match_id = ? AND r.restored_at IS NULL ORDER BY r.rowid", m.ID)
	if err != nil {
		return err
	}
	type restored struct {
		id string
		p  payments.ManualPayment
	}
	var list []restored
	for rows.Next() {
		var r restored
		var amount sqlvalue.Amount
		var currency sqlvalue.Currency
		if err := rows.Scan(&r.id, &r.p.ID, &r.p.OccurrenceID, &amount, &currency); err != nil {
			_ = rows.Close()
			return err
		}
		if r.p.Amount, err = sqlvalue.Money(amount, currency); err != nil {
			_ = rows.Close()
			return err
		}
		list = append(list, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range list {
		if _, err := tx.ExecContext(ctx, "UPDATE manual_payment_replacements SET restored_at = ?, restored_event_id = ? WHERE id = ?",
			store.Timestamp{Time: now}, eventID, r.id); err != nil {
			return fmt.Errorf("matches: restore manual payment: %w", err)
		}
		if err := s.audit.WriteAudit(ctx, tx, payments.RestoredEvent(auditActor(actor), r.p, m.ID, string(by), now)); err != nil {
			return err
		}
	}
	return nil
}

// auditActor is the actor as the match history records it (storedKind,
// storedVia), so the manual payment's audit entries name the same provenance.
func auditActor(a audit.Actor) audit.Actor {
	if storedKind(a.Kind) == audit.ActorSystem {
		return audit.System
	}
	return a
}
