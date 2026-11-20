package fxtest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Clock is a settable clock for tests.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock at t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current test time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type rig struct {
	t     *testing.T
	ctx   context.Context
	cache fx.Cache
	p     *Provider
	clock *Clock
	svc   *fx.Service
}

func newRig(t *testing.T, cache fx.Cache, withProvider bool) *rig {
	t.Helper()
	r := &rig{t: t, ctx: context.Background(), cache: cache, p: NewProvider("frankfurter", "ECB"), clock: NewClock(T0)}
	var p fx.Provider
	if withProvider {
		p = r.p
	}
	svc, err := fx.NewService(cache, p, fx.Options{Now: r.clock.Now, ProviderName: "frankfurter", Source: "ECB"})
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

// seed caches a frankfurter rate retrieved at "at".
func (r *rig) seed(pair, value, date string, at time.Time) {
	r.t.Helper()
	if err := r.cache.Save(r.ctx, []fx.Rate{rate("frankfurter", pair, value, date, at)}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) convert(amount, from, to string) fx.Conversion {
	r.t.Helper()
	c, err := r.svc.Convert(r.ctx, money.MustParse(amount, from), money.MustParseCurrency(to))
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func (r *rig) convertOn(amount, from, to, date string) fx.Conversion {
	r.t.Helper()
	c, err := r.svc.ConvertOn(r.ctx, money.MustParse(amount, from), money.MustParseCurrency(to), mustDate(date))
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func (r *rig) calls(want int) {
	r.t.Helper()
	if got := len(r.p.Calls()); got != want {
		r.t.Errorf("provider calls = %d, want %d: %+v", got, want, r.p.Calls())
	}
}

func expect(t *testing.T, c fx.Conversion, status fx.Status, reason fx.Reason) {
	t.Helper()
	if c.Status != status || c.Reason != reason {
		t.Fatalf("conversion = %s/%q, want %s/%q (%+v)", c.Status, c.Reason, status, reason, c)
	}
	if c.Acceptable() != (status == fx.StatusOK) {
		t.Errorf("Acceptable() = %v for %s", c.Acceptable(), status)
	}
	if status != fx.StatusOK && (c.Value.IsValid() || c.Exact.IsValid()) {
		t.Errorf("a %s conversion carries a value: %s / %s", status, c.Value, c.Exact)
	}
	if status == fx.StatusMissing && c.Rate != nil {
		t.Errorf("a missing conversion carries a rate: %+v", c.Rate)
	}
}

func expectValue(t *testing.T, c fx.Conversion, value, exact string) {
	t.Helper()
	if c.Value.String() != value || c.Exact.String() != exact {
		t.Errorf("value = %s (exact %s), want %s (exact %s)", c.Value, c.Exact, value, exact)
	}
}

var down = &fx.ProviderError{Kind: fx.KindUnavailable, Provider: "frankfurter", Op: "latest", Code: "http_503"}

// ServiceSuite runs the conversion-service tests (FX1 acceptance criteria)
// against a cache. newCache returns an empty cache.
func ServiceSuite(t *testing.T, newCache func(t *testing.T) fx.Cache) {
	t.Run("missing rate", func(t *testing.T) {
		t.Run("provider publishes no rate for the pair", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			c := r.convert("10.00", "EUR", "USD")
			expect(t, c, fx.StatusMissing, fx.ReasonNotPublished)
			r.calls(1)
		})
		t.Run("nothing cached and the provider is down", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.p.SetDown(down)
			c := r.convert("10.00", "EUR", "USD")
			expect(t, c, fx.StatusMissing, fx.ReasonNotCached)
			if !errors.Is(c.RefreshError, fx.ErrUnavailable) {
				t.Errorf("RefreshError = %v, want ErrUnavailable", c.RefreshError)
			}
		})
		t.Run("no provider configured", func(t *testing.T) {
			r := newRig(t, newCache(t), false)
			expect(t, r.convert("10.00", "EUR", "USD"), fx.StatusMissing, fx.ReasonNoProvider)
		})
		t.Run("never inverts or triangulates", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.p.SetDown(down)
			r.seed("USD/EUR", "0.92", "2026-11-13", T0)
			r.seed("EUR/GBP", "0.87", "2026-11-13", T0)
			r.seed("GBP/USD", "1.27", "2026-11-13", T0)
			expect(t, r.convert("10.00", "EUR", "USD"), fx.StatusMissing, fx.ReasonNotCached)
		})
		t.Run("a malformed provider answer is not cached", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.p.Publish("EUR", "USD", "-1.08", "2026-11-13", "ECB")
			c := r.convert("10.00", "EUR", "USD")
			expect(t, c, fx.StatusMissing, fx.ReasonNotCached)
			if !errors.Is(c.RefreshError, fx.ErrPermanent) {
				t.Errorf("RefreshError = %v, want ErrPermanent", c.RefreshError)
			}
			if _, ok, _ := r.cache.Find(r.ctx, "frankfurter", "ECB", Pair("EUR", "USD"), calendar.Date{}); ok {
				t.Error("a negative rate was cached")
			}
		})
	})

	t.Run("stale rate", func(t *testing.T) {
		t.Run("not retrieved for StaleAfter while the provider is down", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.seed("EUR/USD", "1.0834", "2026-11-13", T0)
			r.clock.Advance(5 * 24 * time.Hour)
			r.p.SetDown(down)
			c := r.convert("10.00", "EUR", "USD")
			expect(t, c, fx.StatusStale, fx.ReasonNotRetrievedRecently)
			if c.Rate == nil || c.Rate.Value.String() != "1.0834" || c.Age != 5*24*time.Hour || !c.FromCache || c.RefreshError == nil {
				t.Errorf("stale conversion lacks provenance: %+v", c)
			}
		})
		t.Run("provider still answers with an old rate date", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.p.Publish("EUR", "USD", "1.0834", "2026-11-10", "ECB") // 6 days before T0
			c := r.convert("10.00", "EUR", "USD")
			expect(t, c, fx.StatusStale, fx.ReasonRateDateTooOld)
			if c.FromCache || c.RefreshError != nil || c.Rate == nil {
				t.Errorf("stale-by-date conversion: %+v", c)
			}
		})
		t.Run("weekend gap is not stale", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.p.Publish("EUR", "USD", "1.0834", "2026-11-13", "ECB") // Friday; T0 is Monday
			expect(t, r.convert("10.00", "EUR", "USD"), fx.StatusOK, fx.ReasonNone)
		})
	})

	t.Run("outage keeps cache", func(t *testing.T) {
		r := newRig(t, newCache(t), true)
		r.seed("EUR/USD", "1.0834", "2026-11-13", T0.Add(-48*time.Hour))
		r.p.SetDown(down)
		c := r.convert("12.35", "EUR", "USD")
		expect(t, c, fx.StatusOK, fx.ReasonNone)
		expectValue(t, c, "13.38 USD", "13.379990 USD")
		if !c.FromCache || c.Age != 48*time.Hour || !errors.Is(c.RefreshError, fx.ErrUnavailable) {
			t.Errorf("served from cache without saying so: %+v", c)
		}
		r.calls(1)
		got, ok, err := r.cache.Find(r.ctx, "frankfurter", "ECB", Pair("EUR", "USD"), calendar.Date{})
		if err != nil || !ok || got.Value.String() != "1.0834" {
			t.Errorf("cache after the outage = %+v ok %v err %v", got, ok, err)
		}
	})

	t.Run("quota exhaustion serves the cache and pauses calls", func(t *testing.T) {
		r := newRig(t, newCache(t), true)
		r.seed("EUR/USD", "1.0834", "2026-11-13", T0.Add(-30*time.Hour))
		r.p.FailNext(&fx.ProviderError{Kind: fx.KindRateLimited, Provider: "frankfurter", Op: "latest", RetryAfter: time.Hour})
		c := r.convert("1", "EUR", "USD")
		expect(t, c, fx.StatusOK, fx.ReasonNone)
		if !errors.Is(c.RefreshError, fx.ErrRateLimited) {
			t.Errorf("RefreshError = %v, want ErrRateLimited", c.RefreshError)
		}
		r.clock.Advance(30 * time.Minute)
		c = r.convert("1", "EUR", "USD")
		expect(t, c, fx.StatusOK, fx.ReasonNone)
		r.calls(1)
		if !errors.Is(c.RefreshError, fx.ErrRateLimited) {
			t.Errorf("during the pause RefreshError = %v, want ErrRateLimited", c.RefreshError)
		}
		r.clock.Advance(31 * time.Minute)
		r.p.Publish("EUR", "USD", "1.0900", "2026-11-16", "ECB")
		c = r.convert("1", "EUR", "USD")
		expect(t, c, fx.StatusOK, fx.ReasonNone)
		r.calls(2)
		if c.FromCache || c.Rate.Value.String() != "1.0900" {
			t.Errorf("after the pause: %+v", c)
		}
	})

	t.Run("no provider call when the cached rate is acceptable", func(t *testing.T) {
		r := newRig(t, newCache(t), true)
		r.seed("EUR/USD", "1.0834", "2026-11-13", T0.Add(-23*time.Hour))
		c := r.convert("100", "EUR", "USD")
		expect(t, c, fx.StatusOK, fx.ReasonNone)
		expectValue(t, c, "108.34 USD", "108.3400 USD")
		if !c.FromCache || c.RefreshError != nil || c.Rate.Provider != "frankfurter" || c.Rate.Source != "ECB" {
			t.Errorf("provenance: %+v", c)
		}
		r.calls(0)
		// Once RefreshAfter has passed the provider is asked again, and the
		// answer is cached and used.
		r.clock.Advance(time.Hour)
		r.p.Publish("EUR", "USD", "1.0834", "2026-11-13", "ECB")
		c = r.convert("100", "EUR", "USD")
		r.calls(1)
		if c.FromCache || c.Age != 0 {
			t.Errorf("after refresh: %+v", c)
		}
		got, _, _ := r.cache.Find(r.ctx, "frankfurter", "ECB", Pair("EUR", "USD"), calendar.Date{})
		if !got.RetrievedAt.Equal(r.clock.Now()) || !got.FirstRetrievedAt.Equal(T0.Add(-23*time.Hour)) {
			t.Errorf("re-retrieval not recorded: %+v", got)
		}
		r.convert("100", "EUR", "USD")
		r.calls(1)
	})

	t.Run("a rate from another source is cached but never used", func(t *testing.T) {
		r := newRig(t, newCache(t), true)
		r.p.Publish("EUR", "USD", "1.1072", "2026-11-16", "blended")
		c := r.convert("1", "EUR", "USD")
		expect(t, c, fx.StatusMissing, fx.ReasonNotPublished)
		got, ok, err := r.cache.Find(r.ctx, "frankfurter", "blended", Pair("EUR", "USD"), calendar.Date{})
		if err != nil || !ok || got.Value.String() != "1.1072" {
			t.Errorf("blended rate not cached with its source: %+v ok %v err %v", got, ok, err)
		}
		r.p.Publish("EUR", "USD", "1.1052", "2026-11-13", "ECB")
		r.clock.Advance(25 * time.Hour)
		c = r.convert("1", "EUR", "USD")
		expect(t, c, fx.StatusOK, fx.ReasonNone)
		if c.Rate.Source != "ECB" || c.Rate.Value.String() != "1.1052" {
			t.Errorf("used %+v, want the ECB rate", c.Rate)
		}
	})

	t.Run("same currency needs no rate", func(t *testing.T) {
		r := newRig(t, newCache(t), true)
		c := r.convert("7.5", "EUR", "EUR")
		expect(t, c, fx.StatusOK, fx.ReasonNone)
		if !c.Identity || c.Rate != nil || c.Value.String() != "7.5 EUR" {
			t.Errorf("identity: %+v", c)
		}
		r.calls(0)
	})

	t.Run("rounding is half-even to the target's minor units", func(t *testing.T) {
		r := newRig(t, newCache(t), true)
		r.seed("EUR/JPY", "162.5", "2026-11-13", T0)
		r.seed("EUR/USD", "1.25", "2026-11-13", T0)
		expectValue(t, r.convert("1.00", "EUR", "JPY"), "162 JPY", "162.500 JPY")
		expectValue(t, r.convert("0.03", "EUR", "USD"), "0.04 USD", "0.0375 USD")
		expectValue(t, r.convert("0.01", "EUR", "USD"), "0.01 USD", "0.0125 USD")
		r.calls(0)
	})

	t.Run("dated conversion", func(t *testing.T) {
		t.Run("weekend date uses the last published rate, then the cache", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.p.Publish("EUR", "USD", "1.0800", "2026-11-13", "ECB")
			r.p.Publish("EUR", "USD", "1.0900", "2026-11-16", "ECB")
			c := r.convertOn("10", "EUR", "USD", "2026-11-15")
			expect(t, c, fx.StatusOK, fx.ReasonNone)
			if c.Rate.Date.String() != "2026-11-13" || c.Value.String() != "10.80 USD" {
				t.Errorf("Sunday conversion: %+v", c)
			}
			r.calls(1)
			// Retrieved after Sunday ended, so it is final for Saturday and
			// Sunday: no further call.
			r.convertOn("10", "EUR", "USD", "2026-11-14")
			r.convertOn("10", "EUR", "USD", "2026-11-15")
			r.calls(1)
		})
		t.Run("unconfirmed earlier rate while the provider is down", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.seed("EUR/USD", "1.0800", "2026-11-13", time.Date(2026, 11, 13, 17, 0, 0, 0, time.UTC))
			r.p.SetDown(down)
			c := r.convertOn("10", "EUR", "USD", "2026-11-14")
			expect(t, c, fx.StatusStale, fx.ReasonUnconfirmed)
			if c.Rate == nil || c.RefreshError == nil {
				t.Errorf("unconfirmed: %+v", c)
			}
		})
		t.Run("historical rate is never stale by retrieval age", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.seed("EUR/USD", "1.0700", "2026-10-01", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
			r.p.SetDown(down)
			expect(t, r.convertOn("10", "EUR", "USD", "2026-10-01"), fx.StatusOK, fx.ReasonNone)
			r.calls(0)
		})
		t.Run("gap beyond StaleAfter is stale", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			r.p.Publish("EUR", "USD", "1.0700", "2026-10-01", "ECB")
			expect(t, r.convertOn("10", "EUR", "USD", "2026-10-20"), fx.StatusStale, fx.ReasonRateDateTooOld)
		})
		t.Run("future date is refused", func(t *testing.T) {
			r := newRig(t, newCache(t), true)
			_, err := r.svc.ConvertOn(r.ctx, money.MustParse("1", "EUR"), money.MustParseCurrency("USD"), mustDate("2026-11-17"))
			if !errors.Is(err, fx.ErrFutureDate) {
				t.Errorf("err = %v, want ErrFutureDate", err)
			}
		})
	})

	t.Run("refresh caches every published quote", func(t *testing.T) {
		r := newRig(t, newCache(t), true)
		r.p.Publish("EUR", "USD", "1.0834", "2026-11-16", "ECB")
		r.p.Publish("EUR", "GBP", "0.8712", "2026-11-16", "ECB")
		got, err := r.svc.Refresh(r.ctx, money.MustParseCurrency("EUR"), []money.Currency{money.MustParseCurrency("USD"), money.MustParseCurrency("GBP"), money.MustParseCurrency("SEK")})
		if err != nil || len(got) != 2 {
			t.Fatalf("Refresh = %d rates, %v", len(got), err)
		}
		expect(t, r.convert("1", "EUR", "GBP"), fx.StatusOK, fx.ReasonNone)
		r.calls(1)
	})
}
