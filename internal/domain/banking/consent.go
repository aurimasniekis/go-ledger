package banking

import (
	"context"
	"errors"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
)

// Errors of claiming a consent state.
var (
	// ErrConsentTimedOut: the consent was started longer ago than its time to
	// live. The connection is failed (consent_expired) if it never connected;
	// a connection that was reconnecting keeps its status.
	ErrConsentTimedOut = errors.New("banking: the consent was not completed in time")
	// ErrConsentInitiator: the consent was started by someone else. The state
	// stays valid for them.
	ErrConsentInitiator = errors.New("banking: the consent was started by another user")
)

// ConsentExpiredCode is the error code of a pending connection whose consent
// was never completed in time.
const ConsentExpiredCode = "consent_expired"

// AuditFunc builds the audit event of a change from the connection as it
// stands after the change. A [ConsentStore] writes the event in the change's
// own transaction, so both are kept or lost together.
type AuditFunc func(Connection) audit.Event

// ConsentClaim is a consent state taken for completion. While it is held
// nobody else can complete the same state.
type ConsentClaim struct {
	// Connection is the connection as it was when the state was claimed.
	Connection Connection
	// State is the claimed state, for [ConsentStore.ReleaseConsent].
	State string
}

// ConsentStore is the store side of the user-driven connection flow (B4). It
// adds, to [FactStore], transitions that carry their audit entry in the same
// transaction (nil means none) and the claim of a consent state.
type ConsentStore interface {
	FactStore
	// StartConnection creates a pending connection that remembers when and by
	// whom (initiator, a user id) its consent started.
	StartConnection(ctx context.Context, c NewConnection, initiator string, at time.Time, ev AuditFunc) (Connection, error)
	// BeginReconnectBy is BeginReconnect that also records the start time and
	// initiator.
	BeginReconnectBy(ctx context.Context, id ConnectionID, consentState, initiator string, at time.Time, ev AuditFunc) (Connection, error)
	// ClaimConsent atomically takes the state: afterwards no connection has
	// it, so a second completion finds [ErrNotFound]. It fails with
	// [ErrConsentInitiator] (state kept) when initiator is not the one who
	// started the consent, and with [ErrConsentTimedOut] (state spent) when
	// the consent is older than ttl at at.
	ClaimConsent(ctx context.Context, state, initiator string, at time.Time, ttl time.Duration) (ConsentClaim, error)
	// ReleaseConsent puts a claimed state back after a failure that a retry
	// can fix. It does nothing when the connection has since been
	// disconnected or has a newer state.
	ReleaseConsent(ctx context.Context, claim ConsentClaim) error
	// ActivateClaimed activates a claimed connection (like ActivateConnection)
	// unless it was disconnected meanwhile, which returns [ErrDisconnected].
	ActivateClaimed(ctx context.Context, id ConnectionID, s Session, at time.Time, ev AuditFunc) (Connection, error)
	// DisconnectWithAudit is DisconnectConnection with an audit entry.
	DisconnectWithAudit(ctx context.Context, id ConnectionID, expected int64, at time.Time, ev AuditFunc) (Connection, error)
	// ExpireConsents ends every consent started before at-ttl: a pending
	// connection becomes failed with [ConsentExpiredCode]; any other keeps its
	// status and loses the waiting state. It returns how many it ended.
	ExpireConsents(ctx context.Context, at time.Time, ttl time.Duration) (int, error)
}

// ErrDisconnected: the connection was disconnected on purpose.
var ErrDisconnected = errors.New("banking: the connection is disconnected")
