package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	f()
}

func TestRouteRegistrationRules(t *testing.T) {
	s := apiServer(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	mustPanic(t, "Handle on /api", func() { s.Handle("GET /api/v1/x", ok) })
	mustPanic(t, "HandleAPI outside /api", func() { s.HandleAPI("GET /x", Public(), ok) })
	mustPanic(t, "no access rule", func() { s.HandleAPI("GET /api/v1/x", Access{}, ok) })
	mustPanic(t, "unknown grant", func() { Requires(authz.G("nope", authz.ActionRead)) })
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		mustPanic(t, m+" with a read grant", func() {
			s.HandleAPI(m+" /api/v1/x", Requires(authz.G(authz.ResourceUsers, authz.ActionRead)), ok)
		})
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		mustPanic(t, m+" Authenticated", func() { s.HandleAPI(m+" /api/v1/x", Authenticated(), ok) })
	}
	// Allowed: a GET with a read grant, a write with a write grant.
	s.HandleAPI("GET /api/v1/r", Requires(authz.G(authz.ResourceUsers, authz.ActionRead)), ok)
	s.HandleAPI("POST /api/v1/w", Requires(authz.G(authz.ResourceUsers, authz.ActionWrite)), ok)
	for _, r := range s.APIRoutes("/api/v1/w") {
		if r.Access.String() != "users:write" {
			t.Fatalf("route access %q", r.Access)
		}
	}
}

func TestAccessStrings(t *testing.T) {
	for a, want := range map[Access]string{
		Public(): "public", Authenticated(): "authenticated", {}: "invalid",
		Requires(authz.G(authz.ResourceUsers, authz.ActionWrite)): "users:write",
	} {
		if a.String() != want {
			t.Errorf("%v: %q", want, a.String())
		}
	}
}

// fixed authenticates every request carrying X-Test-Auth. Its user
// principals pass the CSRF check when X-Test-CSRF is not "bad".
func fixed(p authz.Principal, err error) Authenticator {
	return csrfChecked{AuthenticatorFunc(func(r *http.Request) (authz.Principal, error) {
		if r.Header.Get("X-Test-Auth") == "" {
			return authz.Principal{}, ErrNoCredentials
		}
		return p, err
	})}
}

type csrfChecked struct{ AuthenticatorFunc }

func (csrfChecked) VerifyCSRF(r *http.Request, _ authz.Principal) bool {
	return r.Header.Get("X-Test-CSRF") != "bad"
}

// noCSRF authenticates like fixed but cannot verify CSRF tokens.
func noCSRF(p authz.Principal) Authenticator {
	return AuthenticatorFunc(func(r *http.Request) (authz.Principal, error) {
		if r.Header.Get("X-Test-Auth") == "" {
			return authz.Principal{}, ErrNoCredentials
		}
		return p, nil
	})
}

func TestCSRFAndRecentAuthentication(t *testing.T) {
	write := authz.G(authz.ResourceUsers, authz.ActionWrite)
	admin := authz.UserPrincipal("A", authz.RoleAdministrator)
	admin.SessionID = "S"
	token := authz.TokenPrincipal("T", "A", authz.RoleAdministrator, authz.Grants{write})
	confined := admin
	confined.EnrollmentRequired = true
	recentNo := func(authz.Principal) bool { return false }
	recentYes := func(authz.Principal) bool { return true }
	cases := []struct {
		name   string
		authn  Authenticator
		recent func(authz.Principal) bool
		access Access
		method string
		hdr    map[string]string
		status int
		code   Code
	}{
		{"user write with CSRF token", fixed(admin, nil), nil, Requires(write), "POST", nil, 200, ""},
		{"user write with a bad CSRF token", fixed(admin, nil), nil, Requires(write), "POST", map[string]string{"X-Test-CSRF": "bad"}, 403, CodeCSRF},
		{"authenticator without CSRF support fails closed", noCSRF(admin), nil, Requires(write), "PUT", nil, 403, CodeCSRF},
		{"cross-site browser write", fixed(admin, nil), nil, Requires(write), "DELETE", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403, CodeCSRF},
		{"foreign Origin write", fixed(admin, nil), nil, Requires(write), "PATCH", map[string]string{"Origin": "https://evil.example"}, 403, CodeCSRF},
		{"same-origin browser write", fixed(admin, nil), nil, Requires(write), "POST", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200, ""},
		{"user read needs no CSRF token", noCSRF(admin), nil, Requires(authz.G(authz.ResourceUsers, authz.ActionRead)), "GET", nil, 200, ""},
		{"token write needs no CSRF token", noCSRF(token), nil, Requires(write), "POST", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200, ""},
		{"session route refuses tokens", noCSRF(token), nil, SessionOnly(), "POST", nil, 403, CodeForbidden},
		{"session route refuses users without a session", fixed(authz.UserPrincipal("A", authz.RoleReadOnly), nil), nil, SessionOnly(), "GET", nil, 403, CodeForbidden},
		{"session route for any role", fixed(authz.Principal{Kind: authz.PrincipalUser, UserID: "R", Role: authz.RoleReadOnly, SessionID: "S"}, nil), nil, SessionOnly(), "POST", nil, 200, ""},
		{"recent auth without a check fails closed", fixed(admin, nil), nil, Requires(write).WithRecentAuth(), "POST", nil, 403, CodeReauthRequired},
		{"recent auth not recent", fixed(admin, nil), recentNo, Requires(write).WithRecentAuth(), "POST", nil, 403, CodeReauthRequired},
		{"recent auth recent", fixed(admin, nil), recentYes, Requires(write).WithRecentAuth(), "POST", nil, 200, ""},
		{"recent auth exempts tokens", noCSRF(token), recentNo, Requires(write).WithRecentAuth(), "POST", nil, 200, ""},
		{"CSRF is checked before recent auth", fixed(admin, nil), recentNo, Requires(write).WithRecentAuth(), "POST", map[string]string{"X-Test-CSRF": "bad"}, 403, CodeCSRF},
		// Required 2FA (ADR-0030): a confined session reaches only routes
		// marked DuringEnrollment, reads included; tokens are never confined.
		{"confined session cannot read", fixed(confined, nil), nil, Requires(authz.G(authz.ResourceUsers, authz.ActionRead)), "GET", nil, 403, CodeEnrollmentRequired},
		{"confined session cannot write", fixed(confined, nil), recentYes, Requires(write), "POST", nil, 403, CodeEnrollmentRequired},
		{"confined session cannot use Authenticated", fixed(confined, nil), nil, Authenticated(), "GET", nil, 403, CodeEnrollmentRequired},
		{"confined session cannot use other session routes", fixed(confined, nil), nil, SessionOnly(), "GET", nil, 403, CodeEnrollmentRequired},
		{"confined session reaches enrolment routes", fixed(confined, nil), recentYes, SessionOnly().WithRecentAuth().DuringEnrollment(), "POST", nil, 200, ""},
		{"enrolment routes still need CSRF", fixed(confined, nil), recentYes, SessionOnly().DuringEnrollment(), "POST", map[string]string{"X-Test-CSRF": "bad"}, 403, CodeCSRF},
		{"unconfined session on an enrolment route", fixed(admin, nil), nil, SessionOnly().DuringEnrollment(), "GET", nil, 200, ""},
	}
	for _, c := range cases {
		opts := []Option{WithAuthenticators(c.authn)}
		if c.recent != nil {
			opts = append(opts, WithRecentAuth(c.recent))
		}
		s := apiServer(t, opts...)
		s.HandleAPI(c.method+" /api/v1/t", c.access, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
		req := httptest.NewRequest(c.method, "/api/v1/t", nil)
		req.Header.Set("X-Test-Auth", "1")
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		rec := do(s.Handler(), req)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.status, rec.Body.String())
			continue
		}
		if c.code != "" {
			if got := decodeErr(t, rec).Code; got != c.code {
				t.Errorf("%s: code %s, want %s", c.name, got, c.code)
			}
		}
	}
	mustPanic(t, "recent auth on a public route", func() {
		apiServer(t).HandleAPI("POST /api/v1/x", Public().WithRecentAuth(), http.NotFoundHandler())
	})
	if SessionOnly().String() != "session" || !Requires(write).WithRecentAuth().RecentAuth() || Requires(write).RecentAuth() {
		t.Error("access strings")
	}
	for name, a := range map[string]Access{"grant": Requires(write).DuringEnrollment(), "authenticated": Authenticated().DuringEnrollment(), "public": Public().DuringEnrollment()} {
		mustPanic(t, "DuringEnrollment on a "+name+" route", func() { apiServer(t).HandleAPI("GET /api/v1/x", a, http.NotFoundHandler()) })
	}
	if !SessionOnly().DuringEnrollment().AllowedDuringEnrollment() || SessionOnly().AllowedDuringEnrollment() {
		t.Error("DuringEnrollment flag")
	}
}

func TestClientAddress(t *testing.T) {
	s := New(config.HTTP{TrustedProxies: []string{"10.0.0.0/8", "192.0.2.1"}}, slog.New(slog.DiscardHandler))
	for _, c := range []struct{ remote, xff, want string }{
		{"203.0.113.7:5000", "", "203.0.113.7"},
		{"203.0.113.7:5000", "198.51.100.1", "203.0.113.7"},                  // untrusted peer: header ignored
		{"10.1.2.3:5000", "198.51.100.1", "198.51.100.1"},                    // trusted proxy
		{"10.1.2.3:5000", "6.6.6.6, 198.51.100.1, 10.9.9.9", "198.51.100.1"}, // right-most untrusted hop
		{"192.0.2.1:5000", "garbage", "192.0.2.1"},                           // malformed hop: stop
		{"[::ffff:203.0.113.7]:5000", "", "203.0.113.7"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.ClientAddress(r); got != c.want {
			t.Errorf("%s / %q: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestForwardingFromUntrustedProxyIsWarnedOnce(t *testing.T) {
	var buf bytes.Buffer
	s := New(config.HTTP{TrustedProxies: []string{"10.0.0.0/8"}, MaxBodyBytes: 1024}, slog.New(slog.NewTextHandler(&buf, nil)))
	send := func(remote, xff string) {
		r := httptest.NewRequest("GET", "/healthz-none", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		s.Handler().ServeHTTP(httptest.NewRecorder(), r)
	}
	warnings := func() int { return strings.Count(buf.String(), "not in http.trusted_proxies") }
	send("203.0.113.7:1", "198.51.100.1") // public peer: a client sending the header itself
	send("10.1.2.3:1", "198.51.100.1")    // trusted proxy
	send("127.0.0.1:1", "")               // loopback without the header
	if n := warnings(); n != 0 {
		t.Fatalf("%d warnings before an untrusted local proxy forwarded: %s", n, buf.String())
	}
	send("127.0.0.1:1", "198.51.100.1") // a reverse proxy on the same host, not trusted
	if n := warnings(); n != 1 || !strings.Contains(buf.String(), "peer=127.0.0.1") {
		t.Fatalf("%d warnings: %s", n, buf.String())
	}
	send("192.168.1.2:1", "198.51.100.1")
	send("[::1]:1", "198.51.100.1")
	if n := warnings(); n != 1 {
		t.Errorf("warned %d times, want once per process", n)
	}
	other := New(config.HTTP{MaxBodyBytes: 1024}, slog.New(slog.NewTextHandler(&buf, nil)))
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[fd00::5]:1"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	other.Handler().ServeHTTP(httptest.NewRecorder(), r)
	if n := warnings(); n != 2 {
		t.Errorf("a private IPv6 peer was not warned about: %d", n)
	}
}

func TestAuthorization(t *testing.T) {
	read := authz.G(authz.ResourceUsers, authz.ActionRead)
	write := authz.G(authz.ResourceUsers, authz.ActionWrite)
	reader := authz.TokenPrincipal("T", "U", authz.RoleAdministrator, authz.Grants{read})
	admin := authz.UserPrincipal("A", authz.RoleAdministrator)
	ro := authz.UserPrincipal("R", authz.RoleReadOnly)
	cases := []struct {
		name   string
		authn  []Authenticator
		access Access
		method string
		cred   bool
		status int
		code   Code
	}{
		{"no authenticators", nil, Requires(read), "GET", true, 401, CodeUnauthenticated},
		{"no credentials", []Authenticator{fixed(admin, nil)}, Requires(read), "GET", false, 401, CodeUnauthenticated},
		{"invalid credentials", []Authenticator{fixed(authz.Principal{}, ErrInvalidCredentials)}, Authenticated(), "GET", true, 401, CodeUnauthenticated},
		{"authenticator failure", []Authenticator{fixed(authz.Principal{}, errors.New("db down"))}, Authenticated(), "GET", true, 500, CodeInternal},
		{"empty principal", []Authenticator{fixed(authz.Principal{}, nil)}, Authenticated(), "GET", true, 500, CodeInternal},
		{"read grant reads", []Authenticator{fixed(reader, nil)}, Requires(read), "GET", true, 200, ""},
		{"read grant cannot write", []Authenticator{fixed(reader, nil)}, Requires(write), "POST", true, 403, CodeForbidden},
		{"read-only role cannot write", []Authenticator{fixed(ro, nil)}, Requires(write), "PATCH", true, 403, CodeForbidden},
		{"read-only role cannot read admin resource", []Authenticator{fixed(ro, nil)}, Requires(read), "GET", true, 403, CodeForbidden},
		{"admin writes", []Authenticator{fixed(admin, nil)}, Requires(write), "POST", true, 200, ""},
		{"authenticated read-only", []Authenticator{fixed(ro, nil)}, Authenticated(), "GET", true, 200, ""},
		{"second authenticator", []Authenticator{AuthenticatorFunc(func(*http.Request) (authz.Principal, error) { return authz.Principal{}, ErrNoCredentials }), fixed(admin, nil)}, Requires(write), "DELETE", true, 200, ""},
		{"public skips authenticators", []Authenticator{fixed(authz.Principal{}, errors.New("must not run"))}, Public(), "GET", true, 200, ""},
	}
	for _, c := range cases {
		s := apiServer(t, WithAuthenticators(c.authn...))
		var seen authz.Principal
		s.HandleAPI(c.method+" /api/v1/t", c.access, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen, _ = authz.FromContext(r.Context())
			w.WriteHeader(200)
		}))
		req := httptest.NewRequest(c.method, "/api/v1/t", nil)
		if c.cred {
			req.Header.Set("X-Test-Auth", "1")
		}
		rec := do(s.Handler(), req)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.status, rec.Body.String())
			continue
		}
		if c.code != "" {
			if got := decodeErr(t, rec).Code; got != c.code {
				t.Errorf("%s: code %s, want %s", c.name, got, c.code)
			}
		}
		if c.status == 401 && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate", c.name)
		}
		if c.code == CodeForbidden && !strings.Contains(rec.Body.String(), "users:") {
			t.Errorf("%s: forbidden message does not name the grant: %s", c.name, rec.Body.String())
		}
		if c.status == 200 && c.access != Public() && seen.UserID == "" {
			t.Errorf("%s: handler did not get the principal", c.name)
		}
	}
}

func TestMeAndGrants(t *testing.T) {
	tok := authz.TokenPrincipal("01JD3Y4C8V9M2Q7W5R6T0XZBNB", "01JD3Y4C8V9M2Q7W5R6T0XZBNA", authz.RoleAdministrator,
		authz.Grants{authz.G(authz.ResourceUsers, authz.ActionRead)})
	s := apiServer(t, WithAuthenticators(fixed(tok, nil)))
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("X-Test-Auth", "1")
	rec := do(s.Handler(), req)
	var me principalJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != 200 {
		t.Fatalf("me: %d %s", rec.Code, rec.Body.String())
	}
	if me.Kind != authz.PrincipalToken || me.TokenID == nil || *me.TokenID != tok.TokenID || strings.Join(me.Grants, ",") != "users:read" || me.Role != authz.RoleAdministrator {
		t.Fatalf("me: %+v", me)
	}
	req = httptest.NewRequest("GET", "/api/v1/grants", nil)
	req.Header.Set("X-Test-Auth", "1")
	rec = do(s.Handler(), req)
	var v vocabularyJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 {
		t.Fatalf("grants: %d %s", rec.Code, rec.Body.String())
	}
	if len(v.Resources) != len(authz.Resources()) || len(v.Roles) != 2 || v.Roles[0].Name != authz.RoleAdministrator {
		t.Fatalf("grants: %+v", v)
	}
	if rec := do(s.Handler(), httptest.NewRequest("GET", "/api/v1/me", nil)); rec.Code != 401 {
		t.Fatalf("anonymous me: %d", rec.Code)
	}
}

func TestContractChecksAccess(t *testing.T) {
	doc := []byte(`openapi: 3.1.1
servers: [{url: /api/v1}]
paths:
  /a:
    get:
      operationId: a
      x-ledger-access: users:read
      responses: {default: {$ref: "#/components/responses/Error"}}
  /b:
    get:
      operationId: b
      responses: {default: {$ref: "#/components/responses/Error"}}
components: {responses: {Error: {description: e}}}
`)
	routes := []Route{
		{Method: "GET", Path: "/api/v1/a", Access: Requires(authz.G(authz.ResourceUsers, authz.ActionWrite))},
		{Method: "GET", Path: "/api/v1/b", Access: Public()},
	}
	got := strings.Join(CheckContract(doc, routes), "\n")
	for _, want := range []string{`GET /api/v1/a: x-ledger-access is "users:read", the route requires "users:write"`, `GET /api/v1/b: x-ledger-access is "", the route requires "public"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestAuthorizationRunsBeforeAPIMiddleware pins the order: middleware sees only
// allowed requests, with the principal already in the context.
func TestAuthorizationRunsBeforeAPIMiddleware(t *testing.T) {
	var ran int
	var sawPrincipal bool
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran++
			_, sawPrincipal = authz.FromContext(r.Context())
			next.ServeHTTP(w, r)
		})
	}
	admin := authz.UserPrincipal("A", authz.RoleAdministrator)
	s := apiServer(t, WithAuthenticators(fixed(admin, nil)), WithAPIMiddleware(mw))
	s.HandleAPI("GET /api/v1/t", Requires(authz.G(authz.ResourceUsers, authz.ActionRead)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	if rec := do(s.Handler(), httptest.NewRequest("GET", "/api/v1/t", nil)); rec.Code != 401 || ran != 0 {
		t.Fatalf("anonymous: status %d, middleware ran %d times", rec.Code, ran)
	}
	req := httptest.NewRequest("GET", "/api/v1/t", nil)
	req.Header.Set("X-Test-Auth", "1")
	if rec := do(s.Handler(), req); rec.Code != 200 || ran != 1 || !sawPrincipal {
		t.Fatalf("authorised: status %d, ran %d, principal in context %v", rec.Code, ran, sawPrincipal)
	}
}
