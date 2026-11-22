package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
)

// cancellingRefresher is a refresh job whose caller goes away while it runs.
type cancellingRefresher struct {
	fx.RunStatus
	cancel func()
	err    error
}

func (c *cancellingRefresher) Request(context.Context) (fx.RunStatus, error) {
	c.cancel()
	return c.RunStatus, c.err
}
func (c *cancellingRefresher) Last() (fx.RunStatus, bool) { return fx.RunStatus{}, false }
func (c *cancellingRefresher) LastSuccess() time.Time     { return time.Time{} }

// TestRatesRefreshAuditSurvivesACancelledRequest: a manual refresh asked the
// provider, so it is audited (ADR-0048 point 3) even when the caller goes away
// mid-run, both when the run finished and when it was cut short.
func TestRatesRefreshAuditSurvivesACancelledRequest(t *testing.T) {
	for name, c := range map[string]struct {
		run     fx.RunStatus
		err     error
		code    int
		outcome string
	}{
		"finished, then the caller left": {fx.RunStatus{Outcome: fx.RunSucceeded, Refreshed: 2}, nil, 200, "succeeded"},
		"cut short by the caller":        {fx.RunStatus{Outcome: fx.RunPartial, Refreshed: 1}, context.Canceled, 500, "cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAPIFixture(t)
			f.now = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
			admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.rateDeps.Refresher = &cancellingRefresher{RunStatus: c.run, cancel: cancel, err: c.err}
			req := httptest.NewRequest("POST", "/api/v1/rates/refreshes", strings.NewReader("")).WithContext(ctx)
			for k, v := range admin.hdr {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(rec, req)
			if rec.Code != c.code {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.code, rec.Body)
			}
			code, aud, _ := admin.do("GET", "/api/v1/audit?action=rates.refresh_requested", "")
			entries := asList(t, aud["items"])
			if code != 200 || len(entries) != 1 {
				t.Fatalf("audit: %d %v", code, aud)
			}
			if d := asMap(t, asMap(t, entries[0])["details"]); d["outcome"] != c.outcome || d["refreshed"] != map[bool]string{true: "2", false: "1"}[c.outcome == "succeeded"] {
				t.Errorf("details %v", d)
			}
		})
	}
}
