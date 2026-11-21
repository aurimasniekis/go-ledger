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

// rate builds a rate as a latest retrieval at "at" would cache it: confirmed
// through the retrieval's day, or its own date when that is later.
func rate(provider, pair, value, date string, at time.Time) fx.Rate {
	d := mustDate(date)
	if day := mustDate(at.UTC().Format("2006-01-02")); day.After(d) {
		d = day
	}
	return fx.Rate{Provider: provider, Source: "ECB", Pair: Pair(pair[:3], pair[4:]), Value: money.MustParseDecimal(value), Date: mustDate(date), RetrievedAt: at, FirstRetrievedAt: at, ConfirmedThrough: d}
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
			_, err := c.Save(ctx, []fx.Rate{good, bad})
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
		if _, err := c.Save(ctx, []fx.Rate{in}); err != nil {
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
		if _, err := c.Save(ctx, []fx.Rate{
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
		if _, err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.1052", "2026-11-13", T0), blended}); err != nil {
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
		if _, err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.08", "2026-11-13", T0)}); err != nil {
			t.Fatal(err)
		}
		later := T0.Add(24 * time.Hour)
		if _, err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.0800", "2026-11-13", later)}); err != nil {
			t.Fatal(err)
		}
		got, _, _ := c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.Value.String() != "1.08" || !got.RetrievedAt.Equal(later) || !got.FirstRetrievedAt.Equal(T0) {
			t.Errorf("after re-retrieval: %s retrieved %v first %v", got.Value, got.RetrievedAt, got.FirstRetrievedAt)
		}
		if _, err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "9", "2026-11-13", T0.Add(time.Hour))}); err != nil {
			t.Fatal(err)
		}
		got, _, _ = c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.Value.String() != "1.08" || !got.RetrievedAt.Equal(later) {
			t.Errorf("an older retrieval overwrote the rate: %s retrieved %v", got.Value, got.RetrievedAt)
		}
	})

	t.Run("confirmed_through is kept as the maximum", func(t *testing.T) {
		c := newCache(t)
		in := rate("p", "EUR/USD", "1.08", "2026-11-05", T0)
		in.ConfirmedThrough = mustDate("2026-11-07")
		if _, err := c.Save(ctx, []fx.Rate{in}); err != nil {
			t.Fatal(err)
		}
		lower := in
		lower.RetrievedAt, lower.ConfirmedThrough = T0.Add(time.Hour), mustDate("2026-11-06")
		if _, err := c.Save(ctx, []fx.Rate{lower}); err != nil {
			t.Fatal(err)
		}
		got, _, _ := c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.ConfirmedThrough != mustDate("2026-11-07") || !got.RetrievedAt.Equal(T0.Add(time.Hour)) {
			t.Errorf("confirmed through %s retrieved %v, want 2026-11-07 and the later retrieval", got.ConfirmedThrough, got.RetrievedAt)
		}
		higher := in
		higher.RetrievedAt, higher.ConfirmedThrough = T0.Add(2*time.Hour), mustDate("2026-11-12")
		if _, err := c.Save(ctx, []fx.Rate{higher}); err != nil {
			t.Fatal(err)
		}
		got, _, _ = c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.ConfirmedThrough != mustDate("2026-11-12") {
			t.Errorf("confirmed through %s, want 2026-11-12", got.ConfirmedThrough)
		}
		if !got.FinalFor(mustDate("2026-11-12")) || got.FinalFor(mustDate("2026-11-13")) {
			t.Errorf("FinalFor wrong for a rate dated %s confirmed through %s", got.Date, got.ConfirmedThrough)
		}
		// A rate stored without ConfirmedThrough is confirmed through its date.
		bare := rate("p", "EUR/GBP", "0.87", "2026-11-05", mustTime("2026-11-05T10:00:00Z"))
		bare.ConfirmedThrough = mustDate("")
		if _, err := c.Save(ctx, []fx.Rate{bare}); err != nil {
			t.Fatal(err)
		}
		got, _, _ = c.Find(ctx, "p", "ECB", Pair("EUR", "GBP"), mustDate(""))
		if got.ConfirmedThrough != mustDate("2026-11-05") {
			t.Errorf("default confirmed through = %s, want the rate's date", got.ConfirmedThrough)
		}
	})

	t.Run("a revised value keeps the superseded one and restarts provenance", func(t *testing.T) {
		c := newCache(t)
		if _, err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.08", "2026-11-13", T0)}); err != nil {
			t.Fatal(err)
		}
		// The same number written differently is no revision.
		same := T0.Add(time.Hour)
		saved, err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.0800", "2026-11-13", same)})
		if err != nil || len(saved) != 1 || saved[0].Revised != nil {
			t.Fatalf("Save of the same number = %+v, %v; want no revision", saved, err)
		}
		got, _, _ := c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.Previous != nil || !got.FirstRetrievedAt.Equal(T0) || !got.RetrievedAt.Equal(same) {
			t.Errorf("after an unchanged re-retrieval: %+v", got)
		}
		revisedAt := T0.Add(4 * 24 * time.Hour)
		saved, err = c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.18", "2026-11-13", revisedAt)})
		if err != nil || len(saved) != 1 || saved[0].Revised == nil {
			t.Fatalf("Save of a revised value = %+v, %v; want a revision", saved, err)
		}
		rev := saved[0].Revised
		if rev.Old.String() != "1.08" || rev.New.String() != "1.18" || rev.Pair != eurusd || rev.Date != mustDate("2026-11-13") || rev.Provider != "p" || !rev.At.Equal(revisedAt) {
			t.Errorf("revision = %+v", rev)
		}
		got, _, _ = c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.Value.String() != "1.18" || !got.FirstRetrievedAt.Equal(revisedAt) || !got.RetrievedAt.Equal(revisedAt) {
			t.Errorf("after the revision: %s first %v retrieved %v; first retrieval must restart at the revision", got.Value, got.FirstRetrievedAt, got.RetrievedAt)
		}
		if got.Previous == nil || got.Previous.Value.String() != "1.08" || !got.Previous.RevisedAt.Equal(revisedAt) {
			t.Errorf("previous = %+v, want 1.08 revised at %v", got.Previous, revisedAt)
		}
		if !saved[0].Rate.Value.Identical(got.Value) || saved[0].Rate.Previous == nil {
			t.Errorf("Save returned %+v, want the stored row", saved[0].Rate)
		}
		// The same revised value again keeps the history.
		if _, err := c.Save(ctx, []fx.Rate{rate("p", "EUR/USD", "1.18", "2026-11-13", revisedAt.Add(time.Hour))}); err != nil {
			t.Fatal(err)
		}
		got, _, _ = c.Find(ctx, "p", "ECB", eurusd, mustDate(""))
		if got.Previous == nil || got.Previous.Value.String() != "1.08" || !got.FirstRetrievedAt.Equal(revisedAt) {
			t.Errorf("history lost on a repeat: %+v", got)
		}
	})
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
