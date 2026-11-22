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
// up (task E2): first those without an answer, newest first; then those whose
// answer is "unavailable", or "none" when recheckNone is set, and was last
// tried before recheckBefore, oldest attempt first, so a small batch reaches
// every row over several passes.
// Transactions that a booking replaced are left out.
func (s *Store) Pending(ctx context.Context, limit int, recheckBefore time.Time, recheckNone bool) ([]enrichment.Candidate, error) {
	var out []enrichment.Candidate
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.id, t.raw_description, t.counterparty_name
FROM bank_transactions t LEFT JOIN merchant_enrichments e ON e.transaction_id = t.id
WHERE t.superseded_by IS NULL AND t.booking_status <> 'cancelled'
  AND (e.transaction_id IS NULL
       OR ((e.status = 'unavailable' OR (e.status = 'none' AND ?)) AND coalesce(e.last_failed_at, e.retrieved_at) < ?))
ORDER BY e.transaction_id IS NOT NULL, coalesce(e.last_failed_at, e.retrieved_at), t.imported_at DESC, t.id DESC LIMIT ?`, recheckNone, store.Timestamp{Time: recheckBefore.UTC()}, limit)
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

// Confirmations lists the person's confirmed and corrected mappings, and their
// rejections, with the bank text they were made about (task E2: mappings are
// reused as suggestions only; a rejection stops a curated identity).
func (s *Store) Confirmations(ctx context.Context) ([]enrichment.Confirmation, error) {
	var out []enrichment.Confirmation
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.raw_description, t.counterparty_name, r.name, r.decision = 'rejected'
FROM merchant_reviews r JOIN bank_transactions t ON t.id = r.transaction_id
WHERE r.decision IN ('confirmed', 'corrected', 'rejected')
ORDER BY r.transaction_id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c enrichment.Confirmation
			if err := rows.Scan(&c.Subject.Description, &c.Subject.Counterparty, &c.Name, &c.Rejected); err != nil {
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
