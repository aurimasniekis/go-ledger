package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
)

// bankCall is a JSON call as the administrator over the fixture's API.
type bankCall struct {
	t   *testing.T
	f   *apiFixture
	hdr map[string]string
}

func (c bankCall) do(method, path, body string, extra ...string) (int, map[string]any, http.Header) {
	c.t.Helper()
	h := map[string]string{}
	for k, v := range c.hdr {
		h[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	rec := c.f.do(method, path, body, h)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			c.t.Fatalf("%s %s: not JSON: %v: %s", method, path, err, rec.Body.String())
		}
	}
	return rec.Code, out, rec.Header()
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", v)
	}
	return m
}

func asList(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("not a list: %v", v)
	}
	return l
}

func stateOf(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil || u.Query().Get("state") == "" {
		t.Fatalf("authorization URL has no state: %q", authURL)
	}
	return u.Query().Get("state")
}

// TestBankConnectionJourney drives connect, consent redirect, refresh, a
// revoked consent (reconnect required, which retrying cannot fix), a reconnect,
// a transient failure (retryable, which a retry can fix) and disconnect through
// the HTTP API with the fake provider (B4). No live bank is involved.
func TestBankConnectionJourney(t *testing.T) {
	f := newAPIFixture(t)
	c := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	const app = "fake-app"

	code, body, _ := c.do("GET", "/api/v1/bank-applications", "")
	apps := asList(t, body["items"])
	if code != 200 || len(apps) != 2 || asMap(t, apps[0])["name"] != app || asMap(t, apps[0])["account_access"] != "all" ||
		asMap(t, apps[1])["name"] != "fake-developer" || asMap(t, apps[1])["account_access"] != "linked_only" || asMap(t, apps[1])["mode"] != "developer" {
		t.Fatalf("applications (app mode, then developer mode): %d %v", code, body)
	}
	if code, _, _ := c.do("GET", "/api/v1/bank-applications/"+app+"/institutions?country=fi", ""); code != 400 {
		t.Errorf("a lower-case country: %d, want 400", code)
	}
	code, body, _ = c.do("GET", "/api/v1/bank-applications/"+app+"/institutions?country=FI", "")
	if insts := asList(t, body["items"]); code != 200 || len(insts) != 1 {
		t.Fatalf("institutions: %d %v", code, body)
	}

	// Connect: pending until the redirect comes back.
	code, body, _ = c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
	if code != 201 {
		t.Fatalf("start: %d %v", code, body)
	}
	conn := asMap(t, body["connection"])
	id := conn["id"].(string)
	if conn["status"] != "pending" || conn["needs_reconnect"] != false {
		t.Errorf("a new connection is pending: %v", conn)
	}
	state := stateOf(t, body["authorization_url"].(string))
	if !strings.HasPrefix(body["authorization_url"].(string), "https://bank.example/") {
		t.Errorf("authorization URL: %v", body["authorization_url"])
	}
	if code, body, _ := c.do("POST", "/api/v1/bank-connections/"+id+"/refresh", ""); code != 409 || body["code"] != "connection_not_active" {
		t.Errorf("refreshing a pending connection: %d %v", code, body)
	}

	// Redirect: the state works once.
	complete := func(state string) (int, map[string]any) {
		code, body, _ := c.do("POST", "/api/v1/bank-connections/complete", `{"state":"`+state+`","code":"`+bankingtest.AuthorizationCode(state).Reveal()+`"}`)
		return code, body
	}
	code, body = complete(state)
	if code != 200 || body["status"] != "active" || body["freshness"] != "never_refreshed" {
		t.Fatalf("complete: %d %v", code, body)
	}
	if code, body := complete(state); code != 409 || body["code"] != "consent_not_found" {
		t.Errorf("a replayed redirect: %d %v", code, body)
	}
	if _, body, _ := c.do("GET", "/api/v1/bank-connections/"+id, ""); body["status"] != "active" {
		t.Errorf("a replayed redirect must not change an active connection: %v", body)
	}

	// Refresh: accounts, balances and freshness appear; unknown stays unknown.
	code, body, _ = c.do("POST", "/api/v1/bank-connections/"+id+"/refresh", "")
	if code != 200 || body["status"] != "active" || body["freshness"] != "fresh" {
		t.Fatalf("refresh: %d %v", code, body)
	}
	accounts := asList(t, body["accounts"])
	if len(accounts) != 3 {
		t.Fatalf("accounts: %v", accounts)
	}
	var multi map[string]any
	for _, a := range accounts {
		am := asMap(t, a)
		fr := asMap(t, am["freshness"])
		if fr["state"] != "fresh" || fr["last_success_at"] == nil {
			t.Errorf("account freshness: %v", am)
		}
		if am["currency"] == "XXX" {
			multi = am
		}
	}
	if multi == nil || len(asList(t, multi["balances"])) != 0 {
		t.Errorf("an account with no reported balance must have none, not zero: %v", multi)
	}
	run := asMap(t, body["last_refresh_run"])
	if run["outcome"] != "succeeded" {
		t.Errorf("run: %v", run)
	}
	// No bank identifiers leak: only a four-character hint.
	raw, _ := json.Marshal(body)
	for _, secret := range []string{bankingtest.SyntheticIBANEUR, "fake-session", "fake-hash", "fake-code"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the response contains %q", secret)
		}
	}

	// The bank withdraws consent: reconnect required, data stays as stale.
	f.bank.Revoke()
	code, body, _ = c.do("POST", "/api/v1/bank-connections/"+id+"/refresh", "")
	if code != 200 || body["status"] != "reconnect_required" || body["needs_reconnect"] != true || body["freshness"] != "reconnect_required" {
		t.Fatalf("refresh after revoke: %d %v", code, body)
	}
	if e := asMap(t, body["last_refresh_run"])["error"]; e == nil || asMap(t, e)["kind"] != "reconnect_required" {
		t.Errorf("the run must carry the reconnect kind: %v", body["last_refresh_run"])
	}
	for _, a := range asList(t, body["accounts"]) {
		am := asMap(t, a)
		if asMap(t, am["freshness"])["state"] != "reconnect_required" || asMap(t, am["freshness"])["stale"] != true {
			t.Errorf("account after revoke: %v", am)
		}
		if am["currency"] == "EUR" && len(asList(t, am["balances"])) == 0 {
			t.Errorf("the last good balance must stay visible: %v", am)
		}
	}
	if code, body, _ := c.do("POST", "/api/v1/bank-connections/"+id+"/refresh", ""); code != 409 || body["code"] != "connection_not_active" {
		t.Errorf("refresh while reconnect is required: %d %v", code, body)
	}

	// Reconnect: the connection stays as it was until consent completes.
	code, body, _ = c.do("POST", "/api/v1/bank-connections/"+id+"/reconnect", "")
	if code != 200 || asMap(t, body["connection"])["status"] != "reconnect_required" {
		t.Fatalf("reconnect: %d %v", code, body)
	}
	state = stateOf(t, body["authorization_url"].(string))
	if code, body = complete(state); code != 200 || body["status"] != "active" || body["needs_reconnect"] != false {
		t.Fatalf("complete reconnect: %d %v", code, body)
	}
	if _, has := body["previous_session_end_failed"]; has {
		t.Errorf("the replaced session was ended, nothing to flag: %v", body)
	}
	if calls := f.bank.Calls(); !slices.Contains(calls, bankingtest.OpEndSession) {
		t.Errorf("a successful reconnect must end the replaced provider session: %v", calls)
	}

	// A transient failure is retryable and does not ask for a reconnect.
	f.bank.FailNext(bankingtest.OpAccounts, bankingtest.Failure(bankingtest.OpAccounts, banking.KindRetryable, "", "TIMEOUT", 0),
		bankingtest.Failure(bankingtest.OpAccounts, banking.KindRetryable, "", "TIMEOUT", 0), bankingtest.Failure(bankingtest.OpAccounts, banking.KindRetryable, "", "TIMEOUT", 0),
		bankingtest.Failure(bankingtest.OpAccounts, banking.KindRetryable, "", "TIMEOUT", 0))
	code, body, _ = c.do("POST", "/api/v1/bank-connections/"+id+"/refresh", "")
	if code != 200 || body["status"] != "active" || body["needs_reconnect"] != false {
		t.Fatalf("refresh with an outage: %d %v", code, body)
	}
	if e := asMap(t, asMap(t, body["last_refresh_run"])["error"]); e["kind"] != "retryable" {
		t.Errorf("the run must carry the retryable kind: %v", e)
	}

	// Disconnect needs the current version.
	version := body["version"]
	if code, body, _ := c.do("POST", "/api/v1/bank-connections/"+id+"/disconnect", "", "If-Match", `"1"`); code != 409 || body["code"] != "version_conflict" {
		t.Errorf("stale disconnect: %d %v", code, body)
	}
	if code, body, _ := c.do("POST", "/api/v1/bank-connections/"+id+"/disconnect", ""); code != 428 {
		t.Errorf("disconnect without a version: %d %v", code, body)
	}
	code, body, _ = c.do("POST", "/api/v1/bank-connections/"+id+"/disconnect", "", "If-Match", `"`+strconv.FormatInt(int64(version.(float64)), 10)+`"`)
	if code != 200 || body["status"] != "disconnected" || len(asList(t, body["accounts"])) != 3 {
		t.Fatalf("disconnect: %d %v", code, body)
	}
	if calls := f.bank.Calls(); calls[len(calls)-1] != bankingtest.OpEndSession {
		t.Errorf("the provider session must be ended: %v", calls)
	}

	// A refused consent leaves a visible failed connection.
	_, body, _ = c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
	failedID := asMap(t, body["connection"])["id"].(string)
	code, body, _ = c.do("POST", "/api/v1/bank-connections/complete", `{"state":"`+stateOf(t, body["authorization_url"].(string))+`","error":"access_denied"}`)
	if code != 422 || body["code"] != "bank_request_rejected" {
		t.Errorf("refused consent: %d %v", code, body)
	}
	if _, body, _ = c.do("GET", "/api/v1/bank-connections/"+failedID, ""); body["status"] != "failed" || asMap(t, body["last_error"])["kind"] != "permanent" {
		t.Errorf("the refused consent must stay visible as failed: %v", body)
	}

	// The list holds both, oldest first.
	code, body, _ = c.do("GET", "/api/v1/bank-connections", "")
	if items := asList(t, body["items"]); code != 200 || len(items) != 2 || asMap(t, items[0])["id"] != id {
		t.Errorf("list: %d %v", code, body)
	}

	// Audit: the changes are recorded without any secret.
	_, body, _ = c.do("GET", "/api/v1/audit?limit=200", "")
	got := map[string]bool{}
	for _, it := range asList(t, body["items"]) {
		got[asMap(t, it)["action"].(string)] = true
	}
	for _, want := range []string{"bank_connection.started", "bank_connection.connected", "bank_connection.reconnect_started", "bank_connection.refresh_requested", "bank_connection.disconnected"} {
		if !got[want] {
			t.Errorf("no audit entry %s", want)
		}
	}
	auditRaw, _ := json.Marshal(body)
	logs := f.log.String() + string(auditRaw)
	for _, secret := range []string{"fake-code", "fake-session", state} {
		if strings.Contains(logs, secret) {
			t.Errorf("the audit log or server log contains %q", secret)
		}
	}
}

// TestBankConnectionErrorsKeepTheirKind checks that provider failures reach the
// client with distinct codes and statuses: reconnect, retry later, wait, reject.
func TestBankConnectionErrorsKeepTheirKind(t *testing.T) {
	f := newAPIFixture(t)
	c := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	cases := []struct {
		name       string
		err        error
		status     int
		code       string
		retryAfter string
	}{
		{"reconnect", bankingtest.Failure(bankingtest.OpInstitutions, banking.KindReconnectRequired, banking.ReasonConsentExpired, "EXPIRED", 0), 409, "bank_reconnect_required", ""},
		{"retryable", bankingtest.Failure(bankingtest.OpInstitutions, banking.KindRetryable, "", "TIMEOUT", 0), 503, "bank_unavailable", ""},
		{"rate limited", bankingtest.Failure(bankingtest.OpInstitutions, banking.KindRateLimited, "", "RATE", 90*time.Second), 429, "bank_rate_limited", "90"},
		{"permanent", bankingtest.Failure(bankingtest.OpInstitutions, banking.KindPermanent, "", "BAD", 0), 422, "bank_request_rejected", ""},
	}
	for _, tc := range cases {
		f.bank.FailNext(bankingtest.OpInstitutions, tc.err)
		code, body, h := c.do("GET", "/api/v1/bank-applications/fake-app/institutions?country=FI", "")
		if code != tc.status || body["code"] != tc.code || h.Get("Retry-After") != tc.retryAfter {
			t.Errorf("%s: %d %v Retry-After=%q", tc.name, code, body, h.Get("Retry-After"))
		}
	}
	if code, body, _ := c.do("GET", "/api/v1/bank-applications/nope/institutions?country=FI", ""); code != 404 {
		t.Errorf("unknown application: %d %v", code, body)
	}
	if code, body, _ := c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Nope","country":"FI"}`); code != 400 || body["field"] != "institution_id" {
		t.Errorf("unknown institution: %d %v", code, body)
	}
	if code, body, _ := c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app"}`); code != 400 {
		t.Errorf("missing fields: %d %v", code, body)
	}
}

// TestRefreshAfterShutdownIsUnavailable: once shutdown has begun (the service
// is waited on), a refresh starts nothing and answers 503 dependency_unavailable,
// while the same refresh worked before (B4g F2).
func TestRefreshAfterShutdownIsUnavailable(t *testing.T) {
	f := newAPIFixture(t)
	c := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	_, body, _ := c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
	id := asMap(t, body["connection"])["id"].(string)
	state := stateOf(t, body["authorization_url"].(string))
	complete := `{"state":"` + state + `","code":"` + bankingtest.AuthorizationCode(state).Reveal() + `"}`
	if code, body, _ := c.do("POST", "/api/v1/bank-connections/complete", complete); code != 200 {
		t.Fatalf("complete: %d %v", code, body)
	}
	refresh := "/api/v1/bank-connections/" + id + "/refresh"
	if code, body, _ := c.do("POST", refresh, ""); code != 200 {
		t.Fatalf("refresh before shutdown: %d %v", code, body)
	}
	if err := f.connections.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := c.do("POST", refresh, ""); code != 503 || body["code"] != "dependency_unavailable" {
		t.Errorf("refresh after shutdown began: %d %v, want 503 dependency_unavailable", code, body)
	}
}

// TestRetryableConsentCompletionKeepsTheRedirectUsable: a bank that cannot be
// reached while a consent is completed changes nothing, so the callback page's
// retry with the same state and code works (B4, ADR-0040 point 6).
func TestRetryableConsentCompletionKeepsTheRedirectUsable(t *testing.T) {
	f := newAPIFixture(t)
	c := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	_, body, _ := c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
	id := asMap(t, body["connection"])["id"].(string)
	state := stateOf(t, body["authorization_url"].(string))
	complete := `{"state":"` + state + `","code":"` + bankingtest.AuthorizationCode(state).Reveal() + `"}`

	f.bank.FailNext(bankingtest.OpCompleteConsent, bankingtest.Failure(bankingtest.OpCompleteConsent, banking.KindRetryable, "", "TIMEOUT", 0))
	code, body, _ := c.do("POST", "/api/v1/bank-connections/complete", complete)
	if code != 503 || body["code"] != "bank_unavailable" {
		t.Fatalf("complete during an outage: %d %v", code, body)
	}
	if _, body, _ = c.do("GET", "/api/v1/bank-connections/"+id, ""); body["status"] != "pending" {
		t.Errorf("an outage must not fail the connection: %v", body["status"])
	}
	if code, body, _ = c.do("POST", "/api/v1/bank-connections/complete", complete); code != 200 || body["status"] != "active" {
		t.Errorf("the same redirect completed again: %d %v", code, body)
	}
}

// R1: disconnect, reconnect, complete leaves the connection active again.
func TestDisconnectedConnectionCanBeReconnectedThroughTheAPI(t *testing.T) {
	f := newAPIFixture(t)
	c := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	_, body, _ := c.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
	id := asMap(t, body["connection"])["id"].(string)
	state := stateOf(t, body["authorization_url"].(string))
	complete := func(state string) (int, map[string]any) {
		code, body, _ := c.do("POST", "/api/v1/bank-connections/complete", `{"state":"`+state+`","code":"`+bankingtest.AuthorizationCode(state).Reveal()+`"}`)
		return code, body
	}
	if code, body := complete(state); code != 200 {
		t.Fatalf("complete: %d %v", code, body)
	}
	_, body, hdr := c.do("GET", "/api/v1/bank-connections/"+id, "")
	_ = hdr
	version := strconv.FormatInt(int64(body["version"].(float64)), 10)
	code, body, _ := c.do("POST", "/api/v1/bank-connections/"+id+"/disconnect", `{"version":`+version+`}`)
	if code != 200 || body["status"] != "disconnected" {
		t.Fatalf("disconnect: %d %v", code, body)
	}
	code, body, _ = c.do("POST", "/api/v1/bank-connections/"+id+"/reconnect", "")
	if code != 200 {
		t.Fatalf("reconnect: %d %v", code, body)
	}
	if code, body := complete(stateOf(t, body["authorization_url"].(string))); code != 200 || body["status"] != "active" {
		t.Fatalf("complete after reconnect: %d %v", code, body)
	}
	if _, body, _ := c.do("GET", "/api/v1/bank-connections/"+id, ""); body["status"] != "active" || body["refresh_running"] != false {
		t.Errorf("after the journey: %v", body)
	}
}

// TestDeveloperModeApplicationThroughTheAPI: a developer-mode application
// coexists with an app-mode one (D10). It lists only the accounts linked to it,
// and an application with nothing linked refreshes to an empty list, not an
// error.
func TestDeveloperModeApplicationThroughTheAPI(t *testing.T) {
	f := newAPIFixture(t)
	c := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	connect := func() (string, map[string]any) {
		_, body, _ := c.do("POST", "/api/v1/bank-connections", `{"application":"fake-developer","institution_id":"Fake Bank","country":"FI"}`)
		conn := asMap(t, body["connection"])
		if conn["mode"] != "developer" || conn["account_access"] != "linked_only" || conn["application"] != "fake-developer" {
			t.Fatalf("connection mode: %v", conn)
		}
		state := stateOf(t, body["authorization_url"].(string))
		code, done, _ := c.do("POST", "/api/v1/bank-connections/complete", `{"state":"`+state+`","code":"`+bankingtest.AuthorizationCode(state).Reveal()+`"}`)
		if code != 200 || done["status"] != "active" {
			t.Fatalf("complete: %d %v", code, done)
		}
		return conn["id"].(string), done
	}

	id, _ := connect()
	code, body, _ := c.do("POST", "/api/v1/bank-connections/"+id+"/refresh", "")
	if code != 200 || body["status"] != "active" {
		t.Fatalf("refresh: %d %v", code, body)
	}
	var currencies []string
	for _, a := range asList(t, body["accounts"]) {
		currencies = append(currencies, asMap(t, a)["currency"].(string))
	}
	slices.Sort(currencies)
	if !slices.Equal(currencies, []string{"EUR", "XXX"}) {
		t.Errorf("a developer-mode connection must show only linked accounts (EUR, XXX), got %v", currencies)
	}

	// Nothing linked: an empty list, a succeeded run, no error.
	f.devBank.Mutate(func(d *bankingtest.Dataset) {
		for i := range d.Accounts {
			d.Accounts[i].Linked = false
		}
	})
	id2, _ := connect()
	code, body, _ = c.do("POST", "/api/v1/bank-connections/"+id2+"/refresh", "")
	if code != 200 || body["status"] != "active" || len(asList(t, body["accounts"])) != 0 {
		t.Fatalf("refresh with nothing linked: %d %v", code, body)
	}
	if run := asMap(t, body["last_refresh_run"]); run["outcome"] != "succeeded" || run["error"] != nil {
		t.Errorf("an empty list is not an error: %v", run)
	}
}

// TestConsentCannotBeCompletedByAnotherUserOrAfterTheTTL covers F4 through the API.
func TestConsentCannotBeCompletedByAnotherUserOrAfterTheTTL(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Now().UTC()
	alice := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	f.addAdmin(t, "heidi")
	heidi := bankCall{t: t, f: f, hdr: f.signIn(t, "heidi")}
	start := func() (string, string) {
		_, body, _ := alice.do("POST", "/api/v1/bank-connections", `{"application":"fake-app","institution_id":"Fake Bank","country":"FI"}`)
		state := stateOf(t, body["authorization_url"].(string))
		return asMap(t, body["connection"])["id"].(string), `{"state":"` + state + `","code":"` + bankingtest.AuthorizationCode(state).Reveal() + `"}`
	}
	id, complete := start()
	if code, body, _ := heidi.do("POST", "/api/v1/bank-connections/complete", complete); code != 409 || body["code"] != "consent_not_found" {
		t.Errorf("another administrator completing: %d %v", code, body)
	}
	if code, body, _ := alice.do("POST", "/api/v1/bank-connections/complete", complete); code != 200 || body["status"] != "active" {
		t.Errorf("the starter completing after the refusal: %d %v", code, body)
	}

	id2, complete2 := start()
	f.now = f.now.Add(2 * time.Hour)
	if code, body, _ := alice.do("POST", "/api/v1/bank-connections/complete", complete2); code != 409 || body["code"] != "consent_not_found" {
		t.Errorf("completing after the TTL: %d %v", code, body)
	}
	_, body, _ := alice.do("GET", "/api/v1/bank-connections/"+id2, "")
	if body["status"] != "failed" || asMap(t, body["last_error"])["code"] != "consent_expired" {
		t.Errorf("an expired consent must show as failed (consent_expired): %v", body)
	}
	if _, body, _ := alice.do("GET", "/api/v1/bank-connections/"+id, ""); body["status"] != "active" {
		t.Errorf("an unrelated connection changed: %v", body)
	}
}
