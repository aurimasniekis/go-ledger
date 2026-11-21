package monthview_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

type fakeOcc struct {
	occurrences.Reader
	items []occurrences.Occurrence
}

func (f fakeOcc) List(_ context.Context, fl occurrences.ListFilter) ([]occurrences.Occurrence, error) {
	var out []occurrences.Occurrence
	for _, o := range f.items {
		if o.Month == fl.Month && (fl.After == "" || o.ID > fl.After) {
			out = append(out, o)
		}
	}
	if len(out) > fl.Limit+1 {
		out = out[:fl.Limit+1]
	}
	return out, nil
}

// facts implements only the Facts method of payments.Store.
type facts struct {
	payments.Store
	by map[string]payments.Facts
}

func (f facts) Facts(_ context.Context, ids []string) (map[string]payments.Facts, error) {
	return f.by, nil
}

func mustMoney(t *testing.T, amount, cur string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, cur)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func date(t *testing.T, s string) calendar.Date {
	t.Helper()
	d, err := calendar.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMonthTotalsPerCurrencyAreExactAndNeverMixed(t *testing.T) {
	month, _ := calendar.ParseYearMonth("2026-11")
	upd := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	mk := func(id, due, amt, cur string) occurrences.Occurrence {
		return occurrences.Occurrence{ID: id, CommitmentID: "c", Month: month, DueDate: date(t, due), Amount: mustMoney(t, amt, cur), Version: 1, UpdatedAt: upd}
	}
	occ := []occurrences.Occurrence{
		mk("o1", "2026-11-03", "0.10", "EUR"), // overdue, unpaid
		mk("o2", "2026-11-04", "0.20", "EUR"), // partially paid 0.05, overdue remainder
		mk("o3", "2026-11-25", "10.00", "EUR"),
		mk("o4", "2026-11-26", "5.00", "USD"),
	}
	f := facts{by: map[string]payments.Facts{
		"o2": {Evidence: []payments.Evidence{{Kind: payments.EvidenceManual, ID: "m", OccurrenceID: "o2", Amount: mustMoney(t, "0.05", "EUR"), Date: date(t, "2026-11-05")}}},
	}}
	s, err := monthview.Service{Occurrences: fakeOcc{items: occ}, Payments: f}.Month(context.Background(), month, date(t, "2026-11-10"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 4 || len(s.Totals) != 2 {
		t.Fatalf("rows %d totals %d", len(s.Rows), len(s.Totals))
	}
	eur, usd := s.Totals[0], s.Totals[1]
	if eur.Currency.Code() != "EUR" || usd.Currency.Code() != "USD" {
		t.Fatalf("order %v %v", eur.Currency, usd.Currency)
	}
	want := map[string]string{
		"due": eur.Due.String(), "paid": eur.Paid.String(), "outstanding": eur.Outstanding.String(), "overdue": eur.Overdue.String(), "overpaid": eur.Overpaid.String(),
	}
	exp := map[string]string{"due": "10.30 EUR", "paid": "0.05 EUR", "outstanding": "10.25 EUR", "overdue": "0.25 EUR", "overpaid": "0.00 EUR"}
	for k, v := range exp {
		if want[k] != v {
			t.Errorf("EUR %s = %s, want %s", k, want[k], v)
		}
	}
	if eur.OverdueCount != 2 || eur.Occurrences != 3 {
		t.Errorf("counts %d %d", eur.OverdueCount, eur.Occurrences)
	}
	if usd.Due.String() != "5.00 USD" || usd.Overdue.String() != "0.00 USD" || usd.Outstanding.String() != "5.00 USD" {
		t.Errorf("USD totals %v", usd)
	}
	if s.LatestChange == nil || !s.LatestChange.Equal(upd) {
		t.Errorf("latest change %v", s.LatestChange)
	}
}

func TestEmptyMonth(t *testing.T) {
	month, _ := calendar.ParseYearMonth("2026-11")
	s, err := monthview.Service{Occurrences: fakeOcc{}, Payments: facts{}}.Month(context.Background(), month, date(t, "2026-11-10"))
	if err != nil || len(s.Rows) != 0 || len(s.Totals) != 0 || s.LatestChange != nil || s.Rows == nil || s.Totals == nil {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestMonthReadsEveryPage(t *testing.T) {
	month, _ := calendar.ParseYearMonth("2026-11")
	var occ []occurrences.Occurrence
	for i := 0; i < 450; i++ {
		id := string(rune('a'+i/26/26%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i%26))
		occ = append(occ, occurrences.Occurrence{ID: id, Month: month, DueDate: date(t, "2026-11-20"), Amount: mustMoney(t, "1.00", "EUR")})
	}
	s, err := monthview.Service{Occurrences: fakeOcc{items: occ}, Payments: facts{}}.Month(context.Background(), month, date(t, "2026-11-10"))
	if err != nil || len(s.Rows) != 450 || s.Totals[0].Due.String() != "450.00 EUR" {
		t.Fatalf("%d %v %v", len(s.Rows), s.Totals, err)
	}
}

func TestOverridesAndOverpaymentInTotals(t *testing.T) {
	month, _ := calendar.ParseYearMonth("2026-11")
	mk := func(id, amt string) occurrences.Occurrence {
		return occurrences.Occurrence{ID: id, CommitmentID: "c", Month: month, DueDate: date(t, "2026-11-03"), Amount: mustMoney(t, amt, "EUR"), Version: 1}
	}
	ev := func(id, amt string) payments.Evidence {
		return payments.Evidence{Kind: payments.EvidenceManual, ID: "m" + id, OccurrenceID: id, Amount: mustMoney(t, amt, "EUR"), Date: date(t, "2026-11-05")}
	}
	ov := func(status payments.OverrideStatus) *payments.Override {
		return &payments.Override{Status: status, Reason: "test"}
	}
	occ := []occurrences.Occurrence{mk("o1", "10.00"), mk("o2", "10.00"), mk("o3", "10.00"), mk("o4", "10.00"), mk("o5", "10.00")}
	f := facts{by: map[string]payments.Facts{
		"o1": {Evidence: []payments.Evidence{ev("o1", "4.00")}},                                         // partly paid, overdue remainder
		"o2": {Evidence: []payments.Evidence{ev("o2", "4.00")}, Override: ov(payments.OverridePaid)},    // paid override on a partial payment
		"o3": {Evidence: []payments.Evidence{ev("o3", "10.00")}, Override: ov(payments.OverrideUnpaid)}, // unpaid override on a full payment
		"o4": {Evidence: []payments.Evidence{ev("o4", "15.00")}},                                        // overpaid by 5
		"o5": {Override: ov(payments.OverridePaid)},                                                     // paid override without evidence: not applied, still owed
	}}
	s, err := monthview.Service{Occurrences: fakeOcc{items: occ}, Payments: f}.Month(context.Background(), month, date(t, "2026-11-10"))
	if err != nil {
		t.Fatal(err)
	}
	got := s.Totals[0]
	exp := map[string][2]string{
		"due": {got.Due.String(), "50.00 EUR"}, "paid": {got.Paid.String(), "33.00 EUR"}, "outstanding": {got.Outstanding.String(), "26.00 EUR"},
		"overdue": {got.Overdue.String(), "26.00 EUR"}, "overpaid": {got.Overpaid.String(), "5.00 EUR"},
	}
	for k, v := range exp {
		if v[0] != v[1] {
			t.Errorf("%s = %s, want %s", k, v[0], v[1])
		}
	}
	if got.OverdueCount != 3 {
		t.Errorf("overdue count %d", got.OverdueCount)
	}
}

func TestMonthRefusesBeyondHorizonAndCurrentResolvesToday(t *testing.T) {
	far, _ := calendar.ParseYearMonth("2031-12")
	near, _ := calendar.ParseYearMonth("2031-11")
	svc := monthview.Service{Occurrences: fakeOcc{}, Payments: facts{}, Today: func() (calendar.Date, error) { return date(t, "2026-11-21"), nil }}
	if _, err := svc.Current(context.Background(), far); !errors.Is(err, occurrences.ErrBeyondHorizon) {
		t.Fatalf("err %v", err)
	}
	if _, err := svc.Month(context.Background(), far, date(t, "2026-11-21")); !errors.Is(err, occurrences.ErrBeyondHorizon) {
		t.Fatalf("err %v", err)
	}
	s, err := svc.Current(context.Background(), near)
	if err != nil || s.AsOf.String() != "2026-11-21" {
		t.Fatalf("%v %v", s.AsOf, err)
	}
}

// TestMonthTotalsLeaveOutNotRecordedAndCountToleranceAsPaid pins D38 1a and
// D39 5a in the totals: an occurrence due before its commitment was created
// is in Due but neither outstanding nor overdue; one paid within the tolerance
// has nothing outstanding.
func TestMonthTotalsLeaveOutNotRecordedAndCountToleranceAsPaid(t *testing.T) {
	month, _ := calendar.ParseYearMonth("2026-11")
	mk := func(id, due, amt string) occurrences.Occurrence {
		return occurrences.Occurrence{ID: id, CommitmentID: "c", Month: month, DueDate: date(t, due), Amount: mustMoney(t, amt, "EUR"), Version: 1}
	}
	occ := []occurrences.Occurrence{
		mk("o1", "2026-11-03", "100.00"), // created after: not recorded
		mk("o2", "2026-11-12", "20.00"),  // created on the day: overdue
		mk("o3", "2026-11-14", "30.00"),  // paid 29.50 by bank within tolerance 0.50
	}
	tolerance := mustMoney(t, "0.50", "EUR")
	f := facts{by: map[string]payments.Facts{
		"o1": {CommitmentCreatedOn: date(t, "2026-11-10")},
		"o2": {CommitmentCreatedOn: date(t, "2026-11-12")},
		"o3": {Evidence: []payments.Evidence{{Kind: payments.EvidenceBankTransaction, ID: "b", OccurrenceID: "o3", Amount: mustMoney(t, "29.50", "EUR"), Date: date(t, "2026-11-14"), ConfirmedWithin: &tolerance}}},
	}}
	s, err := monthview.Service{Occurrences: fakeOcc{items: occ}, Payments: f}.Month(context.Background(), month, date(t, "2026-11-20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 3 || s.Rows[0].State.Status != payments.StatusNotRecorded || s.Rows[1].State.Status != payments.StatusOverdue || s.Rows[2].State.Status != payments.StatusPaid {
		t.Fatalf("rows: %+v", s.Rows)
	}
	got := s.Totals[0]
	if got.Due.String() != "150.00 EUR" || got.Outstanding.String() != "20.00 EUR" || got.Overdue.String() != "20.00 EUR" || got.OverdueCount != 1 || got.Occurrences != 3 || got.Paid.String() != "29.50 EUR" {
		t.Fatalf("totals: %+v", got)
	}
}
