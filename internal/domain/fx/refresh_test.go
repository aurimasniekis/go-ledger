package fx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx/fxtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

type jobEnv struct {
	prov  *fxtest.Provider
	cache *fxtest.Cache
	job   *fx.RefreshJob
	clock *time.Time
}

func newJobEnv(t *testing.T, currencies ...string) *jobEnv {
	t.Helper()
	now := fxtest.T0
	e := &jobEnv{prov: fxtest.NewProvider("p", "ECB"), cache: fxtest.NewCache(), clock: &now}
	for _, c := range []string{"USD", "GBP", "SEK"} {
		e.prov.Publish(c, "EUR", "1.1000", "2026-11-13", "ECB")
	}
	svc, err := fx.NewService(e.cache, e.prov, fx.Options{Now: func() time.Time { return *e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.job = &fx.RefreshJob{
		Service: svc, Target: money.MustParseCurrency("EUR"), Interval: 24 * time.Hour,
		Currencies: func(context.Context) ([]money.Currency, error) {
			var out []money.Currency
			for _, c := range currencies {
				out = append(out, money.MustParseCurrency(c))
			}
			return out, nil
		},
	}
	return e
}

func TestCycleRefreshesEachCurrencyOnceToTheTarget(t *testing.T) {
	e := newJobEnv(t, "USD", "EUR", "GBP", "USD")
	out := e.job.Cycle(t.Context(), 0)
	if out.Err != nil || out.Refreshed != 2 || out.Wait != 24*time.Hour {
		t.Fatalf("outcome = %+v", out)
	}
	calls := e.prov.Calls()
	if len(calls) != 2 || calls[0].Op != "latest" || calls[0].Base.Code() != "USD" || calls[1].Base.Code() != "GBP" || len(calls[0].Quotes) != 1 || calls[0].Quotes[0].Code() != "EUR" {
		t.Fatalf("calls = %+v", calls)
	}
	r, ok, _ := e.cache.Find(t.Context(), "p", "ECB", fxtest.Pair("USD", "EUR"), calendar.Date{})
	if !ok || r.Value.String() != "1.1000" || !r.RetrievedAt.Equal(fxtest.T0) {
		t.Fatalf("cached = %+v ok %v", r, ok)
	}
}

func TestOutageKeepsCacheAndBacksOffWithoutHammering(t *testing.T) {
	e := newJobEnv(t, "USD", "GBP", "SEK")
	if out := e.job.Cycle(t.Context(), 0); out.Err != nil {
		t.Fatal(out.Err)
	}
	before, _, _ := e.cache.Find(t.Context(), "p", "ECB", fxtest.Pair("USD", "EUR"), calendar.Date{})
	e.prov.SetDown(&fx.ProviderError{Kind: fx.KindUnavailable, Provider: "p", Op: "latest"})
	n := len(e.prov.Calls())
	*e.clock = e.clock.Add(24 * time.Hour)

	var waits []time.Duration
	for failures := range 8 {
		out := e.job.Cycle(t.Context(), failures)
		if !errors.Is(out.Err, fx.ErrUnavailable) {
			t.Fatalf("err = %v", out.Err)
		}
		waits = append(waits, out.Wait)
	}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 160 * time.Minute, 320 * time.Minute, 640 * time.Minute}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i, waits[i], want[i])
		}
	}
	// The pause stops further calls inside one cycle, and a later cycle inside
	// the pause makes none at all: one call across the whole burst.
	if got := len(e.prov.Calls()) - n; got != 1 {
		t.Errorf("provider was called %d times during the outage burst, want 1", got)
	}
	after, ok, _ := e.cache.Find(t.Context(), "p", "ECB", fxtest.Pair("USD", "EUR"), calendar.Date{})
	if !ok || after.Value.String() != before.Value.String() || !after.RetrievedAt.Equal(before.RetrievedAt) {
		t.Errorf("cache changed by a failed refresh: %+v -> %+v", before, after)
	}
}

func TestBackoffNeverExceedsTheIntervalAndHonoursRetryAfter(t *testing.T) {
	e := newJobEnv(t, "USD")
	e.job.Interval = time.Hour
	e.prov.SetDown(&fx.ProviderError{Kind: fx.KindUnavailable, Provider: "p", Op: "latest"})
	if out := e.job.Cycle(t.Context(), 20); out.Wait != time.Hour {
		t.Errorf("capped wait = %v", out.Wait)
	}
	e = newJobEnv(t, "USD")
	e.prov.SetDown(&fx.ProviderError{Kind: fx.KindRateLimited, Provider: "p", Op: "latest", RetryAfter: 3 * time.Hour})
	if out := e.job.Cycle(t.Context(), 0); out.Wait != 3*time.Hour || !errors.Is(out.Err, fx.ErrRateLimited) {
		t.Errorf("rate limited: %+v", out)
	}
}

func TestPermanentFailureSkipsOnlyThatCurrency(t *testing.T) {
	e := newJobEnv(t, "XXX", "USD")
	e.prov.FailNext(&fx.ProviderError{Kind: fx.KindPermanent, Provider: "p", Op: "latest", Code: "http_422"})
	out := e.job.Cycle(t.Context(), 0)
	// A permanent failure is not an outage: the job waits the full interval
	// and does not count a consecutive failure.
	if out.Backoff || out.Wait != e.job.Interval {
		t.Errorf("permanent failure backs off: %+v", out)
	}
	if out.Refreshed != 1 || !errors.Is(out.Err, fx.ErrPermanent) || len(e.prov.Calls()) != 2 {
		t.Fatalf("outcome %+v, %d calls", out, len(e.prov.Calls()))
	}
	if _, ok, _ := e.cache.Find(t.Context(), "p", "ECB", fxtest.Pair("USD", "EUR"), calendar.Date{}); !ok {
		t.Error("the currency after the permanent failure was not refreshed")
	}
}

func TestTransientFailureAfterAPermanentOneStillBacksOff(t *testing.T) {
	e := newJobEnv(t, "XXX", "USD")
	e.prov.FailNext(&fx.ProviderError{Kind: fx.KindPermanent, Provider: "p", Op: "latest", Code: "http_422"})
	e.prov.FailNext(&fx.ProviderError{Kind: fx.KindUnavailable, Provider: "p", Op: "latest"})
	out := e.job.Cycle(t.Context(), 0)
	if !out.Backoff || out.Wait != fx.FirstBackoff {
		t.Errorf("outcome %+v", out)
	}
}

func TestCurrencyListFailureKeepsCache(t *testing.T) {
	e := newJobEnv(t)
	e.job.Currencies = func(context.Context) ([]money.Currency, error) { return nil, errors.New("db busy") }
	out := e.job.Cycle(t.Context(), 0)
	if out.Err == nil || out.Wait != 5*time.Minute || len(e.prov.Calls()) != 0 {
		t.Fatalf("outcome %+v", out)
	}
}

func TestRunRefreshesImmediatelyThenEveryInterval(t *testing.T) {
	e := newJobEnv(t, "USD")
	ticks := make(chan time.Time)
	waits := make(chan time.Duration, 8)
	e.job.After = func(d time.Duration) <-chan time.Time { waits <- d; return ticks }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.job.Run(ctx) }()
	if d := <-waits; d != 24*time.Hour {
		t.Fatalf("first wait = %v", d)
	}
	ticks <- time.Time{}
	<-waits
	if got := len(e.prov.Calls()); got != 2 {
		t.Errorf("%d calls after one interval, want 2 (startup + one)", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v", err)
	}
}

func TestRunIsIdleWithoutATargetAndRejectsBadSetup(t *testing.T) {
	e := newJobEnv(t, "USD")
	e.job.Target = money.Currency{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.job.Run(ctx) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || len(e.prov.Calls()) != 0 {
		t.Errorf("idle job: %v, %d calls", err, len(e.prov.Calls()))
	}
	if err := (&fx.RefreshJob{}).Run(t.Context()); err == nil {
		t.Error("empty job ran")
	}
}

type meterSpy struct{ runs []string }

func (m *meterSpy) RatesRefreshed(trigger string, ok bool, _ time.Time) {
	o := "failure"
	if ok {
		o = "success"
	}
	m.runs = append(m.runs, trigger+"/"+o)
}

func TestRequestRecordsStatusAndMetersAndLimitsManualRuns(t *testing.T) {
	e := newJobEnv(t, "USD", "GBP")
	spy := &meterSpy{}
	e.job.Meter, e.job.Now = spy, func() time.Time { return *e.clock }
	if _, ok := e.job.Last(); ok || !e.job.LastSuccess().IsZero() {
		t.Fatal("a status before any run")
	}
	st, err := e.job.Request(t.Context())
	if err != nil || st.Outcome != fx.RunSucceeded || st.Refreshed != 2 || st.Trigger != fx.TriggerManual || st.ErrorKind != "" {
		t.Fatalf("run = %+v, %v", st, err)
	}
	if last, ok := e.job.Last(); !ok || last != st || !e.job.LastSuccess().Equal(*e.clock) {
		t.Errorf("last = %+v", last)
	}
	// Too soon: refused without calling the provider.
	calls := len(e.prov.Calls())
	*e.clock = e.clock.Add(20 * time.Second)
	var soon *fx.TooSoonError
	if _, err = e.job.Request(t.Context()); !errors.As(err, &soon) || soon.RetryAfter != 40*time.Second || len(e.prov.Calls()) != calls {
		t.Fatalf("second request: %v (retry %v), calls %d -> %d", err, soon, calls, len(e.prov.Calls()))
	}
	// An outage is a failed run, not an error; the last success stays.
	*e.clock = e.clock.Add(time.Minute)
	e.prov.SetDown(&fx.ProviderError{Kind: fx.KindUnavailable, Provider: "p", Op: "latest", Code: "timeout"})
	st, err = e.job.Request(t.Context())
	if err != nil || st.Outcome != fx.RunFailed || st.ErrorKind != "unavailable" || st.ErrorCode != "timeout" || st.Refreshed != 0 {
		t.Fatalf("failed run = %+v, %v", st, err)
	}
	if !e.job.LastSuccess().Equal(fxtest.T0) {
		t.Errorf("last success moved to %v", e.job.LastSuccess())
	}
	if len(spy.runs) != 2 || spy.runs[0] != "manual/success" || spy.runs[1] != "manual/failure" {
		t.Errorf("metered %v", spy.runs)
	}
}

func TestRequestNeverOverlapsARunAndCancellationIsNotRecorded(t *testing.T) {
	e := newJobEnv(t, "USD")
	spy := &meterSpy{}
	e.job.Meter = spy
	release := e.prov.Hold()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := e.job.Request(ctx); done <- err }()
	for len(e.prov.Calls()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := e.job.Request(t.Context()); !errors.Is(err, fx.ErrRefreshInProgress) {
		t.Fatalf("overlapping request: %v", err)
	}
	cancel()
	release()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
	if _, ok := e.job.Last(); ok || len(spy.runs) != 0 {
		t.Errorf("a cancelled run was recorded: %v", spy.runs)
	}
}

func TestRequestTimeoutIsAFailedRun(t *testing.T) {
	e := newJobEnv(t, "USD")
	e.prov.Hold()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	st, err := e.job.Request(ctx)
	if err != nil || st.Outcome != fx.RunFailed || st.ErrorCode != "timeout" || st.ErrorKind != "unavailable" {
		t.Fatalf("run = %+v, %v", st, err)
	}
}

func TestRequestWithoutATargetIsIdle(t *testing.T) {
	e := newJobEnv(t, "USD")
	e.job.Target = money.Currency{}
	if _, err := e.job.Request(t.Context()); !errors.Is(err, fx.ErrRefreshIdle) {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleReasonFollowsD11(t *testing.T) {
	now := fxtest.T0
	svc, err := fx.NewService(fxtest.NewCache(), nil, fx.Options{ProviderName: "p", Source: "ECB", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	rate := func(date string, retrieved time.Time) fx.Rate {
		d, _ := calendar.ParseDate(date)
		return fx.Rate{Date: d, RetrievedAt: retrieved}
	}
	for name, c := range map[string]struct {
		r    fx.Rate
		want fx.Reason
	}{
		"fresh":                {rate("2026-11-13", now.Add(-time.Hour)), fx.ReasonNone},
		"retrieved 5d ago":     {rate("2026-11-15", now.Add(-5*24*time.Hour)), fx.ReasonNotRetrievedRecently},
		"retrieved 4d23h ago":  {rate("2026-11-15", now.Add(-5*24*time.Hour+time.Hour)), fx.ReasonNone},
		"dated over 5d before": {rate("2026-11-01", now.Add(-time.Hour)), fx.ReasonRateDateTooOld},
	} {
		if got := svc.StaleReason(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if svc.HasProvider() || svc.Freshness() != fx.DefaultFreshness {
		t.Error("provider or freshness")
	}
}

// A second manual success while the loop waits after a wake leaves a stale
// wake token: the next scheduled failure must still back off briefly.
func TestLeftoverWakeTokenDoesNotDefeatBackoff(t *testing.T) {
	e := newJobEnv(t, "USD")
	e.job.ManualGap = time.Nanosecond
	e.prov.SetDown(&fx.ProviderError{Kind: fx.KindUnavailable, Provider: "p", Op: "latest"})
	ticks := make(chan time.Time)
	waits := make(chan time.Duration, 8)
	e.job.After = func(d time.Duration) <-chan time.Time { waits <- d; return ticks }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.job.Run(ctx) }()
	if d := <-waits; d != fx.FirstBackoff {
		t.Fatalf("wait after a failure = %v", d)
	}
	*e.clock = e.clock.Add(time.Hour)
	e.prov.SetDown(nil)
	manual := func() {
		*e.clock = e.clock.Add(time.Hour)
		if st, err := e.job.Request(t.Context()); err != nil || st.Outcome != fx.RunSucceeded {
			t.Fatalf("manual refresh = %+v, %v", st, err)
		}
	}
	manual()
	if d := <-waits; d != 24*time.Hour { // the wait after the wake
		t.Fatalf("wait after a manual success = %v", d)
	}
	manual() // leaves a token: the loop is not listening for it
	*e.clock = e.clock.Add(48 * time.Hour)
	e.prov.SetDown(&fx.ProviderError{Kind: fx.KindUnavailable, Provider: "p", Op: "latest"})
	ticks <- time.Now() // the interval ends; a scheduled run fails
	if d := <-waits; d != fx.FirstBackoff {
		t.Errorf("wait after a scheduled failure = %v, want %v", d, fx.FirstBackoff)
	}
	// With the stale token the loop would wake at once and wait a full
	// interval instead.
	select {
	case d := <-waits:
		t.Errorf("a leftover wake token restarted the wait with %v", d)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v", err)
	}
}

// A manual refresh that succeeds ends the scheduled loop's failure backoff:
// the loop waits a normal interval instead of its shortened retry.
func TestSuccessfulManualRefreshResetsTheScheduledBackoff(t *testing.T) {
	e := newJobEnv(t, "USD")
	e.prov.SetDown(&fx.ProviderError{Kind: fx.KindUnavailable, Provider: "p", Op: "latest"})
	ticks := make(chan time.Time)
	waits := make(chan time.Duration, 8)
	e.job.After = func(d time.Duration) <-chan time.Time { waits <- d; return ticks }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.job.Run(ctx) }()
	if d := <-waits; d != fx.FirstBackoff {
		t.Fatalf("wait after a failure = %v, want %v", d, fx.FirstBackoff)
	}
	*e.clock = e.clock.Add(time.Hour) // past the provider's failure pause
	e.prov.SetDown(nil)
	if st, err := e.job.Request(t.Context()); err != nil || st.Outcome != fx.RunSucceeded {
		t.Fatalf("manual refresh = %+v, %v", st, err)
	}
	if d := <-waits; d != 24*time.Hour {
		t.Errorf("wait after a manual success = %v, want the interval", d)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v", err)
	}
}
