// Package audit defines audit events (ADR-0021, ADR-0025): who did what to
// which object. store.AuditWriter is the port features write through, in the
// same transaction as the change they record; store.AuditLog implements it
// with the append-only audit_log table and serves the admin read API.
//
// An event never carries secrets, request bodies or free-form user content:
// every action declares the few detail fields it may carry (see [Validate]),
// and a writer refuses anything else.
package audit

import (
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

// ActorKind says what kind of actor made a change.
type ActorKind string

// Actor kinds.
const (
	ActorUser   ActorKind = "user"   // a signed-in user
	ActorToken  ActorKind = "token"  // an API token (HTTP or MCP); interactive steps such as 2FA were skipped
	ActorSystem ActorKind = "system" // the server itself or a host CLI command (bootstrap, break-glass)
	// ActorAnonymous is nobody who could be identified: a failed sign-in
	// whose username matches no account.
	ActorAnonymous ActorKind = "anonymous"
)

// Actor is who made a change.
type Actor struct {
	Kind ActorKind
	// UserID is the user, or the token's owner; empty for the system.
	UserID string
	// TokenID is set for token actors.
	TokenID string
}

// System is the actor of changes made by the server or a host CLI command.
var System = Actor{Kind: ActorSystem}

// ActorOf returns the actor for a principal.
func ActorOf(p authz.Principal) Actor {
	if p.Kind == authz.PrincipalToken {
		return Actor{Kind: ActorToken, UserID: p.UserID, TokenID: p.TokenID}
	}
	return Actor{Kind: ActorUser, UserID: p.UserID}
}

// Action names an audited change. Names are stable: they are read by the
// audit API and by people.
type Action string

// User management actions.
const (
	UserCreated     Action = "user.created"      // Details: username, role
	UserUpdated     Action = "user.updated"      // Details: fields (comma-separated names of changed profile fields)
	UserRoleChanged Action = "user.role_changed" // Details: from, to
	UserDisabled    Action = "user.disabled"
	UserEnabled     Action = "user.enabled"
)

// Event is one audited change.
type Event struct {
	Action     Action
	Actor      Actor
	TargetType string // for example "user"
	TargetID   string
	At         time.Time
	Details    map[string]string
}
