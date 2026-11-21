package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
)

type viaMatch struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Version   int64  `json:"version"`
	DecidedBy *struct {
		Kind string  `json:"kind"`
		Via  *string `json:"via"`
	} `json:"decided_by"`
	History []struct {
		Action string `json:"action"`
		By     struct {
			Kind string  `json:"kind"`
			Via  *string `json:"via"`
		} `json:"by"`
	} `json:"history"`
}

func (m viaMatch) lastVia() string {
	if len(m.History) == 0 {
		return "<none>"
	}
	return via(m.History[len(m.History)-1].By.Via)
}

// TestMCPUnconfirmAndProvenance (D42 1a, 3a): a token counts as its owner, so
// the person who confirmed a match may unconfirm it over MCP or with the token
// directly; another administrator's token may not. Every event records how it
// came: mcp for the in-process MCP server and for 'ledger mcp stdio' (which
// names its tool in a header), token for a direct API call.
func TestMCPUnconfirmAndProvenance(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	importFakeBankInto(t, f.db)
	secret, _ := f.token(t, f.admin, "commitments:write")
	c := f.connect(t, secret)
	carol, err := f.parts.users.Create(context.Background(), audit.System, users.New{Username: "carol", Role: authz.RoleAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	carolSecret, _ := f.token(t, carol, "commitments:write")
	cc := f.connect(t, carolSecret)

	// No tolerance: 12.99 against 13.49 is only a suggestion.
	args := map[string]any{}
	if err := json.Unmarshal([]byte(`{"name":"Streaming","kind":"subscription","amount":{"amount":"13.49","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-01-01"}`), &args); err != nil {
		t.Fatal(err)
	}
	if r := call(t, c, "create_commitment", args); r.IsError {
		t.Fatal(resultText(r))
	}
	if r := call(t, c, "run_payment_matching", nil); r.IsError {
		t.Fatal(resultText(r))
	}
	queue := decodeJSONText[struct{ Items []viaMatch }](t, call(t, c, "list_payment_matches", map[string]any{"state": []string{"suggested"}}))
	if len(queue.Items) != 1 {
		t.Fatalf("queue: %+v", queue)
	}
	s := queue.Items[0]

	confirmed := decodeJSONText[viaMatch](t, call(t, c, "confirm_payment_match", map[string]any{"id": s.ID, "version": s.Version}))
	if confirmed.DecidedBy == nil || confirmed.DecidedBy.Kind != "token" || via(confirmed.DecidedBy.Via) != "mcp" || confirmed.lastVia() != "mcp" || via(confirmed.History[0].By.Via) != "system" {
		t.Fatalf("confirmed over MCP: %+v", confirmed)
	}
	// Another administrator's token is not the decider.
	if r := call(t, cc, "unconfirm_payment_match", map[string]any{"id": s.ID, "version": confirmed.Version}); !r.IsError || !strings.Contains(resultText(r), "match_undo_not_decider") {
		t.Errorf("another person's token: %q", resultText(r))
	}
	if r := call(t, c, "unconfirm_payment_match", map[string]any{"id": s.ID, "version": confirmed.Version - 1}); !r.IsError || !strings.Contains(resultText(r), "version_conflict") {
		t.Errorf("stale version: %q", resultText(r))
	}
	u := decodeJSONText[viaMatch](t, call(t, c, "unconfirm_payment_match", map[string]any{"id": s.ID, "version": confirmed.Version, "reason": "not this one"}))
	if u.State != "suggested" || u.DecidedBy != nil || u.History[len(u.History)-1].Action != "unconfirmed" || u.lastVia() != "mcp" {
		t.Fatalf("unconfirmed over MCP: %+v", u)
	}

	// The token directly, then as 'ledger mcp stdio' would call.
	api := func(path, body string, hdr map[string]string) viaMatch {
		t.Helper()
		req, err := http.NewRequest("POST", f.ts.URL+"/api/v1"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
		var m viaMatch
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	byToken := api("/payment-matches/"+s.ID+"/confirm", `{"version":`+strconv.FormatInt(u.Version, 10)+`}`, nil)
	if via(byToken.DecidedBy.Via) != "token" || byToken.lastVia() != "token" {
		t.Errorf("confirmed with the token directly: %+v", byToken)
	}
	stdio := api("/payment-matches/"+s.ID+"/unconfirm", `{"version":`+strconv.FormatInt(byToken.Version, 10)+`}`, map[string]string{httpapi.MCPToolHeader: "unconfirm_payment_match"})
	if stdio.State != "suggested" || stdio.lastVia() != "mcp" {
		t.Errorf("unconfirmed as the stdio server calls: %+v", stdio)
	}

	// The audit says how each came.
	vias := []string{}
	for _, e := range f.auditEntries(t, audit.PaymentMatchUnconfirmed) {
		vias = append(vias, e.Details["via"])
	}
	if strings.Join(vias, ",") != "mcp,mcp" {
		t.Errorf("payment_match.unconfirmed vias: %v", vias)
	}
	tools := 0
	for _, e := range f.auditEntries(t, audit.MCPToolCalled) {
		if e.Details["tool"] == "unconfirm_payment_match" {
			tools++
		}
	}
	if tools < 2 {
		t.Errorf("mcp.tool_called entries for unconfirm_payment_match: %d", tools)
	}
}
