package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
)

var svcT0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

var (
	svcAlice = audit.Actor{Kind: audit.ActorUser, UserID: "01JZ00000000000000000000A1"}
	svcBob   = audit.Actor{Kind: audit.ActorUser, UserID: "01JZ00000000000000000000B2"}
)

// gated wraps the fake so a test can hold CompleteConsent inside the provider.
type svcGated struct {
	banking.Provider
	mu      sync.Mutex
	calls   int
	entered chan struct{} // receives once per CompleteConsent call
	release chan struct{} // closed to let held calls go on; nil means no hold
}

func (g *svcGated) CompleteConsent(ctx context.Context, r banking.ConsentCompletion) (banking.Session, error) {
	g.mu.Lock()
	g.calls++
	hold := g.release
	g.mu.Unlock()
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	if hold != nil {
		<-hold
	}
	return g.Provider.CompleteConsent(ctx, r)
}

func (g *svcGated) completeCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

type svcEnv struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	log   *store.AuditLog
	facts *bankfacts.Store
	fake  *bankingtest.Provider
	prov  *svcGated
	svc   *bankconnect.Service
	now   time.Time
}

type failingAudit struct{}

func (failingAudit) WriteAudit(context.Context, *sql.Tx, audit.Event) error {
	return errors.New("audit refused")
}

func newSvcEnv(t *testing.T) *svcEnv {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &svcEnv{t: t, ctx: ctx, db: db, log: store.NewAuditLog(db), now: svcT0}
	e.fake = bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: func() time.Time { return e.now }}, bankingtest.Standard())
	e.prov = &svcGated{Provider: e.fake}
	e.facts = bankfacts.New(db, nil).WithAudit(e.log)
	e.svc = &bankconnect.Service{
		Facts: e.facts, Providers: map[string]banking.Provider{e.fake.Capabilities().Application: e.prov},
		Engine: bankimport.New(e.facts, bankimport.Options{Now: func() time.Time { return e.now }, Sleep: func(context.Context, time.Duration) error { return nil }}),
		Audit:  e.log, RedirectURL: "https://ledger.example/connections/callback", Now: func() time.Time { return e.now },
	}
	return e
}

func (e *svcEnv) start(actor audit.Actor) (banking.ConnectionID, string) {
	e.t.Helper()
	st, err := e.svc.Start(e.ctx, actor, bankconnect.Start{Application: e.fake.Capabilities().Application, InstitutionID: bankingtest.InstitutionID, Country: bankingtest.InstitutionCountry})
	if err != nil {
		e.t.Fatal(err)
	}
	return st.Connection.ID, stateOf(e.t, st.AuthorizationURL)
}

func svcRedirect(state string) bankconnect.Redirect {
	return bankconnect.Redirect{State: state, Code: bankingtest.AuthorizationCode(state)}
}

func (e *svcEnv) conn(id banking.ConnectionID) banking.Connection {
	e.t.Helper()
	c, err := e.facts.Connection(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *svcEnv) actions(a audit.Action) []audit.Record {
	e.t.Helper()
	recs, err := e.log.List(e.ctx, audit.Filter{Limit: 100, Actions: []audit.Action{a}})
	if err != nil {
		e.t.Fatal(err)
	}
	return recs
}

func (e *svcEnv) connect(actor audit.Actor) banking.Connection {
	e.t.Helper()
	id, state := e.start(actor)
	done, err := e.svc.Complete(e.ctx, actor, svcRedirect(state))
	if err != nil {
		e.t.Fatal(err)
	}
	if done.Connection.ID != id {
		e.t.Fatalf("completed %s, started %s", done.Connection.ID, id)
	}
	return done.Connection
}

// F1: two completions of one consent. Only one reaches the provider; the other
// can neither fail the connection nor replace the session.
func TestConcurrentCompletionsOfOneConsent(t *testing.T) {
	e := newSvcEnv(t)
	id, state := e.start(svcAlice)
	e.prov.entered = make(chan struct{}, 2)
	e.prov.release = make(chan struct{})

	first := make(chan error, 1)
	go func() {
		_, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state))
		first <- err
	}()
	<-e.prov.entered // A holds the claim, inside the provider

	// B arrives with the same state (a second tab, a retrying client).
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); !errors.Is(err, bankconnect.ErrUnknownConsent) {
		t.Fatalf("second completion = %v, want ErrUnknownConsent", err)
	}
	if n := e.prov.completeCalls(); n != 1 {
		t.Errorf("the provider was called %d times, want once", n)
	}
	close(e.prov.release)
	if err := <-first; err != nil {
		t.Fatalf("first completion: %v", err)
	}
	c := e.conn(id)
	if c.Status != banking.ConnectionActive || c.Session.IsZero() {
		t.Errorf("connection = %s session set %v; the second completion must not undo the first", c.Status, !c.Session.IsZero())
	}
	if n := len(e.actions(audit.BankConnectionConnected)); n != 1 {
		t.Errorf("%d connected entries, want 1", n)
	}
}

func TestManyConcurrentCompletionsOneWinner(t *testing.T) {
	e := newSvcEnv(t)
	id, state := e.start(svcAlice)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state))
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
	if wins != 1 || e.conn(id).Status != banking.ConnectionActive {
		t.Errorf("%d winners, status %s; want one and active", wins, e.conn(id).Status)
	}
}

func TestRetryableFailureReleasesTheClaimButPermanentSpendsIt(t *testing.T) {
	e := newSvcEnv(t)
	id, state := e.start(svcAlice)
	e.fake.FailNext(bankingtest.OpCompleteConsent, bankingtest.Failure(bankingtest.OpCompleteConsent, banking.KindRetryable, "", "TIMEOUT", 0))
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); banking.Classify(err) != banking.KindRetryable {
		t.Fatalf("first completion = %v, want a retryable error", err)
	}
	if c := e.conn(id); c.Status != banking.ConnectionPending {
		t.Fatalf("a retryable failure changed the status to %s", c.Status)
	}
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); err != nil {
		t.Fatalf("the retry must work: %v", err)
	}

	id2, state2 := e.start(svcAlice)
	e.fake.FailNext(bankingtest.OpCompleteConsent, bankingtest.Failure(bankingtest.OpCompleteConsent, banking.KindPermanent, "", "BAD", 0))
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state2)); banking.Classify(err) != banking.KindPermanent {
		t.Fatalf("permanent failure = %v", err)
	}
	if c := e.conn(id2); c.Status != banking.ConnectionFailed {
		t.Errorf("status = %s, want failed", c.Status)
	}
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state2)); !errors.Is(err, bankconnect.ErrUnknownConsent) {
		t.Errorf("a refused consent's state is spent: %v", err)
	}
}

// F2: a successful reconnect ends the session it replaced.
func TestReconnectEndsTheOldProviderSession(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	old := c.Session
	if _, err := e.fake.Accounts(e.ctx, old); err != nil {
		t.Fatalf("the first session should be live: %v", err)
	}
	st, err := e.svc.Reconnect(e.ctx, svcAlice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	done, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(stateOf(t, st.AuthorizationURL)))
	if err != nil {
		t.Fatal(err)
	}
	if done.PreviousSessionEndFailed || done.Connection.Session.Reveal() == old.Reveal() {
		t.Fatalf("completion = %+v", done)
	}
	if _, err := e.fake.Accounts(e.ctx, old); err == nil {
		t.Error("the replaced session is still usable at the provider")
	}
	if _, err := e.fake.Accounts(e.ctx, done.Connection.Session); err != nil {
		t.Errorf("the new session must be live: %v", err)
	}
	if n := len(e.actions(audit.BankSessionEndFailed)); n != 0 {
		t.Errorf("%d session_end_failed entries after a clean end", n)
	}
}

func TestFailedEndOfTheOldSessionIsSurfacedAndAudited(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	st, err := e.svc.Reconnect(e.ctx, svcAlice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.fake.FailNext(bankingtest.OpEndSession, bankingtest.Failure(bankingtest.OpEndSession, banking.KindRetryable, "", "TIMEOUT", 0))
	done, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(stateOf(t, st.AuthorizationURL)))
	if err != nil {
		t.Fatalf("a failed end of the old session must not undo the reconnect: %v", err)
	}
	if !done.PreviousSessionEndFailed || done.Connection.Status != banking.ConnectionActive {
		t.Errorf("completion = %+v, want active with the failure flagged", done)
	}
	recs := e.actions(audit.BankSessionEndFailed)
	if len(recs) != 1 || recs[0].Details["kind"] != "retryable" || recs[0].TargetID != string(c.ID) {
		t.Fatalf("audit entries = %+v", recs)
	}
	for _, v := range recs[0].Details {
		if v == c.Session.Reveal() {
			t.Error("the audit entry holds a session reference")
		}
	}
}

// recordingRefresher answers a refresh with what the test sets.
type recordingRefresher struct {
	res bankimport.Result
	err error
}

func (r recordingRefresher) Refresh(context.Context, banking.ConnectionID, banking.Provider) (bankimport.Result, error) {
	return r.res, r.err
}

// F3: the refresh answer and its audit entry describe the run the engine
// returned, and an error before a run is not a success.
func TestRefreshReportsItsOwnRunAndPreRunErrors(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	if _, err := e.svc.Refresh(e.ctx, svcAlice, c.ID); err != nil { // a real run: succeeded
		t.Fatal(err)
	}
	last := func() string {
		recs := e.actions(audit.BankRefreshRequested)
		return recs[len(recs)-1].Details["outcome"]
	}
	if last() != "succeeded" {
		t.Fatalf("first refresh audited %q", last())
	}

	// A run that failed: the entry says failed although the stored run before
	// it succeeded.
	failedRun := banking.RefreshRun{ID: "01JZ0000000000000000000099", ConnectionID: c.ID, Outcome: banking.RefreshFailed}
	e.svc.Engine = recordingRefresher{res: bankimport.Result{Run: failedRun}, err: &banking.Error{Kind: banking.KindRetryable, Provider: "fake", Op: "accounts"}}
	if _, err := e.svc.Refresh(e.ctx, svcAlice, c.ID); err != nil {
		t.Fatalf("a failed run is not an error of the call: %v", err)
	}
	if last() != "failed" {
		t.Errorf("audited outcome = %q, want failed (the returned run, not the previous one)", last())
	}

	before := len(e.actions(audit.BankRefreshRequested))
	e.svc.Engine = recordingRefresher{err: bankimport.ErrNotRefreshable}
	if _, err := e.svc.Refresh(e.ctx, svcAlice, c.ID); !errors.Is(err, bankconnect.ErrNotActive) {
		t.Errorf("ErrNotRefreshable = %v, want ErrNotActive", err)
	}
	boom := errors.New("database is locked")
	e.svc.Engine = recordingRefresher{err: boom}
	if _, err := e.svc.Refresh(e.ctx, svcAlice, c.ID); !errors.Is(err, boom) {
		t.Errorf("a pre-run error = %v, want it returned", err)
	}
	e.svc.Engine = recordingRefresher{}
	if _, err := e.svc.Refresh(e.ctx, svcAlice, c.ID); err == nil {
		t.Error("an engine that returns neither a run nor an error must not look like success")
	}
	if n := len(e.actions(audit.BankRefreshRequested)); n != before {
		t.Errorf("%d refresh entries were written for refreshes that never ran", n-before)
	}
}

// F4: the consent state has a lifetime and an owner.
func TestExpiredConsentIsRefusedAndFailsTheConnection(t *testing.T) {
	e := newSvcEnv(t)
	id, state := e.start(svcAlice)
	e.now = e.now.Add(bankconnect.DefaultConsentTTL + time.Second)
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); !errors.Is(err, bankconnect.ErrConsentExpired) {
		t.Fatalf("completion after the TTL = %v, want ErrConsentExpired", err)
	}
	if n := e.prov.completeCalls(); n != 0 {
		t.Errorf("the provider was called %d times for an expired consent", n)
	}
	c := e.conn(id)
	if c.Status != banking.ConnectionFailed || c.LastErrorCode != banking.ConsentExpiredCode {
		t.Errorf("connection = %s %q, want failed consent_expired", c.Status, c.LastErrorCode)
	}
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); !errors.Is(err, bankconnect.ErrUnknownConsent) {
		t.Errorf("an expired state stays spent: %v", err)
	}
}

func TestConsentJustInsideTheTTLCompletes(t *testing.T) {
	e := newSvcEnv(t)
	_, state := e.start(svcAlice)
	e.now = e.now.Add(bankconnect.DefaultConsentTTL - time.Second)
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); err != nil {
		t.Fatal(err)
	}
}

func TestConsentTTLIsConfigurable(t *testing.T) {
	e := newSvcEnv(t)
	e.svc.ConsentTTL = 5 * time.Minute
	_, state := e.start(svcAlice)
	e.now = e.now.Add(6 * time.Minute)
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); !errors.Is(err, bankconnect.ErrConsentExpired) {
		t.Fatalf("= %v", err)
	}
}

func TestAbandonedPendingConnectionExpiresWhenListed(t *testing.T) {
	e := newSvcEnv(t)
	id, _ := e.start(svcAlice)
	e.now = e.now.Add(2 * time.Hour)
	views, err := e.svc.List(e.ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("list = %v, %v", views, err)
	}
	if c := views[0].Connection; c.ID != id || c.Status != banking.ConnectionFailed || c.LastErrorCode != banking.ConsentExpiredCode {
		t.Errorf("abandoned connection = %s %q", c.Status, c.LastErrorCode)
	}
}

func TestExpiredReconnectKeepsTheConnectionAsItWas(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	st, err := e.svc.Reconnect(e.ctx, svcAlice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * time.Hour)
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(stateOf(t, st.AuthorizationURL))); !errors.Is(err, bankconnect.ErrConsentExpired) {
		t.Fatalf("= %v", err)
	}
	got := e.conn(c.ID)
	if got.Status != banking.ConnectionActive || got.Session.Reveal() != c.Session.Reveal() {
		t.Errorf("an expired reconnect changed the connection: %s", got.Status)
	}
}

func TestConsentCannotBeCompletedByAnotherUser(t *testing.T) {
	e := newSvcEnv(t)
	id, state := e.start(svcAlice)
	if _, err := e.svc.Complete(e.ctx, svcBob, svcRedirect(state)); !errors.Is(err, bankconnect.ErrWrongInitiator) {
		t.Fatalf("svcBob completing svcAlice's consent = %v", err)
	}
	if n := e.prov.completeCalls(); n != 0 {
		t.Errorf("the provider was called %d times for another user's consent", n)
	}
	if c := e.conn(id); c.Status != banking.ConnectionPending {
		t.Fatalf("status = %s", c.Status)
	}
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); err != nil {
		t.Errorf("the refused attempt must leave the state valid for svcAlice: %v", err)
	}
}

func TestTokenOfTheSameUserMayComplete(t *testing.T) {
	e := newSvcEnv(t)
	_, state := e.start(svcAlice)
	token := audit.Actor{Kind: audit.ActorToken, UserID: svcAlice.UserID, TokenID: "01JZ00000000000000000000C3"}
	if _, err := e.svc.Complete(e.ctx, token, svcRedirect(state)); err != nil {
		t.Errorf("a token acts as its owner: %v", err)
	}
}

// F6: a store-only transition and its audit entry are kept or lost together.
func TestAuditFailureUndoesStoreOnlyTransitions(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	good := e.facts
	e.facts = bankfacts.New(e.db, nil).WithAudit(failingAudit{})
	e.svc.Facts = e.facts

	if _, err := e.svc.Start(e.ctx, svcAlice, bankconnect.Start{Application: e.fake.Capabilities().Application, InstitutionID: bankingtest.InstitutionID, Country: bankingtest.InstitutionCountry}); err == nil {
		t.Error("Start must fail when its audit entry cannot be written")
	}
	if cs, _ := good.Connections(e.ctx); len(cs) != 1 {
		t.Errorf("%d connections, want the one from before: Start left a row without its audit entry", len(cs))
	}
	if _, err := e.svc.Disconnect(e.ctx, svcAlice, c.ID, e.conn(c.ID).Version); err == nil {
		t.Error("Disconnect must fail when its audit entry cannot be written")
	}
	if got := e.conn(c.ID); got.Status != banking.ConnectionActive {
		t.Errorf("status = %s: the disconnect was kept without its audit entry", got.Status)
	}
	if _, err := e.svc.Reconnect(e.ctx, svcAlice, c.ID, 0); err == nil {
		t.Error("Reconnect must fail when its audit entry cannot be written")
	}
}

func TestFailedAuditOnCompletionEndsTheNewSessionAndLeavesNoOpenConnection(t *testing.T) {
	e := newSvcEnv(t)
	_, state := e.start(svcAlice)
	e.facts = bankfacts.New(e.db, nil).WithAudit(failingAudit{})
	e.svc.Facts = e.facts
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); err == nil {
		t.Fatal("completion must fail when its audit entry cannot be written")
	}
	calls := e.fake.Calls()
	if calls[len(calls)-1] != bankingtest.OpEndSession {
		t.Errorf("the session the provider granted must be ended: %v", calls)
	}
	cs, _ := e.facts.Connections(e.ctx)
	if len(cs) != 1 || cs[0].Status != banking.ConnectionFailed || cs[0].LastErrorCode != "activation_failed" {
		t.Errorf("connections = %+v, want one failed (activation_failed), not active and not waiting", cs)
	}
}

func TestDisconnectedMeanwhileIsNotResurrected(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	st, err := e.svc.Reconnect(e.ctx, svcAlice, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.prov.entered = make(chan struct{}, 1)
	e.prov.release = make(chan struct{})
	res := make(chan error, 1)
	go func() {
		_, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(stateOf(t, st.AuthorizationURL)))
		res <- err
	}()
	<-e.prov.entered
	if _, err := e.svc.Disconnect(e.ctx, svcAlice, c.ID, e.conn(c.ID).Version); err != nil {
		t.Fatal(err)
	}
	close(e.prov.release)
	if err := <-res; !errors.Is(err, bankconnect.ErrDisconnected) {
		t.Errorf("completion after a disconnect = %v, want ErrDisconnected", err)
	}
	if got := e.conn(c.ID); got.Status != banking.ConnectionDisconnected {
		t.Errorf("status = %s, want disconnected", got.Status)
	}
}

// svcSlow holds Accounts (the first provider call of a refresh) until released
// and records whether its context was cancelled while held.
type svcSlow struct {
	banking.Provider
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	err     error
}

func (p *svcSlow) Accounts(ctx context.Context, s banking.Sensitive) ([]banking.Account, error) {
	p.entered <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	p.mu.Lock()
	p.err = ctx.Err()
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return p.Provider.Accounts(ctx, s)
}

// D40.4 with the real engine and store: a refresh that reaches the deadline is
// not cancelled and not recorded as failed. The answer says it is running; the
// run then finishes as a success and is audited.
func TestRefreshPastTheDeadlineIsNotCancelledOrFailed(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	slow := &svcSlow{Provider: e.fake, entered: make(chan struct{}, 1), release: make(chan struct{})}
	e.svc.Providers = map[string]banking.Provider{e.fake.Capabilities().Application: slow}
	e.svc.RefreshTimeout = 50 * time.Millisecond
	v, err := e.svc.Refresh(e.ctx, svcAlice, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.RefreshRunning {
		t.Fatalf("answer at the deadline: running=%v, want running", v.RefreshRunning)
	}
	// The deadline can fire before the detached run has been recorded (a
	// loaded machine), so the stored run is checked once the provider call,
	// which follows the recording, has been entered.
	<-slow.entered
	if v, err := e.svc.View(e.ctx, c.ID); err != nil || v.LastRun == nil || v.LastRun.Outcome != banking.RefreshRunning {
		t.Fatalf("stored state while the run is held: err=%v view=%+v, want a running run", err, v)
	}
	if n := len(e.actions(audit.BankRefreshRequested)); n != 0 {
		t.Errorf("%d refresh entries while the run is still going", n)
	}
	close(slow.release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, err := e.facts.RefreshRuns(e.ctx, c.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Outcome != banking.RefreshRunning {
			if runs[0].Outcome != banking.RefreshSucceeded {
				t.Fatalf("the run ended %s (%s), want succeeded", runs[0].Outcome, runs[0].ErrorKind)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	slow.mu.Lock()
	cancelled := slow.err
	slow.mu.Unlock()
	if cancelled != nil {
		t.Errorf("the provider call was cancelled: %v", cancelled)
	}
	for len(e.actions(audit.BankRefreshRequested)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no audit entry after the run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := e.actions(audit.BankRefreshRequested)[0].Details["outcome"]; got != "succeeded" {
		t.Errorf("audit outcome = %q, want succeeded", got)
	}
	if v, err := e.svc.View(e.ctx, c.ID); err != nil || v.RefreshRunning {
		t.Errorf("after the run: running=%v err=%v", v.RefreshRunning, err)
	}
}

// Shutdown waits for a refresh that outlived its request: Wait blocks while the
// run is held, gives up when its context ends, and returns once the run and its
// audit entry are done.
func TestWaitCoversADetachedRefresh(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	slow := &svcSlow{Provider: e.fake, entered: make(chan struct{}, 1), release: make(chan struct{})}
	e.svc.Providers = map[string]banking.Provider{e.fake.Capabilities().Application: slow}
	e.svc.RefreshTimeout = 50 * time.Millisecond
	if _, err := e.svc.Refresh(e.ctx, svcAlice, c.ID); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := e.svc.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait while the run is held = %v, want the context's deadline error", err)
	}
	close(slow.release)
	long, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := e.svc.Wait(long); err != nil {
		t.Fatalf("Wait after release: %v", err)
	}
	if n := len(e.actions(audit.BankRefreshRequested)); n != 1 {
		t.Errorf("%d refresh audit entries after Wait, want 1", n)
	}
}

// R1: a disconnected connection can be reconnected; the guard only refuses a
// disconnect that happened after the consent was claimed.
func TestReconnectAfterDisconnectCompletes(t *testing.T) {
	e := newSvcEnv(t)
	c := e.connect(svcAlice)
	dc, err := e.svc.Disconnect(e.ctx, svcAlice, c.ID, e.conn(c.ID).Version)
	if err != nil || dc.Status != banking.ConnectionDisconnected {
		t.Fatalf("disconnect: %v %s", err, dc.Status)
	}
	st, err := e.svc.Reconnect(e.ctx, svcAlice, c.ID, 0)
	if err != nil {
		t.Fatalf("reconnect of a disconnected connection: %v", err)
	}
	state := stateOf(t, st.AuthorizationURL)

	// A transient outage keeps the redirect usable (ReleaseConsent on a
	// connection claimed as disconnected).
	e.fake.FailNext(bankingtest.OpCompleteConsent, bankingtest.Failure(bankingtest.OpCompleteConsent, banking.KindRetryable, "", "TIMEOUT", 0))
	if _, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state)); err == nil {
		t.Fatal("completion during an outage succeeded")
	}
	done, err := e.svc.Complete(e.ctx, svcAlice, svcRedirect(state))
	if err != nil {
		t.Fatalf("complete after reconnect: %v", err)
	}
	if done.Connection.Status != banking.ConnectionActive || e.conn(c.ID).Status != banking.ConnectionActive {
		t.Errorf("status = %s, want active", e.conn(c.ID).Status)
	}
	if got := e.actions(audit.BankConnectionConnected); len(got) != 2 {
		t.Errorf("%d connected entries, want 2 (connect and reconnect)", len(got))
	}
}
