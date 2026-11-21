package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type occurrenceWithPayment struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Payment struct {
		Status        string  `json:"status"`
		DerivedStatus string  `json:"derived_status"`
		Timing        *string `json:"timing"`
		Paid          struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"paid_amount"`
		Outstanding struct {
			Amount string `json:"amount"`
		} `json:"outstanding_amount"`
		Unconverted         []string `json:"unconverted_currencies"`
		EvidenceCount       int      `json:"evidence_count"`
		ManualEvidenceCount int      `json:"manual_evidence_count"`
		BankEvidenceCount   int      `json:"bank_evidence_count"`
		GraceUntil          *string  `json:"grace_until"`
		WithinGrace         bool     `json:"within_grace"`
		Override            *struct {
			Status         string  `json:"status"`
			Reason         string  `json:"reason"`
			Applied        bool    `json:"applied"`
			PreviousStatus *string `json:"previous_status"`
		} `json:"override"`
	} `json:"payment"`
}

func decodeOccurrence(t *testing.T, body []byte) occurrenceWithPayment {
	t.Helper()
	var o occurrenceWithPayment
	if err := json.Unmarshal(body, &o); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return o
}

func (f *apiFixture) getOccurrence(t *testing.T, id string) occurrenceWithPayment {
	t.Helper()
	rec := f.do("GET", "/api/v1/occurrences/"+id, "", map[string]string{"X-Test-User": f.ids["bob"]}) // read-only users read payment state
	if rec.Code != 200 {
		t.Fatalf("get occurrence: %d %s", rec.Code, rec.Body.String())
	}
	o := decodeOccurrence(t, rec.Body.Bytes())
	if rec.Header().Get("ETag") != fmt.Sprintf("%q", fmt.Sprint(o.Version)) {
		t.Fatalf("ETag %s for version %d", rec.Header().Get("ETag"), o.Version)
	}
	return o
}

// TestPaymentStateAPI walks the manual payment journey: an overdue
// occurrence, a partial manual payment, the rest, an override with its undo,
// and a deletion, checking status, versions, refusals and the audit trail.
func TestPaymentStateAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	with := func(h map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for a, b := range h {
			out[a] = b
		}
		return out
	}
	id := f.createCommitment(t, monthEndBody) // 10.00 EUR on month ends
	p := f.occurrences(t, "month=2026-10&commitment_id="+id, "alice")
	occID := p.Items[0].ID
	base := "/api/v1/occurrences/" + occID

	o := f.getOccurrence(t, occID)
	if o.Payment.Status != "overdue" || o.Payment.Timing == nil || *o.Payment.Timing != "overdue" || o.Payment.Paid.Amount != "0.00" ||
		o.Payment.Outstanding.Amount != "10.00" || o.Payment.EvidenceCount != 0 || o.Payment.Override != nil || o.Payment.GraceUntil != nil ||
		o.Payment.Unconverted == nil {
		t.Fatalf("unpaid October occurrence: %+v", o.Payment)
	}
	// The list carries the same state.
	if lp := f.occurrences(t, "month=2026-12&commitment_id="+id, "alice"); len(lp.Items) != 1 {
		t.Fatalf("December: %+v", lp)
	}
	rec := f.do("GET", "/api/v1/occurrences?month=2026-12&commitment_id="+id, "", admin)
	if !strings.Contains(rec.Body.String(), `"status":"upcoming"`) {
		t.Fatalf("December list: %s", rec.Body.String())
	}

	// Nothing becomes paid without evidence.
	rec = f.do("PUT", base+"/status-override", `{"status":"paid","reason":"paid in cash","version":1}`, admin)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "payment_evidence_required") {
		t.Fatalf("paid override without evidence: %d %s", rec.Code, rec.Body.String())
	}

	// Refusals of a manual payment.
	for _, c := range []struct{ body, field string }{
		{`{"amount":{"amount":"4.00","currency":"USD"},"paid_on":"2026-11-01"}`, "amount.currency"},
		{`{"amount":{"amount":"0","currency":"EUR"},"paid_on":"2026-11-01"}`, "amount.amount"},
		{`{"amount":{"amount":"4.00","currency":"EUR"},"paid_on":"2026-11-22"}`, "paid_on"},
		{`{"amount":{"amount":"4.00","currency":"EUR"}}`, "paid_on"},
		{`{"amount":{"amount":"4.00","currency":"EUR"},"paid_on":"2026-11-01","extra":1}`, "extra"},
		{`{"amount":{"amount":"4.00","currency":"EUR"},"paid_on":"2026-11-01","note":" "}`, "note"},
	} {
		rec := f.do("POST", base+"/manual-payments", c.body, admin)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"`+c.field+`"`) {
			t.Errorf("%s: %d %s", c.body, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do("POST", "/api/v1/occurrences/01JD3Y4C8V9M2Q7W5R6T0XZBNZ/manual-payments", `{"amount":{"amount":"4.00","currency":"EUR"},"paid_on":"2026-11-01"}`, admin); rec.Code != 404 {
		t.Errorf("unknown occurrence: %d", rec.Code)
	}
	if rec := f.do("POST", base+"/manual-payments", `{"amount":{"amount":"4.00","currency":"EUR"},"paid_on":"2026-11-01"}`, map[string]string{"X-Test-User": f.ids["bob"]}); rec.Code != 403 {
		t.Errorf("read-only user recorded a payment: %d", rec.Code)
	}
	if got := f.getOccurrence(t, occID); got.Version != 1 {
		t.Fatalf("refused writes changed the occurrence: v%d", got.Version)
	}

	// A partial payment, then the rest.
	rec = f.do("POST", base+"/manual-payments", `{"amount":{"amount":"4.00","currency":"EUR"},"paid_on":"2026-11-01","note":"first half"}`, admin)
	if rec.Code != 201 || rec.Header().Get("ETag") != `"1"` || !strings.HasPrefix(rec.Header().Get("Location"), base+"/manual-payments/") {
		t.Fatalf("record: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	var pay struct {
		ID   string  `json:"id"`
		Note *string `json:"note"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pay)
	o = f.getOccurrence(t, occID)
	if o.Version != 2 || o.Payment.Status != "partially_paid" || *o.Payment.Timing != "overdue" || o.Payment.Paid.Amount != "4.00" || o.Payment.Outstanding.Amount != "6.00" ||
		o.Payment.ManualEvidenceCount != 1 || o.Payment.BankEvidenceCount != 0 {
		t.Fatalf("after 4.00: v%d %+v", o.Version, o.Payment)
	}
	rec = f.do("POST", base+"/manual-payments", `{"amount":{"amount":"6.00","currency":"EUR"},"paid_on":"2026-11-21"}`, admin)
	if rec.Code != 201 {
		t.Fatalf("second payment: %d %s", rec.Code, rec.Body.String())
	}
	o = f.getOccurrence(t, occID)
	if o.Version != 3 || o.Payment.Status != "paid" || o.Payment.Timing != nil || o.Payment.Outstanding.Amount != "0.00" || o.Payment.EvidenceCount != 2 ||
		o.Payment.ManualEvidenceCount != 2 || o.Payment.BankEvidenceCount != 0 {
		t.Fatalf("after 10.00: v%d %+v", o.Version, o.Payment)
	}
	rec = f.do("GET", base+"/manual-payments?limit=1", "", map[string]string{"X-Test-User": f.ids["bob"]})
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != pay.ID || page.NextCursor == nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("GET", base+"/manual-payments?limit=1&cursor="+*page.NextCursor, "", admin)
	if !strings.Contains(rec.Body.String(), `"next_cursor":null`) || strings.Contains(rec.Body.String(), pay.ID) {
		t.Fatalf("second page: %s", rec.Body.String())
	}
	if rec := f.do("GET", base+"/manual-payments/"+pay.ID, "", admin); rec.Code != 200 || !strings.Contains(rec.Body.String(), "first half") {
		t.Fatalf("get payment: %d %s", rec.Code, rec.Body.String())
	}

	// Override and undo.
	if rec := f.do("PUT", base+"/status-override", `{"status":"unpaid","reason":"the second transfer bounced"}`, admin); rec.Code != 428 {
		t.Fatalf("override without a version: %d", rec.Code)
	}
	rec = f.do("PUT", base+"/status-override", `{"status":"unpaid","reason":"the second transfer bounced"}`, with(admin, "If-Match", `"2"`))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"current_version":3`) || !strings.Contains(rec.Body.String(), `"derived_status":"paid"`) {
		t.Fatalf("stale override: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("PUT", base+"/status-override", `{"status":"unpaid","reason":"the second transfer bounced"}`, with(admin, "If-Match", `"3"`))
	o = decodeOccurrence(t, rec.Body.Bytes())
	if rec.Code != 200 || rec.Header().Get("ETag") != `"4"` || o.Payment.Status != "overdue" || o.Payment.DerivedStatus != "paid" ||
		o.Payment.Override == nil || !o.Payment.Override.Applied || o.Payment.Override.PreviousStatus != nil {
		t.Fatalf("unpaid override: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PUT", base+"/status-override", `{"status":"waived","reason":""}`, with(admin, "If-Match", `"4"`)); rec.Code != 400 {
		t.Fatalf("bad override: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("DELETE", base+"/status-override", "", admin); rec.Code != 428 {
		t.Fatalf("undo without If-Match: %d", rec.Code)
	}
	rec = f.do("DELETE", base+"/status-override", "", with(admin, "If-Match", `"4"`))
	o = decodeOccurrence(t, rec.Body.Bytes())
	if rec.Code != 200 || rec.Header().Get("ETag") != `"5"` || o.Payment.Status != "paid" || o.Payment.Override != nil {
		t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("DELETE", base+"/status-override", "", with(admin, "If-Match", `"5"`)); rec.Code != 404 {
		t.Fatalf("undo with nothing to undo: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.getOccurrence(t, occID); got.Payment.Status != "paid" || got.Version != 5 {
		t.Fatalf("after undo: %+v", got)
	}

	// Payment history blocks the commitment's delete.
	cv := f.do("GET", "/api/v1/commitments/"+id, "", admin).Header().Get("ETag")
	if rec := f.do("DELETE", "/api/v1/commitments/"+id, "", with(admin, "If-Match", cv)); rec.Code != 409 {
		t.Fatalf("delete of a commitment with payments: %d %s", rec.Code, rec.Body.String())
	}

	// Deleting a payment needs its version.
	if rec := f.do("DELETE", base+"/manual-payments/"+pay.ID, "", admin); rec.Code != 428 {
		t.Fatalf("delete without If-Match: %d", rec.Code)
	}
	if rec := f.do("DELETE", base+"/manual-payments/"+pay.ID, "", with(admin, "If-Match", `"2"`)); rec.Code != 409 {
		t.Fatalf("stale delete: %d", rec.Code)
	}
	if rec := f.do("DELETE", base+"/manual-payments/"+pay.ID, "", with(admin, "If-Match", `"1"`)); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// The soft-deleted payment leaves every count: only the 6.00 payment is manual evidence now.
	if o := f.getOccurrence(t, occID); o.Payment.Status != "partially_paid" || o.Payment.Paid.Amount != "6.00" || o.Version != 6 ||
		o.Payment.EvidenceCount != 1 || o.Payment.ManualEvidenceCount != 1 || o.Payment.BankEvidenceCount != 0 {
		t.Fatalf("after delete: %+v", o)
	}
	// A deleted payment is gone for the API: neither read nor deleted again.
	if rec := f.do("GET", base+"/manual-payments/"+pay.ID, "", admin); rec.Code != 404 {
		t.Fatalf("get a deleted payment: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("DELETE", base+"/manual-payments/"+pay.ID, "", with(admin, "If-Match", `"1"`)); rec.Code != 404 {
		t.Fatalf("second delete: %d %s", rec.Code, rec.Body.String())
	}

	// The audit trail names the occurrence, never amounts, notes or reasons.
	var got []string
	for _, l := range auditLines(t, f.log.String()) {
		if l["target_type"] != "occurrence" {
			continue
		}
		if l["target_id"] != occID || l["actor_user_id"] != f.ids["alice"] {
			t.Errorf("audit line %v", l)
		}
		s := fmt.Sprint(l["action"])
		for _, k := range []string{"detail_currency", "detail_status", "detail_previous", "detail_undone", "detail_restored"} {
			if l[k] != nil {
				s += " " + fmt.Sprint(l[k])
			}
		}
		got = append(got, s)
	}
	want := "manual_payment.recorded EUR|manual_payment.recorded EUR|occurrence.status_overridden unpaid none|occurrence.status_override_undone unpaid none|manual_payment.deleted EUR"
	if strings.Join(got, "|") != want {
		t.Fatalf("audit trail\n got %s\nwant %s", strings.Join(got, "|"), want)
	}
	// Notes and reasons appear nowhere in the log; amounts not in audit lines
	// (other log lines carry timings, which may contain the same digits).
	for _, secret := range []string{"first half", "bounced"} {
		if strings.Contains(f.log.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
	}
	for _, l := range auditLines(t, f.log.String()) {
		if l["action"] == "manual_payment.deleted" {
			// A delete records the deleted payment's amount (D26-3a).
			if l["detail_amount"] != "4.00" {
				t.Errorf("delete audit line amount = %v: %v", l["detail_amount"], l)
			}
			continue
		}
		for k, v := range l {
			if s := fmt.Sprint(v); strings.Contains(s, "4.00") || strings.Contains(s, "6.00") {
				t.Errorf("audit line carries an amount in %s: %v", k, l)
			}
		}
	}
}

// TestDeletedPaymentDoesNotFreezeOccurrence: an occurrence whose only payment
// was deleted is unpaid again, so a later terms edit still rewrites it.
func TestDeletedPaymentDoesNotFreezeOccurrence(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, monthEndBody) // 10.00 EUR on month ends
	occID := f.occurrences(t, "month=2026-12&commitment_id="+id, "alice").Items[0].ID
	base := "/api/v1/occurrences/" + occID

	rec := f.do("POST", base+"/manual-payments", `{"amount":{"amount":"4.00","currency":"EUR"},"paid_on":"2026-11-01"}`, admin)
	if rec.Code != 201 {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	var pay struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pay)
	if rec := f.do("DELETE", base+"/manual-payments/"+pay.ID, "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`}); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("PATCH", "/api/v1/commitments/"+id, `{"amount":{"amount":"20.00"},"schedule":{"day":15},"effective_from":"2026-11-22","version":1}`, admin)
	if rec.Code != 200 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("GET", base, "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"due_date":"2026-12-15"`) || !strings.Contains(rec.Body.String(), `"amount":"20.00"`) {
		t.Fatalf("the edit did not apply to the unpaid occurrence: %d %s", rec.Code, rec.Body.String())
	}
}
