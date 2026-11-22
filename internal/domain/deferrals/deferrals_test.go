package deferrals_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

func d(s string) calendar.Date {
	v, err := calendar.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func occ() occurrences.Occurrence {
	return occurrences.Occurrence{ID: "o", Month: d("2026-10-15").YearMonth(), DueDate: d("2026-10-15"), Amount: money.MustParse("50.00", "EUR")}
}

func state(o occurrences.Occurrence, paid string, today calendar.Date) payments.State {
	var f payments.Facts
	if paid != "" {
		f.Evidence = []payments.Evidence{{Kind: payments.EvidenceManual, ID: "m", OccurrenceID: o.ID, Amount: money.MustParse(paid, "EUR"), Date: today}}
	}
	return payments.Derive(o, f, today, nil)
}

func TestCheckOccurrence(t *testing.T) {
	today := d("2026-10-20")
	o := occ()
	if err := deferrals.CheckOccurrence(o, state(o, "", today), d("2026-11-20"), today); err != nil {
		t.Errorf("cross-month: %v", err)
	}
	if err := deferrals.CheckOccurrence(o, state(o, "", today), d("2026-10-16"), today); err != nil {
		t.Errorf("within month: %v", err)
	}
	if err := deferrals.CheckOccurrence(o, state(o, "20.00", today), d("2026-11-20"), today); err != nil {
		t.Errorf("partially paid can be deferred: %v", err)
	}
	if err := deferrals.CheckOccurrence(o, state(o, "50.00", today), d("2026-11-20"), today); !errors.Is(err, deferrals.ErrNothingOutstanding) {
		t.Errorf("paid: %v", err)
	}
	var ve *commitments.ValidationError
	for _, planned := range []string{"2026-10-15", "2026-10-01", "2031-11-01"} {
		if err := deferrals.CheckOccurrence(o, state(o, "", today), d(planned), today); !errors.As(err, &ve) || ve.Errors[0].Field != "planned_date" {
			t.Errorf("planned %s: %v", planned, err)
		}
	}
	notRecorded := payments.Derive(o, payments.Facts{CommitmentCreatedOn: d("2026-10-18")}, today, nil)
	if err := deferrals.CheckOccurrence(o, notRecorded, d("2026-11-20"), today); !errors.Is(err, deferrals.ErrNothingOutstanding) {
		t.Errorf("not recorded: %v", err)
	}
	at := time.Now()
	c := o
	c.CancelledAt = &at
	if err := deferrals.CheckOccurrence(c, payments.Derive(c, payments.Facts{}, today, nil), d("2026-11-20"), today); !errors.Is(err, occurrences.ErrCancelled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestFieldsAndAdjustmentValidation(t *testing.T) {
	blank, long := " ", string(make([]rune, deferrals.MaxReason+1))
	if errs := (deferrals.Fields{Type: "maybe", Reason: &blank}).Validate(); len(errs) != 3 {
		t.Errorf("fields: %v", errs)
	}
	if errs := (deferrals.Fields{Type: deferrals.TypeCreditorAgreed, PlannedDate: d("2026-11-01"), Reason: &long}).Validate(); len(errs) != 1 || errs[0].Field != "reason" {
		t.Errorf("long reason: %v", errs)
	}
	if errs := (deferrals.Fields{Type: deferrals.TypeDocumentedGrace, PlannedDate: d("2026-11-01")}).Validate(); len(errs) != 0 {
		t.Errorf("valid fields: %v", errs)
	}
	eur := money.MustParse("1", "EUR").Currency()
	for _, c := range []struct {
		in    deferrals.NewAdjustment
		field string
	}{
		{deferrals.NewAdjustment{Kind: "penalty", Amount: money.MustParse("1.00", "EUR")}, "kind"},
		{deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee}, "amount"},
		{deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee, Amount: money.MustParse("1.00", "USD")}, "amount.currency"},
		{deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee, Amount: money.MustParse("0", "EUR")}, "amount.amount"},
		{deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee, Amount: money.MustParse("-1", "EUR")}, "amount.amount"},
	} {
		if errs := c.in.Validate(eur); len(errs) != 1 || errs[0].Field != c.field {
			t.Errorf("%+v: %v", c.in, errs)
		}
	}
}

func TestPlannedMonthAndTotals(t *testing.T) {
	o := occ()
	p := deferrals.Plan{Type: deferrals.TypePersonal, PlannedDate: d("2026-11-20"), OriginalAmount: o.Amount}
	if got := deferrals.PlannedMonth(o, &p); got.String() != "2026-11" {
		t.Errorf("active plan: %s", got)
	}
	if got := deferrals.PlannedMonth(o, nil); got != o.Month {
		t.Errorf("no plan: %s", got)
	}
	at := time.Now()
	undone := p
	undone.UndoneAt = &at
	if got := deferrals.PlannedMonth(o, &undone); got != o.Month || undone.State() != deferrals.StateUndone {
		t.Errorf("undone plan: %s", got)
	}
	if _, ok := p.GraceUntil(); ok {
		t.Error("a personal plan is not a documented grace period")
	}
	g := p
	g.Type = deferrals.TypeDocumentedGrace
	if until, ok := g.GraceUntil(); !ok || until != d("2026-11-20") {
		t.Errorf("grace until %v %v", until, ok)
	}
	if _, ok := undone.GraceUntil(); ok {
		t.Error("an undone plan documents no grace")
	}
	p.Adjustments = []deferrals.Adjustment{{Amount: money.MustParse("1.10", "EUR")}, {Amount: money.MustParse("2.005", "EUR")}, {Amount: money.MustParse("9", "EUR"), RemovedAt: &at}}
	if got := p.AdjustmentTotal().String(); got != "3.105 EUR" {
		t.Errorf("adjustment total %s, want the exact sum of the live adjustments", got)
	}
	if got := (deferrals.Plan{OriginalAmount: o.Amount}).AdjustmentTotal().String(); got != "0.00 EUR" {
		t.Errorf("no adjustments: %s", got)
	}
}
