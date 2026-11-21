package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// Grants the user and authorization routes require.
var (
	grantUsersRead  = authz.G(authz.ResourceUsers, authz.ActionRead)
	grantUsersWrite = authz.G(authz.ResourceUsers, authz.ActionWrite)
)

// principalJSON is GET /me.
type principalJSON struct {
	Kind    authz.PrincipalKind `json:"kind"`
	UserID  string              `json:"user_id"`
	TokenID *string             `json:"token_id"`
	Role    authz.Role          `json:"role"`
	Grants  []string            `json:"grants"`
}

type resourceJSON struct {
	Name        authz.Resource `json:"name"`
	Description string         `json:"description"`
	Actions     []authz.Action `json:"actions"`
	AdminOnly   bool           `json:"admin_only"`
}

type roleJSON struct {
	Name   authz.Role `json:"name"`
	Grants []string   `json:"grants"`
}

type vocabularyJSON struct {
	Resources []resourceJSON `json:"resources"`
	Roles     []roleJSON     `json:"roles"`
}

func registerAuthz(s *Server) {
	s.HandleAPI("GET "+APIPrefix+"/me", Authenticated(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := authz.FromContext(r.Context())
		out := principalJSON{Kind: p.Kind, UserID: p.UserID, Role: p.Role, Grants: p.Grants.Strings()}
		if p.TokenID != "" {
			out.TokenID = &p.TokenID
		}
		WriteJSON(w, http.StatusOK, out)
	}))
	s.HandleAPI("GET "+APIPrefix+"/grants", Authenticated(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var out vocabularyJSON
		for _, ri := range authz.Resources() {
			out.Resources = append(out.Resources, resourceJSON{Name: ri.Name, Description: ri.Description, Actions: ri.Actions, AdminOnly: ri.AdminOnly})
		}
		for _, role := range authz.Roles() {
			out.Roles = append(out.Roles, roleJSON{Name: role, Grants: role.Grants().Strings()})
		}
		WriteJSON(w, http.StatusOK, out)
	}))
}

// userJSON is the API representation of a user.
type userJSON struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName *string    `json:"display_name"`
	Role        authz.Role `json:"role"`
	Disabled    bool       `json:"disabled"`
	DisabledAt  *time.Time `json:"disabled_at"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toUserJSON(u users.User) userJSON {
	return userJSON{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role, Disabled: u.Disabled,
		DisabledAt: u.DisabledAt, Version: u.Version, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

// UsersPath is the user collection.
const UsersPath = APIPrefix + "/users"

var usersListSpec = ListSpec{Filters: map[string]FilterSpec{
	"role": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if err := users.ValidateRole(authz.Role(v)); err != nil {
			var fe *users.FieldError
			errors.As(err, &fe)
			return errors.New(fe.Message)
		}
		return nil
	}},
	"disabled": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
		if v != "true" && v != "false" {
			return errors.New("disabled must be true or false")
		}
		return nil
	}},
	"username": {Ops: []FilterOp{OpPrefix}},
}}

// userError maps the users package's errors to API errors.
func userError(err error) error {
	var fe *users.FieldError
	switch {
	case errors.As(err, &fe):
		return Validation(fe.Field, fe.Message)
	case errors.Is(err, users.ErrUsernameTaken):
		return &Error{Status: http.StatusConflict, Code: CodeConflict, Message: users.ErrUsernameTaken.Error(), Field: "username"}
	case errors.Is(err, users.ErrLastAdministrator):
		return Conflict(users.ErrLastAdministrator.Error())
	}
	return err
}

// refuseUnlessRootToken is decision D32: a token principal may create or
// promote an administrator, re-enable an administrator or set a password only
// when it is the root token of the root account (authz.Principal.CanCreateTokens,
// computed per request from current data). Sessions are unaffected (they need
// recent sign-in instead). It returns nil when p may go on.
func refuseUnlessRootToken(p authz.Principal, what string) error {
	if p.Kind == authz.PrincipalToken && !p.CanCreateTokens {
		return rootTokenRequired(what)
	}
	return nil
}

func actor(r *http.Request) audit.Actor {
	p, _ := authz.FromContext(r.Context())
	return audit.ActorOf(p)
}

func registerUsers(s *Server, st users.Store) {
	s.HandleAPI("GET "+UsersPath, Requires(grantUsersRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, usersListSpec)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := users.ListFilter{After: q.After, Limit: q.Limit}
		for _, v := range q.Filter("role", OpEq) {
			f.Roles = append(f.Roles, authz.Role(v))
		}
		if d := q.Filter("disabled", OpEq); len(d) == 1 {
			v := d[0] == "true"
			f.Disabled = &v
		}
		if p := q.Filter("username", OpPrefix); len(p) == 1 {
			f.UsernamePrefix = p[0]
		}
		// disabled=true&disabled=false asks for both: no filter.
		items, err := st.List(r.Context(), f)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]userJSON, len(items))
		for i, u := range items {
			out[i] = toUserJSON(u)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(u userJSON) string { return u.ID }))
	}))
	s.HandleAPI("POST "+UsersPath, Requires(grantUsersWrite).WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Username    *string     `json:"username"`
			DisplayName *string     `json:"display_name"`
			Role        *authz.Role `json:"role"`
		}
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		if in.Username == nil {
			WriteError(w, r, Validation("username", "username is required"))
			return
		}
		if in.Role == nil {
			WriteError(w, r, Validation("role", "role is required"))
			return
		}
		if *in.Role == authz.RoleAdministrator {
			if err := refuseUnlessRootToken(principal(r), "creating an administrator"); err != nil {
				WriteError(w, r, err)
				return
			}
		}
		u, err := st.Create(r.Context(), actor(r), users.New{Username: *in.Username, DisplayName: in.DisplayName, Role: *in.Role})
		if err != nil {
			WriteError(w, r, userError(err))
			return
		}
		w.Header().Set("Location", UsersPath+"/"+u.ID)
		SetVersion(w, u.Version)
		WriteJSON(w, http.StatusCreated, toUserJSON(u))
	}))
	s.HandleAPI("GET "+UsersPath+"/{id}", Requires(grantUsersRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := st.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		SetVersion(w, u.Version)
		WriteJSON(w, http.StatusOK, toUserJSON(u))
	}))
	// PATCH is a JSON Merge Patch (ADR-0015, ADR-0017). display_name is the
	// only optional member (null clears it). Role changes and disabling are
	// audited; the last active administrator cannot be demoted or disabled.
	s.HandleAPI("PATCH "+UsersPath+"/{id}", Requires(grantUsersWrite).WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patch, err := DecodeMergePatch(r, "username", "display_name", "role", "disabled", "version")
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var bodyVersion *int64
		if patch.Has("version") {
			var v int64
			if patch.IsNull("version") || patch.Decode("version", &v) != nil {
				WriteError(w, r, Validation("version", "version must be a positive integer"))
				return
			}
			bodyVersion = &v
		}
		expected, err := ExpectedVersion(r, bodyVersion)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		p, err := parseUserPatch(patch)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		if err := guardAdministratorPatch(r, st, id, expected, p); err != nil {
			WriteError(w, r, err)
			return
		}
		var u users.User
		if len(patch) == 0 || len(patch) == 1 && patch.Has("version") {
			u, err = st.CheckVersion(r.Context(), id, expected)
		} else {
			u, err = st.Update(r.Context(), actor(r), id, expected, p)
		}
		if err != nil {
			var vc *resource.VersionConflictError
			if errors.As(err, &vc) {
				if cur, gerr := st.Get(r.Context(), id); gerr == nil {
					WriteError(w, r, VersionConflict(cur.Version, toUserJSON(cur)))
					return
				}
			}
			WriteError(w, r, userError(err))
			return
		}
		SetVersion(w, u.Version)
		WriteJSON(w, http.StatusOK, toUserJSON(u))
	}))
}

// guardAdministratorPatch applies D32 to PATCH /users/{id}: a token that is not
// the root token of the root account may not make a user an administrator or
// re-enable a disabled administrator (which also revives their tokens, D25-2).
// D34 adds the root account: demoting or disabling it needs the root token too.
// Demoting or disabling any other user stays open to users:write tokens.
//
// The decision is made on a read outside the write transaction, so it is pinned
// to the version: when the user is no longer at the version the caller expects,
// the guard answers version_conflict. The store's in-transaction check of the
// same expected version then guarantees the write applies to the state judged.
func guardAdministratorPatch(r *http.Request, st users.Store, id string, expected int64, p users.Patch) error {
	pr := principal(r)
	if pr.Kind != authz.PrincipalToken || pr.CanCreateTokens {
		return nil
	}
	promotes := p.Role != nil && *p.Role == authz.RoleAdministrator
	enables := p.Disabled != nil && !*p.Disabled
	demotes := p.Role != nil && *p.Role != authz.RoleAdministrator
	disables := p.Disabled != nil && *p.Disabled
	if !promotes && !enables && !demotes && !disables {
		return nil
	}
	before, err := st.Get(r.Context(), id)
	if err != nil {
		return err
	}
	if before.Version != expected {
		return VersionConflict(before.Version, toUserJSON(before))
	}
	if demotes || disables {
		// D34: the root account is only demoted or disabled by the root token or a session.
		root, err := st.RootAccountID(r.Context())
		if err != nil && !errors.Is(err, resource.ErrNotFound) {
			return err
		}
		if root == id && ((demotes && before.Role == authz.RoleAdministrator) || (disables && !before.Disabled)) {
			return rootTokenRequired("demoting or disabling the root account")
		}
	}
	if promotes && before.Role != authz.RoleAdministrator {
		return rootTokenRequired("making a user an administrator")
	}
	if enables && before.Disabled && before.Role == authz.RoleAdministrator {
		return rootTokenRequired("re-enabling an administrator")
	}
	return nil
}

func parseUserPatch(p MergePatch) (users.Patch, error) {
	var out users.Patch
	required := []string{"username", "role", "disabled"}
	for _, f := range required {
		if p.Has(f) && p.IsNull(f) {
			return users.Patch{}, Validation(f, f+" is required and cannot be cleared")
		}
	}
	if p.Has("username") {
		var v string
		if err := p.Decode("username", &v); err != nil {
			return users.Patch{}, err
		}
		out.Username = &v
	}
	if p.Has("role") {
		var v authz.Role
		if err := p.Decode("role", &v); err != nil {
			return users.Patch{}, err
		}
		out.Role = &v
	}
	if p.Has("disabled") {
		var v bool
		if err := p.Decode("disabled", &v); err != nil {
			return users.Patch{}, err
		}
		out.Disabled = &v
	}
	if p.Has("display_name") {
		out.SetDisplayName = true
		if !p.IsNull("display_name") {
			var v string
			if err := p.Decode("display_name", &v); err != nil {
				return users.Patch{}, err
			}
			out.DisplayName = &v
		}
	}
	if err := out.Validate(); err != nil {
		return users.Patch{}, userError(err)
	}
	return out, nil
}
