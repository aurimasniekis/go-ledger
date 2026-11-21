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
	if out.Refreshed != 1 || !errors.Is(out.Err, fx.ErrPermanent) || len(e.prov.Calls()) != 2 {
		t.Fatalf("outcome %+v, %d calls", out, len(e.prov.Calls()))
	}
	if _, ok, _ := e.cache.Find(t.Context(), "p", "ECB", fxtest.Pair("USD", "EUR"), calendar.Date{}); !ok {
		t.Error("the currency after the permanent failure was not refreshed")
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
