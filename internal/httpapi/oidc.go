package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/oidc"
)

// Single sign-on with one OpenID Connect provider (task A6, ADR-0033).
//
// The browser legs (authorize, callback, recovery sign-in, the sign-in page
// redirect) answer with redirects, never JSON: a failure sends the browser to
// the sign-in page, or back where a link or re-authentication started, with
// an error code in the query (oidc_error, recovery_error). The JSON routes
// (methods, link status, starting a link or a re-authentication) follow the
// API's usual rules.

// Paths of the single-sign-on routes.
const (
	OIDCPath          = AuthPath + "/oidc"
	OIDCAuthorizePath = OIDCPath + "/authorize"
	// SignInPagePath is the UI's sign-in page. When password sign-in is
	// disabled, the server answers it with a redirect to the provider.
	SignInPagePath = "/sign-in"
	// OIDCCookie binds an authorization request to the browser that started
	// it: it holds the request's state. SameSite=Lax, not Strict, because the
	// callback is a top-level navigation from the provider's site.
	OIDCCookie = "__Host-ledger_oidc"
)

// OIDCRelyingParty is the protocol side ([oidc.Client]).
type OIDCRelyingParty interface {
	AuthCodeURL(r *http.Request, a oidc.AuthRequest) (string, error)
	Exchange(r *http.Request, code, verifier string, want oidc.Expect) (oidc.Identity, error)
}

// OIDCDeps configure single sign-on; nil (or a nil Client) means it is not
// configured, and its routes answer 404 (browser routes: a redirect with
// oidc_error=not_configured).
type OIDCDeps struct {
	Client       OIDCRelyingParty
	Transactions *oidc.Transactions
	// Name is the provider's name shown on the sign-in page.
	Name string
}

// RelyingParty adapts an [oidc.Client] to [OIDCRelyingParty].
func RelyingParty(c *oidc.Client) OIDCRelyingParty { return relyingParty{c} }

type relyingParty struct{ c *oidc.Client }

func (p relyingParty) AuthCodeURL(r *http.Request, a oidc.AuthRequest) (string, error) {
	return p.c.AuthCodeURL(r.Context(), a)
}

func (p relyingParty) Exchange(r *http.Request, code, verifier string, want oidc.Expect) (oidc.Identity, error) {
	return p.c.Exchange(r.Context(), code, verifier, want)
}

// Error codes put in the oidc_error query parameter of a failed browser leg.
const (
	oidcErrNotConfigured = "not_configured"
	oidcErrUnavailable   = "provider_unavailable"
	oidcErrState         = "invalid_state"
	oidcErrDenied        = "access_denied"
	oidcErrResponse      = "invalid_response"
	oidcErrNotLinked     = "not_linked"
	oidcErrSignIn        = "sign_in_failed"
	oidcErrAlreadyLinked = "already_linked"
	oidcErrSession       = "session_ended"
	oidcErrMismatch      = "identity_mismatch"
	oidcErrInternal      = "internal_error"
)

// returnPath reads a same-origin path to come back to: it must start with
// one slash, not lead into /api, and hold no control characters. Anything
// else becomes "/".
func returnPath(s string) string {
	if s == "" || len(s) > 512 || s[0] != '/' || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") ||
		strings.ContainsAny(s, "\\\r\n\t") || s == "/api" || strings.HasPrefix(s, "/api/") {
		return "/"
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Fragment != "" {
		return "/"
	}
	return s
}

// withQuery appends key=value to the path p.
func withQuery(p, key, value string) string {
	u, err := url.Parse(p)
	if err != nil {
		u = &url.URL{Path: "/"}
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

func redirect(w http.ResponseWriter, r *http.Request, to string) {
	noStore(w)
	// The callback URL holds the authorization code: never leak it onwards.
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func setOIDCCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{Name: OIDCCookie, Value: state, Path: "/", MaxAge: int(oidc.TransactionTTL / time.Second),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearOIDCCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: OIDCCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

type authorizationJSON struct {
	AuthorizationURL string `json:"authorization_url"`
}

type signInMethodsJSON struct {
	Password struct {
		Enabled bool `json:"enabled"`
	} `json:"password"`
	OIDC struct {
		Enabled      bool    `json:"enabled"`
		Name         *string `json:"name"`
		AuthorizeURL *string `json:"authorize_url"`
	} `json:"oidc"`
}

type oidcLinkJSON struct {
	Configured bool       `json:"configured"`
	Linked     bool       `json:"linked"`
	Provider   *string    `json:"provider"`
	Subject    *string    `json:"subject"`
	Email      *string    `json:"email"`
	CreatedAt  *time.Time `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func registerOIDC(s *Server, svc *auth.Service, d *OIDCDeps) {
	configured := d != nil && d.Client != nil && d.Transactions != nil && svc.Config().OIDC.Enabled
	name := ""
	if configured {
		name = d.Name
	}
	log := func(r *http.Request, msg string, args ...any) {
		logging.FromContext(r.Context()).InfoContext(r.Context(), msg, args...)
	}

	// begin stores a transaction and answers with the provider's URL: as a
	// redirect (sign-in) or as JSON (link and re-authentication, started by
	// the UI with a CSRF-protected request).
	begin := func(w http.ResponseWriter, r *http.Request, t oidc.Transaction) (string, error) {
		state, t := d.Transactions.Begin(t)
		u, err := d.Client.AuthCodeURL(r, oidc.AuthRequest{State: state, Nonce: t.Nonce, Verifier: t.Verifier, Reauth: t.Mode == oidc.ModeReauth})
		if err != nil {
			d.Transactions.Take(state, state)
			return "", err
		}
		setOIDCCookie(w, state)
		return u, nil
	}

	s.HandleAPI("GET "+AuthPath+"/methods", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out signInMethodsJSON
		out.Password.Enabled = !svc.Config().PasswordSignInDisabled
		if configured {
			out.OIDC.Enabled = true
			u := OIDCAuthorizePath
			out.OIDC.Name, out.OIDC.AuthorizeURL = &name, &u
		}
		WriteJSON(w, http.StatusOK, out)
	}))

	s.HandleAPI("GET "+OIDCAuthorizePath, Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := withQuery(SignInPagePath, "oidc_error", oidcErrNotConfigured)
		if !configured {
			redirect(w, r, fail)
			return
		}
		u, err := begin(w, r, oidc.Transaction{Mode: oidc.ModeSignIn, ReturnTo: returnPath(r.URL.Query().Get("return_to"))})
		if err != nil {
			logging.FromContext(r.Context()).WarnContext(r.Context(), "single sign-on: the identity provider is unavailable", "error", err.Error())
			redirect(w, r, withQuery(SignInPagePath, "oidc_error", oidcErrUnavailable))
			return
		}
		redirect(w, r, u)
	}))

	s.HandleAPI("GET "+config.OIDCCallbackPath, Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var cookie string
		if c, err := r.Cookie(OIDCCookie); err == nil {
			cookie = c.Value
		}
		clearOIDCCookie(w)
		if !configured {
			redirect(w, r, withQuery(SignInPagePath, "oidc_error", oidcErrNotConfigured))
			return
		}
		t, ok := d.Transactions.Take(q.Get("state"), cookie)
		if !ok {
			// Unknown, expired, replayed, or started in another browser.
			log(r, "single sign-on: callback with an unknown or replayed state")
			redirect(w, r, withQuery(SignInPagePath, "oidc_error", oidcErrState))
			return
		}
		back := func(code string) {
			if t.Mode == oidc.ModeSignIn {
				redirect(w, r, withQuery(SignInPagePath, "oidc_error", code))
				return
			}
			redirect(w, r, withQuery(t.ReturnTo, "oidc_error", code))
		}
		if e := q.Get("error"); e != "" {
			log(r, "single sign-on: the identity provider returned an error", "mode", t.Mode, "provider_error", truncateASCII(e, 64))
			back(oidcErrDenied)
			return
		}
		id, err := d.Client.Exchange(r, q.Get("code"), t.Verifier, oidc.Expect{Nonce: t.Nonce, Started: t.CreatedAt, Reauth: t.Mode == oidc.ModeReauth})
		var ve *oidc.ValidationError
		switch {
		case errors.As(err, &ve):
			logging.FromContext(r.Context()).WarnContext(r.Context(), "single sign-on: the identity provider's response was rejected", "mode", t.Mode, "check", ve.Check, "error", err.Error())
			if t.Mode == oidc.ModeSignIn {
				if err := svc.OIDCResponseRejected(r.Context()); err != nil {
					back(oidcErrInternal)
					return
				}
			}
			back(oidcErrResponse)
			return
		case errors.Is(err, oidc.ErrUnavailable):
			logging.FromContext(r.Context()).WarnContext(r.Context(), "single sign-on: the identity provider is unavailable", "error", err.Error())
			back(oidcErrUnavailable)
			return
		case err != nil:
			logging.FromContext(r.Context()).ErrorContext(r.Context(), "single sign-on: callback failed", "error", err.Error())
			back(oidcErrInternal)
			return
		}
		ident := auth.OIDCIdentity{Issuer: id.Issuer, Subject: id.Subject, Email: id.Email, PreferredUsername: id.PreferredUsername, Name: id.Name}
		switch t.Mode {
		case oidc.ModeSignIn:
			iss, err := svc.SignInOIDC(r.Context(), auth.OIDCSignInInput{Identity: ident, Address: s.ClientAddress(r), UserAgent: r.UserAgent()})
			switch {
			case errors.Is(err, auth.ErrOIDCNotLinked):
				back(oidcErrNotLinked)
			case errors.Is(err, auth.ErrInvalidCredentials):
				back(oidcErrSignIn)
			case err != nil:
				logging.FromContext(r.Context()).ErrorContext(r.Context(), "single sign-on: sign-in failed", "error", err.Error())
				back(oidcErrInternal)
			case iss.SecondFactor != nil:
				// The challenge goes in the fragment, which browsers never send
				// to a server nor put in a Referer header.
				redirect(w, r, SignInPagePath+"#second_factor="+url.QueryEscape(iss.SecondFactor.Token))
			default:
				setSessionCookie(w, iss.Token, iss.Session.ExpiresAt, iss.Session.ExpiresAt.Sub(iss.Session.CreatedAt))
				redirect(w, r, t.ReturnTo)
			}
		case oidc.ModeLink:
			_, err := svc.LinkOIDC(r.Context(), t.UserID, t.SessionID, ident)
			switch {
			case errors.Is(err, auth.ErrOIDCAlreadyLinked):
				back(oidcErrAlreadyLinked)
			case errors.Is(err, auth.ErrInvalidSession):
				back(oidcErrSession)
			case err != nil:
				logging.FromContext(r.Context()).ErrorContext(r.Context(), "single sign-on: linking failed", "error", err.Error())
				back(oidcErrInternal)
			default:
				redirect(w, r, withQuery(t.ReturnTo, "oidc", "linked"))
			}
		case oidc.ModeReauth:
			_, err := svc.ReauthenticateOIDC(r.Context(), t.UserID, t.SessionID, ident)
			switch {
			case errors.Is(err, auth.ErrOIDCIdentityMismatch):
				back(oidcErrMismatch)
			case errors.Is(err, auth.ErrInvalidSession):
				back(oidcErrSession)
			case err != nil:
				logging.FromContext(r.Context()).ErrorContext(r.Context(), "single sign-on: re-authentication failed", "error", err.Error())
				back(oidcErrInternal)
			default:
				redirect(w, r, withQuery(t.ReturnTo, "oidc", "reauthenticated"))
			}
		default:
			back(oidcErrInternal)
		}
	}))

	// The user's link. It is shown even when single sign-on is no longer
	// configured, so a stale link stays visible.
	s.HandleAPI("GET "+OIDCPath+"/link", SessionOnly(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		out := oidcLinkJSON{Configured: configured}
		l, err := svc.OIDCLinkOf(r.Context(), p.UserID)
		switch {
		case errors.Is(err, resource.ErrNotFound):
		case err != nil:
			WriteError(w, r, err)
			return
		default:
			out.Linked, out.Subject, out.CreatedAt, out.LastUsedAt = true, &l.Subject, &l.CreatedAt, l.LastUsedAt
			if l.Email != "" {
				out.Email = &l.Email
			}
			if configured {
				out.Provider = &name
			}
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, out)
	}))

	// Linking starts here, with a recent password (or OIDC) check; the
	// browser then goes to authorization_url and comes back to the callback.
	s.HandleAPI("POST "+OIDCPath+"/link", SessionOnly().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !configured {
			WriteError(w, r, NotFound(auth.ErrOIDCNotConfigured.Error()))
			return
		}
		ret, err := returnBody(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		p := principal(r)
		if _, err := svc.OIDCLinkOf(r.Context(), p.UserID); err == nil {
			WriteError(w, r, Conflict(auth.ErrOIDCAlreadyLinked.Error()))
			return
		} else if !errors.Is(err, resource.ErrNotFound) {
			WriteError(w, r, err)
			return
		}
		u, err := begin(w, r, oidc.Transaction{Mode: oidc.ModeLink, UserID: p.UserID, SessionID: p.SessionID, ReturnTo: ret})
		if err != nil {
			WriteError(w, r, Unavailable("the identity provider is unavailable", err))
			return
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, authorizationJSON{AuthorizationURL: u})
	}))

	s.HandleAPI("DELETE "+OIDCPath+"/link", SessionOnly().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := svc.UnlinkOIDC(r.Context(), principal(r), false, "")
		if errors.Is(err, resource.ErrNotFound) {
			WriteError(w, r, NotFound("no single-sign-on link"))
			return
		}
		if err != nil {
			WriteError(w, r, authError(w, err, http.StatusForbidden))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// Re-authentication with the provider, for users who sign in with OIDC
	// (and the only way when password sign-in is disabled).
	s.HandleAPI("POST "+OIDCPath+"/reauthenticate", SessionOnly().DuringEnrollment(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !configured {
			WriteError(w, r, NotFound(auth.ErrOIDCNotConfigured.Error()))
			return
		}
		ret, err := returnBody(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		p := principal(r)
		if _, err := svc.OIDCLinkOf(r.Context(), p.UserID); errors.Is(err, resource.ErrNotFound) {
			WriteError(w, r, Conflict("the account has no single-sign-on link"))
			return
		} else if err != nil {
			WriteError(w, r, err)
			return
		}
		u, err := begin(w, r, oidc.Transaction{Mode: oidc.ModeReauth, UserID: p.UserID, SessionID: p.SessionID, ReturnTo: ret})
		if err != nil {
			WriteError(w, r, Unavailable("the identity provider is unavailable", err))
			return
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, authorizationJSON{AuthorizationURL: u})
	}))

	// Break-glass (decision D16): a one-time sign-in issued on the host with
	// ledger auth recovery-sign-in. The token is in the query so the printed
	// link works in any browser; Ledger never logs query strings, and the
	// token is consumed by its first use.
	s.HandleAPI("GET "+AuthPath+"/recovery-sign-in", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iss, err := svc.RecoverySignIn(r.Context(), auth.RecoverySignInInput{Token: r.URL.Query().Get("token"), Address: s.ClientAddress(r), UserAgent: r.UserAgent()})
		if err != nil {
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				logging.FromContext(r.Context()).ErrorContext(r.Context(), "recovery sign-in failed", "error", err.Error())
			}
			redirect(w, r, withQuery(SignInPagePath, "recovery_error", "invalid_or_expired"))
			return
		}
		setSessionCookie(w, iss.Token, iss.Session.ExpiresAt, iss.Session.ExpiresAt.Sub(iss.Session.CreatedAt))
		redirect(w, r, "/")
	}))

	// The UI's sign-in page goes straight to the provider when password
	// sign-in is disabled, unless it is showing the outcome of an attempt.
	s.Handle("GET "+SignInPagePath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if configured && svc.Config().PasswordSignInDisabled && !q.Has("oidc_error") && !q.Has("recovery_error") {
			to := OIDCAuthorizePath
			if ret := returnPath(q.Get("return_to")); ret != "/" {
				to = withQuery(to, "return_to", ret)
			}
			redirect(w, r, to)
			return
		}
		if s.ui != nil {
			s.ui.ServeHTTP(w, r)
			return
		}
		WriteError(w, r, NotFound("no such resource"))
	}))
}

// returnBody reads an optional {"return_to": "/path"} body.
func returnBody(r *http.Request) (string, error) {
	if r.ContentLength == 0 {
		return "/", nil
	}
	var in struct {
		ReturnTo string `json:"return_to"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		return "", err
	}
	return returnPath(in.ReturnTo), nil
}

func truncateASCII(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < n; i++ {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return b.String()
}
