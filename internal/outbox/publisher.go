package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// JobName is the "job" log field and metric label of the publisher.
const JobName = "outbox-publisher"

// MaxRetryAge is how long a pending event is retried before it is marked failed
// (decision D17: backoff for up to 24 hours).
const MaxRetryAge = 24 * time.Hour

// Meter receives the publisher's measurements. A nil Meter discards them.
type Meter interface {
	EventPublished()
	EventDuplicate()
	EventFailed()
	EventRetry()
	SetPending(n int)
	SetConnected(up bool)
	JobDone(job string, ok bool, took time.Duration)
}

type nopMeter struct{}

func (nopMeter) EventPublished()                     {}
func (nopMeter) EventDuplicate()                     {}
func (nopMeter) EventFailed()                        {}
func (nopMeter) EventRetry()                         {}
func (nopMeter) SetPending(int)                      {}
func (nopMeter) SetConnected(bool)                   {}
func (nopMeter) JobDone(string, bool, time.Duration) {}

// PublisherConfig configures a [Publisher]. DB and Dial are required.
type PublisherConfig struct {
	DB    *store.DB
	Dial  Dialer
	Log   *slog.Logger
	Meter Meter
	// Now is the clock; nil means time.Now. Jitter returns a value in [0,1); nil
	// means a random one.
	Now    func() time.Time
	Jitter func() float64

	Poll           time.Duration // how often due rows are looked for; default 2s
	Batch          int           // rows per pass; default 50
	AttemptTimeout time.Duration // per publish attempt; default 5s
	DialTimeout    time.Duration // per connection attempt; default 10s
	MaxAge         time.Duration // retry window; default [MaxRetryAge]
	BackoffBase    time.Duration // first retry delay; default 5s
	BackoffMax     time.Duration // retry delay cap; default 15m
	DialBackoffMax time.Duration // connection retry cap; default 1m
}

// Status is a snapshot of the publisher for a status endpoint or a log line. It
// never gates readiness (decision D17).
type Status struct {
	Connected   bool
	Pending     int
	LastSuccess time.Time // zero until the first acknowledged publish
	LastError   string    // last failure, sanitised; empty when healthy
}

// Publisher is the background worker that sends pending outbox rows to a [Bus].
// It connects lazily, retries with exponential backoff and jitter, reuses each
// row's ID and time on every attempt, and fails rows older than MaxAge.
type Publisher struct {
	cfg   PublisherConfig
	log   *slog.Logger
	meter Meter

	bus          Bus
	dialFailures int
	dialAfter    time.Time
	timeouts     int // consecutive timed-out attempts

	mu     sync.Mutex
	status Status
}

// NewPublisher validates cfg and fills in defaults.
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if cfg.DB == nil || cfg.Dial == nil {
		return nil, errors.New("outbox: publisher needs a database and a dialer")
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.Poll, 2*time.Second)
	def(&cfg.AttemptTimeout, 5*time.Second)
	def(&cfg.DialTimeout, 10*time.Second)
	def(&cfg.MaxAge, MaxRetryAge)
	def(&cfg.BackoffBase, 5*time.Second)
	def(&cfg.BackoffMax, 15*time.Minute)
	def(&cfg.DialBackoffMax, time.Minute)
	if cfg.Batch <= 0 {
		cfg.Batch = 50
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Jitter == nil {
		cfg.Jitter = rand.Float64
	}
	p := &Publisher{cfg: cfg, log: cfg.Log, meter: cfg.Meter}
	if p.log == nil {
		p.log = slog.New(slog.DiscardHandler)
	}
	if p.meter == nil {
		p.meter = nopMeter{}
	}
	return p, nil
}

// Status returns a snapshot.
func (p *Publisher) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *Publisher) update(f func(*Status)) {
	p.mu.Lock()
	f(&p.status)
	p.mu.Unlock()
}

// Run works until ctx is cancelled, then closes the connection. It never
// returns an error: every failure is retried or recorded on the row.
func (p *Publisher) Run(ctx context.Context) {
	defer p.close()
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p.Tick(ctx)
		t.Reset(p.cfg.Poll)
	}
}

func (p *Publisher) close() {
	if p.bus != nil {
		p.bus.Close()
		p.bus = nil
		p.meter.SetConnected(false)
		p.update(func(s *Status) { s.Connected = false })
	}
}

type dueRow struct {
	Message
	attempts int
	created  time.Time
}

// Tick runs one pass: expire old rows and publish what is due, in enqueue order
// per pass (a row backed off by a failure is overtaken by later ones). Purging
// is the [Retention] job's, which runs whether or not publishing is enabled. It
// reports whether it handled any row.
func (p *Publisher) Tick(ctx context.Context) (worked bool) {
	start := time.Now()
	jl, _ := logging.Job(p.log, JobName)
	now := p.cfg.Now().UTC()
	ok := true
	defer func() {
		if worked {
			p.meter.JobDone(JobName, ok, time.Since(start))
		}
	}()

	if n, err := p.expire(ctx, now); err != nil {
		if ctx.Err() == nil {
			jl.Error("expiring old events failed", "error", sanitize(err))
		}
		ok = false
	} else if n > 0 {
		worked = true
		jl.Warn("events marked failed after the retry window", "count", n, "window", p.cfg.MaxAge.String())
	}
	rows, err := p.due(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			jl.Error("reading due events failed", "error", sanitize(err))
		}
		ok = false
	}
	if len(rows) > 0 {
		worked = true
		if p.ensureBus(ctx, jl, now) {
			for _, r := range rows {
				if !p.publishOne(ctx, jl, r) {
					break
				}
			}
		}
	}
	p.syncConnected()
	p.refreshPending(ctx)
	return worked
}

// syncConnected drives the connected gauge from the bus's real connection state
// when it reports one (the client library reconnects by itself).
func (p *Publisher) syncConnected() {
	cr, ok := p.bus.(ConnectionReporter)
	if p.bus == nil || !ok {
		return
	}
	up := cr.Connected()
	p.meter.SetConnected(up)
	p.update(func(s *Status) { s.Connected = up })
}

// dropBus closes a bus whose connection is lost and schedules a new dial.
func (p *Publisher) dropBus(now time.Time) {
	p.close()
	p.dialFailures++
	p.dialAfter = now.Add(p.backoff(time.Second, p.cfg.DialBackoffMax, p.dialFailures))
}

func (p *Publisher) refreshPending(ctx context.Context) {
	var n int
	err := p.cfg.DB.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE state = 'pending'").Scan(&n)
	})
	if err != nil {
		return
	}
	p.meter.SetPending(n)
	p.update(func(s *Status) { s.Pending = n })
}

func (p *Publisher) expire(ctx context.Context, now time.Time) (n int64, err error) {
	n, err = expirePending(ctx, p.cfg.DB, now, p.cfg.MaxAge, "not delivered within the retry window")
	for i := int64(0); i < n; i++ {
		p.meter.EventFailed()
	}
	return n, err
}

func (p *Publisher) due(ctx context.Context, now time.Time) ([]dueRow, error) {
	var out []dueRow
	err := p.cfg.DB.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, subject, type, source, occurred_at, payload, attempts, created_at
			FROM outbox WHERE state = 'pending' AND next_attempt_at <= ?
			ORDER BY next_attempt_at, seq LIMIT ?`, store.Timestamp{Time: now}, p.cfg.Batch)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r dueRow
			var occ, created store.Timestamp
			var payload string
			if err := rs.Scan(&r.ID, &r.Subject, &r.Type, &r.Source, &occ, &payload, &r.attempts, &created); err != nil {
				return err
			}
			r.Time, r.Payload, r.created = occ.Time, []byte(payload), created.Time
			out = append(out, r)
		}
		return rs.Err()
	})
	return out, err
}

// ensureBus connects when there is no connection and the dial backoff allows it.
func (p *Publisher) ensureBus(ctx context.Context, jl *slog.Logger, now time.Time) bool {
	if p.bus != nil {
		return true
	}
	if now.Before(p.dialAfter) {
		return false
	}
	dctx, cancel := context.WithTimeout(ctx, p.cfg.DialTimeout)
	defer cancel()
	bus, err := p.cfg.Dial(dctx)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		p.dialFailures++
		p.dialAfter = now.Add(p.backoff(time.Second, p.cfg.DialBackoffMax, p.dialFailures))
		msg := sanitize(err)
		p.update(func(s *Status) { s.Connected = false; s.LastError = msg })
		p.meter.SetConnected(false)
		if p.dialFailures == 1 || p.dialFailures%10 == 0 {
			jl.Warn("cannot connect to Pulse; events stay queued and the connection is retried with backoff",
				"error", msg, "failures", p.dialFailures, "retry_after", p.dialAfter.Sub(now).Round(time.Second).String())
		}
		return false
	}
	p.bus, p.dialFailures, p.dialAfter = bus, 0, time.Time{}
	p.meter.SetConnected(true)
	p.update(func(s *Status) { s.Connected = true; s.LastError = "" })
	jl.Info("connected to Pulse")
	return true
}

// publishOne sends one row and records the outcome. It returns false when the
// pass should stop (a retryable failure or cancellation).
func (p *Publisher) publishOne(ctx context.Context, jl *slog.Logger, r dueRow) bool {
	actx, cancel := context.WithTimeout(ctx, p.cfg.AttemptTimeout)
	ack, err := p.bus.Publish(actx, r.Message)
	cancel()
	if err != nil && ctx.Err() != nil {
		return false // shutting down: the row stays pending, untouched
	}
	now := p.cfg.Now().UTC()
	if err == nil {
		p.timeouts = 0
		if ack.Duplicate {
			p.meter.EventDuplicate()
		}
		p.meter.EventPublished()
		p.update(func(s *Status) { s.LastSuccess, s.LastError = now, "" })
		if e := p.mark(ctx, r.ID, `UPDATE outbox SET state = 'published', published_at = ?, attempts = attempts + 1, last_error = '' WHERE id = ? AND state = 'pending'`, store.Timestamp{Time: now}, r.ID); e != nil {
			// The event is on the bus; the row stays pending and a later retry reuses the ID.
			jl.Error("recording a published event failed", "event_id", r.ID, "error", sanitize(e))
		}
		return true
	}

	msg := sanitize(err)
	p.update(func(s *Status) { s.LastError = msg })
	if errors.Is(err, ErrPermanent) {
		p.fail(ctx, jl, r, msg)
		return true
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		p.timeouts++
		if p.timeouts == 3 || p.timeouts%30 == 0 {
			jl.Warn("publishing keeps timing out; besides an unreachable Pulse this can mean the credentials lack the publish grant for evt.ledger.>",
				"consecutive_timeouts", p.timeouts)
		}
	} else {
		p.timeouts = 0
	}
	if errors.Is(err, ErrDisconnected) {
		jl.Warn("connection to Pulse lost; reconnecting with backoff", "error", msg)
		p.dropBus(now)
	}
	if now.Sub(r.created) >= p.cfg.MaxAge {
		p.fail(ctx, jl, r, msg)
		return false
	}
	next := now.Add(p.backoff(p.cfg.BackoffBase, p.cfg.BackoffMax, r.attempts+1))
	p.meter.EventRetry()
	if e := p.mark(ctx, r.ID, `UPDATE outbox SET attempts = attempts + 1, next_attempt_at = ?, last_error = ? WHERE id = ? AND state = 'pending'`, store.Timestamp{Time: next}, msg, r.ID); e != nil {
		jl.Error("scheduling an event retry failed", "event_id", r.ID, "error", sanitize(e))
	}
	jl.Debug("publish failed; retry scheduled", "event_id", r.ID, "attempt", r.attempts+1, "retry_at", next, "error", msg)
	return false
}

func (p *Publisher) fail(ctx context.Context, jl *slog.Logger, r dueRow, msg string) {
	now := p.cfg.Now().UTC()
	p.meter.EventFailed()
	if e := p.mark(ctx, r.ID, `UPDATE outbox SET state = 'failed', failed_at = ?, attempts = attempts + 1, last_error = ? WHERE id = ? AND state = 'pending'`, store.Timestamp{Time: now}, msg, r.ID); e != nil {
		jl.Error("marking an event failed did not work", "event_id", r.ID, "error", sanitize(e))
		return
	}
	jl.Warn("event marked failed", "event_id", r.ID, "subject", r.Subject, "attempts", r.attempts+1, "error", msg)
}

// recordTimeout bounds the write that records an attempt's outcome.
const recordTimeout = 5 * time.Second

// mark records an outcome. It ignores cancellation of ctx (shutdown), so an
// event that is already on the bus is not left pending; serve waits for the
// publisher before closing the database.
func (p *Publisher) mark(ctx context.Context, id, q string, args ...any) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	return p.cfg.DB.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("outbox: row %s is no longer pending", id)
		}
		return nil
	})
}

// backoff is base*2^(n-1) capped at max, scaled by a jitter factor in [0.5, 1.0)
// so that rows failing together do not retry together.
func (p *Publisher) backoff(base, max time.Duration, n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := float64(base) * math.Pow(2, float64(n-1))
	if d > float64(max) || math.IsInf(d, 0) {
		d = float64(max)
	}
	return time.Duration(d * (0.5 + 0.5*p.cfg.Jitter()))
}

// sanitize makes an error safe to store and log: one line, bounded.
func sanitize(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
