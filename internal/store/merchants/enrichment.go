package merchants

import (
	"context"
	"database/sql"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/enrichment"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

var _ enrichment.Store = (*Store)(nil)

// Pending lists up to limit transactions the enrichment service should look
// up (task E2): those without an answer, and those whose answer is "none" or
// "unavailable" and was last tried before recheckBefore. Newest first.
// Transactions that a booking replaced are left out.
func (s *Store) Pending(ctx context.Context, limit int, recheckBefore time.Time) ([]enrichment.Candidate, error) {
	var out []enrichment.Candidate
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.id, t.raw_description, t.counterparty_name
FROM bank_transactions t LEFT JOIN merchant_enrichments e ON e.transaction_id = t.id
WHERE t.superseded_by IS NULL AND t.booking_status <> 'cancelled'
  AND (e.transaction_id IS NULL
       OR (e.status IN ('none', 'unavailable') AND coalesce(e.last_failed_at, e.retrieved_at) < ?))
ORDER BY t.imported_at DESC, t.id DESC LIMIT ?`, store.Timestamp{Time: recheckBefore.UTC()}, limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c enrichment.Candidate
			var id string
			if err := rows.Scan(&id, &c.Description, &c.Counterparty); err != nil {
				return err
			}
			c.ID = banking.TransactionID(id)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Confirmations lists the person's confirmed and corrected mappings with the
// bank text they were made about (task E2: reused as suggestions only).
func (s *Store) Confirmations(ctx context.Context) ([]enrichment.Confirmation, error) {
	var out []enrichment.Confirmation
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.raw_description, t.counterparty_name, r.name
FROM merchant_reviews r JOIN bank_transactions t ON t.id = r.transaction_id
WHERE r.decision IN ('confirmed', 'corrected') AND r.name <> ''
ORDER BY r.transaction_id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c enrichment.Confirmation
			if err := rows.Scan(&c.Subject.Description, &c.Subject.Counterparty, &c.Name); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteAnswersBefore deletes the answers of source retrieved before t (a
// licence's retention limit) and reports how many. Answers are derived data
// that can be fetched again; a decision is never touched.
func (s *Store) DeleteAnswersBefore(ctx context.Context, source string, t time.Time) (int, error) {
	var n int64
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM merchant_enrichments WHERE source = ? AND retrieved_at IS NOT NULL AND retrieved_at < ?", source, store.Timestamp{Time: t.UTC()})
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}
