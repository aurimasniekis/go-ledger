package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/logging"
)

// Access is what an API route requires (ADR-0021). Every /api route declares
// one when it is registered ([Server.HandleAPI]); there is no default, so a
// route cannot be added without deciding who may call it.
type Access struct {
	kind  accessKind
	grant authz.Grant
}

type accessKind int

const (
	accessGrant accessKind = iota + 1
	accessAuthenticated
	accessPublic
)

// Requires is a route that needs grant g. The principal must hold it (a write
// grant also allows reading the same resource).
func Requires(g authz.Grant) Access {
	if !g.Valid() {
		panic(fmt.Sprintf("httpapi: route requires unknown grant %q", g.String()))
	}
	return Access{kind: accessGrant, grant: g}
}

// Authenticated is a route any signed-in principal may call, whatever its
// grants (for example, who am I). It never changes shared state.
func Authenticated() Access { return Access{kind: accessAuthenticated} }

// Public is a route anyone may call without credentials (the API description,
// and later sign-in itself).
func Public() Access { return Access{kind: accessPublic} }

// Grant returns the required grant, if the route requires one.
func (a Access) Grant() (authz.Grant, bool) { return a.grant, a.kind == accessGrant }

// String is the access as documented in OpenAPI's x-ledger-access: the grant
// ("users:write"), "authenticated" or "public".
func (a Access) String() string {
	switch a.kind {
	case accessGrant:
		return a.grant.String()
	case accessAuthenticated:
		return "authenticated"
	case accessPublic:
		return "public"
	}
	return "invalid"
}

// Authenticator finds the principal of a request: a session cookie (task A2),
// an API token (task A4). It returns [ErrNoCredentials] when the request
// carries none of its kind, [ErrInvalidCredentials] when it carries some that
// are wrong, expired, revoked or belong to a disabled user, and any other error
// when it could not decide (the request then fails with 500). It must build the
// principal from current data on every request, so role changes, disabling and
// revocation take effect on the next request.
type Authenticator interface {
	Authenticate(r *http.Request) (authz.Principal, error)
}

// AuthenticatorFunc adapts a function to [Authenticator].
type AuthenticatorFunc func(r *http.Request) (authz.Principal, error)

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(r *http.Request) (authz.Principal, error) { return f(r) }

// Authentication outcomes.
var (
	ErrNoCredentials      = errors.New("no credentials")
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// WithAuthenticators sets the authenticators tried, in order, for every API
// route that is not [Public]. Without any, such routes answer 401.
func WithAuthenticators(a ...Authenticator) Option {
	return func(s *Server) { s.authn = append(s.authn, a...) }
}

func safeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// checkAccess panics on an access rule that would let a reader change state:
// an unsafe method guarded only by a read grant.
func checkAccess(method string, a Access) {
	if a.kind == 0 {
		panic("httpapi: API route without an access rule; use Requires, Authenticated or Public")
	}
	if g, ok := a.Grant(); ok && !safeMethod(method) && g.Action != authz.ActionWrite {
		panic(fmt.Sprintf("httpapi: %s route guarded by %s: state-changing routes must require a write grant", method, g))
	}
}

// authorize wraps h so it runs only for a principal allowed by a.
func (s *Server) authorize(a Access, h http.Handler) http.Handler {
	if a.kind == accessPublic {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authenticate(r)
		switch {
		case errors.Is(err, ErrNoCredentials):
			w.Header().Set("WWW-Authenticate", `Bearer realm="ledger"`)
			WriteError(w, r, Unauthenticated("authentication required"))
			return
		case errors.Is(err, ErrInvalidCredentials):
			w.Header().Set("WWW-Authenticate", `Bearer realm="ledger", error="invalid_token"`)
			WriteError(w, r, Unauthenticated("invalid credentials"))
			return
		case err != nil:
			WriteError(w, r, Internal(err))
			return
		}
		if g, ok := a.Grant(); ok && !p.Allows(g) {
			logging.FromContext(r.Context()).InfoContext(r.Context(), "permission denied",
				"principal_kind", string(p.Kind), "user_id", p.UserID, "grant", g.String())
			WriteError(w, r, Forbidden("this requires the "+g.String()+" grant"))
			return
		}
		h.ServeHTTP(w, r.WithContext(authz.WithPrincipal(r.Context(), p)))
	})
}

func (s *Server) authenticate(r *http.Request) (authz.Principal, error) {
	for _, a := range s.authn {
		p, err := a.Authenticate(r)
		if errors.Is(err, ErrNoCredentials) {
			continue
		}
		if err != nil {
			return authz.Principal{}, err
		}
		if p.Kind == "" || p.UserID == "" {
			return authz.Principal{}, errors.New("httpapi: authenticator returned an empty principal")
		}
		return p, nil
	}
	return authz.Principal{}, ErrNoCredentials
}
