package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// addAdministrator creates another administrator with the test password, so
// the root account (alice, the first user of signInEnv) can be disabled or
// demoted without breaking the last-administrator rule.
func (e *signInEnv) addAdministrator(name string) {
	e.t.Helper()
	ctx := context.Background()
	u, err := e.parts.users.Create(ctx, audit.System, users.New{Username: name, Role: authz.RoleAdministrator})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.parts.auth.SetPassword(ctx, authz.Principal{}, true, u.ID, testPassword, auth.ByHost); err != nil {
		e.t.Fatal(err)
	}
	e.ids[name] = u.ID
}

const tokenBody = `{"name":"child","grants":["commitments:read"]}`

// TestOnlyARootTokenOfTheRootAccountCreatesTokens is decision D25: a root
// token of the root account (the first user) may create tokens through the
// API; every other token is refused with 403, and sessions keep their rule.
func TestOnlyARootTokenOfTheRootAccountCreatesTokens(t *testing.T) {
	e := newSignInEnv(t, nil)
	e.addAdministrator("erin")
	root, err := e.parts.users.RootAccountID(context.Background())
	if err != nil || root != e.ids["alice"] {
		t.Fatalf("root account = %q, %v; want alice (the first user)", root, err)
	}
	alice, _ := e.signIn("alice")
	rootTok := e.createToken(alice, `{"name":"terraform","root":true}`)
	narrow := e.createToken(alice, `{"name":"ci","grants":["commitments:write","tokens:write"]}`)
	erin, _ := e.signIn("erin")
	erinRoot := e.createToken(erin, `{"name":"other-root","root":true}`)
	bob, _ := e.signIn("bob")
	bobTok := e.createToken(bob, `{"name":"reader","grants":["commitments:read"]}`)

	// The root account's root token creates a token, needing no re-authentication.
	e.clock.advance(11 * time.Minute)
	w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(rootTok.Secret))
	if w.Code != 201 {
		t.Fatalf("root token of the root account: %d %s", w.Code, w.Body.String())
	}
	var child createdToken
	if err := json.Unmarshal(w.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if child.Token.Root || strings.Join(child.Token.Grants, ",") != "commitments:read" {
		t.Fatalf("child token: %+v", child.Token)
	}
	created := e.auditActions(audit.TokenCreated)
	last := created[len(created)-1]
	if last.TargetID != child.Token.ID || last.Actor.Kind != audit.ActorToken || last.Actor.TokenID != rootTok.Token.ID ||
		last.Actor.UserID != e.ids["alice"] || last.Details["parent_token_id"] != rootTok.Token.ID {
		t.Fatalf("token.created by a token: %+v", last)
	}
	if got, err := e.parts.tokens.Get(context.Background(), child.Token.ID, e.ids["alice"]); err != nil || got.OwnerID != e.ids["alice"] {
		t.Fatalf("child is owned by the root account: %+v %v", got, err)
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(child.Secret)); w.Code != 200 {
		t.Fatalf("the child token works: %d", w.Code)
	}
	// It may also create a root token (it holds every grant).
	if w := e.req("POST", "/api/v1/auth/tokens", `{"name":"r2","root":true}`, nil, bearer(rootTok.Secret)); w.Code != 201 || !strings.Contains(w.Body.String(), "root token") {
		t.Fatalf("root token creates a root token: %d %s", w.Code, w.Body.String())
	}
	// A session-created token has no parent.
	if s := e.auditActions(audit.TokenCreated)[0]; s.Details["parent_token_id"] != "" || s.Actor.Kind != audit.ActorUser {
		t.Fatalf("session-created token entry: %+v", s)
	}

	// Every other token is refused with 403 and creates nothing.
	n := len(e.auditActions(audit.TokenCreated))
	for name, secret := range map[string]string{
		"a non-root token of the root account":     narrow.Secret,
		"a root token of another administrator":    erinRoot.Secret,
		"a read-only user's token":                 bobTok.Secret,
		"a token created by the root token itself": child.Secret,
	} {
		w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(secret))
		if w.Code != 403 || !strings.Contains(w.Body.String(), "forbidden") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if got := len(e.auditActions(audit.TokenCreated)); got != n {
		t.Fatalf("refused tokens created %d tokens", got-n)
	}
	// Refused before the handler: no token.write_used entry for them.
	for _, wu := range e.auditActions(audit.TokenWriteUsed) {
		if wu.Actor.TokenID != rootTok.Token.ID {
			t.Errorf("a refused token's request was write-audited: %+v", wu)
		}
	}
	// Other token routes stay session-only, even for the root token.
	for _, r := range [][2]string{{"GET", "/api/v1/auth/tokens"}, {"DELETE", "/api/v1/auth/tokens/" + narrow.Token.ID}} {
		if w := e.req(r[0], r[1], "", nil, bearer(rootTok.Secret)); w.Code != 403 {
			t.Errorf("%s %s with the root token: %d", r[0], r[1], w.Code)
		}
	}
	// Sessions keep their rule: any user, within their grants, with recent
	// re-authentication.
	if w := e.req("POST", "/api/v1/auth/tokens", tokenBody, bob, nil); w.Code != 403 || !strings.Contains(w.Body.String(), "reauthentication_required") {
		t.Fatalf("stale session: %d %s", w.Code, w.Body.String())
	}
}

// TestRootTokenFollowsTheRootAccountsState: the root account stays the root
// account while disabled or demoted (D25): disabled, its tokens stop (and no
// other account takes over); re-enabled, its unrevoked tokens work again
// (D25-2); demoted, its root token is bounded by the new role.
func TestRootTokenFollowsTheRootAccountsState(t *testing.T) {
	e := newSignInEnv(t, nil)
	e.addAdministrator("erin")
	alice, _ := e.signIn("alice")
	rootTok := e.createToken(alice, `{"name":"terraform","root":true}`)
	erin, _ := e.signIn("erin")
	erinRoot := e.createToken(erin, `{"name":"other-root","root":true}`)
	ctx := context.Background()
	version := func() int64 {
		u, err := e.parts.users.Get(ctx, e.ids["alice"])
		if err != nil {
			t.Fatal(err)
		}
		return u.Version
	}
	if _, err := e.parts.users.Update(ctx, audit.System, e.ids["alice"], version(), users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(rootTok.Secret)); w.Code != 401 {
		t.Fatalf("disabled root account's root token: %d", w.Code)
	}
	if root, _ := e.parts.users.RootAccountID(ctx); root != e.ids["alice"] {
		t.Fatalf("the root account moved to %s while alice is disabled", root)
	}
	if w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(erinRoot.Secret)); w.Code != 403 {
		t.Fatalf("another administrator's root token while the root account is disabled: %d", w.Code)
	}
	if _, err := e.parts.users.Update(ctx, audit.System, e.ids["alice"], version(), users.Patch{Disabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(rootTok.Secret)); w.Code != 201 {
		t.Fatalf("re-enabled root account's root token: %d %s", w.Code, w.Body.String())
	}
	ro := authz.RoleReadOnly
	if _, err := e.parts.users.Update(ctx, audit.System, e.ids["alice"], version(), users.Patch{Role: &ro}); err != nil {
		t.Fatal(err)
	}
	if w := e.req("POST", "/api/v1/auth/tokens", `{"name":"r","root":true}`, nil, bearer(rootTok.Secret)); w.Code != 403 || !strings.Contains(w.Body.String(), "root") {
		t.Fatalf("demoted root account asks for a root token: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/tokens", `{"name":"w","grants":["commitments:write"]}`, nil, bearer(rootTok.Secret)); w.Code != 403 {
		t.Fatalf("demoted root account asks for a write grant: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(rootTok.Secret)); w.Code != 201 {
		t.Fatalf("demoted root account, within its role: %d %s", w.Code, w.Body.String())
	}
	// A revoked root token stops at once.
	if w := e.req("DELETE", "/api/v1/auth/tokens/"+rootTok.Token.ID, "", alice, nil); w.Code != 204 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(rootTok.Secret)); w.Code != 401 {
		t.Fatalf("revoked root token: %d", w.Code)
	}
}

// TestGoClientCreatesTokensWithTheRootToken drives CreateMyToken of the Go
// client against the real handlers.
func TestGoClientCreatesTokensWithTheRootToken(t *testing.T) {
	e := newSignInEnv(t, nil)
	srv := httptest.NewServer(e.srv.Handler())
	t.Cleanup(srv.Close)
	alice, _ := e.signIn("alice")
	rootTok := e.createToken(alice, `{"name":"terraform","root":true}`)
	narrow := e.createToken(alice, `{"name":"ci","grants":["commitments:read"]}`)
	c, err := ledgerclient.New(srv.URL, rootTok.Secret, ledgerclient.WithInsecureHTTP())
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.CreateMyToken(context.Background(), ledgerclient.NewAPIToken{Name: "from-client", Grants: []string{"commitments:read"}})
	if err != nil || !tokenPattern.MatchString(got.Secret) || got.Token.Name != "from-client" || got.Token.OwnerID != e.ids["alice"] || got.Warning != nil {
		t.Fatalf("CreateMyToken: %+v %v", got, err)
	}
	r, err := c.CreateMyToken(context.Background(), ledgerclient.NewAPIToken{Name: "root-from-client", Root: true})
	if err != nil || !r.Token.Root || r.Warning == nil {
		t.Fatalf("CreateMyToken root: %+v %v", r, err)
	}
	other, err := ledgerclient.New(srv.URL, narrow.Secret, ledgerclient.WithInsecureHTTP())
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.CreateMyToken(context.Background(), ledgerclient.NewAPIToken{Name: "x", Grants: []string{"commitments:read"}})
	var ae *ledgerclient.APIError
	if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "forbidden" {
		t.Fatalf("a non-root token through the client: %v", err)
	}
}

// TestTokenCreatedTokensAreContained is the staff reviewer's probe (A4e): a
// root token expiring in 2030 mints a child with no expiry, then is revoked.
// The child expires with its parent and is revoked, with its own children,
// when the parent is; the revocations are audited with cascaded_from.
func TestTokenCreatedTokensAreContained(t *testing.T) {
	e := newSignInEnv(t, nil)
	alice, _ := e.signIn("alice")
	parent := e.createToken(alice, `{"name":"terraform","root":true,"expires_at":"2030-01-01T00:00:00Z"}`)
	forever := e.createToken(alice, `{"name":"forever","root":true}`)
	e.clock.advance(11 * time.Minute)

	mint := func(secret, body string) createdToken {
		t.Helper()
		w := e.req("POST", "/api/v1/auth/tokens", body, nil, bearer(secret))
		if w.Code != 201 {
			t.Fatalf("mint %s: %d %s", body, w.Code, w.Body.String())
		}
		var c createdToken
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	want := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	child := mint(parent.Secret, `{"name":"child","root":true}`)
	late := mint(parent.Secret, `{"name":"late","grants":["commitments:read"],"expires_at":"2035-01-01T00:00:00Z"}`)
	early := mint(parent.Secret, `{"name":"early","grants":["commitments:read"],"expires_at":"2029-01-01T00:00:00Z"}`)
	if child.Token.ExpiresAt == nil || !child.Token.ExpiresAt.Equal(want) || late.Token.ExpiresAt == nil || !late.Token.ExpiresAt.Equal(want) {
		t.Fatalf("a child of a parent expiring in 2030 expires at %v / %v, want 2030", child.Token.ExpiresAt, late.Token.ExpiresAt)
	}
	if early.Token.ExpiresAt == nil || early.Token.ExpiresAt.Year() != 2029 {
		t.Fatalf("an earlier requested expiry is kept: %v", early.Token.ExpiresAt)
	}
	grandchild := mint(child.Secret, `{"name":"grandchild","grants":["commitments:read"]}`)
	if grandchild.Token.ExpiresAt == nil || !grandchild.Token.ExpiresAt.Equal(want) {
		t.Fatalf("a grandchild expires at %v, want the parent's 2030", grandchild.Token.ExpiresAt)
	}
	// A parent that never expires leaves the requested expiry (none) alone.
	if c := mint(forever.Secret, `{"name":"free","grants":["commitments:read"]}`); c.Token.ExpiresAt != nil {
		t.Fatalf("child of a token without expiry: %v", c.Token.ExpiresAt)
	}
	grand := mint(forever.Secret, `{"name":"mid","root":true}`)
	leaf := mint(grand.Secret, `{"name":"leaf","grants":["commitments:read"]}`)

	for name, secret := range map[string]string{"child": child.Secret, "grandchild": grandchild.Secret, "late": late.Secret, "early": early.Secret, "leaf": leaf.Secret} {
		if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(secret)); w.Code != 200 {
			t.Fatalf("%s before revocation: %d", name, w.Code)
		}
	}
	if w := e.req("DELETE", "/api/v1/auth/tokens/"+parent.Token.ID, "", alice, nil); w.Code != 204 {
		t.Fatalf("revoke parent: %d %s", w.Code, w.Body.String())
	}
	for name, secret := range map[string]string{"parent": parent.Secret, "child": child.Secret, "grandchild": grandchild.Secret, "late": late.Secret, "early": early.Secret} {
		if w := e.req("POST", "/api/v1/auth/tokens", tokenBody, nil, bearer(secret)); w.Code != 401 {
			t.Errorf("%s after the parent's revocation creates tokens: %d", name, w.Code)
		}
		if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(secret)); w.Code != 401 {
			t.Errorf("%s after the parent's revocation: %d", name, w.Code)
		}
	}
	// Another lineage is untouched; revoking the middle token revokes its leaf only.
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(leaf.Secret)); w.Code != 200 {
		t.Fatalf("an unrelated lineage was revoked: %d", w.Code)
	}
	if w := e.req("DELETE", "/api/v1/auth/tokens/"+grand.Token.ID, "", alice, nil); w.Code != 204 {
		t.Fatalf("revoke middle: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(leaf.Secret)); w.Code != 401 {
		t.Fatalf("leaf after its parent's revocation: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(forever.Secret)); w.Code != 200 {
		t.Fatalf("the grandparent was revoked: %d", w.Code)
	}
	cascaded := map[string]string{}
	for _, ev := range e.auditActions(audit.TokenRevoked) {
		if from := ev.Details["cascaded_from"]; from != "" {
			cascaded[ev.TargetID] = from
		}
	}
	for _, c := range []createdToken{child, grandchild, late, early} {
		if cascaded[c.Token.ID] != parent.Token.ID {
			t.Errorf("no token.revoked entry with cascaded_from for %s: %v", c.Token.ID, cascaded)
		}
	}
	if cascaded[leaf.Token.ID] != grand.Token.ID {
		t.Errorf("leaf cascade entry: %v", cascaded)
	}
}
