package fx

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Triggers of a refresh run.
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
)

// Run outcomes.
const (
	RunSucceeded = "succeeded" // every currency was refreshed
	RunPartial   = "partial"   // some were refreshed, then a failure stopped or skipped the rest
	RunFailed    = "failed"    // nothing was refreshed
)

// DefaultManualGap is the least time between two manual refreshes: the rate
// service is free and unauthenticated, and a person pressing a button must
// not turn Ledger into a load generator.
const DefaultManualGap = time.Minute

// RunStatus is the outcome of one refresh run, for the status endpoint, the
// audit log and metrics. It holds no URL, body or transport message.
type RunStatus struct {
	Trigger    string
	StartedAt  time.Time
	FinishedAt time.Time
	// Outcome is RunSucceeded, RunPartial or RunFailed.
	Outcome string
	// Refreshed counts the currencies whose rate was fetched and cached.
	Refreshed int
	// ErrorKind and ErrorCode classify the first failure: a provider
	// [ErrorKind] ("unavailable", "rate_limited", "permanent"), or "internal"
	// for Ledger's own failures (the currencies could not be listed); the
	// code is the provider's short code, or "timeout" when the run's time
	// ran out. Both are empty on success.
	ErrorKind string
	ErrorCode string
}

// Meter receives one call per finished refresh run. A run cut short because
// Ledger is stopping is not reported.
type Meter interface {
	RatesRefreshed(trigger string, success bool, at time.Time)
}

// ErrRefreshInProgress is returned by [RefreshJob.Request] while a run is
// already going on.
var ErrRefreshInProgress = errors.New("fx: a rate refresh is already running")

// ErrRefreshIdle is returned by [RefreshJob.Request] when the job has no
// target currency to fetch rates to.
var ErrRefreshIdle = errors.New("fx: the rate refresh has no reporting currency")

// TooSoonError is returned by [RefreshJob.Request] when the last manual
// refresh was less than the manual gap ago.
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string {
	return "fx: a rate refresh was requested too recently; try again in " + e.RetryAfter.Round(time.Second).String()
}

type runState struct {
	mu          sync.Mutex
	last        *RunStatus
	lastSuccess time.Time
	lastManual  time.Time
}

func (j *RefreshJob) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Last returns the most recent finished run (manual or scheduled), or false
// when none has finished since the process started.
func (j *RefreshJob) Last() (RunStatus, bool) {
	j.st.mu.Lock()
	defer j.st.mu.Unlock()
	if j.st.last == nil {
		return RunStatus{}, false
	}
	return *j.st.last, true
}

// LastSuccess is when a run last refreshed every currency, or the zero time.
func (j *RefreshJob) LastSuccess() time.Time {
	j.st.mu.Lock()
	defer j.st.mu.Unlock()
	return j.st.lastSuccess
}

// run is one cycle with its status recorded. The caller holds j.runMu.
func (j *RefreshJob) run(ctx context.Context, failures int, trigger string) (Outcome, RunStatus) {
	st := RunStatus{Trigger: trigger, StartedAt: j.now().UTC()}
	out := j.Cycle(ctx, failures)
	st.FinishedAt = j.now().UTC()
	st.Refreshed = out.Refreshed
	switch {
	case out.Err == nil:
		st.Outcome = RunSucceeded
	case out.Refreshed > 0:
		st.Outcome = RunPartial
	default:
		st.Outcome = RunFailed
	}
	if out.Err != nil {
		st.ErrorKind, st.ErrorCode = "internal", "failed"
		var pe *ProviderError
		switch {
		case errors.As(out.Err, &pe):
			st.ErrorKind, st.ErrorCode = string(pe.Kind), pe.Code
			if st.ErrorCode == "" {
				st.ErrorCode = string(pe.Kind)
			}
		case errors.Is(out.Err, context.DeadlineExceeded):
			st.ErrorCode = "timeout"
		}
	}
	// A run ended by cancellation (shutdown, a caller that went away) says
	// nothing about the provider: it is returned but neither remembered nor
	// counted.
	if errors.Is(ctx.Err(), context.Canceled) {
		return out, st
	}
	j.st.mu.Lock()
	cp := st
	j.st.last = &cp
	if st.Outcome == RunSucceeded {
		j.st.lastSuccess = st.FinishedAt
	}
	j.st.mu.Unlock()
	if j.Meter != nil {
		j.Meter.RatesRefreshed(trigger, st.Outcome == RunSucceeded, st.FinishedAt)
	}
	return out, st
}

// Request runs one refresh now, for a person asking for it. It is bounded by
// ctx (the caller sets a deadline), never overlaps another run
// ([ErrRefreshInProgress]) and is limited to one per ManualGap
// ([TooSoonError]). A run that fails is not an error: its status says so and
// the cache is left as it was. The error is for those refusals and for a
// cancelled ctx.
func (j *RefreshJob) Request(ctx context.Context) (RunStatus, error) {
	if j.Service == nil || j.Currencies == nil || j.Target.IsZero() {
		return RunStatus{}, ErrRefreshIdle
	}
	if !j.runMu.TryLock() {
		return RunStatus{}, ErrRefreshInProgress
	}
	defer j.runMu.Unlock()
	gap := j.ManualGap
	if gap <= 0 {
		gap = DefaultManualGap
	}
	j.st.mu.Lock()
	since := j.now().Sub(j.st.lastManual)
	tooSoon := !j.st.lastManual.IsZero() && since < gap
	if !tooSoon {
		j.st.lastManual = j.now()
	}
	j.st.mu.Unlock()
	if tooSoon {
		return RunStatus{}, &TooSoonError{RetryAfter: gap - since}
	}
	_, st := j.run(ctx, 0, TriggerManual)
	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		return st, err
	}
	return st, nil
}
