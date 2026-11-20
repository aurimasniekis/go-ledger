package outbox

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/store"
)

type fakeBus struct {
	mu     sync.Mutex
	sent   []Message
	errs   []error // consumed one per Publish; nil entry or exhausted = success
	dup    bool
	closed bool
}

func (b *fakeBus) Publish(ctx context.Context, m Message) (Ack, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, m)
	if len(b.errs) > 0 {
		e := b.errs[0]
		b.errs = b.errs[1:]
		if e != nil {
			return Ack{}, e
		}
	}
	return Ack{Duplicate: b.dup}, nil
}
func (b *fakeBus) Close() { b.mu.Lock(); b.closed = true; b.mu.Unlock() }

type fakeMeter struct {
	published, dup, failed, retry, pending int
	connected                              bool
	jobs                                   int
}

func (m *fakeMeter) EventPublished()                     { m.published++ }
func (m *fakeMeter) EventDuplicate()                     { m.dup++ }
func (m *fakeMeter) EventFailed()                        { m.failed++ }
func (m *fakeMeter) EventRetry()                         { m.retry++ }
func (m *fakeMeter) SetPending(n int)                    { m.pending = n }
func (m *fakeMeter) SetConnected(up bool)                { m.connected = up }
func (m *fakeMeter) JobDone(string, bool, time.Duration) { m.jobs++ }

type pubEnv struct {
	db      *store.DB
	ob      *Outbox
	pub     *Publisher
	bus     *fakeBus
	meter   *fakeMeter
	now     time.Time
	dials   int
	dialErr error
	logs    *bytes.Buffer
}

func newPubEnv(t *testing.T, mut func(*PublisherConfig)) *pubEnv {
	t.Helper()
	e := &pubEnv{bus: &fakeBus{}, meter: &fakeMeter{}, now: fixedNow, logs: &bytes.Buffer{}}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e.db = db
	n := 0
	e.ob, err = New(Config{Enabled: true, Registry: testRegistry(t), Source: "ledger-test",
		Now: func() time.Time { return e.now }, NewID: func() string { n++; return fmt.Sprintf("01JD%022d", n) }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := PublisherConfig{DB: db, Meter: e.meter, Now: func() time.Time { return e.now }, Jitter: func() float64 { return 0 },
		Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Dial: func(ctx context.Context) (Bus, error) {
			e.dials++
			if e.dialErr != nil {
				return nil, e.dialErr
			}
			return e.bus, nil
		}}
	if mut != nil {
		mut(&cfg)
	}
	e.pub, err = NewPublisher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *pubEnv) enqueue(t *testing.T) Enqueued {
	t.Helper()
	var q Enqueued
	err := e.db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) (err error) {
		q, err = e.ob.Enqueue(ctx, tx, Event{Entity: "widget", Action: "created", Key: "01jd0000000000000000000000", Payload: map[string]any{"widget_id": "w1"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

type rowState struct {
	State, LastError string
	Attempts         int
	Next             time.Time
}

func (e *pubEnv) row(t *testing.T, id string) rowState {
	t.Helper()
	var r rowState
	var next store.Timestamp
	err := e.db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT state, attempts, next_attempt_at, last_error FROM outbox WHERE id = ?", id).Scan(&r.State, &r.Attempts, &next, &r.LastError)
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Next = next.Time
	return r
}

func TestPublishesWithStoredIDTimeAndSource(t *testing.T) {
	e := newPubEnv(t, nil)
	q := e.enqueue(t)
	e.now = e.now.Add(time.Minute) // publish later than occurred_at
	if !e.pub.Tick(context.Background()) {
		t.Fatal("expected work")
	}
	if len(e.bus.sent) != 1 {
		t.Fatalf("sent %d", len(e.bus.sent))
	}
	m := e.bus.sent[0]
	if m.ID != q.ID || !m.Time.Equal(q.OccurredAt) || m.Source != "ledger-test" || m.Subject != q.Subject || m.Type != q.Type || string(m.Payload) != `{"widget_id":"w1"}` {
		t.Fatalf("message %+v", m)
	}
	if r := e.row(t, q.ID); r.State != StatePublished || r.Attempts != 1 {
		t.Fatalf("row %+v", r)
	}
	if e.meter.published != 1 || e.meter.pending != 0 || !e.meter.connected || !e.pub.Status().Connected || e.pub.Status().LastSuccess.IsZero() {
		t.Fatalf("meter %+v status %+v", e.meter, e.pub.Status())
	}
}

func TestRetriesReuseIDAndTime(t *testing.T) {
	e := newPubEnv(t, nil)
	e.bus.errs = []error{errors.New("boom"), errors.New("boom again")}
	q := e.enqueue(t)
	for i := 0; i < 3; i++ {
		e.now = e.now.Add(time.Hour) // always past the backoff
		e.pub.Tick(context.Background())
	}
	if len(e.bus.sent) != 3 {
		t.Fatalf("attempts: %d", len(e.bus.sent))
	}
	for _, m := range e.bus.sent {
		if m.ID != q.ID || !m.Time.Equal(q.OccurredAt) {
			t.Fatalf("retry changed identity: %+v", m)
		}
	}
	if r := e.row(t, q.ID); r.State != StatePublished || r.Attempts != 3 {
		t.Fatalf("row %+v", r)
	}
	if e.meter.retry != 2 || e.meter.published != 1 {
		t.Fatalf("meter %+v", e.meter)
	}
}

func TestBackoffGrowsWithJitterAndCap(t *testing.T) {
	e := newPubEnv(t, nil)
	e.bus.errs = []error{errors.New("x"), errors.New("x"), errors.New("x")}
	q := e.enqueue(t)
	var waits []time.Duration
	for i := 0; i < 3; i++ {
		e.pub.Tick(context.Background())
		r := e.row(t, q.ID)
		waits = append(waits, r.Next.Sub(e.now))
		e.now = r.Next
	}
	// Jitter fixed at 0 gives the lower bound: half of 5s, 10s, 20s.
	want := []time.Duration{2500 * time.Millisecond, 5 * time.Second, 10 * time.Second}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits %v want %v", waits, want)
		}
	}
	p := e.pub
	p.cfg.Jitter = func() float64 { return 0.999999 }
	if d := p.backoff(5*time.Second, 15*time.Minute, 40); d > 15*time.Minute || d < 14*time.Minute {
		t.Fatalf("cap: %v", d)
	}
	if d := p.backoff(5*time.Second, 15*time.Minute, 5000); d > 15*time.Minute {
		t.Fatalf("overflow: %v", d)
	}
}

func TestNotDueRowsWait(t *testing.T) {
	e := newPubEnv(t, nil)
	e.bus.errs = []error{errors.New("x")}
	q := e.enqueue(t)
	e.pub.Tick(context.Background())
	e.pub.Tick(context.Background()) // same instant: backoff not elapsed
	if len(e.bus.sent) != 1 {
		t.Fatalf("sent %d", len(e.bus.sent))
	}
	e.now = e.row(t, q.ID).Next
	e.pub.Tick(context.Background())
	if len(e.bus.sent) != 2 {
		t.Fatalf("sent %d", len(e.bus.sent))
	}
}

func TestDuplicateAckIsSuccess(t *testing.T) {
	e := newPubEnv(t, nil)
	e.bus.dup = true
	q := e.enqueue(t)
	e.pub.Tick(context.Background())
	if r := e.row(t, q.ID); r.State != StatePublished {
		t.Fatalf("row %+v", r)
	}
	if e.meter.dup != 1 || e.meter.published != 1 || e.meter.failed != 0 {
		t.Fatalf("meter %+v", e.meter)
	}
}

func TestFailsAfterRetryWindow(t *testing.T) {
	e := newPubEnv(t, nil)
	e.bus.errs = []error{errors.New("down"), errors.New("still down")}
	q := e.enqueue(t)
	e.pub.Tick(context.Background())
	if r := e.row(t, q.ID); r.State != StatePending {
		t.Fatalf("row %+v", r)
	}
	e.now = e.now.Add(23*time.Hour + 59*time.Minute)
	e.pub.Tick(context.Background())
	if r := e.row(t, q.ID); r.State != StatePending {
		t.Fatalf("before the window ends: %+v", r)
	}
	e.now = e.now.Add(2 * time.Minute) // past 24h from creation
	e.bus.errs = []error{errors.New("still down")}
	e.pub.Tick(context.Background())
	r := e.row(t, q.ID)
	if r.State != StateFailed || !strings.Contains(r.LastError, "still down") {
		t.Fatalf("row %+v", r)
	}
	if e.meter.failed != 1 {
		t.Fatalf("meter %+v", e.meter)
	}
}

func TestRowsOlderThanWindowAreExpiredWithoutAttempt(t *testing.T) {
	e := newPubEnv(t, nil)
	q := e.enqueue(t) // the process was down for two days
	e.now = e.now.Add(48 * time.Hour)
	e.pub.Tick(context.Background())
	if r := e.row(t, q.ID); r.State != StateFailed || r.LastError == "" {
		t.Fatalf("row %+v", r)
	}
	if len(e.bus.sent) != 0 || e.meter.failed != 1 {
		t.Fatalf("sent %d meter %+v", len(e.bus.sent), e.meter)
	}
}

func TestPermanentErrorFailsImmediately(t *testing.T) {
	e := newPubEnv(t, nil)
	e.bus.errs = []error{fmt.Errorf("%w: bad subject", ErrPermanent)}
	q := e.enqueue(t)
	e.pub.Tick(context.Background())
	if r := e.row(t, q.ID); r.State != StateFailed || r.Attempts != 1 {
		t.Fatalf("row %+v", r)
	}
}

func TestDialFailureIsLazyBackedOffAndLeavesRowsPending(t *testing.T) {
	e := newPubEnv(t, nil)
	// Nothing due: no dial at all.
	e.pub.Tick(context.Background())
	if e.dials != 0 {
		t.Fatalf("dialled with nothing to send: %d", e.dials)
	}
	e.dialErr = errors.New("connection refused")
	q := e.enqueue(t)
	e.pub.Tick(context.Background())
	e.pub.Tick(context.Background()) // still inside the dial backoff
	if e.dials != 1 {
		t.Fatalf("dials %d", e.dials)
	}
	if r := e.row(t, q.ID); r.State != StatePending || r.Attempts != 0 {
		t.Fatalf("row %+v", r)
	}
	if st := e.pub.Status(); st.Connected || !strings.Contains(st.LastError, "refused") || st.Pending != 1 {
		t.Fatalf("status %+v", st)
	}
	if e.meter.connected || e.meter.pending != 1 {
		t.Fatalf("meter %+v", e.meter)
	}
	e.now = e.now.Add(2 * time.Minute)
	e.dialErr = nil
	e.pub.Tick(context.Background())
	if r := e.row(t, q.ID); r.State != StatePublished || e.dials != 2 || e.bus.sent[0].ID != q.ID {
		t.Fatalf("row %+v dials %d", r, e.dials)
	}
	if !e.pub.Status().Connected {
		t.Fatal("not connected after a successful dial")
	}
}

func TestRepeatedTimeoutsHintAtMissingGrant(t *testing.T) {
	e := newPubEnv(t, nil)
	for i := 0; i < 3; i++ {
		e.bus.errs = append(e.bus.errs, fmt.Errorf("%w: context deadline exceeded", ErrTimeout))
	}
	e.enqueue(t)
	for i := 0; i < 3; i++ {
		e.now = e.now.Add(time.Hour)
		e.pub.Tick(context.Background())
	}
	if !strings.Contains(e.logs.String(), "evt.ledger.>") {
		t.Fatalf("no grant hint in logs:\n%s", e.logs.String())
	}
}

func TestAttemptDeadlineIsPassed(t *testing.T) {
	e := newPubEnv(t, func(c *PublisherConfig) { c.AttemptTimeout = 3 * time.Second })
	var dl time.Duration
	e.pub.cfg.Dial = func(ctx context.Context) (Bus, error) {
		return busFunc(func(ctx context.Context, m Message) (Ack, error) {
			d, ok := ctx.Deadline()
			if !ok {
				t.Error("no deadline on the publish context")
			}
			dl = time.Until(d)
			return Ack{}, nil
		}), nil
	}
	e.enqueue(t)
	e.pub.Tick(context.Background())
	if dl <= 0 || dl > 3*time.Second {
		t.Fatalf("deadline %v", dl)
	}
}

type busFunc func(context.Context, Message) (Ack, error)

func (f busFunc) Publish(ctx context.Context, m Message) (Ack, error) { return f(ctx, m) }
func (busFunc) Close()                                                {}

func TestCancelledPublishLeavesRowUntouched(t *testing.T) {
	e := newPubEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.pub.cfg.Dial = func(context.Context) (Bus, error) {
		return busFunc(func(ctx context.Context, m Message) (Ack, error) { cancel(); return Ack{}, ctx.Err() }), nil
	}
	q := e.enqueue(t)
	e.pub.Tick(ctx)
	if r := e.row(t, q.ID); r.State != StatePending || r.Attempts != 0 {
		t.Fatalf("row %+v", r)
	}
}

func (e *pubEnv) count(t *testing.T, id string) (n int) {
	t.Helper()
	_ = e.db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE id = ?", id).Scan(&n)
	})
	return n
}

func TestRetentionPurgesWhetherOrNotPublishing(t *testing.T) {
	for _, publishing := range []bool{true, false} {
		e := newPubEnv(t, nil)
		q := e.enqueue(t)
		e.pub.Tick(context.Background()) // published
		old := e.enqueue(t)              // stays pending
		e.now = e.now.Add(PublishedRetention + 2*time.Hour)
		// No publisher pass here: the retention job alone does the work.
		NewRetention(RetentionConfig{DB: e.db, Publishing: publishing, Now: func() time.Time { return e.now }, Meter: e.meter}).Pass(context.Background())
		if e.count(t, q.ID) != 0 {
			t.Fatalf("publishing=%v: published row was not purged", publishing)
		}
		r := e.row(t, old.ID)
		if r.State != StateFailed {
			t.Fatalf("publishing=%v: pending row past the window = %+v", publishing, r)
		}
	}
}

func TestRetentionWithPublishingDisabledFailsLeftoverPendingRows(t *testing.T) {
	e := newPubEnv(t, nil)
	q := e.enqueue(t) // left over from an earlier enabled period, well inside 24h
	NewRetention(RetentionConfig{DB: e.db, Publishing: false, Now: func() time.Time { return e.now }}).Pass(context.Background())
	if r := e.row(t, q.ID); r.State != StateFailed || r.LastError != "publishing disabled" {
		t.Fatalf("row %+v", r)
	}
	// With publishing on, a young pending row is left for the publisher.
	q2 := e.enqueue(t)
	NewRetention(RetentionConfig{DB: e.db, Publishing: true, Now: func() time.Time { return e.now }}).Pass(context.Background())
	if r := e.row(t, q2.ID); r.State != StatePending {
		t.Fatalf("row %+v", r)
	}
}

func TestOutcomeIsRecordedEvenWhenShutdownArrivesAfterPublish(t *testing.T) {
	e := newPubEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	e.pub.cfg.Dial = func(context.Context) (Bus, error) {
		// The publish succeeds, then shutdown is requested before it is recorded.
		return busFunc(func(context.Context, Message) (Ack, error) { cancel(); return Ack{}, nil }), nil
	}
	q := e.enqueue(t)
	e.pub.Tick(ctx)
	if r := e.row(t, q.ID); r.State != StatePublished || r.Attempts != 1 {
		t.Fatalf("row %+v", r)
	}
}

func TestClosedConnectionIsRedialledWithBackoff(t *testing.T) {
	e := newPubEnv(t, nil)
	var buses []*fakeBus
	e.pub.cfg.Dial = func(context.Context) (Bus, error) {
		e.dials++
		b := &fakeBus{}
		if len(buses) == 0 {
			b.errs = []error{fmt.Errorf("%w: connection closed", ErrDisconnected)}
		}
		buses = append(buses, b)
		return b, nil
	}
	q := e.enqueue(t)
	e.pub.Tick(context.Background())
	if e.dials != 1 || !buses[0].closed || e.pub.bus != nil || e.meter.connected {
		t.Fatalf("dials=%d closed=%v bus=%v connected=%v", e.dials, buses[0].closed, e.pub.bus, e.meter.connected)
	}
	if r := e.row(t, q.ID); r.State != StatePending || r.Attempts != 1 {
		t.Fatalf("row %+v", r)
	}
	e.now = e.now.Add(time.Hour) // past the row backoff and the dial backoff
	e.pub.Tick(context.Background())
	if e.dials != 2 || len(buses[1].sent) != 1 || e.row(t, q.ID).State != StatePublished {
		t.Fatalf("dials=%d row=%+v", e.dials, e.row(t, q.ID))
	}
}

type reportingBus struct {
	fakeBus
	up bool
}

func (b *reportingBus) Connected() bool { return b.up }

func TestConnectedGaugeFollowsBusStatus(t *testing.T) {
	e := newPubEnv(t, nil)
	rb := &reportingBus{up: true}
	e.pub.cfg.Dial = func(context.Context) (Bus, error) { return rb, nil }
	e.enqueue(t)
	e.pub.Tick(context.Background())
	if !e.meter.connected || !e.pub.Status().Connected {
		t.Fatal("expected connected")
	}
	rb.up = false // the library is reconnecting
	e.pub.Tick(context.Background())
	if e.meter.connected || e.pub.Status().Connected {
		t.Fatal("gauge must follow the real connection state")
	}
}

func TestShutdownCancelDoesNotLogErrors(t *testing.T) {
	e := newPubEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.enqueue(t)
	e.pub.Tick(ctx)
	if strings.Contains(e.logs.String(), "level=ERROR") {
		t.Fatalf("logs:\n%s", e.logs.String())
	}
}

func TestRunStopsAndClosesBus(t *testing.T) {
	e := newPubEnv(t, func(c *PublisherConfig) { c.Poll = 5 * time.Millisecond })
	q := e.enqueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.pub.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for e.row(t, q.ID).State != StatePublished {
		select {
		case <-deadline:
			t.Fatal("not published")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	e.bus.mu.Lock()
	defer e.bus.mu.Unlock()
	if !e.bus.closed {
		t.Fatal("bus not closed")
	}
}

func TestNewPublisherRequiresDependencies(t *testing.T) {
	if _, err := NewPublisher(PublisherConfig{}); err == nil {
		t.Fatal("expected an error")
	}
}
