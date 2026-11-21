package fxrates_test

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx/fxtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/store/fxrates"
)

func TestListOrdersFiltersAndResumesAfterAKey(t *testing.T) {
	ctx := context.Background()
	s := fxrates.New(openDB(t))
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	put := func(provider, source, pair, value, date string) {
		d, err := calendar.ParseDate(date)
		if err != nil {
			t.Fatal(err)
		}
		p := fxtest.Pair(pair[:3], pair[4:])
		r := fx.Rate{Provider: provider, Source: source, Pair: p, Value: money.MustParseDecimal(value), Date: d, RetrievedAt: at, FirstRetrievedAt: at, ConfirmedThrough: d}
		if _, err := s.Save(ctx, []fx.Rate{r}); err != nil {
			t.Fatal(err)
		}
	}
	put("frankfurter", "ECB", "USD/EUR", "0.9200", "2026-11-12")
	put("frankfurter", "ECB", "USD/EUR", "0.9300", "2026-11-13")
	put("frankfurter", "ECB", "GBP/EUR", "1.1500", "2026-11-13")
	put("frankfurter", "blended", "USD/EUR", "0.9250", "2026-11-13")
	put("other", "X", "USD/EUR", "0.9000", "2026-11-13")

	keys := func(rs []fx.Rate) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Provider+"/"+r.Source+"/"+r.Pair.String()+"/"+r.Date.String()+"="+r.Value.String())
		}
		return out
	}
	all, err := s.List(ctx, fx.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"frankfurter/ECB/GBP/EUR/2026-11-13=1.1500",
		"frankfurter/ECB/USD/EUR/2026-11-13=0.9300",
		"frankfurter/ECB/USD/EUR/2026-11-12=0.9200",
		"frankfurter/blended/USD/EUR/2026-11-13=0.9250",
		"other/X/USD/EUR/2026-11-13=0.9000",
	}
	got := keys(all)
	if len(got) != len(want) {
		t.Fatalf("listed %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %s, want %s", i, got[i], want[i])
		}
	}
	if all[1].Value.String() != "0.9300" || all[0].RetrievedAt != at {
		t.Errorf("exact value or retrieval time lost: %+v", all[:2])
	}

	// Walk the whole list two at a time through keys: no row skipped or repeated.
	var walked []fx.Rate
	var after *fx.RateKey
	for range 10 {
		page, err := s.List(ctx, fx.ListFilter{After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, page...)
		if len(page) < 2 {
			break
		}
		l := page[len(page)-1]
		after = &fx.RateKey{Provider: l.Provider, Source: l.Source, Pair: l.Pair, Date: l.Date}
	}
	if g := keys(walked); len(g) != len(want) || g[2] != want[2] || g[4] != want[4] {
		t.Errorf("paged walk = %v", g)
	}

	// Filters.
	f := func(f fx.ListFilter) int {
		rs, err := s.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(rs)
	}
	eur, usd := money.MustParseCurrency("EUR"), money.MustParseCurrency("USD")
	from, to := calendar.MustDate(2026, 11, 13), calendar.MustDate(2026, 11, 13)
	for name, c := range map[string]struct {
		f fx.ListFilter
		n int
	}{
		"provider": {fx.ListFilter{Provider: "other"}, 1},
		"source":   {fx.ListFilter{Source: "ECB"}, 3},
		"base":     {fx.ListFilter{Base: usd}, 4},
		"quote":    {fx.ListFilter{Quote: eur, Base: usd, Source: "ECB"}, 2},
		"from-to":  {fx.ListFilter{From: from, To: to}, 4},
		"none":     {fx.ListFilter{Base: eur}, 0},
	} {
		if n := f(c.f); n != c.n {
			t.Errorf("%s: %d rows, want %d", name, n, c.n)
		}
	}
}
