// Package authz is Ledger's permission vocabulary (ADR-0021): resources,
// actions, grants, roles and the principal a request acts as.
//
// One vocabulary serves every entry point. HTTP routes declare the grant they
// require (httpapi.Requires), API tokens store a set of grants that may never
// exceed their owner's (Grants.Covers), and MCP tools declare the same grant as
// the API operation they map to and check it with Principal.Allows. A grant is
// written "resource:action", for example "users:write"; the spelling is part of
// the public API (tokens, Terraform, MCP) and never changes meaning.
//
// The package is pure: no HTTP, SQL or storage.
package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Action is what a grant allows on a resource.
type Action string

// Actions. Write implies read: a principal that may change a resource may also
// read it.
const (
	ActionRead  Action = "read"
	ActionWrite Action = "write"
)

// Resource is a kind of object a grant applies to.
type Resource string

// Resources. Each feature adds its resource here, in its own change, with the
// actions it supports; a resource name is never reused for something else.
const (
	// ResourceAudit is the audit log. It is only ever read through the API.
	ResourceAudit Resource = "audit"
	// ResourceBanking is the bank-sourced text of transactions: descriptions
	// and counterparty names (decision D13 treats counterparty names as
	// sensitive). Other APIs that show it are redacted without this grant
	// (ADR-0035 point 9).
	ResourceBanking Resource = "banking"
	// ResourceConnections is bank connections (task B4): connecting a bank,
	// reconnecting, refreshing on request, disconnecting, and reading each
	// connection's accounts, balances and freshness. Not the bank's
	// transaction text (that is banking:read).
	ResourceConnections Resource = "connections"
	// ResourceCommitments is recurring commitment definitions (task C1).
	ResourceCommitments Resource = "commitments"
	// ResourceMerchants is the merchant identity of bank transactions (task E1,
	// ADR-0051): reading a transaction's merchant, its enrichment and the
	// decision about it, and confirming, correcting, rejecting or clearing that
	// decision. Merchant names derive from bank text, so like banking:read it is
	// for administrators.
	ResourceMerchants Resource = "merchants"
	// ResourceOrganisation is categories, labels and buckets and their
	// assignment to transactions, commitments, accounts and merchants (task
	// O1, ADR-0052). It holds the person's own labelling, not bank text, so the
	// read-only role may read it.
	ResourceOrganisation Resource = "organisation"
	// ResourceRates is the cached exchange rates and their refresh (task FX2c,
	// ADR-0048): reading lists the rates, their provenance and freshness (public
	// reference data, so the read-only role holds it); writing asks the provider
	// for fresh rates, which only an administrator may do.
	ResourceRates Resource = "rates"
	// ResourceTokens is every user's API tokens: listing and revoking them
	// (task A4). A user manages their own tokens with a session, whatever
	// their role; this grant is for administering everyone's.
	ResourceTokens Resource = "tokens"
	// ResourceUsers is user accounts: their roles and whether they are disabled.
	ResourceUsers Resource = "users"
)

// ResourceInfo describes a resource of the vocabulary.
type ResourceInfo struct {
	Name        Resource
	Description string
	// Actions are the actions that exist for the resource.
	Actions []Action
	// AdminOnly resources are not readable by the read-only role: they
	// administer the instance rather than hold finance data.
	AdminOnly bool
}

var vocabulary = []ResourceInfo{
	{Name: ResourceAudit, Description: "The audit log of security-relevant and finance-correcting changes.", Actions: []Action{ActionRead}, AdminOnly: true},
	{Name: ResourceBanking, Description: "Bank-sourced transaction text: raw descriptions and counterparty names.", Actions: []Action{ActionRead}, AdminOnly: true},
	{Name: ResourceCommitments, Description: "Recurring commitment definitions: kind, amount, schedule, dates, priority and notes.", Actions: []Action{ActionRead, ActionWrite}},
	{Name: ResourceConnections, Description: "Bank connections: connect, reconnect, refresh, disconnect, and read accounts, balances and freshness.", Actions: []Action{ActionRead, ActionWrite}, AdminOnly: true},
	{Name: ResourceMerchants, Description: "Merchant identity of bank transactions: the enrichment, and confirming, correcting or rejecting it.", Actions: []Action{ActionRead, ActionWrite}, AdminOnly: true},
	{Name: ResourceOrganisation, Description: "Categories, labels and buckets, and assigning them to transactions, commitments, accounts and merchants.", Actions: []Action{ActionRead, ActionWrite}},
	{Name: ResourceRates, Description: "Cached exchange rates with provider, source, rate date and freshness; write asks the rate provider for fresh rates.", Actions: []Action{ActionRead, ActionWrite}},
	{Name: ResourceTokens, Description: "Every user's API tokens: list them and revoke them. Never the tokens themselves.", Actions: []Action{ActionRead, ActionWrite}, AdminOnly: true},
	{Name: ResourceUsers, Description: "User accounts, their roles and whether they are disabled.", Actions: []Action{ActionRead, ActionWrite}, AdminOnly: true},
}

// Resources returns the vocabulary, sorted by name.
func Resources() []ResourceInfo {
	out := slices.Clone(vocabulary)
	for i := range out {
		out[i].Actions = slices.Clone(out[i].Actions)
	}
	return out
}

func lookup(r Resource) (ResourceInfo, bool) {
	for _, ri := range vocabulary {
		if ri.Name == r {
			return ri, true
		}
	}
	return ResourceInfo{}, false
}

// Grant allows one action on one resource.
type Grant struct {
	Resource Resource
	Action   Action
}

// G is shorthand for Grant{r, a}.
func G(r Resource, a Action) Grant { return Grant{Resource: r, Action: a} }

func (g Grant) String() string { return string(g.Resource) + ":" + string(g.Action) }

// Valid reports whether g is in the vocabulary.
func (g Grant) Valid() bool {
	ri, ok := lookup(g.Resource)
	return ok && slices.Contains(ri.Actions, g.Action)
}

// ErrUnknownGrant means a grant is not in the vocabulary.
var ErrUnknownGrant = errors.New("authz: unknown grant")

// ParseGrant parses "resource:action". Only grants in the vocabulary parse;
// there are no wildcards.
func ParseGrant(s string) (Grant, error) {
	r, a, ok := strings.Cut(s, ":")
	g := Grant{Resource: Resource(r), Action: Action(a)}
	if !ok || !g.Valid() {
		return Grant{}, fmt.Errorf("%w %q", ErrUnknownGrant, s)
	}
	return g, nil
}

// MarshalText writes the "resource:action" form.
func (g Grant) MarshalText() ([]byte, error) {
	if !g.Valid() {
		return nil, fmt.Errorf("%w %q", ErrUnknownGrant, g.String())
	}
	return []byte(g.String()), nil
}

// UnmarshalText parses the "resource:action" form.
func (g *Grant) UnmarshalText(b []byte) error {
	p, err := ParseGrant(string(b))
	if err != nil {
		return err
	}
	*g = p
	return nil
}

// AllGrants returns every grant of the vocabulary, sorted.
func AllGrants() Grants {
	var out Grants
	for _, ri := range vocabulary {
		for _, a := range ri.Actions {
			out = append(out, G(ri.Name, a))
		}
	}
	return normalize(out)
}

// Grants is a set of grants, sorted and without duplicates when built by
// [NewGrants].
type Grants []Grant

// NewGrants validates gs and returns them as a set.
func NewGrants(gs ...Grant) (Grants, error) {
	for _, g := range gs {
		if !g.Valid() {
			return nil, fmt.Errorf("%w %q", ErrUnknownGrant, g.String())
		}
	}
	return normalize(gs), nil
}

func normalize(gs []Grant) Grants {
	out := slices.Clone(gs)
	slices.SortFunc(out, func(a, b Grant) int { return strings.Compare(a.String(), b.String()) })
	return slices.Compact(out)
}

// Allows reports whether the set allows g. A write grant allows the read of
// the same resource. An invalid g is never allowed.
func (s Grants) Allows(g Grant) bool {
	if !g.Valid() {
		return false
	}
	for _, have := range s {
		if have == g || have.Resource == g.Resource && have.Action == ActionWrite && g.Action == ActionRead {
			return true
		}
	}
	return false
}

// Covers reports whether s allows every grant in other. A token's grants must
// be covered by its creator's (no escalation).
func (s Grants) Covers(other Grants) bool {
	for _, g := range other {
		if !s.Allows(g) {
			return false
		}
	}
	return true
}

// Intersect returns the grants of s that limit allows.
func (s Grants) Intersect(limit Grants) Grants {
	var out Grants
	for _, g := range s {
		if limit.Allows(g) {
			out = append(out, g)
		}
	}
	return normalize(out)
}

// Strings returns the "resource:action" forms.
func (s Grants) Strings() []string {
	out := make([]string, len(s))
	for i, g := range s {
		out[i] = g.String()
	}
	return out
}

// Role is a user's role. A user has exactly one.
type Role string

// Roles. Requirements ask for at least these two (decision D4); a role added
// later needs a migration (the users table checks the value).
const (
	RoleAdministrator Role = "administrator"
	RoleReadOnly      Role = "read-only"
)

// Roles returns every role.
func Roles() []Role { return []Role{RoleAdministrator, RoleReadOnly} }

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return slices.Contains(Roles(), r) }

// Grants returns what the role allows. The administrator holds every grant.
// The read-only role holds read on every resource that is not
// [ResourceInfo.AdminOnly] and never a write. An unknown role holds nothing.
func (r Role) Grants() Grants {
	switch r {
	case RoleAdministrator:
		return AllGrants()
	case RoleReadOnly:
		var out Grants
		for _, ri := range vocabulary {
			if !ri.AdminOnly && slices.Contains(ri.Actions, ActionRead) {
				out = append(out, G(ri.Name, ActionRead))
			}
		}
		return normalize(out)
	}
	return nil
}

// PrincipalKind says how a request authenticated.
type PrincipalKind string

// Principal kinds.
const (
	PrincipalUser  PrincipalKind = "user"  // an interactive sign-in (session)
	PrincipalToken PrincipalKind = "token" // an API token (HTTP or MCP)
)

// Principal is who a request acts as and what it may do. Authenticators
// build it on every request from current data, so a role change, a disabled
// user or a revoked token takes effect on the next request.
type Principal struct {
	Kind PrincipalKind
	// UserID is the user, or the owner of the token.
	UserID string
	// TokenID is set for token principals only.
	TokenID string
	// Role is the user's (or token owner's) current role.
	Role Role
	// Grants are the effective grants.
	Grants Grants
	// SessionID is set for a user signed in with a session cookie (A2): its
	// state-changing requests must carry the session's CSRF token.
	SessionID string
	// AuthTime is when the session last checked the user's password (sign-in
	// or re-authentication), for actions that need recent authentication.
	AuthTime time.Time
	// EnrollmentRequired is set for a session principal whose user must
	// enrol a second factor first (auth.require_2fa, ADR-0030): it may reach
	// only the enrolment, session and sign-out routes. Never set for API
	// tokens, which skip interactive steps by design.
	EnrollmentRequired bool
	// CanCreateTokens is set for a root token of the root account (the first
	// user, decision D25): the only token principal that may create API
	// tokens. Never set for other tokens or for users.
	CanCreateTokens bool
}

// UserPrincipal is a signed-in user acting with their role's grants.
func UserPrincipal(userID string, role Role) Principal {
	return Principal{Kind: PrincipalUser, UserID: userID, Role: role, Grants: role.Grants()}
}

// TokenPrincipal is a request made with an API token. Its effective grants
// are the token's grants that its owner's current role still allows, so
// demoting the owner narrows the token at once.
func TokenPrincipal(tokenID, ownerID string, ownerRole Role, tokenGrants Grants) Principal {
	return Principal{Kind: PrincipalToken, UserID: ownerID, TokenID: tokenID, Role: ownerRole,
		Grants: tokenGrants.Intersect(ownerRole.Grants())}
}

// Allows reports whether the principal holds g.
func (p Principal) Allows(g Grant) bool { return p.Grants.Allows(g) }

type ctxKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal of the request, if it authenticated.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
