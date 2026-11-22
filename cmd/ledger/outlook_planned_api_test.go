package main

import (
	"strings"
	"testing"
	"time"
)

// TestOutlookFollowsDeferralPlans (task DF3, D-DF 8): a deferral moves an
// outstanding amount to the month it is planned in without changing what is
// still due, shows it apart as deferred in (or deferred beyond when it is planned
// after the window), and a fee still planned is added and not part of
// still due.
func TestOutlookFollowsDeferralPlans(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitmentAt(t, earlier, `{"name":"Gym","kind":"subscription","amount":{"amount":"50.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":15},"start_date":"2026-10-01"}`)
	oct := f.occurrences(t, "month=2026-10&commitment_id="+id, "alice").Items[0]

	figures := func(query string) string {
		o, raw := f.outlook(t, query, admin)
		if len(o.Currencies) != 1 || !o.PlannedOutflows.Included || !strings.Contains(o.PlannedOutflows.Note, "A plan is not payment") {
			t.Fatalf("outlook %s: %+v %s", query, o.PlannedOutflows, raw)
		}
		c := o.Currencies[0]
		p := c.PlannedOutflows
		return "due " + c.StillDue.String() + " overdue " + c.Overdue.String() + " | planned " + p.Total.String() + " in " + p.DeferredIn.String() + " beyond " + p.DeferredBeyond.String() + " fees " + p.Adjustments.String()
	}
	check := func(query, want string) {
		t.Helper()
		if got := figures(query); got != want {
			t.Errorf("%s:\n got %s\nwant %s", query, got, want)
		}
	}
	check("?through=2026-11", "due 100.00 EUR overdue 50.00 EUR | planned 100.00 EUR in 0.00 EUR beyond 0.00 EUR fees 0.00 EUR")

	rec := f.do("POST", "/api/v1/deferral-plans", `{"occurrence_id":"`+oct.ID+`","plan_type":"personal","planned_date":"2026-11-20"}`, admin)
	if rec.Code != 201 {
		t.Fatalf("defer: %d %s", rec.Code, rec.Body.String())
	}
	p := decodeDeferral(t, rec.Body.Bytes())
	// Deferred within the window: still due and overdue are unchanged, the amount is shown as deferred in.
	check("?through=2026-11", "due 100.00 EUR overdue 50.00 EUR | planned 100.00 EUR in 50.00 EUR beyond 0.00 EUR fees 0.00 EUR")
	// Planned after the window: still due keeps it, planned does not, and it is named as deferred out.
	check("", "due 50.00 EUR overdue 50.00 EUR | planned 0.00 EUR in 0.00 EUR beyond 50.00 EUR fees 0.00 EUR")

	rec = f.do("POST", "/api/v1/deferral-plans/"+p.ID+"/adjustments", `{"kind":"fee","amount":{"amount":"5.00","currency":"EUR"},"version":1}`, admin)
	if rec.Code != 201 {
		t.Fatalf("fee: %d %s", rec.Code, rec.Body.String())
	}
	p = decodeDeferral(t, rec.Body.Bytes())
	check("?through=2026-11", "due 100.00 EUR overdue 50.00 EUR | planned 105.00 EUR in 50.00 EUR beyond 0.00 EUR fees 5.00 EUR")
	// A fee marked paid is no longer planned.
	rec = f.do("POST", "/api/v1/deferral-plans/"+p.ID+"/adjustments/"+p.Adjustments[0].ID+"/paid", "", with(admin, "If-Match", `"2"`))
	if rec.Code != 200 {
		t.Fatalf("mark paid: %d %s", rec.Code, rec.Body.String())
	}
	check("?through=2026-11", "due 100.00 EUR overdue 50.00 EUR | planned 100.00 EUR in 50.00 EUR beyond 0.00 EUR fees 0.00 EUR")
}
