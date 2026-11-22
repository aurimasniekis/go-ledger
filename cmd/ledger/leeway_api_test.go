package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type leewayResp struct {
	Leeway *struct {
		Kind         string  `json:"kind"`
		Days         *int    `json:"days"`
		Until        *string `json:"until"`
		Conditions   *string `json:"conditions"`
		Consequences *string `json:"consequences"`
		Source       *string `json:"source"`
	} `json:"leeway"`
	Version int64 `json:"version"`
}

// TestCommitmentLeewayAPI: leeway is user-entered, unknown is a distinct value
// from nothing recorded and from none, and an overdue occurrence reports what
// the user recorded without ever inventing a date.
func TestCommitmentLeewayAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	reader := map[string]string{"X-Test-User": f.ids["bob"]}
	const base = `"name":"Gym","kind":"subscription","amount":{"amount":"50.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":15},"start_date":"2026-01-01"`
	get := func(id string) leewayResp {
		rec := f.do("GET", "/api/v1/commitments/"+id, "", reader)
		var r leewayResp
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &r) != nil {
			t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"leeway":`) {
			t.Fatalf("leeway member missing: %s", rec.Body.String())
		}
		return r
	}
	payment := func(commitmentID string) (leeway *string, until *string, within bool) {
		o := f.occurrences(t, "month=2026-10&commitment_id="+commitmentID, "alice").Items[0]
		rec := f.do("GET", "/api/v1/occurrences/"+o.ID, "", reader)
		var r struct {
			Payment struct {
				Status      string  `json:"status"`
				Leeway      *string `json:"leeway"`
				GraceUntil  *string `json:"grace_until"`
				WithinGrace bool    `json:"within_grace"`
			} `json:"payment"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || r.Payment.Status != "overdue" {
			t.Fatalf("occurrence: %v %s", err, rec.Body.String())
		}
		return r.Payment.Leeway, r.Payment.GraceUntil, r.Payment.WithinGrace
	}
	str := func(p *string) string {
		if p == nil {
			return "<null>"
		}
		return *p
	}

	// Nothing recorded: null, and the overdue occurrence says not_recorded.
	none := f.createCommitmentAt(t, earlier, "{"+base+"}")
	if got := get(none); got.Leeway != nil {
		t.Fatalf("default leeway %+v, want null", got.Leeway)
	}
	if l, u, w := payment(none); str(l) != "not_recorded" || u != nil || w {
		t.Errorf("not recorded: %s %v %v", str(l), u, w)
	}

	// Explicit unknown is kept as a value and never becomes a date or zero.
	unknown := f.createCommitmentAt(t, earlier, "{"+base+`,"leeway":{"kind":"unknown","source":"the contract is silent"}}`)
	if got := get(unknown); got.Leeway == nil || got.Leeway.Kind != "unknown" || got.Leeway.Days != nil || got.Leeway.Until != nil ||
		got.Leeway.Source == nil || *got.Leeway.Source != "the contract is silent" {
		t.Fatalf("unknown leeway %+v", got.Leeway)
	}
	if l, u, w := payment(unknown); str(l) != "unknown" || u != nil || w {
		t.Errorf("unknown: %s %v %v", str(l), u, w)
	}

	// A period counts from the due date and only annotates the overdue state.
	period := f.createCommitmentAt(t, earlier, "{"+base+`,"leeway":{"kind":"period","days":7,"conditions":"if I tell them","consequences":"late fee on the next invoice"}}`)
	if l, u, w := payment(period); str(l) != "period" || str(u) != "2026-10-22" || !w {
		// Overdue since 2026-10-15; today 2026-10-20 is before 2026-10-22.
		t.Errorf("period: %s %s %v", str(l), str(u), w)
	}

	// Validation names the member; unknown carries no days; zero days is refused.
	for _, c := range []struct{ leeway, field string }{
		{`{"kind":"period"}`, "leeway.days"},
		{`{"kind":"period","days":0}`, "leeway.days"},
		{`{"kind":"unknown","days":3}`, "leeway.days"},
		{`{"kind":"none","until":"2026-12-01"}`, "leeway.until"},
		{`{"kind":"deadline"}`, "leeway.until"},
		{`{"kind":"maybe"}`, "leeway.kind"},
		{`{}`, "leeway.kind"},
		{`{"kind":"unknown","guess":1}`, "leeway.guess"},
		{`"unknown"`, "leeway"},
	} {
		rec := f.do("POST", "/api/v1/commitments", "{"+base+`,"leeway":`+c.leeway+"}", admin)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"`+c.field+`"`) {
			t.Errorf("%s: %d %s", c.leeway, rec.Code, rec.Body.String())
		}
	}

	// Patch: merge member by member, switch kind, clear with null.
	patch := func(id string, version int64, body string) (int, leewayResp) {
		rec := f.do("PATCH", "/api/v1/commitments/"+id, body, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": fmt.Sprintf(`"%d"`, version)})
		var r leewayResp
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return rec.Code, r
	}
	if code, r := patch(none, 1, `{"leeway":{"kind":"none"}}`); code != 200 || r.Leeway == nil || r.Leeway.Kind != "none" || r.Version != 2 {
		t.Fatalf("set none: %d %+v", code, r)
	}
	if l, u, _ := payment(none); str(l) != "none" || u != nil {
		t.Errorf("none: %s %v", str(l), u)
	}
	if code, r := patch(none, 2, `{"leeway":{"kind":"period","days":3}}`); code != 200 || r.Leeway.Days == nil || *r.Leeway.Days != 3 {
		t.Fatalf("to period: %d %+v", code, r)
	}
	if code, r := patch(none, 3, `{"leeway":{"kind":"unknown","days":null}}`); code != 200 || r.Leeway.Kind != "unknown" || r.Leeway.Days != nil {
		t.Fatalf("to unknown: %d %+v", code, r)
	}
	if code, _ := patch(none, 4, `{"leeway":{"kind":"none","days":2}}`); code != 400 {
		t.Errorf("none with days: %d", code)
	}
	if code, r := patch(none, 4, `{"leeway":null}`); code != 200 || r.Leeway != nil {
		t.Fatalf("clear: %d %+v", code, r)
	}
	// Reads and writes keep their grants.
	if rec := f.do("POST", "/api/v1/commitments", "{"+base+`,"leeway":{"kind":"unknown"}}`, reader); rec.Code != 403 {
		t.Errorf("read-only user set leeway: %d", rec.Code)
	}
}
