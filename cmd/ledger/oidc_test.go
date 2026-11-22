package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/oidc/oidctest"
	"github.com/aurimasniekis/go-ledger/internal/redact"
	"github.com/aurimasniekis/go-ledger/internal/store"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
)

// The single-sign-on tests run ledger serve's API stack against a local fake
// OpenID Connect provider (internal/oidc/oidctest): no real provider, no
// network beyond loopback.

const oidcRedirect = "https://ledger.example.com/api/v1/auth/oidc/callback"

type oidcEnv struct {
	*signInEnv
	op *oidctest.Provider
}

func newOIDCEnv(t *testing.T, mod func(*config.Auth)) *oidcEnv {
	t.Helper()
	op := oidctest.New(t)
	e := newSignInEnvWith(t, func(c *config.Auth) {
		c.OIDC = config.AuthOIDC{Enabled: true, Name: "Test IdP", Issuer: op.Issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
			RedirectURL: oidcRedirect, Scopes: []string{"openid", "email", "profile"}, ClockSkew: time.Minute,
			Provisioning: config.AuthOIDCProvisioning{Role: "read-only"}}
		if mod != nil {
			mod(c)
		}
	}, func(e *signInEnv, cfg config.Auth) {
		op.Now = e.clock.now
		d, err := oidcDeps(cfg.OIDC, e.clock.now)
		if err != nil {
			t.Fatal(err)
		}
		e.parts.oidc = d
	})
	return &oidcEnv{signInEnv: e, op: op}
}

func cookieValue(w *httptest.ResponseRecorder, name string) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == name && c.MaxAge >= 0 {
			return c.Value
		}
	}
	return ""
}

// authorize starts a sign-in and returns the provider URL and the state cookie.
func (e *oidcEnv) authorize(query string) (string, string) {
	e.t.Helper()
	w := e.req("GET", httpapi.OIDCAuthorizePath+query, "", nil, nil)
	if w.Code != 303 {
		e.t.Fatalf("authorize: %d %s", w.Code, w.Body.String())
	}
	return w.Header().Get("Location"), cookieValue(w, httpapi.OIDCCookie)
}

// callback plays the provider leg and returns Ledger's answer to the callback.
func (e *oidcEnv) callback(providerURL, cookie string) (*httptest.ResponseRecorder, string) {
	e.t.Helper()
	cb, err := e.op.Login(providerURL)
	if err != nil {
		e.t.Fatal(err)
	}
	if !strings.HasPrefix(cb.String(), oidcRedirect+"?") {
		e.t.Fatalf("the provider sent the browser to %s", cb)
	}
	return e.replay(cb.RequestURI(), cookie), cb.RequestURI()
}

func (e *oidcEnv) replay(uri, cookie string) *httptest.ResponseRecorder {
	hdr := map[string]string{}
	if cookie != "" {
		hdr["Cookie"] = httpapi.OIDCCookie + "=" + cookie
	}
	return e.req("GET", uri, "", nil, hdr)
}

func (e *oidcEnv) signInOIDC() *httptest.ResponseRecorder {
	e.t.Helper()
	w, _ := e.callback(e.authorize(""))
	return w
}

// sessionFrom returns the browser of a callback that started a session.
func (e *oidcEnv) sessionFrom(w *httptest.ResponseRecorder) *browser {
	e.t.Helper()
	tok := cookieValue(w, httpapi.SessionCookie)
	if tok == "" {
		e.t.Fatalf("no session cookie: %d %s", w.Code, w.Header().Get("Location"))
	}
	b := &browser{cookie: tok}
	r := e.req("GET", "/api/v1/auth/session", "", b, nil)
	var cs currentSession
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &cs) != nil {
		e.t.Fatalf("session: %d %s", r.Code, r.Body.String())
	}
	b.csrf = cs.CSRFToken
	return b
}

// start posts to a route that answers with an authorization URL and returns
// it with the state cookie.
func (e *oidcEnv) start(path string, b *browser) (string, string) {
	e.t.Helper()
	w := e.req("POST", path, "", b, nil)
	if w.Code != 200 {
		e.t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	var out struct {
		URL string `json:"authorization_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !strings.HasPrefix(out.URL, e.op.Issuer+"/authorize?") {
		e.t.Fatalf("authorization_url %q", out.URL)
	}
	return out.URL, cookieValue(w, httpapi.OIDCCookie)
}

// link links the provider's current subject to b's account.
func (e *oidcEnv) link(b *browser) *httptest.ResponseRecorder {
	e.t.Helper()
	w, _ := e.callback(e.start("/api/v1/auth/oidc/link", b))
	return w
}

func location(w *httptest.ResponseRecorder) string { return w.Header().Get("Location") }

func TestOIDCLinkThenSignIn(t *testing.T) {
	e := newOIDCEnv(t, nil)
	alice, _ := e.signIn("alice")
	if w := e.req("GET", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"linked":false`) ||
		!strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatalf("link status: %d %s", w.Code, w.Body.String())
	}
	w := e.link(alice)
	if w.Code != 303 || location(w) != "/?oidc=linked" || cookieValue(w, httpapi.SessionCookie) != "" {
		t.Fatalf("link callback: %d %q", w.Code, location(w))
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("callback headers %v", w.Header())
	}
	st := e.req("GET", "/api/v1/auth/oidc/link", "", alice, nil).Body.String()
	if !strings.Contains(st, `"linked":true`) || !strings.Contains(st, `"subject":"subject-0001"`) || !strings.Contains(st, `"provider":"Test IdP"`) {
		t.Fatalf("link status: %s", st)
	}
	linked := e.auditActions(audit.OIDCLinked)
	if len(linked) != 1 || linked[0].Actor.UserID != e.ids["alice"] || linked[0].Details["by"] != "self" || linked[0].Details["provider"] != "Test IdP" {
		t.Fatalf("oidc.linked: %+v", linked)
	}
	// A second link is refused before leaving Ledger.
	if w := e.req("POST", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 409 {
		t.Fatalf("second link: %d %s", w.Code, w.Body.String())
	}

	// Now the identity signs alice in; the session is an ordinary one.
	w = e.signInOIDC()
	if w.Code != 303 || location(w) != "/" {
		t.Fatalf("sign-in callback: %d %q", w.Code, location(w))
	}
	set := strings.Join(w.Header().Values("Set-Cookie"), "\n")
	for _, want := range []string{httpapi.SessionCookie + "=", "SameSite=Strict", "HttpOnly", "Secure", httpapi.OIDCCookie + "=; Path=/; Max-Age=0"} {
		if !strings.Contains(set, want) {
			t.Errorf("Set-Cookie lacks %q:\n%s", want, set)
		}
	}
	b := e.sessionFrom(w)
	r := e.req("GET", "/api/v1/auth/session", "", b, nil)
	if !strings.Contains(r.Body.String(), `"method":"oidc"`) || !strings.Contains(r.Body.String(), e.ids["alice"]) {
		t.Fatalf("session: %s", r.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/sign-out", "", b, nil); w.Code != 204 {
		t.Fatalf("sign-out of an OIDC session: %d", w.Code)
	}
	ok := e.auditActions(audit.SignInSucceeded)
	if last := ok[len(ok)-1]; last.Details["method"] != "oidc" || last.Actor.UserID != e.ids["alice"] {
		t.Fatalf("signin audit: %+v", last)
	}
	// Linking never disables the password.
	e.signIn("alice")
	// return_to is honoured when it is a same-origin path, and replaced otherwise.
	for q, want := range map[string]string{"?return_to=%2Fcommitments%3Fmonth%3D2026-11": "/commitments?month=2026-11",
		"?return_to=https%3A%2F%2Fevil.example.com%2F": "/", "?return_to=%2F%2Fevil.example.com": "/", "?return_to=%2Fapi%2Fv1%2Fme": "/"} {
		w, _ := e.callback(e.authorize(q))
		if location(w) != want {
			t.Errorf("return_to %s: %q, want %q", q, location(w), want)
		}
	}
	// The link records its last use.
	if l, err := e.parts.auth.OIDCLinkOf(context.Background(), e.ids["alice"]); err != nil || l.LastUsedAt == nil || l.Email != "owner@example.com" {
		t.Errorf("link %+v %v", l, err)
	}
}

func TestOIDCUnlinkedIdentityIsDenied(t *testing.T) {
	e := newOIDCEnv(t, nil)
	before := len(e.auditActions(audit.UserCreated))
	w := e.signInOIDC()
	if w.Code != 303 || location(w) != "/sign-in?oidc_error=not_linked" || cookieValue(w, httpapi.SessionCookie) != "" {
		t.Fatalf("unlinked: %d %q", w.Code, location(w))
	}
	failed := e.auditActions(audit.SignInFailed)
	if len(failed) != 1 || failed[0].Actor.Kind != audit.ActorAnonymous || failed[0].Details["reason"] != "identity_not_linked" || failed[0].Details["method"] != "oidc" {
		t.Fatalf("signin_failed: %+v", failed)
	}
	if len(e.auditActions(audit.UserCreated)) != before {
		t.Fatal("an account was created without provisioning")
	}
	// The same email address as an existing link does not help: identity is
	// the subject.
	alice, _ := e.signIn("alice")
	e.link(alice)
	e.op.Subject = "subject-other"
	if w := e.signInOIDC(); location(w) != "/sign-in?oidc_error=not_linked" {
		t.Fatalf("same email, other subject: %q", location(w))
	}
}

func TestOIDCProvisioning(t *testing.T) {
	e := newOIDCEnv(t, func(c *config.Auth) { c.OIDC.Provisioning.Enabled = true })
	w := e.signInOIDC()
	if w.Code != 303 || location(w) != "/" {
		t.Fatalf("provisioned sign-in: %d %q", w.Code, location(w))
	}
	b := e.sessionFrom(w)
	var me struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	r := e.req("GET", "/api/v1/me", "", b, nil)
	_ = json.Unmarshal(r.Body.Bytes(), &me)
	u, err := e.parts.users.Get(context.Background(), me.UserID)
	if err != nil || u.Username != "owner" || u.Role != authz.RoleReadOnly || u.DisplayName == nil || *u.DisplayName != "Test Owner" {
		t.Fatalf("provisioned user %+v %v (%s)", u, err, r.Body.String())
	}
	if ok, _ := e.parts.auth.HasPasswordSignIn(context.Background(), u.ID); ok {
		t.Error("a provisioned account has a password")
	}
	created := e.auditActions(audit.UserCreated)
	linked := e.auditActions(audit.OIDCLinked)
	if c := created[len(created)-1]; c.TargetID != u.ID || c.Actor.Kind != audit.ActorSystem || len(linked) != 1 || linked[0].Details["by"] != "provisioning" {
		t.Fatalf("audit: %+v %+v", c, linked)
	}
	// The next sign-in reuses the account.
	n := len(created)
	e.sessionFrom(e.signInOIDC())
	if len(e.auditActions(audit.UserCreated)) != n {
		t.Fatal("a second account was provisioned")
	}
	// A taken username gets a suffix; email is never used to match accounts.
	e.op.Subject, e.op.PreferredUsername, e.op.Email = "subject-0002", "alice", "alice@example.com"
	b = e.sessionFrom(e.signInOIDC())
	r = e.req("GET", "/api/v1/me", "", b, nil)
	_ = json.Unmarshal(r.Body.Bytes(), &me)
	if me.UserID == e.ids["alice"] {
		t.Fatal("matched an existing account by name")
	}
	if u, _ := e.parts.users.Get(context.Background(), me.UserID); u.Username != "alice-2" {
		t.Fatalf("username %q", u.Username)
	}
}

func TestOIDCProvisioningAsAdministrator(t *testing.T) {
	e := newOIDCEnv(t, func(c *config.Auth) {
		c.OIDC.Provisioning = config.AuthOIDCProvisioning{Enabled: true, Role: "administrator"}
	})
	e.op.PreferredUsername, e.op.Email, e.op.Name = "", "", ""
	b := e.sessionFrom(e.signInOIDC())
	r := e.req("GET", "/api/v1/me", "", b, nil)
	if !strings.Contains(r.Body.String(), `"role":"administrator"`) {
		t.Fatalf("me: %s", r.Body.String())
	}
	list, _ := e.parts.users.List(context.Background(), users.ListFilter{UsernamePrefix: "oidc-", Limit: 10})
	if len(list) != 1 || len(list[0].Username) != len("oidc-")+16 {
		t.Fatalf("derived username: %+v", list)
	}
}

func TestOIDCCallbackRejections(t *testing.T) {
	e := newOIDCEnv(t, nil)
	alice, _ := e.signIn("alice")
	e.link(alice)

	// A replayed callback finds no state.
	w, uri := e.callback(e.authorize(""))
	if location(w) != "/" {
		t.Fatalf("first use: %q", location(w))
	}
	if w := e.replay(uri, w.Result().Cookies()[0].Value); location(w) != "/sign-in?oidc_error=invalid_state" || cookieValue(w, httpapi.SessionCookie) != "" {
		t.Fatalf("replayed state: %q", location(w))
	}
	// Without the state cookie, or with another browser's, the state is refused.
	u, _ := e.authorize("")
	if w, _ := e.callback(u, ""); location(w) != "/sign-in?oidc_error=invalid_state" {
		t.Fatalf("no cookie: %q", location(w))
	}
	u, _ = e.authorize("")
	_, other := e.authorize("")
	if w, _ := e.callback(u, other); location(w) != "/sign-in?oidc_error=invalid_state" {
		t.Fatalf("other browser's cookie: %q", location(w))
	}
	// An expired request.
	u, c := e.authorize("")
	e.clock.advance(11 * time.Minute)
	if w, _ := e.callback(u, c); location(w) != "/sign-in?oidc_error=invalid_state" {
		t.Fatalf("expired request: %q", location(w))
	}

	failedBefore := len(e.auditActions(audit.SignInFailed))
	for name, setup := range map[string]func(){
		"bad signature":  func() { e.op.SignWith = oidctest.NewKey(t) },
		"wrong audience": func() { e.op.Mutate = func(c map[string]any) { c["aud"] = "another-client" } },
		"wrong nonce":    func() { e.op.Mutate = func(c map[string]any) { c["nonce"] = "forged" } },
		"expired token":  func() { e.op.Mutate = func(c map[string]any) { c["exp"] = e.clock.now().Add(-5 * time.Minute).Unix() } },
	} {
		e.op.SignWith, e.op.Mutate = nil, nil
		setup()
		w := e.signInOIDC()
		if location(w) != "/sign-in?oidc_error=invalid_response" || cookieValue(w, httpapi.SessionCookie) != "" {
			t.Errorf("%s: %d %q", name, w.Code, location(w))
		}
	}
	e.op.SignWith, e.op.Mutate = nil, nil
	failed := e.auditActions(audit.SignInFailed)
	if f := failed[len(failed)-1]; len(failed)-failedBefore != 4 || f.Details["reason"] != "invalid_credentials" || f.Actor.Kind != audit.ActorAnonymous {
		t.Fatalf("rejected responses audited: %d %+v", len(failed)-failedBefore, f)
	}
	if strings.Contains(e.log.String(), "eyJ") || strings.Contains(e.log.String(), oidctest.ClientSecret) {
		t.Fatal("a token or the client secret was logged")
	}
	// The user refuses at the provider.
	e.op.Deny = true
	if w := e.signInOIDC(); location(w) != "/sign-in?oidc_error=access_denied" {
		t.Fatalf("denied: %q", location(w))
	}
	e.op.Deny = false
	// A disabled account.
	bob, _ := e.signIn("bob")
	e.op.Subject = "subject-bob"
	e.link(bob)
	if _, err := e.parts.users.Update(context.Background(), audit.System, e.ids["bob"], 1, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if w := e.signInOIDC(); location(w) != "/sign-in?oidc_error=sign_in_failed" {
		t.Fatalf("disabled: %q", location(w))
	}
	if f := e.auditActions(audit.SignInFailed); f[len(f)-1].Details["reason"] != "account_disabled" || f[len(f)-1].Actor.UserID != e.ids["bob"] {
		t.Fatalf("disabled audit: %+v", f[len(f)-1])
	}
	// The provider is down: the sign-in page says so.
	e.op.Server.Close()
	e.clock.advance(2 * time.Hour) // past the discovery cache
	if w := e.req("GET", httpapi.OIDCAuthorizePath, "", nil, nil); w.Code != 303 || location(w) != "/sign-in?oidc_error=provider_unavailable" {
		t.Fatalf("provider down: %d %q", w.Code, location(w))
	}
}

func TestOIDCLinkConflicts(t *testing.T) {
	e := newOIDCEnv(t, nil)
	alice, _ := e.signIn("alice")
	e.link(alice)
	// The identity is alice's: bob cannot link it.
	bob, _ := e.signIn("bob")
	if w := e.link(bob); location(w) != "/?oidc_error=already_linked" {
		t.Fatalf("bob linking alice's identity: %q", location(w))
	}
	// A link started from a session that ended meanwhile is refused.
	dave2, _ := e.signIn("bob")
	u, c := e.start("/api/v1/auth/oidc/link", dave2)
	if w := e.req("POST", "/api/v1/auth/sign-out", "", dave2, nil); w.Code != 204 {
		t.Fatal(w.Code)
	}
	e.op.Subject = "subject-fresh"
	if w, _ := e.callback(u, c); location(w) != "/?oidc_error=session_ended" {
		t.Fatalf("ended session: %q", location(w))
	}
	// Linking needs recent authentication.
	e.clock.advance(11 * time.Minute)
	if w := e.req("POST", "/api/v1/auth/oidc/link", "", bob, nil); w.Code != 403 || errCode(w.Body.String()) != "reauthentication_required" {
		t.Fatalf("stale link: %d %s", w.Code, w.Body.String())
	}
	// A return path is kept for link outcomes.
	e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`"}`, bob, nil)
	w := e.req("POST", "/api/v1/auth/oidc/link", `{"return_to":"/settings/security"}`, bob, nil)
	var out struct {
		URL string `json:"authorization_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w, _ := e.callback(out.URL, cookieValue(w, httpapi.OIDCCookie)); location(w) != "/settings/security?oidc=linked" {
		t.Fatalf("return_to: %q", location(w))
	}
}

func TestOIDCUnlinkNeedsRecentAuthAndAnotherMethod(t *testing.T) {
	e := newOIDCEnv(t, nil)
	alice, _ := e.signIn("alice")
	e.link(alice)
	e.clock.advance(11 * time.Minute)
	if w := e.req("DELETE", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 403 || errCode(w.Body.String()) != "reauthentication_required" {
		t.Fatalf("stale unlink: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`"}`, alice, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := e.req("DELETE", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 204 {
		t.Fatalf("unlink: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("DELETE", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 404 {
		t.Fatalf("unlink again: %d", w.Code)
	}
	un := e.auditActions(audit.OIDCUnlinked)
	if len(un) != 1 || un[0].Details["by"] != "self" || un[0].Actor.UserID != e.ids["alice"] {
		t.Fatalf("oidc.unlinked: %+v", un)
	}
	if w := e.signInOIDC(); location(w) != "/sign-in?oidc_error=not_linked" {
		t.Fatalf("after unlinking: %q", location(w))
	}

	// An account without a password keeps its only sign-in method.
	ctx := context.Background()
	np := e.ids["nopass"]
	if err := authstore.New(e.db, e.al).CreateOIDCLink(ctx, auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN1", UserID: np, Issuer: e.op.Issuer, Subject: "subject-np", CreatedAt: e.clock.now()}); err != nil {
		t.Fatal(err)
	}
	e.op.Subject = "subject-np"
	b := e.sessionFrom(e.signInOIDC())
	if w := e.req("DELETE", "/api/v1/auth/oidc/link", "", b, nil); w.Code != 409 || !strings.Contains(w.Body.String(), "without a sign-in method") {
		t.Fatalf("last method: %d %s", w.Code, w.Body.String())
	}
}

func TestOIDCReauthentication(t *testing.T) {
	e := newOIDCEnv(t, nil)
	alice, _ := e.signIn("alice")
	e.link(alice)
	b := e.sessionFrom(e.signInOIDC())
	e.clock.advance(11 * time.Minute)
	if w := e.req("POST", "/api/v1/auth/tokens", `{"name":"x","grants":["users:read"]}`, b, nil); w.Code != 403 || errCode(w.Body.String()) != "reauthentication_required" {
		t.Fatalf("stale token creation: %d %s", w.Code, w.Body.String())
	}
	u, c := e.start("/api/v1/auth/oidc/reauthenticate", b)
	if q := e.op.LastAuthorize(); q != nil && q.Get("prompt") != "" {
		t.Logf("previous request %v", q)
	}
	w, _ := e.callback(u, c)
	if location(w) != "/?oidc=reauthenticated" {
		t.Fatalf("reauth callback: %q", location(w))
	}
	if q := e.op.LastAuthorize(); q.Get("prompt") != "login" || q.Get("max_age") != "0" {
		t.Errorf("reauth request %v", q)
	}
	if w := e.req("POST", "/api/v1/auth/tokens", `{"name":"x","grants":["users:read"]}`, b, nil); w.Code != 201 {
		t.Fatalf("token creation after OIDC re-authentication: %d %s", w.Code, w.Body.String())
	}
	if r := e.auditActions(audit.Reauthenticated); r[len(r)-1].Details["method"] != "oidc" {
		t.Fatalf("reauth audit: %+v", r[len(r)-1])
	}
	// Another identity at the provider does not re-authenticate alice.
	e.op.Subject = "subject-someone-else"
	u, c = e.start("/api/v1/auth/oidc/reauthenticate", b)
	if w, _ := e.callback(u, c); location(w) != "/?oidc_error=identity_mismatch" {
		t.Fatalf("mismatch: %q", location(w))
	}
	if f := e.auditActions(audit.ReauthenticationFailed); f[len(f)-1].Details["reason"] != "identity_mismatch" {
		t.Fatalf("mismatch audit: %+v", f[len(f)-1])
	}
	// A provider that does not authenticate again is refused.
	e.op.Subject = "subject-0001"
	e.op.Mutate = func(c map[string]any) { c["auth_time"] = e.clock.now().Add(-time.Hour).Unix() }
	u, c = e.start("/api/v1/auth/oidc/reauthenticate", b)
	if w, _ := e.callback(u, c); location(w) != "/?oidc_error=invalid_response" {
		t.Fatalf("stale auth_time: %q", location(w))
	}
	// Without a link there is nothing to re-authenticate with.
	bob, _ := e.signIn("bob")
	if w := e.req("POST", "/api/v1/auth/oidc/reauthenticate", "", bob, nil); w.Code != 409 {
		t.Fatalf("no link: %d", w.Code)
	}
}

func TestPasswordSignInDisabledRedirectsToOIDC(t *testing.T) {
	e := newOIDCEnv(t, func(c *config.Auth) { c.Password.Enabled = false })
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil)
	if w.Code != 403 || errCode(w.Body.String()) != "password_sign_in_disabled" {
		t.Fatalf("password sign-in: %d %s", w.Code, w.Body.String())
	}
	if n := e.hasher.verifies.Load(); n != 0 {
		t.Errorf("a password was checked: %d", n)
	}
	w = e.req("GET", "/sign-in", "", nil, nil)
	if w.Code != 303 || location(w) != httpapi.OIDCAuthorizePath {
		t.Fatalf("sign-in page: %d %q", w.Code, location(w))
	}
	if w := e.req("GET", "/sign-in?return_to=%2Fcommitments", "", nil, nil); location(w) != httpapi.OIDCAuthorizePath+"?return_to=%2Fcommitments" {
		t.Fatalf("sign-in page with return_to: %q", location(w))
	}
	// Showing the outcome of an attempt does not loop back to the provider.
	if w := e.req("GET", "/sign-in?oidc_error=not_linked", "", nil, nil); w.Code == 303 {
		t.Fatalf("error page redirected: %q", location(w))
	}
	var m struct {
		Password struct {
			Enabled bool `json:"enabled"`
		} `json:"password"`
		OIDC struct {
			Enabled      bool   `json:"enabled"`
			Name         string `json:"name"`
			AuthorizeURL string `json:"authorize_url"`
		} `json:"oidc"`
	}
	r := e.req("GET", "/api/v1/auth/methods", "", nil, nil)
	if json.Unmarshal(r.Body.Bytes(), &m) != nil || m.Password.Enabled || !m.OIDC.Enabled || m.OIDC.Name != "Test IdP" || m.OIDC.AuthorizeURL != httpapi.OIDCAuthorizePath {
		t.Fatalf("methods: %s", r.Body.String())
	}
	// A linked user signs in with OIDC; password re-authentication is refused,
	// OIDC re-authentication works.
	ctx := context.Background()
	if err := authstore.New(e.db, e.al).CreateOIDCLink(ctx, auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN2", UserID: e.ids["alice"], Issuer: e.op.Issuer, Subject: "subject-0001", CreatedAt: e.clock.now()}); err != nil {
		t.Fatal(err)
	}
	b := e.sessionFrom(e.signInOIDC())
	if w := e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`"}`, b, nil); w.Code != 403 || errCode(w.Body.String()) != "password_sign_in_disabled" {
		t.Fatalf("password reauth: %d %s", w.Code, w.Body.String())
	}
	// With passwords off, alice's password does not count as a remaining method.
	if w := e.req("DELETE", "/api/v1/auth/oidc/link", "", b, nil); w.Code != 409 {
		t.Fatalf("unlink with passwords disabled: %d %s", w.Code, w.Body.String())
	}
}

func TestSignInPageWithPasswordsEnabled(t *testing.T) {
	e := newOIDCEnv(t, nil)
	if w := e.req("GET", "/sign-in", "", nil, nil); w.Code == 303 {
		t.Fatalf("sign-in page redirected with passwords enabled: %q", location(w))
	}
	r := e.req("GET", "/api/v1/auth/methods", "", nil, nil)
	if !strings.Contains(r.Body.String(), `"password":{"enabled":true}`) || !strings.Contains(r.Body.String(), `"enabled":true,"name":"Test IdP"`) {
		t.Fatalf("methods: %s", r.Body.String())
	}
}

func TestOIDCNotConfigured(t *testing.T) {
	e := newSignInEnv(t, nil)
	r := e.req("GET", "/api/v1/auth/methods", "", nil, nil)
	if !strings.Contains(r.Body.String(), `"oidc":{"enabled":false,"name":null,"authorize_url":null}`) {
		t.Fatalf("methods: %s", r.Body.String())
	}
	if w := e.req("GET", httpapi.OIDCAuthorizePath, "", nil, nil); location(w) != "/sign-in?oidc_error=not_configured" {
		t.Fatalf("authorize: %d %q", w.Code, location(w))
	}
	if w := e.req("GET", "/api/v1/auth/oidc/callback?state=x&code=y", "", nil, nil); location(w) != "/sign-in?oidc_error=not_configured" {
		t.Fatalf("callback: %q", location(w))
	}
	alice, _ := e.signIn("alice")
	if w := e.req("POST", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 404 {
		t.Fatalf("link: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("link status: %d %s", w.Code, w.Body.String())
	}
}

const secondFactorPrefix = "/sign-in?step=second_factor#second_factor="

// With passwords off, the page the callback sends a user with a second factor
// to must be served, not bounced back to the provider.
func TestOIDCSecondFactorPageWithPasswordsOff(t *testing.T) {
	e := newOIDCEnv(t, func(c *config.Auth) { c.Password.Enabled = false; c.Require2FA = true; c.OIDC.Require2FA = true })
	if err := authstore.New(e.db, e.al).CreateOIDCLink(context.Background(), auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN5", UserID: e.ids["alice"],
		Issuer: e.op.Issuer, Subject: "subject-0001", CreatedAt: e.clock.now()}); err != nil {
		t.Fatal(err)
	}
	e.enrol(e.sessionFrom(e.signInOIDC()))
	w := e.signInOIDC()
	loc := location(w)
	if w.Code != 303 || !strings.HasPrefix(loc, secondFactorPrefix) {
		t.Fatalf("callback: %d %q", w.Code, loc)
	}
	// The browser requests the path without the fragment.
	path := strings.SplitN(loc, "#", 2)[0]
	if p := e.req("GET", path, "", nil, nil); p.Code == 303 || p.Header().Get("Location") != "" {
		t.Fatalf("second-factor page redirected: %d %q", p.Code, location(p))
	}
	if p := e.req("GET", "/sign-in", "", nil, nil); p.Code != 303 {
		t.Fatalf("bare sign-in page: %d", p.Code)
	}
}

// The page the user was heading for survives the second factor.
func TestOIDCSecondFactorKeepsReturnTo(t *testing.T) {
	e := newOIDCEnv(t, func(c *config.Auth) { c.Require2FA = true; c.OIDC.Require2FA = true })
	if err := authstore.New(e.db, e.al).CreateOIDCLink(context.Background(), auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN6", UserID: e.ids["alice"],
		Issuer: e.op.Issuer, Subject: "subject-0001", CreatedAt: e.clock.now()}); err != nil {
		t.Fatal(err)
	}
	e.enrol(e.sessionFrom(e.signInOIDC()))
	w, _ := e.callback(e.authorize("?return_to=%2Faccount%2Ftokens%3Fx%3D1"))
	want := "/sign-in?step=second_factor&redirect=%2Faccount%2Ftokens%3Fx%3D1#second_factor="
	if loc := location(w); w.Code != 303 || !strings.HasPrefix(loc, want) {
		t.Fatalf("callback: %d %q, want prefix %q", w.Code, loc, want)
	}
}

func TestOIDCSessionsAndRequiredTwoFactor(t *testing.T) {
	// By default Ledger trusts the provider: auth.require_2fa does not
	// confine an OIDC session (decision D16).
	e := newOIDCEnv(t, func(c *config.Auth) { c.Require2FA = true })
	alice, _ := e.signIn("alice")
	if w := e.req("GET", "/api/v1/me", "", alice, nil); w.Code != 403 {
		t.Fatalf("password session not confined: %d", w.Code)
	}
	// A confined session cannot start a link (it is not an enrolment route).
	if w := e.req("POST", "/api/v1/auth/oidc/link", "", alice, nil); w.Code != 403 {
		t.Fatalf("confined link: %d", w.Code)
	}
	linkAlice := func(e *oidcEnv) {
		if err := authstore.New(e.db, e.al).CreateOIDCLink(context.Background(), auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN4", UserID: e.ids["alice"],
			Issuer: e.op.Issuer, Subject: "subject-0001", CreatedAt: e.clock.now()}); err != nil {
			t.Fatal(err)
		}
	}
	linkAlice(e)
	b := e.sessionFrom(e.signInOIDC())
	if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 200 {
		t.Fatalf("trusted OIDC session confined: %d %s", w.Code, w.Body.String())
	}
	if r := e.req("GET", "/api/v1/auth/session", "", b, nil); !strings.Contains(r.Body.String(), `"enrollment_required":false`) {
		t.Fatalf("session: %s", r.Body.String())
	}

	// With auth.oidc.require_2fa, OIDC sessions are confined like any other
	// and users with TOTP give their code after the provider.
	e = newOIDCEnv(t, func(c *config.Auth) { c.Require2FA = true; c.OIDC.Require2FA = true })
	linkAlice(e)
	b = e.sessionFrom(e.signInOIDC())
	if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 403 || errCode(w.Body.String()) != "two_factor_enrollment_required" {
		t.Fatalf("OIDC session not confined: %d %s", w.Code, w.Body.String())
	}
	en := e.enrol(b)
	w := e.signInOIDC()
	loc := location(w)
	if w.Code != 303 || !strings.HasPrefix(loc, secondFactorPrefix) || cookieValue(w, httpapi.SessionCookie) != "" {
		t.Fatalf("second factor after OIDC: %d %q", w.Code, loc)
	}
	challenge, _ := url.QueryUnescape(strings.TrimPrefix(loc, secondFactorPrefix))
	e.clock.advance(31 * time.Second)
	res := e.secondStep(challenge, "code", auth.TOTPCode(en.secret, e.clock.now()))
	if res.code != 200 || res.b == nil {
		t.Fatalf("second step: %d %s", res.code, res.body)
	}
	if r := e.req("GET", "/api/v1/auth/session", "", res.b, nil); !strings.Contains(r.Body.String(), `"method":"oidc"`) {
		t.Fatalf("session after the second step: %s", r.Body.String())
	}
}

func link2Token(t *testing.T, line string) string {
	u, err := url.Parse(line)
	if err != nil || u.Query().Get("token") == "" {
		t.Fatalf("link %q", line)
	}
	return u.Query().Get("token")
}

// A sign-in is only as recent as the provider's own authentication.
func TestOIDCSignInRecencyFollowsAuthTime(t *testing.T) {
	create := `{"name":"x","grants":["users:read"]}`
	for name, c := range map[string]struct {
		mutate func(e *oidcEnv) func(map[string]any)
		recent bool
	}{
		"fresh": {func(*oidcEnv) func(map[string]any) { return nil }, true},
		"stale": {func(e *oidcEnv) func(map[string]any) {
			return func(c map[string]any) { c["auth_time"] = e.clock.now().Add(-30 * 24 * time.Hour).Unix() }
		}, false},
		"missing": {func(*oidcEnv) func(map[string]any) { return func(c map[string]any) { delete(c, "auth_time") } }, false},
		// A claim far in the future is clamped to now (not kept as a window that
		// would only open later).
		"far future": {func(e *oidcEnv) func(map[string]any) {
			return func(c map[string]any) { c["auth_time"] = e.clock.now().Add(time.Hour).Unix() }
		}, true},
		"future": {func(e *oidcEnv) func(map[string]any) {
			return func(c map[string]any) { c["auth_time"] = e.clock.now().Add(30 * time.Second).Unix() }
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newOIDCEnv(t, nil)
			alice, _ := e.signIn("alice")
			e.link(alice)
			e.op.Mutate = c.mutate(e)
			b := e.sessionFrom(e.signInOIDC())
			w := e.req("POST", "/api/v1/auth/tokens", create, b, nil)
			if c.recent && w.Code != 201 {
				t.Fatalf("recent sign-in: %d %s", w.Code, w.Body.String())
			}
			if !c.recent && (w.Code != 403 || errCode(w.Body.String()) != "reauthentication_required") {
				t.Fatalf("stale sign-in: %d %s", w.Code, w.Body.String())
			}
			if !c.recent {
				if r := e.req("GET", "/api/v1/auth/session", "", b, nil); !strings.Contains(r.Body.String(), `"recent_auth_until":null`) {
					t.Fatalf("session: %s", r.Body.String())
				}
				// Re-authenticating at the provider restores it.
				e.op.Mutate = nil
				if w, _ := e.callback(e.start("/api/v1/auth/oidc/reauthenticate", b)); location(w) != "/?oidc=reauthenticated" {
					t.Fatalf("reauth: %q", location(w))
				}
				if w := e.req("POST", "/api/v1/auth/tokens", create, b, nil); w.Code != 201 {
					t.Fatalf("after re-authentication: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

// Anonymous sign-in starts are limited per address, so a flood cannot push a
// pending sign-in out of the transaction store.
func TestOIDCAuthorizeIsRateLimitedPerAddress(t *testing.T) {
	e := newOIDCEnv(t, nil)
	pending, cookie := e.authorize("")
	for range httpapi.DefaultAuthorizeLimit.Max {
		if w := e.req("GET", httpapi.OIDCAuthorizePath, "", nil, nil); w.Code != 303 {
			t.Fatalf("within the limit: %d", w.Code)
		}
	}
	w := e.req("GET", httpapi.OIDCAuthorizePath, "", nil, nil)
	if location(w) != "/sign-in?oidc_error=rate_limited" || w.Header().Get("Retry-After") == "" || cookieValue(w, httpapi.OIDCCookie) != "" {
		t.Fatalf("over the limit: %d %q", w.Code, location(w))
	}
	// Another address is not affected, and the window passing frees the first.
	if w := e.req("GET", httpapi.OIDCAuthorizePath, "", &browser{addr: "192.0.2.77:4000"}, nil); w.Code != 303 || strings.Contains(location(w), "oidc_error") {
		t.Fatalf("other address: %d %q", w.Code, location(w))
	}
	e.clock.advance(httpapi.DefaultAuthorizeLimit.Window + time.Second)
	if w := e.req("GET", httpapi.OIDCAuthorizePath, "", nil, nil); strings.Contains(location(w), "oidc_error") {
		t.Fatalf("after the window: %q", location(w))
	}
	// The sign-in that was pending before the flood still completes.
	if w, _ := e.callback(pending, cookie); cookieValue(w, httpapi.SessionCookie) == "" && !strings.Contains(location(w), "not_linked") {
		t.Fatalf("pending sign-in: %q", location(w))
	}
}

// One IPv6 host owns a whole /64, so its starts are counted together.
func TestOIDCAuthorizeIsRateLimitedPerIPv6Prefix(t *testing.T) {
	e := newOIDCEnv(t, nil)
	limited := 0
	for i := range 200 {
		b := &browser{addr: fmt.Sprintf("[2001:db8:0:0:%x::1]:4000", i+1)}
		if w := e.req("GET", httpapi.OIDCAuthorizePath, "", b, nil); strings.Contains(location(w), "oidc_error=rate_limited") {
			limited++
		}
	}
	if want := 200 - httpapi.DefaultAuthorizeLimit.Max; limited != want {
		t.Fatalf("%d of 200 starts from one /64 were limited, want %d", limited, want)
	}
	// Another /64 is not affected.
	if w := e.req("GET", httpapi.OIDCAuthorizePath, "", &browser{addr: "[2001:db8:0:1::1]:4000"}, nil); strings.Contains(location(w), "oidc_error") {
		t.Fatalf("other prefix: %q", location(w))
	}
}

// The provider's auth_time survives the second-factor step: a stale one stays
// stale, a fresh one is recent.
func TestOIDCAuthTimeSurvivesSecondFactor(t *testing.T) {
	for name, c := range map[string]struct {
		mutate func(e *oidcEnv) func(map[string]any)
		recent bool
	}{
		"fresh": {func(*oidcEnv) func(map[string]any) { return nil }, true},
		"stale": {func(e *oidcEnv) func(map[string]any) {
			return func(c map[string]any) { c["auth_time"] = e.clock.now().Add(-30 * 24 * time.Hour).Unix() }
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newOIDCEnv(t, func(c *config.Auth) { c.Require2FA = true; c.OIDC.Require2FA = true })
			if err := authstore.New(e.db, e.al).CreateOIDCLink(context.Background(), auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN4", UserID: e.ids["alice"],
				Issuer: e.op.Issuer, Subject: "subject-0001", CreatedAt: e.clock.now()}); err != nil {
				t.Fatal(err)
			}
			en := e.enrol(e.sessionFrom(e.signInOIDC()))
			e.op.Mutate = c.mutate(e)
			w := e.signInOIDC()
			challenge, _ := url.QueryUnescape(strings.TrimPrefix(location(w), secondFactorPrefix))
			e.clock.advance(31 * time.Second)
			res := e.secondStep(challenge, "code", auth.TOTPCode(en.secret, e.clock.now()))
			if res.code != 200 || res.b == nil {
				t.Fatalf("second step: %d %s", res.code, res.body)
			}
			tw := e.req("POST", "/api/v1/auth/tokens", `{"name":"x","grants":["users:read"]}`, res.b, nil)
			if c.recent && tw.Code != 201 {
				t.Fatalf("recent: %d %s", tw.Code, tw.Body.String())
			}
			if !c.recent && (tw.Code != 403 || errCode(tw.Body.String()) != "reauthentication_required") {
				t.Fatalf("stale: %d %s", tw.Code, tw.Body.String())
			}
		})
	}
}

func TestRecoverySignInAndUnlinkCommands(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ledger.db")
	clk := &clock{t: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
	vars := map[string]string{"LEDGER_DATABASE_PATH": dbPath, "LEDGER_KEK_VALUE": testKEK}
	env := opsEnv{Now: clk.now, Options: config.Options{
		LookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		Environ:   func() []string { return nil },
	}}
	run := func(stdin string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		e := env
		e.Stdin = strings.NewReader(stdin)
		code := runAuth(args, &out, &errb, e)
		return code, out.String(), errb.String()
	}
	if code, _, stderr := run(testPassword+"\n", "set-password", "--create", "administrator", "owner"); code != 0 {
		t.Fatalf("set-password: %s", stderr)
	}
	for _, args := range [][]string{{"recovery-sign-in"}, {"recovery-sign-in", "a", "b"}, {"unlink-oidc"}} {
		if code, stdout, _ := run("", args...); code != 2 || stdout != "" {
			t.Errorf("%q: exit %d %q", args, code, stdout)
		}
	}
	if code, _, stderr := run("", "recovery-sign-in", "nobody"); code != 1 || !strings.Contains(stderr, "no user") {
		t.Fatalf("unknown user: %d %s", code, stderr)
	}
	if code, _, stderr := run("", "recovery-sign-in", "--ttl", "2h", "owner"); code != 1 || !strings.Contains(stderr, "at most") {
		t.Fatalf("long ttl: %d %s", code, stderr)
	}
	// Without a redirect URL the address is a placeholder; with one it is used.
	_, stdout, _ := run("", "recovery-sign-in", "owner")
	if !strings.Contains(stdout, "<Ledger's address>/api/v1/auth/recovery-sign-in?token=") {
		t.Fatalf("stdout: %q", stdout)
	}
	vars["LEDGER_AUTH_OIDC_REDIRECT_URL"] = oidcRedirect
	code, stdout, stderr := run("", "recovery-sign-in", "--ttl", "10m", "owner")
	if code != 0 || !strings.Contains(stdout, "valid until 2026-11-21T09:10:00Z") {
		t.Fatalf("recovery-sign-in: %d %q %s", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	link, err := url.Parse(lines[len(lines)-1])
	if err != nil || link.Host != "ledger.example.com" || link.Path != "/api/v1/auth/recovery-sign-in" {
		t.Fatalf("link %q", lines[len(lines)-1])
	}
	token := link.Query().Get("token")

	// Serve the database: the link signs in once, whatever the password setting.
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if dump := dumpDatabase(t, db); strings.Contains(dump, token) {
		t.Fatal("the recovery token is stored in clear")
	}
	al := store.NewAuditLog(db)
	cfg := testAuthConfig()
	parts, err := buildAPI(db, al, cfg, testKEK, auth.NewArgon2id(cheapParams, 4), clk.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	logBuf := &bytes.Buffer{}
	log, _ := logging.New(logBuf, "debug", "json", redact.New())
	srv := httpapi.New(config.HTTP{MaxBodyBytes: 1 << 16, ShutdownTimeout: time.Second}, log,
		httpapi.WithAuthenticators(httpapi.NewSessionAuth(parts.auth)), httpapi.WithRecentAuth(parts.auth.Recent))
	registerAPI(srv, al, parts, calendar.MustLoadZone("UTC"), clk.now)
	get := func(uri string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", uri, nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	post := func(token string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", link.Path, strings.NewReader(url.Values{"token": {token}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	clk.advance(time.Minute)
	// A GET (a link previewer) only shows a confirmation page: it neither
	// starts a session nor consumes the token, however often it is fetched.
	for range 2 {
		pg := get(link.RequestURI())
		if pg.Code != 200 || cookieValue(pg, httpapi.SessionCookie) != "" || !strings.Contains(pg.Body.String(), `name="token" value="`+token+`"`) ||
			!strings.Contains(pg.Header().Get("Content-Type"), "text/html") || pg.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("confirmation page: %d %v %s", pg.Code, pg.Header(), pg.Body.String())
		}
		// The page holds a live token: it must not be framed or leak it. The
		// referrer policy is same-origin (not no-referrer) so that browsers
		// without Sec-Fetch-Site still send a real Origin on the POST.
		h := pg.Header()
		if h.Get("Referrer-Policy") != "same-origin" || h.Get("X-Frame-Options") != "DENY" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") ||
			!strings.Contains(h.Get("Content-Security-Policy"), "form-action 'self'") ||
			!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("confirmation page headers: %v", h)
		}
	}
	// A cross-origin POST is refused and leaves the token usable.
	if x := post(token, map[string]string{"Sec-Fetch-Site": "cross-site"}); x.Code != 403 || cookieValue(x, httpapi.SessionCookie) != "" {
		t.Fatalf("cross-origin: %d %s", x.Code, x.Body.String())
	}
	w := post(token, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if w.Code != 303 || location(w) != "/" || cookieValue(w, httpapi.SessionCookie) == "" {
		t.Fatalf("recovery sign-in: %d %q", w.Code, location(w))
	}
	if strings.Contains(logBuf.String(), token) {
		t.Fatal("the recovery token was logged")
	}
	sess := &browser{cookie: cookieValue(w, httpapi.SessionCookie)}
	r := httptest.NewRequest("GET", "/api/v1/auth/session", nil)
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: sess.cookie})
	rw := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rw, r)
	if !strings.Contains(rw.Body.String(), `"method":"recovery"`) {
		t.Fatalf("session: %s", rw.Body.String())
	}
	if w := post(token, nil); location(w) != "/sign-in?recovery_error=invalid_or_expired" {
		t.Fatalf("second use: %q", location(w))
	}
	// An expired link.
	_, stdout, _ = run("", "recovery-sign-in", "--ttl", "1m", "owner")
	lines = strings.Split(strings.TrimSpace(stdout), "\n")
	clk.advance(2 * time.Minute)
	if w := post(link2Token(t, lines[len(lines)-1]), nil); location(w) != "/sign-in?recovery_error=invalid_or_expired" {
		t.Fatalf("expired: %q", location(w))
	}
	recs, _ := al.List(ctx, audit.Filter{Limit: 100})
	var got []string
	for _, x := range recs {
		if strings.HasPrefix(string(x.Action), "auth.recovery") || x.Details["method"] == "recovery_sign_in" {
			got = append(got, string(x.Action)+"/"+string(x.Actor.Kind)+"/"+x.Details["by"]+x.Details["reason"])
		}
	}
	want := "auth.recovery_sign_in_issued/system/host auth.recovery_sign_in_issued/system/host auth.signin_succeeded/user/ auth.signin_failed/anonymous/invalid_credentials auth.recovery_sign_in_issued/system/host auth.signin_failed/user/invalid_credentials"
	if strings.Join(got, " ") != want {
		t.Fatalf("audit\n got %s\nwant %s", strings.Join(got, " "), want)
	}

	// unlink-oidc removes a link from the host.
	ownerID := ""
	if c, err := authstore.New(db, al).CredentialByUsername(ctx, "owner"); err == nil {
		ownerID = c.UserID
	}
	if err := authstore.New(db, al).CreateOIDCLink(ctx, auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN3", UserID: ownerID, Issuer: "https://id.example.com", Subject: "s", CreatedAt: clk.now()}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if code, stdout, stderr := run("", "unlink-oidc", "owner"); code != 0 || !strings.Contains(stdout, "link removed for owner") {
		t.Fatalf("unlink-oidc: %d %q %s", code, stdout, stderr)
	}
	if code, stdout, _ := run("", "unlink-oidc", "owner"); code != 0 || !strings.Contains(stdout, "nothing to remove") {
		t.Fatalf("unlink-oidc again: %d %q", code, stdout)
	}
	db2, err := store.Open(ctx, dbPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	un, _ := store.NewAuditLog(db2).List(ctx, audit.Filter{Actions: []audit.Action{audit.OIDCUnlinked}, Limit: 10})
	if len(un) != 1 || un[0].Details["by"] != "host" || un[0].Actor.Kind != audit.ActorSystem {
		t.Fatalf("oidc.unlinked: %+v", un)
	}
}
