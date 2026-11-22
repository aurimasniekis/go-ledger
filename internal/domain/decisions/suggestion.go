package decisions

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

// ObjectRef names what a suggestion is about: a transaction, commitment,
// account, merchant (by key) or occurrence. The decision layer never reads or
// writes the object itself.
type ObjectRef struct {
	Type string
	ID   string
}

var objectTypePattern = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// Valid reports whether the reference is well formed.
func (o ObjectRef) Valid() bool {
	return objectTypePattern.MatchString(o.Type) && o.ID != "" && utf8.RuneCountInString(o.ID) <= 200 && utf8.ValidString(o.ID)
}

// SuggestionState is where a stored suggestion stands.
type SuggestionState string

// States. A suggestion is pending until the person (or, for categorisation
// with auto-apply on, the caller applying it) accepts or rejects it; a newer
// suggestion for the same task and object supersedes a pending one.
const (
	StatePending    SuggestionState = "pending"
	StateAccepted   SuggestionState = "accepted"
	StateRejected   SuggestionState = "rejected"
	StateSuperseded SuggestionState = "superseded"
)

// States returns every state.
func States() []SuggestionState {
	return []SuggestionState{StatePending, StateAccepted, StateRejected, StateSuperseded}
}

// Valid reports whether s is a state.
func (s SuggestionState) Valid() bool { return slices.Contains(States(), s) }

// NewSuggestion is a validated answer to record.
type NewSuggestion struct {
	Task     TaskName
	Provider string
	// Model is the model the provider reported (the configured one when it
	// reported none): providers may change what an alias points to, so the
	// answer records what actually answered.
	Model      string
	ResponseID string
	Object     ObjectRef
	Decision   Decision
	// AutoApply is true when the task's configuration opted into
	// auto-apply (categorisation only, decision D13). Applying stays the
	// caller's job, and is reversible.
	AutoApply bool
	CreatedAt time.Time
}

// Suggestion is a stored model suggestion. It is never a bank fact, a match,
// a payment or an assignment: those stay where they are until someone applies
// the suggestion through their own rules.
type Suggestion struct {
	NewSuggestion
	ID        string
	State     SuggestionState
	DecidedAt *time.Time
	Version   int64
}

// Reversal records that a person took back an auto-applied suggestion's effect.
type Reversal struct {
	// ByKind is user, token or system; ByUserID is the person or the token's
	// owner (empty for the system).
	ByKind   string
	ByUserID string
	At       time.Time
}

// SuggestionView is a stored suggestion with its reversal, when it has one.
type SuggestionView struct {
	Suggestion
	Reversal *Reversal
}

// StateReverted is the review state of an accepted suggestion whose
// auto-applied effect was taken back. It is derived (the suggestion's own state
// stays accepted) and is only used to filter and show suggestions.
const StateReverted SuggestionState = "reverted"

// SuggestionFilter selects suggestions for listing, oldest first (by
// identifier). Empty fields match everything. States may name any of the
// stored states or [StateReverted]: "accepted" then means accepted and not
// reverted.
type SuggestionFilter struct {
	Tasks      []TaskName
	States     []SuggestionState
	ObjectType string
	ObjectID   string
	// After is the identifier of the last suggestion of the previous page.
	After string
	// Limit is the number of suggestions to return; zero means 100.
	Limit int
}

// Suggestions stores suggestions apart from facts.
type Suggestions interface {
	// Record stores a new pending suggestion and marks any pending one for
	// the same task and object superseded, in one transaction.
	Record(ctx context.Context, s NewSuggestion) (Suggestion, error)
}

// Errors of suggestion handling (beyond resource.ErrNotFound).
var (
	// ErrNotPending means the suggestion was already accepted, rejected or
	// superseded: a state moves once.
	ErrNotPending = errors.New("the suggestion is not pending")
	// ErrNotReversible means the suggestion has nothing to take back: it was
	// not auto-applied, was already reverted, or the person (or a stronger
	// origin) has since changed what it applied.
	ErrNotReversible = errors.New("the suggestion's effect cannot be reverted")
)

// Consents answers whether consent is recorded for sending data to a
// provider for a purpose. Consent is exact: a grant for (typesafe,
// payment_matching) says nothing about any other provider, any other purpose
// or any upstream provider a routing provider reaches.
type Consents interface {
	// Granted returns the fields the active consent covers; ok is false when
	// there is none.
	Granted(ctx context.Context, provider string, purpose TaskName) (fields []Field, ok bool, err error)
}

// Usage counts requests against the per-task monthly cap (decision D13).
// Reserve counts one attempt for the task in the period ("2006-01", UTC)
// when fewer than limit are counted, and reports whether it did.
type Usage interface {
	Reserve(ctx context.Context, task TaskName, period string, limit int) (bool, error)
}
