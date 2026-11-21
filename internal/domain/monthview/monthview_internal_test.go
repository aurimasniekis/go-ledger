package monthview

import (
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// TestTotalsOfSkipsCancelledRows: a cancelled row adds to no total or count,
// even if one reaches totalsOf. The listing never returns one (cancelled_at
// IS NULL), so this is the second line of defence.
func TestTotalsOfSkipsCancelledRows(t *testing.T) {
	month, _ := calendar.ParseYearMonth("2026-11")
	due, _ := calendar.ParseDate("2026-11-03")
	upd := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	eur := func(s string) money.Money {
		m, err := money.Parse(s, "EUR")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	row := func(id string, st payments.State) Row {
		return Row{Occurrence: occurrences.Occurrence{ID: id, CommitmentID: "c", Month: month, DueDate: due, Amount: eur("10.00"), Version: 1, UpdatedAt: upd}, State: st}
	}
	live := row("live", payments.State{Status: payments.StatusOverdue, Derived: payments.StatusOverdue, Timing: payments.StatusOverdue,
		Paid: eur("0.00"), Outstanding: eur("10.00"), Overpaid: eur("0.00")})
	cancelled := row("cancelled", payments.State{Status: payments.StatusCancelled, Derived: payments.StatusCancelled,
		Paid: eur("0.00"), Outstanding: eur("0.00"), Overpaid: eur("0.00")})

	totals, latest := totalsOf([]Row{live, cancelled})
	if len(totals) != 1 {
		t.Fatalf("totals %d, want 1 currency", len(totals))
	}
	got := totals[0]
	if got.Due.String() != "10.00 EUR" || got.Outstanding.String() != "10.00 EUR" || got.Overdue.String() != "10.00 EUR" {
		t.Errorf("totals include the cancelled row: due %s outstanding %s overdue %s", got.Due, got.Outstanding, got.Overdue)
	}
	if got.Occurrences != 1 || got.OverdueCount != 1 {
		t.Errorf("counts %d %d, want 1 1", got.Occurrences, got.OverdueCount)
	}
	if latest == nil || !latest.Equal(upd) {
		t.Errorf("latest change %v, want %v", latest, upd)
	}
}
