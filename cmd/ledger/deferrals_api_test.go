package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type deferralResp struct {
	ID                     string                  `json:"id"`
	OccurrenceID           string                  `json:"occurrence_id"`
	PlanType               string                  `json:"plan_type"`
	State                  string                  `json:"state"`
	PlannedDate            string                  `json:"planned_date"`
	PlannedMonth           string                  `json:"planned_month"`
	Reason                 *string                 `json:"reason"`
	OriginalDueDate        string                  `json:"original_due_date"`
	OccurrenceStatus       string                  `json:"occurrence_status"`
	OverdueSince           *string                 `json:"overdue_since"`
	GraceUntil             *string                 `json:"grace_until"`
	WithinGrace            bool                    `json:"within_grace"`
	OutstandingAmount      struct{ Amount string } `json:"outstanding_amount"`
	OutstandingWhenPlanned struct{ Amount string } `json:"outstanding_when_planned"`
	AdjustmentTotal        struct{ Amount string } `json:"adjustment_total"`
	Adjustments            []struct {
		ID        string  `json:"id"`
		RemovedAt *string `json:"removed_at"`
	} `json:"adjustments"`
	Version int64 `json:"version"`
}

type monthPlanned struct {
	Items []struct {
		ID       string        `json:"id"`
		Deferral *deferralResp `json:"deferral"`
		Payment  struct {
			Status string `json:"status"`
		} `json:"payment"`
	} `json:"items"`
	CarriedIn []struct {
		ID       string        `json:"id"`
		DueDate  string        `json:"due_date"`
		Deferral *deferralResp `json:"deferral"`
	} `json:"carried_in"`
	Totals []struct {
		Currency    string                  `json:"currency"`
		Outstanding struct{ Amount string } `json:"outstanding"`
		Overdue     struct{ Amount string } `json:"overdue"`
		Planned     struct{ Amount string } `json:"planned"`
		DeferredOut struct{ Amount string } `json:"deferred_out"`
		CarriedIn   struct{ Amount string } `json:"carried_in"`
		Adjustments struct{ Amount string } `json:"adjustments"`
	} `json:"totals"`
}

func decodeDeferral(t *testing.T, body []byte) deferralResp {
	t.Helper()
	var d deferralResp
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return d
}

func (f *apiFixture) monthPlanned(t *testing.T, month string) monthPlanned {
	t.Helper()
	rec := f.do("GET", "/api/v1/occurrences/summary?month="+month, "", map[string]string{"X-Test-User": f.ids["bob"]})
	if rec.Code != 200 {
		t.Fatalf("summary %s: %d %s", month, rec.Code, rec.Body.String())
	}
	var m monthPlanned
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestDeferralPlanAPI walks the deferral journey over HTTP: two outstanding
// 50 EUR occurrences, the first deferred into the second's month (planned
// total 100, nothing paid), a revision, an explicit fee, the history, undo,
// and the refusals and grants on the way.
func TestDeferralPlanAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	reader := map[string]string{"X-Test-User": f.ids["bob"]}
	id := f.createCommitmentAt(t, earlier, `{"name":"Gym","kind":"subscription","amount":{"amount":"50.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":15},"start_date":"2026-01-01"}`)
	oct := f.occurrences(t, "month=2026-10&commitment_id="+id, "alice").Items[0]
	occBefore := f.getOccurrence(t, oct.ID)

	// Refusals before anything is stored.
	for _, c := range []struct{ body, field string }{
		{`{"occurrence_id":"` + oct.ID + `","plan_type":"later","planned_date":"2026-11-20"}`, "plan_type"},
		{`{"occurrence_id":"` + oct.ID + `","plan_type":"personal","planned_date":"2026-10-15"}`, "planned_date"},
		{`{"occurrence_id":"` + oct.ID + `","plan_type":"personal"}`, "planned_date"},
		{`{"occurrence_id":"01JD3Y4C8V9M2Q7W5R6T0XZBNZ","plan_type":"personal","planned_date":"2026-11-20"}`, "occurrence_id"},
		{`{"occurrence_id":"` + oct.ID + `","plan_type":"personal","planned_date":"2026-11-20","paid":true}`, "paid"},
	} {
		rec := f.do("POST", "/api/v1/deferral-plans", c.body, admin)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"`+c.field+`"`) {
			t.Errorf("%s: %d %s", c.body, rec.Code, rec.Body.String())
		}
	}
	body := `{"occurrence_id":"` + oct.ID + `","plan_type":"personal","planned_date":"2026-11-20","reason":"salary comes late"}`
	if rec := f.do("POST", "/api/v1/deferral-plans", body, reader); rec.Code != 403 {
		t.Fatalf("read-only user deferred: %d", rec.Code)
	}
	rec := f.do("POST", "/api/v1/deferral-plans", body, admin)
	if rec.Code != 201 || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v1/deferral-plans/") || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	p := decodeDeferral(t, rec.Body.Bytes())
	if p.State != "active" || p.PlannedMonth != "2026-11" || p.OriginalDueDate != "2026-10-15" || p.OccurrenceStatus != "overdue" ||
		p.OverdueSince == nil || *p.OverdueSince != "2026-10-15" || p.OutstandingAmount.Amount != "50.00" || p.GraceUntil != nil {
		t.Fatalf("created plan: %+v", p)
	}
	if rec := f.do("POST", "/api/v1/deferral-plans", body, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "deferral_plan_exists") {
		t.Errorf("second plan: %d %s", rec.Code, rec.Body.String())
	}
	// Deferral never marks paid and never touches the occurrence.
	if o := f.getOccurrence(t, oct.ID); o.Payment.Status != "overdue" || o.Version != occBefore.Version || o.Payment.EvidenceCount != 0 {
		t.Fatalf("occurrence after deferral: %+v", o)
	}
	m := f.monthPlanned(t, "2026-11")
	if len(m.Totals) != 1 || m.Totals[0].Planned.Amount != "100.00" || m.Totals[0].Outstanding.Amount != "50.00" || m.Totals[0].CarriedIn.Amount != "50.00" {
		t.Fatalf("November totals: %+v", m.Totals)
	}
	if len(m.CarriedIn) != 1 || m.CarriedIn[0].ID != oct.ID || m.CarriedIn[0].DueDate != "2026-10-15" || m.CarriedIn[0].Deferral == nil || len(m.Items) != 1 || m.Items[0].ID == oct.ID {
		t.Fatalf("November rows: %+v carried %+v", m.Items, m.CarriedIn)
	}
	mo := f.monthPlanned(t, "2026-10")
	if mo.Totals[0].Planned.Amount != "0.00" || mo.Totals[0].Overdue.Amount != "50.00" || mo.Totals[0].DeferredOut.Amount != "50.00" || mo.Items[0].Deferral == nil || mo.Items[0].Payment.Status != "overdue" {
		t.Fatalf("October: %+v", mo)
	}

	item := "/api/v1/deferral-plans/" + p.ID
	// Revise: a stale or missing version is refused; documented grace annotates.
	if rec := f.do("PATCH", item, `{"plan_type":"documented_grace"}`, admin); rec.Code != 428 {
		t.Errorf("revise without version: %d", rec.Code)
	}
	if rec := f.do("PATCH", item, `{"plan_type":"documented_grace","version":7}`, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "version_conflict") {
		t.Errorf("stale revise: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("PATCH", item, `{"plan_type":"documented_grace","planned_date":"2026-10-31","reason":null}`, with(admin, "If-Match", `"1"`))
	if rec.Code != 200 {
		t.Fatalf("revise: %d %s", rec.Code, rec.Body.String())
	}
	p = decodeDeferral(t, rec.Body.Bytes())
	if p.Version != 2 || p.PlanType != "documented_grace" || p.Reason != nil || p.GraceUntil == nil || *p.GraceUntil != "2026-10-31" || !p.WithinGrace || p.OccurrenceStatus != "overdue" {
		t.Fatalf("revised: %+v", p)
	}
	if m := f.monthPlanned(t, "2026-10"); m.Totals[0].Planned.Amount != "50.00" || m.Totals[0].DeferredOut.Amount != "0.00" {
		t.Errorf("within-month revision: October %+v", m.Totals[0])
	}
	if rec := f.do("PATCH", item, `{"plan_type":"documented_grace","version":2}`, admin); rec.Code != 200 || decodeDeferral(t, rec.Body.Bytes()).Version != 2 {
		t.Errorf("no-op revise: %d %s", rec.Code, rec.Body.String())
	}

	// An explicit fee, then its removal.
	if rec := f.do("POST", item+"/adjustments", `{"kind":"fee","amount":{"amount":"3.00","currency":"USD"},"version":2}`, admin); rec.Code != 400 || !strings.Contains(rec.Body.String(), "amount.currency") {
		t.Errorf("fee in another currency: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("POST", item+"/adjustments", `{"kind":"fee","amount":{"amount":"3.00","currency":"EUR"},"note":"late fee","version":2}`, admin)
	if rec.Code != 201 {
		t.Fatalf("fee: %d %s", rec.Code, rec.Body.String())
	}
	p = decodeDeferral(t, rec.Body.Bytes())
	if p.Version != 3 || p.AdjustmentTotal.Amount != "3.00" || len(p.Adjustments) != 1 {
		t.Fatalf("after fee: %+v", p)
	}
	if m := f.monthPlanned(t, "2026-10"); m.Totals[0].Planned.Amount != "53.00" || m.Totals[0].Adjustments.Amount != "3.00" || m.Totals[0].Outstanding.Amount != "50.00" {
		t.Errorf("October with fee: %+v", m.Totals[0])
	}
	if rec := f.do("DELETE", item+"/adjustments/"+p.Adjustments[0].ID, "", admin); rec.Code != 428 {
		t.Errorf("remove without version: %d", rec.Code)
	}
	rec = f.do("DELETE", item+"/adjustments/"+p.Adjustments[0].ID, "", with(admin, "If-Match", `"3"`))
	if rec.Code != 200 || decodeDeferral(t, rec.Body.Bytes()).Adjustments[0].RemovedAt == nil {
		t.Fatalf("remove fee: %d %s", rec.Code, rec.Body.String())
	}

	// Undo restores; the undone plan is history.
	rec = f.do("POST", item+"/undo", `{"version":4,"reason":"paid from savings"}`, admin)
	if rec.Code != 200 || decodeDeferral(t, rec.Body.Bytes()).State != "undone" {
		t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", item+"/undo", `{"version":5}`, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "deferral_plan_undone") {
		t.Errorf("undo twice: %d %s", rec.Code, rec.Body.String())
	}
	if m := f.monthPlanned(t, "2026-11"); m.Totals[0].Planned.Amount != "50.00" || len(m.CarriedIn) != 0 {
		t.Errorf("November after undo: %+v", m)
	}
	if o := f.getOccurrence(t, oct.ID); o.Payment.Status != "overdue" || o.Version != occBefore.Version {
		t.Errorf("occurrence after undo: %+v", o)
	}

	// History, read access and filters.
	rec = f.do("GET", item+"/history", "", reader)
	var h struct {
		Items []struct {
			Action      string  `json:"action"`
			PlanVersion int64   `json:"plan_version"`
			Reason      *string `json:"reason"`
			By          struct {
				Via *string `json:"via"`
			} `json:"by"`
		} `json:"items"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &h) != nil || len(h.Items) != 5 || h.Items[0].Action != "created" || h.Items[4].Action != "undone" ||
		*h.Items[4].Reason != "paid from savings" || h.Items[0].By.Via == nil || *h.Items[0].By.Via != "session" {
		t.Fatalf("history: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("GET", "/api/v1/deferral-plans?state=undone&commitment_id="+id+"&planned_month=2026-10", "", reader)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), p.ID) {
		t.Errorf("filtered list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/api/v1/deferral-plans?state=paid", "", reader); rec.Code != 400 {
		t.Errorf("bad state filter: %d", rec.Code)
	}

	// Audit: one entry per change, never a reason or an amount.
	var actions []string
	for _, a := range []string{"deferral_plan.created", "deferral_plan.revised", "deferral_plan.adjustment_added", "deferral_plan.adjustment_removed", "deferral_plan.undone"} {
		d := f.auditAction(t, a)
		if d["occurrence_id"] != oct.ID || d["via"] != "session" {
			t.Errorf("%s details %v", a, d)
		}
		if _, ok := d["amount"]; ok {
			t.Errorf("%s carries an amount", a)
		}
		actions = append(actions, a)
	}
	_ = actions

	// A paid occurrence cannot be deferred.
	nov := f.occurrences(t, "month=2026-11&commitment_id="+id, "alice").Items[0]
	if rec := f.do("POST", "/api/v1/occurrences/"+nov.ID+"/manual-payments", `{"amount":{"amount":"50.00","currency":"EUR"},"paid_on":"2026-10-20"}`, admin); rec.Code != 201 {
		t.Fatalf("pay November: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/api/v1/deferral-plans", `{"occurrence_id":"`+nov.ID+`","plan_type":"personal","planned_date":"2026-12-01"}`, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "nothing_outstanding") {
		t.Errorf("defer a paid occurrence: %d %s", rec.Code, rec.Body.String())
	}
}

func with(h map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for a, b := range h {
		out[a] = b
	}
	return out
}

// TestDeferralPlanMCP: the MCP tools reach the same routes, and the history
// records that the change came through MCP.
func TestDeferralPlanMCP(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	secret, _ := f.token(t, f.admin, "commitments:write")
	c := f.connect(t, secret)
	created := call(t, c, "create_commitment", newCommitmentArgs("Deferred"))
	var cm struct{ ID string }
	_ = json.Unmarshal([]byte(resultText(created)), &cm)
	var occ struct {
		Items []struct{ ID, CommitmentID string } `json:"items"`
	}
	_ = json.Unmarshal([]byte(resultText(call(t, c, "list_occurrences", map[string]any{"month": "2026-11", "commitment_id": []string{cm.ID}}))), &occ)
	if len(occ.Items) != 1 {
		t.Fatalf("occurrences: %+v", occ)
	}
	res := call(t, c, "create_deferral_plan", map[string]any{"occurrence_id": occ.Items[0].ID, "plan_type": "creditor_agreed", "planned_date": "2026-12-10"})
	if res.IsError {
		t.Fatalf("create: %s", resultText(res))
	}
	p := decodeDeferral(t, []byte(resultText(res)))
	res = call(t, c, "revise_deferral_plan", map[string]any{"id": p.ID, "version": p.Version, "planned_date": "2026-12-12"})
	if res.IsError || decodeDeferral(t, []byte(resultText(res))).PlannedDate != "2026-12-12" {
		t.Fatalf("revise: %s", resultText(res))
	}
	res = call(t, c, "add_deferral_adjustment", map[string]any{"id": p.ID, "version": 2, "kind": "interest", "amount": map[string]any{"amount": "0.40", "currency": "EUR"}})
	if res.IsError {
		t.Fatalf("adjustment: %s", resultText(res))
	}
	adj := decodeDeferral(t, []byte(resultText(res))).Adjustments[0].ID
	if res := call(t, c, "remove_deferral_adjustment", map[string]any{"id": p.ID, "adjustment_id": adj, "version": 3}); res.IsError {
		t.Fatalf("remove adjustment: %s", resultText(res))
	}
	if res := call(t, c, "undo_deferral_plan", map[string]any{"id": p.ID, "version": 4}); res.IsError || decodeDeferral(t, []byte(resultText(res))).State != "undone" {
		t.Fatalf("undo: %s", resultText(res))
	}
	hist := resultText(call(t, c, "list_deferral_plan_history", map[string]any{"id": p.ID}))
	if strings.Count(hist, `"via":"mcp"`) != 5 {
		t.Errorf("history does not record MCP provenance: %s", hist)
	}
	if list := resultText(call(t, c, "list_deferral_plans", map[string]any{"state": []string{"undone"}})); !strings.Contains(list, p.ID) {
		t.Errorf("list: %s", list)
	}
}
