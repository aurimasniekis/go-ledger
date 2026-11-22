package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
)

// Cooldown tests (D-B6c): after a refresh finished, a refresh on request for
// that connection is refused for banking.refresh.request_cooldown with 429
// refresh_cooldown and Retry-After, through the API, the CLI and MCP (in
// process and "ledger mcp stdio"). Fake clocks and fake providers only.

var cooldownT0 = time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)

// connectedAPI returns a fixture with an active connection and the cooldown on.
func connectedAPI(t *testing.T) (*apiFixture, bankCall, string) {
	t.Helper()
	f := newAPIFixture(t)
	f.now = cooldownT0
	f.connections.RefreshCooldown = 5 * time.Minute
	c := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	_, body, _ := c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
	id := asMap(t, body["connection"])["id"].(string)
	state := stateOf(t, body["authorization_url"].(string))
	complete := `{"state":"` + state + `","code":"` + bankingtest.AuthorizationCode(state).Reveal() + `"}`
	if code, body, _ := c.do("POST", "/api/v1/bank-connections/complete", complete); code != 200 {
		t.Fatalf("complete: %d %v", code, body)
	}
	return f, c, id
}

func TestAPIRefreshCooldown(t *testing.T) {
	f, c, id := connectedAPI(t)
	path := "/api/v1/bank-connections/" + id + "/refresh"
	code, body, _ := c.do("POST", path, "")
	if code != 200 || body["refresh_available_at"] != cooldownT0.Add(5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("first refresh: %d %v", code, body)
	}
	firstRun := asMap(t, body["last_refresh_run"])["id"]
	f.now = cooldownT0.Add(2 * time.Minute)
	code, body, hdr := c.do("POST", path, "")
	if code != 429 || body["code"] != "refresh_cooldown" || hdr.Get("Retry-After") != "180" {
		t.Fatalf("inside the window: %d %v Retry-After=%q, want 429 refresh_cooldown 180", code, body, hdr.Get("Retry-After"))
	}
	if !strings.Contains(body["message"].(string), "3 min") {
		t.Errorf("message %q does not say how long", body["message"])
	}
	// Reading stays allowed and says when a refresh is allowed again.
	if code, body, _ := c.do("GET", "/api/v1/bank-connections/"+id, ""); code != 200 || body["refresh_available_at"] != cooldownT0.Add(5*time.Minute).Format(time.RFC3339) {
		t.Errorf("get: %d %v", code, body)
	}
	// A refused request records no run.
	if _, b, _ := c.do("GET", "/api/v1/bank-connections/"+id, ""); asMap(t, b["last_refresh_run"])["id"] != firstRun {
		t.Errorf("a refused request must not start a run: %v", b["last_refresh_run"])
	}
	f.now = cooldownT0.Add(5 * time.Minute)
	if code, body, _ := c.do("POST", path, ""); code != 200 || body["refresh_available_at"] != cooldownT0.Add(10*time.Minute).Format(time.RFC3339) {
		t.Fatalf("after the window: %d %v", code, body)
	}
	f.now = cooldownT0.Add(11 * time.Minute)
	if code, body, _ := c.do("GET", "/api/v1/bank-connections/"+id, ""); code != 200 || body["refresh_available_at"] != nil {
		t.Errorf("after the second window refresh_available_at must be null: %v", body["refresh_available_at"])
	}
}

func TestCLIRefreshCooldown(t *testing.T) {
	c := newCLIFixture(t)
	c.api.now = cooldownT0
	c.api.connections.RefreshCooldown = 5 * time.Minute
	bc := bankCall{t: t, f: c.api, hdr: c.api.signIn(t, "alice")}
	_, body, _ := bc.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
	id := asMap(t, body["connection"])["id"].(string)
	state := stateOf(t, body["authorization_url"].(string))
	if code, body, _ := bc.do("POST", "/api/v1/bank-connections/complete", `{"state":"`+state+`","code":"`+bankingtest.AuthorizationCode(state).Reveal()+`"}`); code != 200 {
		t.Fatalf("complete: %d %v", code, body)
	}
	if rc, out, errb := c.run(adminToken, "bank", "refresh", id); rc != 0 {
		t.Fatalf("first refresh: %d %s %s", rc, out, errb)
	}
	c.api.now = cooldownT0.Add(90 * time.Second)
	rc, _, errb := c.run(adminToken, "bank", "refresh", id)
	if rc != 1 || !strings.Contains(errb, "429 refresh_cooldown") || !strings.Contains(errb, "retry after 210 seconds") {
		t.Fatalf("inside the window: rc=%d %q", rc, errb)
	}
	c.api.now = cooldownT0.Add(5 * time.Minute)
	if rc, out, errb := c.run(adminToken, "bank", "refresh", id); rc != 0 {
		t.Fatalf("after the window: %d %s %s", rc, out, errb)
	}
}

func TestMCPRefreshCooldownInProcessAndStdio(t *testing.T) {
	ctx := context.Background()
	f := newMCPFixture(t, mcpOpts{})
	f.parts.banking.RefreshCooldown = 5 * time.Minute
	st, err := f.parts.banking.Start(ctx, audit.System, bankconnect.Start{Application: f.bank.Capabilities().Application, InstitutionID: bankingtest.InstitutionID, Country: bankingtest.InstitutionCountry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.parts.banking.Complete(ctx, audit.System, svcRedirect(stateOf(t, st.AuthorizationURL))); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"id": string(st.Connection.ID)}
	secret, _ := f.token(t, f.admin, "connections:write")

	inProcess := f.connect(t, secret)
	if r := call(t, inProcess, "refresh_bank_connection", args); r.IsError {
		t.Fatalf("first refresh: %s", resultText(r))
	}
	wantRefused := func(who string, r *mcp.CallToolResult) {
		t.Helper()
		txt := resultText(r)
		if !r.IsError || !strings.Contains(txt, "HTTP 429") || !strings.Contains(txt, "refresh_cooldown") || !strings.Contains(txt, "retry after 300 seconds") {
			t.Errorf("%s: %q, want a 429 refresh_cooldown refusal with the wait", who, txt)
		}
	}
	wantRefused("in process", call(t, inProcess, "refresh_bank_connection", args))

	// "ledger mcp stdio" is a client of the server's API: same refusal.
	ct, stt := mcp.NewInMemoryTransports()
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan int, 1)
	var stderr bytes.Buffer
	env := mcpEnv{Transport: stt, mgmtEnv: mgmtEnv{Getenv: func(k string) string {
		switch k {
		case "LEDGER_TOKEN":
			return secret
		case "LEDGER_URL":
			return f.ts.URL
		}
		return ""
	}, ReadFile: func(string) ([]byte, error) { return nil, io.EOF }}}
	go func() { done <- runMCP(sctx, []string{"stdio", "--insecure"}, io.Discard, &stderr, env) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(sctx, ct, nil)
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	t.Cleanup(func() { _ = cs.Close(); cancel(); <-done })
	wantRefused("stdio", call(t, cs, "refresh_bank_connection", args))
}
