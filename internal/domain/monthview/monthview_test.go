package monthview_test

import (
	"context"
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
