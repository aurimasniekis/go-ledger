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
//   - Outstanding is the sum of what is still owed, as derived per occurrence.
//   - Overdue is the part of Outstanding that belongs to occurrences past their
//     original due date, whether or not a grace period is documented (D7).
//   - Overpaid is informational: evidence above an occurrence's amount.
//
// Evidence in a different currency is never converted and is counted in none
// of the totals; Unconverted counts the occurrences that have some.
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
}

// Summary is one reporting month.
type Summary struct {
	Month calendar.YearMonth
	// AsOf is today in the reporting zone: the date statuses were derived for.
	AsOf   calendar.Date
	Rows   []Row
	Totals []CurrencyTotals // one per currency, sorted by currency code
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
	out := Summary{Month: month, AsOf: today, Rows: []Row{}, Totals: []CurrencyTotals{}}
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
			for _, o := range items {
				name, err := nameOf(o.CommitmentID)
				if err != nil {
					return Summary{}, err
				}
				out.Rows = append(out.Rows, Row{Occurrence: o, State: payments.Derive(o, facts[o.ID], today, s.Grace), CommitmentName: name})
			}
			after = items[len(items)-1].ID
		}
		if !more {
			break
		}
	}
	out.Totals, out.LatestChange = totalsOf(out.Rows)
	return out, nil
}

type accum struct {
	due, paid, outstanding, overdue, overpaid money.Totals
	n, overdueN, unconverted                  int
}

func totalsOf(rows []Row) ([]CurrencyTotals, *time.Time) {
	by := map[money.Currency]*accum{}
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
		a := by[cur]
		if a == nil {
			a = &accum{}
			by[cur] = a
		}
		zero, _ := money.Zero(cur, o.Amount.Amount().Scale())
		overdue := zero
		if st.Status != payments.StatusPaid && st.Timing == payments.StatusOverdue {
			overdue = st.Outstanding
			a.overdueN++
		}
		// Every total gets an entry per occurrence (a zero when nothing
		// applies) so each currency reports all five figures.
		// Add only fails for a Money without a currency, which cannot occur
		// here: every figure derives from the occurrence's amount.
		_ = a.due.Add(o.Amount)
		_ = a.paid.Add(st.Paid)
		_ = a.outstanding.Add(st.Outstanding)
		_ = a.overdue.Add(overdue)
		_ = a.overpaid.Add(st.Overpaid)
		a.n++
		if len(st.Unconverted) > 0 {
			a.unconverted++
		}
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
			Overdue: get(&a.overdue), Overpaid: get(&a.overpaid), Occurrences: a.n, OverdueCount: a.overdueN, Unconverted: a.unconverted})
	}
	if latest.IsZero() {
		return out, nil
	}
	return out, &latest
}
