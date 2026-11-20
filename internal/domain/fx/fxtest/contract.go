package fxtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// T0 is the reference instant of the contract and suite tests: a Monday.
var T0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// Pair builds a pair from codes.
func Pair(base, quote string) fx.Pair {
	return fx.Pair{Base: money.MustParseCurrency(base), Quote: money.MustParseCurrency(quote)}
}

func rate(provider, pair, value, date string, at time.Time) fx.Rate {
	return fx.Rate{Provider: provider, Source: "ECB", Pair: Pair(pair[:3], pair[4:]), Value: money.MustParseDecimal(value), Date: mustDate(date), RetrievedAt: at, FirstRetrievedAt: at}
}

// CacheContract checks an [fx.Cache] implementation. newCache returns an
// empty cache.
func CacheContract(t *testing.T, newCache func(t *testing.T) fx.Cache) {
	ctx := context.Background()
	eurusd := Pair("EUR", "USD")

	t.Run("empty", func(t *testing.T) {
		c := newCache(t)
		if _, ok, err := c.Find(ctx, "p", "ECB", eurusd, mustDate("")); err != nil || ok {
			t.Fatalf("Find on empty cache = ok %v, err %v", ok, err)
		}
	})

	t.Run("invalid rates are refused and nothing is saved", func(t *testing.T) {
		c := newCache(t)
		good := rate("p", "EUR/USD", "1.0834", "2026-11-13", T0)
		for name, bad := range map[string]fx.Rate{
			"zero":          rate("p", "EUR/USD", "0.000", "2026-11-14", T0),
			"negative":      rate("p", "EUR/USD", "-1.2", "2026-11-14", T0),
			"same currency": rate("p", "EUR/EUR", "1", "2026-11-14", T0),
			"bad provider":  rate("P-1", "EUR/USD", "1.2", "2026-11-14", T0),
			"no time":       rate("p", "EUR/USD", "1.2", "2026-11-14", time.Time{}),
		} {
			err := c.Save(ctx, []fx.Rate{good, bad})
			if !errors.Is(err, fx.ErrInvalidRate) {
				t.Errorf("%s: Save = %v, want ErrInvalidRate", name, err)
			}
		}
		if _, ok, _ := c.Find(ctx, "p", "ECB", eurusd, mustDate("")); ok {
			t.Error("a batch with an invalid rate saved its valid rate")
		}
	})

	t.Run("exact round trip and provenance", func(t *testing.T) {
		c := newCache(t)
		in := rate("p", "EUR/USD", "1.083400", "2026-11-13", T0)
		if err := c.Save(ctx, []fx.Rate{in}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if err != nil || !ok {
			t.Fatalf("Find = ok %v, err %v", ok, err)
		}
		if !got.Value.Identical(in.Value) || got.Value.String() != "1.083400" {
			t.Errorf("value = %s, want 1.083400 exactly", got.Value)
		}
		if got.Provider != "p" || got.Source != "ECB" || got.Pair != eurusd || got.Date != in.Date {
			t.Errorf("provenance = %+v", got)
		}
		if !got.RetrievedAt.Equal(T0) || !got.FirstRetrievedAt.Equal(T0) || got.RetrievedAt.Location() != time.UTC {
			t.Errorf("retrieved = %v / %v, want %v UTC", got.RetrievedAt, got.FirstRetrievedAt, T0)
		}
	})

	t.Run("latest on or before a date", func(t *testing.T) {
		c := newCache(t)
		if err := c.Save(ctx, []fx.Rate{
			rate("p", "EUR/USD", "1.08", "2026-11-12", T0),
			rate("p", "EUR/USD", "1.09", "2026-11-13", T0),
			rate("p", "EUR/USD", "1.07", "2026-11-10", T0),
			rate("p", "USD/EUR", "0.9", "2026-11-14", T0),
			rate("q", "EUR/USD", "2.0", "2026-11-14", T0),
		}); err != nil {
			t.Fatal(err)
		}
		for bound, want := range map[string]string{"": "2026-11-13", "2026-11-15": "2026-11-13", "2026-11-12": "2026-11-12", "2026-11-11": "2026-11-10"} {
			got, ok, err := c.Find(ctx, "p", "ECB", eurusd, mustDate(bound))
			if err != nil || !ok || got.Date.String() != want {
				t.Errorf("Find(on or before %q) = %s ok %v err %v, want %s", bound, got.Date, ok, err, want)
			}
		}
		if _, ok, _ := c.Find(ctx, "p", "ECB", eurusd, mustDate("2026-11-09")); ok {
			t.Error("Find before every cached date found a rate")
		}
		if _, ok, _ := c.Find(ctx, "r", "ECB", eurusd, mustDate("")); ok {
			t.Error("Find for another provider found a rate")
		}
		if _, ok, _ := c.Find(ctx, "p", "blended", eurusd, mustDate("")); ok {
			t.Error("Find for another source found a rate")
		}
	})

	t.Run("sources are kept apart", func(t *testing.T) {
		c := newCache(t)
		blended := rate("p", "EUR/USD", "1.1072", "2026-11-13", T0)
		blended.Source = "blended"
		if err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.1052", "2026-11-13", T0), blended}); err != nil {
			t.Fatal(err)
		}
		for source, want := range map[string]string{"ECB": "1.1052", "blended": "1.1072"} {
			got, ok, err := c.Find(ctx, "p", source, eurusd, mustDate(""))
			if err != nil || !ok || got.Value.String() != want || got.Source != source {
				t.Errorf("Find(%s) = %+v ok %v err %v, want %s", source, got, ok, err, want)
			}
		}
	})

	t.Run("re-retrieval updates, an older retrieval does not", func(t *testing.T) {
		c := newCache(t)
		if err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.08", "2026-11-13", T0)}); err != nil {
			t.Fatal(err)
		}
		later := T0.Add(24 * time.Hour)
		if err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.0801", "2026-11-13", later)}); err != nil {
			t.Fatal(err)
		}
		got, _, _ := c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.Value.String() != "1.0801" || !got.RetrievedAt.Equal(later) || !got.FirstRetrievedAt.Equal(T0) {
			t.Errorf("after re-retrieval: %s retrieved %v first %v", got.Value, got.RetrievedAt, got.FirstRetrievedAt)
		}
		if err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "9", "2026-11-13", T0.Add(time.Hour))}); err != nil {
			t.Fatal(err)
		}
		got, _, _ = c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.Value.String() != "1.0801" || !got.RetrievedAt.Equal(later) {
			t.Errorf("an older retrieval overwrote the rate: %s retrieved %v", got.Value, got.RetrievedAt)
		}
	})
}
