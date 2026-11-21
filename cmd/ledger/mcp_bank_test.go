package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/mcpserver"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// TestMCPBankConnectionTools walks the bank connection tools over real MCP and
// the real API with the fake provider: the reads, a refresh, a refresh that
// finds the consent withdrawn (reported as data, with the stored balances kept),
// the grants (administrator-only, read versus write) and the audit of the write.
// Starting, renewing and ending a connection are deliberately not tools. No live
// bank is involved and no consent code may reach a result, the audit log or
// the log.
func TestMCPBankConnectionTools(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	ctx := context.Background()

	// Only the reads and the refresh exist, and they are annotated honestly.
	tools := map[string]mcpserver.Tool{}
	for _, tl := range mcpserver.Tools() {
		if strings.Contains(tl.Name, "bank") {
			tools[tl.Name] = tl
		}
	}
	want := map[string]mcpserver.Kind{"list_bank_applications": mcpserver.KindRead, "list_bank_institutions": mcpserver.KindRead, "list_bank_connections": mcpserver.KindRead,
		"get_bank_connection": mcpserver.KindRead, "refresh_bank_connection": mcpserver.KindWrite}
	if len(tools) != len(want) {
		t.Errorf("bank tools: %v", tools)
	}
	for name, kind := range want {
		tl, ok := tools[name]
		if !ok || tl.Kind != kind || tl.Grant.Resource != authz.ResourceConnections || (kind == mcpserver.KindWrite) != (tl.Grant.Action == authz.ActionWrite) {
			t.Errorf("%s: %+v", name, tl)
		}
	}

	// A connection made the way the browser does it: start, then the redirect.
	actor := audit.ActorOf(authz.UserPrincipal(f.admin.ID, f.admin.Role))
	inst := bankingtest.Standard().Institutions[0]
	st, err := f.parts.banking.Start(ctx, actor, bankconnect.Start{Application: "fake-app", InstitutionID: inst.ID, Country: inst.Country})
	if err != nil {
		t.Fatal(err)
	}
	state := stateOf(t, st.AuthorizationURL)
	code := bankingtest.AuthorizationCode(state).Reveal()
	if _, err := f.parts.banking.Complete(ctx, actor, bankconnect.Redirect{State: state, Code: bankingtest.AuthorizationCode(state)}); err != nil {
		t.Fatal(err)
	}
	id := string(st.Connection.ID)

	readSecret, _ := f.token(t, f.admin, "connections:read")
	writeSecret, _ := f.token(t, f.admin, "connections:write")
	other, _ := f.token(t, f.admin, "commitments:write")
	reader, writer := f.connect(t, readSecret), f.connect(t, writeSecret)
	var results []string

	apps := decodeJSONText[struct {
		Items []ledgerclient.BankApplication
	}](t, call(t, reader, "list_bank_applications", nil))
	if len(apps.Items) != 2 || apps.Items[0].Name != "fake-app" || apps.Items[1].Mode != "developer" {
		t.Errorf("applications: %+v", apps)
	}
	insts := decodeJSONText[struct {
		Items []ledgerclient.BankInstitution
	}](t, call(t, reader, "list_bank_institutions", map[string]any{"application": "fake-app", "country": "FI"}))
	if len(insts.Items) != 1 {
		t.Errorf("institutions: %+v", insts)
	}
	if r := call(t, reader, "list_bank_institutions", map[string]any{"application": "fake-app", "country": "fi"}); !r.IsError || !strings.Contains(resultText(r), "invalid arguments") {
		t.Errorf("a lower-case country must be refused by the schema: %s", resultText(r))
	}

	// Before any refresh: connected, never fetched. A read cannot refresh.
	list := decodeJSONText[ledgerclient.Page[ledgerclient.BankConnection]](t, call(t, reader, "list_bank_connections", nil))
	if len(list.Items) != 1 || list.Items[0].ID != id || list.Items[0].Status != "active" || list.Items[0].Freshness != "never_refreshed" {
		t.Fatalf("list: %+v", list)
	}
	denied := call(t, reader, "refresh_bank_connection", map[string]any{"id": id})
	if !denied.IsError || !strings.Contains(resultText(denied), "connections:write") || !strings.Contains(resultText(denied), "permission denied") {
		t.Fatalf("a read grant must not refresh: %s", resultText(denied))
	}
	if r := call(t, f.connect(t, other), "list_bank_connections", nil); !r.IsError || !strings.Contains(resultText(r), "connections:read") {
		t.Errorf("a token without connections grants: %s", resultText(r))
	}
	calls := len(f.bank.Calls())

	// Refresh: data and freshness appear, with the answer of the run.
	res := call(t, writer, "refresh_bank_connection", map[string]any{"id": id})
	results = append(results, resultText(res))
	got := decodeJSONText[ledgerclient.BankConnection](t, res)
	if got.Freshness != "fresh" || len(got.Accounts) != 3 || got.LastRefreshRun == nil || got.LastRefreshRun.Outcome != "succeeded" || got.RefreshRunning {
		t.Fatalf("refresh: %+v", got)
	}
	if len(f.bank.Calls()) == calls {
		t.Error("a refresh must call the provider")
	}
	for _, a := range got.Accounts {
		if a.Freshness.State != "fresh" || a.Freshness.LastSuccessAt == nil {
			t.Errorf("account freshness: %+v", a)
		}
		if a.Currency == "XXX" && len(a.Balances) != 0 {
			t.Errorf("an account with no reported balance must show none, not zero: %+v", a)
		}
	}
	if strings.Contains(resultText(res), bankingtest.SyntheticIBANEUR) {
		t.Error("the full account number reached the result")
	}

	// A withdrawn consent is data, not a tool failure; the stored balances stay.
	f.bank.Revoke()
	res = call(t, writer, "refresh_bank_connection", map[string]any{"id": id})
	results = append(results, resultText(res))
	got = decodeJSONText[ledgerclient.BankConnection](t, res)
	if got.Status != "reconnect_required" || !got.NeedsReconnect || got.LastRefreshRun == nil || got.LastRefreshRun.Error == nil || got.LastRefreshRun.Error.Kind != "reconnect_required" {
		t.Fatalf("refresh after revoke: %+v", got)
	}
	kept := false
	for _, a := range got.Accounts {
		kept = kept || (a.Currency == "EUR" && len(a.Balances) > 0 && a.Freshness.Stale)
	}
	if !kept {
		t.Errorf("the last balances must stay visible as stale: %+v", got.Accounts)
	}
	if r := call(t, writer, "refresh_bank_connection", map[string]any{"id": id}); !r.IsError || !strings.Contains(resultText(r), "connection_not_active") {
		t.Errorf("refresh while a reconnect is needed: %s", resultText(r))
	}
	one := decodeJSONText[ledgerclient.BankConnection](t, call(t, reader, "get_bank_connection", map[string]any{"id": id}))
	if one.ID != id || one.Status != "reconnect_required" {
		t.Errorf("get: %+v", one)
	}
	if r := call(t, reader, "get_bank_connection", map[string]any{"id": "01JD3Y4C8V9M2Q7W5R6T0XZBNT"}); !r.IsError || !strings.Contains(resultText(r), "not_found") {
		t.Errorf("an unknown connection: %s", resultText(r))
	}

	// The write is audited with the token; nothing secret is in any trace.
	if n := len(f.auditEntries(t, "mcp.tool_called")); n < 3 {
		t.Errorf("every refresh call (and the denied one) is audited as mcp.tool_called, got %d entries", n)
	}
	if n := len(f.auditEntries(t, "token.write_used")); n < 2 {
		t.Errorf("token.write_used entries: %d", n)
	}
	var all strings.Builder
	entries, err := f.al.List(ctx, audit.Filter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := json.Marshal(e)
		all.Write(b)
	}
	if !strings.Contains(all.String(), "bank_connection.refresh_requested") {
		t.Error("the refresh itself is audited")
	}
	all.WriteString(strings.Join(results, "\n"))
	for _, secret := range []string{code, "fake-session", state} {
		if strings.Contains(all.String(), secret) {
			t.Errorf("%q reached a result or the audit log", secret[:min(len(secret), 4)]+"…")
		}
	}
}
