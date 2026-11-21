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
	// recent: a signed-in user must have proved their password within the
	// re-authentication window (ADR-0026). API tokens are exempt by design.
	recent bool
	// enrollment: the route stays reachable for a session confined to
	// enrolling a second factor (auth.require_2fa, ADR-0030).
	enrollment bool
}

type accessKind int

const (
	accessGrant accessKind = iota + 1
	accessAuthenticated
	accessPublic
	accessSession
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
// sign-in itself).
func Public() Access { return Access{kind: accessPublic} }

// SessionOnly is a route for a user signed in with a session cookie, acting
// on their own session or credentials (sign-out, their session list, their
// password), whatever their role. API tokens are refused with 403: a token has
// no session and cannot prove a password. It is the one access rule besides a
// write grant that may guard a state-changing method, because it only ever
// touches the caller's own sign-in state (ADR-0026).
func SessionOnly() Access { return Access{kind: accessSession} }

// WithRecentAuth returns a with the requirement that a signed-in user proved
// their password recently (POST /auth/reauthenticate). Requests made with an
// API token are exempt: tokens skip interactive steps by design, and their
// state-changing requests are audited as token.write_used.
func (a Access) WithRecentAuth() Access {
	a.recent = true
	return a
}

// RecentAuth reports whether the route requires recent authentication.
func (a Access) RecentAuth() bool { return a.recent }

// DuringEnrollment returns a with the route reachable by a session whose user
// must enrol a second factor first (auth.require_2fa). Every other non-public
// route refuses such a session with 403 two_factor_enrollment_required. Only
// [SessionOnly] routes may be marked: enrolment, the current session,
// re-authentication and sign-out (ADR-0030).
func (a Access) DuringEnrollment() Access {
	a.enrollment = true
	return a
}

// AllowedDuringEnrollment reports whether the route is reachable during
// required enrolment (OpenAPI x-ledger-during-enrollment).
func (a Access) AllowedDuringEnrollment() bool { return a.enrollment }

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
	case accessSession:
		return "session"
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

// CSRFVerifier is implemented by an authenticator whose credentials a browser
// sends by itself (a session cookie). For every state-changing request of a
// user principal, the server asks the authenticator that produced it to check
// the request's CSRF token; an authenticator without this method cannot
// authorize such requests at all (fail closed).
type CSRFVerifier interface {
	VerifyCSRF(r *http.Request, p authz.Principal) bool
}

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

// WithRecentAuth sets the check behind [Access.WithRecentAuth] for user
// principals (auth.Service.Recent). Without it, such routes refuse every user
// principal with 403 reauthentication_required (fail closed).
func WithRecentAuth(recent func(authz.Principal) bool) Option {
	return func(s *Server) { s.recentAuth = recent }
}

// crossOrigin rejects browser requests that come from another origin, using
// the Sec-Fetch-Site and Origin headers (net/http's CrossOriginProtection).
// Requests without either header (non-browser clients) pass; the CSRF token
// still guards cookie-authenticated writes.
var crossOrigin = http.NewCrossOriginProtection()

func safeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// checkAccess panics on an access rule that would let a reader change state:
// an unsafe method guarded only by a read grant or by Authenticated (which any
// read-only user or narrow token passes). A state-changing route a signed-in
// principal must reach whatever its grants (sign-out, task A2) will need an
// explicit, named exception here.
func checkAccess(method string, a Access) {
	if a.kind == 0 {
		panic("httpapi: API route without an access rule; use Requires, Authenticated or Public")
	}
	if a.recent && a.kind != accessGrant && a.kind != accessSession {
		panic("httpapi: recent authentication applies to grant and session routes only")
	}
	if a.enrollment && a.kind != accessSession {
		panic("httpapi: only session routes may be reachable during required two-factor enrolment")
	}
	if a.kind == accessAuthenticated && !safeMethod(method) {
		panic(fmt.Sprintf("httpapi: %s route is Authenticated: state-changing routes must require a write grant", method))
	}
	if g, ok := a.Grant(); ok && !safeMethod(method) && g.Action != authz.ActionWrite {
		panic(fmt.Sprintf("httpapi: %s route guarded by %s: state-changing routes must require a write grant", method, g))
	}
}

// authorize wraps h so it runs only for a principal allowed by a.
func (s *Server) authorize(pattern string, a Access, h http.Handler) http.Handler {
	if a.kind == accessPublic {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, by, err := s.authenticate(r)
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
		if a.kind == accessSession && (p.Kind != authz.PrincipalUser || p.SessionID == "") {
			WriteError(w, r, Forbidden("this requires a signed-in session; API tokens cannot use it"))
			return
		}
		// Required 2FA (ADR-0030): a session whose user has not enrolled a
		// second factor reaches only the routes marked DuringEnrollment.
		if p.EnrollmentRequired && !a.enrollment {
			WriteError(w, r, enrollmentRequired())
			return
		}
		// CSRF (ADR-0026): every state-changing request of a signed-in user
		// must come from Ledger's own origin and carry the session's CSRF
		// token. Bearer-token requests are exempt by design: a browser never
		// attaches them on its own.
		if !safeMethod(r.Method) && p.Kind == authz.PrincipalUser {
			v, ok := by.(CSRFVerifier)
			if err := crossOrigin.Check(r); err != nil || !ok || !v.VerifyCSRF(r, p) {
				logging.FromContext(r.Context()).InfoContext(r.Context(), "csrf check failed", "user_id", p.UserID)
				WriteError(w, r, csrfFailed())
				return
			}
		}
		if g, ok := a.Grant(); ok && !p.Allows(g) {
			logging.FromContext(r.Context()).InfoContext(r.Context(), "permission denied",
				"principal_kind", string(p.Kind), "user_id", p.UserID, "grant", g.String())
			WriteError(w, r, Forbidden("this requires the "+g.String()+" grant"))
			return
		}
		if a.recent && p.Kind == authz.PrincipalUser && (s.recentAuth == nil || !s.recentAuth(p)) {
			WriteError(w, r, reauthRequired())
			return
		}
		if !s.auditTokenWrite(w, r, p, pattern) {
			return
		}
		h.ServeHTTP(w, r.WithContext(authz.WithPrincipal(r.Context(), p)))
	})
}

// authenticate returns the principal and the authenticator that found it.
func (s *Server) authenticate(r *http.Request) (authz.Principal, Authenticator, error) {
	for _, a := range s.authn {
		p, err := a.Authenticate(r)
		if errors.Is(err, ErrNoCredentials) {
			continue
		}
		if err != nil {
			return authz.Principal{}, nil, err
		}
		if p.Kind == "" || p.UserID == "" {
			return authz.Principal{}, nil, errors.New("httpapi: authenticator returned an empty principal")
		}
		return p, a, nil
	}
	return authz.Principal{}, nil, ErrNoCredentials
}
