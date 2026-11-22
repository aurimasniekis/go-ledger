package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
)

type mcpMatch struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	Version          int64  `json:"version"`
	OccurrenceID     string `json:"occurrence_id"`
	CountsAsEvidence bool   `json:"counts_as_evidence"`
	BankTextRedacted bool   `json:"bank_text_redacted"`
	DecidedBy        *struct {
		Kind string `json:"kind"`
	} `json:"decided_by"`
	History []struct {
		Action string `json:"action"`
		By     struct {
			Kind string `json:"kind"`
		} `json:"by"`
	} `json:"history"`
	Transaction struct {
		Description *string `json:"description"`
	} `json:"transaction"`
}

func decodeJSONText[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var v T
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	if err := json.Unmarshal([]byte(resultText(res)), &v); err != nil {
		t.Fatalf("%v: %s", err, resultText(res))
	}
	return v
}

// TestMCPPaymentMatchJourney walks the matching tools over real MCP and a real API: run matching, review the queue,
// reject, confirm, correct, unmatch, a stale version, a manual payment and its removal; checks the tool annotations,
// bank-text redaction, grants and the audit of every write.
func TestMCPPaymentMatchJourney(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	importFakeBankInto(t, f.db)
	secret, _ := f.token(t, f.admin, "commitments:write", "banking:read")
	c := f.connect(t, secret)

	create := func(name, amount, day, extra string) string {
		t.Helper()
		args := map[string]any{}
		raw := `{"name":"` + name + `","kind":"subscription","amount":{"amount":"` + amount + `","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":` + day + `},"start_date":"2026-01-01"` + extra + `}`
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
		return decodeJSONText[struct{ ID string }](t, call(t, c, "create_commitment", args)).ID
	}
	create("Rent", "800.00", "2", "")
	create("Streaming", "13.49", "5", `,"match_tolerance":{"amount":"0.50","currency":"EUR"}`)
	coffee := create("Coffee", "3.50", "7", "")

	// The tolerance reached the commitment (a model that sets it can read it back).
	got := decodeJSONText[struct {
		Items []struct {
			Name           string
			MatchTolerance *struct{ Amount string } `json:"match_tolerance"`
		}
	}](t, call(t, c, "list_commitments", nil))
	for _, it := range got.Items {
		if (it.Name == "Streaming") != (it.MatchTolerance != nil && it.MatchTolerance.Amount == "0.50") {
			t.Errorf("match_tolerance of %s: %+v", it.Name, it.MatchTolerance)
		}
	}

	run := decodeJSONText[matchingRunJSON](t, call(t, c, "run_payment_matching", nil))
	if run != (matchingRunJSON{Candidates: 4, Confirmed: 2, Suggested: 2}) {
		t.Fatalf("run: %+v", run)
	}
	if again := decodeJSONText[matchingRunJSON](t, call(t, c, "run_payment_matching", nil)); again.Confirmed != 0 || again.Suggested != 0 || again.Dropped != 0 {
		t.Errorf("a second run changed something: %+v", again)
	}

	// D33 over MCP: what the engine confirmed names the system as the decider, never the token that started the run.
	auto := decodeJSONText[struct{ Items []mcpMatch }](t, call(t, c, "list_payment_matches", map[string]any{"state": []string{"confirmed"}}))
	if len(auto.Items) != run.Confirmed {
		t.Fatalf("engine confirmations: %d listed, run said %d", len(auto.Items), run.Confirmed)
	}
	for _, m := range auto.Items {
		if m.DecidedBy == nil || m.DecidedBy.Kind != "system" {
			t.Errorf("decided_by of an automatic confirmation: %+v", m.DecidedBy)
		}
		if len(m.History) == 0 || m.History[len(m.History)-1].By.Kind != "system" {
			t.Errorf("history of an automatic confirmation: %+v", m.History)
		}
	}

	queue := decodeJSONText[struct{ Items []mcpMatch }](t, call(t, c, "list_payment_matches", map[string]any{"state": []string{"suggested"}, "origin": []string{"auto_rule"}}))
	if len(queue.Items) != 2 {
		t.Fatalf("review queue: %+v", queue)
	}
	first, second := queue.Items[0], queue.Items[1]
	if first.CountsAsEvidence || first.BankTextRedacted || first.Transaction.Description == nil || !strings.Contains(*first.Transaction.Description, "COFFEE") {
		t.Errorf("suggestion with banking:read: %+v", first)
	}
	got1 := decodeJSONText[mcpMatch](t, call(t, c, "get_payment_match", map[string]any{"id": first.ID}))
	if got1.ID != first.ID || got1.Version != first.Version {
		t.Errorf("get_payment_match: %+v", got1)
	}

	// A stale version is a conflict that names the current one, and changes nothing.
	stale := call(t, c, "confirm_payment_match", map[string]any{"id": first.ID, "version": first.Version + 5})
	if !stale.IsError || !strings.Contains(resultText(stale), "version_conflict") {
		t.Fatalf("stale confirm: %q", resultText(stale))
	}
	rejected := decodeJSONText[mcpMatch](t, call(t, c, "reject_payment_match", map[string]any{"id": second.ID, "version": second.Version, "reason": "the other charge"}))
	if rejected.State != "rejected" || rejected.CountsAsEvidence {
		t.Errorf("reject: %+v", rejected)
	}
	confirmed := decodeJSONText[mcpMatch](t, call(t, c, "confirm_payment_match", map[string]any{"id": first.ID, "version": first.Version}))
	if confirmed.State != "confirmed" || !confirmed.CountsAsEvidence {
		t.Fatalf("confirm: %+v", confirmed)
	}
	corrected := decodeJSONText[mcpMatch](t, call(t, c, "correct_payment_match", map[string]any{"id": first.ID, "version": confirmed.Version, "amount": map[string]any{"amount": "3.00", "currency": "EUR"}}))
	if corrected.State != "confirmed" || corrected.Version <= confirmed.Version {
		t.Fatalf("correct: %+v", corrected)
	}
	occ := decodeJSONText[struct{ Payment struct{ Status string } }](t, call(t, c, "get_occurrence", map[string]any{"id": first.OccurrenceID}))
	if occ.Payment.Status != "partially_paid" {
		t.Errorf("after correcting to 3.00 of 3.50: %q", occ.Payment.Status)
	}
	unmatched := decodeJSONText[mcpMatch](t, call(t, c, "unmatch_payment_match", map[string]any{"id": first.ID, "version": corrected.Version}))
	if unmatched.State != "rejected" {
		t.Errorf("unmatch: %+v", unmatched)
	}
	// rejected is final.
	if r := call(t, c, "reverse_payment_match", map[string]any{"id": first.ID, "version": unmatched.Version}); !r.IsError || !strings.Contains(resultText(r), "invalid_match_state") {
		t.Errorf("reversing a rejected match: %q", resultText(r))
	}

	// A manual payment, then its removal.
	pay := decodeJSONText[struct {
		ID      string
		Version int64
	}](t, call(t, c, "record_manual_payment", map[string]any{"id": first.OccurrenceID, "amount": map[string]any{"amount": "3.50", "currency": "EUR"}, "paid_on": "2026-10-07"}))
	if o := decodeJSONText[struct{ Payment struct{ Status string } }](t, call(t, c, "get_occurrence", map[string]any{"id": first.OccurrenceID})); o.Payment.Status != "paid" {
		t.Errorf("after the manual payment: %q", o.Payment.Status)
	}
	if r := call(t, c, "delete_manual_payment", map[string]any{"id": first.OccurrenceID, "payment_id": pay.ID, "version": pay.Version}); r.IsError {
		t.Fatalf("delete_manual_payment: %s", resultText(r))
	}
	if o := decodeJSONText[struct{ Payment struct{ Status string } }](t, call(t, c, "get_occurrence", map[string]any{"id": first.OccurrenceID})); o.Payment.Status == "paid" {
		t.Errorf("still paid after the removal: %q", o.Payment.Status)
	}
	_ = coffee

	// Annotations: destructive tools are marked, reads are read-only, nothing else is destructive.
	lt, err := c.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	destructive := map[string]bool{"reject_payment_match": true, "unmatch_payment_match": true, "reverse_payment_match": true, "delete_manual_payment": true, "delete_commitment": true,
		"undo_deferral_plan": true, "remove_deferral_adjustment": true}
	seen := 0
	for _, tl := range lt.Tools {
		d := tl.Annotations != nil && tl.Annotations.DestructiveHint != nil && *tl.Annotations.DestructiveHint
		if d != destructive[tl.Name] {
			t.Errorf("%s: destructiveHint=%v", tl.Name, d)
		}
		if strings.Contains(tl.Name, "payment_match") || strings.HasSuffix(tl.Name, "_manual_payment") {
			seen++
		}
	}
	if seen != 13 {
		t.Errorf("expected 13 matching and manual payment tools, listed %d", seen)
	}

	// A read-only owner: the queue is readable with the bank text redacted, writes name the missing grant.
	rs, _ := f.token(t, f.ro, "commitments:read")
	rc := f.connect(t, rs)
	view := call(t, rc, "list_payment_matches", map[string]any{"state": []string{"rejected"}})
	if view.IsError || !strings.Contains(resultText(view), `"bank_text_redacted":true`) || strings.Contains(resultText(view), "COFFEE") {
		t.Errorf("redaction for a read-only token: %q", resultText(view))
	}
	for _, tool := range []string{"confirm_payment_match", "run_payment_matching"} {
		args := map[string]any{"id": first.ID, "version": 1}
		if tool == "run_payment_matching" {
			args = nil
		}
		if r := call(t, rc, tool, args); !r.IsError || !strings.Contains(resultText(r), "commitments:write") {
			t.Errorf("%s without the write grant: %q", tool, resultText(r))
		}
	}

	// Every write call is audited (the engine's matches naming the system is asserted above).
	tools := map[string]int{}
	for _, e := range f.auditEntries(t, audit.MCPToolCalled) {
		tools[e.Details["tool"]]++
	}
	for _, tool := range []string{"run_payment_matching", "confirm_payment_match", "reject_payment_match", "correct_payment_match", "unmatch_payment_match", "record_manual_payment", "delete_manual_payment"} {
		if tools[tool] == 0 {
			t.Errorf("no mcp.tool_called entry for %s: %v", tool, tools)
		}
	}
}
