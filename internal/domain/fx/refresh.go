package fx

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// RefreshJob is the daily rate refresh (D11): every Interval it asks the
// provider, through [Service.Refresh], for the latest rate from each
// currency Ledger holds to the reporting currency, and caches the answers.
//
// It only ever adds to the cache. A failed cycle changes nothing already
// cached, so conversions keep using the last rates until they age out under
// the freshness policy. After a failure the job retries sooner than
// Interval, backing off (and honouring a provider's Retry-After), but never
// later than Interval.
type RefreshJob struct {
	Service *Service
	// Target is the currency rates are fetched to, normally the reporting
	// currency. A zero Target makes the job idle: there is nothing to
	// convert to.
	Target money.Currency
	// Currencies lists the currencies in use, for example those of bank
	// accounts and commitments. Target and duplicates are skipped.
	Currencies func(ctx context.Context) ([]money.Currency, error)
	// Interval is the time between successful cycles; D11 says daily.
	Interval time.Duration
	// Log receives one line per cycle outcome; defaults to discard.
	Log *slog.Logger
	// After returns a channel that fires after d; defaults to time.After.
	// Tests inject a fake clock.
	After func(d time.Duration) <-chan time.Time
	// Meter, when set, is told of every finished run.
	Meter Meter
	// Now is the clock for run times; defaults to time.Now.
	Now func() time.Time
	// ManualGap is the least time between manual refreshes
	// ([RefreshJob.Request]); defaults to [DefaultManualGap].
	ManualGap time.Duration

	runMu sync.Mutex // one run at a time, scheduled or manual
	st    runState
}

// FirstBackoff is the wait after the first failed cycle; it doubles per
// consecutive failure up to the job's Interval.
const FirstBackoff = 5 * time.Minute

// Outcome of one cycle, for logs and tests.
type Outcome struct {
	// Refreshed counts the currencies whose rate was fetched and cached.
	Refreshed int
	// Err is the first failure of the cycle; nil when every currency was
	// refreshed.
	Err error
	// Wait is how long until the next cycle.
	Wait time.Duration
	// Backoff is true when the failure was transient (the provider did not
	// answer, rate limited, or the currencies could not be listed), so Wait
	// is a shortened retry. A permanent failure of one currency sets Err
	// but not Backoff: retrying sooner would fail the same way.
	Backoff bool
}

// Run refreshes immediately and then repeats until ctx ends. It returns
// ctx.Err().
func (j *RefreshJob) Run(ctx context.Context) error {
	if j.Service == nil || j.Currencies == nil || j.Interval <= 0 {
		return errors.New("fx: refresh job needs a service, a currency source and a positive interval")
	}
	after := j.After
	if after == nil {
		after = time.After
	}
	if j.Target.IsZero() {
		j.log().Info("exchange-rate refresh is idle: no reporting currency is configured")
		<-ctx.Done()
		return ctx.Err()
	}
	failures := 0
	for {
		j.runMu.Lock()
		out, _ := j.run(ctx, failures, TriggerScheduled)
		j.runMu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if out.Backoff {
			failures++
		} else {
			failures = 0
		}
		wait := out.Wait
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-after(wait):
		case <-j.wakeCh():
			// A manual refresh succeeded meanwhile: forget the failures and
			// wait a normal interval from now.
			failures = 0
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-after(j.Interval):
			}
		}
	}
}

func (j *RefreshJob) log() *slog.Logger {
	if j.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return j.Log
}

// Cycle runs one refresh. failures is the number of consecutive failed
// cycles before this one; it sets the wait after another failure.
func (j *RefreshJob) Cycle(ctx context.Context, failures int) Outcome {
	var out Outcome
	currencies, err := j.Currencies(ctx)
	if err != nil {
		out.Err, out.Backoff = err, true
		out.Wait = j.backoff(failures, 0)
		if ctx.Err() == nil {
			j.log().Warn("exchange-rate refresh could not list the currencies in use; cached rates stay in use", "error", err.Error(), "retry_in", out.Wait.String())
		}
		return out
	}
	seen := map[money.Currency]bool{j.Target: true}
	var retryAfter time.Duration
	transient := false
	for _, c := range currencies {
		if c.IsZero() || seen[c] {
			continue
		}
		seen[c] = true
		if ctx.Err() != nil {
			out.Err = ctx.Err()
			return out
		}
		_, err := j.Service.Refresh(ctx, c, []money.Currency{j.Target})
		if err == nil {
			out.Refreshed++
			continue
		}
		if ctx.Err() != nil {
			out.Err = ctx.Err()
			return out
		}
		var pe *ProviderError
		errors.As(err, &pe)
		if out.Err == nil {
			out.Err = err
		}
		j.log().Warn("exchange-rate refresh failed; cached rates stay in use", "base", c.Code(), "quote", j.Target.Code(), "error", err.Error())
		if pe != nil && pe.Kind == KindPermanent {
			// This currency cannot be served; the others still can.
			continue
		}
		// Unavailable, rate limited or paused: the provider is not
		// answering, so asking for more currencies would only add load.
		transient = true
		if pe != nil {
			retryAfter = pe.RetryAfter
		}
		break
	}
	out.Wait = j.Interval
	if transient {
		out.Backoff = true
		out.Wait = j.backoff(failures, retryAfter)
	}
	if out.Err == nil {
		j.log().Info("exchange rates refreshed", "currencies", out.Refreshed, "quote", j.Target.Code(), "next_in", out.Wait.String())
	}
	return out
}

// backoff is the wait after failure number failures+1: FirstBackoff doubling
// per consecutive failure, at least retryAfter, at most Interval.
func (j *RefreshJob) backoff(failures int, retryAfter time.Duration) time.Duration {
	d := FirstBackoff
	for i := 0; i < failures && d < j.Interval; i++ {
		d *= 2
	}
	return min(max(d, retryAfter), j.Interval)
}
