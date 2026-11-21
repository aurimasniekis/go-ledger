package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/aurimasniekis/go-ledger/internal/redact"
	"github.com/aurimasniekis/go-ledger/internal/store"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// countingHasher counts Hash and Verify calls, to show every failed sign-in
// does the same work whatever the reason. delay (nanoseconds) slows every
// Verify down, standing in for a real Argon2id cost in concurrency tests.
type countingHasher struct {
	auth.Hasher
	hashes   atomic.Int64
	verifies atomic.Int64
	delay    atomic.Int64
}

func (h *countingHasher) Hash(ctx context.Context, pw string) (string, error) {
	h.hashes.Add(1)
	return h.Hasher.Hash(ctx, pw)
}

func (h *countingHasher) Verify(ctx context.Context, encoded, pw string) (bool, bool, error) {
	h.verifies.Add(1)
	if d := time.Duration(h.delay.Load()); d > 0 {
		time.Sleep(d)
	}
	return h.Hasher.Verify(ctx, encoded, pw)
}

// signInEnv is ledger serve's API stack (bearer tokens, then session
// cookies, and the token-write audit recorder) with a test clock and a
// counting hasher.
type signInEnv struct {
	t      *testing.T
	srv    *httpapi.Server
	db     *store.DB
	al     *store.AuditLog
	parts  apiParts
	clock  *clock
	hasher *countingHasher
	log    *bytes.Buffer
	ids    map[string]string
}

func newSignInEnv(t *testing.T, mod func(*config.Auth)) *signInEnv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	buf := &bytes.Buffer{}
	log, err := logging.New(buf, "debug", "json", redact.New())
	if err != nil {
		t.Fatal(err)
	}
	e := &signInEnv{t: t, db: db, al: store.NewAuditLog(db), clock: &clock{t: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)},
		hasher: &countingHasher{Hasher: auth.NewArgon2id(cheapParams, 4)}, log: buf, ids: map[string]string{}}
	cfg := testAuthConfig()
	if mod != nil {
		mod(&cfg)
	}
	e.parts, err = buildAPI(db, e.al, cfg, e.hasher, e.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httpapi.New(config.HTTP{MaxBodyBytes: 1 << 16, ShutdownTimeout: time.Second}, log,
		httpapi.WithAuthenticators(httpapi.NewTokenAuth(e.parts.tokens), httpapi.NewSessionAuth(e.parts.auth)), httpapi.WithRecentAuth(e.parts.auth.Recent),
		httpapi.WithAuditRecorder(e.al, e.clock.now))
	registerAPI(e.srv, e.al, e.parts, calendar.MustLoadZone("UTC"), e.clock.now)
	for _, u := range []struct {
		name string
		role authz.Role
		pw   bool
	}{{"alice", authz.RoleAdministrator, true}, {"bob", authz.RoleReadOnly, true}, {"nopass", authz.RoleReadOnly, false}, {"dave", authz.RoleReadOnly, true}} {
		x, err := e.parts.users.Create(ctx, audit.System, users.New{Username: u.name, Role: u.role})
		if err != nil {
			t.Fatal(err)
		}
		e.ids[u.name] = x.ID
		if u.pw {
			if _, err := e.parts.auth.SetPassword(ctx, authz.Principal{}, true, x.ID, testPassword, auth.ByHost); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := e.parts.users.Update(ctx, audit.System, e.ids["dave"], 1, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	return e
}

// browser is a client holding one session cookie and its CSRF token.
type browser struct {
	cookie, csrf string
	addr         string
}

func (e *signInEnv) req(method, path, body string, b *browser, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if method == "PATCH" {
		r.Header.Set("Content-Type", httpapi.MergePatchType)
	}
	r.RemoteAddr = "192.0.2.10:4000"
	if b != nil {
		if b.addr != "" {
			r.RemoteAddr = b.addr
		}
		if b.cookie != "" {
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: b.cookie})
		}
		if b.csrf != "" && method != "GET" {
			r.Header.Set(httpapi.CSRFHeader, b.csrf)
		}
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, r)
	return w
}

func signInBody(user, pw string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	return string(b)
}

type currentSession struct {
	UserID  string `json:"user_id"`
	Session struct {
		ID        string    `json:"id"`
		Current   bool      `json:"current"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"session"`
	CSRFToken       string     `json:"csrf_token"`
	RecentAuthUntil *time.Time `json:"recent_auth_until"`
}

// signIn signs in and returns the browser, failing the test otherwise.
func (e *signInEnv) signIn(user string) (*browser, currentSession) {
	e.t.Helper()
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody(user, testPassword), nil, nil)
	if w.Code != 200 {
		e.t.Fatalf("sign in %s: %d %s", user, w.Code, w.Body.String())
	}
	var cs currentSession
	if err := json.Unmarshal(w.Body.Bytes(), &cs); err != nil {
		e.t.Fatal(err)
	}
	var tok string
	for _, c := range w.Result().Cookies() {
		if c.Name == httpapi.SessionCookie {
			tok = c.Value
		}
	}
	if tok == "" {
		e.t.Fatal("no session cookie")
	}
	return &browser{cookie: tok, csrf: cs.CSRFToken}, cs
}

func (e *signInEnv) auditActions(action audit.Action) []audit.Record {
	e.t.Helper()
	got, err := e.al.List(context.Background(), audit.Filter{Actions: []audit.Action{action}, Limit: 200})
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func TestSignInSetsASecureSessionCookie(t *testing.T) {
	e := newSignInEnv(t, nil)
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody("Alice", testPassword), nil, map[string]string{"User-Agent": "test-browser/1.0"})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	set := w.Header().Get("Set-Cookie")
	for _, want := range []string{httpapi.SessionCookie + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Strict", "Max-Age=43200", "Expires="} {
		if !strings.Contains(set, want) {
			t.Errorf("Set-Cookie %q lacks %q", set, want)
		}
	}
	// The __Host- prefix: browsers keep it only when Secure, Path=/ and
	// no Domain, so a sibling subdomain cannot set it.
	if !strings.HasPrefix(set, "__Host-ledger_session=") || strings.Contains(strings.ToLower(set), "domain=") {
		t.Errorf("Set-Cookie %q: want a __Host- cookie without Domain", set)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
	}
	var cs currentSession
	_ = json.Unmarshal(w.Body.Bytes(), &cs)
	if cs.UserID != e.ids["alice"] || !cs.Session.Current || cs.CSRFToken == "" || cs.RecentAuthUntil == nil ||
		!cs.Session.ExpiresAt.Equal(e.clock.now().Add(12*time.Hour)) {
		t.Errorf("body %s", w.Body.String())
	}
	tok := strings.TrimPrefix(strings.SplitN(set, ";", 2)[0], httpapi.SessionCookie+"=")
	if strings.Contains(w.Body.String(), tok) {
		t.Error("the session token is in the response body")
	}
	// The token is stored only as a hash.
	var n int
	if err := e.db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE token_hash = ? OR user_agent <> 'test-browser/1.0'", tok).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("stored session: n=%d err=%v", n, err)
	}
	// The cookie authenticates; GET /me shows the principal.
	b := &browser{cookie: tok, csrf: cs.CSRFToken}
	if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 200 || !strings.Contains(w.Body.String(), e.ids["alice"]) {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/auth/session", "", b, nil); w.Code != 200 || !strings.Contains(w.Body.String(), cs.CSRFToken) {
		t.Fatalf("session: %d %s", w.Code, w.Body.String())
	}
	// Success is audited with the user as actor and target.
	ok := e.auditActions(audit.SignInSucceeded)
	if len(ok) != 1 || ok[0].Actor.UserID != e.ids["alice"] || ok[0].TargetID != e.ids["alice"] || ok[0].Details["method"] != "password" {
		t.Errorf("audit %+v", ok)
	}
	if strings.Contains(e.log.String(), testPassword) || strings.Contains(e.log.String(), tok) {
		t.Error("a password or session token reached the log")
	}
}

var requestIDField = regexp.MustCompile(`"request_id":"[^"]*"`)

// TestSignInFailuresAreUniform: an unknown user, a wrong password, a user
// without a password and a disabled user (with the right password) all get
// byte-identical answers apart from the request ID, and each costs exactly
// one password-hash verification, so neither the answer nor the work tells
// them apart. The test counts work instead of timing it, so it does not
// depend on the machine.
func TestSignInFailuresAreUniform(t *testing.T) {
	e := newSignInEnv(t, nil)
	var first string
	var firstHdr http.Header
	for _, c := range []struct{ name, user, pw string }{
		{"unknown user", "mallory", testPassword},
		{"wrong password", "alice", "not the right password"},
		{"no password set", "nopass", testPassword},
		{"disabled user, right password", "dave", testPassword},
		{"invalid username", "NOT A USER!", testPassword},
		{"over-long password", "alice", strings.Repeat("x", auth.MaxPasswordBytes+1)},
	} {
		before, hashesBefore := e.hasher.verifies.Load(), e.hasher.hashes.Load()
		w := e.req("POST", "/api/v1/auth/sign-in", signInBody(c.user, c.pw), nil, nil)
		if n := e.hasher.verifies.Load() - before; n != 1 {
			t.Errorf("%s: %d hash verifications, want 1", c.name, n)
		}
		// No hash is made either: not the dummy hash (made at start, so the
		// first unknown username after a start costs no more), not a rehash.
		if n := e.hasher.hashes.Load() - hashesBefore; n != 0 {
			t.Errorf("%s: %d hashes made, want 0", c.name, n)
		}
		if w.Code != 401 {
			t.Errorf("%s: %d", c.name, w.Code)
		}
		body := requestIDField.ReplaceAllString(w.Body.String(), "")
		hdr := w.Header().Clone()
		hdr.Del("X-Request-Id")
		if first == "" {
			first, firstHdr = body, hdr
			if !strings.Contains(body, `"code":"invalid_credentials"`) || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("%s: %s", c.name, body)
			}
			continue
		}
		if body != first {
			t.Errorf("%s: body %s differs from %s", c.name, body, first)
		}
		if len(hdr) != len(firstHdr) {
			t.Errorf("%s: headers %v differ from %v", c.name, hdr, firstHdr)
		}
		for k := range firstHdr {
			if hdr.Get(k) != firstHdr.Get(k) {
				t.Errorf("%s: header %s %q, want %q", c.name, k, hdr.Get(k), firstHdr.Get(k))
			}
		}
	}
	// Failures are audited without the username: anonymous when no account
	// matched, the account otherwise, and a stage code as reason.
	failed := e.auditActions(audit.SignInFailed)
	if len(failed) != 6 {
		t.Fatalf("%d failure entries", len(failed))
	}
	kinds := []string{}
	for _, f := range failed {
		kinds = append(kinds, string(f.Actor.Kind)+"/"+f.Details["reason"])
		for _, v := range f.Details {
			if strings.Contains(v, "mallory") || strings.Contains(v, "alice") || strings.Contains(v, "password!") {
				t.Errorf("failure entry carries a username: %+v", f)
			}
		}
	}
	want := "anonymous/invalid_credentials user/invalid_credentials user/invalid_credentials user/account_disabled anonymous/invalid_credentials user/invalid_credentials"
	if strings.Join(kinds, " ") != want {
		t.Errorf("failure entries %s", strings.Join(kinds, " "))
	}
	if strings.Contains(e.log.String(), "mallory") {
		t.Error("an attempted username reached the log")
	}
}

func TestSignInRateLimits(t *testing.T) {
	t.Run("per account", func(t *testing.T) {
		e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AccountMaxFailures = 3 })
		for i := range 3 {
			// From different addresses: only the account limit applies.
			b := &browser{addr: "198.51.100." + string(rune('1'+i)) + ":1"}
			if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", "wrong password!"), b, nil); w.Code != 401 {
				t.Fatalf("attempt %d: %d", i, w.Code)
			}
		}
		before := e.hasher.verifies.Load()
		w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "203.0.113.9:1"}, nil)
		if w.Code != 429 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"code":"rate_limited"`) {
			t.Fatalf("blocked account with the right password: %d %s", w.Code, w.Body.String())
		}
		if e.hasher.verifies.Load() != before {
			t.Error("a blocked attempt still hashed")
		}
		// An unknown username is limited exactly like a known one.
		for range 3 {
			e.req("POST", "/api/v1/auth/sign-in", signInBody("ghost", "wrong password!"), nil, nil)
		}
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("ghost", "wrong password!"), nil, nil); w.Code != 429 {
			t.Errorf("unknown account not limited: %d", w.Code)
		}
		// Other accounts are unaffected; the block ends with the window.
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("bob", testPassword), &browser{addr: "203.0.113.10:1"}, nil); w.Code != 200 {
			t.Errorf("bob: %d", w.Code)
		}
		e.clock.advance(15 * time.Minute)
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil); w.Code != 200 {
			t.Errorf("after the window: %d %s", w.Code, w.Body.String())
		}
		// The block is audited once (locked_out), not per refused attempt.
		var locked int
		for _, f := range e.auditActions(audit.SignInFailed) {
			if f.Details["reason"] == "locked_out" {
				locked++
			}
		}
		if locked != 2 { // alice once, ghost once
			t.Errorf("locked_out entries: %d, want 2", locked)
		}
	})
	t.Run("per address", func(t *testing.T) {
		e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AddressMaxFailures = 3 })
		attacker := &browser{addr: "[2001:db8:1:2::5]:1"}
		for _, u := range []string{"u1", "u2", "u3"} {
			e.req("POST", "/api/v1/auth/sign-in", signInBody(u, "wrong password!"), attacker, nil)
		}
		// Another host in the same /64 is the same address for the limit.
		neighbour := &browser{addr: "[2001:db8:1:2::77]:1"}
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), neighbour, nil); w.Code != 429 {
			t.Fatalf("same /64: %d", w.Code)
		}
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "[2001:db8:1:3::5]:1"}, nil); w.Code != 200 {
			t.Fatalf("other address: %d %s", w.Code, w.Body.String())
		}
	})
}

// TestSignInLimitsHoldUnderConcurrency sends many wrong passwords at once
// while every verification is slow: the limits count attempts in flight, so
// no more than the limit are ever hashed (or audited), and the rest get 429.
func TestSignInLimitsHoldUnderConcurrency(t *testing.T) {
	burst := func(t *testing.T, e *signInEnv, n int, body func(i int) string, addr func(i int) string) (codes map[int]int) {
		t.Helper()
		e.hasher.delay.Store(int64(50 * time.Millisecond))
		var mu sync.Mutex
		codes = map[int]int{}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Go(func() {
				<-start
				w := e.req("POST", "/api/v1/auth/sign-in", body(i), &browser{addr: addr(i)}, nil)
				mu.Lock()
				codes[w.Code]++
				mu.Unlock()
			})
		}
		close(start)
		wg.Wait()
		e.hasher.delay.Store(0)
		return codes
	}
	failures := func(e *signInEnv) (failed, locked int) {
		for _, f := range e.auditActions(audit.SignInFailed) {
			if f.Details["reason"] == "locked_out" {
				locked++
			} else {
				failed++
			}
		}
		return failed, locked
	}
	t.Run("per account", func(t *testing.T) {
		e := newSignInEnv(t, nil) // 10 failures per account
		before := e.hasher.verifies.Load()
		codes := burst(t, e, 60, func(int) string { return signInBody("alice", "wrong password!") },
			func(i int) string { return fmt.Sprintf("198.51.100.%d:1", i+1) })
		if n := e.hasher.verifies.Load() - before; n != 10 {
			t.Errorf("%d hash verifications for 60 concurrent attempts, want 10", n)
		}
		if codes[401] != 10 || codes[429] != 50 {
			t.Errorf("status codes %v, want 10 x 401 and 50 x 429", codes)
		}
		if failed, locked := failures(e); failed != 10 || locked > 1 {
			t.Errorf("%d failure and %d locked_out audit entries, want 10 and at most 1", failed, locked)
		}
		// The burst counted: the account is now blocked, even with the right password.
		if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), &browser{addr: "203.0.113.9:1"}, nil); w.Code != 429 {
			t.Errorf("after the burst: %d", w.Code)
		}
	})
	t.Run("per address", func(t *testing.T) {
		e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AddressMaxFailures = 30 })
		before := e.hasher.verifies.Load()
		codes := burst(t, e, 100, func(i int) string { return signInBody(fmt.Sprintf("user%d", i), "wrong password!") },
			func(int) string { return "203.0.113.50:1" })
		if n := e.hasher.verifies.Load() - before; n != 30 {
			t.Errorf("%d hash verifications for 100 concurrent attempts from one address, want 30", n)
		}
		if codes[401] != 30 || codes[429] != 70 {
			t.Errorf("status codes %v", codes)
		}
	})
	t.Run("a success releases its reservation", func(t *testing.T) {
		e := newSignInEnv(t, func(c *config.Auth) { c.SignIn.AccountMaxFailures = 2 })
		codes := burst(t, e, 2, func(int) string { return signInBody("alice", testPassword) },
			func(i int) string { return fmt.Sprintf("198.51.100.%d:1", i+1) })
		if codes[200] != 2 {
			t.Fatalf("two concurrent right passwords within the limit: %v", codes)
		}
		// Neither success left a pending attempt behind.
		for i := range 2 {
			if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil); w.Code != 200 {
				t.Fatalf("sign-in %d after the burst: %d", i, w.Code)
			}
		}
	})
}

// TestRehashOnlyForAccountsThatMaySignIn: a hash with old parameters is
// replaced after a successful sign-in, but a disabled account with the right
// password costs no extra hash (which would reveal through timing that the
// password was right).
func TestRehashOnlyForAccountsThatMaySignIn(t *testing.T) {
	e := newSignInEnv(t, nil)
	ctx := context.Background()
	old := auth.NewArgon2id(auth.Params{MemoryKiB: 128, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}, 1)
	st := authstore.New(e.db, e.al)
	for _, u := range []string{"alice", "dave"} {
		c, err := st.CredentialByUser(ctx, e.ids[u])
		if err != nil {
			t.Fatal(err)
		}
		h, err := old.Hash(ctx, testPassword)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceHash(ctx, e.ids[u], c.Hash, h); err != nil {
			t.Fatal(err)
		}
	}
	hashes, verifies := e.hasher.hashes.Load(), e.hasher.verifies.Load()
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("dave", testPassword), nil, nil); w.Code != 401 {
		t.Fatalf("disabled account: %d", w.Code)
	}
	if h, v := e.hasher.hashes.Load()-hashes, e.hasher.verifies.Load()-verifies; h != 0 || v != 1 {
		t.Errorf("disabled account with the right password: %d hashes and %d verifications, want 0 and 1", h, v)
	}
	if c, _ := st.CredentialByUser(ctx, e.ids["dave"]); !strings.Contains(c.Hash, "$m=128,") {
		t.Errorf("the disabled account's hash was replaced: %s", c.Hash)
	}
	hashes = e.hasher.hashes.Load()
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil); w.Code != 200 {
		t.Fatalf("alice: %d", w.Code)
	}
	if h := e.hasher.hashes.Load() - hashes; h != 1 {
		t.Errorf("%d hashes after a successful sign-in with old parameters, want 1 (the rehash)", h)
	}
	if c, _ := st.CredentialByUser(ctx, e.ids["alice"]); !strings.Contains(c.Hash, "$m=64,") {
		t.Errorf("not rehashed with the current parameters: %s", c.Hash)
	}
}

// TestOverLongUsernameMatchesNoAccount: a submitted name longer than any
// username is refused like an unknown one, not truncated to an account's name.
func TestOverLongUsernameMatchesNoAccount(t *testing.T) {
	e := newSignInEnv(t, nil)
	ctx := context.Background()
	long := strings.Repeat("a", users.MaxUsername)
	u, err := e.parts.users.Create(ctx, audit.System, users.New{Username: long, Role: authz.RoleReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.parts.auth.SetPassword(ctx, authz.Principal{}, true, u.ID, testPassword, auth.ByHost); err != nil {
		t.Fatal(err)
	}
	verifies := e.hasher.verifies.Load()
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody(long+"x", testPassword), nil, nil); w.Code != 401 {
		t.Fatalf("name plus one character: %d %s", w.Code, w.Body.String())
	}
	if v := e.hasher.verifies.Load() - verifies; v != 1 {
		t.Errorf("%d verifications, want 1 (the dummy hash)", v)
	}
	failed := e.auditActions(audit.SignInFailed)
	if len(failed) != 1 || failed[0].Actor.Kind != audit.ActorAnonymous {
		t.Errorf("failure entries %+v, want one anonymous", failed)
	}
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody(long, testPassword), nil, nil); w.Code != 200 {
		t.Errorf("the exact name: %d", w.Code)
	}
}

func TestSessionTerminationTakesEffectOnTheNextRequest(t *testing.T) {
	e := newSignInEnv(t, nil)
	laptop, lcs := e.signIn("alice")
	phone, pcs := e.signIn("alice")
	if w := e.req("GET", "/api/v1/me", "", phone, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := e.req("GET", "/api/v1/auth/sessions", "", laptop, nil)
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 2 || list.Items[0].ID != pcs.Session.ID || list.Items[0].Current || !list.Items[1].Current {
		t.Fatalf("sessions: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), phone.cookie) || strings.Contains(w.Body.String(), laptop.cookie) {
		t.Fatal("session list exposes a token")
	}
	// Bob cannot end alice's session.
	bobB, _ := e.signIn("bob")
	if w := e.req("DELETE", "/api/v1/auth/sessions/"+pcs.Session.ID, "", bobB, nil); w.Code != 404 {
		t.Fatalf("bob ends alice's session: %d", w.Code)
	}
	if w := e.req("DELETE", "/api/v1/auth/sessions/"+pcs.Session.ID, "", laptop, nil); w.Code != 204 {
		t.Fatalf("terminate: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/me", "", phone, nil); w.Code != 401 {
		t.Fatalf("terminated session still works: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/me", "", laptop, nil); w.Code != 200 {
		t.Fatalf("laptop: %d", w.Code)
	}
	ended := e.auditActions(audit.SessionEnded)
	if len(ended) != 1 || ended[0].TargetID != pcs.Session.ID || ended[0].Details["reason"] != "terminated" {
		t.Errorf("audit %+v", ended)
	}
	// Sign-out ends the current session and clears the cookie.
	w = e.req("POST", "/api/v1/auth/sign-out", "", laptop, nil)
	if w.Code != 204 || !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("sign-out: %d %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if w := e.req("GET", "/api/v1/me", "", laptop, nil); w.Code != 401 {
		t.Fatalf("signed-out session still works: %d", w.Code)
	}
	_ = lcs
	// Disabling a user stops their sessions on the next request.
	if w := e.req("GET", "/api/v1/me", "", bobB, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := e.parts.users.Update(context.Background(), audit.System, e.ids["bob"], 1, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if w := e.req("GET", "/api/v1/me", "", bobB, nil); w.Code != 401 {
		t.Fatalf("disabled user's session: %d", w.Code)
	}
}

func TestSessionTimeouts(t *testing.T) {
	e := newSignInEnv(t, nil) // idle 30m, absolute 12h
	b, _ := e.signIn("alice")
	// Used every 20 minutes, the session survives the idle timeout...
	for range 40 {
		e.clock.advance(20 * time.Minute)
		if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 200 {
			if e.clock.now().Sub(time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)) < 12*time.Hour {
				t.Fatalf("at %s: %d", e.clock.now(), w.Code)
			}
			break
		}
	}
	// ...but not the absolute timeout.
	if got := e.clock.now().Sub(time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)); got != 12*time.Hour {
		t.Fatalf("ended after %s, want 12h", got)
	}
	// An idle session ends after 30 minutes.
	b2, _ := e.signIn("alice")
	e.clock.advance(29 * time.Minute)
	if w := e.req("GET", "/api/v1/me", "", b2, nil); w.Code != 200 {
		t.Fatalf("29m idle: %d", w.Code)
	}
	e.clock.advance(30 * time.Minute)
	if w := e.req("GET", "/api/v1/me", "", b2, nil); w.Code != 401 {
		t.Fatalf("30m idle: %d", w.Code)
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	e := newSignInEnv(t, nil)
	a1, _ := e.signIn("alice")
	a2, _ := e.signIn("alice")
	bob, _ := e.signIn("bob")
	w := e.req("PUT", "/api/v1/auth/password", `{"password":"short"}`, a1, nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "at least 12 bytes") {
		t.Fatalf("policy: %d %s", w.Code, w.Body.String())
	}
	// No composition rules: twelve spaces or multi-byte letters count by bytes.
	w = e.req("PUT", "/api/v1/auth/password", `{"password":"ąčęėįšųūąčęė"}`, a1, nil) // 11 letters, 22 bytes
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sessions_ended":1`) {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/me", "", a2, nil); w.Code != 401 {
		t.Errorf("other session survived: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/me", "", a1, nil); w.Code != 200 {
		t.Errorf("current session ended: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/me", "", bob, nil); w.Code != 200 {
		t.Errorf("another user's session ended: %d", w.Code)
	}
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil); w.Code != 401 {
		t.Errorf("old password still works: %d", w.Code)
	}
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", "ąčęėįšųūąčęė"), nil, nil); w.Code != 200 {
		t.Errorf("new password: %d", w.Code)
	}
	// An administrator setting bob's password ends all of bob's sessions.
	w = e.req("PUT", "/api/v1/users/"+e.ids["bob"]+"/password", `{"password":"another long password"}`, a1, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sessions_ended":1`) {
		t.Fatalf("admin set: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/me", "", bob, nil); w.Code != 401 {
		t.Errorf("bob's session survived: %d", w.Code)
	}
	// A read-only user cannot set someone's password.
	bob2 := func() *browser {
		w := e.req("POST", "/api/v1/auth/sign-in", signInBody("bob", "another long password"), nil, nil)
		var cs currentSession
		_ = json.Unmarshal(w.Body.Bytes(), &cs)
		return &browser{cookie: strings.TrimPrefix(strings.SplitN(w.Header().Get("Set-Cookie"), ";", 2)[0], httpapi.SessionCookie+"="), csrf: cs.CSRFToken}
	}()
	if w := e.req("PUT", "/api/v1/users/"+e.ids["alice"]+"/password", `{"password":"another long password"}`, bob2, nil); w.Code != 403 {
		t.Errorf("read-only sets a password: %d", w.Code)
	}
	var by []string
	for _, r := range e.auditActions(audit.PasswordChanged) {
		by = append(by, r.Details["by"]+":"+r.Details["sessions_ended"])
		if r.Actor.Kind == audit.ActorUser && r.Actor.UserID != e.ids["alice"] {
			t.Errorf("actor %+v", r.Actor)
		}
	}
	if got := strings.Join(by, " "); got != "host:0 host:0 host:0 self:1 administrator:1" {
		t.Errorf("password audit %s", got)
	}
	if strings.Contains(e.log.String(), "another long password") {
		t.Error("a password reached the log")
	}
}

func TestCSRFProtectsCookieWrites(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("alice")
	create := func(name string, br *browser, hdr map[string]string) int {
		return e.req("POST", "/api/v1/users", `{"username":"`+name+`","role":"read-only"}`, br, hdr).Code
	}
	if c := create("x1", &browser{cookie: b.cookie}, nil); c != 403 {
		t.Errorf("no CSRF token: %d", c)
	}
	if c := create("x2", &browser{cookie: b.cookie, csrf: "forged"}, nil); c != 403 {
		t.Errorf("wrong CSRF token: %d", c)
	}
	other, _ := e.signIn("alice")
	if c := create("x3", &browser{cookie: b.cookie, csrf: other.csrf}, nil); c != 403 {
		t.Errorf("another session's CSRF token: %d", c)
	}
	if c := create("x4", b, map[string]string{"Sec-Fetch-Site": "cross-site"}); c != 403 {
		t.Errorf("cross-site request with the token: %d", c)
	}
	if c := create("x5", b, map[string]string{"Origin": "https://attacker.example"}); c != 403 {
		t.Errorf("foreign origin: %d", c)
	}
	w := e.req("POST", "/api/v1/auth/sign-out", "", &browser{cookie: b.cookie}, nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"csrf_failed"`) {
		t.Errorf("sign-out without token: %d %s", w.Code, w.Body.String())
	}
	if c := create("x6", b, map[string]string{"Sec-Fetch-Site": "same-origin"}); c != 201 {
		t.Errorf("same-origin with the token: %d", c)
	}
	// Reads need no token.
	if w := e.req("GET", "/api/v1/users", "", &browser{cookie: b.cookie}, nil); w.Code != 200 {
		t.Errorf("read: %d", w.Code)
	}
	// Login CSRF: a cross-site sign-in is refused before any check.
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Errorf("cross-site sign-in: %d", w.Code)
	}
	users, _ := e.parts.users.List(context.Background(), users.ListFilter{Limit: 50})
	for _, u := range users {
		if u.Username != "x6" && strings.HasPrefix(u.Username, "x") {
			t.Errorf("forged request created %s", u.Username)
		}
	}
}

func TestRecentAuthenticationWindow(t *testing.T) {
	e := newSignInEnv(t, nil) // window 10m
	b, cs := e.signIn("alice")
	if cs.RecentAuthUntil == nil || !cs.RecentAuthUntil.Equal(e.clock.now().Add(10*time.Minute)) {
		t.Fatalf("recent until %v", cs.RecentAuthUntil)
	}
	e.clock.advance(9 * time.Minute)
	if w := e.req("POST", "/api/v1/users", `{"username":"erin","role":"read-only"}`, b, nil); w.Code != 201 {
		t.Fatalf("within the window: %d %s", w.Code, w.Body.String())
	}
	e.clock.advance(time.Minute)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/users", `{"username":"frank","role":"read-only"}`},
		{"PATCH", "/api/v1/users/" + e.ids["bob"], `{"role":"administrator","version":1}`},
		{"PUT", "/api/v1/users/" + e.ids["bob"] + "/password", `{"password":"a fresh long password"}`},
		{"PUT", "/api/v1/auth/password", `{"password":"a fresh long password"}`},
	} {
		w := e.req(c.method, c.path, c.body, b, nil)
		if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"reauthentication_required"`) {
			t.Errorf("%s %s after the window: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	// Ordinary actions still work.
	if w := e.req("GET", "/api/v1/users", "", b, nil); w.Code != 200 {
		t.Errorf("read: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/auth/session", "", b, nil); !strings.Contains(w.Body.String(), `"recent_auth_until":null`) {
		t.Errorf("session: %s", w.Body.String())
	}
	// A wrong password is 403 invalid_credentials and keeps the session.
	w := e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"wrong password!"}`, b, nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"invalid_credentials"`) {
		t.Fatalf("wrong re-auth: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 200 {
		t.Fatalf("session lost after a failed re-auth: %d", w.Code)
	}
	w = e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`"}`, b, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "recent_auth_until") {
		t.Fatalf("re-auth: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/users", `{"username":"frank","role":"read-only"}`, b, nil); w.Code != 201 {
		t.Fatalf("after re-auth: %d %s", w.Code, w.Body.String())
	}
	if n := len(e.auditActions(audit.Reauthenticated)); n != 1 {
		t.Errorf("reauthenticated entries: %d", n)
	}
	if f := e.auditActions(audit.ReauthenticationFailed); len(f) != 1 || f[0].Details["reason"] != "invalid_credentials" || f[0].Actor.UserID != e.ids["alice"] {
		t.Errorf("failed re-auth entries: %+v", f)
	}
}

func TestAuthSetPasswordCommand(t *testing.T) {
	dir := t.TempDir()
	env := opsEnv{Options: config.Options{
		LookupEnv: func(k string) (string, bool) {
			switch k {
			case "LEDGER_DATABASE_PATH":
				return filepath.Join(dir, "ledger.db"), true
			case "LEDGER_KEK_VALUE":
				return "test-kek-value-0123456789abcdef", true
			}
			return "", false
		},
		Environ: func() []string { return nil },
	}}
	run := func(stdin string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		env.Stdin = strings.NewReader(stdin)
		code := runAuth(args, &out, &errb, env)
		return code, out.String(), errb.String()
	}
	for _, args := range [][]string{nil, {"set-password"}, {"set-password", "-h"}} {
		if _, _, stderr := run("", args...); !strings.Contains(stderr, "echoed") {
			t.Errorf("%q: the help text does not warn that a terminal echoes the password: %s", args, stderr)
		}
	}
	if code, _, stderr := run(testPassword+"\n", "set-password", "root"); code != 1 || !strings.Contains(stderr, "--create") {
		t.Fatalf("missing user: %d %s", code, stderr)
	}
	if code, _, stderr := run("short\n", "set-password", "--create", "administrator", "root"); code != 1 || !strings.Contains(stderr, "at least 12 bytes") {
		t.Fatalf("short password: %d %s", code, stderr)
	}
	code, stdout, stderr := run(testPassword+"\n", "set-password", "--create", "administrator", "root")
	if code != 0 || !strings.Contains(stdout, "created user root") || !strings.Contains(stdout, "password set for root") {
		t.Fatalf("create: %d %s %s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, testPassword) {
		t.Error("the password was printed")
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	al := store.NewAuditLog(db)
	parts := mustParts(t, db, al)
	if _, err := parts.auth.SignIn(context.Background(), auth.SignInInput{Username: "root", Password: testPassword, Address: "127.0.0.1"}); err != nil {
		t.Fatalf("sign in with the new password: %v", err)
	}
	got, _ := al.List(context.Background(), audit.Filter{Actions: []audit.Action{audit.PasswordChanged}, Limit: 10})
	if len(got) != 1 || got[0].Actor.Kind != audit.ActorSystem || got[0].Details["by"] != "host" {
		t.Errorf("audit %+v", got)
	}
}
