package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Deleted-payment occurrences (C3f, decision D31): an occurrence that is
// removed by a date correction, by an edit's plan or by a commitment delete,
// but that a soft-deleted manual payment still refers to, is cancelled.

type cancelledOcc struct {
	ID              string  `json:"id"`
	Month           string  `json:"month"`
	Version         int64   `json:"version"`
	CancelledAt     *string `json:"cancelled_at"`
	CancelledReason *string `json:"cancelled_reason"`
}

// listAll returns the month's occurrences, cancelled ones included when asked.
func (f *apiFixture) listMonth(t *testing.T, month string, withCancelled bool) []cancelledOcc {
	t.Helper()
	q := "month=" + month
	if withCancelled {
		q += "&include_cancelled=true"
	}
	rec := f.do("GET", "/api/v1/occurrences?"+q, "", map[string]string{"X-Test-User": f.ids["alice"]})
	if rec.Code != 200 {
		t.Fatalf("list %s: %d %s", q, rec.Code, rec.Body.String())
	}
	var p struct {
		Items []cancelledOcc `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p.Items
}

// payAndDelete records a manual payment on the occurrence of month and
// deletes it again, leaving only the soft-deleted row.
func (f *apiFixture) payAndDelete(t *testing.T, month string) string {
	return f.payAndDeleteOn(t, month, month+"-10")
}

func (f *apiFixture) payAndDeleteOn(t *testing.T, month, paidOn string) string {
	t.Helper()
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	items := f.listMonth(t, month, false)
	if len(items) != 1 {
		t.Fatalf("month %s: %d occurrences", month, len(items))
	}
	id := items[0].ID
	rec := f.do("POST", "/api/v1/occurrences/"+id+"/manual-payments", `{"amount":{"amount":"10.00","currency":"EUR"},"paid_on":"`+paidOn+`"}`, admin)
	if rec.Code != 201 {
		t.Fatalf("pay %s: %d %s", month, rec.Code, rec.Body.String())
	}
	var pay struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &pay)
	h := map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`}
	if rec := f.do("DELETE", "/api/v1/occurrences/"+id+"/manual-payments/"+pay.ID, "", h); rec.Code != 204 {
		t.Fatalf("delete payment %s: %d %s", month, rec.Code, rec.Body.String())
	}
	return id
}

func (f *apiFixture) regeneratedAudit(t *testing.T) map[string]string {
	t.Helper()
	p := f.auditList(t, "?limit=200", "alice", 200)
	var last map[string]string
	for _, e := range p.Items {
		if e.Action == "occurrences.regenerated" {
			last = e.Details
			if e.Details["cancelled_referenced"] != "0" {
				return e.Details
			}
		}
	}
	return last
}

func (f *apiFixture) auditAction(t *testing.T, action string) map[string]string {
	t.Helper()
	for _, e := range f.auditList(t, "?limit=200", "alice", 200).Items {
		if e.Action == action {
			return e.Details
		}
	}
	t.Fatalf("no %s audit entry", action)
	return nil
}

// TestDateCorrectionCancelsReferencedAPI: the probe of the re-review. A start
// date correction on a commitment whose January payment was deleted used to
// fail with 500; January is now cancelled and February, which nothing refers
// to, is removed.
func TestDateCorrectionCancelsReferencedAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":10`, 1))
	f.occurrences(t, "month=2026-05", "alice")
	jan := f.payAndDelete(t, "2026-01")
	if rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"start_date":"2026-03-01","version":1}`, admin); rec.Code != 200 {
		t.Fatalf("start date correction: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(f.listMonth(t, "2026-01", false)); n != 0 {
		t.Errorf("a cancelled occurrence is listed by default: %d", n)
	}
	got := f.listMonth(t, "2026-01", true)
	if len(got) != 1 || got[0].ID != jan || got[0].CancelledAt == nil || got[0].CancelledReason == nil || *got[0].CancelledReason != "out_of_range" {
		t.Fatalf("January with include_cancelled: %+v", got)
	}
	if n := len(f.listMonth(t, "2026-02", true)); n != 0 {
		t.Errorf("February was not removed: %d", n)
	}
	if rec := f.do("GET", "/api/v1/occurrences/"+jan, "", admin); rec.Code != 200 || !strings.Contains(rec.Body.String(), "cancelled_at") {
		t.Errorf("a cancelled occurrence stays readable: %d %s", rec.Code, rec.Body.String())
	}
	// It is not in the monthly view and takes no new payment.
	rec := f.do("GET", "/api/v1/occurrences/summary?month=2026-01", "", admin)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), jan) || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("summary shows the cancelled occurrence: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/api/v1/occurrences/"+jan+"/manual-payments", `{"amount":{"amount":"10.00","currency":"EUR"},"paid_on":"2026-01-11"}`, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "occurrence_cancelled") {
		t.Errorf("payment on a cancelled occurrence: %d %s", rec.Code, rec.Body.String())
	}
	d := f.regeneratedAudit(t)
	if d["removed_out_of_range"] != "1" || d["cancelled_referenced"] != "1" || d["removed"] != "0" {
		t.Errorf("audit counts %v", d)
	}
}

// TestRegenerationCancelsReferencedAPI: an end date edit whose plan removes an
// occurrence with a deleted payment cancels it (it used to be skipped silently
// and counted as removed), the counts add up, and an edit that schedules the
// month again revives it.
func TestRegenerationCancelsReferencedAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":10`, 1))
	f.occurrences(t, "month=2026-05", "alice")
	may := f.payAndDeleteOn(t, "2026-05", "2026-03-10") // a payment cannot be dated in the future
	if rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"end_date":"2026-03-31","version":1}`, admin); rec.Code != 200 {
		t.Fatalf("end date: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(f.listMonth(t, "2026-04", true)); n != 0 {
		t.Errorf("April was not removed: %d", n)
	}
	if n := len(f.listMonth(t, "2026-05", false)); n != 0 {
		t.Errorf("May stays listed after the commitment ended: %d", n)
	}
	got := f.listMonth(t, "2026-05", true)
	if len(got) != 1 || got[0].ID != may || got[0].CancelledReason == nil || *got[0].CancelledReason != "terms_change" {
		t.Fatalf("May: %+v", got)
	}
	d := f.regeneratedAudit(t)
	if d["removed"] != "1" || d["cancelled_referenced"] != "1" || d["removed_out_of_range"] != "0" {
		t.Errorf("audit counts %v (April removed, May cancelled)", d)
	}
	// Extending the end date schedules May again: the cancelled row is revived.
	if rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"end_date":null,"version":2}`, admin); rec.Code != 200 {
		t.Fatalf("reopen: %d %s", rec.Code, rec.Body.String())
	}
	got = f.listMonth(t, "2026-05", false)
	if len(got) != 1 || got[0].ID != may || got[0].CancelledAt != nil {
		t.Errorf("May after the end date was lifted: %+v", got)
	}
}

// TestCommitmentDeleteCancelsReferencedAPI: deleting a commitment whose only
// payment history is a deleted payment succeeds (it used to be 409), cancels
// that occurrence, keeps the history readable and hides the commitment.
func TestCommitmentDeleteCancelsReferencedAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":10`, 1))
	f.occurrences(t, "month=2026-03", "alice")
	jan := f.payAndDelete(t, "2026-01")
	del := func(c string, v int) int {
		return f.do("DELETE", "/api/v1/commitments/"+c, "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": fmt.Sprintf(`"%d"`, v)}).Code
	}
	if code := del(id, 1); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if rec := f.do("GET", "/api/v1/commitments/"+id, "", admin); rec.Code != 404 {
		t.Errorf("deleted commitment: %d", rec.Code)
	}
	if rec := f.do("GET", "/api/v1/commitments", "", admin); strings.Contains(rec.Body.String(), id) {
		t.Errorf("deleted commitment is listed: %s", rec.Body.String())
	}
	got := f.listMonth(t, "2026-01", true)
	if len(got) != 1 || got[0].ID != jan || got[0].CancelledReason == nil || *got[0].CancelledReason != "commitment_deleted" {
		t.Fatalf("January: %+v", got)
	}
	if n := len(f.listMonth(t, "2026-01", false)) + len(f.listMonth(t, "2026-02", true)) + len(f.listMonth(t, "2026-03", true)); n != 0 {
		t.Errorf("%d occurrences left besides the cancelled one", n)
	}
	d := f.auditAction(t, "commitment.deleted")
	if d["cancelled_referenced"] != "1" || d["archived"] != "true" || d["unpaid_occurrences_removed"] != "2" {
		t.Errorf("audit %v (February and March removed, January cancelled)", d)
	}
	if code := del(id, 1); code != 404 {
		t.Errorf("deleting again: %d", code)
	}
}

// TestCommitmentDeleteWithLivePaymentStillRefusedAPI: live payment evidence
// still blocks the delete, and nothing is cancelled by the refused attempt.
func TestCommitmentDeleteWithLivePaymentStillRefusedAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":10`, 1))
	f.occurrences(t, "month=2026-03", "alice")
	f.payAndDelete(t, "2026-01")
	feb := f.listMonth(t, "2026-02", false)[0].ID
	if rec := f.do("POST", "/api/v1/occurrences/"+feb+"/manual-payments", `{"amount":{"amount":"10.00","currency":"EUR"},"paid_on":"2026-02-10"}`, admin); rec.Code != 201 {
		t.Fatalf("pay: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("DELETE", "/api/v1/commitments/"+id, "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`}); rec.Code != 409 {
		t.Fatalf("delete with a live payment: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(f.listMonth(t, "2026-01", false)); n != 1 {
		t.Errorf("the refused delete changed January: %d listed", n)
	}
	if rec := f.do("GET", "/api/v1/commitments/"+id, "", admin); rec.Code != 200 {
		t.Errorf("commitment after a refused delete: %d", rec.Code)
	}
}
