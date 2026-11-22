// Package occurrences persists commitment occurrences (task C2, ADR-0027) in
// the tables of migration 0011_commitment_occurrences. It materialises them
// from commitment definitions month by month, applies changed terms from an
// effective date forward (decision D6 edits option a) and implements
// [occurrences.Reader].
//
// The transaction-level methods (MaterialiseTx, ApplyTx, RemoveUnpaidTx) run
// inside the caller's write transaction, so a definition change and its
// occurrences commit together (internal/store/commitments calls them).
package occurrences

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/sqlvalue"
)

// DefaultEvidence lists the payment evidence that makes an occurrence paid
// and so immutable: each entry is a SQL boolean expression over the
// occurrence aliased "o". Any evidence counts, so a partially paid occurrence
// (D7) is immutable too. Task C3 (ADR-0031) adds manual payment records, and
// a deleted one no longer counts: an occurrence whose payments were all
// deleted is editable again. Task X1 adds confirmed bank-transaction
// allocations here and to internal/store/payments EvidenceSources. Evidence
// tables must refer to commitment_occurrences(id) with ON DELETE RESTRICT, so
// the database also refuses to drop a paid occurrence.
var DefaultEvidence = []string{
	// A manual payment replaced by a bank match (D45, ADR-0049) does not
	// count itself: the confirmed (or reversed) match below keeps the
	// occurrence instead, whether its transaction counts or not, so the
	// replacement being on hold (X5c, payments.EvidenceSources) changes
	// nothing here.
	"EXISTS (SELECT 1 FROM manual_payments mp WHERE mp.occurrence_id = o.id AND mp.deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM manual_payment_replacements r WHERE r.manual_payment_id = mp.id AND r.restored_at IS NULL))",
	// Payment matches (X1, ADR-0035): a confirmed match keeps its occurrence
	// even while its transaction does not count (no longer reported), and a
	// reversed one is payment history that is kept too. Suggestions and
	// rejections do not: they are dropped with the occurrence.
	"EXISTS (SELECT 1 FROM payment_matches pm WHERE pm.occurrence_id = o.id AND pm.state IN ('confirmed', 'reversed'))",
}

// DefaultReferences lists what a delete of an occurrence must not touch: every
// row that refers to it with ON DELETE RESTRICT. It is [DefaultEvidence] plus
// the manual payments that were soft-deleted (D26-3a), which stay as history
// and keep their occurrence from being removed, though they do not make it
// paid. Deleting a referenced occurrence would fail on the foreign key.
var DefaultReferences = []string{
	"EXISTS (SELECT 1 FROM manual_payments mp WHERE mp.occurrence_id = o.id)",
	DefaultEvidence[1],
	// Deferral plans (DF1, ADR-0050), active or undone, are planning history:
	// an occurrence with one is cancelled instead of deleted, so the plan and
	// its history stay readable. A plan is never evidence: it does not make
	// the occurrence paid or keep it from being rewritten.
	"EXISTS (SELECT 1 FROM deferral_plans dp WHERE dp.occurrence_id = o.id)",
}

// Options configures a [Store].
type Options struct {
	// Now and NewID default to time.Now and resource.NewID.
	Now   func() time.Time
	NewID func() string
	// Evidence replaces [DefaultEvidence] (tests use it to stand in for C3).
	Evidence []string
	// References replaces [DefaultReferences]. When Evidence is replaced and
	// References is not, References is Evidence.
	References []string
}

// Store reads and writes occurrences on a [store.DB].
type Store struct {
	db    *store.DB
	now   func() time.Time
	newID func() string
	paid  string // SQL boolean expression over alias o: counting evidence exists
	refd  string // SQL boolean expression over alias o: a row refers to it
}

var _ occurrences.Reader = (*Store)(nil)

// New returns a Store.
func New(db *store.DB, opts Options) *Store {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = resource.NewID
	}
	ev := opts.Evidence
	if ev == nil {
		ev = DefaultEvidence
	}
	refs := opts.References
	if refs == nil {
		refs = DefaultReferences
		if opts.Evidence != nil {
			refs = opts.Evidence
		}
	}
	join := func(parts []string) string {
		if len(parts) == 0 {
			return "0"
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}
	return &Store{db: db, now: opts.Now, newID: opts.NewID, paid: join(ev), refd: join(refs)}
}

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func horizon(ctx context.Context, q querier, commitmentID string) (calendar.YearMonth, bool, error) {
	var s string
	err := q.QueryRowContext(ctx, "SELECT generated_through FROM commitment_occurrence_horizons WHERE commitment_id = ?", commitmentID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return calendar.YearMonth{}, false, nil
	}
	if err != nil {
		return calendar.YearMonth{}, false, err
	}
	m, err := calendar.ParseYearMonth(s)
	return m, err == nil, err
}

// pending returns the effective date of the commitment's latest terms edit
// that took effect after the day it was made, if one is recorded. The caller
// decides whether it has passed.
func pending(ctx context.Context, q querier, commitmentID string) (calendar.Date, bool, error) {
	var s sql.NullString
	err := q.QueryRowContext(ctx, "SELECT pending_effective_from FROM commitment_occurrence_horizons WHERE commitment_id = ?", commitmentID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !s.Valid) {
		return calendar.Date{}, false, nil
	}
	if err != nil {
		return calendar.Date{}, false, err
	}
	d, err := calendar.ParseDate(s.String)
	return d, err == nil, err
}

func (s *Store) setHorizon(ctx context.Context, tx *sql.Tx, commitmentID string, through calendar.YearMonth) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO commitment_occurrence_horizons (commitment_id, generated_through, updated_at) VALUES (?, ?, ?)"+
		" ON CONFLICT (commitment_id) DO UPDATE SET generated_through = excluded.generated_through, updated_at = excluded.updated_at"+
		" WHERE excluded.generated_through > commitment_occurrence_horizons.generated_through",
		commitmentID, through.String(), store.Timestamp{Time: s.now()})
	return err
}

// insert stores a planned occurrence. A month that already has one is left
// alone (generation is idempotent), except that with revive a cancelled
// occurrence of the month (D31) is revived with the planned date and amount:
// an edit that schedules the month again must not leave it empty.
func (s *Store) insert(ctx context.Context, tx *sql.Tx, commitmentID string, p occurrences.Planned, now time.Time, revive bool) (bool, error) {
	onConflict := " ON CONFLICT (commitment_id, month) DO NOTHING"
	if revive {
		onConflict = " ON CONFLICT (commitment_id, month) DO UPDATE SET due_date = excluded.due_date, amount = excluded.amount, currency = excluded.currency," +
			" updated_at = excluded.updated_at, version = version + 1, cancelled_at = NULL, cancelled_reason = NULL WHERE commitment_occurrences.cancelled_at IS NOT NULL"
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO commitment_occurrences (id, commitment_id, month, due_date, amount, currency, created_at, updated_at)"+
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?)"+onConflict,
		s.newID(), commitmentID, p.Month.String(), sqlvalue.Date{Date: p.DueDate}, sqlvalue.Amount{Decimal: p.Amount.Amount()},
		sqlvalue.Currency{Currency: p.Amount.Currency()}, store.Timestamp{Time: now}, store.Timestamp{Time: now})
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MaterialiseTx stores the occurrences t schedules in the months after the
// commitment's horizon through through, and moves the horizon there. Months
// already materialised are never regenerated, so calling it again (or with
// an earlier month) changes nothing: generation is idempotent.
func (s *Store) MaterialiseTx(ctx context.Context, tx *sql.Tx, commitmentID string, t occurrences.Terms, through calendar.YearMonth) error {
	cur, has, err := horizon(ctx, tx, commitmentID)
	if err != nil {
		return err
	}
	from := t.FirstMonth()
	if has {
		if cur.Compare(through) >= 0 {
			return nil
		}
		next, err := cur.AddMonths(1)
		if err != nil {
			return err
		}
		if next.Compare(from) > 0 {
			from = next
		}
	}
	now := s.now().UTC()
	for _, p := range occurrences.Generate(t, from, through) {
		if _, err := s.insert(ctx, tx, commitmentID, p, now, false); err != nil {
			return fmt.Errorf("occurrences: materialise: %w", err)
		}
	}
	return s.setHorizon(ctx, tx, commitmentID, through)
}

// ApplyTx applies changed terms effective from effective (D6 edits option a):
// it first materialises the old terms through effective's month, so every
// month before the effective date keeps the occurrences the old terms gave
// it, then rewrites, removes or adds unpaid occurrences from that month
// through the horizon as [occurrences.PlanEdit] decides. Paid occurrences are
// never changed. It returns the plan it carried out.
//
// today is the current date in the reporting zone. A terms edit effective
// after today is recorded as pending; until that date passes, an edit
// effective earlier is refused with [occurrences.PendingTermsError], because
// the definition no longer holds the terms the months in between were
// generated from.
func (s *Store) ApplyTx(ctx context.Context, tx *sql.Tx, commitmentID string, old, next occurrences.Terms, effective, today calendar.Date) (occurrences.Plan, error) {
	if effective.IsZero() || today.IsZero() {
		return occurrences.Plan{}, errors.New("occurrences: an effective date and today are required to apply changed terms")
	}
	if p, ok, err := pending(ctx, tx, commitmentID); err != nil {
		return occurrences.Plan{}, err
	} else if ok && p.After(today) && effective.Before(p) {
		return occurrences.Plan{}, &occurrences.PendingTermsError{Pending: p, Effective: effective}
	}
	through, has, err := horizon(ctx, tx, commitmentID)
	if err != nil {
		return occurrences.Plan{}, err
	}
	if em := effective.YearMonth(); !has || through.Compare(em) < 0 {
		through = em
	}
	if err := s.MaterialiseTx(ctx, tx, commitmentID, old, through); err != nil {
		return occurrences.Plan{}, err
	}
	// A start or end date change first drops the unpaid out-of-range
	// occurrences due before the effective date (history, which the plan would
	// leave alone). Those due on or after it are left to the plan, which
	// rewrites them in place or removes them.
	var outOfRange, keptOverride, cancelled int
	if old.Start != next.Start || !sameEnd(old.End, next.End) {
		if outOfRange, cancelled, keptOverride, err = s.removeOutOfRange(ctx, tx, commitmentID, next, effective); err != nil {
			return occurrences.Plan{}, err
		}
	}
	all, err := s.existing(ctx, tx, commitmentID, effective.YearMonth(), through)
	if err != nil {
		return occurrences.Plan{}, err
	}
	plan := occurrences.PlanEdit(all, through, next, effective)
	// Unpaid history rows still outside the terms are the ones kept for an
	// override: audit them as kept_override only, not also as kept_history.
	for _, e := range all {
		if !e.Paid && e.DueDate.Before(effective) && (e.DueDate.Before(next.Start) || (next.End != nil && e.DueDate.After(*next.End))) {
			plan.KeptHistory--
		}
	}
	// An overridden row outside the new dates that the plan would remove stays:
	// the override would cascade away with it (D23). Count it as kept_override.
	outside := map[string]bool{}
	for _, e := range all {
		if e.DueDate.Before(next.Start) || (next.End != nil && e.DueDate.After(*next.End)) {
			outside[e.ID] = true
		}
	}
	kept := plan.Remove[:0:0]
	for _, id := range plan.Remove {
		if outside[id] {
			var has bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM occurrence_status_overrides WHERE occurrence_id = ?)", id).Scan(&has); err != nil {
				return occurrences.Plan{}, fmt.Errorf("occurrences: check override: %w", err)
			}
			if has {
				keptOverride++
				continue
			}
		}
		kept = append(kept, id)
	}
	plan.Remove = kept
	plan.OutOfRange, plan.KeptOverride = outOfRange, keptOverride
	now := s.now().UTC()
	// Remove deletes what nothing refers to and cancels what only deleted
	// payments refer to (D31); the plan then lists only the deleted ones, so
	// the audit counts each occurrence once.
	removed := plan.Remove[:0:0]
	for _, id := range plan.Remove {
		gone, err := s.removeOrCancel(ctx, tx, id, occurrences.CancelTermsChange, now)
		if err != nil {
			return occurrences.Plan{}, fmt.Errorf("occurrences: remove: %w", err)
		}
		switch gone {
		case goneDeleted:
			removed = append(removed, id)
		case goneCancelled:
			cancelled++
		}
	}
	plan.Remove, plan.CancelledReferenced = removed, cancelled
	for _, u := range plan.Update {
		if _, err := tx.ExecContext(ctx, "UPDATE commitment_occurrences AS o SET due_date = ?, amount = ?, currency = ?, updated_at = ?, version = version + 1"+
			" WHERE id = ? AND NOT "+s.paid,
			sqlvalue.Date{Date: u.DueDate}, sqlvalue.Amount{Decimal: u.Amount.Amount()}, sqlvalue.Currency{Currency: u.Amount.Currency()},
			store.Timestamp{Time: now}, u.ID); err != nil {
			return occurrences.Plan{}, fmt.Errorf("occurrences: update: %w", err)
		}
	}
	created := plan.Create[:0:0]
	for _, p := range plan.Create {
		ok, err := s.insert(ctx, tx, commitmentID, p, now, true)
		if err != nil {
			return occurrences.Plan{}, fmt.Errorf("occurrences: create: %w", err)
		}
		if ok { // defensive: the plan creates only into months with no live row, so a conflict inserted nothing; never count it as created
			created = append(created, p)
		}
	}
	plan.Create = created
	var marker any // an edit effective today or earlier leaves nothing pending
	if effective.After(today) {
		marker = sqlvalue.Date{Date: effective}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE commitment_occurrence_horizons SET pending_effective_from = ? WHERE commitment_id = ?", marker, commitmentID); err != nil {
		return occurrences.Plan{}, fmt.Errorf("occurrences: pending marker: %w", err)
	}
	return plan, nil
}

func sameEnd(a, b *calendar.Date) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// gone is what removeOrCancel did to an occurrence.
type gone int

const (
	goneNothing   gone = iota // paid, or already cancelled: left alone
	goneDeleted               // no row referred to it: deleted
	goneCancelled             // only deleted payments referred to it: cancelled (D31)
)

// removeOrCancel is the one way an occurrence goes away (D31). An occurrence
// with live payment evidence is never touched. One that rows still refer to
// (soft-deleted manual payments, kept as history) cannot be deleted, so it is
// cancelled with reason and its open match suggestions are dropped. Anything
// else is deleted.
func (s *Store) removeOrCancel(ctx context.Context, tx *sql.Tx, id, reason string, now time.Time) (gone, error) {
	var paid, refd, cancelled bool
	err := tx.QueryRowContext(ctx, "SELECT "+s.paid+", "+s.refd+", o.cancelled_at IS NOT NULL FROM commitment_occurrences AS o WHERE o.id = ?", id).Scan(&paid, &refd, &cancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return goneNothing, nil
	}
	if err != nil {
		return goneNothing, err
	}
	switch {
	case paid || cancelled:
		return goneNothing, nil
	case !refd:
		_, err = tx.ExecContext(ctx, "DELETE FROM commitment_occurrences AS o WHERE o.id = ? AND NOT "+s.refd, id)
		return goneDeleted, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM payment_matches WHERE occurrence_id = ? AND state IN ('suggested', 'rejected')", id); err != nil {
		return goneNothing, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE commitment_occurrences SET cancelled_at = ?, cancelled_reason = ?, updated_at = ?, version = version + 1 WHERE id = ?",
		store.Timestamp{Time: now}, reason, store.Timestamp{Time: now}, id)
	return goneCancelled, err
}

// removeOutOfRange removes the unpaid occurrences due before the start date or
// after the end date (D23 2b) and before the effective date: later ones are
// rewritten or removed by the edit plan. Occurrences with live payment
// evidence stay, and so do those with a status override of any state: the
// override is the user's record and would cascade away with the row. One that
// only deleted payments refer to is cancelled (D31). It returns how many it
// deleted, how many it cancelled and how many it kept for an override.
func (s *Store) removeOutOfRange(ctx context.Context, tx *sql.Tx, commitmentID string, t occurrences.Terms, effective calendar.Date) (removed, cancelled, keptOverride int, err error) {
	where := " FROM commitment_occurrences AS o WHERE commitment_id = ? AND cancelled_at IS NULL AND NOT " + s.paid + " AND due_date < ? AND (due_date < ?"
	args := []any{commitmentID, sqlvalue.Date{Date: effective}, sqlvalue.Date{Date: t.Start}}
	if t.End != nil {
		where += " OR due_date > ?"
		args = append(args, sqlvalue.Date{Date: *t.End})
	}
	where += ")"
	const hasOverride = " AND EXISTS (SELECT 1 FROM occurrence_status_overrides so WHERE so.occurrence_id = o.id)"
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*)"+where+hasOverride, args...).Scan(&keptOverride); err != nil {
		return 0, 0, 0, fmt.Errorf("occurrences: count kept overrides: %w", err)
	}
	ids, err := s.ids(ctx, tx, "SELECT o.id"+where+" AND NOT EXISTS (SELECT 1 FROM occurrence_status_overrides so WHERE so.occurrence_id = o.id)", args...)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("occurrences: find out of range: %w", err)
	}
	now := s.now().UTC()
	for _, id := range ids {
		g, err := s.removeOrCancel(ctx, tx, id, occurrences.CancelOutOfRange, now)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("occurrences: remove out of range: %w", err)
		}
		switch g {
		case goneDeleted:
			removed++
		case goneCancelled:
			cancelled++
		}
	}
	return removed, cancelled, keptOverride, nil
}

func (s *Store) ids(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PaidTx reports whether any of the commitment's occurrences has live payment
// evidence: payment history that blocks deleting the commitment.
func (s *Store) PaidTx(ctx context.Context, tx *sql.Tx, commitmentID string) (bool, error) {
	var paid bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM commitment_occurrences AS o WHERE o.commitment_id = ? AND o.cancelled_at IS NULL AND "+s.paid+")", commitmentID).Scan(&paid)
	return paid, err
}

// RemoveUnpaidTx is what a commitment delete does to the commitment's
// occurrences (D31): it deletes those nothing refers to and cancels those only
// deleted payments refer to, so the caller can then delete the commitment, or
// keep its row when kept is true (cancelled occurrences refer to it). The
// caller refuses the delete first when [Store.PaidTx] says payment evidence
// exists. It returns how many it deleted and how many it cancelled.
func (s *Store) RemoveUnpaidTx(ctx context.Context, tx *sql.Tx, commitmentID string) (removed, cancelled int, kept bool, err error) {
	ids, err := s.ids(ctx, tx, "SELECT id FROM commitment_occurrences WHERE commitment_id = ? AND cancelled_at IS NULL", commitmentID)
	if err != nil {
		return 0, 0, false, err
	}
	now := s.now().UTC()
	for _, id := range ids {
		g, err := s.removeOrCancel(ctx, tx, id, occurrences.CancelCommitmentDelete, now)
		if err != nil {
			return 0, 0, false, err
		}
		switch g {
		case goneDeleted:
			removed++
		case goneCancelled:
			cancelled++
		}
	}
	err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM commitment_occurrences WHERE commitment_id = ?)", commitmentID).Scan(&kept)
	return removed, cancelled, kept, err
}

const columns = "o.id, o.commitment_id, o.month, o.due_date, o.amount, o.currency, o.version, o.created_at, o.updated_at, o.cancelled_at, o.cancelled_reason"

func scan(row interface{ Scan(...any) error }, extra ...any) (occurrences.Occurrence, error) {
	var o occurrences.Occurrence
	var month string
	var due sqlvalue.Date
	var amount sqlvalue.Amount
	var currency sqlvalue.Currency
	var created, updated store.Timestamp
	var cancelledAt, cancelledReason sql.NullString
	dst := append([]any{&o.ID, &o.CommitmentID, &month, &due, &amount, &currency, &o.Version, &created, &updated, &cancelledAt, &cancelledReason}, extra...)
	if err := row.Scan(dst...); err != nil {
		return occurrences.Occurrence{}, err
	}
	m, err := calendar.ParseYearMonth(month)
	if err != nil {
		return occurrences.Occurrence{}, err
	}
	money, err := sqlvalue.Money(amount, currency)
	if err != nil {
		return occurrences.Occurrence{}, err
	}
	o.Month, o.DueDate, o.Amount, o.CreatedAt, o.UpdatedAt = m, due.Date, money, created.Time, updated.Time
	if cancelledAt.Valid {
		var ts store.Timestamp
		if err := ts.Scan(cancelledAt.String); err != nil {
			return occurrences.Occurrence{}, err
		}
		o.CancelledAt, o.CancelledReason = &ts.Time, cancelledReason.String
	}
	return o, nil
}

func (s *Store) existing(ctx context.Context, q querier, commitmentID string, from, to calendar.YearMonth) ([]occurrences.Existing, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+", "+s.paid+" FROM commitment_occurrences AS o"+
		" WHERE o.commitment_id = ? AND o.cancelled_at IS NULL AND o.month >= ? AND o.month <= ? ORDER BY o.month", commitmentID, from.String(), to.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []occurrences.Existing
	for rows.Next() {
		var paid bool
		o, err := scan(rows, &paid)
		if err != nil {
			return nil, err
		}
		out = append(out, occurrences.Existing{Occurrence: o, Paid: paid})
	}
	return out, rows.Err()
}

// ExistingTx returns the commitment's stored occurrences of the months from
// through to with their paid flag, in month order.
func (s *Store) ExistingTx(ctx context.Context, tx *sql.Tx, commitmentID string, from, to calendar.YearMonth) ([]occurrences.Existing, error) {
	return s.existing(ctx, tx, commitmentID, from, to)
}

// MaterialiseAllTx materialises every commitment through month.
func (s *Store) MaterialiseAllTx(ctx context.Context, tx *sql.Tx, month calendar.YearMonth) error {
	rows, err := tx.QueryContext(ctx, "SELECT c.id, c.amount, c.currency, c.schedule, c.start_date, c.end_date FROM commitments c"+
		" LEFT JOIN commitment_occurrence_horizons h ON h.commitment_id = c.id"+
		" WHERE c.deleted_at IS NULL AND (h.generated_through IS NULL OR h.generated_through < ?) ORDER BY c.id", month.String())
	if err != nil {
		return err
	}
	type pending struct {
		id    string
		terms occurrences.Terms
	}
	var todo []pending
	for rows.Next() {
		var id, schedule string
		var amount sqlvalue.Amount
		var currency sqlvalue.Currency
		var start sqlvalue.Date
		var end sql.NullString
		if err := rows.Scan(&id, &amount, &currency, &schedule, &start, &end); err != nil {
			_ = rows.Close()
			return err
		}
		m, err := sqlvalue.Money(amount, currency)
		if err != nil {
			_ = rows.Close()
			return err
		}
		sch, errs := commitments.ParseSchedule([]byte(schedule), "schedule")
		if len(errs) > 0 {
			_ = rows.Close()
			return errors.Join(fmt.Errorf("occurrences: stored schedule of commitment %s", id), commitments.Validation(errs))
		}
		t := occurrences.Terms{Amount: m, Schedule: sch, Start: start.Date}
		if end.Valid {
			d, err := calendar.ParseDate(end.String)
			if err != nil {
				_ = rows.Close()
				return err
			}
			t.End = &d
		}
		todo = append(todo, pending{id, t})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range todo {
		if err := s.MaterialiseTx(ctx, tx, p.id, p.terms, month); err != nil {
			return err
		}
	}
	return nil
}

// List returns up to f.Limit+1 occurrences of f.Month after f.After, by due
// date then ID. When a commitment is not yet materialised through that month
// it first materialises them in one write transaction; otherwise (the usual
// case) it only reads, so a month read does not take the write lock.
func (s *Store) List(ctx context.Context, f occurrences.ListFilter) ([]occurrences.Occurrence, error) {
	var needs bool
	if err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		needs, err = needsMaterialising(ctx, tx, f.Month)
		return err
	}); err != nil {
		return nil, err
	}
	var out []occurrences.Occurrence
	if !needs {
		err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			out, err = listMonth(ctx, tx, f)
			return err
		})
		return out, err
	}
	err := s.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := s.MaterialiseAllTx(ctx, tx, f.Month); err != nil {
			return err
		}
		var err error
		out, err = listMonth(ctx, tx, f)
		return err
	})
	return out, err
}

// needsMaterialising reports whether some commitment is not materialised
// through month.
func needsMaterialising(ctx context.Context, q querier, month calendar.YearMonth) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM commitments c"+
		" LEFT JOIN commitment_occurrence_horizons h ON h.commitment_id = c.id"+
		" WHERE c.deleted_at IS NULL AND (h.generated_through IS NULL OR h.generated_through < ?))", month.String()).Scan(&n)
	return n != 0, err
}

func listMonth(ctx context.Context, tx *sql.Tx, f occurrences.ListFilter) ([]occurrences.Occurrence, error) {
	where := []string{"o.month = ?"}
	vals := []any{f.Month.String()}
	if !f.IncludeCancelled {
		where = append(where, "o.cancelled_at IS NULL")
	}
	if len(f.CommitmentIDs) > 0 {
		where = append(where, "o.commitment_id IN (?"+strings.Repeat(", ?", len(f.CommitmentIDs)-1)+")")
		for _, id := range f.CommitmentIDs {
			vals = append(vals, id)
		}
	}
	if f.After != "" {
		var due string
		err := tx.QueryRowContext(ctx, "SELECT due_date FROM commitment_occurrences WHERE id = ?", f.After).Scan(&due)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, occurrences.ErrCursorGone
		}
		if err != nil {
			return nil, err
		}
		where = append(where, "(o.due_date, o.id) > (?, ?)")
		vals = append(vals, due, f.After)
	}
	vals = append(vals, f.Limit+1)
	rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM commitment_occurrences AS o WHERE "+strings.Join(where, " AND ")+
		" ORDER BY o.due_date, o.id LIMIT ?", vals...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []occurrences.Occurrence
	for rows.Next() {
		o, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Get returns one occurrence or resource.ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (occurrences.Occurrence, error) {
	var o occurrences.Occurrence
	err := s.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		o, err = GetTx(ctx, tx, id)
		return err
	})
	return o, err
}

// GetTx returns one occurrence read in the caller's transaction, or
// resource.ErrNotFound (internal/store/payments reads the occurrence it
// changes in its own write transaction).
func GetTx(ctx context.Context, tx *sql.Tx, id string) (occurrences.Occurrence, error) {
	o, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM commitment_occurrences AS o WHERE o.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return occurrences.Occurrence{}, resource.ErrNotFound
	}
	return o, err
}
