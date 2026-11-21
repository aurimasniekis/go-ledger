package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
)

// Device cookies (decision D23-1b, ADR-0026 amendment): a browser that
// signed in before is exempt from the per-username lockout.

// knownBrowser signs user in from addr and returns the browser with its
// session and device cookies.
func (e *signInEnv) knownBrowser(user, addr string) *browser {
	e.t.Helper()
	b := &browser{addr: addr}
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody(user, testPassword), b, nil)
	if w.Code != 200 {
		e.t.Fatalf("sign in %s: %d %s", user, w.Code, w.Body.String())
	}
	var cs currentSession
	_ = json.Unmarshal(w.Body.Bytes(), &cs)
	b.cookie, b.csrf, b.device = cookieValue(w, httpapi.SessionCookie), cs.CSRFToken, cookieValue(w, httpapi.DeviceCookie)
	if b.cookie == "" || b.device == "" {
		e.t.Fatalf("sign in %s: session cookie %t, device cookie %t", user, b.cookie != "", b.device != "")
	}
	return b
}

// lockOut fails n sign-ins for user, each from a new address, so only the
// username's limit fills up, and checks an unknown browser is then refused.
func (e *signInEnv) lockOut(user string, n int) {
	e.t.Helper()
	for i := range n {
		b := &browser{addr: fmt.Sprintf("198.51.100.%d:1", 100+i)}
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody(user, "wrong password!"), b, nil); w.Code != 401 {
			e.t.Fatalf("failure %d for %s: %d %s", i, user, w.Code, w.Body.String())
		}
	}
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody(user, testPassword), &browser{addr: "198.51.100.250:1"}, nil); w.Code != 429 {
		e.t.Fatalf("%s is not locked out for an unknown browser: %d %s", user, w.Code, w.Body.String())
	}
}

func (e *signInEnv) countDeviceCookies(userID string) int {
	e.t.Helper()
	var n int
	if err := e.db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM device_cookies WHERE user_id = ?", userID).Scan(&n)
	}); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestDeviceCookieIsASecureHostCookieStoredAsAHash(t *testing.T) {
	e := newSignInEnv(t, nil)
	b := e.knownBrowser("alice", "203.0.113.5:1")
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var set string
	for _, v := range w.Header().Values("Set-Cookie") {
		if strings.HasPrefix(v, httpapi.DeviceCookie+"=") {
			set = v
		}
	}
	for _, want := range []string{"Path=/", "HttpOnly", "Secure", "SameSite=Strict", "Max-Age=7776000", "Expires="} {
		if !strings.Contains(set, want) {
			t.Errorf("Set-Cookie %q lacks %q", set, want)
		}
	}
	if strings.Contains(strings.ToLower(set), "domain=") {
		t.Errorf("Set-Cookie %q has a Domain", set)
	}
	tok := cookieValue(w, httpapi.DeviceCookie)
	if strings.Contains(w.Body.String(), tok) {
		t.Error("the device token is in the response body")
	}
	if n := e.countDeviceCookies(e.ids["alice"]); n != 2 {
		t.Errorf("%d device cookies stored, want 2 (one per browser)", n)
	}
	var clear int
	if err := e.db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM device_cookies WHERE token_hash IN (?, ?)", tok, b.device).Scan(&clear)
	}); err != nil || clear != 0 {
		t.Errorf("device tokens stored in clear: %d %v", clear, err)
	}
	// Signing out keeps the browser known.
	if w := e.req("POST", "/api/v1/auth/sign-out", "", b, nil); w.Code != 204 || strings.Contains(strings.Join(w.Header().Values("Set-Cookie"), ";"), httpapi.DeviceCookie) {
		t.Errorf("sign-out: %d %v", w.Code, w.Header().Values("Set-Cookie"))
	}
	if n := e.countDeviceCookies(e.ids["alice"]); n != 2 {
		t.Errorf("%d device cookies after sign-out, want 2", n)
	}
}

func TestDeviceCookieExemptsAKnownBrowserFromTheUsernameLockout(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AccountMaxFailures = 3 })
	known := e.knownBrowser("alice", "203.0.113.5:1")
	first := known.device

	// Someone else locks the username out: an unknown browser gets 429 even
	// with the right password.
	e.lockOut("alice", 3)

	// The known browser still signs in, and its device cookie is rotated.
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), known, nil)
	if w.Code != 200 {
		t.Fatalf("known browser while the username is locked: %d %s", w.Code, w.Body.String())
	}
	known.device = cookieValue(w, httpapi.DeviceCookie)
	if known.device == "" || known.device == first {
		t.Fatalf("device cookie not rotated: %q", known.device)
	}
	if n := e.countDeviceCookies(e.ids["alice"]); n != 1 {
		t.Errorf("%d device cookies after rotation, want 1 (the old one deleted)", n)
	}
	// The replaced cookie gets no exemption.
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "203.0.113.6:1", device: first}, nil); w.Code != 429 {
		t.Errorf("rotated-out cookie: %d %s", w.Code, w.Body.String())
	}
	// The cookie never replaces the password.
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", "wrong password!"), known, nil); w.Code != 401 {
		t.Errorf("known browser with a wrong password: %d %s", w.Code, w.Body.String())
	}
	// The username stays locked for unknown browsers.
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "203.0.113.7:1"}, nil); w.Code != 429 {
		t.Errorf("unknown browser after the known one signed in: %d", w.Code)
	}

	// Audit: the known browser's sign-in and failure are marked.
	var okKnown, failKnown int
	for _, r := range e.auditActions(audit.SignInSucceeded) {
		if r.Details["device"] == "known" {
			okKnown++
		}
	}
	for _, r := range e.auditActions(audit.SignInFailed) {
		if r.Details["device"] == "known" {
			failKnown++
		}
	}
	if okKnown != 1 || failKnown != 1 {
		t.Errorf("audit entries with device=known: %d succeeded, %d failed; want 1 and 1", okKnown, failKnown)
	}
}

func TestDeviceCookieFailuresCountAgainstTheCookie(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AccountMaxFailures = 3 })
	known := e.knownBrowser("alice", "203.0.113.5:1")
	for i := range 3 {
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", "wrong password!"), known, nil); w.Code != 401 {
			t.Fatalf("failure %d: %d", i, w.Code)
		}
	}
	before := e.hasher.verifies.Load()
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), known, nil); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("known browser after its own failures: %d %s", w.Code, w.Body.String())
	}
	if e.hasher.verifies.Load() != before {
		t.Error("a blocked known browser still hashed")
	}
	// Its failures did not count against the username.
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "203.0.113.9:1"}, nil); w.Code != 200 {
		t.Errorf("unknown browser: %d %s", w.Code, w.Body.String())
	}
}

func TestDeviceCookieGivesNoExemptionWhenInvalid(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AccountMaxFailures = 2 })
	ctx := context.Background()
	revoked := e.knownBrowser("alice", "203.0.113.5:1")
	valid := e.knownBrowser("alice", "203.0.113.6:1")
	bobs := e.knownBrowser("bob", "203.0.113.7:1")
	e.lockOut("alice", 2)

	try := func(name, device string, want int) {
		t.Helper()
		w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "203.0.113.20:1", device: device}, nil)
		if w.Code != want {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body.String(), want)
		}
	}
	try("forged cookie", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 429)
	try("over-long cookie", strings.Repeat("a", auth.MaxTokenLength+1), 429)
	try("another user's cookie", bobs.device, 429)
	try("a session token as the cookie", valid.cookie, 429)
	try("valid cookie", valid.device, 200)

	// Setting the password revokes every device cookie of the user.
	if _, err := e.parts.auth.SetPassword(ctx, authz.Principal{}, true, e.ids["alice"], testPassword, auth.ByHost); err != nil {
		t.Fatal(err)
	}
	if n := e.countDeviceCookies(e.ids["alice"]); n != 0 {
		t.Errorf("%d device cookies after the password change", n)
	}
	try("cookie from before the password change", revoked.device, 429)
	if n := e.countDeviceCookies(e.ids["bob"]); n != 1 {
		t.Errorf("bob's device cookies: %d, want 1", n)
	}

	// Expiry: valid until the lifetime ends, then no exemption.
	e.clock.advance(16 * time.Minute)
	exp := e.knownBrowser("alice", "203.0.113.8:1")
	e.clock.advance(auth.DefaultDeviceCookieTTL - time.Minute)
	e.lockOut("alice", 2)
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), exp, nil)
	if w.Code != 200 {
		t.Fatalf("cookie just before expiry: %d %s", w.Code, w.Body.String())
	}
	exp.device = cookieValue(w, httpapi.DeviceCookie)
	e.clock.advance(auth.DefaultDeviceCookieTTL)
	e.lockOut("alice", 2)
	try("expired cookie", exp.device, 429)
}

func TestDeviceCookieKeepsThePerAddressLimit(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AddressMaxFailures = 2 })
	known := e.knownBrowser("alice", "203.0.113.5:1")
	for _, u := range []string{"u1", "u2"} {
		e.req("POST", "/api/v1/auth/sign-in", signInBody(u, "wrong password!"), &browser{addr: "203.0.113.5:2"}, nil)
	}
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), known, nil); w.Code != 429 {
		t.Errorf("known browser from a blocked address: %d %s", w.Code, w.Body.String())
	}
}

func TestDeviceCookieNeverSkipsTheSecondFactor(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AccountMaxFailures = 2 })
	known := e.knownBrowser("alice", "203.0.113.5:1")
	en := e.enrol(known)
	e.lockOut("alice", 2)

	// The known browser gets past the username lock, but only to the
	// second step: no cookie yet.
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), known, nil)
	if w.Code != 202 || len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("password step from a known browser: %d %v %s", w.Code, w.Header().Values("Set-Cookie"), w.Body.String())
	}
	var ch struct {
		Challenge string `json:"challenge"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ch)
	second := func(code string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"challenge": ch.Challenge, "code": code})
		return e.req("POST", "/api/v1/auth/sign-in/second-factor", string(body), &browser{addr: known.addr}, nil)
	}
	e.clock.advance(time.Minute)
	if w := second("000000"); w.Code != 401 {
		t.Fatalf("wrong code: %d %s", w.Code, w.Body.String())
	}
	w = second(auth.TOTPCode(en.secret, e.clock.now()))
	if w.Code != 200 {
		t.Fatalf("second step: %d %s", w.Code, w.Body.String())
	}
	if d := cookieValue(w, httpapi.DeviceCookie); d == "" || d == known.device {
		t.Errorf("complete two-step sign-in did not rotate the device cookie: %q", d)
	}
	// The unknown browser is still locked out.
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "203.0.113.30:1"}, nil); w.Code != 429 {
		t.Errorf("unknown browser: %d", w.Code)
	}
}

func TestDeviceCookieExemptsReauthenticationFromTheUsernameLockout(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AccountMaxFailures = 2 })
	known := e.knownBrowser("alice", "203.0.113.5:1")
	other := e.knownBrowser("alice", "203.0.113.6:1")
	other.device = "" // a session in a browser without its device cookie
	e.lockOut("alice", 2)
	body := fmt.Sprintf(`{"password":%q}`, testPassword)
	if w := e.req("POST", "/api/v1/auth/reauthenticate", body, known, nil); w.Code != 200 {
		t.Errorf("re-authentication in a known browser: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/reauthenticate", body, other, nil); w.Code != 429 {
		t.Errorf("re-authentication without a device cookie: %d %s", w.Code, w.Body.String())
	}
}

func TestOwnPasswordChangeClearsTheDeviceCookie(t *testing.T) {
	e := newSignInEnv(t, nil)
	known := e.knownBrowser("alice", "203.0.113.5:1")
	w := e.req("PUT", "/api/v1/auth/password", `{"password":"another long password"}`, known, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	cleared := false
	for _, v := range w.Header().Values("Set-Cookie") {
		if strings.HasPrefix(v, httpapi.DeviceCookie+"=;") && strings.Contains(v, "Max-Age=0") {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("device cookie not cleared: %v", w.Header().Values("Set-Cookie"))
	}
	if n := e.countDeviceCookies(e.ids["alice"]); n != 0 {
		t.Errorf("%d device cookies left", n)
	}
}
