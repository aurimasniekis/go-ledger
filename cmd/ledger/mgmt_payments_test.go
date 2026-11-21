package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// callRecorder answers the payment and token routes with canned documents and
// records each request's method, path, If-Match and body.
type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *callRecorder) handler() http.HandlerFunc {
	money := map[string]any{"amount": "0.00", "currency": "EUR"}
	occ := map[string]any{
		"id": "o1", "commitment_id": "c1", "month": "2026-11", "due_date": "2026-11-05",
		"amount": map[string]any{"amount": "10.00", "currency": "EUR"}, "version": 4,
		"created_at": "2026-11-01T00:00:00Z", "updated_at": "2026-11-01T00:00:00Z",
		"payment": map[string]any{
			"status": "paid", "derived_status": "partially_paid", "timing": nil,
			"paid_amount": map[string]any{"amount": "4.00", "currency": "EUR"}, "outstanding_amount": money, "overpaid_amount": money,
			"unconverted_currencies": []string{"USD"}, "evidence_count": 1, "manual_evidence_count": 1, "bank_evidence_count": 0, "grace_until": nil, "within_grace": false,
			"override": map[string]any{"status": "paid", "reason": "settled by cash", "set_at": "2026-11-02T00:00:00Z", "applied": true, "previous_status": nil},
		},
	}
	pay := map[string]any{
		"id": "p1", "occurrence_id": "o1", "amount": map[string]any{"amount": "4.00", "currency": "EUR"},
		"paid_on": "2026-11-02", "note": nil, "version": 7, "created_at": "2026-11-02T00:00:00Z", "updated_at": "2026-11-02T00:00:00Z",
	}
	tok := map[string]any{
		"id": "t1", "name": "ci", "owner_id": "u1", "root": false, "grants": []string{"commitments:read"}, "hint": "ldgr_AbCd...wXyZ",
		"prefix": "ldgr_AbCd", "last_chars": "wXyZ", "status": "active", "created_at": "2026-11-01T00:00:00Z",
		"expires_at": nil, "last_used_at": nil, "revoked_at": nil,
	}
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.calls = append(r.calls, req.Method+" "+req.URL.Path+" if-match="+req.Header.Get("If-Match")+" "+string(b))
		r.mu.Unlock()
		var reply any
		switch {
		case req.Method == http.MethodDelete && (strings.Contains(req.URL.Path, "manual-payments/") || strings.HasPrefix(req.URL.Path, "/api/v1/tokens/")):
			w.WriteHeader(http.StatusNoContent)
			return
		case strings.HasSuffix(req.URL.Path, "/manual-payments") && req.Method == http.MethodGet:
			reply = map[string]any{"items": []any{pay}, "next_cursor": nil}
		case strings.Contains(req.URL.Path, "/manual-payments"):
			reply = pay
		case req.URL.Path == "/api/v1/tokens":
			reply = map[string]any{"items": []any{tok}, "next_cursor": nil}
		case strings.HasPrefix(req.URL.Path, "/api/v1/tokens/"):
			reply = tok
		default:
			reply = occ
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}
}

func TestPaymentAndOverrideVerbsSendWhatTheAPIDocuments(t *testing.T) {
	cases := []struct {
		args []string
		want string // the request the last call must be
		out  string
	}{
		{[]string{"occurrences", "payments", "o1"}, "GET /api/v1/occurrences/o1/manual-payments if-match= ", "p1"},
		{[]string{"occurrences", "pay", "o1", "--amount", "4.00", "--currency", "EUR", "--paid-on", "2026-11-02", "--note", "cash"},
			"POST /api/v1/occurrences/o1/manual-payments if-match= {\"amount\":{\"amount\":\"4.00\",\"currency\":\"EUR\"},\"paid_on\":\"2026-11-02\",\"note\":\"cash\"}", "4.00 EUR"},
		{[]string{"occurrences", "payment", "o1", "p1"}, "GET /api/v1/occurrences/o1/manual-payments/p1 if-match= ", "version"},
		{[]string{"occurrences", "delete-payment", "o1", "p1", "--version", "7"}, "DELETE /api/v1/occurrences/o1/manual-payments/p1 if-match=\"7\" ", "deleted p1"},
		{[]string{"occurrences", "override", "o1", "--status", "paid", "--reason", "settled by cash", "--version", "4"},
			"PUT /api/v1/occurrences/o1/status-override if-match=\"4\" {\"status\":\"paid\",\"reason\":\"settled by cash\"}", "override"},
		{[]string{"occurrences", "undo-override", "o1", "--version", "5"}, "DELETE /api/v1/occurrences/o1/status-override if-match=\"5\" ", "partially_paid"},
		{[]string{"tokens", "list", "--owner-id", "u1"}, "GET /api/v1/tokens if-match= ", "ldgr_AbCd"},
		{[]string{"tokens", "get", "t1"}, "GET /api/v1/tokens/t1 if-match= ", "active"},
		{[]string{"tokens", "revoke", "t1"}, "DELETE /api/v1/tokens/t1 if-match= ", "revoked t1"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			var rec callRecorder
			rc, out, errb := cliRun(t, rec.handler(), "10s", c.args...)
			if rc != 0 {
				t.Fatalf("exit %d: %s", rc, errb)
			}
			if got := rec.calls[len(rec.calls)-1]; got != c.want {
				t.Errorf("last request %q, want %q", got, c.want)
			}
			if !strings.Contains(out, c.out) {
				t.Errorf("output %q lacks %q", out, c.out)
			}
		})
	}
}

func TestOverrideReadsAndReportsTheOccurrenceVersionWhenNotGiven(t *testing.T) {
	var rec callRecorder
	rc, _, errb := cliRun(t, rec.handler(), "10s", "occurrences", "override", "o1", "--status", "unpaid", "--reason", "x")
	if rc != 0 || !strings.Contains(errb, "using version 4") {
		t.Fatalf("exit %d, stderr %q", rc, errb)
	}
	if len(rec.calls) != 2 || !strings.HasPrefix(rec.calls[1], "PUT /api/v1/occurrences/o1/status-override if-match=\"4\"") {
		t.Errorf("calls %q", rec.calls)
	}
}

func TestOverrideRefusesBadInputWithoutSending(t *testing.T) {
	for _, args := range [][]string{
		{"occurrences", "override", "o1", "--status", "maybe", "--reason", "x"},
		{"occurrences", "override", "o1", "--status", "paid"},
		{"occurrences", "delete-payment", "o1"},
	} {
		var rec callRecorder
		if rc, _, _ := cliRun(t, rec.handler(), "10s", args...); rc != 2 || len(rec.calls) != 0 {
			t.Errorf("%v: exit %d, %d calls", args, rc, len(rec.calls))
		}
	}
}

func TestTimeoutMustBePositive(t *testing.T) {
	for _, d := range []string{"0", "0s", "-5s"} {
		var rec callRecorder
		rc, _, errb := cliRun(t, rec.handler(), d, "occurrences", "get", "o1")
		if rc != 2 || len(rec.calls) != 0 || !strings.Contains(errb, "greater than zero") {
			t.Errorf("--timeout %s: exit %d, %d calls, stderr %q", d, rc, len(rec.calls), errb)
		}
	}
}

func TestPrintableEscapesBidiAndLineSeparators(t *testing.T) {
	got := printable(string([]rune{'a', 0x202e, 'b', 0x2028, 'c', 0x2029, 'd', 0x200b, 'e', 0x85, 'f'}))
	want := fmt.Sprintf("a\\u%04xb\\u%04xc\\u%04xd\\u%04xe\\x%02xf", 0x202e, 0x2028, 0x2029, 0x200b, 0x85)
	if got != want {
		t.Errorf("printable = %q, want %q", got, want)
	}
	if printable("plain é 日本") != "plain é 日本" {
		t.Error("ordinary text changed")
	}
}
