package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Bus is the destination the publisher sends events to. The production
// implementation wraps the Pulse SDK (internal/pulsebus); tests use a fake, so
// nothing here needs a live Pulse or NATS server.
type Bus interface {
	// Publish sends one event and waits for the broker's acknowledgement. The
	// message ID is the idempotency key: a retry passes the same one.
	Publish(ctx context.Context, m Message) (Ack, error)
	// Close releases the connection.
	Close()
}

// Dialer opens a [Bus]. The publisher calls it lazily and again after a failure,
// with its own backoff; a failing Dialer never stops the application.
type Dialer func(ctx context.Context) (Bus, error)

// Message is one outbox row as sent: every field comes from the stored row, so
// a retry is byte-for-byte the same event.
type Message struct {
	ID      string
	Subject string
	Type    string
	Source  string
	Time    time.Time
	Payload json.RawMessage
}

// Ack is the broker's acknowledgement.
type Ack struct {
	// Duplicate is true when the broker had already stored this ID (inside its
	// deduplication window). It counts as success.
	Duplicate bool
}

var (
	// ErrTimeout marks an attempt that hit its deadline. The broker also answers
	// a publish outside the credentials' grants with a timeout, not a refusal.
	ErrTimeout = errors.New("outbox: publish timed out")
	// ErrPermanent marks an event that can never be published as it is (for
	// example a malformed subject); the row fails at once instead of retrying.
	ErrPermanent = errors.New("outbox: event cannot be published")
)
