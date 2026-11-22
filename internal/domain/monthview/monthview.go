// Package monthview builds the monthly view of commitment occurrences: every
// occurrence of one reporting month with its derived payment state, and exact
// per-currency totals (task C4, ADR-0034).
//
// It is the read logic behind GET /api/v1/occurrences/summary and is meant to
// be reused by the MCP list_occurrences tool (task P-MCP2), so no HTTP types
// appear here. All arithmetic is exact decimal arithmetic in Go ([money]);
// amounts in different currencies are never combined.
//
// The totals describe recorded commitments only. They are not a statement of
// money that is safe to spend.
package monthview

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// pageSize is how many occurrences one read of the store fetches; a month is
// read to its end page by page.
const pageSize = 200

// Row is one occurrence with its derived payment state.
type Row struct {
	Occurrence occurrences.Occurrence
	State      payments.State
	// CommitmentName is the name of the commitment the occurrence belongs
	// to; empty when the Service has no Commitments.
	CommitmentName string
	// Evidence is the payment evidence behind State (manual payments and
	// confirmed bank matches, in any currency), newest facts as stored.
	Evidence []payments.Evidence
	// Deferral is the occurrence's active deferral plan (DF1, ADR-0050), nil
	// when it has none or the Service has no Deferrals. It never changes
	// State: a deferred occurrence keeps its due date, its outstanding amount
	// and, past its due date, its overdue status (D7).
	Deferral *deferrals.Plan
}

// CurrencyTotals are the exact totals of the occurrences of one currency.
//
//   - Due is the sum of the occurrence amounts.
//   - Paid is the sum of payment evidence in that currency. It can exceed Due
//     (see Overpaid) and can be below Due - Outstanding when a "paid" status
//     override settles an occurrence the evidence does not cover; the
//     override is a user decision and is visible on the row. With an
//     "unpaid" override the evidence still counts in Paid while Outstanding
//     is the full amount, so Paid + Outstanding can exceed Due.
//   - Outstanding is the sum of what is still owed, as derived per occurrence;
//     occurrences "not recorded" (due before their commitment was created,
//     D38 1a) owe nothing here, and neither does one paid within the match
//     tolerance (D39 5a).
//   - Overdue is the part of Outstanding that belongs to occurrences past their
//     original due date, whether or not a grace period is documented (D7).
//   - Overpaid is informational: evidence above an occurrence's amount.
//
// Evidence in a different currency is never converted and is counted in none
// of the totals; Unconverted counts the occurrences that have some.
//
// The planned figures (DF1, ADR-0050) place each outstanding amount in the
// month it is planned to be paid ([deferrals.PlannedMonth]), so an amount is
// planned in exactly one month and never counted twice:
//
//   - DeferredOut is the part of Outstanding whose active deferral plan
//     falls in a later month; it is still owed (and overdue, past its due
//     date) here, but planned there.
//   - CarriedIn is the outstanding amount of occurrences of earlier months
//     whose active plan falls in this month.
//   - Adjustments sums the explicit fees and charges of the active plans
//     planned in this month whose occurrence still owes something; a plan
//     never adds one by itself (D14).
//   - Planned = Outstanding - DeferredOut + CarriedIn + Adjustments.
//
// Two occurrences of 50 EUR still owed, one of them deferred into the month
// of the other, plan 100 EUR there and nothing in the deferred one's month.
type CurrencyTotals struct {
	Currency     money.Currency
	Due          money.Money
	Paid         money.Money
	Outstanding  money.Money
	Overdue      money.Money
	Overpaid     money.Money
	Occurrences  int
	OverdueCount int
	Unconverted  int
	Planned      money.Money
	DeferredOut  money.Money
	CarriedIn    money.Money
	Adjustments  money.Money
}

// Summary is one reporting month.
type Summary struct {
	Month calendar.YearMonth
	// AsOf is today in the reporting zone: the date statuses were derived for.
	AsOf calendar.Date
	Rows []Row
	// CarriedIn are occurrences of earlier months whose active deferral plan
	// falls in this month (DF1), due-date ordered; empty without Deferrals.
	// They are not this month's occurrences: they count only in the planned
	// figures of the totals.
	CarriedIn []Row
	Totals    []CurrencyTotals // one per currency, sorted by currency code
	// LatestChange is the newest UpdatedAt among the month's occurrences,
	// when occurrences were last created or rewritten; nil for an empty
	// month. It is not the age of any bank data.
	LatestChange *time.Time
}

// Service reads months. Grace may be nil (no grace period is known).
type Service struct {
	Occurrences occurrences.Reader
	Payments    payments.Store
	Grace       payments.GraceFunc
	// Today resolves today in the reporting zone for [Service.Current].
	Today func() (calendar.Date, error)
	// Commitments names each row's commitment. Nil leaves names empty.
	Commitments interface {
		Get(ctx context.Context, id string) (commitments.Commitment, error)
	}
	// Deferrals reads deferral plans (DF1). Nil leaves Row.Deferral and
	// CarriedIn empty and plans every outstanding amount in its own month.
	Deferrals deferrals.Reader
}

// Current is [Service.Month] for today in the reporting zone, as given by
// Today. Callers (HTTP, MCP) use it so the zone and the horizon are applied in
// one place.
func (s Service) Current(ctx context.Context, month calendar.YearMonth) (Summary, error) {
	if s.Today == nil {
		return Summary{}, fmt.Errorf("monthview: no Today function configured")
	}
	today, err := s.Today()
	if err != nil {
		return Summary{}, fmt.Errorf("monthview: resolving today: %w", err)
	}
	return s.Month(ctx, month, today)
}

// Month returns the occurrences of month, due-date ordered, with their payment
// state as of today and the per-currency totals. A month later than
// [occurrences.HorizonLimit] allows fails with [occurrences.ErrBeyondHorizon].
func (s Service) Month(ctx context.Context, month calendar.YearMonth, today calendar.Date) (Summary, error) {
	if month.Compare(occurrences.HorizonLimit(today)) > 0 {
		return Summary{}, occurrences.ErrBeyondHorizon
	}
	out := Summary{Month: month, AsOf: today, Rows: []Row{}, CarriedIn: []Row{}, Totals: []CurrencyTotals{}}
	names := map[string]string{}
	nameOf := func(id string) (string, error) {
		if s.Commitments == nil {
			return "", nil
		}
		if n, ok := names[id]; ok {
			return n, nil
		}
		c, err := s.Commitments.Get(ctx, id)
		if err != nil {
			return "", err
		}
		names[id] = c.Name
		return c.Name, nil
	}
	after := ""
	for {
		items, err := s.Occurrences.List(ctx, occurrences.ListFilter{Month: month, After: after, Limit: pageSize})
		if err != nil {
			return Summary{}, err
		}
		more := len(items) > pageSize
		if more {
			items = items[:pageSize]
		}
		if len(items) > 0 {
			ids := make([]string, len(items))
			for i, o := range items {
				ids[i] = o.ID
			}
			facts, err := s.Payments.Facts(ctx, ids)
			if err != nil {
				return Summary{}, err
			}
			plans := map[string]deferrals.Plan{}
			if s.Deferrals != nil {
				if plans, err = s.Deferrals.Active(ctx, ids); err != nil {
					return Summary{}, err
				}
			}
			for _, o := range items {
				name, err := nameOf(o.CommitmentID)
				if err != nil {
					return Summary{}, err
				}
				row := Row{Occurrence: o, State: payments.Derive(ctx, o, facts[o.ID], today, s.Grace), CommitmentName: name, Evidence: facts[o.ID].Evidence}
				if p, ok := plans[o.ID]; ok {
					row.Deferral = &p
				}
				out.Rows = append(out.Rows, row)
			}
			after = items[len(items)-1].ID
		}
		if !more {
			break
		}
	}
	if s.Deferrals != nil {
		carried, err := s.carriedIn(ctx, month, today, nameOf)
		if err != nil {
			return Summary{}, err
		}
		out.CarriedIn = carried
	}
	out.Totals, out.LatestChange = totalsOf(month, out.Rows, out.CarriedIn)
	return out, nil
}

// carriedIn reads the occurrences of earlier months whose active plan falls in
// month, with their payment state as of today.
func (s Service) carriedIn(ctx context.Context, month calendar.YearMonth, today calendar.Date, nameOf func(string) (string, error)) ([]Row, error) {
	plans, err := s.Deferrals.PlannedIn(ctx, month)
	if err != nil {
		return nil, err
	}
	var ids []string
	byID := map[string]deferrals.Plan{}
	for _, p := range plans {
		ids = append(ids, p.OccurrenceID)
		byID[p.OccurrenceID] = p
	}
	out := []Row{}
	if len(ids) == 0 {
		return out, nil
	}
	facts, err := s.Payments.Facts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		o, err := s.Occurrences.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if o.Month.Compare(month) >= 0 {
			continue // planned within its own month: already a row
		}
		name, err := nameOf(o.CommitmentID)
		if err != nil {
			return nil, err
		}
		p := byID[id]
		out = append(out, Row{Occurrence: o, State: payments.Derive(ctx, o, facts[id], today, s.Grace), CommitmentName: name, Evidence: facts[id].Evidence, Deferral: &p})
	}
	slices.SortStableFunc(out, func(a, b Row) int {
		if c := a.Occurrence.DueDate.Compare(b.Occurrence.DueDate); c != 0 {
			return c
		}
		return strings.Compare(a.Occurrence.ID, b.Occurrence.ID)
	})
	return out, nil
}

type accum struct {
	due, paid, outstanding, overdue, overpaid    money.Totals
	planned, deferredOut, carriedIn, adjustments money.Totals
	n, overdueN, unconverted                     int
}

// owed is what a row still owes in the totals: its outstanding amount, but
// nothing for an occurrence "not recorded" (D38 1a). A cancelled row is
// skipped before this.
func owed(st payments.State, zero money.Money) money.Money {
	if st.Status != payments.StatusPaid && st.Timing == payments.StatusNotRecorded {
		return zero
	}
	return st.Outstanding
}

// planAdjustments is what a row's active plan adds to its planned month: its
// active (not removed) adjustments while the occurrence still owes something.
func planAdjustments(r Row, owes money.Money) []money.Money {
	if r.Deferral == nil || !r.Deferral.Active() || owes.Sign() <= 0 {
		return nil
	}
	var out []money.Money
	for _, a := range r.Deferral.Adjustments {
		if a.RemovedAt == nil {
			out = append(out, a.Amount)
		}
	}
	return out
}

func totalsOf(month calendar.YearMonth, rows, carried []Row) ([]CurrencyTotals, *time.Time) {
	by := map[money.Currency]*accum{}
	get := func(cur money.Currency, zero money.Money) *accum {
		a := by[cur]
		if a == nil {
			a = &accum{}
			by[cur] = a
		}
		// Every total gets an entry (a zero when nothing applies) so each
		// currency reports all figures.
		for _, t := range []*money.Totals{&a.due, &a.paid, &a.outstanding, &a.overdue, &a.overpaid, &a.planned, &a.deferredOut, &a.carriedIn, &a.adjustments} {
			_ = t.Add(zero)
		}
		return a
	}
	// addAdjustments adds a row's plan adjustments under the adjustments' own
	// currency, never the occurrence's: a fee can then never silently vanish.
	addAdjustments := func(r Row, owes money.Money) {
		for _, adj := range planAdjustments(r, owes) {
			zero, _ := money.Zero(adj.Currency(), adj.Amount().Scale())
			a := get(adj.Currency(), zero)
			_ = a.planned.Add(adj)
			_ = a.adjustments.Add(adj)
		}
	}
	var latest time.Time
	for _, r := range rows {
		o, st := r.Occurrence, r.State
		if o.UpdatedAt.After(latest) {
			latest = o.UpdatedAt
		}
		if st.Status == payments.StatusCancelled {
			// A cancelled occurrence is owed nothing: it adds to no total and no count.
			continue
		}
		cur := o.Amount.Currency()
		zero, _ := money.Zero(cur, o.Amount.Amount().Scale())
		a := get(cur, zero)
		overdue := zero
		outstanding := owed(st, zero)
		if st.Status != payments.StatusPaid && st.Timing == payments.StatusOverdue {
			overdue = st.Outstanding
			a.overdueN++
		}
		if deferrals.PlannedMonth(o, r.Deferral) != month {
			_ = a.deferredOut.Add(outstanding)
		} else {
			_ = a.planned.Add(outstanding)
			addAdjustments(r, outstanding)
		}
		// Add only fails for a Money without a currency, which cannot occur
		// here: every figure derives from the occurrence's amount.
		_ = a.due.Add(o.Amount)
		_ = a.paid.Add(st.Paid)
		_ = a.outstanding.Add(outstanding)
		_ = a.overdue.Add(overdue)
		_ = a.overpaid.Add(st.Overpaid)
		a.n++
		if len(st.Unconverted) > 0 {
			a.unconverted++
		}
	}
	for _, r := range carried {
		o, st := r.Occurrence, r.State
		if st.Status == payments.StatusCancelled {
			continue
		}
		cur := o.Amount.Currency()
		zero, _ := money.Zero(cur, o.Amount.Amount().Scale())
		a := get(cur, zero)
		outstanding := owed(st, zero)
		_ = a.carriedIn.Add(outstanding)
		_ = a.planned.Add(outstanding)
		addAdjustments(r, outstanding)
	}
	currencies := make([]money.Currency, 0, len(by))
	for cur := range by {
		currencies = append(currencies, cur)
	}
	slices.SortFunc(currencies, func(a, b money.Currency) int { return strings.Compare(a.Code(), b.Code()) })
	out := make([]CurrencyTotals, 0, len(by))
	for _, cur := range currencies {
		a := by[cur]
		get := func(t *money.Totals) money.Money { m, _ := t.Get(cur); return m }
		out = append(out, CurrencyTotals{Currency: cur, Due: get(&a.due), Paid: get(&a.paid), Outstanding: get(&a.outstanding),
			Overdue: get(&a.overdue), Overpaid: get(&a.overpaid), Occurrences: a.n, OverdueCount: a.overdueN, Unconverted: a.unconverted,
			Planned: get(&a.planned), DeferredOut: get(&a.deferredOut), CarriedIn: get(&a.carriedIn), Adjustments: get(&a.adjustments)})
	}
	if latest.IsZero() {
		return out, nil
	}
	return out, &latest
}
