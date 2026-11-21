package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type matchingRunJSON struct {
	Candidates int `json:"candidates"`
	Confirmed  int `json:"confirmed"`
	Suggested  int `json:"suggested"`
	Dropped    int `json:"dropped"`
	Skipped    int `json:"skipped"`
}

// TestPaymentMatchingRunAPI runs the automatic matching through the API over
// imported bank data: exact and within-tolerance payments are confirmed by
// the system, two identical transactions are left as suggestions, a second
// run changes nothing, and the request is audited for the caller.
func TestPaymentMatchingRunAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	reader := map[string]string{"X-Test-User": f.ids["bob"]}
	importFakeBank(t, f)
	rent := f.createCommitment(t, `{"name":"Rent","kind":"service","amount":{"amount":"800.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":2},"start_date":"2026-01-01"}`)
	f.createCommitment(t, `{"name":"Streaming","kind":"subscription","amount":{"amount":"13.49","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-01-01","match_tolerance":{"amount":"0.50","currency":"EUR"}}`)
	coffee := f.createCommitment(t, `{"name":"Coffee","kind":"subscription","amount":{"amount":"3.50","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":7},"start_date":"2026-01-01"}`)

	if rec := f.do("POST", "/api/v1/payment-matching/runs", "", reader); rec.Code != 403 {
		t.Errorf("read-only: %d", rec.Code)
	}
	if rec := f.do("POST", "/api/v1/payment-matching/runs", `{"dry_run":true}`, admin); rec.Code != 400 || errFields(t, rec.Body.Bytes()) != "dry_run" {
		t.Errorf("unknown option: %d %s", rec.Code, rec.Body.String())
	}
	run := func(body string) matchingRunJSON {
		t.Helper()
		rec := f.do("POST", "/api/v1/payment-matching/runs", body, admin)
		if rec.Code != 200 {
			t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
		}
		var r matchingRunJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(""); r != (matchingRunJSON{Candidates: 4, Confirmed: 2, Suggested: 2}) {
		t.Fatalf("first run: %+v", r)
	}
	oct := f.occurrences(t, "month=2026-10&commitment_id="+rent, "alice").Items[0].ID
	if o := f.getOccurrence(t, oct); o.Payment.Status != "paid" {
		t.Errorf("rent: %+v", o.Payment)
	}
	coffeeOct := f.occurrences(t, "month=2026-10&commitment_id="+coffee, "alice").Items[0].ID
	rec := f.do("GET", "/api/v1/payment-matches?occurrence_id="+coffeeOct, "", admin)
	var page struct {
		Items []matchJSON `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 {
		t.Fatalf("coffee matches: %v %s", err, rec.Body.String())
	}
	for _, m := range page.Items {
		if m.State != "suggested" || m.Origin != "auto_rule" || m.DecidedBy != nil || m.Reason == nil || !strings.Contains(*m.Reason, "several_transactions") {
			t.Errorf("coffee suggestion: %+v", m)
		}
	}
	// A read-only user sees the review queue without bank text.
	rec = f.do("GET", "/api/v1/payment-matches?state=suggested&origin=auto_rule", "", reader)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"bank_text_redacted":true`) || strings.Contains(rec.Body.String(), "COFFEE") {
		t.Errorf("review queue for a reader: %d %s", rec.Code, rec.Body.String())
	}

	// Idempotent; an empty object is accepted too.
	if r := run("{}"); r != (matchingRunJSON{Candidates: 2}) {
		t.Errorf("second run: %+v", r)
	}
	d := f.auditAction(t, "payment_matching.run")
	if d["confirmed"] != "2" || d["suggested"] != "2" || d["dropped"] != "0" || d["skipped"] != "0" {
		t.Errorf("audit details: %v", d)
	}
	var systemConfirmed, runs int
	for _, l := range auditLines(t, f.log.String()) {
		switch l["action"] {
		case "payment_match.confirmed":
			if actor, _ := l["actor_kind"].(string); actor == "system" {
				systemConfirmed++
			}
		case "payment_matching.run":
			runs++
			if l["actor_user_id"] != f.ids["alice"] {
				t.Errorf("run actor: %v", l)
			}
		}
	}
	if systemConfirmed != 2 || runs != 2 {
		t.Errorf("audit: %d system confirmations, %d runs", systemConfirmed, runs)
	}
}

// TestCommitmentMatchTolerance: the optional per-commitment tolerance is
// created, read, patched and cleared through the API, in the commitment's
// currency only.
func TestCommitmentMatchTolerance(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	base := `"name":"VPS","kind":"server","amount":{"amount":"20.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":3},"start_date":"2026-01-01"`
	for body, field := range map[string]string{
		"{" + base + `,"match_tolerance":{"amount":"1.00","currency":"USD"}}`:  "match_tolerance.currency",
		"{" + base + `,"match_tolerance":{"amount":"-1.00","currency":"EUR"}}`: "match_tolerance.amount",
		"{" + base + `,"match_tolerance":{"amount":"1.00"}}`:                   "match_tolerance.currency",
		"{" + base + `,"match_tolerance":"1.00"}`:                              "match_tolerance",
	} {
		rec := f.do("POST", "/api/v1/commitments", body, admin)
		if rec.Code != 400 || errFields(t, rec.Body.Bytes()) != field {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	rec := f.do("POST", "/api/v1/commitments", "{"+base+"}", admin)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"match_tolerance":null`) {
		t.Fatalf("without: %d %s", rec.Code, rec.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	patch := func(body string, version int) string {
		t.Helper()
		rec := f.do("PATCH", "/api/v1/commitments/"+c.ID, body, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"` + string(rune('0'+version)) + `"`, "Content-Type": "application/merge-patch+json"})
		if rec.Code != 200 {
			t.Fatalf("patch %s: %d %s", body, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if got := patch(`{"match_tolerance":{"amount":"0.50","currency":"EUR"}}`, 1); !strings.Contains(got, `"match_tolerance":{"amount":"0.50","currency":"EUR"}`) {
		t.Errorf("set: %s", got)
	}
	if got := patch(`{"match_tolerance":{"amount":"0"}}`, 2); !strings.Contains(got, `"match_tolerance":{"amount":"0","currency":"EUR"}`) {
		t.Errorf("merge the amount: %s", got)
	}
	// Changing the currency alone would leave the tolerance in the old one.
	rec = f.do("PATCH", "/api/v1/commitments/"+c.ID, `{"amount":{"currency":"USD"}}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"3"`, "Content-Type": "application/merge-patch+json"})
	if rec.Code != 400 || errFields(t, rec.Body.Bytes()) != "match_tolerance.currency" {
		t.Errorf("currency change: %d %s", rec.Code, rec.Body.String())
	}
	if got := patch(`{"match_tolerance":null}`, 3); !strings.Contains(got, `"match_tolerance":null`) {
		t.Errorf("clear: %s", got)
	}
	if d := f.auditAction(t, "commitment.updated"); d["fields"] != "match_tolerance" {
		t.Errorf("audit: %v", d)
	}
}
