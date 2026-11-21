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
// (D7) is immutable too. Task C3 (ADR-0031) adds manual payment records; task
// X1 adds confirmed bank-transaction allocations here and to
// internal/store/payments EvidenceSources. Evidence tables must refer to
// commitment_occurrences(id) with ON DELETE RESTRICT, so the database also
// refuses to drop a paid occurrence.
var DefaultEvidence = []string{
	"EXISTS (SELECT 1 FROM manual_payments mp WHERE mp.occurrence_id = o.id)",
	// Payment matches (X1, ADR-0035): a confirmed match keeps its occurrence
	// even while its transaction does not count (no longer reported), and a
	// reversed one is payment history that is kept too. Suggestions and
	// rejections do not: they are dropped with the occurrence.
	"EXISTS (SELECT 1 FROM payment_matches pm WHERE pm.occurrence_id = o.id AND pm.state IN ('confirmed', 'reversed'))",
}

// Options configures a [Store].
type Options struct {
	// Now and NewID default to time.Now and resource.NewID.
	Now   func() time.Time
	NewID func() string
	// Evidence replaces [DefaultEvidence] (tests use it to stand in for C3).
	Evidence []string
}

// Store reads and writes occurrences on a [store.DB].
type Store struct {
	db    *store.DB
	now   func() time.Time
	newID func() string
	paid  string // SQL boolean expression over alias o
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
	paid := "0"
	if len(ev) > 0 {
		paid = "(" + strings.Join(ev, " OR ") + ")"
	}
	return &Store{db: db, now: opts.Now, newID: opts.NewID, paid: paid}
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

func (s *Store) insert(ctx context.Context, tx *sql.Tx, commitmentID string, p occurrences.Planned, now time.Time) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO commitment_occurrences (id, commitment_id, month, due_date, amount, currency, created_at, updated_at)"+
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (commitment_id, month) DO NOTHING",
		s.newID(), commitmentID, p.Month.String(), sqlvalue.Date{Date: p.DueDate}, sqlvalue.Amount{Decimal: p.Amount.Amount()},
		sqlvalue.Currency{Currency: p.Amount.Currency()}, store.Timestamp{Time: now}, store.Timestamp{Time: now})
	return err
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
		if err := s.insert(ctx, tx, commitmentID, p, now); err != nil {
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
	existing, err := s.existing(ctx, tx, commitmentID, effective.YearMonth(), through)
	if err != nil {
		return occurrences.Plan{}, err
	}
	plan := occurrences.PlanEdit(existing, through, next, effective)
	now := s.now().UTC()
	for _, id := range plan.Remove {
		if _, err := tx.ExecContext(ctx, "DELETE FROM commitment_occurrences AS o WHERE id = ? AND NOT "+s.paid, id); err != nil {
			return occurrences.Plan{}, fmt.Errorf("occurrences: remove: %w", err)
		}
	}
	for _, u := range plan.Update {
		if _, err := tx.ExecContext(ctx, "UPDATE commitment_occurrences AS o SET due_date = ?, amount = ?, currency = ?, updated_at = ?, version = version + 1"+
			" WHERE id = ? AND NOT "+s.paid,
			sqlvalue.Date{Date: u.DueDate}, sqlvalue.Amount{Decimal: u.Amount.Amount()}, sqlvalue.Currency{Currency: u.Amount.Currency()},
			store.Timestamp{Time: now}, u.ID); err != nil {
			return occurrences.Plan{}, fmt.Errorf("occurrences: update: %w", err)
		}
	}
	for _, p := range plan.Create {
		if err := s.insert(ctx, tx, commitmentID, p, now); err != nil {
			return occurrences.Plan{}, fmt.Errorf("occurrences: create: %w", err)
		}
	}
	var marker any // an edit effective today or earlier leaves nothing pending
	if effective.After(today) {
		marker = sqlvalue.Date{Date: effective}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE commitment_occurrence_horizons SET pending_effective_from = ? WHERE commitment_id = ?", marker, commitmentID); err != nil {
		return occurrences.Plan{}, fmt.Errorf("occurrences: pending marker: %w", err)
	}
	return plan, nil
}

// RemoveUnpaidTx deletes the commitment's occurrences that no payment
// evidence refers to, before the commitment itself is deleted. Paid ones stay
// and their foreign key then refuses the delete.
// It returns how many it deleted.
func (s *Store) RemoveUnpaidTx(ctx context.Context, tx *sql.Tx, commitmentID string) (int, error) {
	res, err := tx.ExecContext(ctx, "DELETE FROM commitment_occurrences AS o WHERE commitment_id = ? AND NOT "+s.paid, commitmentID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

const columns = "o.id, o.commitment_id, o.month, o.due_date, o.amount, o.currency, o.version, o.created_at, o.updated_at"

func scan(row interface{ Scan(...any) error }, extra ...any) (occurrences.Occurrence, error) {
	var o occurrences.Occurrence
	var month string
	var due sqlvalue.Date
	var amount sqlvalue.Amount
	var currency sqlvalue.Currency
	var created, updated store.Timestamp
	dst := append([]any{&o.ID, &o.CommitmentID, &month, &due, &amount, &currency, &o.Version, &created, &updated}, extra...)
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
	return o, nil
}

func (s *Store) existing(ctx context.Context, q querier, commitmentID string, from, to calendar.YearMonth) ([]occurrences.Existing, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+", "+s.paid+" FROM commitment_occurrences AS o"+
		" WHERE o.commitment_id = ? AND o.month >= ? AND o.month <= ? ORDER BY o.month", commitmentID, from.String(), to.String())
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
		" WHERE h.generated_through IS NULL OR h.generated_through < ? ORDER BY c.id", month.String())
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
		" WHERE h.generated_through IS NULL OR h.generated_through < ?)", month.String()).Scan(&n)
	return n != 0, err
}

func listMonth(ctx context.Context, tx *sql.Tx, f occurrences.ListFilter) ([]occurrences.Occurrence, error) {
	where := []string{"o.month = ?"}
	vals := []any{f.Month.String()}
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
