package payments_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
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

var occ = occurrences.Occurrence{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN0", CommitmentID: "01JD3Y4C8V9M2Q7W5R6T0XZBN1",
	DueDate: d("2026-03-15"), Amount: money.MustParse("50.00", "EUR"), Version: 1}

func ev(amount, currency string) payments.Evidence {
	return payments.Evidence{Kind: payments.EvidenceManual, ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN2", OccurrenceID: occ.ID,
		Amount: money.MustParse(amount, currency), Date: d("2026-03-10")}
}

func ov(s payments.OverrideStatus) *payments.Override {
	return &payments.Override{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN3", OccurrenceID: occ.ID, Status: s, Reason: "settled by phone"}
}

func TestDerive(t *testing.T) {
	cases := []struct {
		name        string
		facts       payments.Facts
		today       string
		status      payments.Status
		derived     payments.Status
		timing      payments.Status
		paid        string
		outstanding string
		unconverted string
		applied     bool
	}{
		{name: "no evidence before the due date", today: "2026-03-14", status: "upcoming", derived: "upcoming", timing: "upcoming", paid: "0.00 EUR", outstanding: "50.00 EUR"},
		{name: "no evidence on the due date", today: "2026-03-15", status: "due", derived: "due", timing: "due", paid: "0.00 EUR", outstanding: "50.00 EUR"},
		{name: "no evidence after the due date", today: "2026-03-16", status: "overdue", derived: "overdue", timing: "overdue", paid: "0.00 EUR", outstanding: "50.00 EUR"},
		{name: "paid in full", facts: payments.Facts{Evidence: []payments.Evidence{ev("50", "EUR")}}, today: "2026-03-16",
			status: "paid", derived: "paid", paid: "50.00 EUR", outstanding: "0.00 EUR"},
		{name: "paid in parts", facts: payments.Facts{Evidence: []payments.Evidence{ev("20.00", "EUR"), ev("30.00", "EUR")}}, today: "2026-04-01",
			status: "paid", derived: "paid", paid: "50.00 EUR", outstanding: "0.00 EUR"},
		{name: "overpaid", facts: payments.Facts{Evidence: []payments.Evidence{ev("60.005", "EUR")}}, today: "2026-03-01",
			status: "paid", derived: "paid", paid: "60.005 EUR", outstanding: "0.00 EUR"},
		{name: "partially paid, remainder overdue", facts: payments.Facts{Evidence: []payments.Evidence{ev("20.00", "EUR")}}, today: "2026-03-20",
			status: "partially_paid", derived: "partially_paid", timing: "overdue", paid: "20.00 EUR", outstanding: "30.00 EUR"},
		{name: "partially paid, remainder upcoming", facts: payments.Facts{Evidence: []payments.Evidence{ev("0.01", "EUR")}}, today: "2026-03-01",
			status: "partially_paid", derived: "partially_paid", timing: "upcoming", paid: "0.01 EUR", outstanding: "49.99 EUR"},
		{name: "other currency is flagged, never counted", facts: payments.Facts{Evidence: []payments.Evidence{ev("80", "USD"), ev("10.00", "EUR"), ev("1", "GBP"), ev("5", "USD")}}, today: "2026-03-20",
			status: "partially_paid", derived: "partially_paid", timing: "overdue", paid: "10.00 EUR", outstanding: "40.00 EUR", unconverted: "GBP,USD"},
		{name: "only other currency", facts: payments.Facts{Evidence: []payments.Evidence{ev("60", "USD")}}, today: "2026-03-20",
			status: "overdue", derived: "overdue", timing: "overdue", paid: "0.00 EUR", outstanding: "50.00 EUR", unconverted: "USD"},
		{name: "paid override with other-currency evidence", facts: payments.Facts{Evidence: []payments.Evidence{ev("60", "USD")}, Override: ov(payments.OverridePaid)}, today: "2026-03-20",
			status: "paid", derived: "overdue", paid: "0.00 EUR", outstanding: "0.00 EUR", unconverted: "USD", applied: true},
		{name: "paid override with partial evidence", facts: payments.Facts{Evidence: []payments.Evidence{ev("45", "EUR")}, Override: ov(payments.OverridePaid)}, today: "2026-03-20",
			status: "paid", derived: "partially_paid", paid: "45.00 EUR", outstanding: "0.00 EUR", applied: true},
		{name: "paid override without evidence has no effect", facts: payments.Facts{Override: ov(payments.OverridePaid)}, today: "2026-03-20",
			status: "overdue", derived: "overdue", timing: "overdue", paid: "0.00 EUR", outstanding: "50.00 EUR"},
		{name: "unpaid override over full evidence", facts: payments.Facts{Evidence: []payments.Evidence{ev("50.00", "EUR")}, Override: ov(payments.OverrideUnpaid)}, today: "2026-03-15",
			status: "due", derived: "paid", timing: "due", paid: "50.00 EUR", outstanding: "50.00 EUR", applied: true},
		{name: "unpaid override over full evidence when overdue", facts: payments.Facts{Evidence: []payments.Evidence{ev("50.00", "EUR")}, Override: ov(payments.OverrideUnpaid)}, today: "2026-03-20",
			status: "overdue", derived: "paid", timing: "overdue", paid: "50.00 EUR", outstanding: "50.00 EUR", applied: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := payments.Derive(occ, c.facts, d(c.today), nil)
			var un []string
			for _, cur := range st.Unconverted {
				un = append(un, cur.Code())
			}
			got := []string{string(st.Status), string(st.Derived), string(st.Timing), st.Paid.String(), st.Outstanding.String(), strings.Join(un, ",")}
			want := []string{string(c.status), string(c.derived), string(c.timing), c.paid, c.outstanding, c.unconverted}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("got  %v\nwant %v", got, want)
			}
			if st.OverrideApplied != c.applied {
				t.Errorf("applied = %v, want %v", st.OverrideApplied, c.applied)
			}
			if st.EvidenceCount != len(c.facts.Evidence) {
				t.Errorf("evidence count = %d", st.EvidenceCount)
			}
		})
	}
}

// TestDeriveOutstandingAndOverpaid: outstanding is never zero while the status
// is not paid, and an overpayment is visible without changing the status.
func TestDeriveOutstandingAndOverpaid(t *testing.T) {
	st := payments.Derive(occ, payments.Facts{Evidence: []payments.Evidence{ev("50.00", "EUR"), ev("9.995", "EUR")}}, d("2026-03-20"), nil)
	if st.Status != payments.StatusPaid || st.Overpaid.String() != "9.995 EUR" || st.Outstanding.Sign() != 0 {
		t.Errorf("overpaid: status=%s overpaid=%s outstanding=%s", st.Status, st.Overpaid, st.Outstanding)
	}
	for _, f := range []payments.Facts{
		{},
		{Evidence: []payments.Evidence{ev("50.00", "EUR")}, Override: ov(payments.OverrideUnpaid)},
		{Evidence: []payments.Evidence{ev("60.00", "EUR")}, Override: ov(payments.OverrideUnpaid)},
		{Evidence: []payments.Evidence{ev("20.00", "EUR")}},
	} {
		st := payments.Derive(occ, f, d("2026-03-20"), nil)
		if st.Status != payments.StatusPaid && st.Outstanding.Sign() <= 0 {
			t.Errorf("status %s with outstanding %s", st.Status, st.Outstanding)
		}
	}
	if st := payments.Derive(occ, payments.Facts{Evidence: []payments.Evidence{ev("20.00", "EUR")}}, d("2026-03-20"), nil); st.Overpaid.Sign() != 0 {
		t.Errorf("overpaid = %s", st.Overpaid)
	}
}

// TestDeriveNeverPaysWithoutEvidence: whatever the override and date, an
// occurrence with no evidence is never paid or partially paid.
func TestDeriveNeverPaysWithoutEvidence(t *testing.T) {
	for _, o := range []*payments.Override{nil, ov(payments.OverridePaid), ov(payments.OverrideUnpaid)} {
		for _, today := range []string{"2026-01-01", "2026-03-15", "2026-12-31"} {
			st := payments.Derive(occ, payments.Facts{Override: o}, d(today), nil)
			if st.Status == payments.StatusPaid || st.Status == payments.StatusPartiallyPaid || st.Paid.Sign() != 0 {
				t.Errorf("override %v, today %s: status %s, paid %s", o, today, st.Status, st.Paid)
			}
		}
	}
}

func TestGraceAnnotatesButNeverHidesOverdue(t *testing.T) {
	grace := func(o occurrences.Occurrence) (calendar.Date, bool) { return d("2026-03-25"), o.ID == occ.ID }
	for _, c := range []struct {
		today  string
		status payments.Status
		until  string
		within bool
	}{
		{"2026-03-10", "upcoming", "", false}, // not overdue: no annotation
		{"2026-03-16", "overdue", "2026-03-25", true},
		{"2026-03-25", "overdue", "2026-03-25", true},
		{"2026-03-26", "overdue", "2026-03-25", false},
	} {
		st := payments.Derive(occ, payments.Facts{}, d(c.today), grace)
		until := ""
		if st.GraceUntil != nil {
			until = st.GraceUntil.String()
		}
		if st.Status != c.status || until != c.until || st.WithinGrace != c.within {
			t.Errorf("today %s: %s until %q within %v; want %s %q %v", c.today, st.Status, until, st.WithinGrace, c.status, c.until, c.within)
		}
	}
	// A paid occurrence carries no grace annotation.
	if st := payments.Derive(occ, payments.Facts{Evidence: []payments.Evidence{ev("50", "EUR")}}, d("2026-03-20"), grace); st.GraceUntil != nil {
		t.Errorf("paid occurrence annotated with grace")
	}
	// No hook, no grace: there is no default length.
	if st := payments.Derive(occ, payments.Facts{}, d("2026-03-20"), nil); st.GraceUntil != nil || st.WithinGrace {
		t.Errorf("grace without a hook")
	}
}

func TestValidateManual(t *testing.T) {
	today := d("2026-03-20")
	note := func(s string) *string { return &s }
	cases := []struct {
		in   payments.NewManual
		want string
	}{
		{payments.NewManual{Amount: money.MustParse("10.00", "EUR"), PaidOn: today}, ""},
		{payments.NewManual{Amount: money.MustParse("10.00", "EUR"), PaidOn: d("2026-03-01"), Note: note("cash")}, ""},
		{payments.NewManual{Amount: money.MustParse("10.00", "USD"), PaidOn: today}, "amount.currency"},
		{payments.NewManual{Amount: money.MustParse("0.00", "EUR"), PaidOn: today}, "amount.amount"},
		{payments.NewManual{Amount: money.MustParse("-1", "EUR"), PaidOn: today}, "amount.amount"},
		{payments.NewManual{PaidOn: today}, "amount"},
		{payments.NewManual{Amount: money.MustParse("1", "EUR"), PaidOn: d("2026-03-21")}, "paid_on"},
		{payments.NewManual{Amount: money.MustParse("1", "EUR")}, "paid_on"},
		{payments.NewManual{Amount: money.MustParse("1", "EUR"), PaidOn: d("1899-12-31")}, "paid_on"},
		{payments.NewManual{Amount: money.MustParse("1", "EUR"), PaidOn: today, Note: note("  ")}, "note"},
		{payments.NewManual{Amount: money.MustParse("1", "EUR"), PaidOn: today, Note: note(strings.Repeat("x", 2001))}, "note"},
	}
	for i, c := range cases {
		var fields []string
		for _, e := range c.in.Validate(occ, today) {
			fields = append(fields, e.Field)
		}
		if strings.Join(fields, ",") != c.want {
			t.Errorf("case %d: fields %v, want %q", i, fields, c.want)
		}
	}
}

func TestValidateOverride(t *testing.T) {
	if errs := payments.ValidateOverride(payments.OverridePaid, "bank transfer from another account"); len(errs) != 0 {
		t.Errorf("valid override refused: %v", errs)
	}
	if errs := payments.ValidateOverride("waived", ""); len(errs) != 2 {
		t.Errorf("want status and reason errors, got %v", errs)
	}
	if errs := payments.ValidateOverride(payments.OverrideUnpaid, strings.Repeat("y", payments.MaxReason+1)); len(errs) != 1 || errs[0].Field != "reason" {
		t.Errorf("long reason: %v", errs)
	}
}

func TestEventsAreValidAudit(t *testing.T) {
	at := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	actor := audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}
	p := payments.ManualPayment{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN2", OccurrenceID: occ.ID, Amount: money.MustParse("12.50", "EUR")}
	paid := payments.OverridePaid
	for _, e := range []audit.Event{
		payments.RecordedEvent(actor, p, at),
		payments.DeletedEvent(actor, p, at),
		payments.OverriddenEvent(actor, occ.ID, payments.OverrideUnpaid, nil, at),
		payments.OverriddenEvent(actor, occ.ID, payments.OverrideUnpaid, &paid, at),
		payments.UndoneEvent(actor, occ.ID, payments.OverrideUnpaid, &paid, at),
		payments.UndoneEvent(actor, occ.ID, payments.OverridePaid, nil, at),
	} {
		if err := audit.Validate(e); err != nil {
			t.Errorf("%s: %v", e.Action, err)
		}
		if e.TargetType != "occurrence" || e.TargetID != occ.ID {
			t.Errorf("%s: target %s/%s", e.Action, e.TargetType, e.TargetID)
		}
		for k, v := range e.Details {
			if strings.Contains(v, "12.5") {
				t.Errorf("%s: detail %s carries the amount", e.Action, k)
			}
		}
	}
}
