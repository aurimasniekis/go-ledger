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
// up (task E2): first those without an answer, newest first; then those
// flagged for a recheck because a decision about another transaction of the
// same merchant changed (task M1); then those whose answer is "unavailable",
// or "none" when recheckNone is set, and was last tried before recheckBefore,
// and those whose "none" or "ambiguous" answer is older than changedBefore
// (the person's confirmations changed since; zero means never) and whose last
// failed re-lookup, if any, is also before recheckBefore (a failure backs the
// row off for the recheck interval, like any other), oldest
// attempt first, so a small batch reaches every row over several passes.
// Transactions that a booking replaced are left out.
func (s *Store) Pending(ctx context.Context, limit int, recheckBefore time.Time, recheckNone bool, changedBefore time.Time) ([]enrichment.Candidate, error) {
	var out []enrichment.Candidate
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// A zero watermark is never compared (the flag below is false), but
		// a timestamp argument must still be a real time.
		since := changedBefore
		if since.IsZero() {
			since = time.Unix(0, 0)
		}
		rows, err := tx.QueryContext(ctx, `SELECT t.id, t.raw_description, t.counterparty_name
FROM bank_transactions t LEFT JOIN merchant_enrichments e ON e.transaction_id = t.id
WHERE t.superseded_by IS NULL AND t.booking_status <> 'cancelled'
  AND (e.transaction_id IS NULL
       OR e.recheck = 1
       OR ((e.status = 'unavailable' OR (e.status = 'none' AND ?)) AND coalesce(e.last_failed_at, e.retrieved_at) < ?)
       OR (? AND e.status IN ('none', 'ambiguous') AND coalesce(e.last_failed_at, e.retrieved_at) < ? AND (e.last_failed_at IS NULL OR e.last_failed_at < ?)))
ORDER BY CASE WHEN e.transaction_id IS NULL THEN 0 WHEN e.recheck = 1 THEN 1 ELSE 2 END, coalesce(e.last_failed_at, e.retrieved_at), t.imported_at DESC, t.id DESC LIMIT ?`,
			recheckNone, store.Timestamp{Time: recheckBefore.UTC()}, !changedBefore.IsZero(), store.Timestamp{Time: since.UTC()}, store.Timestamp{Time: recheckBefore.UTC()}, limit)
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
// reused as suggestions only; a rejection stops a curated identity). A
// mapping names its merchant record's current display name (task M1), so a
// renamed or merged merchant is suggested by its name now.
func (s *Store) Confirmations(ctx context.Context) ([]enrichment.Confirmation, error) {
	var out []enrichment.Confirmation
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.raw_description, t.counterparty_name, coalesce(m.display_name, r.name), r.decision = 'rejected'
FROM merchant_reviews r JOIN bank_transactions t ON t.id = r.transaction_id LEFT JOIN merchants m ON m.id = r.merchant_id
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
