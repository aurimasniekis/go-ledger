package main

import (
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// TestCLIBankConnectionJourney drives the bank commands through the real
// handlers with the fake provider: applications, institutions, connect (the
// consent page, never credentials), the redirect completed the way the browser
// page does, refresh, a revoked consent, reconnect and disconnect. It checks
// the grants and that no consent code reaches output or logs. No live bank is
// involved.
func TestCLIBankConnectionJourney(t *testing.T) {
	c := newCLIFixture(t)
	const app = "fake-app"
	var codes []string
	// run is c.run that also fails if a consent code was printed.
	run := func(tok string, args ...string) (int, string, string) {
		t.Helper()
		rc, out, errb := c.run(tok, args...)
		for _, code := range codes {
			if strings.Contains(out, code) || strings.Contains(errb, code) {
				t.Fatalf("%v printed a consent code", args)
			}
		}
		return rc, out, errb
	}

	rc, out, errb := run(adminToken, "bank", "applications")
	if rc != 0 || !strings.Contains(out, app) || !strings.Contains(out, "fake-developer") || !strings.Contains(out, "linked_only") {
		t.Fatalf("applications: %d %s %s", rc, out, errb)
	}
	var apps struct {
		Items []ledgerclient.BankApplication `json:"items"`
	}
	c.json(adminToken, &apps, "bank", "applications")
	if len(apps.Items) != 2 || apps.Items[0].Mode != "app" || apps.Items[1].Mode != "developer" {
		t.Fatalf("applications json: %+v", apps)
	}
	var insts struct {
		Items []ledgerclient.BankInstitution `json:"items"`
	}
	c.json(adminToken, &insts, "bank", "institutions", app, "--country", "FI")
	if len(insts.Items) != 1 || insts.Items[0].Name == "" {
		t.Fatalf("institutions: %+v", insts)
	}
	c.fail(adminToken, 2, "--country is required", "bank", "institutions", app)
	c.fail(adminToken, 1, "country", "bank", "institutions", app, "--country", "fi")
	c.fail(adminToken, 1, "no such provider application", "bank", "institutions", "nope", "--country", "FI")

	// Connect: pending, with the consent page; nothing is fetched yet.
	c.fail(adminToken, 2, "--application, --institution-id and --country are required", "bank", "connect", "--application", app)
	var start ledgerclient.BankConsentStart
	c.json(adminToken, &start, "bank", "connect", "--application", app, "--institution-id", insts.Items[0].ID, "--country", "FI")
	id := start.Connection.ID
	if start.Connection.Status != "pending" || !strings.HasPrefix(start.AuthorizationURL, "https://bank.example/") {
		t.Fatalf("connect: %+v", start)
	}
	rc, out, errb = run(adminToken, "bank", "connect", "--application", app, "--institution-id", insts.Items[0].ID, "--country", "FI")
	if rc != 0 || !strings.Contains(out, "https://bank.example/") || !strings.Contains(out, "Open this page in a browser") {
		t.Fatalf("connect text: %d %s %s", rc, out, errb)
	}
	c.fail(adminToken, 1, "connection_not_active", "bank", "refresh", id)

	// The browser callback page completes it (not the CLI).
	complete := func(authURL string) {
		t.Helper()
		state := stateOf(t, authURL)
		code := bankingtest.AuthorizationCode(state).Reveal()
		codes = append(codes, code)
		bc := bankCall{t: t, f: c.api, hdr: c.api.signIn(t, "alice")}
		if status, body, _ := bc.do("POST", "/api/v1/bank-connections/complete", `{"state":"`+state+`","code":"`+code+`"}`); status != 200 || body["status"] != "active" {
			t.Fatalf("complete: %d %v", status, body)
		}
	}
	// A second attempt (the text-mode connect above) stays pending.
	complete(start.AuthorizationURL)

	// Refresh: accounts, balances and freshness are shown.
	rc, out, errb = run(adminToken, "bank", "refresh", id)
	for _, want := range []string{"status:", "active", "freshness:   fresh", "EUR", "none reported", "booked"} {
		if rc != 0 || !strings.Contains(out, want) {
			t.Fatalf("refresh text lacks %q: %d %s %s", want, rc, out, errb)
		}
	}
	if strings.Contains(out, bankingtest.SyntheticIBANEUR) {
		t.Errorf("the full account number was printed: %s", out)
	}
	var conn ledgerclient.BankConnection
	c.json(adminToken, &conn, "bank", "get", id)
	if conn.Status != "active" || len(conn.Accounts) != 3 || conn.LastRefreshRun == nil || conn.LastRefreshRun.Outcome != "succeeded" {
		t.Fatalf("get: %+v", conn)
	}
	var list ledgerclient.Page[ledgerclient.BankConnection]
	c.json(adminToken, &list, "bank", "list", "--all")
	if len(list.Items) != 2 {
		t.Fatalf("list (the active one and the pending second attempt): %+v", list)
	}
	if rc, out, _ = run(adminToken, "bank", "list"); rc != 0 || !strings.Contains(out, id) || !strings.Contains(out, "fresh") {
		t.Fatalf("list text: %d %s", rc, out)
	}

	// A revoked consent: the answer says reconnect, not retry, and keeps the data.
	c.api.bank.Revoke()
	rc, out, errb = run(adminToken, "bank", "refresh", id)
	if rc != 0 || !strings.Contains(out, "reconnect_required") || !strings.Contains(out, "reconnect needed: ledger bank reconnect "+id) {
		t.Fatalf("refresh after revoke: %d %s %s", rc, out, errb)
	}
	c.fail(adminToken, 1, "connection_not_active", "bank", "refresh", id)

	// Reconnect: a new consent page; the connection stays as it was.
	var re ledgerclient.BankConsentStart
	c.json(adminToken, &re, "bank", "reconnect", id, "--valid-for-days", "30")
	if re.Connection.Status != "reconnect_required" || re.AuthorizationURL == "" {
		t.Fatalf("reconnect: %+v", re)
	}
	complete(re.AuthorizationURL)
	c.json(adminToken, &conn, "bank", "get", id)
	if conn.Status != "active" || conn.NeedsReconnect {
		t.Fatalf("after reconnect: %+v", conn)
	}

	// Disconnect: a stale version is a conflict, an omitted one uses the current.
	c.fail(adminToken, 1, "version is now", "bank", "disconnect", id, "--version", "1")
	rc, out, errb = run(adminToken, "bank", "disconnect", id)
	if rc != 0 || !strings.Contains(out, "disconnected") || !strings.Contains(errb, "using version") {
		t.Fatalf("disconnect: %d %s %s", rc, out, errb)
	}
	c.json(adminToken, &conn, "bank", "get", id)
	if conn.Status != "disconnected" || len(conn.Accounts) != 3 {
		t.Fatalf("a disconnected connection keeps its history: %+v", conn)
	}

	// Grants: connections are administrator-only, whatever the token holds.
	for _, args := range [][]string{{"bank", "applications"}, {"bank", "list"}, {"bank", "get", id}, {"bank", "refresh", id}, {"bank", "disconnect", id, "--version", "1"},
		{"bank", "connect", "--application", app, "--institution-id", "x", "--country", "FI"}, {"bank", "reconnect", id}} {
		c.fail(roToken, 1, "lacks the grant", args...)
		c.fail(scopedTok, 1, "lacks the grant", args...)
	}
	if strings.Contains(c.api.log.String(), "fake-code") {
		t.Error("a consent code reached the logs")
	}
	for _, code := range codes {
		if strings.Contains(c.api.log.String(), code) {
			t.Error("a consent code reached the logs")
		}
	}
	// Usage errors.
	c.fail(adminToken, 2, "an object ID is required", "bank", "get")
	c.fail(adminToken, 2, "unknown subcommand", "bank", "nope")
}
