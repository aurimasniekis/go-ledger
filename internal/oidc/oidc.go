// Package oidc is Ledger's OpenID Connect relying party for one configured
// identity provider (task A6, ADR-0033): discovery, the authorization code
// flow with PKCE (S256), state and nonce, and ID token validation. It knows
// nothing about Ledger accounts: it turns a callback into a validated
// [Identity], and internal/domain/auth decides what that identity may do.
//
// Validation is go-oidc's (signature against the provider's JWKS, issuer,
// audience contains the client, expiry) plus Ledger's own checks that go-oidc
// leaves to the caller: the nonce, an audience of exactly this client (OIDC
// Core 3.1.3.7, errata set 2), azp when present, a bounded iat, a non-empty
// subject and, for re-authentication, a fresh auth_time.
package oidc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Defaults (ADR-0033 point 13, accepted by the operator 2026-11-21).
const (
	// DiscoveryTTL is how long a fetched discovery document (and with it the
	// cached signing keys) is used before it is fetched again. go-oidc never
	// refreshes discovery by itself.
	DiscoveryTTL = time.Hour
	// MaxResponseBytes bounds every response read from the provider.
	MaxResponseBytes = 1 << 20
	// ConnectTimeout and RequestTimeout follow decision D17 (10 s connect,
	// 30 s total).
	ConnectTimeout = 10 * time.Second
	RequestTimeout = 30 * time.Second
	// MaxSubjectBytes is the longest subject accepted (OIDC Core: at most 255
	// ASCII characters).
	MaxSubjectBytes = 255
)

// Config is the one configured provider.
type Config struct {
	// Issuer is compared exactly with the discovery document and every ID
	// token (a trailing slash matters).
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is the callback URL registered at the provider.
	RedirectURL string
	Scopes      []string
	// ClockSkew is tolerated on exp, nbf, iat and auth_time.
	ClockSkew time.Duration
	// HTTPClient talks to the provider; nil means [NewHTTPClient].
	HTTPClient *http.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Identity is what a validated ID token says about the user. Issuer and
// Subject together are the stable identity; the rest is display data and
// never identifies anyone (an email address can change hands).
type Identity struct {
	Issuer            string
	Subject           string
	Email             string
	EmailVerified     bool
	PreferredUsername string
	Name              string
	// AuthTime is when the user last authenticated at the provider; zero when
	// the token does not say.
	AuthTime time.Time
}

// ErrUnavailable means the provider could not be reached or answered
// something unusable at the transport level (discovery, keys, token
// endpoint). Nothing about the user was decided.
var ErrUnavailable = errors.New("the identity provider is unavailable")

// ValidationError is a response that failed a check. Check names the check
// (for logs and tests); the cause is never shown to the browser, and it never
// contains a token.
type ValidationError struct {
	Check string
	Err   error
}

func (e *ValidationError) Error() string {
	if e.Err == nil {
		return "oidc: response failed the " + e.Check + " check"
	}
	return "oidc: response failed the " + e.Check + " check: " + truncate(e.Err.Error(), 200)
}

func (e *ValidationError) Unwrap() error { return e.Err }

func invalid(check string, err error) error { return &ValidationError{Check: check, Err: err} }

type unavailableError struct{ err error }

func (e *unavailableError) Error() string {
	return ErrUnavailable.Error() + ": " + truncate(e.err.Error(), 200)
}
func (e *unavailableError) Is(target error) bool { return target == ErrUnavailable }
func (e *unavailableError) Unwrap() error        { return e.err }

func unavailable(err error) error { return &unavailableError{err: err} }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// Client is the relying party. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	provider  *gooidc.Provider
	fetchedAt time.Time
}

// New returns a Client. Nothing is fetched until the first sign-in, so an
// unreachable provider never stops Ledger from starting.
func New(cfg Config) (*Client, error) {
	switch {
	case cfg.Issuer == "", cfg.ClientID == "", cfg.ClientSecret == "", cfg.RedirectURL == "":
		return nil, errors.New("oidc: issuer, client ID, client secret and redirect URL are required")
	case !slices.Contains(cfg.Scopes, gooidc.ScopeOpenID):
		return nil, errors.New("oidc: the scopes must include openid")
	case cfg.ClockSkew < 0:
		return nil, errors.New("oidc: the clock skew must not be negative")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = NewHTTPClient()
	}
	return &Client{cfg: cfg, http: hc}, nil
}

// Issuer returns the configured issuer.
func (c *Client) Issuer() string { return c.cfg.Issuer }

// NewHTTPClient returns the client used for the provider: D17 timeouts,
// responses limited to [MaxResponseBytes], no redirects followed, proxies
// from the environment.
func NewHTTPClient() *http.Client {
	d := &net.Dialer{Timeout: ConnectTimeout}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: d.DialContext, TLSHandshakeTimeout: ConnectTimeout,
		ResponseHeaderTimeout: RequestTimeout, MaxIdleConns: 4, IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: true}
	return &http.Client{Timeout: RequestTimeout, Transport: LimitBody(tr, MaxResponseBytes),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// LimitBody wraps rt so that reading more than n bytes of a response body
// fails instead of buffering without bound (go-oidc reads whole bodies).
func LimitBody(rt http.RoundTripper, n int64) http.RoundTripper {
	return limitTransport{rt: rt, n: n}
}

type limitTransport struct {
	rt http.RoundTripper
	n  int64
}

func (t limitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedBody{rc: resp.Body, left: t.n}
	return resp, nil
}

type limitedBody struct {
	rc interface {
		Read([]byte) (int, error)
		Close() error
	}
	left int64
}

var errTooLarge = errors.New("oidc: the provider's response is too large")

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// Probe one byte: a body of exactly the limit is fine.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			return 0, errTooLarge
		}
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

func (c *Client) ctx(ctx context.Context) context.Context {
	return gooidc.ClientContext(context.WithValue(ctx, oauth2.HTTPClient, c.http), c.http)
}

// discovery is the part of the discovery document Ledger reads itself.
type discovery struct {
	CodeChallengeMethods []string `json:"code_challenge_methods_supported"`
}

// getProvider returns the provider, fetching discovery when there is none or
// it is older than [DiscoveryTTL]. A failed fetch is not cached: the next
// sign-in tries again.
func (c *Client) getProvider(ctx context.Context) (*gooidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	if c.provider != nil && now.Sub(c.fetchedAt) < DiscoveryTTL && !now.Before(c.fetchedAt) {
		return c.provider, nil
	}
	// The key set keeps the context it was created with (without its
	// cancellation) for later fetches, so it must carry the client too.
	p, err := gooidc.NewProvider(c.ctx(context.WithoutCancel(ctx)), c.cfg.Issuer)
	if err != nil {
		var mismatch *gooidc.IssuerMismatchError
		if errors.As(err, &mismatch) {
			return nil, unavailable(errors.New("the discovery document names another issuer"))
		}
		return nil, unavailable(err)
	}
	var d discovery
	if err := p.Claims(&d); err != nil {
		return nil, unavailable(err)
	}
	// PKCE is always used. A provider that lists its methods without S256
	// would reject it, so that is a configuration error; one that lists none
	// is tried anyway (ADR-0033).
	if len(d.CodeChallengeMethods) > 0 && !slices.Contains(d.CodeChallengeMethods, "S256") {
		return nil, unavailable(errors.New("the provider does not support PKCE with S256"))
	}
	c.provider, c.fetchedAt = p, now
	return p, nil
}

func (c *Client) oauth(p *gooidc.Provider) *oauth2.Config {
	return &oauth2.Config{ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret, RedirectURL: c.cfg.RedirectURL,
		Endpoint: p.Endpoint(), Scopes: c.cfg.Scopes}
}

// AuthRequest is one authorization request.
type AuthRequest struct {
	State    string
	Nonce    string
	Verifier string
	// Reauth asks the provider to authenticate the user again (prompt=login,
	// max_age=0); the callback then requires a fresh auth_time.
	Reauth bool
}

// AuthCodeURL returns the provider's authorization URL for a.
func (c *Client) AuthCodeURL(ctx context.Context, a AuthRequest) (string, error) {
	if a.State == "" || a.Nonce == "" || a.Verifier == "" {
		return "", errors.New("oidc: state, nonce and verifier are required")
	}
	p, err := c.getProvider(ctx)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{gooidc.Nonce(a.Nonce), oauth2.S256ChallengeOption(a.Verifier)}
	if a.Reauth {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	return c.oauth(p).AuthCodeURL(a.State, opts...), nil
}

// Expect is what the callback must match.
type Expect struct {
	Nonce string
	// Started is when the authorization request was made: the ID token must
	// not be issued before it (and, for Reauth, nor the authentication).
	Started time.Time
	Reauth  bool
}

// claims are the ID token claims Ledger reads beyond go-oidc's.
type claims struct {
	AuthorizedParty   string          `json:"azp"`
	AuthTime          json.Number     `json:"auth_time"`
	Email             string          `json:"email"`
	EmailVerified     json.RawMessage `json:"email_verified"`
	PreferredUsername string          `json:"preferred_username"`
	Name              string          `json:"name"`
}

// Exchange redeems code with the PKCE verifier and validates the ID token.
// It returns ErrUnavailable (wrapped) when the provider could not be
// reached, and a *ValidationError when the response failed a check.
func (c *Client) Exchange(ctx context.Context, code, verifier string, want Expect) (Identity, error) {
	if code == "" || len(code) > 2048 {
		return Identity{}, invalid("code", nil)
	}
	p, err := c.getProvider(ctx)
	if err != nil {
		return Identity{}, err
	}
	tok, err := c.oauth(p).Exchange(c.ctx(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			// The provider answered and refused (invalid_grant, a wrong
			// verifier, a bad client secret): its body may hold details, so
			// only the error code is kept.
			return Identity{}, invalid("token_response", fmt.Errorf("token endpoint answered %d %s", re.Response.StatusCode, truncate(re.ErrorCode, 64)))
		}
		return Identity{}, unavailable(err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, invalid("id_token_missing", nil)
	}
	skew := c.cfg.ClockSkew
	now := c.cfg.Now()
	v := p.Verifier(&gooidc.Config{ClientID: c.cfg.ClientID, Now: func() time.Time { return now.Add(-skew) }})
	idt, err := v.Verify(c.ctx(ctx), raw)
	if err != nil {
		return Identity{}, classify(err)
	}
	// go-oidc accepts an audience that merely contains the client; OIDC Core
	// (errata set 2) rejects audiences the client does not trust.
	for _, a := range idt.Audience {
		if a != c.cfg.ClientID {
			return Identity{}, invalid("audience", errors.New("the token has an audience other than this client"))
		}
	}
	var cl claims
	if err := idt.Claims(&cl); err != nil {
		return Identity{}, invalid("claims", err)
	}
	if cl.AuthorizedParty != "" && cl.AuthorizedParty != c.cfg.ClientID {
		return Identity{}, invalid("azp", errors.New("the token was issued to another client"))
	}
	// go-oidc does not compare the nonce.
	if want.Nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(want.Nonce)) != 1 {
		return Identity{}, invalid("nonce", nil)
	}
	if idt.IssuedAt.IsZero() || idt.IssuedAt.After(now.Add(skew)) || idt.IssuedAt.Before(want.Started.Add(-skew)) {
		return Identity{}, invalid("issued_at", errors.New("the token's iat is outside this sign-in"))
	}
	if idt.Subject == "" || len(idt.Subject) > MaxSubjectBytes || !utf8.ValidString(idt.Subject) {
		return Identity{}, invalid("subject", nil)
	}
	id := Identity{Issuer: idt.Issuer, Subject: idt.Subject, Email: cl.Email, PreferredUsername: cl.PreferredUsername, Name: cl.Name}
	id.EmailVerified = strings.Trim(string(cl.EmailVerified), `"`) == "true"
	if cl.AuthTime != "" {
		n, err := cl.AuthTime.Int64()
		if err != nil {
			return Identity{}, invalid("auth_time", err)
		}
		id.AuthTime = time.Unix(n, 0).UTC()
	}
	if want.Reauth && (id.AuthTime.IsZero() || id.AuthTime.Before(want.Started.Add(-skew)) || id.AuthTime.After(now.Add(skew))) {
		return Identity{}, invalid("auth_time", errors.New("the provider did not authenticate the user again"))
	}
	if idt.Issuer != c.cfg.Issuer {
		return Identity{}, invalid("issuer", nil)
	}
	return id, nil
}

// classify names the check a go-oidc verification error failed.
func classify(err error) error {
	var exp *gooidc.TokenExpiredError
	if errors.As(err, &exp) {
		return invalid("expired", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "fetching keys") || strings.Contains(msg, "get keys failed") {
		return unavailable(err)
	}
	if strings.Contains(msg, "different provider") {
		return invalid("issuer", err)
	}
	for _, k := range []string{"signature", "audience", "issuer"} {
		if strings.Contains(msg, k) {
			return invalid(k, err)
		}
	}
	return invalid("id_token", err)
}
