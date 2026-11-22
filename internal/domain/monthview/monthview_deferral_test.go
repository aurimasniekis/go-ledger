package monthview_test

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// occWithGet adds Get to fakeOcc, for carried-in occurrences.
type occWithGet struct{ fakeOcc }

func (f occWithGet) Get(_ context.Context, id string) (occurrences.Occurrence, error) {
	for _, o := range f.items {
		if o.ID == id {
			return o, nil
		}
	}
	return occurrences.Occurrence{}, resource.ErrNotFound
}

type fakePlans struct{ plans []deferrals.Plan }

func (f fakePlans) Active(_ context.Context, ids []string) (map[string]deferrals.Plan, error) {
	out := map[string]deferrals.Plan{}
	for _, p := range f.plans {
		for _, id := range ids {
			if p.Active() && p.OccurrenceID == id {
				out[id] = p
			}
		}
	}
	return out, nil
}

func (f fakePlans) PlannedIn(_ context.Context, m calendar.YearMonth) ([]deferrals.Plan, error) {
	var out []deferrals.Plan
	for _, p := range f.plans {
		if p.Active() && p.PlannedMonth() == m {
			out = append(out, p)
		}
	}
	return out, nil
}

type deferralFixture struct {
	t        *testing.T
	oct, nov occurrences.Occurrence
}

func newDeferralFixture(t *testing.T) deferralFixture {
	oct, _ := calendar.ParseYearMonth("2026-10")
	nov, _ := calendar.ParseYearMonth("2026-11")
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return deferralFixture{t: t,
		oct: occurrences.Occurrence{ID: "A", CommitmentID: "c", Month: oct, DueDate: date(t, "2026-10-15"), Amount: mustMoney(t, "50.00", "EUR"), Version: 1, UpdatedAt: at},
		nov: occurrences.Occurrence{ID: "B", CommitmentID: "c", Month: nov, DueDate: date(t, "2026-11-15"), Amount: mustMoney(t, "50.00", "EUR"), Version: 1, UpdatedAt: at},
	}
}

func (f deferralFixture) plan(o occurrences.Occurrence, planned string, adjustments ...string) deferrals.Plan {
	p := deferrals.Plan{ID: "P" + o.ID, OccurrenceID: o.ID, CommitmentID: o.CommitmentID, Type: deferrals.TypePersonal, PlannedDate: date(f.t, planned),
		OriginalDueDate: o.DueDate, OriginalAmount: o.Amount, OutstandingWhenPlanned: o.Amount, Version: 1}
	for i, a := range adjustments {
		p.Adjustments = append(p.Adjustments, deferrals.Adjustment{ID: "J" + string(rune('0'+i)), PlanID: p.ID, Kind: deferrals.AdjustmentFee, Amount: mustMoney(f.t, a, "EUR")})
	}
	return p
}

func (f deferralFixture) month(m string, today string, by map[string]payments.Facts, plans ...deferrals.Plan) monthview.Summary {
	f.t.Helper()
	ym, _ := calendar.ParseYearMonth(m)
	svc := monthview.Service{Occurrences: occWithGet{fakeOcc{items: []occurrences.Occurrence{f.oct, f.nov}}}, Payments: facts{by: by}, Deferrals: fakePlans{plans: plans}}
	s, err := svc.Month(context.Background(), ym, date(f.t, today))
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func totalsEUR(t *testing.T, s monthview.Summary) monthview.CurrencyTotals {
	t.Helper()
	if len(s.Totals) != 1 || s.Totals[0].Currency.Code() != "EUR" {
		t.Fatalf("totals %+v, want one EUR entry", s.Totals)
	}
	return s.Totals[0]
}

// The acceptance case of DF1: two outstanding 50 EUR occurrences, the first
// deferred into the second's month, plan 100 EUR there and nothing in the
// first's month, where the first is still owed and overdue.
func TestDeferredOccurrencePlansOnceAndStaysOverdue(t *testing.T) {
	f := newDeferralFixture(t)
	p := f.plan(f.oct, "2026-11-20")

	oct := f.month("2026-10", "2026-10-20", nil, p)
	tot := totalsEUR(t, oct)
	if tot.Outstanding.String() != "50.00 EUR" || tot.Overdue.String() != "50.00 EUR" || tot.OverdueCount != 1 {
		t.Errorf("October: outstanding %s overdue %s (%d), want the deferred occurrence still owed and overdue", tot.Outstanding, tot.Overdue, tot.OverdueCount)
	}
	if tot.Planned.String() != "0.00 EUR" || tot.DeferredOut.String() != "50.00 EUR" {
		t.Errorf("October: planned %s deferred out %s, want 0.00 and 50.00", tot.Planned, tot.DeferredOut)
	}
	if len(oct.Rows) != 1 || oct.Rows[0].Deferral == nil || oct.Rows[0].State.Status != payments.StatusOverdue {
		t.Fatalf("October rows %+v, want the overdue occurrence with its plan", oct.Rows)
	}
	if len(oct.CarriedIn) != 0 {
		t.Errorf("October carried in %d rows, want none", len(oct.CarriedIn))
	}

	nov := f.month("2026-11", "2026-10-20", nil, p)
	tot = totalsEUR(t, nov)
	if tot.Planned.String() != "100.00 EUR" {
		t.Errorf("November planned %s, want 100.00 EUR (50 deferred + 50 due, no double counting)", tot.Planned)
	}
	if tot.Due.String() != "50.00 EUR" || tot.Outstanding.String() != "50.00 EUR" || tot.CarriedIn.String() != "50.00 EUR" || tot.Occurrences != 1 {
		t.Errorf("November due %s outstanding %s carried in %s count %d: the deferred occurrence must stay apart from November's own", tot.Due, tot.Outstanding, tot.CarriedIn, tot.Occurrences)
	}
	if len(nov.Rows) != 1 || nov.Rows[0].Occurrence.ID != "B" || nov.Rows[0].Deferral != nil {
		t.Errorf("November rows %+v, want only the normal occurrence B", nov.Rows)
	}
	if len(nov.CarriedIn) != 1 || nov.CarriedIn[0].Occurrence.ID != "A" || nov.CarriedIn[0].Occurrence.DueDate != f.oct.DueDate {
		t.Errorf("November carried in %+v, want occurrence A with its original due date", nov.CarriedIn)
	}
}

func TestPlannedTotalsAddOnlyExplicitAdjustments(t *testing.T) {
	f := newDeferralFixture(t)
	p := f.plan(f.oct, "2026-11-20", "2.50", "0.75")
	removedAt := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	p.Adjustments[1].RemovedAt = &removedAt
	tot := totalsEUR(t, f.month("2026-11", "2026-10-20", nil, p))
	if tot.Planned.String() != "102.50 EUR" || tot.Adjustments.String() != "2.50 EUR" {
		t.Errorf("planned %s adjustments %s, want 102.50 and 2.50 (removed adjustments do not count)", tot.Planned, tot.Adjustments)
	}
	if tot.Outstanding.String() != "50.00 EUR" || tot.Due.String() != "50.00 EUR" {
		t.Errorf("an adjustment changed the obligation: due %s outstanding %s", tot.Due, tot.Outstanding)
	}
}

func TestPaidDeferredOccurrencePlansNothingAndKeepsItsPlan(t *testing.T) {
	f := newDeferralFixture(t)
	p := f.plan(f.oct, "2026-11-20", "2.50")
	paid := map[string]payments.Facts{"A": {Evidence: []payments.Evidence{{Kind: payments.EvidenceBankTransaction, ID: "m", OccurrenceID: "A", Amount: mustMoney(t, "50.00", "EUR"), Date: date(t, "2026-11-20")}}}}
	nov := f.month("2026-11", "2026-11-21", paid, p)
	tot := totalsEUR(t, nov)
	if tot.Planned.String() != "50.00 EUR" || tot.CarriedIn.String() != "0.00 EUR" || tot.Adjustments.String() != "0.00 EUR" {
		t.Errorf("planned %s carried in %s adjustments %s, want only November's own 50.00", tot.Planned, tot.CarriedIn, tot.Adjustments)
	}
	if len(nov.CarriedIn) != 1 || nov.CarriedIn[0].State.Status != payments.StatusPaid || nov.CarriedIn[0].Deferral == nil {
		t.Errorf("carried in %+v, want the paid occurrence listed with its plan", nov.CarriedIn)
	}
}

func TestUndoneOrMissingPlanPlansInOwnMonth(t *testing.T) {
	f := newDeferralFixture(t)
	p := f.plan(f.oct, "2026-11-20")
	undone := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	p.UndoneAt = &undone
	if tot := totalsEUR(t, f.month("2026-10", "2026-10-20", nil, p)); tot.Planned.String() != "50.00 EUR" || tot.DeferredOut.String() != "0.00 EUR" {
		t.Errorf("October after undo: planned %s deferred out %s, want 50.00 and 0.00", tot.Planned, tot.DeferredOut)
	}
	if tot := totalsEUR(t, f.month("2026-11", "2026-10-20", nil, p)); tot.Planned.String() != "50.00 EUR" || tot.CarriedIn.String() != "0.00 EUR" {
		t.Errorf("November after undo: planned %s carried in %s, want 50.00 and 0.00", tot.Planned, tot.CarriedIn)
	}
}

func TestWithinMonthDeferralStaysInItsMonth(t *testing.T) {
	f := newDeferralFixture(t)
	p := f.plan(f.nov, "2026-11-28", "1.00")
	nov := f.month("2026-11", "2026-11-20", nil, p)
	tot := totalsEUR(t, nov)
	if tot.Planned.String() != "51.00 EUR" || tot.DeferredOut.String() != "0.00 EUR" || tot.CarriedIn.String() != "0.00 EUR" {
		t.Errorf("planned %s deferred out %s carried in %s, want 51.00, 0.00, 0.00", tot.Planned, tot.DeferredOut, tot.CarriedIn)
	}
	if len(nov.CarriedIn) != 0 || len(nov.Rows) != 1 || nov.Rows[0].Deferral == nil {
		t.Errorf("rows %+v carried %+v, want B once, with its plan", nov.Rows, nov.CarriedIn)
	}
	if nov.Rows[0].State.Status != payments.StatusOverdue {
		t.Errorf("status %s, want overdue from the original due date", nov.Rows[0].State.Status)
	}
}

// A fee is added under its own currency: even if it differs from the
// occurrence's (which the store refuses), it never vanishes from the totals.
func TestAdjustmentIsAddedUnderItsOwnCurrency(t *testing.T) {
	f := newDeferralFixture(t)
	p := f.plan(f.oct, "2026-11-20")
	p.Adjustments = []deferrals.Adjustment{{ID: "J0", PlanID: p.ID, Kind: deferrals.AdjustmentFee, Amount: mustMoney(t, "5.00", "USD")}}
	nov := f.month("2026-11", "2026-10-20", nil, p)
	var usd, eur *monthview.CurrencyTotals
	for i := range nov.Totals {
		switch nov.Totals[i].Currency.Code() {
		case "USD":
			usd = &nov.Totals[i]
		case "EUR":
			eur = &nov.Totals[i]
		}
	}
	if usd == nil || eur == nil || usd.Adjustments.String() != "5.00 USD" || usd.Planned.String() != "5.00 USD" || eur.Adjustments.String() != "0.00 EUR" || eur.Planned.String() != "100.00 EUR" {
		t.Fatalf("totals %+v, want the 5.00 USD fee under USD and EUR planned 100.00", nov.Totals)
	}
}

func TestCarriedInCurrencyGetsItsOwnTotals(t *testing.T) {
	f := newDeferralFixture(t)
	f.oct.Amount = mustMoney(t, "30", "USD")
	p := f.plan(f.oct, "2026-11-20")
	nov := f.month("2026-11", "2026-10-20", nil, p)
	if len(nov.Totals) != 2 {
		t.Fatalf("totals %+v, want EUR and USD apart", nov.Totals)
	}
	usd := nov.Totals[1]
	if usd.Currency.Code() != "USD" || usd.Planned.String() != "30 USD" || usd.Due.String() != "0 USD" || usd.Occurrences != 0 {
		t.Errorf("USD totals %+v, want planned 30 with nothing due this month", usd)
	}
	if nov.Totals[0].Planned.String() != "50.00 EUR" {
		t.Errorf("EUR planned %s, want 50.00: currencies are never combined", nov.Totals[0].Planned)
	}
}
