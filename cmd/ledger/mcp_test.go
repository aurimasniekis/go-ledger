package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/mcpserver"
	"github.com/aurimasniekis/go-ledger/internal/store"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
	paymentstore "github.com/aurimasniekis/go-ledger/internal/store/payments"
)

// mcpFixture is the real API with real API tokens (hashed in the database, the
// token authenticator, the audit table) and the MCP endpoint in front of it.
type mcpFixture struct {
	srv   *httpapi.Server
	ts    *httptest.Server
	db    *store.DB
	al    *store.AuditLog
	parts apiParts
	admin users.User
	ro    users.User
	// bank is the fake provider behind the bank connection routes.
	bank *bankingtest.Provider
	// ratesDouble and job are the Frankfurter test double and the refresh job
	// behind the rates routes.
	ratesDouble *frankfurterDouble
	job         *fx.RefreshJob
}

type mcpOpts struct {
	disabled bool
	limiter  *mcpserver.Limiter
	maxBytes int64
}

var mcpNow = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func newMCPFixture(t *testing.T, o mcpOpts) *mcpFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	al := store.NewAuditLog(db)
	parts := mustParts(t, db, al)
	clock := func() time.Time { return mcpNow }
	parts.commitments = commitmentstore.New(db, al, clock, nil)
	parts.payments = paymentstore.New(db, al, clock, nil)
	parts.matches = matchstore.New(db, al, clock, nil)
	bank, _, svc := newFakeBanking(db, al, clock)
	parts.banking = svc
	f := &mcpFixture{db: db, al: al, parts: parts, bank: bank}
	f.ratesDouble, f.job, _, f.parts.rateCache = newRatesRig(t, db, al, func() time.Time { return time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC) })
	f.admin, err = parts.users.Create(ctx, audit.System, users.New{Username: "alice", Role: authz.RoleAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	f.ro, err = parts.users.Create(ctx, audit.System, users.New{Username: "bob", Role: authz.RoleReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httpapi.New(config.HTTP{MaxBodyBytes: 1 << 20, ShutdownTimeout: time.Second}, slog.New(slog.DiscardHandler),
		httpapi.WithAuthenticators(httpapi.NewTokenAuth(parts.tokens)), httpapi.WithAuditRecorder(al, nil))
	if !o.disabled {
		msrv, err := mcpserver.NewServer(mcpserver.Options{Caller: mcpserver.InProcess(f.srv.Handler()), Audit: al, Version: "test", Now: clock})
		if err != nil {
			t.Fatal(err)
		}
		f.parts.mcp = mcpserver.Handler(msrv, mcpserver.HandlerOptions{Limiter: o.limiter, MaxRequestBytes: o.maxBytes})
	}
	registerAPI(f.srv, al, f.parts, calendar.MustLoadZone("UTC"), clock)
	f.ts = httptest.NewServer(f.srv.Handler())
	t.Cleanup(f.ts.Close)
	return f
}

// token issues a token owned by the administrator (or, with owner set, by
// that user) holding grants, and returns its secret and ID.
func (f *mcpFixture) token(t *testing.T, owner users.User, grants ...string) (secret, id string) {
	t.Helper()
	var gs authz.Grants
	for _, s := range grants {
		g, err := authz.ParseGrant(s)
		if err != nil {
			t.Fatal(err)
		}
		gs = append(gs, g)
	}
	iss, err := f.parts.tokens.Create(context.Background(), audit.System, authz.UserPrincipal(owner.ID, owner.Role), tokens.New{Name: "mcp-test", Grants: gs})
	if err != nil {
		t.Fatal(err)
	}
	return iss.Secret, iss.Token.ID
}

type bearerTransport struct{ secret string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.secret)
	return http.DefaultTransport.RoundTrip(r)
}

func (f *mcpFixture) connect(t *testing.T, secret string) *mcp.ClientSession {
	t.Helper()
	tr := &mcp.StreamableClientTransport{Endpoint: f.ts.URL + httpapi.MCPPath, HTTPClient: &http.Client{Transport: bearerTransport{secret}}, DisableStandaloneSSE: true}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func newCommitmentArgs(name string) map[string]any {
	return map[string]any{
		"name": name, "kind": "subscription",
		"amount":     map[string]any{"amount": "12.50", "currency": "EUR"},
		"schedule":   map[string]any{"version": 1, "frequency": "monthly", "interval": 1, "day": 20},
		"start_date": "2026-11-01",
	}
}

func (f *mcpFixture) auditEntries(t *testing.T, action audit.Action) []audit.Record {
	t.Helper()
	got, err := f.al.List(context.Background(), audit.Filter{Actions: []audit.Action{action}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *mcpFixture) post(t *testing.T, secret, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", f.ts.URL+httpapi.MCPPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// TestMCPClientListsToolsAndCallsReadTool is the P-MCP1 acceptance path: a
// client authenticates with an API token, lists the tools and calls a read tool.
func TestMCPClientListsToolsAndCallsReadTool(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	secret, _ := f.token(t, f.admin, "commitments:write")
	cs := f.connect(t, secret)

	// A commitment made through the HTTP API is visible through MCP.
	body, _ := json.Marshal(newCommitmentArgs("Streaming"))
	req, _ := http.NewRequest("POST", f.ts.URL+"/api/v1/commitments", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create over HTTP: %v %v", err, resp)
	}
	_ = resp.Body.Close()

	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := mcpserver.Tools()
	if len(lt.Tools) != len(want) {
		t.Fatalf("listed %d tools, registry has %d", len(lt.Tools), len(want))
	}
	byName := map[string]*mcp.Tool{}
	for _, tl := range lt.Tools {
		byName[tl.Name] = tl
		if tl.Description == "" || tl.InputSchema == nil || tl.Annotations == nil {
			t.Errorf("tool %s lacks a description, schema or annotations", tl.Name)
		}
	}
	for _, w := range want {
		a := byName[w.Name].Annotations
		switch w.Kind {
		case mcpserver.KindRead:
			if !a.ReadOnlyHint {
				t.Errorf("%s must be read-only", w.Name)
			}
		case mcpserver.KindWrite:
			if a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
				t.Errorf("%s must be a non-destructive write: %+v", w.Name, a)
			}
		case mcpserver.KindDestructive:
			if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Errorf("%s must be destructive: %+v", w.Name, a)
			}
		}
	}

	res := call(t, cs, "list_commitments", map[string]any{"kind": []string{"subscription"}})
	if res.IsError {
		t.Fatalf("list_commitments: %s", resultText(res))
	}
	var page struct {
		Items []struct {
			Name   string
			Amount struct{ Amount, Currency string }
		}
	}
	if err := json.Unmarshal([]byte(resultText(res)), &page); err != nil || len(page.Items) != 1 || page.Items[0].Name != "Streaming" || page.Items[0].Amount.Amount != "12.50" {
		t.Fatalf("list_commitments result %s (%v)", resultText(res), err)
	}
	if res.StructuredContent == nil {
		t.Error("a read result carries structured content")
	}

	sum := call(t, cs, "get_month_summary", map[string]any{"month": "2026-11"})
	if sum.IsError || !strings.Contains(resultText(sum), "freshness") || !strings.Contains(resultText(sum), "12.50") {
		t.Fatalf("get_month_summary: %s", resultText(sum))
	}
	occ := call(t, cs, "list_occurrences", map[string]any{"month": "2026-11"})
	if occ.IsError || !strings.Contains(resultText(occ), "2026-11-20") {
		t.Fatalf("list_occurrences: %s", resultText(occ))
	}

	// Resources: read-only context without secrets.
	rl, err := cs.ListResources(context.Background(), nil)
	if err != nil || len(rl.Resources) != 3 {
		t.Fatalf("resources: %v %v", rl, err)
	}
	me, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "ledger://me"})
	if err != nil || !strings.Contains(me.Contents[0].Text, "commitments:write") {
		t.Fatalf("ledger://me: %v %v", me, err)
	}
	if strings.Contains(me.Contents[0].Text, secret) {
		t.Error("a resource must never carry the token")
	}
}

func TestMCPRequiresAnAPIToken(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	secret, id := f.token(t, f.admin, "commitments:read")
	if got := f.post(t, "", initBody, nil).StatusCode; got != 401 {
		t.Errorf("no token: %d, want 401", got)
	}
	if got := f.post(t, "ldgr_notatoken", initBody, nil).StatusCode; got != 401 {
		t.Errorf("bad token: %d, want 401", got)
	}
	if got := f.post(t, secret, initBody, nil).StatusCode; got != 200 {
		t.Errorf("valid token: %d, want 200", got)
	}
	// GET and DELETE are not served (stateless transport).
	for _, m := range []string{"GET", "DELETE"} {
		req, _ := http.NewRequest(m, f.ts.URL+httpapi.MCPPath, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 405 {
			t.Errorf("%s: %d, want 405", m, resp.StatusCode)
		}
	}
	// Revocation applies to the very next MCP request.
	cs := f.connect(t, secret)
	if res := call(t, cs, "list_commitments", nil); res.IsError {
		t.Fatalf("before revoke: %s", resultText(res))
	}
	if err := f.parts.tokens.Revoke(context.Background(), audit.System, id, f.admin.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.post(t, secret, initBody, nil).StatusCode; got != 401 {
		t.Errorf("revoked token: %d, want 401", got)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_commitments"}); err == nil {
		t.Error("a revoked token's open client must fail")
	}
}

func TestMCPDisabledAnswers404(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{disabled: true})
	secret, _ := f.token(t, f.admin, "commitments:read")
	if got := f.post(t, secret, initBody, nil).StatusCode; got != 404 {
		t.Errorf("disabled: %d, want 404", got)
	}
	if got := f.post(t, "", initBody, nil).StatusCode; got != 401 {
		t.Errorf("disabled, no token: %d, want 401", got)
	}
}

func TestMCPRefusesCrossSiteBrowserRequests(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	secret, _ := f.token(t, f.admin, "commitments:read")
	resp := f.post(t, secret, initBody, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != 403 {
		t.Errorf("cross-site: %d, want 403", resp.StatusCode)
	}
}

func TestMCPRateAndSizeLimits(t *testing.T) {
	now := time.Unix(1000, 0)
	lim := mcpserver.NewLimiter(60, 2, func() time.Time { return now })
	f := newMCPFixture(t, mcpOpts{limiter: lim, maxBytes: 2048})
	a, _ := f.token(t, f.admin, "commitments:read")
	b, _ := f.token(t, f.admin, "commitments:read")
	for i := 0; i < 2; i++ {
		if got := f.post(t, a, initBody, nil).StatusCode; got != 200 {
			t.Fatalf("request %d: %d", i, got)
		}
	}
	resp := f.post(t, a, initBody, nil)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("third request: %d retry-after %q, want 429 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	var env struct{ Code string }
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Code != "rate_limited" {
		t.Errorf("code %q, want rate_limited", env.Code)
	}
	// Another token has its own bucket; time refills the first.
	if got := f.post(t, b, initBody, nil).StatusCode; got != 200 {
		t.Errorf("other token: %d", got)
	}
	now = now.Add(2 * time.Second)
	if got := f.post(t, a, initBody, nil).StatusCode; got != 200 {
		t.Errorf("after refill: %d", got)
	}
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + strings.Repeat("x", 4096) + `"}}`
	if got := f.post(t, b, big, nil).StatusCode; got != 413 {
		t.Errorf("oversized body: %d, want 413", got)
	}
}

// TestMCPRefusesBatches: a JSON-RPC batch must not get around the per-token
// rate limit, whatever protocol version the client claims (or none).
func TestMCPRefusesBatches(t *testing.T) {
	now := time.Unix(1000, 0)
	lim := mcpserver.NewLimiter(1, 1, func() time.Time { return now })
	f := newMCPFixture(t, mcpOpts{limiter: lim})
	secret, _ := f.token(t, f.admin, "commitments:write")
	var msgs []string
	for i := 0; i < 25; i++ {
		msgs = append(msgs, `{"jsonrpc":"2.0","id":`+strconv.Itoa(i+1)+`,"method":"tools/call","params":{"name":"create_commitment","arguments":{"name":"B`+strconv.Itoa(i)+`","kind":"service"}}}`)
	}
	for _, lead := range []string{"", " \n\t"} {
		batch := lead + "[" + strings.Join(msgs, ",") + "]"
		now = now.Add(time.Hour) // a fresh burst each round
		if got := f.post(t, secret, batch, nil).StatusCode; got != 400 {
			t.Errorf("batch %q: %d, want 400", lead, got)
		}
	}
	if n := len(f.auditEntries(t, audit.TokenWriteUsed)); n != 0 {
		t.Errorf("a refused batch created %d writes", n)
	}
	// A single message still works under the same limiter.
	now = now.Add(time.Hour)
	if got := f.post(t, secret, initBody, nil).StatusCode; got != 200 {
		t.Errorf("single message: %d, want 200", got)
	}
}

// TestMCPMissingGrantIsAClearPermissionError and the audit of every write call.
func TestMCPMissingGrantAndWriteAudit(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	reader, readerID := f.token(t, f.admin, "commitments:read")
	writer, writerID := f.token(t, f.admin, "commitments:write")
	// A token whose owner is read-only: the write grant is cut down by the role.
	capped, cappedID := f.token(t, f.ro, "commitments:read")

	rc := f.connect(t, reader)
	res := call(t, rc, "create_commitment", newCommitmentArgs("Denied"))
	if !res.IsError || !strings.Contains(resultText(res), "commitments:write") || !strings.Contains(resultText(res), "permission denied") {
		t.Fatalf("a missing grant must be a clear permission error naming the grant: %q", resultText(res))
	}
	if list := call(t, rc, "list_commitments", nil); strings.Contains(resultText(list), "Denied") {
		t.Fatal("a denied call changed state")
	}
	// A read grant is enough for reads; a write grant also reads.
	if r := call(t, f.connect(t, writer), "list_commitments", nil); r.IsError {
		t.Fatalf("write implies read: %s", resultText(r))
	}
	cr := call(t, f.connect(t, capped), "delete_commitment", map[string]any{"id": "01JD3Y4C8V9M2Q7W5R6T0XZBNT", "version": 1})
	if !cr.IsError || !strings.Contains(resultText(cr), "commitments:write") {
		t.Fatalf("capped token delete: %q", resultText(cr))
	}

	wc := f.connect(t, writer)
	ok := call(t, wc, "create_commitment", newCommitmentArgs("Allowed"))
	if ok.IsError || !strings.Contains(resultText(ok), "Allowed") {
		t.Fatalf("create_commitment: %s", resultText(ok))
	}
	var created struct {
		ID      string
		Version int64
	}
	if err := json.Unmarshal([]byte(resultText(ok)), &created); err != nil || created.ID == "" {
		t.Fatal(err, resultText(ok))
	}
	// Invalid input is rejected by the schema, and by the API's validation when
	// the schema allows it; both are audited as failed writes.
	if r := call(t, wc, "create_commitment", map[string]any{"name": "x"}); !r.IsError || !strings.Contains(resultText(r), "invalid arguments") {
		t.Fatalf("schema rejection: %q", resultText(r))
	}
	bad := newCommitmentArgs("Bad")
	bad["amount"] = map[string]any{"amount": "0", "currency": "EUR"}
	if r := call(t, wc, "create_commitment", bad); !r.IsError || !strings.Contains(resultText(r), "validation") {
		t.Fatalf("API validation: %q", resultText(r))
	}
	// Destructive call, with the optimistic version.
	if r := call(t, wc, "delete_commitment", map[string]any{"id": created.ID, "version": created.Version + 5}); !r.IsError || !strings.Contains(resultText(r), "version_conflict") {
		t.Fatalf("stale delete: %q", resultText(r))
	}
	if r := call(t, wc, "delete_commitment", map[string]any{"id": created.ID, "version": created.Version}); r.IsError {
		t.Fatalf("delete: %s", resultText(r))
	}

	type row struct{ tool, outcome, token string }
	var got []row
	for _, e := range f.auditEntries(t, audit.MCPToolCalled) {
		if e.Actor.Kind != audit.ActorToken || e.Actor.UserID == "" {
			t.Errorf("entry %+v does not name a token actor", e)
		}
		got = append(got, row{e.Details["tool"], e.Details["outcome"], e.Actor.TokenID})
	}
	wantRows := []row{
		{"create_commitment", "denied", readerID},
		{"delete_commitment", "denied", cappedID},
		{"create_commitment", "succeeded", writerID},
		{"create_commitment", "failed", writerID},
		{"create_commitment", "failed", writerID},
		{"delete_commitment", "failed", writerID},
		{"delete_commitment", "succeeded", writerID},
	}
	if len(got) != len(wantRows) {
		t.Fatalf("audit rows %v, want %v", got, wantRows)
	}
	for i := range wantRows {
		if got[i] != wantRows[i] {
			t.Errorf("audit row %d = %v, want %v", i, got[i], wantRows[i])
		}
	}
	// Reads are not audited as MCP calls.
	for _, r := range got {
		if r.tool == "list_commitments" {
			t.Error("a read call was audited")
		}
	}
	// The operation itself is still audited as a token write, and no entry or
	// table holds a token secret.
	if n := len(f.auditEntries(t, audit.TokenWriteUsed)); n != 4 {
		t.Errorf("token.write_used entries = %d, want 4 (the writes that reached the API: not the schema rejection, not the denied ones)", n)
	}
	dump := dumpDatabase(t, f.db)
	for _, s := range []string{reader, writer, capped} {
		if strings.Contains(dump, s) {
			t.Fatal("a token secret is stored in the database in the clear")
		}
	}
}

// TestMCPHTTPGrantParity: the grant a tool declares is the grant of the route it
// maps to, and for every tool and every kind of token the MCP outcome (allowed
// or permission error) is the HTTP outcome (not 403, or 403).
const fakeID = "01JD3Y4C8V9M2Q7W5R6T0XZBNT"

func createMatchArgs() map[string]any {
	return map[string]any{"transaction_id": fakeID, "occurrence_id": fakeID, "amount": map[string]any{"amount": "1.00", "currency": "EUR"}}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestMCPHTTPGrantParity(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	routes := map[string]httpapi.Route{}
	for _, r := range f.srv.APIRoutes(httpapi.APIPrefix) {
		routes[r.Method+" "+strings.TrimPrefix(r.Path, httpapi.APIPrefix)] = r
	}
	for _, tl := range mcpserver.Tools() {
		r, ok := routes[tl.Method+" "+tl.Path]
		if !ok {
			t.Errorf("tool %s maps to %s %s, which is not a registered route", tl.Name, tl.Method, tl.Path)
			continue
		}
		if g, ok := r.Access.Grant(); !ok || g != tl.Grant {
			t.Errorf("tool %s declares grant %s, its route requires %s", tl.Name, tl.Grant, r.Access)
		}
	}

	// A tool that takes an object_type declares the merchants grant the route's merchantGate
	// asks for: read for a read, write for a change. The route is the authority; its answer to a
	// merchant object is checked below with and without that grant.
	matched := 0
	for _, tl := range mcpserver.Tools() {
		if !strings.HasPrefix(tl.Path, "/organisation/{object_type}/") {
			continue
		}
		matched++
		want := authz.G(authz.ResourceMerchants, authz.ActionWrite)
		if tl.Kind == mcpserver.KindRead {
			want = authz.G(authz.ResourceMerchants, authz.ActionRead)
		}
		if g, ok := tl.ObjectGrants["merchant"]; !ok || g != want {
			t.Errorf("tool %s declares object grants %v for merchant, the route's merchantGate needs %s", tl.Name, tl.ObjectGrants, want)
		}
	}
	// Zero matches would pass the loop above vacuously, so the guard must not be silent.
	if matched == 0 {
		t.Fatal("no MCP tool maps to /organisation/{object_type}/: the merchant grant parity check would test nothing")
	}

	// Real objects for the argument samples.
	adminSecret, _ := f.token(t, f.admin, "commitments:write")
	ac := f.connect(t, adminSecret)
	created := call(t, ac, "create_commitment", newCommitmentArgs("Parity"))
	var c struct {
		ID      string
		Version int64
	}
	_ = json.Unmarshal([]byte(resultText(created)), &c)
	var occ struct{ Items []struct{ ID string } }
	_ = json.Unmarshal([]byte(resultText(call(t, ac, "list_occurrences", map[string]any{"month": "2026-11"}))), &occ)
	if c.ID == "" || len(occ.Items) == 0 {
		t.Fatalf("no sample objects: %s", resultText(created))
	}
	httpReq := map[string]func() (string, string, string){
		"list_commitments":  func() (string, string, string) { return "GET", "/commitments", "" },
		"get_commitment":    func() (string, string, string) { return "GET", "/commitments/" + c.ID, "" },
		"list_occurrences":  func() (string, string, string) { return "GET", "/occurrences?month=2026-11", "" },
		"get_month_summary": func() (string, string, string) { return "GET", "/occurrences/summary?month=2026-11", "" },
		"get_outlook":       func() (string, string, string) { return "GET", "/outlook", "" },
		"get_occurrence":    func() (string, string, string) { return "GET", "/occurrences/" + occ.Items[0].ID, "" },
		"create_commitment": func() (string, string, string) {
			b, _ := json.Marshal(newCommitmentArgs("Parity 2"))
			return "POST", "/commitments", string(b)
		},
		"delete_commitment": func() (string, string, string) { return "DELETE", "/commitments/01JD3Y4C8V9M2Q7W5R6T0XZBNT", "" },

		"list_payment_matches": func() (string, string, string) { return "GET", "/payment-matches", "" },
		"get_payment_match":    func() (string, string, string) { return "GET", "/payment-matches/" + fakeID, "" },
		"create_payment_match": func() (string, string, string) {
			return "POST", "/payment-matches", string(mustJSON(createMatchArgs()))
		},
		"confirm_payment_match": func() (string, string, string) {
			return "POST", "/payment-matches/" + fakeID + "/confirm", `{"version":1}`
		},
		"correct_payment_match": func() (string, string, string) {
			return "POST", "/payment-matches/" + fakeID + "/correct", `{"version":1,"amount":{"amount":"1.00","currency":"EUR"}}`
		},
		"reject_payment_match": func() (string, string, string) {
			return "POST", "/payment-matches/" + fakeID + "/reject", `{"version":1}`
		},
		"unmatch_payment_match": func() (string, string, string) {
			return "POST", "/payment-matches/" + fakeID + "/unmatch", `{"version":1}`
		},
		"reverse_payment_match": func() (string, string, string) {
			return "POST", "/payment-matches/" + fakeID + "/reverse", `{"version":1}`
		},
		"unconfirm_payment_match": func() (string, string, string) {
			return "POST", "/payment-matches/" + fakeID + "/unconfirm", `{"version":1}`
		},
		"confirm_payment_match_replacing": func() (string, string, string) {
			return "POST", "/payment-matches/" + fakeID + "/confirm-replacing", `{"version":1,"manual_payment_ids":["` + fakeID + `"]}`
		},
		"run_payment_matching": func() (string, string, string) { return "POST", "/payment-matching/runs", "{}" },
		"record_manual_payment": func() (string, string, string) {
			return "POST", "/occurrences/" + fakeID + "/manual-payments", `{"amount":{"amount":"1.00","currency":"EUR"},"paid_on":"2026-11-01"}`
		},
		"delete_manual_payment": func() (string, string, string) {
			return "DELETE", "/occurrences/" + fakeID + "/manual-payments/" + fakeID, ""
		},

		"list_deferral_plans":        func() (string, string, string) { return "GET", "/deferral-plans", "" },
		"get_deferral_plan":          func() (string, string, string) { return "GET", "/deferral-plans/" + fakeID, "" },
		"list_deferral_plan_history": func() (string, string, string) { return "GET", "/deferral-plans/" + fakeID + "/history", "" },
		"create_deferral_plan": func() (string, string, string) {
			return "POST", "/deferral-plans", `{"occurrence_id":"` + fakeID + `","plan_type":"personal","planned_date":"2026-12-01"}`
		},
		"revise_deferral_plan": func() (string, string, string) {
			return "PATCH", "/deferral-plans/" + fakeID, `{"version":1,"planned_date":"2026-12-02"}`
		},
		"undo_deferral_plan": func() (string, string, string) { return "POST", "/deferral-plans/" + fakeID + "/undo", `{"version":1}` },
		"add_deferral_adjustment": func() (string, string, string) {
			return "POST", "/deferral-plans/" + fakeID + "/adjustments", `{"version":1,"kind":"fee","amount":{"amount":"1.00","currency":"EUR"}}`
		},
		"remove_deferral_adjustment": func() (string, string, string) {
			return "DELETE", "/deferral-plans/" + fakeID + "/adjustments/" + fakeID, ""
		},
		"mark_deferral_adjustment_paid": func() (string, string, string) {
			return "POST", "/deferral-plans/" + fakeID + "/adjustments/" + fakeID + "/paid", ""
		},
		"unmark_deferral_adjustment_paid": func() (string, string, string) {
			return "POST", "/deferral-plans/" + fakeID + "/adjustments/" + fakeID + "/unpaid", ""
		},

		"list_bank_applications": func() (string, string, string) { return "GET", "/bank-applications", "" },
		"list_bank_institutions": func() (string, string, string) {
			return "GET", "/bank-applications/fake-app/institutions?country=FI", ""
		},
		"list_bank_connections":   func() (string, string, string) { return "GET", "/bank-connections", "" },
		"get_bank_connection":     func() (string, string, string) { return "GET", "/bank-connections/" + fakeID, "" },
		"refresh_bank_connection": func() (string, string, string) { return "POST", "/bank-connections/" + fakeID + "/refresh", "{}" },

		"list_exchange_rates":      func() (string, string, string) { return "GET", "/rates", "" },
		"get_exchange_rate_status": func() (string, string, string) { return "GET", "/rates/status", "" },

		"list_categories":     func() (string, string, string) { return "GET", "/categories", "" },
		"get_category":        func() (string, string, string) { return "GET", "/categories/" + fakeID, "" },
		"create_category":     func() (string, string, string) { return "POST", "/categories", `{"name":"Parity"}` },
		"update_category":     func() (string, string, string) { return "PATCH", "/categories/" + fakeID, `{"name":"Parity 2"}` },
		"archive_category":    func() (string, string, string) { return "POST", "/categories/" + fakeID + "/archive", "{}" },
		"unarchive_category":  func() (string, string, string) { return "POST", "/categories/" + fakeID + "/unarchive", "{}" },
		"delete_category":     func() (string, string, string) { return "DELETE", "/categories/" + fakeID, "" },
		"list_labels":         func() (string, string, string) { return "GET", "/labels", "" },
		"get_label":           func() (string, string, string) { return "GET", "/labels/" + fakeID, "" },
		"create_label":        func() (string, string, string) { return "POST", "/labels", `{"name":"Parity"}` },
		"update_label":        func() (string, string, string) { return "PATCH", "/labels/" + fakeID, `{"name":"Parity 2"}` },
		"archive_label":       func() (string, string, string) { return "POST", "/labels/" + fakeID + "/archive", "{}" },
		"unarchive_label":     func() (string, string, string) { return "POST", "/labels/" + fakeID + "/unarchive", "{}" },
		"delete_label":        func() (string, string, string) { return "DELETE", "/labels/" + fakeID, "" },
		"list_buckets":        func() (string, string, string) { return "GET", "/buckets", "" },
		"get_bucket":          func() (string, string, string) { return "GET", "/buckets/" + fakeID, "" },
		"create_bucket":       func() (string, string, string) { return "POST", "/buckets", `{"name":"Parity"}` },
		"update_bucket":       func() (string, string, string) { return "PATCH", "/buckets/" + fakeID, `{"name":"Parity 2"}` },
		"archive_bucket":      func() (string, string, string) { return "POST", "/buckets/" + fakeID + "/archive", "{}" },
		"unarchive_bucket":    func() (string, string, string) { return "POST", "/buckets/" + fakeID + "/unarchive", "{}" },
		"delete_bucket":       func() (string, string, string) { return "DELETE", "/buckets/" + fakeID, "" },
		"list_bucket_members": func() (string, string, string) { return "GET", "/buckets/" + fakeID + "/members", "" },
		"add_bucket_member": func() (string, string, string) {
			return "PUT", "/buckets/" + fakeID + "/members/commitment/" + fakeID, ""
		},
		"remove_bucket_member": func() (string, string, string) {
			return "DELETE", "/buckets/" + fakeID + "/members/commitment/" + fakeID, ""
		},
		"list_suggestions":  func() (string, string, string) { return "GET", "/suggestions", "" },
		"get_suggestion":    func() (string, string, string) { return "GET", "/suggestions/" + fakeID, "" },
		"accept_suggestion": func() (string, string, string) { return "POST", "/suggestions/" + fakeID + "/accept", "{}" },
		"correct_suggestion": func() (string, string, string) {
			return "POST", "/suggestions/" + fakeID + "/correct", `{"category_id":null}`
		},
		"reject_suggestion":       func() (string, string, string) { return "POST", "/suggestions/" + fakeID + "/reject", "{}" },
		"revert_suggestion":       func() (string, string, string) { return "POST", "/suggestions/" + fakeID + "/revert", "{}" },
		"list_alerts":             func() (string, string, string) { return "GET", "/alerts", "" },
		"get_alert":               func() (string, string, string) { return "GET", "/alerts/" + fakeID, "" },
		"dismiss_alert":           func() (string, string, string) { return "POST", "/alerts/" + fakeID + "/dismiss", "{}" },
		"get_object_organisation": func() (string, string, string) { return "GET", "/organisation/transaction/" + fakeID, "" },
		"set_object_category": func() (string, string, string) {
			return "PUT", "/organisation/transaction/" + fakeID + "/category", `{"category_id":null}`
		},
		"reset_object_category": func() (string, string, string) {
			return "DELETE", "/organisation/transaction/" + fakeID + "/category", ""
		},
		"add_object_label": func() (string, string, string) {
			return "PUT", "/organisation/transaction/" + fakeID + "/labels/" + fakeID, ""
		},
		"remove_object_label": func() (string, string, string) {
			return "DELETE", "/organisation/transaction/" + fakeID + "/labels/" + fakeID, ""
		},
	}
	// The same tools against a merchant object: the route refuses without the declared merchants
	// grant (403) and lets it through with it (the sample merchant simply does not exist).
	for _, tl := range mcpserver.Tools() {
		mg, ok := tl.ObjectGrants["merchant"]
		if !ok {
			continue
		}
		method, path, body := httpReq[tl.Name]()
		path = strings.Replace(path, "/organisation/transaction/", "/organisation/merchant/", 1)
		for _, withGrant := range []bool{false, true} {
			grants := []string{tl.Grant.String()}
			if withGrant {
				grants = append(grants, mg.String())
			}
			secret, _ := f.token(t, f.admin, grants...)
			req, _ := http.NewRequest(method, f.ts.URL+"/api/v1"+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+secret)
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if denied := resp.StatusCode == 403; denied == withGrant {
				t.Errorf("%s on a merchant with grants %v: HTTP status %d", tl.Name, grants, resp.StatusCode)
			}
		}
	}
	mcpArgs := map[string]map[string]any{
		"list_commitments":  nil,
		"get_commitment":    {"id": c.ID},
		"list_occurrences":  {"month": "2026-11"},
		"get_month_summary": {"month": "2026-11"},
		"get_outlook":       nil,
		"get_occurrence":    {"id": occ.Items[0].ID},
		"create_commitment": newCommitmentArgs("Parity 2"),
		"delete_commitment": {"id": "01JD3Y4C8V9M2Q7W5R6T0XZBNT", "version": 1},

		"list_payment_matches":            nil,
		"get_payment_match":               {"id": fakeID},
		"create_payment_match":            createMatchArgs(),
		"confirm_payment_match":           {"id": fakeID, "version": 1},
		"correct_payment_match":           {"id": fakeID, "version": 1, "amount": map[string]any{"amount": "1.00", "currency": "EUR"}},
		"reject_payment_match":            {"id": fakeID, "version": 1},
		"unmatch_payment_match":           {"id": fakeID, "version": 1},
		"reverse_payment_match":           {"id": fakeID, "version": 1},
		"unconfirm_payment_match":         {"id": fakeID, "version": 1},
		"confirm_payment_match_replacing": {"id": fakeID, "version": 1, "manual_payment_ids": []string{fakeID}},
		"run_payment_matching":            nil,
		"record_manual_payment":           {"id": fakeID, "amount": map[string]any{"amount": "1.00", "currency": "EUR"}, "paid_on": "2026-11-01"},
		"delete_manual_payment":           {"id": fakeID, "payment_id": fakeID, "version": 1},

		"list_deferral_plans":        nil,
		"get_deferral_plan":          {"id": fakeID},
		"list_deferral_plan_history": {"id": fakeID},
		"create_deferral_plan":       {"occurrence_id": fakeID, "plan_type": "personal", "planned_date": "2026-12-01"},
		"revise_deferral_plan":       {"id": fakeID, "version": 1, "planned_date": "2026-12-02"},
		"undo_deferral_plan":         {"id": fakeID, "version": 1},
		"add_deferral_adjustment":    {"id": fakeID, "version": 1, "kind": "fee", "amount": map[string]any{"amount": "1.00", "currency": "EUR"}},
		"remove_deferral_adjustment": {"id": fakeID, "adjustment_id": fakeID, "version": 1},

		"mark_deferral_adjustment_paid":   {"id": fakeID, "adjustment_id": fakeID, "version": 1},
		"unmark_deferral_adjustment_paid": {"id": fakeID, "adjustment_id": fakeID, "version": 1},

		"list_bank_applications":  nil,
		"list_bank_institutions":  {"application": "fake-app", "country": "FI"},
		"list_bank_connections":   nil,
		"get_bank_connection":     {"id": fakeID},
		"refresh_bank_connection": {"id": fakeID},

		"list_exchange_rates":      nil,
		"get_exchange_rate_status": nil,

		"list_categories":         nil,
		"get_category":            {"id": fakeID},
		"create_category":         {"name": "Parity"},
		"update_category":         {"id": fakeID, "version": 1, "name": "Parity 2"},
		"archive_category":        {"id": fakeID, "version": 1},
		"unarchive_category":      {"id": fakeID, "version": 1},
		"delete_category":         {"id": fakeID, "version": 1},
		"list_labels":             nil,
		"get_label":               {"id": fakeID},
		"create_label":            {"name": "Parity"},
		"update_label":            {"id": fakeID, "version": 1, "name": "Parity 2"},
		"archive_label":           {"id": fakeID, "version": 1},
		"unarchive_label":         {"id": fakeID, "version": 1},
		"delete_label":            {"id": fakeID, "version": 1},
		"list_buckets":            nil,
		"get_bucket":              {"id": fakeID},
		"create_bucket":           {"name": "Parity"},
		"update_bucket":           {"id": fakeID, "version": 1, "name": "Parity 2"},
		"archive_bucket":          {"id": fakeID, "version": 1},
		"unarchive_bucket":        {"id": fakeID, "version": 1},
		"delete_bucket":           {"id": fakeID, "version": 1},
		"list_bucket_members":     {"id": fakeID},
		"add_bucket_member":       {"id": fakeID, "object_type": "commitment", "object_id": fakeID},
		"remove_bucket_member":    {"id": fakeID, "object_type": "commitment", "object_id": fakeID},
		"list_suggestions":        nil,
		"get_suggestion":          {"id": fakeID},
		"accept_suggestion":       {"id": fakeID},
		"correct_suggestion":      {"id": fakeID, "category_id": nil},
		"reject_suggestion":       {"id": fakeID},
		"revert_suggestion":       {"id": fakeID},
		"list_alerts":             nil,
		"get_alert":               {"id": fakeID},
		"dismiss_alert":           {"id": fakeID},
		"get_object_organisation": {"object_type": "transaction", "object_id": fakeID},
		"set_object_category":     {"object_type": "transaction", "object_id": fakeID, "category_id": nil},
		"reset_object_category":   {"object_type": "transaction", "object_id": fakeID},
		"add_object_label":        {"object_type": "transaction", "object_id": fakeID, "label_id": fakeID},
		"remove_object_label":     {"object_type": "transaction", "object_id": fakeID, "label_id": fakeID},
	}
	subjects := []struct {
		name   string
		owner  users.User
		grants []string
	}{
		{"commitments:read", f.admin, []string{"commitments:read"}},
		{"commitments:write", f.admin, []string{"commitments:write"}},
		{"users:read only", f.admin, []string{"users:read"}},
		{"banking:read only", f.admin, []string{"banking:read"}},
		{"audit:read only", f.admin, []string{"audit:read"}},
		{"connections:read", f.admin, []string{"connections:read"}},
		{"connections:write", f.admin, []string{"connections:write"}},
		{"rates:read", f.admin, []string{"rates:read"}},
		{"organisation:read", f.admin, []string{"organisation:read"}},
		{"organisation:write", f.admin, []string{"organisation:write"}},
		{"read grant of a read-only owner", f.ro, []string{"commitments:read"}},
	}
	for _, sub := range subjects {
		secret, _ := f.token(t, sub.owner, sub.grants...)
		cs := f.connect(t, secret)
		for _, tl := range mcpserver.Tools() {
			method, path, body := httpReq[tl.Name]()
			req, _ := http.NewRequest(method, f.ts.URL+"/api/v1"+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+secret)
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if method == "DELETE" {
				req.Header.Set("If-Match", `"1"`)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			httpDenied := resp.StatusCode == 403

			res := call(t, cs, tl.Name, mcpArgs[tl.Name])
			mcpDenied := res.IsError && strings.Contains(resultText(res), "permission denied") && strings.Contains(resultText(res), tl.Grant.String())
			if httpDenied != mcpDenied {
				t.Errorf("%s / %s: HTTP status %d but MCP result isError=%v %q", sub.name, tl.Name, resp.StatusCode, res.IsError, resultText(res))
			}
			// A read of an object that does not exist (a sample id) is allowed: the grant passed, the object is missing.
			if !mcpDenied && res.IsError && tl.Kind == mcpserver.KindRead && !strings.Contains(resultText(res), "(HTTP 404, not_found)") {
				t.Errorf("%s / %s: allowed read failed: %s", sub.name, tl.Name, resultText(res))
			}
		}
	}
}

// TestMCPStdioServesTheSameToolsThroughTheAPI: ledger mcp stdio is a client of
// the server's API, so the server's grants and audit apply to it too.
func TestMCPStdioServesTheSameToolsThroughTheAPI(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	reader, _ := f.token(t, f.admin, "commitments:read")
	writer, writerID := f.token(t, f.admin, "commitments:write")

	run := func(secret string) *mcp.ClientSession {
		ct, st := mcp.NewInMemoryTransports()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan int, 1)
		var stderr bytes.Buffer
		env := mcpEnv{Transport: st, mgmtEnv: mgmtEnv{Getenv: func(k string) string {
			if k == "LEDGER_TOKEN" {
				return secret
			}
			if k == "LEDGER_URL" {
				return f.ts.URL
			}
			return ""
		}, ReadFile: func(string) ([]byte, error) { return nil, io.EOF }}}
		go func() { done <- runMCP(ctx, []string{"stdio", "--insecure"}, io.Discard, &stderr, env) }()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err, stderr.String())
		}
		t.Cleanup(func() {
			_ = cs.Close()
			cancel()
			<-done
			if strings.Contains(stderr.String(), secret) {
				t.Error("the token reached stderr")
			}
		})
		return cs
	}

	rs := run(reader)
	lt, err := rs.ListTools(context.Background(), nil)
	if err != nil || len(lt.Tools) != len(mcpserver.Tools()) {
		t.Fatalf("stdio tools: %v %v", lt, err)
	}
	if r := call(t, rs, "list_commitments", nil); r.IsError {
		t.Fatalf("stdio read: %s", resultText(r))
	}
	if r := call(t, rs, "create_commitment", newCommitmentArgs("Denied")); !r.IsError || !strings.Contains(resultText(r), "commitments:write") || !strings.Contains(resultText(r), "permission denied") {
		t.Fatalf("stdio missing grant: %q", resultText(r))
	}
	ws := run(writer)
	if r := call(t, ws, "create_commitment", newCommitmentArgs("Via stdio")); r.IsError {
		t.Fatalf("stdio write: %s", resultText(r))
	}
	w := f.auditEntries(t, audit.TokenWriteUsed)
	if len(w) != 1 || w[0].Actor.TokenID != writerID {
		t.Fatalf("a stdio write is audited by the server as a token write: %+v", w)
	}
	// The tool is also recorded, as asserted by the client: "requested".
	if m := f.auditEntries(t, audit.MCPToolCalled); len(m) != 1 || m[0].Actor.TokenID != writerID ||
		m[0].Details["tool"] != "create_commitment" || m[0].Details["outcome"] != "requested" {
		t.Fatalf("a stdio write records mcp.tool_called (requested): %+v", m)
	}
	if me, err := ws.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "ledger://me"}); err != nil || strings.Contains(me.Contents[0].Text, writer) {
		t.Fatalf("stdio resource: %v %v", me, err)
	}
	if got := runMCP(context.Background(), nil, io.Discard, io.Discard, mcpEnv{}); got != 2 {
		t.Errorf("no subcommand: exit %d, want 2", got)
	}
	if got := runMCP(context.Background(), []string{"stdio"}, io.Discard, io.Discard, mcpEnv{mgmtEnv: mgmtEnv{Getenv: func(string) string { return "" }, ReadFile: func(string) ([]byte, error) { return nil, io.EOF }}}); got != 2 {
		t.Errorf("no token: exit %d, want 2", got)
	}
}
