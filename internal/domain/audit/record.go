package audit

import (
	"context"
	"time"
)

// Record is an event as stored: the event plus its identifier and position.
type Record struct {
	// ID is the entry's ULID; lists are in ID order, which is creation order
	// to the millisecond.
	ID string
	Event
}

// Filter selects records for [Reader.List]. Zero fields do not filter.
type Filter struct {
	// After returns only records with an ID greater than it ("" for all).
	After string
	// Limit is the most records to return. The reader fetches Limit+1 so the
	// caller can tell whether another page follows.
	Limit        int
	Actions      []Action // any of
	ActionPrefix string   // for example "token."
	ActorKinds   []ActorKind
	ActorUserID  string
	ActorTokenID string
	TargetType   string
	TargetID     string
	From         time.Time // At >= From
	To           time.Time // At < To
}

// Reader reads the audit log in ID order.
type Reader interface {
	List(ctx context.Context, f Filter) ([]Record, error)
}

// Recorder records one event on its own, outside any other change. It is for
// things that are not part of a state change, such as a failed sign-in or the
// use of a token; a change records its event through store.AuditWriter in its
// own transaction instead.
type Recorder interface {
	Record(ctx context.Context, e Event) error
}
