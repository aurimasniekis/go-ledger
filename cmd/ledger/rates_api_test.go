package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/fxrates"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

const (
	usdBody = `{"name":"US host","kind":"server","amount":{"amount":"20.00","currency":"USD"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-11-01"}`
	gbpBody = `{"name":"UK host","kind":"server","amount":{"amount":"30.00","currency":"GBP"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":6},"start_date":"2026-11-01"}`
)

// newRatesRig wires the rates routes to the real adapter and SQLite cache
// against a Frankfurter test double, with run metrics.
func newRatesRig(t *testing.T, db *store.DB, al store.AuditWriter, clock func() time.Time) (*frankfurterDouble, *fx.RefreshJob, *metrics.App, *httpapi.RatesDeps) {
	t.Helper()
	d := newDouble(t)
	svc, job, err := buildRates(ratesCfg(d.URL), eur(), db, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := metrics.NewApp("test")
	job.Meter = app
	return d, job, app, &httpapi.RatesDeps{Lister: fxrates.New(db), Policy: svc, Target: eur(), Interval: 24 * time.Hour, Timeout: 10 * time.Second,
		Audit: recorder{db: db, w: al}, Refresher: job, Now: clock}
}

// TestRatesAPIListStatusAndRefresh drives the rates routes as ledger serve
// wires them: the real adapter and SQLite cache against a Frankfurter test
// double, with the fixture clock. It checks the grants, exact values and
// provenance, filtering and cursor paging, the staleness flag (D11), the
// bounded and audited manual refresh (including a failed one), metrics and
// that readiness is untouched. No live service is involved.
func TestRatesAPIListStatusAndRefresh(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	reader := bankCall{t: t, f: f, hdr: f.signIn(t, "bob")}
	for _, b := range []string{usdBody, gbpBody} {
		if code, body, _ := admin.do("POST", "/api/v1/commitments", b); code != 201 {
			t.Fatalf("create commitment: %d %v", code, body)
		}
	}

	// Nothing cached yet; the read-only role may read, and the status says the
	// provider is on without a run yet.
	code, body, _ := reader.do("GET", "/api/v1/rates", "")
	if code != 200 || len(asList(t, body["items"])) != 0 || body["next_cursor"] != nil {
		t.Fatalf("empty list: %d %v", code, body)
	}
	code, st, _ := reader.do("GET", "/api/v1/rates/status", "")
	if code != 200 || st["enabled"] != true || st["provider"] != "frankfurter" || st["source"] != "ECB" || st["reporting_currency"] != "EUR" ||
		st["refresh_available"] != true || st["stale_after_days"] != float64(5) || st["last_run"] != nil || st["last_success_at"] != nil {
		t.Fatalf("status before a run: %d %v", code, st)
	}

	// Only an administrator may refresh; a token needs rates:write.
	if code, _, _ = reader.do("POST", "/api/v1/rates/refreshes", ""); code != 403 {
		t.Errorf("read-only refresh: %d", code)
	}
	tok := bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "rates:read"}}
	if code, _, _ = tok.do("POST", "/api/v1/rates/refreshes", ""); code != 403 {
		t.Errorf("token with rates:read refreshed: %d", code)
	}
	if code, _, _ = admin.do("POST", "/api/v1/rates/refreshes", `{"x":1}`); code != 400 {
		t.Errorf("unknown body field: %d", code)
	}
	if f.ratesDouble.count() != 0 {
		t.Fatal("a refused request reached the provider")
	}

	code, run, _ := admin.do("POST", "/api/v1/rates/refreshes", "")
	if code != 200 || run["trigger"] != "manual" || run["outcome"] != "succeeded" || run["currencies_refreshed"] != float64(2) || run["error"] != nil {
		t.Fatalf("refresh: %d %v", code, run)
	}
	if n := f.ratesDouble.count(); n != 2 {
		t.Errorf("provider calls = %d, want one per currency", n)
	}
	// Once a minute at most.
	if code, _, h := admin.do("POST", "/api/v1/rates/refreshes", ""); code != 429 || h.Get("Retry-After") == "" {
		t.Errorf("second refresh: %d retry-after %q", code, h.Get("Retry-After"))
	}

	// Exact values and provenance, in the documented order.
	code, body, _ = reader.do("GET", "/api/v1/rates", "")
	items := asList(t, body["items"])
	if code != 200 || len(items) != 2 {
		t.Fatalf("list: %d %v", code, body)
	}
	gbp, usd := asMap(t, items[0]), asMap(t, items[1])
	if gbp["base"] != "GBP" || gbp["value"] != "1.16210" || usd["base"] != "USD" || usd["value"] != "0.90992" {
		t.Errorf("rates %v %v", gbp, usd)
	}
	for k, want := range map[string]any{"provider": "frankfurter", "source": "ECB", "quote": "EUR", "date": "2026-11-20", "retrieved_at": "2026-11-21T09:00:00Z",
		"first_retrieved_at": "2026-11-21T09:00:00Z", "stale": false, "stale_reason": nil, "previous": nil} {
		if usd[k] != want {
			t.Errorf("usd[%s] = %v, want %v", k, usd[k], want)
		}
	}

	// Filters and paging.
	for q, n := range map[string]int{"base=USD": 1, "quote=EUR": 2, "quote=USD": 0, "provider=frankfurter&source=ECB": 2, "source=blended": 0,
		"date[gte]=2026-11-21": 0, "date[lte]=2026-11-20&date[gte]=2026-11-20": 2} {
		code, body, _ = reader.do("GET", "/api/v1/rates?"+q, "")
		if code != 200 || len(asList(t, body["items"])) != n {
			t.Errorf("?%s: %d %v, want %d items", q, code, body, n)
		}
	}
	for _, q := range []string{"base=usd", "base=USD&base=GBP", "date[gte]=2026-13-01", "date[gte]=x", "stale=true", "limit=0", "date[gt]=2026-11-01", "provider=Frank", "cursor=abc"} {
		if code, _, _ = reader.do("GET", "/api/v1/rates?"+q, ""); code != 400 {
			t.Errorf("?%s: %d, want 400", q, code)
		}
	}
	code, p1, _ := reader.do("GET", "/api/v1/rates?limit=1", "")
	cur, _ := p1["next_cursor"].(string)
	if code != 200 || len(asList(t, p1["items"])) != 1 || cur == "" {
		t.Fatalf("page 1: %d %v", code, p1)
	}
	code, p2, _ := reader.do("GET", "/api/v1/rates?limit=1&cursor="+cur, "")
	if code != 200 || len(asList(t, p2["items"])) != 1 || p2["next_cursor"] != nil || asMap(t, asList(t, p2["items"])[0])["base"] != "USD" {
		t.Fatalf("page 2: %d %v", code, p2)
	}
	if code, _, _ = reader.do("GET", "/api/v1/rates?limit=1&base=USD&cursor="+cur, ""); code != 400 {
		t.Errorf("a cursor with other filters: %d", code)
	}

	code, st, _ = reader.do("GET", "/api/v1/rates/status", "")
	lr := asMap(t, st["last_run"])
	if code != 200 || lr["outcome"] != "succeeded" || lr["trigger"] != "manual" || st["last_success_at"] == nil {
		t.Fatalf("status after a run: %d %v", code, st)
	}

	// Five days on, with no new retrieval, the rates are stale (D11) but still
	// listed with their age.
	f.now = f.now.Add(5 * 24 * time.Hour)
	_, body, _ = reader.do("GET", "/api/v1/rates?base=USD", "")
	if r := asMap(t, asList(t, body["items"])[0]); r["stale"] != true || r["stale_reason"] != "not_retrieved_recently" || r["value"] != "0.90992" {
		t.Errorf("stale rate: %v", r)
	}

	// A failed run is a 200 that says so, keeps the cache, and is audited.
	f.job.ManualGap = time.Nanosecond
	f.ratesDouble.set(503)
	code, run, _ = admin.do("POST", "/api/v1/rates/refreshes", "")
	er := asMap(t, run["error"])
	if code != 200 || run["outcome"] != "failed" || run["currencies_refreshed"] != float64(0) || er["kind"] != "unavailable" {
		t.Fatalf("failed refresh: %d %v", code, run)
	}
	if strings.Contains(fmtAny(run), f.ratesDouble.URL) {
		t.Errorf("the provider URL leaked: %v", run)
	}
	_, body, _ = reader.do("GET", "/api/v1/rates?base=USD", "")
	if r := asMap(t, asList(t, body["items"])[0]); r["value"] != "0.90992" {
		t.Errorf("a failed run changed the cache: %v", r)
	}
	code, aud, _ := admin.do("GET", "/api/v1/audit?action=rates.refresh_requested", "")
	entries := asList(t, aud["items"])
	if code != 200 || len(entries) != 2 {
		t.Fatalf("audit: %d %v", code, aud)
	}
	d0, d1 := asMap(t, asMap(t, entries[0])["details"]), asMap(t, asMap(t, entries[1])["details"])
	if d0["outcome"] != "succeeded" || d0["refreshed"] != "2" || d1["outcome"] != "failed" || d1["error_kind"] != "unavailable" || asMap(t, entries[0])["actor_kind"] != "user" {
		t.Errorf("audit details %v %v", d0, d1)
	}

	// Metrics count both runs; readiness never mentions rates.
	rec := httptest.NewRecorder()
	f.metrics.Registry.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	m := rec.Body.String()
	for _, want := range []string{
		`ledger_rates_refresh_runs_total{trigger="manual",outcome="success"} 1`,
		`ledger_rates_refresh_runs_total{trigger="manual",outcome="failure"} 1`,
		"ledger_rates_last_success_timestamp_seconds ", "ledger_rates_last_run_timestamp_seconds ",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %q", want)
		}
	}

	// Without a refresh job (provider off or no reporting currency) a refresh
	// is a clear 409; listing still works.
	f.rateDeps.Refresher = nil
	code, body, _ = admin.do("POST", "/api/v1/rates/refreshes", "")
	if code != 409 || body["code"] != "rates_refresh_unavailable" {
		t.Errorf("no job: %d %v", code, body)
	}
}

func fmtAny(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

// TestCLIRates drives "ledger rates" through the real handlers.
func TestCLIRates(t *testing.T) {
	c := newCLIFixture(t)
	c.api.now = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	admin := bankCall{t: t, f: c.api, hdr: c.api.signIn(t, "alice")}
	if code, _, _ := admin.do("POST", "/api/v1/commitments", usdBody); code != 201 {
		t.Fatal("create commitment")
	}
	if rc, out, _ := c.run(roToken, "rates", "list"); rc != 0 || !strings.Contains(out, "(none)") {
		t.Fatalf("empty list: %d %s", rc, out)
	}
	c.fail(roToken, 1, "forbidden", "rates", "refresh")
	rc, out, errb := c.run(adminToken, "rates", "refresh")
	if rc != 0 || !strings.Contains(out, "manual succeeded") || !strings.Contains(out, "refreshed:") {
		t.Fatalf("refresh: %d %s %s", rc, out, errb)
	}
	var page ledgerclient.Page[ledgerclient.CachedRate]
	c.json(roToken, &page, "rates", "list", "--base", "USD", "--all")
	if len(page.Items) != 1 || page.Items[0].Value != "0.90992" || page.Items[0].Date != "2026-11-20" || page.Items[0].Stale {
		t.Fatalf("list json: %+v", page)
	}
	rc, out, _ = c.run(roToken, "rates", "list")
	for _, want := range []string{"frankfurter", "ECB", "USD/EUR", "0.90992", "2026-11-20", "ok"} {
		if rc != 0 || !strings.Contains(out, want) {
			t.Fatalf("list text lacks %q: %d %s", want, rc, out)
		}
	}
	var st ledgerclient.RatesStatus
	c.json(roToken, &st, "rates", "status")
	if !st.Enabled || st.LastRun == nil || st.LastRun.Outcome != "succeeded" || st.StaleAfterDays != 5 {
		t.Fatalf("status: %+v", st)
	}
	if rc, out, _ = c.run(roToken, "rates", "status"); rc != 0 || !strings.Contains(out, "frankfurter") || !strings.Contains(out, "stale after:") {
		t.Fatalf("status text: %d %s", rc, out)
	}
	c.fail(adminToken, 1, "rate_limited", "rates", "refresh")
	c.fail(roToken, 1, "validation", "rates", "list", "--base", "usd")
}
