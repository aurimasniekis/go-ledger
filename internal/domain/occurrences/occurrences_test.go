package occurrences

import (
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func d(s string) calendar.Date {
	v, err := calendar.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func ym(s string) calendar.YearMonth {
	v, err := calendar.ParseYearMonth(s)
	if err != nil {
		panic(err)
	}
	return v
}

func ptr[T any](v T) *T { return &v }

func terms(s commitments.Schedule, start string) Terms {
	return Terms{Amount: money.MustParse("10.00", "EUR"), Schedule: s, Start: d(start)}
}

// dates lists the due dates generated from from through to.
func dates(t Terms, from, to string) string {
	var out []string
	for _, p := range Generate(t, ym(from), ym(to)) {
		if p.Month != p.DueDate.YearMonth() {
			panic("due date outside its month")
		}
		out = append(out, p.DueDate.String())
	}
	return strings.Join(out, " ")
}

func TestMonthEndsClamp(t *testing.T) {
	got := dates(terms(commitments.Monthly(1, 31), "2024-01-01"), "2024-01", "2024-06")
	if want := "2024-01-31 2024-02-29 2024-03-31 2024-04-30 2024-05-31 2024-06-30"; got != want {
		t.Errorf("day 31 in a leap year:\n got %s\nwant %s", got, want)
	}
	got = dates(terms(commitments.Monthly(1, 30), "2025-01-01"), "2025-01", "2025-03")
	if want := "2025-01-30 2025-02-28 2025-03-30"; got != want {
		t.Errorf("day 30 in a common year:\n got %s\nwant %s", got, want)
	}
	got = dates(terms(commitments.Monthly(1, 29), "2100-01-01"), "2100-02", "2100-02")
	if want := "2100-02-28"; got != want { // 2100 is not a leap year
		t.Errorf("day 29 in February 2100: got %s want %s", got, want)
	}
}

func TestFebruary29Annual(t *testing.T) {
	got := dates(terms(commitments.Annual(time.February, 29), "2024-02-29"), "2024-01", "2029-12")
	if want := "2024-02-29 2025-02-28 2026-02-28 2027-02-28 2028-02-29 2029-02-28"; got != want {
		t.Errorf("annual Feb 29:\n got %s\nwant %s", got, want)
	}
	// Starting on Feb 28 of a common year still gets that year's renewal.
	got = dates(terms(commitments.Annual(time.February, 29), "2025-02-28"), "2025-01", "2028-12")
	if want := "2025-02-28 2026-02-28 2027-02-28 2028-02-29"; got != want {
		t.Errorf("annual Feb 29 from 2025-02-28:\n got %s\nwant %s", got, want)
	}
}

func TestAnnualRenewals(t *testing.T) {
	tm := terms(commitments.Annual(time.November, 15), "2026-03-01")
	tm.End = ptr(d("2029-11-14"))
	if got, want := dates(tm, "2026-01", "2031-12"), "2026-11-15 2027-11-15 2028-11-15"; got != want {
		t.Errorf("annual with end before the 2029 renewal:\n got %s\nwant %s", got, want)
	}
	tm.End = ptr(d("2029-11-15")) // the end date is inclusive
	if got, want := dates(tm, "2029-01", "2031-12"), "2029-11-15"; got != want {
		t.Errorf("inclusive end: got %s want %s", got, want)
	}
	for _, m := range []string{"2027-10", "2027-12"} {
		if _, ok := tm.DueIn(ym(m)); ok {
			t.Errorf("annual November schedule due in %s", m)
		}
	}
}

func TestFirstOccurrence(t *testing.T) {
	cases := []struct {
		name  string
		terms Terms
		want  string
	}{
		{"start after the scheduled day skips the start month", terms(commitments.Monthly(1, 15), "2026-01-20"), "2026-02-15 2026-03-15 2026-04-15"},
		{"start on the scheduled day counts", terms(commitments.Monthly(1, 15), "2026-01-15"), "2026-01-15 2026-02-15 2026-03-15 2026-04-15"},
		{"start before the scheduled day counts", terms(commitments.Monthly(1, 15), "2026-01-02"), "2026-01-15 2026-02-15 2026-03-15 2026-04-15"},
		{"interval counts from the start month", terms(commitments.Monthly(3, 15), "2026-01-20"), "2026-04-15"},
		{"clamped start-month date on the start date", terms(commitments.Monthly(1, 31), "2026-02-28"), "2026-02-28 2026-03-31 2026-04-30"},
		{"annual after this year's date waits a year", terms(commitments.Annual(time.January, 10), "2026-01-11"), ""},
	}
	for _, c := range cases {
		if got := dates(c.terms, "2025-12", "2026-04"); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestIntervalMonths(t *testing.T) {
	got := dates(terms(commitments.Monthly(3, 1), "2026-01-01"), "2026-01", "2027-01")
	if want := "2026-01-01 2026-04-01 2026-07-01 2026-10-01 2027-01-01"; got != want {
		t.Errorf("quarterly:\n got %s\nwant %s", got, want)
	}
	if got := dates(terms(commitments.Monthly(1, 1), "2026-05-01"), "2026-01", "2026-04"); got != "" {
		t.Errorf("before the start: %s", got)
	}
}

func TestTermsEqual(t *testing.T) {
	a := terms(commitments.Monthly(1, 5), "2026-01-01")
	b := a
	b.Amount = money.MustParse("10.0", "EUR")
	if a.Equal(b) {
		t.Error("amounts with a different scale are different source values")
	}
	b = a
	b.End = ptr(d("2027-01-01"))
	if a.Equal(b) || !b.Equal(b) {
		t.Error("end date comparison")
	}
}

func occ(id, due, amount string, paid bool) Existing {
	dd := d(due)
	return Existing{Occurrence: Occurrence{ID: id, Month: dd.YearMonth(), DueDate: dd, Amount: money.MustParse(amount, "EUR")}, Paid: paid}
}

func summary(p Plan) string {
	var out []string
	for _, c := range p.Create {
		out = append(out, "+"+c.DueDate.String()+"@"+c.Amount.Amount().String())
	}
	for _, u := range p.Update {
		out = append(out, "~"+u.ID+">"+u.DueDate.String()+"@"+u.Amount.Amount().String())
	}
	for _, id := range p.Remove {
		out = append(out, "-"+id)
	}
	return strings.Join(out, " ")
}

func TestPlanEdit(t *testing.T) {
	old := terms(commitments.Monthly(1, 10), "2026-01-01")
	// Jan..Jun materialised; Feb and May paid.
	existing := []Existing{
		occ("jan", "2026-01-10", "10.00", false),
		occ("feb", "2026-02-10", "10.00", true),
		occ("mar", "2026-03-10", "10.00", false),
		occ("apr", "2026-04-10", "10.00", false),
		occ("may", "2026-05-10", "10.00", true),
		occ("jun", "2026-06-10", "10.00", false),
	}
	in := func(from string) []Existing { // what the store passes: months from the effective month
		var out []Existing
		for _, e := range existing {
			if e.Month.Compare(ym(from)) >= 0 {
				out = append(out, e)
			}
		}
		return out
	}
	t.Run("amount from mid-March: overdue and paid stay", func(t *testing.T) {
		next := old
		next.Amount = money.MustParse("12.00", "EUR")
		p := PlanEdit(in("2026-03"), ym("2026-06"), next, d("2026-03-15"))
		if got, want := summary(p), "~apr>2026-04-10@12.00 ~jun>2026-06-10@12.00"; got != want {
			t.Errorf("got %s want %s", got, want)
		}
		if p.KeptPaid != 1 || p.KeptHistory != 1 { // May paid; March due before the 15th
			t.Errorf("kept paid %d history %d", p.KeptPaid, p.KeptHistory)
		}
	})
	t.Run("an earlier due day rewrites the month in place", func(t *testing.T) {
		next := old
		next.Schedule = commitments.Monthly(1, 5)
		p := PlanEdit(in("2026-04"), ym("2026-06"), next, d("2026-04-08"))
		// April's unpaid occurrence (10th) is on/after the 8th, so it moves to
		// the 5th rather than leaving April empty.
		if got, want := summary(p), "~apr>2026-04-05@10.00 ~jun>2026-06-05@10.00"; got != want {
			t.Errorf("got %s want %s", got, want)
		}
	})
	t.Run("a later due day keeps the earlier month's history", func(t *testing.T) {
		next := old
		next.Schedule = commitments.Monthly(1, 20)
		p := PlanEdit(in("2026-03"), ym("2026-06"), next, d("2026-03-15"))
		// March 10 is before the effective date: it stays, and March gets no second occurrence.
		if got, want := summary(p), "~apr>2026-04-20@10.00 ~jun>2026-06-20@10.00"; got != want {
			t.Errorf("got %s want %s", got, want)
		}
	})
	t.Run("an end date removes only unpaid future occurrences", func(t *testing.T) {
		next := old
		next.End = ptr(d("2026-03-31"))
		p := PlanEdit(in("2026-03"), ym("2026-06"), next, d("2026-03-01"))
		if got, want := summary(p), "-apr -jun"; got != want {
			t.Errorf("got %s want %s", got, want)
		}
		if p.KeptPaid != 1 {
			t.Errorf("paid May must stay: %+v", p)
		}
	})
	t.Run("a longer interval removes and a new start adds", func(t *testing.T) {
		next := old
		next.Schedule = commitments.Monthly(2, 10) // Jan, Mar, May from a January start
		p := PlanEdit(in("2026-03"), ym("2026-06"), next, d("2026-03-01"))
		if got, want := summary(p), "-apr -jun"; got != want {
			t.Errorf("got %s want %s", got, want)
		}
		next.Start = d("2025-12-01") // now Dec, Feb, Apr, Jun
		p = PlanEdit(in("2026-03"), ym("2026-06"), next, d("2026-03-01"))
		if got, want := summary(p), "-mar"; got != want { // Apr and Jun stay unchanged, May is paid
			t.Errorf("got %s want %s", got, want)
		}
	})
	t.Run("same terms change nothing", func(t *testing.T) {
		if p := PlanEdit(in("2026-01"), ym("2026-06"), old, d("2026-01-01")); !p.Empty() {
			t.Errorf("idempotent plan: %s", summary(p))
		}
	})
	t.Run("empty months get new occurrences on or after the effective date", func(t *testing.T) {
		next := old
		next.Schedule = commitments.Monthly(1, 10)
		p := PlanEdit(nil, ym("2026-08"), next, d("2026-07-11"))
		if got, want := summary(p), "+2026-08-10@10.00"; got != want { // July 10 is before the effective date
			t.Errorf("got %s want %s", got, want)
		}
	})
}

func TestHorizonLimit(t *testing.T) {
	if got := HorizonLimit(d("2026-11-21")).String(); got != "2031-11" {
		t.Errorf("limit %s", got)
	}
	if got := HorizonLimit(d("9998-01-01")).String(); got != "9999-12" {
		t.Errorf("limit near the end of the calendar %s", got)
	}
}

func TestRegeneratedEventIsAllowed(t *testing.T) {
	p := Plan{Create: make([]Planned, 2), Remove: []string{"x"}, KeptPaid: 3}
	e := RegeneratedEvent(audit.System, "01JD3Y4C8V9M2Q7W5R6T0XZBNA", d("2026-03-15"), p, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if err := audit.Validate(e); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"effective_from": "2026-03-15", "created": "2", "updated": "0", "removed": "1", "kept_paid": "3"}
	for k, v := range want {
		if e.Details[k] != v {
			t.Errorf("%s = %q, want %q", k, e.Details[k], v)
		}
	}
}
