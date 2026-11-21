package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// frankfurterDouble is a Frankfurter test server: it answers /v2/rates from
// its table (recorded response shape) or with the scripted failure, and
// counts requests. Nothing here reaches the live service.
type frankfurterDouble struct {
	*httptest.Server
	mu    sync.Mutex
	fail  int
	date  string
	rates map[string]string // base -> rate to EUR, as published text
	calls int
}

func newDouble(t *testing.T) *frankfurterDouble {
	t.Helper()
	d := &frankfurterDouble{date: "2026-11-20", rates: map[string]string{"USD": "0.90992", "GBP": "1.16210", "SEK": "0.09100"}}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls++
		if d.fail != 0 {
			if d.fail == 429 {
				w.Header().Set("Retry-After", "3600")
			}
			w.WriteHeader(d.fail)
			return
		}
		q := r.URL.Query()
		v, ok := d.rates[q.Get("base")]
		if r.URL.Path != "/v2/rates" || q.Get("quotes") != "EUR" || !ok {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":422,"message":"invalid currency"}`))
			return
		}
		_, _ = w.Write([]byte(`[{"date":"` + d.date + `","base":"` + q.Get("base") + `","quote":"EUR","rate":` + v + `}]`))
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *frankfurterDouble) set(fail int) { d.mu.Lock(); d.fail = fail; d.mu.Unlock() }
func (d *frankfurterDouble) count() int   { d.mu.Lock(); defer d.mu.Unlock(); return d.calls }

func ratesDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func ratesCfg(url string) config.Rates {
	return config.Rates{Enabled: true, BaseURL: url, Source: "ECB", RefreshInterval: 24 * time.Hour, StaleAfter: 120 * time.Hour, Timeout: 2 * time.Second}
}

func eur() moneypkg.Currency { return moneypkg.MustParseCurrency("EUR") }

func usd(t *testing.T, amount string) moneypkg.Money {
	t.Helper()
	m, err := moneypkg.New(moneypkg.MustParseDecimal(amount), moneypkg.MustParseCurrency("USD"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestRatesQuotaAndOutageKeepCachedRates is the FX2 acceptance test: the
// real adapter and the real SQLite cache against a test server, with a
// fake clock. A quota refusal and an outage keep serving the cached rate
// (visibly: RefreshError, Age), the rate turns stale after five days, and
// no rate is ever invented.
func TestRatesQuotaAndOutageKeepCachedRates(t *testing.T) {
	ctx := context.Background()
	d := newDouble(t)
	db := ratesDB(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	svc, job, err := buildRates(ratesCfg(d.URL), eur(), db, func() time.Time { return now }, nil)
	if err != nil || job == nil {
		t.Fatalf("buildRates: %v, job %v", err, job)
	}

	c, err := svc.Convert(ctx, usd(t, "100.00"), eur())
	if err != nil || c.Status != fx.StatusOK || c.Rate == nil || c.FromCache {
		t.Fatalf("first conversion: %+v, %v", c, err)
	}
	if c.Rate.Provider != "frankfurter" || c.Rate.Source != "ECB" || c.Rate.Value.String() != "0.90992" || c.Rate.Date.String() != "2026-11-20" || !c.Rate.RetrievedAt.Equal(now) {
		t.Errorf("provenance = %+v", c.Rate)
	}
	if c.Value.Amount().String() != "90.99" || c.Exact.Amount().String() != "90.9920000" {
		t.Errorf("converted %s (exact %s)", c.Value.Amount(), c.Exact.Amount())
	}
	if n := d.count(); n != 1 {
		t.Fatalf("calls = %d", n)
	}

	// Within a day the cache answers: Frankfurter is not asked again.
	now = now.Add(23 * time.Hour)
	if c, _ = svc.Convert(ctx, usd(t, "1"), eur()); !c.FromCache || c.Status != fx.StatusOK || d.count() != 1 {
		t.Fatalf("within the refresh interval: %+v, calls %d", c, d.count())
	}

	// Quota exhausted: the cached rate is still used, the refusal is visible, and
	// the pause means no second request.
	d.set(429)
	now = now.Add(2 * time.Hour)
	for range 3 {
		c, err = svc.Convert(ctx, usd(t, "100"), eur())
		if err != nil || c.Status != fx.StatusOK || !c.FromCache || !errors.Is(c.RefreshError, fx.ErrRateLimited) || c.Age != 25*time.Hour {
			t.Fatalf("rate limited: %+v, %v", c, err)
		}
	}
	if n := d.count(); n != 2 {
		t.Errorf("calls during the quota pause = %d, want 2", n)
	}

	// Outage after the pause: same, as unavailable.
	d.set(503)
	now = now.Add(3 * time.Hour)
	if c, _ = svc.Convert(ctx, usd(t, "100"), eur()); c.Status != fx.StatusOK || !errors.Is(c.RefreshError, fx.ErrUnavailable) || c.Rate.Value.String() != "0.90992" {
		t.Fatalf("outage: %+v", c)
	}

	// Five days after the last retrieval the rate is stale: no value is computed.
	now = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC).Add(5 * 24 * time.Hour)
	if c, _ = svc.Convert(ctx, usd(t, "100"), eur()); c.Status != fx.StatusStale || c.Reason != fx.ReasonNotRetrievedRecently || !c.Value.IsZero() || c.Rate == nil {
		t.Fatalf("stale: %+v", c)
	}

	// A pair the provider rejects is missing, never defaulted.
	d.set(0)
	now = now.Add(10 * time.Minute) // past the pause the stale conversion's failed refresh started
	gbp := moneypkg.MustParseCurrency("CHF")
	m, _ := moneypkg.New(moneypkg.MustParseDecimal("5"), gbp)
	if c, _ = svc.Convert(ctx, m, eur()); c.Status != fx.StatusMissing || !c.Value.IsZero() || c.Rate != nil {
		t.Fatalf("unsupported currency: %+v", c)
	}
	// Recovery: once Frankfurter publishes a current rate again, conversions work again.
	d.mu.Lock()
	d.date = "2026-11-25"
	d.mu.Unlock()
	if c, _ = svc.Convert(ctx, usd(t, "100"), eur()); c.Status != fx.StatusOK || c.FromCache {
		t.Fatalf("recovery: %+v", c)
	}
}

func TestRatesDisabledServesOnlyTheCache(t *testing.T) {
	ctx := context.Background()
	d := newDouble(t)
	db := ratesDB(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	off := ratesCfg(d.URL)
	off.Enabled = false
	svc, job, err := buildRates(off, eur(), db, clock, nil)
	if err != nil || job != nil {
		t.Fatalf("disabled: %v, job %v", err, job)
	}
	if c, _ := svc.Convert(ctx, usd(t, "10"), eur()); c.Status != fx.StatusMissing || c.Reason != fx.ReasonNoProvider {
		t.Fatalf("empty cache, disabled: %+v", c)
	}

	// Rates cached while enabled stay usable, with their age, after disabling.
	on, _, _ := buildRates(ratesCfg(d.URL), eur(), db, clock, nil)
	if c, _ := on.Convert(ctx, usd(t, "10"), eur()); c.Status != fx.StatusOK {
		t.Fatalf("enabled: %+v", c)
	}
	calls := d.count()
	now = now.Add(48 * time.Hour)
	c, _ := svc.Convert(ctx, usd(t, "10"), eur())
	if c.Status != fx.StatusOK || !c.FromCache || c.Age != 48*time.Hour || d.count() != calls {
		t.Fatalf("disabled with a cached rate: %+v (calls %d -> %d)", c, calls, d.count())
	}
}

func TestRatesJobNeedsAProviderAndAReportingCurrency(t *testing.T) {
	db := ratesDB(t)
	if _, job, err := buildRates(ratesCfg("http://127.0.0.1:1"), moneypkg.Currency{}, db, time.Now, nil); err != nil || job != nil {
		t.Errorf("no reporting currency: job %v, %v", job, err)
	}
	bad := ratesCfg("http://127.0.0.1:1")
	bad.Source = "EC B"
	if _, _, err := buildRates(bad, eur(), db, time.Now, nil); err == nil {
		t.Error("invalid source accepted")
	}
}

func TestRatesJobRefreshesTheCurrenciesInUse(t *testing.T) {
	ctx := context.Background()
	d := newDouble(t)
	db := ratesDB(t)
	const ts = "2026-11-16T09:00:00.000000000Z"
	err := db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for i, cur := range []string{"USD", "GBP", "EUR", "USD"} {
			id := "01JD0000000000000000000" + strings.Repeat("0", 2) + string(rune('1'+i))
			if _, err := tx.ExecContext(ctx, "INSERT INTO commitments (id, name, kind, amount, currency, schedule, start_date, created_at, updated_at) VALUES (?, 'x', 'service', '1.00', ?, '{\"version\":1}', '2026-01-01', ?, ?)", id, cur, ts, ts); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := currenciesInUse(ctx, db)
	if err != nil || len(got) != 3 || got[0].Code() != "EUR" || got[1].Code() != "GBP" || got[2].Code() != "USD" {
		t.Fatalf("currencies in use = %v, %v", got, err)
	}
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	svc, job, err := buildRates(ratesCfg(d.URL), eur(), db, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := job.Cycle(ctx, 0)
	if out.Err != nil || out.Refreshed != 2 || out.Wait != 24*time.Hour || d.count() != 2 {
		t.Fatalf("cycle %+v, calls %d", out, d.count())
	}
	// The cycle's rates now serve conversions without another request.
	if c, _ := svc.Convert(ctx, usd(t, "10"), eur()); c.Status != fx.StatusOK || !c.FromCache || d.count() != 2 {
		t.Fatalf("after the job: %+v", c)
	}
}
