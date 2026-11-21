package frankfurter_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/providers/frankfurter"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newServer(t *testing.T, h http.HandlerFunc) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r)
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[len(s.reqs)-1]
}

func body(b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

func client(t *testing.T, s *server, mod func(*frankfurter.Options)) *frankfurter.Client {
	t.Helper()
	o := frankfurter.Options{BaseURL: s.URL}
	if mod != nil {
		mod(&o)
	}
	c, err := frankfurter.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func cur(codes ...string) []money.Currency {
	var out []money.Currency
	for _, c := range codes {
		out = append(out, money.MustParseCurrency(c))
	}
	return out
}

func mustErr(t *testing.T, err error, kind fx.ErrorKind, code string) {
	t.Helper()
	_ = providerErr(t, err, kind, code)
}

func providerErr(t *testing.T, err error, kind fx.ErrorKind, code string) *fx.ProviderError {
	t.Helper()
	var pe *fx.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a ProviderError", err)
	}
	if pe.Kind != kind || pe.Code != code {
		t.Fatalf("error = kind %s code %s, want %s %s", pe.Kind, pe.Code, kind, code)
	}
	return pe
}

func TestLatestParsesExactRatesAndSendsTheRoute(t *testing.T) {
	s := newServer(t, body(fixture(t, "latest_usd_eur_gbp_jpy.json")))
	c := client(t, s, nil)
	if c.Name() != "frankfurter" || c.Source() != "ECB" {
		t.Fatalf("name %q source %q", c.Name(), c.Source())
	}
	got, err := c.Latest(context.Background(), money.MustParseCurrency("USD"), cur("EUR", "GBP", "JPY"))
	if err != nil {
		t.Fatal(err)
	}
	r := s.last()
	if r.URL.Path != "/v2/rates" || r.URL.Query().Get("base") != "USD" || r.URL.Query().Get("quotes") != "EUR,GBP,JPY" || r.URL.Query().Get("providers") != "ECB" || r.URL.Query().Has("date") {
		t.Errorf("request = %s", r.URL)
	}
	if r.Header.Get("User-Agent") != "ledger-fx" || r.Header.Get("Authorization") != "" {
		t.Errorf("headers = %v", r.Header)
	}
	if len(got) != 3 {
		t.Fatalf("got %d quotes", len(got))
	}
	want := []string{"0.90992", "0.76192", "159.40"} // the trailing zero is kept: exactly as published
	for i, q := range got {
		if q.Value.String() != want[i] || q.Source != "ECB" || q.Pair.Base.Code() != "USD" || q.Date.String() != "2026-11-20" {
			t.Errorf("quote %d = %+v", i, q)
		}
	}
}

func TestNormalizeSource(t *testing.T) {
	for in, want := range map[string]string{"ecb": "ECB", " Ecb ": "ECB", "BLENDED": "blended", "blended": "blended", "": ""} {
		if got := frankfurter.NormalizeSource(in); got != want {
			t.Errorf("NormalizeSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSourceKeyCaseIsNormalised(t *testing.T) {
	s := newServer(t, body([]byte("{}")))
	for in, want := range map[string]string{"ecb": "ECB", "Blended": frankfurter.Blended} {
		c := client(t, s, func(o *frankfurter.Options) { o.Source = in })
		if c.Source() != want {
			t.Errorf("%q -> %q, want %q", in, c.Source(), want)
		}
	}
}

func TestOnDateAndSources(t *testing.T) {
	ctx := context.Background()
	usd, eur := money.MustParseCurrency("USD"), cur("EUR")
	d, _ := calendar.ParseDate("2026-11-14")

	// ECB answers a Saturday with the previous working day; the quote says so.
	s := newServer(t, body(fixture(t, "ecb_weekend_usd_eur.json")))
	got, err := client(t, s, nil).OnDate(ctx, usd, eur, d)
	if err != nil || len(got) != 1 || got[0].Date.String() != "2026-11-13" || got[0].Value.String() != "0.90481" {
		t.Fatalf("ecb = %+v, %v", got, err)
	}
	if q := s.last().URL.Query(); q.Get("date") != "2026-11-14" || q.Get("providers") != "ECB" {
		t.Errorf("query = %v", q)
	}

	// The blended route sends no providers parameter and names its source.
	s = newServer(t, body(fixture(t, "blended_usd_eur_2026-11-14.json")))
	c := client(t, s, func(o *frankfurter.Options) { o.Source = frankfurter.Blended })
	got, err = c.OnDate(ctx, usd, eur, d)
	if err != nil || got[0].Source != "blended" || c.Source() != "blended" || got[0].Date != d {
		t.Fatalf("blended = %+v, %v", got, err)
	}
	if s.last().URL.Query().Has("providers") {
		t.Error("blended route sent providers")
	}

	if _, err := c.OnDate(ctx, usd, eur, calendar.Date{}); err == nil {
		t.Error("OnDate without a date succeeded")
	}
}

func TestUnpublishedPairIsAbsentNotAnError(t *testing.T) {
	s := newServer(t, body([]byte("[]")))
	got, err := client(t, s, nil).Latest(context.Background(), money.MustParseCurrency("USD"), cur("EUR"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestStatusClassification(t *testing.T) {
	ctx := context.Background()
	usd, eur := money.MustParseCurrency("USD"), cur("EUR")
	status := func(code int, hdr map[string]string, b []byte) *server {
		return newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range hdr {
				w.Header().Set(k, v)
			}
			w.WriteHeader(code)
			_, _ = w.Write(b)
		})
	}
	t.Run("invalid currency is permanent", func(t *testing.T) {
		_, err := client(t, status(422, nil, fixture(t, "invalid_currency.json")), nil).Latest(ctx, usd, eur)
		mustErr(t, err, fx.KindPermanent, "http_422")
		if !errors.Is(err, fx.ErrPermanent) {
			t.Error("not ErrPermanent")
		}
	})
	t.Run("quota", func(t *testing.T) {
		_, err := client(t, status(429, map[string]string{"Retry-After": "120"}, nil), nil).Latest(ctx, usd, eur)
		pe := providerErr(t, err, fx.KindRateLimited, "http_429")
		if pe.RetryAfter != 2*time.Minute || !errors.Is(err, fx.ErrRateLimited) {
			t.Errorf("retry after %v", pe.RetryAfter)
		}
	})
	t.Run("retry-after as a date and capped", func(t *testing.T) {
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		mod := func(o *frankfurter.Options) { o.Now = func() time.Time { return now } }
		_, err := client(t, status(429, map[string]string{"Retry-After": now.Add(time.Hour).Format(http.TimeFormat)}, nil), mod).Latest(ctx, usd, eur)
		if pe := providerErr(t, err, fx.KindRateLimited, "http_429"); pe.RetryAfter != time.Hour {
			t.Errorf("date form = %v", pe.RetryAfter)
		}
		_, err = client(t, status(429, map[string]string{"Retry-After": "99999999"}, nil), mod).Latest(ctx, usd, eur)
		if pe := providerErr(t, err, fx.KindRateLimited, "http_429"); pe.RetryAfter != 24*time.Hour {
			t.Errorf("cap = %v", pe.RetryAfter)
		}
		_, err = client(t, status(429, map[string]string{"Retry-After": "soon"}, nil), mod).Latest(ctx, usd, eur)
		if pe := providerErr(t, err, fx.KindRateLimited, "http_429"); pe.RetryAfter != 0 {
			t.Errorf("junk = %v", pe.RetryAfter)
		}
	})
	t.Run("server errors are transient", func(t *testing.T) {
		for _, code := range []int{500, 502, 503, 504} {
			_, err := client(t, status(code, nil, []byte("oops")), nil).Latest(ctx, usd, eur)
			mustErr(t, err, fx.KindUnavailable, "http_"+strconv.Itoa(code))
		}
	})
	t.Run("other client errors are permanent", func(t *testing.T) {
		_, err := client(t, status(404, nil, nil), nil).Latest(ctx, usd, eur)
		mustErr(t, err, fx.KindPermanent, "http_404")
	})
}

func TestOutageTimeoutAndCancellation(t *testing.T) {
	usd, eur := money.MustParseCurrency("USD"), cur("EUR")
	t.Run("connection refused", func(t *testing.T) {
		s := newServer(t, body(nil))
		c := client(t, s, nil)
		s.Close()
		_, err := c.Latest(context.Background(), usd, eur)
		mustErr(t, err, fx.KindUnavailable, "network")
		if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "http") {
			t.Errorf("error leaks the URL: %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		s := newServer(t, func(http.ResponseWriter, *http.Request) { <-release })
		defer close(release)
		c := client(t, s, func(o *frankfurter.Options) { o.Timeout = 30 * time.Millisecond })
		_, err := c.Latest(context.Background(), usd, eur)
		mustErr(t, err, fx.KindUnavailable, "network")
	})
	t.Run("caller cancellation stays visible", func(t *testing.T) {
		release := make(chan struct{})
		s := newServer(t, func(http.ResponseWriter, *http.Request) { <-release })
		defer close(release)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		_, err := client(t, s, nil).Latest(ctx, usd, eur)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled in the chain", err)
		}
	})
}

func TestMalformedAnswersAreRefusedWhole(t *testing.T) {
	ctx := context.Background()
	usd := money.MustParseCurrency("USD")
	cases := map[string]struct {
		body string
		code string
	}{
		"not json":          {"<html>", "malformed_response"},
		"object not array":  {`{"rates":{}}`, "malformed_response"},
		"trailing data":     {`[] []`, "malformed_response"},
		"bad date":          {`[{"date":"13/11/2026","base":"USD","quote":"EUR","rate":1.1}]`, "malformed_response"},
		"exponent rate":     {`[{"date":"2026-11-13","base":"USD","quote":"EUR","rate":1e-1}]`, "invalid_rate"},
		"string rate":       {`[{"date":"2026-11-13","base":"USD","quote":"EUR","rate":"abc"}]`, "malformed_response"},
		"zero rate":         {`[{"date":"2026-11-13","base":"USD","quote":"EUR","rate":0}]`, "invalid_rate"},
		"negative rate":     {`[{"date":"2026-11-13","base":"USD","quote":"EUR","rate":-1.2}]`, "invalid_rate"},
		"missing rate":      {`[{"date":"2026-11-13","base":"USD","quote":"EUR"}]`, "invalid_rate"},
		"other base":        {`[{"date":"2026-11-13","base":"GBP","quote":"EUR","rate":1.1}]`, "unexpected_pair"},
		"unasked quote":     {`[{"date":"2026-11-13","base":"USD","quote":"CHF","rate":1.1}]`, "unexpected_pair"},
		"duplicate quote":   {`[{"date":"2026-11-13","base":"USD","quote":"EUR","rate":1.1},{"date":"2026-11-13","base":"USD","quote":"EUR","rate":1.2}]`, "unexpected_pair"},
		"one bad of two":    {`[{"date":"2026-11-13","base":"USD","quote":"EUR","rate":1.1},{"date":"2026-11-13","base":"USD","quote":"GBP","rate":0}]`, "invalid_rate"},
		"too many decimals": {`[{"date":"2026-11-13","base":"USD","quote":"EUR","rate":0.1234567890123456789012345678901234567890}]`, "invalid_rate"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := client(t, newServer(t, body([]byte(c.body))), nil).Latest(ctx, usd, cur("EUR", "GBP"))
			mustErr(t, err, fx.KindPermanent, c.code)
			if got != nil {
				t.Errorf("a refused answer returned quotes: %v", got)
			}
		})
	}
	t.Run("later date than asked", func(t *testing.T) {
		d, _ := calendar.ParseDate("2026-11-12")
		_, err := client(t, newServer(t, body(fixture(t, "ecb_weekend_usd_eur.json"))), nil).OnDate(ctx, usd, cur("EUR"), d)
		mustErr(t, err, fx.KindPermanent, "date_after_requested")
	})
	t.Run("oversized", func(t *testing.T) {
		_, err := client(t, newServer(t, body([]byte("["+strings.Repeat(" ", 2<<20)+"]"))), nil).Latest(ctx, usd, cur("EUR"))
		mustErr(t, err, fx.KindPermanent, "response_too_large")
	})
}

func TestBadRequestsNeverReachTheNetwork(t *testing.T) {
	s := newServer(t, body([]byte("[]")))
	c := client(t, s, nil)
	usd := money.MustParseCurrency("USD")
	for name, call := range map[string]func() error{
		"no base":      func() error { _, e := c.Latest(context.Background(), money.Currency{}, cur("EUR")); return e },
		"no quotes":    func() error { _, e := c.Latest(context.Background(), usd, nil); return e },
		"same on both": func() error { _, e := c.Latest(context.Background(), usd, cur("USD")); return e },
	} {
		if err := call(); !errors.Is(err, fx.ErrPermanent) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(s.reqs) != 0 {
		t.Errorf("%d requests were sent", len(s.reqs))
	}
}

func TestNewValidatesOptions(t *testing.T) {
	for name, o := range map[string]frankfurter.Options{
		"relative url":  {BaseURL: "/v2"},
		"ftp":           {BaseURL: "ftp://example.com"},
		"credentials":   {BaseURL: "https://u:p@example.com"},
		"query":         {BaseURL: "https://example.com?k=1"},
		"bad source":    {Source: "EC B"},
		"source in url": {Source: "ECB&x=1"},
	} {
		if _, err := frankfurter.New(o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c, err := frankfurter.New(frankfurter.Options{})
	if err != nil || c.Source() != "ECB" {
		t.Fatalf("defaults: %v", err)
	}
	if _, err := frankfurter.New(frankfurter.Options{Source: "TCMB.FOREX"}); err != nil {
		t.Errorf("dotted key refused: %v", err)
	}
}
