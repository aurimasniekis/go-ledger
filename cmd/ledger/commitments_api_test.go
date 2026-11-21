package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type errBody struct {
	Code   string `json:"code"`
	Field  string `json:"field"`
	Errors []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

func errFields(t *testing.T, body []byte) string {
	t.Helper()
	var b errBody
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if len(b.Errors) == 0 {
		return b.Field
	}
	var out []string
	for _, e := range b.Errors {
		out = append(out, e.Field)
	}
	if b.Field != out[0] {
		t.Errorf("field %q is not the first of errors %v", b.Field, out)
	}
	return strings.Join(out, ",")
}

const vpsBody = `{"name":"VPS","kind":"server","amount":{"amount":"12.50","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":31},"start_date":"2026-01-31","priority":"essential","notes":"Hetzner-like host"}`

// TestCommitmentLifecycle runs commitment CRUD as ledger serve wires it and
// checks representations, merge patches, concurrency and the audit trail.
func TestCommitmentLifecycle(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	rec := f.do("POST", "/api/v1/commitments", vpsBody, admin)
	if rec.Code != 201 || rec.Header().Get("ETag") != `"1"` || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v1/commitments/") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var c map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	id, _ := c["id"].(string)
	for _, want := range []string{`"amount":{"amount":"12.50","currency":"EUR"}`, `"schedule":{"version":1,"frequency":"monthly","interval":1,"day":31}`,
		`"start_date":"2026-01-31"`, `"end_date":null`, `"priority":"essential"`, `"kind":"server"`, `"version":1`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("create body lacks %s: %s", want, rec.Body.String())
		}
	}
	path := "/api/v1/commitments/" + id

	// Read-only users read commitments (non-administrative data) but never write them.
	ro := map[string]string{"X-Test-User": f.ids["bob"]}
	if rec := f.do("GET", path, "", ro); rec.Code != 200 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("read-only get: %d", rec.Code)
	}
	if rec := f.do("GET", "/api/v1/commitments", "", ro); rec.Code != 200 || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("read-only list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PATCH", path, `{"name":"x"}`, map[string]string{"X-Test-User": f.ids["bob"], "If-Match": `"1"`}); rec.Code != 403 {
		t.Fatalf("read-only patch: %d", rec.Code)
	}

	steps := []struct {
		body, ifMatch, want string
	}{
		// Nested members merge: only the day changes, interval and version stay.
		{`{"schedule":{"day":15}}`, `"1"`, `"schedule":{"version":1,"frequency":"monthly","interval":1,"day":15}`},
		// Switching to annual: drop interval, add month.
		{`{"schedule":{"frequency":"annual","interval":null,"month":2,"day":29},"version":2}`, "", `"schedule":{"version":1,"frequency":"annual","month":2,"day":29}`},
		// Amount merges member by member too; the scale is kept as written.
		{`{"amount":{"amount":"13.5"},"end_date":"2027-12-31","notes":null,"priority":null}`, `"3"`, `"amount":{"amount":"13.5","currency":"EUR"}`},
	}
	for i, s := range steps {
		hdr := map[string]string{"X-Test-User": f.ids["alice"]}
		if s.ifMatch != "" {
			hdr["If-Match"] = s.ifMatch
		}
		rec := f.do("PATCH", path, s.body, hdr)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), s.want) || rec.Header().Get("ETag") != fmt.Sprintf(`"%d"`, i+2) {
			t.Fatalf("step %d: %d %s %s", i, rec.Code, rec.Header().Get("ETag"), rec.Body.String())
		}
	}
	rec = f.do("GET", path, "", admin)
	for _, want := range []string{`"end_date":"2027-12-31"`, `"notes":null`, `"priority":null`, `"version":4`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("after patches lacks %s: %s", want, rec.Body.String())
		}
	}
	// A stale version is a conflict that carries the current commitment.
	rec = f.do("PATCH", path, `{"name":"Late"}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"current_version":4`) || !strings.Contains(rec.Body.String(), `"name":"VPS"`) {
		t.Fatalf("stale patch: %d %s", rec.Code, rec.Body.String())
	}
	// A patch that changes nothing keeps the version and is not audited.
	f.log.Reset()
	rec = f.do("PATCH", path, `{"name":"VPS"}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"4"`})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"4"` || len(auditLines(t, f.log.String())) != 0 {
		t.Fatalf("no-op patch: %d %s", rec.Code, rec.Header().Get("ETag"))
	}
	// DELETE needs If-Match and a current version.
	if rec := f.do("DELETE", path, "", admin); rec.Code != 428 {
		t.Fatalf("delete without If-Match: %d", rec.Code)
	}
	if rec := f.do("DELETE", path, "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"3"`}); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"current_version":4`) {
		t.Fatalf("stale delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("DELETE", path, "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"4"`}); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", path, "", admin); rec.Code != 404 {
		t.Fatalf("get after delete: %d", rec.Code)
	}

	// The audit trail: who, what, which fields; never names, notes or amounts.
	f.log.Reset()
	rec = f.do("POST", "/api/v1/commitments", vpsBody, admin)
	var c2 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &c2)
	f.do("PATCH", "/api/v1/commitments/"+c2.ID, `{"notes":"Renewal moved","amount":{"amount":"20.00"}}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`})
	f.do("DELETE", "/api/v1/commitments/"+c2.ID, "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"2"`})
	var got []string
	for _, l := range auditLines(t, f.log.String()) {
		if l["target_id"] != c2.ID || l["target_type"] != "commitment" || l["actor_user_id"] != f.ids["alice"] {
			t.Errorf("audit line %v", l)
			continue
		}
		s := fmt.Sprint(l["action"])
		if l["detail_fields"] != nil {
			s += " " + fmt.Sprint(l["detail_fields"])
		}
		got = append(got, s)
	}
	if want := "commitment.created|commitment.updated amount,notes|commitment.deleted"; strings.Join(got, "|") != want {
		t.Fatalf("audit trail\n got %s\nwant %s", strings.Join(got, "|"), want)
	}
	for _, secret := range []string{"Hetzner-like", "Renewal moved", "20.00", "VPS"} {
		if strings.Contains(f.log.String(), secret) {
			t.Errorf("the log carries user content %q", secret)
		}
	}
}

// TestCommitmentValidationPerField checks that every invalid field is
// reported, each under its own name, and that nothing invalid is stored.
func TestCommitmentValidationPerField(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	for _, c := range []struct {
		body, fields string
	}{
		{`{}`, "name,kind,amount,schedule,start_date"},
		{`{"name":"","kind":"car","amount":{"amount":"0","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":0,"day":40},"start_date":"2026-02-30","priority":"urgent","notes":""}`,
			"name,kind,amount.amount,schedule.interval,schedule.day,start_date,priority,notes"},
		{`{"name":1,"kind":"service","amount":{"amount":12.5,"currency":"eur"},"schedule":{"version":2},"start_date":"2026-01-01","end_date":"2025-01-01"}`,
			"name,amount.amount,amount.currency,schedule.version,end_date"},
		{`{"name":"x","kind":"service","amount":{"amount":"1","currency":"EUR","fx":"1"},"schedule":{"version":1,"frequency":"weekly"},"start_date":"2026-01-01","extra":true}`,
			"extra,amount.fx,schedule.frequency"},
		{`{"name":"x","kind":"service","amount":"12.50 EUR","schedule":{"version":1,"frequency":"annual","month":4,"day":31},"start_date":"2026-01-01"}`,
			"amount,schedule.day"},
		{`{"name":"x","kind":"service","amount":{"currency":"EUR"},"schedule":[],"start_date":"2026-01-01"}`,
			"amount.amount,schedule"},
	} {
		rec := f.do("POST", "/api/v1/commitments", c.body, admin)
		if rec.Code != 400 {
			t.Errorf("%s: %d %s", c.body, rec.Code, rec.Body.String())
			continue
		}
		if got := errFields(t, rec.Body.Bytes()); got != c.fields {
			t.Errorf("%s:\n got %s\nwant %s\n%s", c.body, got, c.fields, rec.Body.String())
		}
	}
	if rec := f.do("POST", "/api/v1/commitments", `[1]`, admin); rec.Code != 400 {
		t.Errorf("array body: %d", rec.Code)
	}
	if rec := f.do("GET", "/api/v1/commitments", "", admin); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("something invalid was stored: %s", rec.Body.String())
	}

	rec := f.do("POST", "/api/v1/commitments", vpsBody, admin)
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	path := "/api/v1/commitments/" + c.ID
	for _, p := range []struct {
		body   string
		hdr    map[string]string
		status int
		fields string
	}{
		{`{"name":null,"amount":null}`, map[string]string{"If-Match": `"1"`}, 400, "name,amount"},
		{`{"schedule":{"version":null}}`, map[string]string{"If-Match": `"1"`}, 400, "schedule.version"},
		{`{"schedule":{"frequency":"annual"}}`, map[string]string{"If-Match": `"1"`}, 400, "schedule.interval,schedule.month"},
		{`{"amount":{"currency":"XX"},"end_date":"2025-01-01"}`, map[string]string{"If-Match": `"1"`}, 400, "amount.currency,end_date"},
		{`{"secret":1}`, map[string]string{"If-Match": `"1"`}, 400, "secret"},
		{`{"name":"y"}`, nil, 428, ""},
		{`{"name":"y","version":0}`, nil, 400, "version"},
	} {
		hdr := map[string]string{"X-Test-User": f.ids["alice"]}
		for k, v := range p.hdr {
			hdr[k] = v
		}
		rec := f.do("PATCH", path, p.body, hdr)
		if rec.Code != p.status {
			t.Errorf("patch %s: %d %s", p.body, rec.Code, rec.Body.String())
			continue
		}
		if p.fields != "" {
			if got := errFields(t, rec.Body.Bytes()); got != p.fields {
				t.Errorf("patch %s:\n got %s\nwant %s", p.body, got, p.fields)
			}
		}
	}
	if rec := f.do("GET", path, "", admin); rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("a refused patch changed the commitment: %s", rec.Body.String())
	}
	for _, q := range []string{"kind=car", "priority=urgent", "currency=eur", "name=x"} {
		if rec := f.do("GET", "/api/v1/commitments?"+q, "", admin); rec.Code != 400 {
			t.Errorf("list ?%s: %d", q, rec.Code)
		}
	}
	if rec := f.do("GET", "/api/v1/commitments?kind=server&priority=essential&currency=EUR", "", admin); !strings.Contains(rec.Body.String(), c.ID) {
		t.Errorf("filtered list: %s", rec.Body.String())
	}
	if rec := f.do("GET", "/api/v1/commitments?kind=domain", "", admin); strings.Contains(rec.Body.String(), c.ID) {
		t.Errorf("kind filter: %s", rec.Body.String())
	}
}
