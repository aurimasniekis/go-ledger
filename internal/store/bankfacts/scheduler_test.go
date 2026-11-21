package bankfacts_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
)

// Background refresh scheduler (B6) tests: bankimport.Scheduler with the real
// engine and store, the fake provider and a fake clock. They live here for the
// same reason as the engine tests.

type schedMeter struct {
	mu    sync.Mutex
	jobs  map[string]int
	fresh map[string]int
}

func (m *schedMeter) JobDone(job string, _ bool, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs == nil {
		m.jobs = map[string]int{}
	}
	m.jobs[job]++
}

func (m *schedMeter) SetBankFreshness(c map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fresh = c
}

type schedEnv struct {
	*engineEnv
	meter *schedMeter
	opts  bankimport.SchedulerOptions
	s     *bankimport.Scheduler
}

func newSchedEnv(t *testing.T, tweak func(*bankimport.SchedulerOptions)) *schedEnv {
	t.Helper()
	e := &schedEnv{engineEnv: newEngineEnv(t, banking.ModeApp), meter: &schedMeter{}}
	e.opts = bankimport.SchedulerOptions{
		Now:    e.clock.Now,
		Jitter: func(d time.Duration) time.Duration { return d },
		Meter:  e.meter,
	}
	if tweak != nil {
		tweak(&e.opts)
	}
	e.restart()
	return e
}

// restart builds a new scheduler over the same database: nothing in memory
// survives, as after a process restart.
func (e *schedEnv) restart() {
	e.s = bankimport.NewScheduler(e.facts, e.engine, map[string]banking.Provider{e.h.Capabilities().Application: e.h}, e.opts)
}

func (e *schedEnv) runs() []banking.RefreshRun {
	e.t.Helper()
	r, err := e.facts.RefreshRuns(e.ctx, e.conn.ID, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *schedEnv) status() bankimport.ConnectionStatus {
	e.t.Helper()
	st := e.s.Status()
	if len(st) != 1 {
		e.t.Fatalf("status has %d connections", len(st))
	}
	return st[0]
}

func (e *schedEnv) advance(d time.Duration) { e.clock.now = e.clock.now.Add(d) }

// failAccounts makes the next refresh fail at the account list: for a transient
// failure one call plus the engine's three retries.
func (e *schedEnv) failAccounts(kind banking.ErrorKind, code string, retryAfter time.Duration) {
	var errs []error
	n := 4
	if kind != banking.KindRetryable { // only transient failures are retried
		n = 1
	}
	for range n {
		errs = append(errs, bankingtest.Failure(bankingtest.OpAccounts, kind, banking.ReasonNone, code, retryAfter))
	}
	e.p.FailNext(bankingtest.OpAccounts, errs...)
}

func TestSchedulerRefreshesOnTheIntervalWithAFakeClock(t *testing.T) {
	e := newSchedEnv(t, nil)
	wait := e.s.Pass(e.ctx)
	if n := len(e.runs()); n != 1 {
		t.Fatalf("first pass: %d runs, want 1 (a never-refreshed connection is due at once)", n)
	}
	if wait != bankimport.DefaultPollEvery {
		t.Fatalf("wait = %v, want the poll interval", wait)
	}
	st := e.status()
	if st.Schedule != bankimport.ScheduleWaiting || st.Freshness != banking.FreshnessFresh || st.Stale {
		t.Fatalf("status %+v", st)
	}
	e.advance(bankimport.DefaultInterval - time.Minute)
	if wait := e.s.Pass(e.ctx); len(e.runs()) != 1 || wait != time.Minute {
		t.Fatalf("early pass: %d runs, wait %v; want 1 run and 1m", len(e.runs()), wait)
	}
	e.advance(time.Minute)
	e.s.Pass(e.ctx)
	if n := len(e.runs()); n != 2 {
		t.Fatalf("pass at the interval: %d runs, want 2", n)
	}
	if e.meter.jobs[bankimport.SchedulerJobName] != 2 || e.meter.jobs[bankimport.JobName] != 0 {
		// The engine has no meter here; the scheduler meters its own passes
		// that did work, and idle passes are not counted.
		t.Fatalf("job counts %v", e.meter.jobs)
	}
	if e.meter.fresh[string(banking.FreshnessFresh)] == 0 {
		t.Fatalf("freshness gauge %v", e.meter.fresh)
	}
}

func TestSchedulerBacksOffAfterFailuresAndRecovers(t *testing.T) {
	e := newSchedEnv(t, nil)
	e.failAccounts(banking.KindRetryable, "bank_down", 0)
	e.s.Pass(e.ctx)
	if got := e.p.Calls(); countOp(got, bankingtest.OpAccounts) != 4 {
		t.Fatalf("accounts calls = %d, want 1 + 3 retries and no replay by the scheduler", countOp(got, bankingtest.OpAccounts))
	}
	st := e.status()
	if st.Schedule != bankimport.ScheduleBackoff || st.ConsecutiveFailures != 1 || st.LastErrorKind != banking.KindRetryable || !st.Stale {
		t.Fatalf("status after one failure %+v", st)
	}
	if want := e.clock.now.Add(bankimport.DefaultFailureBackoff); !st.NextAttemptAt.Equal(want) {
		t.Fatalf("next attempt %v, want %v", st.NextAttemptAt, want)
	}
	e.advance(bankimport.DefaultFailureBackoff - time.Second)
	e.s.Pass(e.ctx)
	if len(e.runs()) != 1 {
		t.Fatal("refreshed before the backoff ended")
	}
	e.advance(time.Second)
	e.failAccounts(banking.KindRetryable, "bank_down", 0)
	e.s.Pass(e.ctx)
	if st := e.status(); st.ConsecutiveFailures != 2 || !st.NextAttemptAt.Equal(e.clock.now.Add(2*bankimport.DefaultFailureBackoff)) {
		t.Fatalf("second failure doubles the delay: %+v", st)
	}
	// A restart keeps the schedule: it is derived from the run history.
	e.restart()
	e.advance(2*bankimport.DefaultFailureBackoff - time.Second)
	e.s.Pass(e.ctx)
	if len(e.runs()) != 2 {
		t.Fatal("restart forgot the backoff")
	}
	e.advance(time.Second)
	e.s.Pass(e.ctx)
	if st := e.status(); len(e.runs()) != 3 || st.Schedule != bankimport.ScheduleWaiting || st.ConsecutiveFailures != 0 || st.Freshness != banking.FreshnessFresh {
		t.Fatalf("recovery: %d runs, %+v", len(e.runs()), st)
	}
}

func TestSchedulerBackoffIsBounded(t *testing.T) {
	e := newSchedEnv(t, func(o *bankimport.SchedulerOptions) { o.MaxBackoff = 40 * time.Minute; o.Interval = time.Minute })
	for range 5 {
		e.failAccounts(banking.KindRetryable, "bank_down", 0)
		e.s.Pass(e.ctx)
		e.advance(12 * time.Hour) // spaced out so the daily budget does not bind
	}
	e.failAccounts(banking.KindRetryable, "bank_down", 0)
	e.s.Pass(e.ctx)
	if st := e.status(); !st.NextAttemptAt.Equal(e.clock.now.Add(40 * time.Minute)) {
		t.Fatalf("delay not capped: %+v", st)
	}
}

func TestSchedulerHonoursRateLimitsAndRetryAfter(t *testing.T) {
	e := newSchedEnv(t, nil)
	// Without Retry-After: the 6 h rate-limit backoff, also after a restart.
	e.failAccounts(banking.KindRateLimited, "rate_limit", 0)
	e.s.Pass(e.ctx)
	if st := e.status(); st.Schedule != bankimport.ScheduleRateLimited || !st.NextAttemptAt.Equal(e.clock.now.Add(6*time.Hour)) {
		t.Fatalf("status %+v", st)
	}
	if n := countOp(e.p.Calls(), bankingtest.OpAccounts); n != 1 {
		t.Fatalf("a rate limit without Retry-After was retried: %d calls", n)
	}
	e.restart()
	e.advance(6*time.Hour - time.Second)
	e.s.Pass(e.ctx)
	if len(e.runs()) != 1 {
		t.Fatal("restart forgot the rate limit")
	}
	e.advance(time.Second)
	// A Retry-After beyond 6 h (and beyond what the engine waits) is honoured.
	e.failAccounts(banking.KindRateLimited, "rate_limit", 10*time.Hour)
	e.s.Pass(e.ctx)
	if len(e.runs()) != 2 {
		t.Fatalf("%d runs", len(e.runs()))
	}
	if st := e.status(); !st.NextAttemptAt.Equal(e.clock.now.Add(10 * time.Hour)) {
		t.Fatalf("Retry-After ignored: %+v", st)
	}
	// An absurd one is capped.
	e.advance(10 * time.Hour)
	e.failAccounts(banking.KindRateLimited, "rate_limit", 500*time.Hour)
	e.s.Pass(e.ctx)
	if st := e.status(); !st.NextAttemptAt.Equal(e.clock.now.Add(bankimport.DefaultMaxRetryAfter)) {
		t.Fatalf("Retry-After not capped: %+v", st)
	}
}

func TestSchedulerKeepsToTheDailyFetchBudget(t *testing.T) {
	e := newSchedEnv(t, func(o *bankimport.SchedulerOptions) { o.Interval = time.Hour })
	start := e.clock.now
	for range 4 { // the fake allows four background fetches a day
		e.s.Pass(e.ctx)
		e.advance(time.Hour)
	}
	if len(e.runs()) != 4 {
		t.Fatalf("%d runs", len(e.runs()))
	}
	e.restart() // the budget comes from the history, not from memory
	e.s.Pass(e.ctx)
	if st := e.status(); len(e.runs()) != 4 || st.Schedule != bankimport.ScheduleBudgetExhausted || !st.NextAttemptAt.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("budget not enforced: %d runs, %+v", len(e.runs()), st)
	}
	e.clock.now = start.Add(24*time.Hour - time.Second)
	e.s.Pass(e.ctx)
	if len(e.runs()) != 4 {
		t.Fatal("refreshed before the oldest run left the window")
	}
	e.clock.now = start.Add(24 * time.Hour)
	e.s.Pass(e.ctx)
	if len(e.runs()) != 5 {
		t.Fatalf("%d runs, want 5 once the budget frees up", len(e.runs()))
	}
}

func TestSchedulerNeverIssuesConsentOrDeleteCalls(t *testing.T) {
	e := newSchedEnv(t, nil)
	before := len(e.p.Calls())
	e.failAccounts(banking.KindRetryable, "bank_down", 0)
	e.s.Pass(e.ctx)
	e.advance(time.Hour)
	e.s.Pass(e.ctx)
	e.p.Revoke() // the session dies: the connection needs the user, not a retry
	e.advance(bankimport.DefaultInterval)
	e.s.Pass(e.ctx)
	for _, op := range e.p.Calls()[before:] {
		switch op {
		case bankingtest.OpStartConsent, bankingtest.OpCompleteConsent, bankingtest.OpEndSession, bankingtest.OpInstitutions:
			t.Fatalf("the scheduler called %s", op)
		}
	}
}

func TestSchedulerLeavesAConnectionThatNeedsReconnectAlone(t *testing.T) {
	e := newSchedEnv(t, nil)
	e.s.Pass(e.ctx)
	e.p.Revoke()
	e.advance(bankimport.DefaultInterval)
	e.s.Pass(e.ctx) // discovers the revocation: the engine sets reconnect_required
	n := len(e.runs())
	calls := len(e.p.Calls())
	e.advance(48 * time.Hour)
	e.s.Pass(e.ctx)
	if len(e.runs()) != n || len(e.p.Calls()) != calls {
		t.Fatal("a reconnect-required connection was refreshed again")
	}
	st := e.status()
	if st.Schedule != bankimport.ScheduleNotActive || st.Freshness != banking.FreshnessReconnectRequired || !st.Stale {
		t.Fatalf("status %+v", st)
	}
	if e.meter.fresh[string(banking.FreshnessReconnectRequired)] == 0 {
		t.Fatalf("freshness gauge %v", e.meter.fresh)
	}
}

func TestSchedulerShowsStaleDataAfter24Hours(t *testing.T) {
	e := newSchedEnv(t, func(o *bankimport.SchedulerOptions) { o.Interval = 48 * time.Hour })
	e.s.Pass(e.ctx)
	e.advance(24*time.Hour + time.Minute)
	e.s.Pass(e.ctx)
	if st := e.status(); st.Freshness != banking.FreshnessStale || !st.Stale {
		t.Fatalf("status %+v", st)
	}
	if e.meter.fresh[string(banking.FreshnessStale)] == 0 || e.meter.fresh[string(banking.FreshnessFresh)] != 0 {
		t.Fatalf("freshness gauge %v", e.meter.fresh)
	}
}

func TestSchedulerSkipsAConnectionRefreshedElsewhere(t *testing.T) {
	e := newSchedEnv(t, nil)
	if _, err := e.facts.StartRefresh(e.ctx, e.conn.ID, e.clock.now, 0); err != nil { // a user-triggered run in progress
		t.Fatal(err)
	}
	e.s.Pass(e.ctx)
	if n := countOp(e.p.Calls(), bankingtest.OpAccounts); n != 0 || len(e.runs()) != 1 {
		t.Fatalf("refreshed under a running run: %d calls, %d runs", n, len(e.runs()))
	}
	if st := e.status(); st.Schedule != bankimport.ScheduleRunning || st.ConsecutiveFailures != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestSchedulerShowsAConnectionWithoutAProvider(t *testing.T) {
	e := newSchedEnv(t, nil)
	s := bankimport.NewScheduler(e.facts, e.engine, nil, e.opts)
	s.Pass(e.ctx)
	if len(e.runs()) != 0 {
		t.Fatal("refreshed without a provider")
	}
	if st := s.Status()[0]; st.Schedule != bankimport.ScheduleNoProvider {
		t.Fatalf("status %+v", st)
	}
}

func TestSchedulerRunStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newSchedEnv(t, func(o *bankimport.SchedulerOptions) {
		waits := 0
		o.Wait = func(ctx context.Context, d time.Duration) error {
			waits++
			if waits == 2 {
				cancel()
			}
			return ctx.Err()
		}
	})
	done := make(chan struct{})
	go func() { e.s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestSchedulerCancelledRefreshIsRecordedAsInterruptedAndRetriedAtOnce(t *testing.T) {
	e := newSchedEnv(t, nil)
	ctx, cancel := context.WithCancel(e.ctx)
	e.h.onTx = func(context.Context, int) error { cancel(); return context.Canceled }
	e.s.Pass(ctx)
	rs := e.runs()
	if len(rs) != 1 || rs[0].Outcome != banking.RefreshInterrupted {
		t.Fatalf("runs %+v", rs)
	}
	e.h.onTx = nil
	e.restart()
	e.s.Pass(e.ctx) // no waiting for the interval: the shutdown was not a failure
	if rs := e.runs(); len(rs) != 2 || rs[0].Outcome != banking.RefreshSucceeded {
		t.Fatalf("runs %+v", rs)
	}
}

func countOp(ops []bankingtest.Op, op bankingtest.Op) int {
	n := 0
	for _, o := range ops {
		if o == op {
			n++
		}
	}
	return n
}
