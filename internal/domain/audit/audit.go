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

// Commitment actions (task C1). Details never carry names or notes.
const (
	CommitmentCreated Action = "commitment.created" // Details: kind, currency
	CommitmentUpdated Action = "commitment.updated" // Details: fields (comma-separated names of changed fields)
	CommitmentDeleted Action = "commitment.deleted" // Details: kind, unpaid_occurrences_removed

	// OccurrencesRegenerated records changed commitment terms reaching stored
	// occurrences (task C2, ADR-0027); the target is the commitment.
	// Details: effective_from, created, updated, removed, kept_paid (counts).
	OccurrencesRegenerated Action = "occurrences.regenerated"
)

// Payment-state actions (task C3, ADR-0031). The target is the occurrence.
// Details never carry amounts, notes or override reasons.
const (
	ManualPaymentRecorded          Action = "manual_payment.recorded"           // Details: payment_id, currency
	ManualPaymentDeleted           Action = "manual_payment.deleted"            // Details: payment_id, currency
	OccurrenceStatusOverridden     Action = "occurrence.status_overridden"      // Details: status, previous (none, paid, unpaid)
	OccurrenceStatusOverrideUndone Action = "occurrence.status_override_undone" // Details: undone, restored (none, paid, unpaid)
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
