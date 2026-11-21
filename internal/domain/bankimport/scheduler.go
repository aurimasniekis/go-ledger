package bankimport

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// SchedulerJobName is the "job" log field and metric label of the scheduler's
// own passes. Each refresh it starts is a separate "bank_refresh" job whose
// job_id is the run id ([JobName]).
const SchedulerJobName = "bank_refresh_scheduler"

// Defaults for [SchedulerOptions]. Interval, backoff and rate-limit values are
// proposals (ADR-0028), not operator decisions; D17 fixes only the retry
// shape, the 24 h staleness and the log fields.
const (
	// DefaultInterval is the time between successful refreshes of a
	// connection: four a day, the background-fetch budget banks commonly allow.
	DefaultInterval = 6 * time.Hour
	// DefaultFailureBackoff is the first delay after a failed refresh; it
	// doubles per consecutive failure.
	DefaultFailureBackoff = 15 * time.Minute
	// DefaultMaxBackoff caps the failure backoff.
	DefaultMaxBackoff = 6 * time.Hour
	// DefaultRateLimitBackoff is the wait after a rate limit that named no
	// Retry-After (research R1: Enable Banking recommends 6 h).
	DefaultRateLimitBackoff = 6 * time.Hour
	// DefaultMaxRetryAfter caps a provider's Retry-After, so a bad header
	// cannot park a connection for good.
	DefaultMaxRetryAfter = 24 * time.Hour
	// DefaultPollEvery is the longest the scheduler sleeps between passes, so
	// new connections and changed states are noticed.
	DefaultPollEvery = time.Minute

	budgetWindow = 24 * time.Hour
	runHistory   = 64
)

// ScheduleStore is what the scheduler reads. banking.FactStore implements it.
type ScheduleStore interface {
	Connections(ctx context.Context) ([]banking.Connection, error)
	RefreshRuns(ctx context.Context, id banking.ConnectionID, limit int) ([]banking.RefreshRun, error)
	Accounts(ctx context.Context, id banking.ConnectionID) ([]banking.StoredAccount, error)
	AccountSync(ctx context.Context, id banking.AccountID) (banking.AccountSync, error)
}

// Refresher runs one refresh. *Engine implements it.
type Refresher interface {
	Refresh(ctx context.Context, id banking.ConnectionID, p banking.Provider) (Result, error)
}

// SchedulerMeter receives the scheduler's measurements. internal/metrics.App
// implements it.
type SchedulerMeter interface {
	JobDone(job string, ok bool, took time.Duration)
	// SetBankFreshness sets, for every freshness state, how many accounts are
	// in it.
	SetBankFreshness(counts map[string]int)
}

// SchedulerOptions configure a [Scheduler]. Zero values take the defaults.
type SchedulerOptions struct {
	Now func() time.Time
	// Wait sleeps between passes; nil waits on a timer and stops early when
	// ctx ends. Tests replace it to advance a fake clock.
	Wait func(ctx context.Context, d time.Duration) error
	// Jitter spreads a delay; nil picks uniformly from [d/2, d].
	Jitter func(d time.Duration) time.Duration
	// NewID makes job ids (ULIDs); nil means resource.NewID.
	NewID func() string
	Log   *slog.Logger
	Meter SchedulerMeter

	Interval         time.Duration
	FailureBackoff   time.Duration
	MaxBackoff       time.Duration
	RateLimitBackoff time.Duration
	MaxRetryAfter    time.Duration
	PollEvery        time.Duration
	// StaleAfter is the staleness threshold for the freshness gauge; zero
	// means banking.DefaultStaleAfter (D17: 24 h).
	StaleAfter time.Duration
	// RunLease must match the engine's; zero means banking.DefaultRunLease.
	RunLease time.Duration
}

// ScheduleState says why a connection is, or is not, being refreshed.
type ScheduleState string

const (
	// ScheduleWaiting: refreshed on schedule; the next attempt is NextAttemptAt.
	ScheduleWaiting ScheduleState = "waiting"
	// ScheduleDue: a refresh is due now.
	ScheduleDue ScheduleState = "due"
	// ScheduleRunning: a refresh of the connection is running.
	ScheduleRunning ScheduleState = "running"
	// ScheduleBackoff: the last refresh failed; waiting before the next one.
	ScheduleBackoff ScheduleState = "backoff"
	// ScheduleRateLimited: the provider asked to back off.
	ScheduleRateLimited ScheduleState = "rate_limited"
	// ScheduleBudgetExhausted: the provider's daily background-fetch budget is
	// used up; the next attempt waits for the oldest counted run to age out.
	ScheduleBudgetExhausted ScheduleState = "budget_exhausted"
	// ScheduleNotActive: the connection is not active (reconnect required or
	// disconnected); only the user can change that.
	ScheduleNotActive ScheduleState = "not_active"
	// ScheduleNoProvider: no configured application matches the connection.
	ScheduleNoProvider ScheduleState = "no_provider"
)

// ConnectionStatus is the scheduler's view of one connection, for a status
// endpoint (none exists yet) and for tests. It holds no identifiers of the
// bank side and no payloads.
type ConnectionStatus struct {
	ConnectionID banking.ConnectionID
	Application  string
	Schedule     ScheduleState
	// NextAttemptAt is zero for states that never refresh by themselves.
	NextAttemptAt       time.Time
	ConsecutiveFailures int
	LastErrorKind       banking.ErrorKind
	LastErrorCode       string
	// Freshness is the worst freshness among the connection's accounts.
	Freshness banking.FreshnessState
	Stale     bool
}

// Scheduler refreshes active connections periodically (task B6, ADR-0028).
//
// Its state lives in the refresh-run history (B2), so a restart loses
// nothing: the next attempt, the failure count, the rate-limit wait and the
// daily budget are derived from the runs. Only an exact Retry-After longer
// than the derived wait is kept in memory.
//
// It makes at most one attempt per connection per pass and never repeats a
// failed refresh within a pass; retries of single reads live inside the
// engine (D17). It only calls [Refresher.Refresh], which only reads from the
// provider: consent steps and session ends are never issued from here.
type Scheduler struct {
	store ScheduleStore
	eng   Refresher
	provs map[string]banking.Provider
	o     SchedulerOptions
	log   *slog.Logger

	mu     sync.Mutex
	next   map[banking.ConnectionID]time.Time // in-memory floor for the next attempt
	jit    map[banking.ConnectionID]jittered
	last   map[banking.ConnectionID]banking.FreshnessState
	status []ConnectionStatus
}

// jittered is the attempt time drawn for the backoff after one finished run.
type jittered struct {
	run banking.RefreshRunID
	at  time.Time
}

// NewScheduler returns a scheduler refreshing through eng. providers maps a
// provider application name (banking.Connection.Application) to its adapter.
func NewScheduler(store ScheduleStore, eng Refresher, providers map[string]banking.Provider, o SchedulerOptions) *Scheduler {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Wait == nil {
		o.Wait = sleep
	}
	if o.Jitter == nil {
		o.Jitter = jitter
	}
	if o.NewID == nil {
		o.NewID = resource.NewID
	}
	for _, d := range []struct {
		v *time.Duration
		d time.Duration
	}{{&o.Interval, DefaultInterval}, {&o.FailureBackoff, DefaultFailureBackoff}, {&o.MaxBackoff, DefaultMaxBackoff},
		{&o.RateLimitBackoff, DefaultRateLimitBackoff}, {&o.MaxRetryAfter, DefaultMaxRetryAfter}, {&o.PollEvery, DefaultPollEvery},
		{&o.StaleAfter, banking.DefaultStaleAfter}, {&o.RunLease, banking.DefaultRunLease}} {
		if *d.v <= 0 {
			*d.v = d.d
		}
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Scheduler{store: store, eng: eng, provs: providers, o: o, log: log,
		next: map[banking.ConnectionID]time.Time{}, jit: map[banking.ConnectionID]jittered{}, last: map[banking.ConnectionID]banking.FreshnessState{}}
}

// Run passes until ctx ends, then returns. An in-flight refresh gets the
// cancelled context; the engine records it as interrupted.
func (s *Scheduler) Run(ctx context.Context) {
	for ctx.Err() == nil {
		wait := s.Pass(ctx)
		if s.o.Wait(ctx, wait) != nil {
			return
		}
	}
}

// Status returns the state of every connection as of the latest pass.
func (s *Scheduler) Status() []ConnectionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.status)
}

// plan is when a connection may next be refreshed and why.
type plan struct {
	state    ScheduleState
	at       time.Time
	failures int
	errKind  banking.ErrorKind
	errCode  string
	// rateLimited marks the last failure as a rate limit, for the wait rule.
	rateLimited bool
}

// derive computes the plan from the run history (newest first) at now,
// without jitter: it is the conservative, restart-proof schedule.
func (s *Scheduler) derive(runs []banking.RefreshRun, now time.Time) plan {
	p := plan{state: ScheduleDue, at: now}
	var last *banking.RefreshRun
	for i := range runs {
		r := &runs[i]
		if r.Outcome == banking.RefreshRunning {
			if now.Sub(r.StartedAt) < s.o.RunLease {
				return plan{state: ScheduleRunning, at: r.StartedAt.Add(s.o.RunLease)}
			}
			continue // abandoned: the engine marks it interrupted at the next start
		}
		if last == nil {
			last = r
		}
	}
	if last != nil {
		switch last.Outcome {
		case banking.RefreshSucceeded:
			p.state, p.at = ScheduleWaiting, last.FinishedAt.Add(s.o.Interval)
		case banking.RefreshInterrupted:
			// Cancelled by a shutdown: resume at once; committed windows stay.
		default:
			p.errKind, p.errCode = last.ErrorKind, last.ErrorCode
			p.rateLimited = last.ErrorKind == banking.KindRateLimited
			for _, r := range runs {
				if r.Outcome == banking.RefreshRunning || r.Outcome == banking.RefreshInterrupted {
					continue
				}
				if r.Outcome != banking.RefreshFailed && r.Outcome != banking.RefreshPartial {
					break
				}
				p.failures++
			}
			wait := s.backoff(p.failures)
			p.state = ScheduleBackoff
			if p.rateLimited {
				wait, p.state = max(wait, s.o.RateLimitBackoff), ScheduleRateLimited
			}
			p.at = last.FinishedAt.Add(wait)
		}
	}
	return p
}

// backoff is the failure delay after n consecutive failures: FailureBackoff
// doubling, capped at MaxBackoff.
func (s *Scheduler) backoff(n int) time.Duration {
	d := s.o.FailureBackoff
	for i := 1; i < n && d < s.o.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, s.o.MaxBackoff)
}

// budgetAt is when the provider's daily background-fetch budget allows
// another run, or the zero time when it already does. Every recorded run
// counts, including the user's own, so the estimate errs on the safe side
// (a run is one "fetch": an approximation, ADR-0028).
func budgetAt(runs []banking.RefreshRun, limit int, now time.Time) time.Time {
	if limit <= 0 {
		return time.Time{}
	}
	var starts []time.Time
	for _, r := range runs {
		if now.Sub(r.StartedAt) < budgetWindow {
			starts = append(starts, r.StartedAt)
		}
	}
	if len(starts) < limit {
		return time.Time{}
	}
	slices.SortFunc(starts, func(a, b time.Time) int { return b.Compare(a) })
	return starts[limit-1].Add(budgetWindow)
}

// Pass refreshes every connection that is due, one after another, and
// returns how long to wait before the next pass.
func (s *Scheduler) Pass(ctx context.Context) time.Duration {
	start := time.Now()
	now := s.o.Now().UTC()
	jl := s.log.With("job_id", s.o.NewID(), "job", SchedulerJobName)
	conns, err := s.store.Connections(ctx)
	if err != nil {
		if ctx.Err() == nil {
			jl.Error("listing connections failed", "error", err)
			s.meterJob(false, start)
		}
		return s.o.PollEvery
	}
	wake := now.Add(s.o.PollEvery)
	var statuses []ConnectionStatus
	freshCounts := map[string]int{}
	attempted, failed := 0, 0
	for _, c := range conns {
		if ctx.Err() != nil {
			return 0
		}
		cs := ConnectionStatus{ConnectionID: c.ID, Application: c.Application}
		prov := s.provs[c.Application]
		runs, rerr := s.store.RefreshRuns(ctx, c.ID, runHistory)
		switch {
		case rerr != nil:
			if ctx.Err() == nil {
				jl.Error("reading refresh history failed", "connection_id", string(c.ID), "error", rerr)
			}
			cs.Schedule = ScheduleBackoff
		case c.Status != banking.ConnectionActive:
			cs.Schedule = ScheduleNotActive
		case prov == nil:
			cs.Schedule = ScheduleNoProvider
		default:
			pl, ran, ok := s.attempt(ctx, jl, c, prov, runs, now)
			if ran {
				attempted++
				if !ok {
					failed++
				}
				if ctx.Err() != nil {
					return 0
				}
				// The run changed the history: plan again from it.
				if runs, rerr = s.store.RefreshRuns(ctx, c.ID, runHistory); rerr == nil {
					pl = s.final(c, prov, runs, s.o.Now().UTC(), true)
				}
			}
			cs.Schedule, cs.NextAttemptAt, cs.ConsecutiveFailures = pl.state, pl.at, pl.failures
			cs.LastErrorKind, cs.LastErrorCode = pl.errKind, pl.errCode
			if pl.state != ScheduleRunning && pl.at.Before(wake) {
				wake = pl.at
			}
		}
		cs.Freshness, cs.Stale = s.freshness(ctx, c, freshCounts)
		s.noteFreshness(jl, c.ID, cs.Freshness)
		statuses = append(statuses, cs)
	}
	s.mu.Lock()
	s.status = statuses
	s.mu.Unlock()
	if m := s.o.Meter; m != nil {
		for _, st := range freshnessStates {
			if _, ok := freshCounts[string(st)]; !ok {
				freshCounts[string(st)] = 0
			}
		}
		m.SetBankFreshness(freshCounts)
	}
	if attempted > 0 {
		s.meterJob(failed == 0, start)
		jl.Info("refresh pass finished", "attempted", attempted, "failed", failed)
	}
	return max(wake.Sub(s.o.Now().UTC()), 0)
}

func (s *Scheduler) meterJob(ok bool, start time.Time) {
	if s.o.Meter != nil {
		s.o.Meter.JobDone(SchedulerJobName, ok, time.Since(start))
	}
}

// final combines the derived plan, the in-memory floor and the budget. With
// fresh set, jitter spreads a backoff (a just-finished run); without it the
// derived time is used as is.
func (s *Scheduler) final(c banking.Connection, p banking.Provider, runs []banking.RefreshRun, now time.Time, fresh bool) plan {
	pl := s.derive(runs, now)
	if pl.state == ScheduleRunning {
		return pl
	}
	s.mu.Lock()
	floor := s.next[c.ID]
	s.mu.Unlock()
	if pl.state == ScheduleBackoff || pl.state == ScheduleRateLimited {
		// The jitter is drawn once per finished run and remembered, so later
		// passes keep the same attempt time.
		if i := slices.IndexFunc(runs, func(r banking.RefreshRun) bool { return r.Outcome != banking.RefreshRunning }); i >= 0 {
			r := runs[i]
			s.mu.Lock()
			j, ok := s.jit[c.ID]
			s.mu.Unlock()
			switch {
			case ok && j.run == r.ID:
				pl.at = j.at
			case fresh:
				pl.at = r.FinishedAt.Add(s.o.Jitter(pl.at.Sub(r.FinishedAt)))
				s.mu.Lock()
				s.jit[c.ID] = jittered{run: r.ID, at: pl.at}
				s.mu.Unlock()
			}
		}
	}
	if floor.After(pl.at) {
		pl.at = floor
		if pl.state == ScheduleWaiting || pl.state == ScheduleDue {
			pl.state = ScheduleBackoff
		}
	}
	if b := budgetAt(runs, p.Capabilities().BackgroundFetchesPerDay, now); b.After(pl.at) {
		pl.at, pl.state = b, ScheduleBudgetExhausted
	}
	if !pl.at.After(now) {
		pl.state, pl.at = ScheduleDue, now
	}
	return pl
}

// attempt refreshes c if it is due. ran says whether a refresh was started,
// ok whether it succeeded.
func (s *Scheduler) attempt(ctx context.Context, jl *slog.Logger, c banking.Connection, p banking.Provider, runs []banking.RefreshRun, now time.Time) (pl plan, ran, ok bool) {
	pl = s.final(c, p, runs, now, false)
	if pl.state != ScheduleDue {
		return pl, false, false
	}
	res, err := s.eng.Refresh(ctx, c.ID, p)
	switch {
	case err == nil:
		s.setFloor(c.ID, time.Time{})
		return pl, true, true
	case errors.Is(err, ErrRefreshInProgress):
		// Someone else is refreshing (a user-triggered run): not a failure.
		s.setFloor(c.ID, now.Add(s.o.PollEvery))
		jl.Debug("refresh already running; skipped", "connection_id", string(c.ID))
		return plan{state: ScheduleRunning, at: now.Add(s.o.PollEvery)}, false, false
	case errors.Is(err, ErrNotRefreshable):
		return plan{state: ScheduleNotActive}, false, false
	case ctx.Err() != nil:
		return pl, true, false
	}
	// Keep an exact Retry-After longer than the derived wait; a rate limit
	// without one gets the rate-limit backoff from the derived plan.
	if ra, has := banking.RetryAfter(err); has {
		s.setFloor(c.ID, s.o.Now().UTC().Add(min(ra, s.o.MaxRetryAfter)))
	} else {
		s.setFloor(c.ID, time.Time{})
	}
	jl.Warn("scheduled refresh failed; backing off", "connection_id", string(c.ID), "refresh_run_id", string(res.Run.ID),
		"error_kind", banking.Classify(err), "error_code", res.Run.ErrorCode)
	return pl, true, false
}

func (s *Scheduler) setFloor(id banking.ConnectionID, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.IsZero() {
		delete(s.next, id)
		return
	}
	s.next[id] = t
}

// freshnessStates is the closed set of states, worst first.
var freshnessStates = []banking.FreshnessState{
	banking.FreshnessReconnectRequired, banking.FreshnessDisconnected, banking.FreshnessFailed,
	banking.FreshnessStale, banking.FreshnessNeverRefreshed, banking.FreshnessFresh,
}

// freshness derives the worst freshness among the connection's accounts and
// adds each account to counts. A connection without accounts counts once, so
// a connection that never listed any is not invisible.
func (s *Scheduler) freshness(ctx context.Context, c banking.Connection, counts map[string]int) (banking.FreshnessState, bool) {
	now := s.o.Now().UTC()
	var fs []banking.Freshness
	accs, err := s.store.Accounts(ctx, c.ID)
	if err == nil {
		for _, a := range accs {
			if !a.NoLongerReportedAt.IsZero() {
				continue
			}
			sync, serr := s.store.AccountSync(ctx, a.ID)
			if serr != nil {
				err = serr
				break
			}
			fs = append(fs, banking.AccountFreshness(c.Status, sync, now, s.o.StaleAfter))
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("reading account freshness failed", "connection_id", string(c.ID), "error", err)
		}
		return banking.FreshnessFailed, true
	}
	if len(fs) == 0 {
		fs = append(fs, banking.AccountFreshness(c.Status, banking.AccountSync{}, now, s.o.StaleAfter))
	}
	worst := fs[0]
	for _, f := range fs {
		counts[string(f.State)]++
		if slices.Index(freshnessStates, f.State) < slices.Index(freshnessStates, worst.State) {
			worst = f
		}
	}
	return worst.State, worst.Stale
}

// noteFreshness logs a change of a connection's freshness once.
func (s *Scheduler) noteFreshness(jl *slog.Logger, id banking.ConnectionID, st banking.FreshnessState) {
	s.mu.Lock()
	prev, seen := s.last[id]
	s.last[id] = st
	s.mu.Unlock()
	if seen && prev == st {
		return
	}
	lvl := slog.LevelInfo
	if st != banking.FreshnessFresh && st != banking.FreshnessNeverRefreshed {
		lvl = slog.LevelWarn
	}
	jl.Log(context.Background(), lvl, "bank data freshness", "connection_id", string(id), "freshness", string(st))
}

var _ Refresher = (*Engine)(nil)
