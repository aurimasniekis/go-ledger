package bankconnect_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
)

// These tests exercise the service against an in-memory store that is atomic
// the way the SQL store is, and the fake provider. The service against the real
// store (and the audit transaction) is covered in cmd/ledger.

var (
	t0    = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	alice = audit.Actor{Kind: audit.ActorUser, UserID: "alice"}
)

// memStore implements only what the tested paths call; any other method panics
// through the nil embedded interface.
type memStore struct {
	banking.ConsentStore
	mu      sync.Mutex
	conn    banking.Connection
	state   string // the claimable consent state; "" once claimed
	events  []audit.Event
	claims  int
	release int
}

func (m *memStore) ClaimConsent(_ context.Context, state, _ string, _ time.Time, _ time.Duration) (banking.ConsentClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state == "" || m.state != state {
		return banking.ConsentClaim{}, banking.ErrNotFound
	}
	m.state = ""
	m.claims++
	return banking.ConsentClaim{Connection: m.conn, State: state}, nil
}

func (m *memStore) ReleaseConsent(_ context.Context, c banking.ConsentClaim) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == "" {
		m.state = c.State
	}
	m.release++
	return nil
}

func (m *memStore) ActivateClaimed(_ context.Context, _ banking.ConsentClaim, s banking.Session, _ time.Time, ev banking.AuditFunc) (banking.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conn.Status, m.conn.Session = banking.ConnectionActive, s.Reference
	if ev != nil {
		m.events = append(m.events, ev(m.conn))
	}
	return m.conn, nil
}

func (m *memStore) SetConnectionStatus(_ context.Context, _ banking.ConnectionID, st banking.ConnectionStatus, cause error, _ time.Time) (banking.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conn.Status, m.conn.LastErrorKind = st, banking.Classify(cause)
	return m.conn, nil
}

func (m *memStore) FailClaimed(_ context.Context, c banking.ConsentClaim, st banking.ConnectionStatus, cause error, _ time.Time) (banking.Connection, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn.Version != c.Connection.Version || m.conn.Status == banking.ConnectionDisconnected {
		return m.conn, false, nil
	}
	m.conn.Status, m.conn.LastErrorKind = st, banking.Classify(cause)
	m.conn.Version++
	return m.conn, true, nil
}

func (m *memStore) Connection(context.Context, banking.ConnectionID) (banking.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conn, nil
}

type auditSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *auditSink) Record(_ context.Context, e audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *auditSink) outcomes(act audit.Action) []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audit.Event
	for _, e := range a.events {
		if e.Action == act {
			out = append(out, e)
		}
	}
	return out
}

// gated holds CompleteConsent inside the provider until released.
type gated struct {
	banking.Provider
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (g *gated) CompleteConsent(ctx context.Context, r banking.ConsentCompletion) (banking.Session, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	if g.release != nil {
		<-g.release
	}
	return g.Provider.CompleteConsent(ctx, r)
}

func (g *gated) completeCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

type setup struct {
	fake *bankingtest.Provider
	prov *gated
	mem  *memStore
	log  *auditSink
	svc  *bankconnect.Service
}

const waiting = "state-waiting"

func newSetup(t *testing.T, status banking.ConnectionStatus, old banking.Sensitive) *setup {
	t.Helper()
	fake := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: func() time.Time { return t0 }}, bankingtest.Standard())
	if _, err := fake.StartConsent(context.Background(), banking.ConsentRequest{Institution: bankingtest.Standard().Institutions[0], RedirectURL: "https://ledger.example/cb", State: waiting, PSUType: banking.PSUPersonal}); err != nil {
		t.Fatal(err)
	}
	caps := fake.Capabilities()
	s := &setup{fake: fake, prov: &gated{Provider: fake}, log: &auditSink{}}
	s.mem = &memStore{state: waiting, conn: banking.Connection{ID: "conn-1", Provider: caps.Provider, Application: caps.Application, Status: status, Session: old}}
	s.svc = &bankconnect.Service{Facts: s.mem, Providers: map[string]banking.Provider{caps.Application: s.prov}, Audit: s.log, RedirectURL: "https://ledger.example/cb", Now: func() time.Time { return t0 }}
	return s
}

func redirect(state string) bankconnect.Redirect {
	return bankconnect.Redirect{State: state, Code: bankingtest.AuthorizationCode(state)}
}

// F1: two completions of one consent. Only one reaches the provider; the
// other can neither fail the connection nor replace the session.
func TestConcurrentCompletionsOfOneConsent(t *testing.T) {
	s := newSetup(t, banking.ConnectionPending, banking.Sensitive{})
	s.prov.entered = make(chan struct{}, 2)
	s.prov.release = make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := s.svc.Complete(context.Background(), alice, redirect(waiting))
		first <- err
	}()
	<-s.prov.entered // A holds the claim, inside the provider

	if _, err := s.svc.Complete(context.Background(), alice, redirect(waiting)); !errors.Is(err, bankconnect.ErrUnknownConsent) {
		t.Fatalf("second completion = %v, want ErrUnknownConsent", err)
	}
	if n := s.prov.completeCalls(); n != 1 {
		t.Errorf("the provider was called %d times, want once", n)
	}
	close(s.prov.release)
	if err := <-first; err != nil {
		t.Fatalf("first completion: %v", err)
	}
	if c, _ := s.mem.Connection(context.Background(), "conn-1"); c.Status != banking.ConnectionActive || c.Session.IsZero() {
		t.Errorf("connection = %s, session set %v: the second completion must not undo the first", c.Status, !c.Session.IsZero())
	}
	if len(s.mem.events) != 1 {
		t.Errorf("%d connected entries, want 1", len(s.mem.events))
	}
}

func TestManyConcurrentCompletionsOneWinner(t *testing.T) {
	s := newSetup(t, banking.ConnectionPending, banking.Sensitive{})
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.svc.Complete(context.Background(), alice, redirect(waiting))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, bankconnect.ErrUnknownConsent):
			t.Errorf("unexpected error %v", err)
		}
	}
	if wins != 1 || s.mem.claims != 1 {
		t.Errorf("%d winners, %d claims; want one each", wins, s.mem.claims)
	}
}

func TestOnlyARetryableFailureReleasesTheClaim(t *testing.T) {
	s := newSetup(t, banking.ConnectionPending, banking.Sensitive{})
	s.fake.FailNext(bankingtest.OpCompleteConsent, bankingtest.Failure(bankingtest.OpCompleteConsent, banking.KindRetryable, "", "TIMEOUT", 0))
	if _, err := s.svc.Complete(context.Background(), alice, redirect(waiting)); banking.Classify(err) != banking.KindRetryable {
		t.Fatalf("= %v", err)
	}
	if s.mem.release != 1 || s.mem.conn.Status != banking.ConnectionPending {
		t.Fatalf("released %d times, status %s: a retryable failure puts the state back and changes nothing", s.mem.release, s.mem.conn.Status)
	}
	if _, err := s.svc.Complete(context.Background(), alice, redirect(waiting)); err != nil {
		t.Fatalf("the retry must work: %v", err)
	}

	p := newSetup(t, banking.ConnectionPending, banking.Sensitive{})
	p.fake.FailNext(bankingtest.OpCompleteConsent, bankingtest.Failure(bankingtest.OpCompleteConsent, banking.KindPermanent, "", "BAD", 0))
	if _, err := p.svc.Complete(context.Background(), alice, redirect(waiting)); banking.Classify(err) != banking.KindPermanent {
		t.Fatalf("permanent failure = %v", err)
	}
	if p.mem.release != 0 || p.mem.conn.Status != banking.ConnectionFailed || p.mem.state != "" {
		t.Errorf("permanent failure: released %d, status %s, state %q; the state must stay spent", p.mem.release, p.mem.conn.Status, p.mem.state)
	}
	if _, err := p.svc.Complete(context.Background(), alice, redirect(waiting)); !errors.Is(err, bankconnect.ErrUnknownConsent) {
		t.Errorf("a refused consent's state is spent: %v", err)
	}
}

// F2: a successful reconnect ends the session it replaced; a failure to do so
// is audited and reported, and does not undo the reconnect.
func TestReconnectEndsTheOldSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{"ended", false}, {"end fails", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fake := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: func() time.Time { return t0 }}, bankingtest.Standard())
			inst := bankingtest.Standard().Institutions[0]
			first, _ := fake.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: "old", PSUType: banking.PSUPersonal})
			_ = first
			old, err := fake.CompleteConsent(ctx, banking.ConsentCompletion{State: "old", Code: bankingtest.AuthorizationCode("old")})
			if err != nil {
				t.Fatal(err)
			}
			s := newSetup(t, banking.ConnectionActive, old.Reference)
			s.fake, s.prov.Provider = fake, fake
			s.svc.Providers = map[string]banking.Provider{fake.Capabilities().Application: s.prov}
			if _, err := fake.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: waiting, PSUType: banking.PSUPersonal}); err != nil {
				t.Fatal(err)
			}
			if tc.fail {
				fake.FailNext(bankingtest.OpEndSession, bankingtest.Failure(bankingtest.OpEndSession, banking.KindRetryable, "", "TIMEOUT", 0))
			}
			done, err := s.svc.Complete(ctx, alice, redirect(waiting))
			if err != nil {
				t.Fatal(err)
			}
			if done.Connection.Session.Reveal() == old.Reference.Reveal() {
				t.Fatal("the session was not replaced")
			}
			_, oldErr := fake.Accounts(ctx, old.Reference)
			evs := s.log.outcomes(audit.BankSessionEndFailed)
			if tc.fail {
				if oldErr != nil {
					t.Error("the end failed, so the old session is still live at the provider")
				}
				if !done.PreviousSessionEndFailed || len(evs) != 1 || evs[0].Details["kind"] != "retryable" {
					t.Errorf("failed end: done=%+v audit=%+v", done, evs)
				}
				return
			}
			if oldErr == nil {
				t.Error("the replaced session is still usable at the provider")
			}
			if done.PreviousSessionEndFailed || len(evs) != 0 {
				t.Errorf("clean end: done=%+v audit=%+v", done, evs)
			}
		})
	}
}

// stubEngine answers a refresh with what the test sets.
type stubEngine struct {
	res bankimport.Result
	err error
}

func (e stubEngine) Refresh(context.Context, banking.ConnectionID, banking.Provider) (bankimport.Result, error) {
	return e.res, e.err
}

// F3: the answer and the audit entry describe the run the engine returned,
// and an error before a run is not a success.
func TestRefreshUsesTheReturnedRunAndSurfacesPreRunErrors(t *testing.T) {
	ctx := context.Background()
	s := newSetup(t, banking.ConnectionActive, banking.NewSensitive("sess"))
	s.svc.Facts = &viewStore{memStore: s.mem}

	run := banking.RefreshRun{ID: "run-2", Outcome: banking.RefreshFailed}
	s.svc.Engine = stubEngine{res: bankimport.Result{Run: run}, err: &banking.Error{Kind: banking.KindRetryable, Provider: "fake", Op: "accounts"}}
	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); err != nil {
		t.Fatalf("a failed run is not an error of the call: %v", err)
	}
	if evs := s.log.outcomes(audit.BankRefreshRequested); len(evs) != 1 || evs[0].Details["outcome"] != "failed" {
		t.Fatalf("audit = %+v, want outcome failed", evs)
	}

	s.svc.Engine = stubEngine{err: bankimport.ErrNotRefreshable}
	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); !errors.Is(err, bankconnect.ErrNotActive) {
		t.Errorf("ErrNotRefreshable = %v, want ErrNotActive", err)
	}
	boom := errors.New("database is locked")
	s.svc.Engine = stubEngine{err: boom}
	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); !errors.Is(err, boom) {
		t.Errorf("a pre-run error = %v, want it returned", err)
	}
	s.svc.Engine = stubEngine{}
	if _, err := s.svc.Refresh(ctx, alice, "conn-1"); err == nil {
		t.Error("an engine that returns neither a run nor an error must not look like success")
	}
	if n := len(s.log.outcomes(audit.BankRefreshRequested)); n != 1 {
		t.Errorf("%d refresh entries, want only the one for the run that happened", n)
	}
}

// viewStore adds what View reads after a refresh.
type viewStore struct{ *memStore }

func (viewStore) Accounts(context.Context, banking.ConnectionID) ([]banking.StoredAccount, error) {
	return nil, nil
}
func (viewStore) RefreshRuns(context.Context, banking.ConnectionID, int) ([]banking.RefreshRun, error) {
	return nil, nil
}
func (viewStore) ExpireConsents(context.Context, time.Time, time.Duration) (int, error) {
	return 0, nil
}

// D40.4: at the deadline the run is not cancelled; the answer says it is
// still running, and the run's audit entry is written when it ends.
func TestRefreshPastTheDeadlineKeepsRunning(t *testing.T) {
	s := newSetup(t, banking.ConnectionActive, banking.NewSensitive("sess"))
	s.svc.Facts = &viewStore{memStore: s.mem}
	s.svc.RefreshTimeout = 30 * time.Millisecond
	eng := &slowEngine{release: make(chan struct{}), seen: make(chan struct{})}
	s.svc.Engine = eng
	v, err := s.svc.Refresh(context.Background(), alice, "conn-1")
	if err != nil || !v.RefreshRunning {
		t.Fatalf("Refresh = %+v, %v; want the view with RefreshRunning", v, err)
	}
	time.Sleep(50 * time.Millisecond) // a cancelled engine would have noticed by now
	if eng.ctxErr() != nil {
		t.Fatalf("the engine's context was cancelled at the deadline: %v", eng.ctxErr())
	}
	if n := len(s.log.outcomes(audit.BankRefreshRequested)); n != 0 {
		t.Fatalf("%d refresh entries before the run ended", n)
	}
	close(eng.release)
	deadline := time.Now().Add(5 * time.Second)
	for len(s.log.outcomes(audit.BankRefreshRequested)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no audit entry after the run ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if evs := s.log.outcomes(audit.BankRefreshRequested); evs[0].Details["outcome"] != "succeeded" {
		t.Errorf("audit = %+v, want outcome succeeded", evs)
	}
}

// slowEngine holds the refresh until released and records whether its context
// was cancelled meanwhile.
type slowEngine struct {
	release chan struct{}
	seen    chan struct{}
	mu      sync.Mutex
	err     error
}

func (e *slowEngine) Refresh(ctx context.Context, _ banking.ConnectionID, _ banking.Provider) (bankimport.Result, error) {
	select {
	case <-e.release:
	case <-ctx.Done():
	}
	e.mu.Lock()
	e.err = ctx.Err()
	e.mu.Unlock()
	return bankimport.Result{Run: banking.RefreshRun{ID: "run-1", Outcome: banking.RefreshSucceeded}}, nil
}

// ctxErr is the context error seen when the engine stopped waiting; nil while
// it is still held or when released without cancellation.
func (e *slowEngine) ctxErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// R5: the expiry sweep is housekeeping: a failing one does not fail a read.
type failingSweepStore struct{ *viewStore }

func (failingSweepStore) ExpireConsents(context.Context, time.Time, time.Duration) (int, error) {
	return 0, errors.New("database is locked")
}

func TestReadsSurviveAFailingExpirySweep(t *testing.T) {
	s := newSetup(t, banking.ConnectionActive, banking.NewSensitive("sess"))
	s.svc.Facts = failingSweepStore{&viewStore{memStore: s.mem}}
	if _, err := s.svc.View(context.Background(), "conn-1"); err != nil {
		t.Errorf("View failed because the sweep failed: %v", err)
	}
}

// B4g F1: a run still going when Wait's bound passes is cancelled; the engine
// ends it as interrupted and its audit entry is written.
func TestCancelRefreshesInterruptsARunPastTheShutdownBound(t *testing.T) {
	s := newSetup(t, banking.ConnectionActive, banking.NewSensitive("sess"))
	s.svc.Facts = &viewStore{memStore: s.mem}
	s.svc.RefreshTimeout = 20 * time.Millisecond
	s.svc.Engine = &cancelAwareEngine{}
	if v, err := s.svc.Refresh(context.Background(), alice, "conn-1"); err != nil || !v.RefreshRunning {
		t.Fatalf("Refresh = %+v, %v; want RefreshRunning", v, err)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer wcancel()
	if err := s.svc.Wait(wctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want the deadline error", err)
	}
	if n := len(s.log.outcomes(audit.BankRefreshRequested)); n != 0 {
		t.Fatalf("%d audit entries before cancelling", n)
	}
	s.svc.CancelRefreshes()
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if err := s.svc.Wait(cctx); err != nil {
		t.Fatalf("second Wait = %v, want the run ended", err)
	}
	evs := s.log.outcomes(audit.BankRefreshRequested)
	if len(evs) != 1 || evs[0].Details["outcome"] != string(banking.RefreshInterrupted) {
		t.Fatalf("audit = %+v, want one entry with outcome interrupted", evs)
	}
}

// B4g F2: once Wait began, Refresh starts nothing.
func TestRefreshAfterWaitIsRejected(t *testing.T) {
	s := newSetup(t, banking.ConnectionActive, banking.NewSensitive("sess"))
	s.svc.Facts = &viewStore{memStore: s.mem}
	s.svc.Engine = &cancelAwareEngine{}
	if err := s.svc.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.Refresh(context.Background(), alice, "conn-1"); !errors.Is(err, bankconnect.ErrShuttingDown) {
		t.Fatalf("Refresh after Wait = %v, want ErrShuttingDown", err)
	}
}

// cancelAwareEngine runs until its context is cancelled, then reports the
// run as interrupted, as the real engine does.
type cancelAwareEngine struct{}

func (cancelAwareEngine) Refresh(ctx context.Context, _ banking.ConnectionID, _ banking.Provider) (bankimport.Result, error) {
	<-ctx.Done()
	return bankimport.Result{Run: banking.RefreshRun{ID: "run-1", Outcome: banking.RefreshInterrupted}}, ctx.Err()
}

// B5c: a permanent failure of the authorise call must not overwrite what
// changed while the provider call was in flight.
func TestConsentFailureDoesNotOverwriteALaterChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(m *memStore)
		want   banking.ConnectionStatus
	}{
		{"another consent completed", func(m *memStore) {
			m.conn.Status, m.conn.Session = banking.ConnectionActive, banking.NewSensitive("newer-session")
			m.conn.Version++
		}, banking.ConnectionActive},
		{"disconnected", func(m *memStore) {
			m.conn.Status = banking.ConnectionDisconnected
			m.conn.Version++
		}, banking.ConnectionDisconnected},
		{"a newer reconnect started", func(m *memStore) {
			m.state = "state-newer"
			m.conn.Version++
		}, banking.ConnectionReconnectRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSetup(t, banking.ConnectionReconnectRequired, banking.NewSensitive("old-session"))
			s.prov.entered = make(chan struct{}, 1)
			s.prov.release = make(chan struct{})
			s.fake.FailNext(bankingtest.OpCompleteConsent, bankingtest.Failure(bankingtest.OpCompleteConsent, banking.KindPermanent, "", "BAD", 0))
			done := make(chan error, 1)
			go func() {
				_, err := s.svc.Complete(context.Background(), alice, redirect(waiting))
				done <- err
			}()
			<-s.prov.entered
			s.mem.mu.Lock()
			tc.mutate(s.mem)
			s.mem.mu.Unlock()
			close(s.prov.release)
			if err := <-done; banking.Classify(err) != banking.KindPermanent {
				t.Fatalf("= %v", err)
			}
			if s.mem.conn.Status != tc.want || s.mem.conn.LastErrorKind != "" {
				t.Errorf("status %s, error kind %q: the stale failure must not be written", s.mem.conn.Status, s.mem.conn.LastErrorKind)
			}
			if tc.name == "a newer reconnect started" && s.mem.state != "state-newer" {
				t.Errorf("consent state = %q, the newer one must stay claimable", s.mem.state)
			}
		})
	}
}
