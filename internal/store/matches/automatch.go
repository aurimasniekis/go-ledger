package matches

import (
	"context"
	"database/sql"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/automatch"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	occstore "github.com/aurimasniekis/go-ledger/internal/store/occurrences"
	paymentstore "github.com/aurimasniekis/go-ledger/internal/store/payments"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

var _ automatch.Store = (*Store)(nil)

// Snapshot implements [automatch.Store]: it materialises every commitment's
// occurrences through month through (one write transaction, as a month read
// does), then reads in one read transaction the transactions the engine may
// match (outgoing, booked or pending, still reported, not superseded), the
// open occurrences (not cancelled, of a commitment that is not deleted, with
// no payment evidence and no confirmed match) and every match with whether a
// person ever touched it.
func (s *Store) Snapshot(ctx context.Context, through calendar.YearMonth) (automatch.Snapshot, error) {
	occ := occstore.New(s.db, occstore.Options{Now: s.now, NewID: s.newID})
	if err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return occ.MaterialiseAllTx(ctx, tx, through)
	}); err != nil {
		return automatch.Snapshot{}, err
	}
	var snap automatch.Snapshot
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		if snap.Transactions, err = snapshotTransactions(ctx, tx); err != nil {
			return err
		}
		if snap.Occurrences, err = s.snapshotOccurrences(ctx, tx); err != nil {
			return err
		}
		snap.Matches, err = snapshotMatches(ctx, tx)
		return err
	})
	return snap, err
}

func snapshotTransactions(ctx context.Context, tx *sql.Tx) ([]automatch.Transaction, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, booking_status, source_amount, source_currency, coalesce(booking_date, value_date, transaction_date),"+
		" raw_description, counterparty_name FROM bank_transactions"+
		" WHERE no_longer_reported_at IS NULL AND superseded_by IS NULL AND booking_status IN ('booked', 'pending') AND source_amount GLOB '-*'"+
		" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []automatch.Transaction
	for rows.Next() {
		var t automatch.Transaction
		var amt sqlvalue.Amount
		var cur sqlvalue.Currency
		var date sqlvalue.Date
		var desc, counterparty string
		if err := rows.Scan(&t.ID, &t.Status, &amt, &cur, &date, &desc, &counterparty); err != nil {
			return nil, err
		}
		if t.Amount, err = sqlvalue.Money(amt, cur); err != nil {
			return nil, err
		}
		t.Date, t.Text = date.Date, desc+" "+counterparty
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) snapshotOccurrences(ctx context.Context, tx *sql.Tx) ([]automatch.Occurrence, error) {
	rows, err := tx.QueryContext(ctx, "SELECT o.id, o.commitment_id, c.name, o.due_date, o.amount, o.currency, c.match_tolerance_amount, c.match_tolerance_currency"+
		" FROM commitment_occurrences o JOIN commitments c ON c.id = o.commitment_id"+
		" WHERE o.cancelled_at IS NULL AND c.deleted_at IS NULL"+
		" AND NOT EXISTS (SELECT 1 FROM payment_matches m WHERE m.occurrence_id = o.id AND m.state = 'confirmed')"+
		" ORDER BY o.due_date, o.id")
	if err != nil {
		return nil, err
	}
	var all []automatch.Occurrence
	for rows.Next() {
		var o automatch.Occurrence
		var amt sqlvalue.Amount
		var cur sqlvalue.Currency
		var due sqlvalue.Date
		var tolAmt sql.Null[sqlvalue.Amount]
		var tolCur sql.Null[sqlvalue.Currency]
		if err := rows.Scan(&o.ID, &o.CommitmentID, &o.Name, &due, &amt, &cur, &tolAmt, &tolCur); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if o.Amount, err = sqlvalue.Money(amt, cur); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if tolAmt.Valid {
			t, err := sqlvalue.Money(tolAmt.V, tolCur.V)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			o.Tolerance = &t
		}
		o.DueDate = due.Date
		all = append(all, o)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, len(all))
	for i, o := range all {
		ids[i] = o.ID
	}
	facts, err := paymentstore.FactsTx(ctx, tx, s.zone, ids)
	if err != nil {
		return nil, err
	}
	// An occurrence with any payment evidence (a manual payment or a
	// countable confirmed match) is not open: automatic matching is 1:1
	// only (decision D8), so partly paid occurrences are left to a person.
	out := all[:0]
	for _, o := range all {
		if len(facts[o.ID].Evidence) == 0 {
			out = append(out, o)
		}
	}
	return out, nil
}

func snapshotMatches(ctx context.Context, tx *sql.Tx) ([]automatch.Existing, error) {
	rows, err := tx.QueryContext(ctx, "SELECT m.id, m.transaction_id, m.occurrence_id, m.state, m.origin, m.version, m.created_by_kind,"+
		" (m.created_by_kind IN ('user', 'token') OR coalesce(m.decided_by_kind, '') IN ('user', 'token')"+
		" OR EXISTS (SELECT 1 FROM payment_match_events e WHERE e.match_id = m.id AND e.actor_kind IN ('user', 'token')))"+
		" FROM payment_matches m ORDER BY m.id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []automatch.Existing
	for rows.Next() {
		var e automatch.Existing
		var txID, createdBy string
		var person int64
		if err := rows.Scan(&e.ID, &txID, &e.OccurrenceID, &e.State, &e.Origin, &e.Version, &createdBy, &person); err != nil {
			return nil, err
		}
		e.TransactionID = banking.TransactionID(txID)
		e.ByPerson, e.BySystem = person != 0, audit.ActorKind(createdBy) == audit.ActorSystem
		out = append(out, e)
	}
	return out, rows.Err()
}
