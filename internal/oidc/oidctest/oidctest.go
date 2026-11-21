// Package oidctest is a local OpenID Connect provider for offline tests
// (task A6): discovery, JWKS, an authorization endpoint that signs in a
// configured identity at once, and a token endpoint that checks the client
// secret, the redirect URI and the PKCE verifier (S256) before returning an
// ID token signed with a test RSA key. Hooks bend the token to test every
// check of the relying party. It never talks to a real provider.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Test client credentials (synthetic).
const (
	ClientID     = "ledger-test"
	ClientSecret = "test-client-secret-not-real"
)

type grant struct {
	challenge, nonce, redirect string
	authTime                   time.Time
	subject                    string
}

// Provider is the fake identity provider. Exported fields may be changed
// between requests (under the test's own synchronisation).
type Provider struct {
	Server *httptest.Server
	Issuer string

	// Subject and the display claims are the identity the authorization
	// endpoint signs in.
	Subject, Email, PreferredUsername, Name string
	// Now is the provider's clock; nil means time.Now.
	Now func() time.Time
	// Mutate, when set, edits the claims of every ID token before signing.
	Mutate func(claims map[string]any)
	// SignWith, when set, signs ID tokens with another key (under the same
	// key ID), so the signature does not verify.
	SignWith *rsa.PrivateKey
	// DiscoveryIssuer, when set, is the issuer the discovery document claims.
	DiscoveryIssuer string
	// Methods is code_challenge_methods_supported (default S256).
	Methods []string
	// OmitIDToken leaves id_token out of the token response.
	OmitIDToken bool
	// Deny makes the authorization endpoint answer error=access_denied.
	Deny bool

	// Counters of requests per endpoint.
	Discoveries, KeyFetches, TokenRequests atomic.Int64

	mu    sync.Mutex
	key   *rsa.PrivateKey
	kid   string
	codes map[string]grant
	// LastAuthorize is the query of the last authorization request.
	lastAuthorize url.Values
}

// New starts a provider; it stops when the test ends.
func New(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{key: key, kid: "test-key-1", codes: map[string]grant{}, Subject: "subject-0001", Email: "owner@example.com",
		PreferredUsername: "owner", Name: "Test Owner"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(mux)
	p.Issuer = p.Server.URL
	t.Cleanup(p.Server.Close)
	return p
}

// NewKey returns a fresh RSA key (for SignWith).
func NewKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// RotateKey replaces the signing key and its key ID.
func (p *Provider) RotateKey(t testing.TB) {
	k := NewKey(t)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key, p.kid = k, fmt.Sprintf("test-key-%d", time.Now().UnixNano())
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	p.Discoveries.Add(1)
	iss := p.Issuer
	if p.DiscoveryIssuer != "" {
		iss = p.DiscoveryIssuer
	}
	methods := p.Methods
	if methods == nil {
		methods = []string{"S256"}
	}
	writeJSON(w, 200, map[string]any{
		"issuer": iss, "authorization_endpoint": p.Server.URL + "/authorize", "token_endpoint": p.Server.URL + "/token",
		"jwks_uri": p.Server.URL + "/jwks", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": methods,
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	p.KeyFetches.Add(1)
	p.mu.Lock()
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: p.kid, Algorithm: "RS256", Use: "sig"}}}
	p.mu.Unlock()
	writeJSON(w, 200, set)
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// LastAuthorize returns the query of the last authorization request.
func (p *Provider) LastAuthorize() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAuthorize
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	p.lastAuthorize = q
	p.mu.Unlock()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("client_id") != ClientID || q.Get("response_type") != "code" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	back := redirect.Query()
	back.Set("state", q.Get("state"))
	if p.Deny {
		back.Set("error", "access_denied")
	} else {
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "PKCE S256 required", http.StatusBadRequest)
			return
		}
		code := random()
		p.mu.Lock()
		p.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"), authTime: p.now(), subject: p.Subject}
		p.mu.Unlock()
		back.Set("code", code)
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func tokenError(w http.ResponseWriter, code string) {
	writeJSON(w, 400, map[string]string{"error": code, "error_description": "details the relying party must not show"})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	p.TokenRequests.Add(1)
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(ClientSecret)) != 1 {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	g, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code")) // single use
	key, kid := p.key, p.kid
	p.mu.Unlock()
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != g.redirect {
		tokenError(w, "invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		tokenError(w, "invalid_grant")
		return
	}
	resp := map[string]any{"access_token": random(), "token_type": "Bearer", "expires_in": 300}
	if !p.OmitIDToken {
		now := p.now()
		claims := map[string]any{"iss": p.Issuer, "sub": g.subject, "aud": ClientID, "exp": now.Add(5 * time.Minute).Unix(),
			"iat": now.Unix(), "nonce": g.nonce, "auth_time": g.authTime.Unix(), "email": p.Email, "email_verified": true,
			"preferred_username": p.PreferredUsername, "name": p.Name}
		if p.Mutate != nil {
			p.Mutate(claims)
		}
		if p.SignWith != nil {
			key = p.SignWith
		}
		raw, err := Sign(key, kid, claims)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		resp["id_token"] = raw
	}
	writeJSON(w, 200, resp)
}

// Sign returns a compact RS256 JWS of claims with key ID kid.
func Sign(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	obj, err := sig.Sign(payload)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

// Login plays the browser at the provider: it opens authURL (Ledger's
// redirect target) without following redirects and returns the callback URL
// the provider sends the browser to.
func (p *Provider) Login(authURL string) (*url.URL, error) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second}
	resp, err := c.Get(authURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		return nil, fmt.Errorf("authorize answered %d", resp.StatusCode)
	}
	return url.Parse(resp.Header.Get("Location"))
}
