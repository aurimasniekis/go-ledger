package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// fixed authenticates every request carrying X-Test-Auth.
func fixed(p authz.Principal, err error) Authenticator {
	return AuthenticatorFunc(func(r *http.Request) (authz.Principal, error) {
		if r.Header.Get("X-Test-Auth") == "" {
			return authz.Principal{}, ErrNoCredentials
		}
		return p, err
	})
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
