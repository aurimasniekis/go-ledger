package bankfacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// Refresh bookkeeping (migration 0005_bank_refresh, ADR-0020): refresh runs,
// per-account refresh state and the window import of the B2 import engine.

const runColumns = "id, connection_id, started_at, finished_at, outcome, error_kind, error_code, accounts_reported, accounts_succeeded, accounts_failed, accounts_no_longer_reported, balances_added, balances_rejected, transactions_inserted, transactions_updated, transactions_unchanged, transactions_kept_booked, transactions_no_longer_reported, transactions_superseded, transactions_rejected"

func scanRun(r rowScanner) (banking.RefreshRun, error) {
	var run banking.RefreshRun
	var started store.Timestamp
	var finished sql.Null[store.Timestamp]
	c := &run.RefreshCounts
	err := r.Scan(&run.ID, &run.ConnectionID, &started, &finished, &run.Outcome, &run.ErrorKind, &run.ErrorCode,
		&c.AccountsReported, &c.AccountsSucceeded, &c.AccountsFailed, &c.AccountsNoLongerReported, &c.BalancesAdded, &c.BalancesRejected,
		&c.TransactionsInserted, &c.TransactionsUpdated, &c.TransactionsUnchanged, &c.TransactionsKeptBooked,
		&c.TransactionsNoLongerReported, &c.TransactionsSuperseded, &c.TransactionsRejected)
	if err != nil {
		return banking.RefreshRun{}, err
	}
	run.StartedAt, run.FinishedAt = started.Time, finished.V.Time
	return run, nil
}

func runTx(ctx context.Context, tx *sql.Tx, id banking.RefreshRunID) (banking.RefreshRun, error) {
	run, err := scanRun(tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM bank_refresh_runs WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return banking.RefreshRun{}, notFound("refresh run", id)
	}
	if err != nil {
		return banking.RefreshRun{}, fmt.Errorf("bankfacts: read refresh run %s: %w", id, err)
	}
	return run, nil
}

// StartRefresh implements [banking.FactStore].
func (s *Store) StartRefresh(ctx context.Context, id banking.ConnectionID, at time.Time, lease time.Duration) (banking.RefreshRun, error) {
	if lease <= 0 {
		lease = banking.DefaultRunLease
	}
	if err := requireTime("refresh start", at); err != nil {
		return banking.RefreshRun{}, err
	}
	rid := banking.RefreshRunID(s.newID())
	var out banking.RefreshRun
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := connectionTx(ctx, tx, "id = ?", id); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM bank_refresh_runs WHERE connection_id = ? AND outcome = 'running' AND started_at > ?", id, ts(at.Add(-lease))).Scan(&live); err != nil {
			return fmt.Errorf("bankfacts: read running refresh runs: %w", err)
		}
		if live > 0 {
			return banking.ErrRefreshInProgress
		}
		if _, err := tx.ExecContext(ctx, "UPDATE bank_refresh_runs SET outcome = 'interrupted', finished_at = max(?, started_at) WHERE connection_id = ? AND outcome = 'running'", ts(at), id); err != nil {
			return fmt.Errorf("bankfacts: close unfinished refresh runs: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bank_refresh_runs (id, connection_id, started_at, outcome) VALUES (?, ?, ?, 'running')", rid, id, ts(at)); err != nil {
			return fmt.Errorf("bankfacts: start refresh run: %w", err)
		}
		var err error
		out, err = runTx(ctx, tx, rid)
		return err
	})
	return out, err
}

// FinishRefresh implements [banking.FactStore].
func (s *Store) FinishRefresh(ctx context.Context, run banking.RefreshRun) (banking.RefreshRun, error) {
	if !run.Outcome.Valid() || run.Outcome == banking.RefreshRunning {
		return banking.RefreshRun{}, fmt.Errorf("bankfacts: cannot finish a refresh run as %q", run.Outcome)
	}
	if err := requireTime("refresh finish", run.FinishedAt); err != nil {
		return banking.RefreshRun{}, err
	}
	c := run.RefreshCounts
	var out banking.RefreshRun
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE bank_refresh_runs SET finished_at = max(?, started_at), outcome = ?, error_kind = ?, error_code = ?, accounts_reported = ?, accounts_succeeded = ?, accounts_failed = ?, accounts_no_longer_reported = ?, balances_added = ?, balances_rejected = ?, transactions_inserted = ?, transactions_updated = ?, transactions_unchanged = ?, transactions_kept_booked = ?, transactions_no_longer_reported = ?, transactions_superseded = ?, transactions_rejected = ? WHERE id = ? AND outcome = 'running'",
			ts(run.FinishedAt), run.Outcome, run.ErrorKind, run.ErrorCode,
			c.AccountsReported, c.AccountsSucceeded, c.AccountsFailed, c.AccountsNoLongerReported, c.BalancesAdded, c.BalancesRejected,
			c.TransactionsInserted, c.TransactionsUpdated, c.TransactionsUnchanged, c.TransactionsKeptBooked,
			c.TransactionsNoLongerReported, c.TransactionsSuperseded, c.TransactionsRejected, run.ID)
		if err != nil {
			return fmt.Errorf("bankfacts: finish refresh run %s: %w", run.ID, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			if _, err := runTx(ctx, tx, run.ID); err != nil {
				return err
			}
			return fmt.Errorf("bankfacts: refresh run %s is already finished", run.ID)
		}
		out, err = runTx(ctx, tx, run.ID)
		return err
	})
	return out, err
}

// RefreshRuns implements [banking.FactStore].
func (s *Store) RefreshRuns(ctx context.Context, id banking.ConnectionID, limit int) ([]banking.RefreshRun, error) {
	if limit <= 0 {
		limit = -1
	}
	var out []banking.RefreshRun
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := connectionTx(ctx, tx, "id = ?", id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+runColumns+" FROM bank_refresh_runs WHERE connection_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ?", id, limit)
		if err != nil {
			return fmt.Errorf("bankfacts: list refresh runs: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return fmt.Errorf("bankfacts: read refresh run: %w", err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// idList encodes ids as a JSON array for json_each.
func idList[T ~string](ids []T) (string, error) {
	if ids == nil {
		ids = []T{}
	}
	b, err := json.Marshal(ids)
	return string(b), err
}

// MarkAccountsNoLongerReported implements [banking.FactStore].
func (s *Store) MarkAccountsNoLongerReported(ctx context.Context, id banking.ConnectionID, reported []banking.AccountID, at time.Time) (int, error) {
	if err := requireTime("seen", at); err != nil {
		return 0, err
	}
	list, err := idList(reported)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := connectionTx(ctx, tx, "id = ?", id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE bank_accounts SET no_longer_reported_at = ? WHERE connection_id = ? AND no_longer_reported_at IS NULL AND id NOT IN (SELECT value FROM json_each(?))", ts(at), id, list)
		if err != nil {
			return fmt.Errorf("bankfacts: mark accounts no longer reported: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// syncTx reads an account's refresh state (zero if it has none yet) and its
// oldest open pending transaction.
func syncTx(ctx context.Context, tx *sql.Tx, id banking.AccountID) (banking.AccountSync, error) {
	out := banking.AccountSync{AccountID: id}
	var attempt store.Timestamp
	var success sql.Null[store.Timestamp]
	var through, pending sql.Null[sqlvalue.Date]
	err := tx.QueryRowContext(ctx, "SELECT last_attempt_at, last_success_at, last_error_kind, last_error_code, last_notice_code, transactions_through FROM bank_account_sync WHERE account_id = ?", id).
		Scan(&attempt, &success, &out.LastErrorKind, &out.LastErrorCode, &out.LastNoticeCode, &through)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return banking.AccountSync{}, fmt.Errorf("bankfacts: read account refresh state %s: %w", id, err)
	default:
		out.LastAttemptAt, out.LastSuccessAt, out.TransactionsThrough = attempt.Time, success.V.Time, through.V.Date
	}
	err = tx.QueryRowContext(ctx, "SELECT min(coalesce(booking_date, value_date, transaction_date)) FROM bank_transactions WHERE account_id = ? AND booking_status = 'pending' AND no_longer_reported_at IS NULL AND superseded_by IS NULL", id).Scan(&pending)
	if err != nil {
		return banking.AccountSync{}, fmt.Errorf("bankfacts: read open pending transactions %s: %w", id, err)
	}
	out.OldestOpenPending = pending.V.Date
	return out, nil
}

// BeginAccountRefresh implements [banking.FactStore].
func (s *Store) BeginAccountRefresh(ctx context.Context, id banking.AccountID, at time.Time) (banking.AccountSync, error) {
	if err := requireTime("attempt", at); err != nil {
		return banking.AccountSync{}, err
	}
	var out banking.AccountSync
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := accountProvider(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bank_account_sync (account_id, last_attempt_at) VALUES (?, ?) ON CONFLICT (account_id) DO UPDATE SET last_attempt_at = excluded.last_attempt_at", id, ts(at)); err != nil {
			return fmt.Errorf("bankfacts: record refresh attempt %s: %w", id, err)
		}
		var err error
		out, err = syncTx(ctx, tx, id)
		return err
	})
	return out, err
}

// EndAccountRefresh implements [banking.FactStore].
func (s *Store) EndAccountRefresh(ctx context.Context, id banking.AccountID, cause error, notice string, at time.Time) (banking.AccountSync, error) {
	if err := requireTime("refresh end", at); err != nil {
		return banking.AccountSync{}, err
	}
	var out banking.AccountSync
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := accountProvider(ctx, tx, id); err != nil {
			return err
		}
		// A row exists after BeginAccountRefresh; insert one otherwise so
		// the end is never lost.
		if _, err := tx.ExecContext(ctx, "INSERT INTO bank_account_sync (account_id, last_attempt_at) VALUES (?, ?) ON CONFLICT (account_id) DO NOTHING", id, ts(at)); err != nil {
			return fmt.Errorf("bankfacts: record refresh end %s: %w", id, err)
		}
		var err error
		if cause == nil {
			_, err = tx.ExecContext(ctx, "UPDATE bank_account_sync SET last_success_at = ?, last_error_kind = '', last_error_code = '', last_notice_code = ? WHERE account_id = ?", ts(at), notice, id)
		} else {
			kind, code := banking.Classify(cause), ""
			if e, ok := errors.AsType[*banking.Error](cause); ok {
				code = e.Code
			}
			_, err = tx.ExecContext(ctx, "UPDATE bank_account_sync SET last_error_kind = ?, last_error_code = ?, last_notice_code = ? WHERE account_id = ?", kind, code, notice, id)
		}
		if err != nil {
			return fmt.Errorf("bankfacts: record refresh end %s: %w", id, err)
		}
		out, err = syncTx(ctx, tx, id)
		return err
	})
	return out, err
}

// AccountSync implements [banking.FactStore].
func (s *Store) AccountSync(ctx context.Context, id banking.AccountID) (banking.AccountSync, error) {
	var out banking.AccountSync
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := accountProvider(ctx, tx, id); err != nil {
			return err
		}
		var err error
		out, err = syncTx(ctx, tx, id)
		return err
	})
	return out, err
}

// ImportTransactionWindow implements [banking.FactStore].
func (s *Store) ImportTransactionWindow(ctx context.Context, id banking.AccountID, w banking.TransactionWindow, seenAt time.Time) (banking.WindowResult, error) {
	if err := requireTime("seen", seenAt); err != nil {
		return banking.WindowResult{}, err
	}
	if w.From.IsZero() || w.To.IsZero() || w.From.After(w.To) {
		return banking.WindowResult{}, errors.New("bankfacts: a transaction window needs From <= To")
	}
	if err := checkBatch(w.Transactions); err != nil {
		return banking.WindowResult{}, err
	}
	var out banking.WindowResult
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		provider, err := accountProvider(ctx, tx, id)
		if err != nil {
			return err
		}
		ups, err := s.upsertTransactionsTx(ctx, tx, id, provider, w.Transactions, seenAt)
		if err != nil {
			return err
		}
		seen := make([]banking.TransactionID, len(ups))
		for i, u := range ups {
			seen[i] = u.ID
		}
		list, err := idList(seen)
		if err != nil {
			return err
		}
		var marked []markedRow
		if !w.Incomplete {
			if marked, err = markUnreported(ctx, tx, id, w, list, seenAt); err != nil {
				return err
			}
		}
		superseded, err := linkSupersededByWindow(ctx, tx, id, w, list)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO bank_account_sync (account_id, last_attempt_at, transactions_through) VALUES (?, ?, ?) ON CONFLICT (account_id) DO UPDATE SET transactions_through = CASE WHEN bank_account_sync.transactions_through IS NULL OR bank_account_sync.transactions_through < excluded.transactions_through THEN excluded.transactions_through ELSE bank_account_sync.transactions_through END",
			id, ts(seenAt), sqlvalue.Date{Date: w.To})
		if err != nil {
			return fmt.Errorf("bankfacts: advance transactions cursor %s: %w", id, err)
		}
		out = banking.WindowResult{Upserts: ups, NoLongerReported: len(marked), Superseded: superseded}
		return nil
	})
	if err != nil {
		return banking.WindowResult{}, err
	}
	return out, nil
}

type markedRow struct {
	id     banking.TransactionID
	status banking.BookingStatus
	ref    string
}

// linkSupersededByWindow links every pending transaction that is no longer
// reported and not yet linked to the booked transaction of the window that
// carries its entry reference. That covers a pending row marked by this very
// window, one marked by an earlier refresh (a hold that vanished before its
// booking appeared) and one dated in another chunk. It returns how many were
// linked.
func linkSupersededByWindow(ctx context.Context, tx *sql.Tx, id banking.AccountID, w banking.TransactionWindow, seen string) (int, error) {
	var refs []string
	for _, k := range w.Transactions {
		if k.Status == banking.StatusBooked && k.EntryReference != "" && !slices.Contains(refs, k.EntryReference) {
			refs = append(refs, k.EntryReference)
		}
	}
	if len(refs) == 0 {
		return 0, nil
	}
	list, err := idList(refs)
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, entry_reference FROM bank_transactions WHERE account_id = ? AND booking_status = 'pending' AND no_longer_reported_at IS NOT NULL AND superseded_by IS NULL AND entry_reference IN (SELECT value FROM json_each(?))", id, list)
	if err != nil {
		return 0, fmt.Errorf("bankfacts: find unlinked pending transactions: %w", err)
	}
	type cand struct {
		id  banking.TransactionID
		ref string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.ref); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("bankfacts: find unlinked pending transactions: %w", err)
		}
		cands = append(cands, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, fmt.Errorf("bankfacts: find unlinked pending transactions: %w", err)
	}
	perRef := map[string]int{}
	for _, c := range cands {
		perRef[c.ref]++
	}
	n := 0
	for _, c := range cands {
		if perRef[c.ref] != 1 {
			continue // several holds share the reference: linking would be a guess
		}
		ok, err := linkSuperseded(ctx, tx, id, c.id, c.ref, seen)
		if err != nil {
			return 0, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// markUnreported marks the account's reported transactions dated inside the
// window that the window did not contain (seen is a JSON id list).
func markUnreported(ctx context.Context, tx *sql.Tx, id banking.AccountID, w banking.TransactionWindow, seen string, at time.Time) ([]markedRow, error) {
	rows, err := tx.QueryContext(ctx, "UPDATE bank_transactions SET no_longer_reported_at = ? WHERE account_id = ? AND no_longer_reported_at IS NULL AND coalesce(booking_date, value_date, transaction_date) BETWEEN ? AND ? AND id NOT IN (SELECT value FROM json_each(?)) RETURNING id, booking_status, coalesce(entry_reference, '')",
		ts(at), id, sqlvalue.Date{Date: w.From}, sqlvalue.Date{Date: w.To}, seen)
	if err != nil {
		return nil, fmt.Errorf("bankfacts: mark transactions no longer reported: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []markedRow
	for rows.Next() {
		var m markedRow
		if err := rows.Scan(&m.id, &m.status, &m.ref); err != nil {
			return nil, fmt.Errorf("bankfacts: mark transactions no longer reported: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bankfacts: mark transactions no longer reported: %w", err)
	}
	return out, nil
}

// MaxSupersedeGapDays bounds how long after a pending transaction's date its
// booked successor may be dated: the default chunk (31 days) plus the default
// overlap (7). A booking dated before the hold, or later than the bound, is
// not linked even with the same entry reference.
const MaxSupersedeGapDays = 38

// linkSuperseded links a pending transaction that stopped being reported to
// the booked transaction of the same window with the same entry reference,
// when there is exactly one dated from the pending date up to
// [MaxSupersedeGapDays] later; with none or several it links nothing (an
// ambiguous link would be a guess).
func linkSuperseded(ctx context.Context, tx *sql.Tx, account banking.AccountID, pending banking.TransactionID, ref, seen string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM bank_transactions WHERE account_id = ? AND entry_reference = ? AND booking_status = 'booked' AND id <> ? AND id IN (SELECT value FROM json_each(?)) AND julianday(coalesce(booking_date, value_date, transaction_date)) - (SELECT julianday(coalesce(p.booking_date, p.value_date, p.transaction_date)) FROM bank_transactions p WHERE p.id = ?) BETWEEN 0 AND ? LIMIT 2", account, ref, pending, seen, pending, MaxSupersedeGapDays)
	if err != nil {
		return false, fmt.Errorf("bankfacts: find booked successor: %w", err)
	}
	var ids []banking.TransactionID
	for rows.Next() {
		var tid banking.TransactionID
		if err := rows.Scan(&tid); err != nil {
			_ = rows.Close()
			return false, err
		}
		ids = append(ids, tid)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, fmt.Errorf("bankfacts: find booked successor: %w", err)
	}
	if len(ids) != 1 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", ids[0], pending); err != nil {
		return false, fmt.Errorf("bankfacts: link superseded transaction %s: %w", pending, err)
	}
	return true, nil
}
