package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
)

// Paths of the API token routes (ADR-0029). A user manages their own tokens
// under /auth/tokens with a session; /tokens is everyone's, for holders of
// the tokens grants.
const (
	MyTokensPath = AuthPath + "/tokens"
	TokensPath   = APIPrefix + "/tokens"
)

var (
	grantTokensRead  = authz.G(authz.ResourceTokens, authz.ActionRead)
	grantTokensWrite = authz.G(authz.ResourceTokens, authz.ActionWrite)
)

// TokenAuth authenticates requests by "Authorization: Bearer ldgr_…". Install
// it with [WithAuthenticators] before the session cookie, and together with
// [WithAuditRecorder]: [New] refuses a server that would let tokens write
// without auditing it.
type TokenAuth struct{ svc *tokens.Service }

var _ Authenticator = TokenAuth{}

// NewTokenAuth returns the bearer-token authenticator of svc.
func NewTokenAuth(svc *tokens.Service) TokenAuth { return TokenAuth{svc: svc} }

// Authenticate reads the Authorization header. Without one it returns
// [ErrNoCredentials] so the next authenticator (the session cookie) can try;
// any Authorization header that is not one valid, active bearer token is
// [ErrInvalidCredentials], never a fall-back to the cookie. The token is
// looked up on every request, so revocation applies to the next one. Nothing
// here logs or returns the token.
func (a TokenAuth) Authenticate(r *http.Request) (authz.Principal, error) {
	h := r.Header.Values("Authorization")
	if len(h) == 0 {
		return authz.Principal{}, ErrNoCredentials
	}
	if len(h) != 1 {
		return authz.Principal{}, ErrInvalidCredentials
	}
	scheme, cred, ok := strings.Cut(h[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return authz.Principal{}, ErrInvalidCredentials
	}
	p, err := a.svc.Authenticate(r.Context(), strings.TrimSpace(cred))
	if errors.Is(err, tokens.ErrInvalidToken) {
		return authz.Principal{}, ErrInvalidCredentials
	}
	return p, err
}

// tokenJSON is a token as the API shows it. It never contains the token.
type tokenJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnerID string `json:"owner_id"`
	Root    bool   `json:"root"`
	// Grants are the token's own grants (empty for a root token); requests
	// get only those the owner's current role still allows.
	Grants        []string   `json:"grants"`
	Hint          string     `json:"hint"`
	DisplayPrefix string     `json:"prefix"`
	LastChars     string     `json:"last_chars"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
	// ParentTokenID is the token that created this one through the API; null
	// when there is none (made on the host or by bootstrap, or before parents were recorded).
	ParentTokenID *string `json:"parent_token_id"`
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toTokenJSON(t tokens.Token, now time.Time) tokenJSON {
	grants := t.Grants.Strings()
	return tokenJSON{ID: t.ID, Name: t.Name, OwnerID: t.OwnerID, Root: t.Root, Grants: grants, Hint: t.Hint(),
		DisplayPrefix: t.DisplayPrefix, LastChars: t.LastChars, Status: t.Status(now), CreatedAt: t.CreatedAt.UTC(),
		ExpiresAt: utcPtr(t.ExpiresAt), LastUsedAt: utcPtr(t.LastUsedAt), RevokedAt: utcPtr(t.RevokedAt), ParentTokenID: nonEmpty(t.ParentTokenID)}
}

// createdTokenJSON answers token creation: the only time the token is shown.
type createdTokenJSON struct {
	// Secret is the token. It is never shown again.
	Secret string    `json:"secret"`
	Token  tokenJSON `json:"token"`
	// Warning explains a root token's power; null otherwise.
	Warning *string `json:"warning"`
}

type createTokenBody struct {
	Name      *string    `json:"name"`
	Grants    []string   `json:"grants"`
	Root      bool       `json:"root"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// tokenError maps the token service's errors to API errors.
func tokenError(err error) error {
	var fe *tokens.FieldError
	var ee *tokens.EscalationError
	switch {
	case errors.As(err, &fe):
		return Validation(fe.Field, fe.Message)
	case errors.As(err, &ee):
		return Forbidden(ee.Error())
	case errors.Is(err, tokens.ErrNotUser), errors.Is(err, tokens.ErrTokenCannotCreate):
		return Forbidden(err.Error())
	case errors.Is(err, resource.ErrNotFound):
		return NotFound("no such token")
	}
	return err
}

func tokenList(w http.ResponseWriter, r *http.Request, svc *tokens.Service, q ListQuery, owner string) {
	list, err := svc.List(r.Context(), tokens.ListFilter{OwnerID: owner, After: q.After, Limit: q.Limit})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	now := svc.Now()
	out := make([]tokenJSON, len(list))
	for i, t := range list {
		out[i] = toTokenJSON(t, now)
	}
	WriteJSON(w, http.StatusOK, NewPage(out, q, func(t tokenJSON) string { return t.ID }))
}

var tokensListSpec = ListSpec{Filters: map[string]FilterSpec{"owner_id": {Ops: []FilterOp{OpEq}, Check: checkULID}}}

func registerTokens(s *Server, svc *tokens.Service) {
	// The signed-in user's own tokens, whatever their role. A token cannot
	// list or revoke tokens here (SessionOnly). Creating one needs a session
	// with a recent password check, or a root token of the root account
	// (decision D25), which creates tokens owned by the root account.
	s.HandleAPI("GET "+MyTokensPath, SessionOnly(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, ListSpec{})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		tokenList(w, r, svc, q, principal(r).UserID)
	}))

	s.HandleAPI("POST "+MyTokensPath, SessionOrTokenCreator().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in createTokenBody
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		if in.Name == nil {
			WriteError(w, r, Validation("name", "name is required"))
			return
		}
		var grants authz.Grants
		for _, v := range in.Grants {
			g, err := authz.ParseGrant(v)
			if err != nil {
				WriteError(w, r, Validation("grants", "unknown grant; see GET /grants"))
				return
			}
			grants = append(grants, g)
		}
		p := principal(r)
		iss, err := svc.Create(r.Context(), audit.ActorOf(p), p, tokens.New{Name: *in.Name, Root: in.Root, Grants: grants, ExpiresAt: in.ExpiresAt})
		if err != nil {
			WriteError(w, r, tokenError(err))
			return
		}
		out := createdTokenJSON{Secret: iss.Secret, Token: toTokenJSON(iss.Token, svc.Now())}
		if iss.Token.Root {
			warning := tokens.RootWarning
			out.Warning = &warning
		}
		noStore(w)
		w.Header().Set("Location", MyTokensPath+"/"+iss.Token.ID)
		WriteJSON(w, http.StatusCreated, out)
	}))

	s.HandleAPI("GET "+MyTokensPath+"/{id}", SessionOnly(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getToken(w, r, svc, principal(r).UserID)
	}))

	s.HandleAPI("DELETE "+MyTokensPath+"/{id}", SessionOnly(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeToken(w, r, svc, principal(r).UserID)
	}))

	// Everyone's tokens, for administrators and tokens holding the grants.
	s.HandleAPI("GET "+TokensPath, Requires(grantTokensRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, tokensListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		owner := q.Filter("owner_id", OpEq)
		if len(owner) > 1 {
			WriteError(w, r, Validation("owner_id", "this filter must be given once"))
			return
		}
		tokenList(w, r, svc, q, strings.Join(owner, ""))
	}))

	s.HandleAPI("GET "+TokensPath+"/{id}", Requires(grantTokensRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getToken(w, r, svc, "")
	}))

	s.HandleAPI("DELETE "+TokensPath+"/{id}", Requires(grantTokensWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Decision D34: only the root token itself or a session may revoke
		// the root token of the root account (which ends its children too).
		if p := principal(r); p.Kind == authz.PrincipalToken && !p.CanCreateTokens {
			protected, err := svc.IsProtectedRoot(r.Context(), r.PathValue("id"))
			if err != nil {
				WriteError(w, r, err)
				return
			}
			if protected {
				WriteError(w, r, rootTokenRequired("revoking the root token"))
				return
			}
		}
		revokeToken(w, r, svc, "")
	}))
}

func getToken(w http.ResponseWriter, r *http.Request, svc *tokens.Service, owner string) {
	id := r.PathValue("id")
	if !resource.ValidID(id) {
		WriteError(w, r, NotFound("no such token"))
		return
	}
	t, err := svc.Get(r.Context(), id, owner)
	if err != nil {
		WriteError(w, r, tokenError(err))
		return
	}
	WriteJSON(w, http.StatusOK, toTokenJSON(t, svc.Now()))
}

// revokeToken revokes at once: the token's next request is 401. Revoking a
// revoked token is a no-op (204, no new audit entry).
func revokeToken(w http.ResponseWriter, r *http.Request, svc *tokens.Service, owner string) {
	id := r.PathValue("id")
	if !resource.ValidID(id) {
		WriteError(w, r, NotFound("no such token"))
		return
	}
	if err := svc.Revoke(r.Context(), audit.ActorOf(principal(r)), id, owner); err != nil {
		WriteError(w, r, tokenError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
