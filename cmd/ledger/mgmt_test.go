package main

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The CLI integration test runs the management commands against the real API
// handlers (registerAPI, as ledger serve wires them) behind a test
// authenticator that accepts fixed bearer values. The bearer-token
// authenticator itself is task A4's; here a value maps to a test principal.
const (
	adminToken = "ldgr_test_admin_token_value"
	roToken    = "ldgr_test_readonly_token_value"
	scopedTok  = "ldgr_test_commitments_read_token"
)

type cliFixture struct {
	t    *testing.T
	api  *apiFixture
	srv  *httptest.Server
	home string
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	f := newAPIFixture(t)
	h := f.srv.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Test-User")
		r.Header.Del("X-Test-Token-Grants")
		switch r.Header.Get("Authorization") {
		case "Bearer " + adminToken:
			r.Header.Set("X-Test-User", f.ids["alice"])
		case "Bearer " + roToken:
			r.Header.Set("X-Test-User", f.ids["bob"])
		case "Bearer " + scopedTok:
			r.Header.Set("X-Test-User", f.ids["alice"])
			r.Header.Set("X-Test-Token-Grants", "commitments:read")
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LEDGER_TOKEN", "")
	t.Setenv("LEDGER_URL", "")
	t.Setenv("LEDGER_CLIENT_CONFIG", "")
	return &cliFixture{t: t, api: f, srv: srv, home: home}
}

func (c *cliFixture) tokenFile(tok string) string {
	c.t.Helper()
	p := filepath.Join(c.t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		c.t.Fatal(err)
	}
	return p
}

// run executes the real entry point with the common flags appended and
// returns exit code, stdout and stderr. It fails when the token appears in
// either stream.
func (c *cliFixture) run(tok string, args ...string) (int, string, string) {
	c.t.Helper()
	var out, errb bytes.Buffer
	full := append(append([]string{}, args...), "--url", c.srv.URL, "--token-file", c.tokenFile(tok), "--timeout", "10s")
	rc := run(full, &out, &errb)
	for _, s := range []string{out.String(), errb.String()} {
		if strings.Contains(s, tok) {
			c.t.Fatalf("the token was printed: %q", s)
		}
	}
	return rc, out.String(), errb.String()
}

// json runs a command with --output json and decodes its output into v.
func (c *cliFixture) json(tok string, v any, args ...string) {
	c.t.Helper()
	rc, out, errb := c.run(tok, append(args, "--output", "json")...)
	if rc != 0 {
		c.t.Fatalf("%v: exit %d: %s", args, rc, errb)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		c.t.Fatalf("%v: %v: %s", args, err, out)
	}
}

func (c *cliFixture) fail(tok string, wantRC int, wantErr string, args ...string) {
	c.t.Helper()
	rc, out, errb := c.run(tok, args...)
	if rc != wantRC || !strings.Contains(errb, wantErr) {
		c.t.Fatalf("%v: exit %d (want %d), stderr %q (want %q), stdout %q", args, rc, wantRC, errb, wantErr, out)
	}
}

type idVersion struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

func TestCLIManagementJourney(t *testing.T) {
	c := newCLIFixture(t)

	// me and grants, text and JSON.
	rc, out, errb := c.run(adminToken, "me")
	if rc != 0 || !strings.Contains(out, "administrator") || !strings.Contains(out, "users:write") {
		t.Fatalf("me: %d %s %s", rc, out, errb)
	}
	var me struct {
		Role   string   `json:"role"`
		Grants []string `json:"grants"`
	}
	c.json(adminToken, &me, "me")
	if me.Role != "administrator" || len(me.Grants) == 0 {
		t.Fatalf("me json: %+v", me)
	}
	if rc, out, _ = c.run(adminToken, "grants"); rc != 0 || !strings.Contains(out, "commitments") || !strings.Contains(out, "role read-only:") {
		t.Fatalf("grants: %d %s", rc, out)
	}

	// users: create, get, list, update with an implicit and a stale version.
	var u idVersion
	c.json(adminToken, &u, "users", "create", "--username", "erin", "--role", "read-only", "--display-name", "Erin")
	if u.ID == "" || u.Version != 1 {
		t.Fatalf("user: %+v", u)
	}
	c.fail(adminToken, 1, "conflict", "users", "create", "--username", "erin", "--role", "read-only")
	var got struct {
		Role        string  `json:"role"`
		DisplayName *string `json:"display_name"`
		Version     int64   `json:"version"`
	}
	c.json(adminToken, &got, "users", "update", u.ID, "--role", "administrator", "--clear-display-name")
	if got.Role != "administrator" || got.DisplayName != nil || got.Version != 2 {
		t.Fatalf("update: %+v", got)
	}
	c.fail(adminToken, 1, "retry with --version 2", "users", "update", u.ID, "--disabled=true", "--version", "1")
	c.json(adminToken, &got, "users", "update", u.ID, "--disabled=true", "--version", "2")
	if rc, out, _ = c.run(adminToken, "users", "list", "--role", "administrator", "--username-prefix", "er"); rc != 0 || !strings.Contains(out, "erin") || strings.Contains(out, "alice") {
		t.Fatalf("users list: %d %s", rc, out)
	}
	c.fail(adminToken, 2, "nothing to change", "users", "update", u.ID)

	// commitments: validation reports every field.
	c.fail(adminToken, 1, "validation_failed", "commitments", "create", "--name", "", "--kind", "server", "--amount", "0", "--currency", "EUR",
		"--frequency", "monthly", "--day", "40", "--start-date", "2026-01-01")
	_, _, errb = c.run(adminToken, "commitments", "create", "--name", "", "--kind", "server", "--amount", "0", "--currency", "EUR", "--frequency", "monthly", "--day", "40", "--start-date", "2026-01-01")
	for _, f := range []string{"name", "amount", "schedule.day"} {
		if !strings.Contains(errb, f) {
			t.Errorf("validation output lacks field %s: %s", f, errb)
		}
	}
	month := time.Now().UTC().Format("2006-01")
	var cm1, cm2 struct {
		idVersion
		Amount   struct{ Amount, Currency string } `json:"amount"`
		Schedule struct {
			Frequency string `json:"frequency"`
			Interval  *int   `json:"interval"`
			Month     *int   `json:"month"`
			Day       int    `json:"day"`
		} `json:"schedule"`
		Priority *string `json:"priority"`
	}
	c.json(adminToken, &cm1, "commitments", "create", "--name", "VPS", "--kind", "server", "--amount", "12.50", "--currency", "EUR",
		"--frequency", "monthly", "--day", "15", "--start-date", month+"-01", "--priority", "essential")
	c.json(adminToken, &cm2, "commitments", "create", "--name", "Domain", "--kind", "domain", "--amount", "9.00", "--currency", "USD",
		"--frequency", "annual", "--month", "3", "--day", "1", "--start-date", month+"-01")
	if cm1.Amount.Amount != "12.50" || cm1.Schedule.Frequency != "monthly" || *cm1.Schedule.Interval != 1 || cm2.Schedule.Month == nil {
		t.Fatalf("created: %+v %+v", cm1, cm2)
	}

	// Switch cm1 to annual (the CLI nulls interval), keep the amount's scale, clear priority.
	cm1.Schedule.Interval, cm1.Schedule.Month = nil, nil // decode into a clean value: absent members keep old ones
	c.json(adminToken, &cm1, "commitments", "update", cm1.ID, "--frequency", "annual", "--month", "2", "--day", "28", "--amount", "13.5", "--clear-priority")
	if cm1.Schedule.Frequency != "annual" || cm1.Schedule.Interval != nil || *cm1.Schedule.Month != 2 || cm1.Amount.Amount != "13.5" || cm1.Amount.Currency != "EUR" || cm1.Priority != nil || cm1.Version != 2 {
		t.Fatalf("updated: %+v", cm1)
	}
	c.fail(adminToken, 1, "retry with --version 2", "commitments", "update", cm1.ID, "--name", "x", "--version", "1")
	if rc, out, _ = c.run(adminToken, "commitments", "get", cm1.ID); rc != 0 || !strings.Contains(out, "13.5 EUR") || !strings.Contains(out, "yearly on 02-28") {
		t.Fatalf("get: %d %s", rc, out)
	}

	// Paging: a page of one carries a cursor, --all follows it, filters stay fixed.
	var page struct {
		Items      []idVersion `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	c.json(adminToken, &page, "commitments", "list", "--limit", "1")
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("page: %+v", page)
	}
	_, out, _ = c.run(adminToken, "commitments", "list", "--limit", "1")
	if !strings.Contains(out, "--cursor "+*page.NextCursor) {
		t.Errorf("text list lacks the cursor hint: %s", out)
	}
	first := page.Items[0].ID
	var next struct {
		Items      []idVersion `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	c.json(adminToken, &next, "commitments", "list", "--limit", "1", "--cursor", *page.NextCursor)
	if len(next.Items) != 1 || next.Items[0].ID == first {
		t.Fatalf("second page: %+v", next)
	}
	page.Items, page.NextCursor = nil, nil
	c.json(adminToken, &page, "commitments", "list", "--limit", "1", "--all")
	if len(page.Items) != 2 || page.NextCursor != nil {
		t.Fatalf("--all: %+v", page)
	}
	page.Items = nil
	c.json(adminToken, &page, "commitments", "list", "--all", "--kind", "domain")
	if len(page.Items) != 1 || page.Items[0].ID != cm2.ID {
		t.Fatalf("filtered: %+v", page)
	}
	c.fail(adminToken, 2, "cannot be combined", "commitments", "list", "--all", "--cursor", "x")

	// Occurrences of the month, and one by ID: a monthly commitment due on the 15th.
	var cm3 idVersion
	c.json(adminToken, &cm3, "commitments", "create", "--name", "Rent", "--kind", "service", "--amount", "700", "--currency", "EUR",
		"--frequency", "monthly", "--day", "15", "--start-date", month+"-01")
	var occ struct {
		Items []struct {
			ID           string `json:"id"`
			CommitmentID string `json:"commitment_id"`
			DueDate      string `json:"due_date"`
		} `json:"items"`
	}
	c.json(adminToken, &occ, "occurrences", "list", "--month", month, "--commitment-id", cm3.ID)
	if len(occ.Items) != 1 || occ.Items[0].DueDate != month+"-15" || occ.Items[0].CommitmentID != cm3.ID {
		t.Fatalf("occurrences: %+v", occ)
	}
	if rc, out, _ = c.run(adminToken, "occurrences", "get", occ.Items[0].ID); rc != 0 || !strings.Contains(out, month+"-15") || !strings.Contains(out, "700 EUR") {
		t.Fatalf("occurrence get: %d %s", rc, out)
	}
	if rc, out, _ = c.run(adminToken, "occurrences", "list", "--month", month); rc != 0 || !strings.Contains(out, "DUE") {
		t.Fatalf("occurrence text list: %d %s", rc, out)
	}
	c.fail(adminToken, 1, "validation_failed", "occurrences", "list", "--month", "2026-13")

	// Delete needs the right version (read implicitly), then the object is gone.
	if rc, out, _ = c.run(adminToken, "commitments", "delete", cm2.ID); rc != 0 || !strings.Contains(out, "deleted "+cm2.ID) {
		t.Fatalf("delete: %d %s", rc, out)
	}
	c.fail(adminToken, 1, "not_found", "commitments", "get", cm2.ID)

	// The audit log shows what the CLI did, attributed to the acting user.
	var aud struct {
		Items []struct {
			Action     string `json:"action"`
			ActorKind  string `json:"actor_kind"`
			TargetType string `json:"target_type"`
		} `json:"items"`
	}
	c.json(adminToken, &aud, "audit", "list", "--action-prefix", "commitment.", "--all")
	var actions []string
	for _, e := range aud.Items {
		actions = append(actions, e.Action)
	}
	if strings.Join(actions, ",") != "commitment.created,commitment.created,commitment.updated,commitment.created,commitment.deleted" {
		t.Fatalf("audit actions: %v", actions)
	}
	if rc, out, _ = c.run(adminToken, "audit", "list", "--target-type", "commitment", "--actor-kind", "user"); rc != 0 || !strings.Contains(out, "commitment.created") {
		t.Fatalf("audit text: %d %s", rc, out)
	}
}

func TestCLIGrantsAndCredentialsAreEnforcedByTheServer(t *testing.T) {
	c := newCLIFixture(t)
	// A read-only user's token reads commitments but cannot write: 403 from the server.
	if rc, _, errb := c.run(roToken, "commitments", "list"); rc != 0 {
		t.Fatalf("read-only list: %d %s", rc, errb)
	}
	c.fail(roToken, 1, "lacks the grant", "commitments", "create", "--name", "X", "--kind", "server", "--amount", "1", "--currency", "EUR",
		"--frequency", "monthly", "--day", "1", "--start-date", "2026-01-01")
	c.fail(roToken, 1, "forbidden", "audit", "list")
	// A token scoped to commitments:read cannot list users even though its owner can.
	if rc, _, errb := c.run(scopedTok, "commitments", "list"); rc != 0 {
		t.Fatalf("scoped list: %d %s", rc, errb)
	}
	c.fail(scopedTok, 1, "forbidden", "users", "list")
	// A wrong token is refused, and its value never appears.
	c.fail("ldgr_not_a_valid_token", 1, "token was refused", "me")
	// No token at all is a usage error before any request is made.
	var out, errb bytes.Buffer
	if rc := run([]string{"me", "--url", c.srv.URL}, &out, &errb); rc != 2 || !strings.Contains(errb.String(), "no API token") {
		t.Fatalf("no token: %d %s", rc, errb.String())
	}
}

func TestCLITokenAndURLSources(t *testing.T) {
	c := newCLIFixture(t)
	runEnv := func(env map[string]string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		rc := runMgmt(args, &out, &errb, mgmtEnv{Getenv: func(k string) string { return env[k] }})
		return rc, out.String(), errb.String()
	}
	// LEDGER_TOKEN and LEDGER_URL.
	if rc, _, e := runEnv(map[string]string{"LEDGER_TOKEN": adminToken, "LEDGER_URL": c.srv.URL}, "me"); rc != 0 {
		t.Fatalf("env: %d %s", rc, e)
	}
	// A client config entry with token_file (relative to the config) and url.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tok"), []byte(adminToken), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "client.yaml")
	if err := os.WriteFile(cfg, []byte("url: "+c.srv.URL+"\ntoken_file: tok\noutput: json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, out, e := runEnv(nil, "me", "--client-config", cfg)
	if rc != 0 || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("config token_file: %d %s %s", rc, out, e)
	}
	// Precedence: LEDGER_TOKEN beats the config; --token-file beats LEDGER_TOKEN.
	if rc, _, _ := runEnv(map[string]string{"LEDGER_TOKEN": "wrong"}, "me", "--client-config", cfg); rc != 1 {
		t.Fatalf("env should win over config: %d", rc)
	}
	if rc, _, e := runEnv(map[string]string{"LEDGER_TOKEN": "wrong"}, "me", "--client-config", cfg, "--token-file", filepath.Join(dir, "tok")); rc != 0 {
		t.Fatalf("flag should win over env: %d %s", rc, e)
	}
	// An inline token in a config readable by others is refused; 0600 works.
	inline := filepath.Join(dir, "inline.yaml")
	if err := os.WriteFile(inline, []byte("url: "+c.srv.URL+"\ntoken: "+adminToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(inline, 0o644); err != nil { // the process umask may have masked the mode above
		t.Fatal(err)
	}
	if rc, _, e := runEnv(nil, "me", "--client-config", inline); rc != 2 || !strings.Contains(e, "readable by others") || strings.Contains(e, adminToken) {
		t.Fatalf("0644 inline token: %d %s", rc, e)
	}
	if err := os.Chmod(inline, 0o600); err != nil {
		t.Fatal(err)
	}
	if rc, _, e := runEnv(nil, "me", "--client-config", inline); rc != 0 {
		t.Fatalf("0600 inline token: %d %s", rc, e)
	}
	// Unknown config keys, a missing explicit config, a bad output and a plain-http remote are refused.
	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("tokn: x\n"), 0o600)
	if rc, _, e := runEnv(nil, "me", "--client-config", bad); rc != 2 || !strings.Contains(e, "tokn") {
		t.Fatalf("unknown key: %d %s", rc, e)
	}
	if rc, _, _ := runEnv(nil, "me", "--client-config", filepath.Join(dir, "missing.yaml")); rc != 2 {
		t.Fatalf("missing explicit config: %d", rc)
	}
	env := map[string]string{"LEDGER_TOKEN": adminToken, "LEDGER_URL": c.srv.URL}
	if rc, _, _ := runEnv(env, "me", "--output", "yaml"); rc != 2 {
		t.Fatalf("bad output: %d", rc)
	}
	env["LEDGER_URL"] = "http://ledger.example.com"
	if rc, _, e := runEnv(env, "me"); rc != 2 || !strings.Contains(e, "plain http") {
		t.Fatalf("plain http remote: %d %s", rc, e)
	}
}

func TestCLIUsageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{{"users"}, {"users", "nope"}, {"users", "get"}, {"commitments", "get", "--url", "x"}, {"users", "get", "id", "extra"}} {
		out.Reset()
		errb.Reset()
		if rc := run(args, &out, &errb); rc != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, rc, errb.String())
		}
	}
	out.Reset()
	if rc := run([]string{"users", "help"}, &out, &errb); rc != 0 {
		t.Errorf("help exit %d", rc)
	}
}

// TestCLIUsesOnlyThePublicAPI keeps the management commands from reaching
// storage or server internals: they may import the public client and nothing
// that opens a database.
func TestCLIUsesOnlyThePublicAPI(t *testing.T) {
	files, err := filepath.Glob("mgmt*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no mgmt files", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == "database/sql" || strings.Contains(p, "go-ledger/internal/") {
				t.Errorf("%s imports %s: management commands must use pkg/ledgerclient only", f, p)
			}
		}
	}
}
