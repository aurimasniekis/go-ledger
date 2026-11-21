package oidc_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/oidc"
	"github.com/aurimasniekis/go-ledger/internal/oidc/oidctest"
)

const redirectURL = "http://127.0.0.1:8080/api/v1/auth/oidc/callback"

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

type env struct {
	t     *testing.T
	op    *oidctest.Provider
	c     *oidc.Client
	clock *clock
	txs   *oidc.Transactions
}

func newEnv(t *testing.T) *env {
	t.Helper()
	op := oidctest.New(t)
	ck := &clock{t: time.Now().UTC().Truncate(time.Second)}
	op.Now = ck.now
	c, err := oidc.New(oidc.Config{Issuer: op.Issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, RedirectURL: redirectURL,
		Scopes: []string{"openid", "email"}, ClockSkew: time.Minute, Now: ck.now})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, op: op, c: c, clock: ck, txs: oidc.NewTransactions(ck.now)}
}

// flow runs an authorization request through the fake provider and returns
// the exchange's outcome. reauth asks for a fresh authentication.
func (e *env) flow(reauth bool) (oidc.Identity, error) {
	e.t.Helper()
	state, tx := e.txs.Begin(oidc.Transaction{Mode: oidc.ModeSignIn})
	u, err := e.c.AuthCodeURL(context.Background(), oidc.AuthRequest{State: state, Nonce: tx.Nonce, Verifier: tx.Verifier, Reauth: reauth})
	if err != nil {
		return oidc.Identity{}, err
	}
	cb, err := e.op.Login(u)
	if err != nil {
		e.t.Fatal(err)
	}
	got, ok := e.txs.Take(cb.Query().Get("state"), state)
	if !ok {
		e.t.Fatal("state did not round-trip")
	}
	return e.c.Exchange(context.Background(), cb.Query().Get("code"), got.Verifier, oidc.Expect{Nonce: got.Nonce, Started: got.CreatedAt, Reauth: reauth})
}

func check(err error) string {
	var ve *oidc.ValidationError
	if errors.As(err, &ve) {
		return ve.Check
	}
	if errors.Is(err, oidc.ErrUnavailable) {
		return "unavailable"
	}
	if err != nil {
		return "other: " + err.Error()
	}
	return ""
}

func TestSignInFlowWithPKCE(t *testing.T) {
	e := newEnv(t)
	id, err := e.flow(false)
	if err != nil {
		t.Fatal(err)
	}
	if id.Issuer != e.op.Issuer || id.Subject != "subject-0001" || id.Email != "owner@example.com" || !id.EmailVerified ||
		id.PreferredUsername != "owner" || id.Name != "Test Owner" || id.AuthTime.IsZero() {
		t.Errorf("identity %+v", id)
	}
	q := e.op.LastAuthorize()
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("nonce") == "" || q.Get("state") == "" ||
		q.Get("scope") != "openid email" || q.Get("redirect_uri") != redirectURL || q.Has("prompt") || q.Has("max_age") {
		t.Errorf("authorization request %v", q)
	}
	// Discovery and keys are cached: a second sign-in fetches neither.
	if _, err := e.flow(false); err != nil {
		t.Fatal(err)
	}
	if e.op.Discoveries.Load() != 1 || e.op.KeyFetches.Load() != 1 {
		t.Errorf("discoveries %d, key fetches %d", e.op.Discoveries.Load(), e.op.KeyFetches.Load())
	}
	// After DiscoveryTTL discovery is fetched again.
	e.clock.advance(oidc.DiscoveryTTL + time.Second)
	if _, err := e.flow(false); err != nil {
		t.Fatal(err)
	}
	if e.op.Discoveries.Load() != 2 {
		t.Errorf("discoveries after the TTL: %d", e.op.Discoveries.Load())
	}
	// A rotated key (new kid) is fetched once and verifies.
	e.op.RotateKey(t)
	if _, err := e.flow(false); err != nil {
		t.Fatalf("after key rotation: %v", err)
	}
}

func TestExchangeRejectsBadResponses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"bad signature", func(e *env) { e.op.SignWith = oidctest.NewKey(t) }, "signature"},
		{"wrong audience", func(e *env) { e.op.Mutate = func(c map[string]any) { c["aud"] = "someone-else" } }, "audience"},
		{"an extra untrusted audience", func(e *env) {
			e.op.Mutate = func(c map[string]any) { c["aud"] = []string{oidctest.ClientID, "someone-else"} }
		}, "audience"},
		{"azp of another client", func(e *env) { e.op.Mutate = func(c map[string]any) { c["azp"] = "someone-else" } }, "azp"},
		{"wrong nonce", func(e *env) { e.op.Mutate = func(c map[string]any) { c["nonce"] = "not-the-nonce" } }, "nonce"},
		{"no nonce", func(e *env) { e.op.Mutate = func(c map[string]any) { delete(c, "nonce") } }, "nonce"},
		{"expired", func(e *env) {
			e.op.Mutate = func(c map[string]any) { c["exp"] = e.clock.now().Add(-2 * time.Minute).Unix() }
		}, "expired"},
		{"issued in the future", func(e *env) {
			e.op.Mutate = func(c map[string]any) { c["iat"] = e.clock.now().Add(5 * time.Minute).Unix() }
		}, "issued_at"},
		{"issued before the request", func(e *env) {
			e.op.Mutate = func(c map[string]any) { c["iat"] = e.clock.now().Add(-time.Hour).Unix() }
		}, "issued_at"},
		{"another issuer", func(e *env) { e.op.Mutate = func(c map[string]any) { c["iss"] = "https://evil.example.com" } }, "issuer"},
		{"empty subject", func(e *env) { e.op.Mutate = func(c map[string]any) { c["sub"] = "" } }, "subject"},
		{"subject too long", func(e *env) { e.op.Mutate = func(c map[string]any) { c["sub"] = strings.Repeat("s", 256) } }, "subject"},
		{"no ID token", func(e *env) { e.op.OmitIDToken = true }, "id_token_missing"},
		{"discovery names another issuer", func(e *env) { e.op.DiscoveryIssuer = "https://other.example.com" }, "unavailable"},
		{"no PKCE S256", func(e *env) { e.op.Methods = []string{"plain"} }, "unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			_, err := e.flow(false)
			if got := check(err); got != c.want {
				t.Fatalf("got %q (%v), want %q", got, err, c.want)
			}
			if err != nil && strings.Contains(err.Error(), "eyJ") {
				t.Errorf("the error contains a token: %v", err)
			}
		})
	}
}

func TestExchangeChecksTheVerifierAndCode(t *testing.T) {
	e := newEnv(t)
	state, tx := e.txs.Begin(oidc.Transaction{Mode: oidc.ModeSignIn})
	u, err := e.c.AuthCodeURL(context.Background(), oidc.AuthRequest{State: state, Nonce: tx.Nonce, Verifier: tx.Verifier})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := e.op.Login(u)
	if err != nil {
		t.Fatal(err)
	}
	code := cb.Query().Get("code")
	// A wrong verifier is refused by the token endpoint (PKCE), and the
	// provider's error description is not repeated.
	_, err = e.c.Exchange(context.Background(), code, "a-wrong-verifier-a-wrong-verifier-a-wrong-v", oidc.Expect{Nonce: tx.Nonce, Started: tx.CreatedAt})
	if check(err) != "token_response" || strings.Contains(err.Error(), "must not show") {
		t.Fatalf("wrong verifier: %v", err)
	}
	// The code was used up by that attempt: a replay fails too.
	if _, err := e.c.Exchange(context.Background(), code, tx.Verifier, oidc.Expect{Nonce: tx.Nonce, Started: tx.CreatedAt}); check(err) != "token_response" {
		t.Fatalf("replayed code: %v", err)
	}
	if _, err := e.c.Exchange(context.Background(), "", tx.Verifier, oidc.Expect{Nonce: tx.Nonce}); check(err) != "code" {
		t.Fatalf("empty code: %v", err)
	}
}

func TestReauthenticationNeedsAFreshAuthTime(t *testing.T) {
	e := newEnv(t)
	if _, err := e.flow(true); err != nil {
		t.Fatal(err)
	}
	q := e.op.LastAuthorize()
	if q.Get("prompt") != "login" || q.Get("max_age") != "0" {
		t.Errorf("reauth request %v", q)
	}
	e.op.Mutate = func(c map[string]any) { c["auth_time"] = e.clock.now().Add(-time.Hour).Unix() }
	if _, err := e.flow(true); check(err) != "auth_time" {
		t.Fatalf("stale auth_time: %v", err)
	}
	e.op.Mutate = func(c map[string]any) { delete(c, "auth_time") }
	if _, err := e.flow(true); check(err) != "auth_time" {
		t.Fatalf("no auth_time: %v", err)
	}
	// A sign-in does not need auth_time.
	if _, err := e.flow(false); err != nil {
		t.Fatalf("sign-in without auth_time: %v", err)
	}
}

func TestUnavailableProvider(t *testing.T) {
	e := newEnv(t)
	e.op.Server.Close()
	_, err := e.c.AuthCodeURL(context.Background(), oidc.AuthRequest{State: "s", Nonce: "n", Verifier: "v"})
	if !errors.Is(err, oidc.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	base := oidc.Config{Issuer: "https://id.example.com", ClientID: "c", ClientSecret: "s", RedirectURL: redirectURL, Scopes: []string{"openid"}}
	if _, err := oidc.New(base); err != nil {
		t.Fatal(err)
	}
	for _, mod := range []func(*oidc.Config){
		func(c *oidc.Config) { c.Issuer = "" }, func(c *oidc.Config) { c.ClientSecret = "" },
		func(c *oidc.Config) { c.Scopes = []string{"email"} }, func(c *oidc.Config) { c.ClockSkew = -time.Second },
	} {
		c := base
		mod(&c)
		if _, err := oidc.New(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestLimitBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	}))
	defer srv.Close()
	for _, c := range []struct {
		limit int64
		ok    bool
	}{{100, true}, {99, false}} {
		hc := &http.Client{Transport: oidc.LimitBody(http.DefaultTransport, c.limit)}
		resp, err := hc.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if (err == nil) != c.ok || (c.ok && len(b) != 100) {
			t.Errorf("limit %d: %d bytes, err %v", c.limit, len(b), err)
		}
	}
}

func TestTransactions(t *testing.T) {
	ck := &clock{t: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
	txs := oidc.NewTransactions(ck.now)
	state, tx := txs.Begin(oidc.Transaction{Mode: oidc.ModeLink, UserID: "u"})
	if len(state) < 43 || tx.Nonce == "" || len(tx.Verifier) != 43 || !tx.CreatedAt.Equal(ck.now()) {
		t.Fatalf("begin: %q %+v", state, tx)
	}
	if _, ok := txs.Take(state, "another-browser"); ok {
		t.Fatal("a mismatched cookie was accepted")
	}
	got, ok := txs.Take(state, state)
	if !ok || got.UserID != "u" || got.Mode != oidc.ModeLink {
		t.Fatalf("take: %v %+v", ok, got)
	}
	if _, ok := txs.Take(state, state); ok {
		t.Fatal("a state was accepted twice")
	}
	state, _ = txs.Begin(oidc.Transaction{Mode: oidc.ModeSignIn})
	ck.advance(oidc.TransactionTTL)
	if _, ok := txs.Take(state, state); ok {
		t.Fatal("an expired state was accepted")
	}
	// Full: the oldest is dropped.
	first, _ := txs.Begin(oidc.Transaction{})
	ck.advance(time.Second)
	for i := 1; i < oidc.MaxTransactions; i++ {
		txs.Begin(oidc.Transaction{})
	}
	last, _ := txs.Begin(oidc.Transaction{})
	if txs.Len() != oidc.MaxTransactions {
		t.Errorf("len %d", txs.Len())
	}
	if _, ok := txs.Take(first, first); ok {
		t.Error("the oldest transaction survived")
	}
	if _, ok := txs.Take(last, last); !ok {
		t.Error("the newest transaction was dropped")
	}
	_ = url.Values{}
}
