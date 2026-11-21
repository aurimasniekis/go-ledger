package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Session cookie and CSRF header (ADR-0026).
const (
	// SessionCookie holds the session token: Secure, HttpOnly,
	// SameSite=Strict, Path=/, no Domain, expiring with the session's absolute
	// timeout. The __Host- prefix makes browsers refuse the cookie unless it
	// has exactly those Secure, Path and Domain attributes, so a sibling
	// subdomain or a plain-HTTP page cannot set or overwrite it.
	SessionCookie = "__Host-ledger_session"
	// CSRFHeader carries the session's CSRF token on every state-changing
	// request made with the session cookie.
	CSRFHeader = "X-CSRF-Token"
	// DeviceCookie marks a browser that completed a password sign-in (decision
	// D23-1b): its failed sign-ins count against the cookie instead of the
	// username. Same attributes as the session cookie, expiring with
	// auth.sign_in.device_cookie_ttl; it outlives sign-out.
	DeviceCookie = "__Host-ledger_device"
)

// Paths of the sign-in and session routes.
const (
	AuthPath     = APIPrefix + "/auth"
	SessionsPath = AuthPath + "/sessions"
)

// SessionAuth authenticates requests by the session cookie and verifies their
// CSRF token. Install it with [WithAuthenticators] and the service's Recent
// check with [WithRecentAuth].
type SessionAuth struct{ svc *auth.Service }

var (
	_ Authenticator = SessionAuth{}
	_ CSRFVerifier  = SessionAuth{}
)

// NewSessionAuth returns the session-cookie authenticator of svc.
func NewSessionAuth(svc *auth.Service) SessionAuth { return SessionAuth{svc: svc} }

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// Authenticate looks the session up on every request, so a terminated
// session fails on its next request.
func (a SessionAuth) Authenticate(r *http.Request) (authz.Principal, error) {
	tok := sessionToken(r)
	if tok == "" {
		return authz.Principal{}, ErrNoCredentials
	}
	_, p, err := a.svc.Authenticate(r.Context(), tok)
	if errors.Is(err, auth.ErrInvalidSession) {
		return authz.Principal{}, ErrInvalidCredentials
	}
	return p, err
}

// VerifyCSRF compares the X-CSRF-Token header with the token derived from
// the session cookie.
func (a SessionAuth) VerifyCSRF(r *http.Request, _ authz.Principal) bool {
	return auth.VerifyCSRF(sessionToken(r), r.Header.Get(CSRFHeader))
}

func parseTrusted(list []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return out
}

func (s *Server) isTrusted(a netip.Addr) bool {
	for _, p := range s.trusted {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// forwardingWarning logs a warning, once per process, for the first request
// that carries X-Forwarded-For from a loopback or private peer that is not in
// http.trusted_proxies. That is almost always a reverse proxy the operator
// forgot to trust: every client then has the proxy's address, so the
// per-address sign-in limit becomes one lockout shared by everyone (ADR-0026).
// The header is still ignored; only the configuration can trust it.
func (s *Server) forwardingWarning(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.warnedForwarding.Load() && r.Header.Get("X-Forwarded-For") != "" {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			if peer, err := netip.ParseAddr(host); err == nil {
				peer = peer.Unmap()
				if (peer.IsLoopback() || peer.IsPrivate()) && !s.isTrusted(peer) && s.warnedForwarding.CompareAndSwap(false, true) {
					s.log.Warn("X-Forwarded-For received from a proxy that is not in http.trusted_proxies; "+
						"the header is ignored, so every client shares the proxy's address and one sign-in limit. "+
						"Add the proxy to http.trusted_proxies", "peer", peer.String())
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ClientAddress is the address of the client: the connection's peer, or,
// when the peer is a trusted proxy (http.trusted_proxies), the right-most
// X-Forwarded-For entry that is not a trusted proxy. It keys the per-address
// sign-in limit and is shown in the session list.
func (s *Server) ClientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !s.isTrusted(peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // a malformed hop: stop trusting the chain here
		}
		if !s.isTrusted(a) {
			return a.Unmap().String()
		}
	}
	return peer.String()
}

// sessionJSON is a session as the API shows it. It never contains the token.
type sessionJSON struct {
	ID              string    `json:"id"`
	Current         bool      `json:"current"`
	CreatedAt       time.Time `json:"created_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	AuthenticatedAt time.Time `json:"authenticated_at"`
	IdleExpiresAt   time.Time `json:"idle_expires_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	UserAgent       string    `json:"user_agent"`
	Address         string    `json:"address"`
	Method          string    `json:"method"`
}

func toSessionJSON(x auth.Session, idle time.Duration, current string) sessionJSON {
	return sessionJSON{ID: x.ID, Current: x.ID == current, CreatedAt: x.CreatedAt.UTC(), LastSeenAt: x.LastSeenAt.UTC(),
		AuthenticatedAt: x.AuthenticatedAt.UTC(), IdleExpiresAt: x.IdleExpiresAt(idle).UTC(), ExpiresAt: x.ExpiresAt.UTC(),
		UserAgent: x.UserAgent, Address: x.Address, Method: x.Method}
}

// currentSessionJSON is the answer of sign-in and GET /auth/session.
type currentSessionJSON struct {
	UserID    string      `json:"user_id"`
	Session   sessionJSON `json:"session"`
	CSRFToken string      `json:"csrf_token"`
	// RecentAuthUntil is when the last password check stops counting as
	// recent; null when it already has.
	RecentAuthUntil *time.Time `json:"recent_auth_until"`
	// TwoFactor tells the UI whether to send the user to enrolment.
	TwoFactor sessionTwoFactorJSON `json:"two_factor"`
}

type sessionTwoFactorJSON struct {
	Enabled  bool `json:"enabled"`
	Required bool `json:"required"`
	// EnrollmentRequired: the session may reach only enrolment, this
	// session, re-authentication and sign-out until TOTP is enabled.
	EnrollmentRequired bool `json:"enrollment_required"`
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", Expires: expires.UTC(), MaxAge: max(1, int(maxAge/time.Second)),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func deviceToken(r *http.Request) string {
	c, err := r.Cookie(DeviceCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func setDeviceCookie(w http.ResponseWriter, token string, expires time.Time, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: DeviceCookie, Value: token, Path: "/", Expires: expires.UTC(), MaxAge: max(1, int(maxAge/time.Second)),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func clearDeviceCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: DeviceCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func invalidCredentials(status int) *Error {
	return &Error{Status: status, Code: CodeInvalidCredentials, Message: auth.ErrInvalidCredentials.Error()}
}

// authError maps the auth service's errors to API errors.
func authError(w http.ResponseWriter, err error, failStatus int) error {
	var rl *auth.RateLimitedError
	var pe *auth.PolicyError
	switch {
	case errors.Is(err, auth.ErrPasswordSignInDisabled):
		return &Error{Status: http.StatusForbidden, Code: CodePasswordSignInDisabled, Message: err.Error()}
	case errors.Is(err, auth.ErrOIDCNotConfigured):
		return NotFound(err.Error())
	case errors.Is(err, auth.ErrOIDCAlreadyLinked), errors.Is(err, auth.ErrLastSignInMethod):
		return Conflict(err.Error())
	case errors.Is(err, auth.ErrInvalidSecondFactor):
		return invalidSecondFactor(failStatus)
	case errors.Is(err, auth.ErrInvalidChallenge):
		return Unauthenticated(auth.ErrInvalidChallenge.Error())
	case errors.Is(err, auth.ErrNoPendingEnrollment), errors.Is(err, auth.ErrTwoFactorNotEnabled), errors.Is(err, auth.ErrTwoFactorRequired):
		return Conflict(err.Error())
	case errors.Is(err, auth.ErrTwoFactorUnavailable):
		return Unavailable(err.Error(), err)
	case errors.Is(err, auth.ErrUnsealable):
		return Internal(err)
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int((rl.RetryAfter+time.Second-1)/time.Second)))
		return RateLimited(rl.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		return invalidCredentials(failStatus)
	case errors.Is(err, auth.ErrInvalidSession):
		return Unauthenticated("invalid credentials")
	case errors.As(err, &pe):
		return Validation("password", pe.Message)
	}
	return err
}

func principal(r *http.Request) authz.Principal {
	p, _ := authz.FromContext(r.Context())
	return p
}

// passwordBody decodes {"password": "..."}.
func passwordBody(r *http.Request) (string, error) {
	var in struct {
		Password *string `json:"password"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		return "", err
	}
	if in.Password == nil {
		return "", Validation("password", "password is required")
	}
	return *in.Password, nil
}

type passwordSetJSON struct {
	SessionsEnded int `json:"sessions_ended"`
}

func registerSessions(s *Server, svc *auth.Service) {
	current := func(w http.ResponseWriter, r *http.Request, x auth.Session, token string, status int) {
		cfg := svc.Config()
		tf, err := svc.TwoFactorStatus(r.Context(), x.UserID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		confined, err := svc.SessionConfined(r.Context(), x)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := currentSessionJSON{UserID: x.UserID, Session: toSessionJSON(x, cfg.IdleTimeout, x.ID), CSRFToken: auth.CSRFToken(token),
			TwoFactor: sessionTwoFactorJSON{Enabled: tf.Enabled, Required: tf.Required, EnrollmentRequired: confined}}
		if svc.Recent(authz.Principal{Kind: authz.PrincipalUser, SessionID: x.ID, AuthTime: x.AuthenticatedAt}) {
			t := x.AuthenticatedAt.Add(cfg.ReauthWindow).UTC()
			out.RecentAuthUntil = &t
		}
		noStore(w)
		WriteJSON(w, status, out)
	}
	started := func(w http.ResponseWriter, r *http.Request, iss auth.Issued) {
		setSessionCookie(w, iss.Token, iss.Session.ExpiresAt, iss.Session.ExpiresAt.Sub(iss.Session.CreatedAt))
		if iss.DeviceToken != "" {
			setDeviceCookie(w, iss.DeviceToken, iss.DeviceExpiresAt, iss.DeviceExpiresAt.Sub(iss.Session.CreatedAt))
		}
		current(w, r, iss.Session, iss.Token, http.StatusOK)
	}

	// Sign-in. Every failure is the same 401 invalid_credentials, after the
	// same work; a blocked account or address is 429 with Retry-After. A
	// cross-origin browser request is refused (login CSRF). A user with TOTP
	// enabled gets 202 and a challenge for the second step, no session. The
	// device cookie, when the browser has one, chooses which failure count
	// applies (decision D23-1b); a complete sign-in replaces it.
	s.HandleAPI("POST "+AuthPath+"/sign-in", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := crossOrigin.Check(r); err != nil {
			WriteError(w, r, csrfFailed())
			return
		}
		var in struct {
			Username *string `json:"username"`
			Password *string `json:"password"`
		}
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		if in.Username == nil {
			WriteError(w, r, Validation("username", "username is required"))
			return
		}
		if in.Password == nil {
			WriteError(w, r, Validation("password", "password is required"))
			return
		}
		iss, err := svc.SignIn(r.Context(), auth.SignInInput{Username: *in.Username, Password: *in.Password,
			Address: s.ClientAddress(r), UserAgent: r.UserAgent(), DeviceToken: deviceToken(r)})
		if err != nil {
			noStore(w)
			WriteError(w, r, authError(w, err, http.StatusUnauthorized))
			return
		}
		if iss.SecondFactor != nil {
			noStore(w)
			WriteJSON(w, http.StatusAccepted, secondFactorJSON{SecondFactorRequired: true, Challenge: iss.SecondFactor.Token,
				ExpiresAt: iss.SecondFactor.ExpiresAt.UTC(), Methods: iss.SecondFactor.Methods})
			return
		}
		started(w, r, iss)
	}))

	// The second step of a sign-in with TOTP enabled: the challenge from
	// sign-in and a TOTP code or a recovery code. Wrong codes count towards
	// the sign-in limits (ADR-0030).
	s.HandleAPI("POST "+AuthPath+"/sign-in/second-factor", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := crossOrigin.Check(r); err != nil {
			WriteError(w, r, csrfFailed())
			return
		}
		var in struct {
			Challenge    *string `json:"challenge"`
			Code         string  `json:"code"`
			RecoveryCode string  `json:"recovery_code"`
		}
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		if in.Challenge == nil {
			WriteError(w, r, Validation("challenge", "challenge is required"))
			return
		}
		if (in.Code == "") == (in.RecoveryCode == "") {
			WriteError(w, r, Validation("code", "send exactly one of code and recovery_code"))
			return
		}
		iss, err := svc.CompleteSignIn(r.Context(), auth.SecondFactorInput{Challenge: *in.Challenge, Code: in.Code,
			RecoveryCode: in.RecoveryCode, Address: s.ClientAddress(r), UserAgent: r.UserAgent()})
		if err != nil {
			noStore(w)
			WriteError(w, r, authError(w, err, http.StatusUnauthorized))
			return
		}
		started(w, r, iss)
	}))

	s.HandleAPI("GET "+AuthPath+"/session", SessionOnly().DuringEnrollment(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := sessionToken(r)
		x, _, err := svc.Authenticate(r.Context(), tok)
		if err != nil {
			WriteError(w, r, authError(w, err, http.StatusUnauthorized))
			return
		}
		current(w, r, x, tok, http.StatusOK)
	}))

	s.HandleAPI("POST "+AuthPath+"/sign-out", SessionOnly().DuringEnrollment(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if err := svc.EndSession(r.Context(), p, p.SessionID); err != nil && !errors.Is(err, resource.ErrNotFound) {
			WriteError(w, r, err)
			return
		}
		clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}))

	s.HandleAPI("GET "+SessionsPath, SessionOnly(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		list, err := svc.Sessions(r.Context(), p.UserID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]sessionJSON, len(list))
		for i, x := range list {
			out[i] = toSessionJSON(x, svc.Config().IdleTimeout, p.SessionID)
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}))

	// Ending a session takes effect on its next request: every request looks
	// its session up.
	s.HandleAPI("DELETE "+SessionsPath+"/{id}", SessionOnly(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		id := r.PathValue("id")
		if !resource.ValidID(id) {
			WriteError(w, r, NotFound("no such session"))
			return
		}
		if err := svc.EndSession(r.Context(), p, id); err != nil {
			if errors.Is(err, resource.ErrNotFound) {
				err = NotFound("no such session")
			}
			WriteError(w, r, err)
			return
		}
		if id == p.SessionID {
			clearSessionCookie(w)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	s.HandleAPI("POST "+AuthPath+"/reauthenticate", SessionOnly().DuringEnrollment(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pw, err := passwordBody(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		at, err := svc.Reauthenticate(r.Context(), principal(r), pw, s.ClientAddress(r), deviceToken(r))
		if err != nil {
			noStore(w)
			WriteError(w, r, authError(w, err, http.StatusForbidden))
			return
		}
		until := at.Add(svc.Config().ReauthWindow).UTC()
		noStore(w)
		WriteJSON(w, http.StatusOK, map[string]time.Time{"authenticated_at": at.UTC(), "recent_auth_until": until})
	}))

	// Changing one's own password ends every other session of the user and
	// revokes the user's device cookies, this browser's included.
	s.HandleAPI("PUT "+AuthPath+"/password", SessionOnly().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pw, err := passwordBody(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		p := principal(r)
		n, err := svc.SetPassword(r.Context(), p, false, p.UserID, pw, auth.ByUser)
		if err != nil {
			WriteError(w, r, authError(w, err, http.StatusForbidden))
			return
		}
		clearDeviceCookie(w)
		WriteJSON(w, http.StatusOK, passwordSetJSON{SessionsEnded: n})
	}))
}

// registerUserPassword is an administrator setting a user's password (user
// management): it ends every session of that user (except the caller's own
// current one, when they set their own).
func registerUserPassword(s *Server, svc *auth.Service) {
	s.HandleAPI("PUT "+UsersPath+"/{id}/password", Requires(grantUsersWrite).WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pw, err := passwordBody(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		p := principal(r)
		if err := refuseUnlessRootToken(p, "setting a password"); err != nil {
			WriteError(w, r, err)
			return
		}
		by := auth.ByAdministrator
		if p.Kind == authz.PrincipalUser && p.UserID == r.PathValue("id") {
			by = auth.ByUser
		}
		n, err := svc.SetPassword(r.Context(), p, false, r.PathValue("id"), pw, by)
		if err != nil {
			WriteError(w, r, authError(w, err, http.StatusForbidden))
			return
		}
		WriteJSON(w, http.StatusOK, passwordSetJSON{SessionsEnded: n})
	}))
}
