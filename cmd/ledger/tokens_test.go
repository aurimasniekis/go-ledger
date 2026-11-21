package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
)

var tokenPattern = regexp.MustCompile(`^ldgr_[0-9A-Za-z]{43}$`)

type createdToken struct {
	Secret string `json:"secret"`
	Token  struct {
		ID         string     `json:"id"`
		Root       bool       `json:"root"`
		Grants     []string   `json:"grants"`
		Hint       string     `json:"hint"`
		Status     string     `json:"status"`
		ExpiresAt  *time.Time `json:"expires_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
	} `json:"token"`
	Warning *string `json:"warning"`
}

// createToken creates a token with the browser's session and fails the test
// unless it is 201.
func (e *signInEnv) createToken(b *browser, body string) createdToken {
	e.t.Helper()
	w := e.req("POST", "/api/v1/auth/tokens", body, b, nil)
	if w.Code != 201 {
		e.t.Fatalf("create token %s: %d %s", body, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		e.t.Errorf("token creation is cacheable: %q", w.Header().Get("Cache-Control"))
	}
	var c createdToken
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		e.t.Fatal(err)
	}
	if !tokenPattern.MatchString(c.Secret) || c.Token.Hint != c.Secret[:9]+"…"+c.Secret[len(c.Secret)-4:] {
		e.t.Fatalf("token format: hint %q", c.Token.Hint)
	}
	return c
}

func bearer(secret string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + secret}
}

func TestAPITokenLifecycle(t *testing.T) {
	e := newSignInEnv(t, nil)
	alice, _ := e.signIn("alice")
	c := e.createToken(alice, `{"name":"ci","grants":["commitments:write"]}`)
	if c.Token.Root || c.Warning != nil || strings.Join(c.Token.Grants, ",") != "commitments:write" || c.Token.Status != "active" || c.Token.ExpiresAt != nil {
		t.Fatalf("created %+v", c.Token)
	}
	tok := bearer(c.Secret)

	// The token authenticates as itself, with its own grants.
	w := e.req("GET", "/api/v1/me", "", nil, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"token"`) || !strings.Contains(w.Body.String(), `"token_id":"`+c.Token.ID+`"`) {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	// A write needs no CSRF token or recent password check, and is audited
	// as a token write that skipped interactive steps.
	if w := e.req("POST", "/api/v1/commitments", vpsBody, nil, tok); w.Code != 201 {
		t.Fatalf("token write: %d %s", w.Code, w.Body.String())
	}
	writes := e.auditActions(audit.TokenWriteUsed)
	if len(writes) != 1 || writes[0].Actor.Kind != audit.ActorToken || writes[0].Actor.TokenID != c.Token.ID || writes[0].Actor.UserID != e.ids["alice"] ||
		writes[0].Details["method"] != "POST" || writes[0].Details["route"] != "/api/v1/commitments" || writes[0].Details["interactive_steps"] != "skipped" {
		t.Fatalf("token.write_used entries: %+v", writes)
	}
	if c := e.auditActions(audit.CommitmentCreated); len(c) != 1 || c[0].Actor.Kind != audit.ActorToken {
		t.Fatalf("the change itself names the token: %+v", c)
	}
	// Reads are not write-audited.
	e.req("GET", "/api/v1/commitments", "", nil, tok)
	if n := len(e.auditActions(audit.TokenWriteUsed)); n != 1 {
		t.Fatalf("a read was audited as a write: %d", n)
	}
	// It has no other grant, and cannot manage tokens or sessions.
	for _, r := range [][2]string{{"GET", "/api/v1/users"}, {"GET", "/api/v1/tokens"}, {"GET", "/api/v1/auth/tokens"}, {"POST", "/api/v1/auth/tokens"}, {"GET", "/api/v1/auth/sessions"}} {
		if w := e.req(r[0], r[1], `{"name":"x","grants":["commitments:read"]}`, nil, tok); w.Code != 403 {
			t.Errorf("%s %s with the token: %d", r[0], r[1], w.Code)
		}
	}
	// A request with an Authorization header is judged by it alone: a wrong
	// token does not fall back to the session cookie.
	if w := e.req("GET", "/api/v1/me", "", alice, bearer("ldgr_"+strings.Repeat("Q", 43))); w.Code != 401 {
		t.Fatalf("wrong token with a valid cookie: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/me", "", alice, map[string]string{"Authorization": "Basic YWxpY2U6eA=="}); w.Code != 401 {
		t.Fatalf("basic auth with a valid cookie: %d", w.Code)
	}
	// Lists show last use but never the token.
	w = e.req("GET", "/api/v1/auth/tokens", "", alice, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), c.Token.ID) || strings.Contains(w.Body.String(), c.Secret) || strings.Contains(w.Body.String(), `"last_used_at":null`) {
		t.Fatalf("my tokens: %d %s", w.Code, w.Body.String())
	}
	// Another user cannot see or revoke it through their own list.
	bob, _ := e.signIn("bob")
	if w := e.req("GET", "/api/v1/auth/tokens/"+c.Token.ID, "", bob, nil); w.Code != 404 {
		t.Fatalf("bob reads alice's token: %d", w.Code)
	}
	if w := e.req("DELETE", "/api/v1/auth/tokens/"+c.Token.ID, "", bob, nil); w.Code != 404 {
		t.Fatalf("bob revokes alice's token: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/tokens", "", bob, nil); w.Code != 403 {
		t.Fatalf("read-only lists every token: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/tokens?owner_id="+e.ids["alice"], "", alice, nil); w.Code != 200 || !strings.Contains(w.Body.String(), c.Token.ID) {
		t.Fatalf("administrator lists alice's tokens: %d %s", w.Code, w.Body.String())
	}

	// Revocation takes effect on the next request.
	if w := e.req("DELETE", "/api/v1/auth/tokens/"+c.Token.ID, "", alice, nil); w.Code != 204 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/me", "", nil, tok); w.Code != 401 {
		t.Fatalf("revoked token still works: %d", w.Code)
	}
	if w := e.req("POST", "/api/v1/commitments", vpsBody, nil, tok); w.Code != 401 {
		t.Fatalf("revoked token still writes: %d", w.Code)
	}
	if w := e.req("DELETE", "/api/v1/auth/tokens/"+c.Token.ID, "", alice, nil); w.Code != 204 {
		t.Fatalf("second revoke: %d", w.Code)
	}
	if r := e.auditActions(audit.TokenRevoked); len(r) != 1 || r[0].TargetID != c.Token.ID || r[0].Actor.UserID != e.ids["alice"] {
		t.Fatalf("token.revoked entries: %+v", r)
	}
	w = e.req("GET", "/api/v1/auth/tokens/"+c.Token.ID, "", alice, nil)
	if !strings.Contains(w.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoked token: %s", w.Body.String())
	}

	// An administrator (or a token with tokens:write) revokes anyone's.
	bt := e.createToken(bob, `{"name":"reader","grants":["commitments:read"]}`)
	root := e.createToken(alice, `{"name":"root","root":true}`)
	if w := e.req("DELETE", "/api/v1/tokens/"+bt.Token.ID, "", nil, bearer(root.Secret)); w.Code != 204 {
		t.Fatalf("root token revokes bob's: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(bt.Secret)); w.Code != 401 {
		t.Fatalf("bob's revoked token: %d", w.Code)
	}
	if r := e.auditActions(audit.TokenRevoked); len(r) != 2 || r[1].Actor.Kind != audit.ActorToken || r[1].Actor.TokenID != root.Token.ID {
		t.Fatalf("revocation by a token: %+v", r)
	}
}

func TestAPITokenEscalationIsRejected(t *testing.T) {
	e := newSignInEnv(t, nil)
	bob, _ := e.signIn("bob")
	alice, _ := e.signIn("alice")
	for _, body := range []string{
		`{"name":"x","grants":["commitments:write"]}`,
		`{"name":"x","grants":["commitments:read","audit:read"]}`,
		`{"name":"x","grants":["users:read"]}`,
		`{"name":"x","root":true}`,
	} {
		w := e.req("POST", "/api/v1/auth/tokens", body, bob, nil)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "forbidden") {
			t.Errorf("read-only user asked for %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for body, field := range map[string]string{
		`{"name":"x","grants":["nope:read"]}`:                                      "grants",
		`{"name":"x","grants":[]}`:                                                 "grants",
		`{"name":"x","root":true,"grants":["users:read"]}`:                         "grants",
		`{"grants":["users:read"]}`:                                                "name",
		`{"name":"x","grants":["users:read"],"expires_at":"2026-01-01T00:00:00Z"}`: "expires_at",
	} {
		w := e.req("POST", "/api/v1/auth/tokens", body, alice, nil)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if n := len(e.auditActions(audit.TokenCreated)); n != 0 {
		t.Fatalf("refused requests created %d tokens", n)
	}
	// Within one's grants it works; a root token carries the warning.
	e.createToken(bob, `{"name":"reader","grants":["commitments:read"]}`)
	root := e.createToken(alice, `{"name":"root","root":true}`)
	if !root.Token.Root || root.Warning == nil || !strings.Contains(*root.Warning, "root token") {
		t.Fatalf("root token: %+v", root)
	}
	if w := e.req("GET", "/api/v1/audit", "", nil, bearer(root.Secret)); w.Code != 200 {
		t.Fatalf("root token reads the audit log: %d", w.Code)
	}
	created := e.auditActions(audit.TokenCreated)
	if len(created) != 2 || created[1].Details["root"] != "true" || created[0].Details["grants"] != "commitments:read" || created[0].Actor.UserID != e.ids["bob"] {
		t.Fatalf("token.created entries: %+v", created)
	}
}

func TestAPITokenCreationNeedsRecentAuthentication(t *testing.T) {
	e := newSignInEnv(t, nil)
	alice, _ := e.signIn("alice")
	e.clock.advance(11 * time.Minute)
	w := e.req("POST", "/api/v1/auth/tokens", `{"name":"ci","grants":["commitments:read"]}`, alice, nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "reauthentication_required") {
		t.Fatalf("stale session: %d %s", w.Code, w.Body.String())
	}
	// Without the CSRF token the request is refused before anything else.
	if w := e.req("POST", "/api/v1/auth/tokens", `{"name":"ci","grants":["commitments:read"]}`, &browser{cookie: alice.cookie}, nil); w.Code != 403 || !strings.Contains(w.Body.String(), "csrf") {
		t.Fatalf("no CSRF token: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`"}`, alice, nil); w.Code != 200 {
		t.Fatalf("reauthenticate: %d", w.Code)
	}
	e.createToken(alice, `{"name":"ci","grants":["commitments:read"]}`)
}

func TestAPITokenExpiry(t *testing.T) {
	e := newSignInEnv(t, nil)
	alice, _ := e.signIn("alice")
	exp := e.clock.now().Add(time.Hour).Format(time.RFC3339)
	c := e.createToken(alice, `{"name":"short","grants":["commitments:read"],"expires_at":"`+exp+`"}`)
	if c.Token.ExpiresAt == nil || c.Token.ExpiresAt.Format(time.RFC3339) != exp {
		t.Fatalf("expires_at %v", c.Token.ExpiresAt)
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(c.Secret)); w.Code != 200 {
		t.Fatalf("before expiry: %d", w.Code)
	}
	e.clock.advance(time.Hour)
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(c.Secret)); w.Code != 401 {
		t.Fatalf("at expiry: %d", w.Code)
	}
	alice, _ = e.signIn("alice")
	if w := e.req("GET", "/api/v1/auth/tokens/"+c.Token.ID, "", alice, nil); !strings.Contains(w.Body.String(), `"status":"expired"`) {
		t.Fatalf("expired token: %s", w.Body.String())
	}
}

func TestAPITokenSurvivesPasswordChangeButNotDisabling(t *testing.T) {
	e := newSignInEnv(t, nil)
	alice, _ := e.signIn("alice")
	c := e.createToken(alice, `{"name":"ci","grants":["commitments:read"]}`)
	// ADR-0029 (Proposed): a password change ends other sessions but leaves
	// API tokens alone; revoke them explicitly.
	if w := e.req("PUT", "/api/v1/auth/password", `{"password":"another long passphrase"}`, alice, nil); w.Code != 200 {
		t.Fatalf("password change: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(c.Secret)); w.Code != 200 {
		t.Fatalf("after password change: %d", w.Code)
	}
	bob, _ := e.signIn("bob")
	bt := e.createToken(bob, `{"name":"reader","grants":["commitments:read"]}`)
	if _, err := e.parts.users.Update(context.Background(), audit.System, e.ids["bob"], 1, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(bt.Secret)); w.Code != 401 {
		t.Fatalf("disabled owner's token: %d", w.Code)
	}
}

// TestAPITokenNeverLoggedOrAudited is the sentinel test: after creating,
// using (reads, writes, refusals), revoking and reusing tokens, neither the
// log, the audit log, any response except the creation's, nor any database
// row contains a token.
func TestAPITokenNeverLoggedOrAudited(t *testing.T) {
	e := newSignInEnv(t, nil)
	alice, _ := e.signIn("alice")
	var bodies bytes.Buffer
	reqs := func(method, path, body string, b *browser, hdr map[string]string) {
		w := e.req(method, path, body, b, hdr)
		bodies.WriteString(w.Body.String())
		for k, v := range w.Header() {
			bodies.WriteString(k + ": " + strings.Join(v, ",") + "\n")
		}
	}
	c := e.createToken(alice, `{"name":"ci","grants":["commitments:write"]}`)
	root := e.createToken(alice, `{"name":"root","root":true}`)
	unknown := "ldgr_" + strings.Repeat("S3nt1nel", 6)[:43]
	secrets := []string{c.Secret, root.Secret, unknown}
	tok := bearer(c.Secret)
	reqs("GET", "/api/v1/me", "", nil, tok)
	reqs("POST", "/api/v1/commitments", vpsBody, nil, tok)
	reqs("POST", "/api/v1/commitments", `{"name":""}`, nil, tok) // a validation error
	reqs("GET", "/api/v1/users", "", nil, tok)                   // forbidden
	reqs("GET", "/api/v1/users?token="+c.Secret[5:], "", nil, bearer(root.Secret))
	reqs("GET", "/api/v1/me", "", nil, bearer(unknown))
	reqs("GET", "/api/v1/me", "", nil, map[string]string{"Authorization": "bearer " + c.Secret + "x"})
	reqs("GET", "/api/v1/auth/tokens", "", alice, nil)
	reqs("GET", "/api/v1/tokens", "", nil, bearer(root.Secret))
	reqs("DELETE", "/api/v1/auth/tokens/"+c.Token.ID, "", alice, nil)
	reqs("POST", "/api/v1/commitments", vpsBody, nil, tok) // revoked
	reqs("DELETE", "/api/v1/tokens/"+root.Token.ID, "", nil, bearer(root.Secret))
	reqs("GET", "/api/v1/me", "", nil, bearer(root.Secret))

	records, err := e.al.List(context.Background(), audit.Filter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	auditJSON, _ := json.Marshal(records)
	if !strings.Contains(string(auditJSON), "token.write_used") || !strings.Contains(string(auditJSON), "token.revoked") || !strings.Contains(e.log.String(), "/api/v1/me") {
		t.Fatal("the sentinel test saw no token activity")
	}
	dump := dumpDatabase(t, e.db)
	for i, s := range secrets {
		for where, text := range map[string]string{"log": e.log.String(), "audit log": string(auditJSON), "responses": bodies.String(), "database": dump} {
			// The random part alone must not appear either (a prefix-stripped copy).
			if strings.Contains(text, s) || strings.Contains(text, s[5:]) {
				t.Errorf("token %d appears in the %s", i, where)
			}
		}
	}
}

// dumpDatabase returns every row of every table as text.
func dumpDatabase(t *testing.T, db *store.DB) string {
	t.Helper()
	var b strings.Builder
	err := db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table'")
		if err != nil {
			return err
		}
		var tables []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			tables = append(tables, n)
		}
		_ = rows.Close()
		for _, tbl := range tables {
			rs, err := tx.QueryContext(ctx, "SELECT * FROM \""+tbl+"\"")
			if err != nil {
				return err
			}
			cols, _ := rs.Columns()
			for rs.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rs.Scan(ptrs...); err != nil {
					return err
				}
				for _, v := range vals {
					if bs, ok := v.([]byte); ok {
						v = string(bs)
					}
					fmt.Fprint(&b, v, "|")
				}
				b.WriteString("\n")
			}
			_ = rs.Close()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestTokenCreateCommandBootstrapsAFreshDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ledger.db")
	env := opsEnv{Options: config.Options{
		LookupEnv: func(k string) (string, bool) {
			switch k {
			case "LEDGER_DATABASE_PATH":
				return dbPath, true
			case "LEDGER_KEK_VALUE":
				return "test-kek-value-0123456789abcdef", true
			}
			return "", false
		},
		Environ: func() []string { return nil },
	}}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runToken(args, &out, &errb, env)
		return code, out.String(), errb.String()
	}
	for _, args := range [][]string{nil, {"create"}, {"list"}, {"create", "--grant", "nope:read", "admin"}, {"create", "admin"},
		{"create", "--root", "--grant", "users:read", "admin"}, {"create", "--root", "--expires", "yesterday", "admin"}} {
		if code, stdout, _ := run(args...); code != 2 || stdout != "" {
			t.Errorf("%q: exit %d, stdout %q", args, code, stdout)
		}
	}
	if code, stdout, stderr := run("create", "--root", "admin"); code != 1 || stdout != "" || !strings.Contains(stderr, "--create") {
		t.Fatalf("missing user: %d %q %s", code, stdout, stderr)
	}
	// A read-only owner cannot get a root token, and the refusal leaves no user behind.
	if code, stdout, stderr := run("create", "--create", "read-only", "--root", "viewer"); code != 1 || stdout != "" || !strings.Contains(stderr, "cannot have grants") {
		t.Fatalf("read-only root: %d %q %s", code, stdout, stderr)
	}

	// Bootstrap: a fresh database, an administrator created on the way, a root token printed once.
	code, stdout, stderr := run("create", "--create", "administrator", "--root", "admin")
	if code != 0 {
		t.Fatalf("bootstrap: %d %s", code, stderr)
	}
	secret := strings.TrimSuffix(stdout, "\n")
	if !tokenPattern.MatchString(secret) || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout is not exactly the token: %q", stdout)
	}
	if strings.Contains(stderr, secret) || strings.Contains(stderr, secret[5:]) || !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "root token") || !strings.Contains(stderr, "created user admin") {
		t.Fatalf("stderr: %s", stderr)
	}
	code, stdout2, stderr := run("create", "--name", "reports", "--grant", "commitments:read,users:read", "--expires", "24h", "admin")
	if code != 0 || strings.Contains(stderr, "WARNING") {
		t.Fatalf("grant token: %d %s", code, stderr)
	}
	narrow := strings.TrimSpace(stdout2)

	db, err := store.Open(context.Background(), dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	al := store.NewAuditLog(db)
	parts := mustParts(t, db, al)
	p, err := parts.tokens.Authenticate(context.Background(), secret)
	if err != nil || p.Kind != authz.PrincipalToken || !p.Grants.Covers(authz.AllGrants()) {
		t.Fatalf("bootstrap token: %+v %v", p, err)
	}
	p, err = parts.tokens.Authenticate(context.Background(), narrow)
	if err != nil || strings.Join(p.Grants.Strings(), ",") != "commitments:read,users:read" {
		t.Fatalf("grant token: %+v %v", p.Grants, err)
	}
	if strings.Contains(dumpDatabase(t, db), secret[5:]) {
		t.Fatal("the database holds the token")
	}
	if _, err := authstore.New(db, al).CredentialByUsername(context.Background(), "viewer"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatal("the refused bootstrap created a user")
	}
	created, _ := al.List(context.Background(), audit.Filter{Actions: []audit.Action{audit.TokenCreated}, Limit: 10})
	if len(created) != 2 || created[0].Actor.Kind != audit.ActorSystem || created[0].Details["root"] != "true" || created[1].Details["expires_at"] == "never" {
		t.Fatalf("token.created entries: %+v", created)
	}
	if n, _ := al.List(context.Background(), audit.Filter{Actions: []audit.Action{audit.UserCreated}, Limit: 10}); len(n) != 1 {
		t.Fatalf("user.created entries: %d", len(n))
	}
}
