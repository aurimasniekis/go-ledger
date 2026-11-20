package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// Retention windows (decision D17): published rows are kept for diagnostics,
// failed rows longer so an operator can see what never got out.
const (
	PublishedRetention = 7 * 24 * time.Hour
	FailedRetention    = 30 * 24 * time.Hour
)

// States of an outbox row.
const (
	StatePending   = "pending"
	StatePublished = "published"
	StateFailed    = "failed"
)

// Config configures an [Outbox].
type Config struct {
	// Registry is the allow-list of events. Required.
	Registry *Registry
	// Enabled turns recording on (events.pulse.enabled). When false, Enqueue
	// validates the event and then records nothing: no replay on enabling later.
	Enabled bool
	// Source is the CloudEvents-style source stored on every event
	// (events.pulse.source). Required.
	Source string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// NewID returns a ULID; nil means logging.NewULID.
	NewID func() string
}

// Outbox enqueues events. It holds no database handle: every call takes the
// caller's transaction.
type Outbox struct {
	enabled bool
	reg     *Registry
	source  string
	now     func() time.Time
	newID   func() string
}

// New validates cfg and returns an Outbox.
func New(cfg Config) (*Outbox, error) {
	if cfg.Registry == nil {
		return nil, errors.New("outbox: a registry is required")
	}
	src := strings.TrimSpace(cfg.Source)
	if src == "" || len(src) > 128 || strings.IndexFunc(src, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return nil, errors.New("outbox: source must be 1-128 printable characters")
	}
	o := &Outbox{enabled: cfg.Enabled, reg: cfg.Registry, source: src, now: cfg.Now, newID: cfg.NewID}
	if o.now == nil {
		o.now = time.Now
	}
	if o.newID == nil {
		o.newID = logging.NewULID
	}
	return o, nil
}

// Event is one event to enqueue.
type Event struct {
	Entity string
	Action string
	// Key is the optional trailing subject token; its shape is fixed by the
	// event's [Definition.Key] (for example a lowercase ULID). Empty for none.
	Key     string
	Payload map[string]any
}

// Enqueued identifies the row Enqueue wrote.
type Enqueued struct {
	// Recorded is false when publishing is disabled and nothing was written;
	// the other fields are then empty.
	Recorded   bool
	ID         string
	Subject    string
	Type       string
	OccurredAt time.Time
}

// Enqueue inserts the event into the outbox using tx, which must be the
// write transaction that makes the state change (see [store.DB.WriteTx]). It
// never commits: when the caller rolls back, the row disappears with the change.
// The event is validated first (registered, subject, type, payload allow-list);
// a validation error leaves the transaction usable, and the caller normally
// returns it so the transaction rolls back. When the outbox is disabled the
// event is still validated (so mistakes show up in every mode) but nothing is
// written and Enqueued.Recorded is false.
func (o *Outbox) Enqueue(ctx context.Context, tx *sql.Tx, ev Event) (Enqueued, error) {
	def, ok := o.reg.Lookup(ev.Entity, ev.Action)
	if !ok {
		return Enqueued{}, fmt.Errorf("outbox: event %s.%s is not registered", ev.Entity, ev.Action)
	}
	if err := def.Key.check(ev.Key); err != nil {
		return Enqueued{}, fmt.Errorf("outbox: event %s: %w", def.id(), err)
	}
	subject, err := Subject(ev.Entity, ev.Action, ev.Key)
	if err != nil {
		return Enqueued{}, err
	}
	typ := def.Type()
	if err := ValidateType(typ); err != nil {
		return Enqueued{}, err
	}
	payload := ev.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	if err := def.checkPayload(payload); err != nil {
		return Enqueued{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Enqueued{}, fmt.Errorf("outbox: encode payload: %w", err)
	}
	if len(body) > MaxPayloadBytes {
		return Enqueued{}, fmt.Errorf("outbox: event %s payload is %d bytes, over the %d byte limit", def.id(), len(body), MaxPayloadBytes)
	}
	if !o.enabled {
		return Enqueued{}, nil
	}
	now := o.now().UTC()
	id := o.newID()
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox
		(id, subject, type, source, occurred_at, payload, next_attempt_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, subject, typ, o.source, store.Timestamp{Time: now}, string(body), store.Timestamp{Time: now}, store.Timestamp{Time: now})
	if err != nil {
		return Enqueued{}, fmt.Errorf("outbox: insert %s: %w", subject, err)
	}
	return Enqueued{Recorded: true, ID: id, Subject: subject, Type: typ, OccurredAt: now}, nil
}

// MaxPayloadBytes bounds an encoded event payload, well under the 1 MiB message
// limit of the bus, so a published event can never exceed it.
const MaxPayloadBytes = 256 << 10

// PurgeResult counts the rows Purge deleted.
type PurgeResult struct {
	Published int64
	Failed    int64
}

// Purge deletes published rows older than [PublishedRetention] and failed rows
// older than [FailedRetention], measured from published_at and failed_at.
// Pending rows are never deleted. Run it inside a write transaction.
func Purge(ctx context.Context, tx *sql.Tx, now time.Time) (PurgeResult, error) {
	var res PurgeResult
	del := func(q string, cutoff time.Time) (int64, error) {
		r, err := tx.ExecContext(ctx, q, store.Timestamp{Time: cutoff})
		if err != nil {
			return 0, err
		}
		return r.RowsAffected()
	}
	var err error
	if res.Published, err = del(`DELETE FROM outbox WHERE state = 'published' AND published_at < ?`, now.Add(-PublishedRetention)); err != nil {
		return res, fmt.Errorf("outbox: purge published: %w", err)
	}
	if res.Failed, err = del(`DELETE FROM outbox WHERE state = 'failed' AND failed_at < ?`, now.Add(-FailedRetention)); err != nil {
		return res, fmt.Errorf("outbox: purge failed: %w", err)
	}
	return res, nil
}
