package metrics

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func render(t *testing.T, r *Registry) string {
	t.Helper()
	var b bytes.Buffer
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestTextFormat(t *testing.T) {
	r := NewRegistry(0)
	c := r.NewCounter("t_total", "A \"counter\".", "a")
	c.Inc("x\"y\nz\\")
	c.Add(2, "x\"y\nz\\")
	c.Add(-5, "x\"y\nz\\") // ignored
	g := r.NewGauge("t_gauge", "g")
	g.Set(3)
	g.Add(-1)
	h := r.NewHistogram("t_seconds", "h", []float64{1, 2}, "r")
	h.Observe(0.5, "a")
	h.Observe(1.5, "a")
	h.Observe(9, "a")
	out := render(t, r)
	for _, want := range []string{
		"# TYPE t_total counter\n",
		"t_total{a=\"x\\\"y\\nz\\\\\"} 3\n",
		"t_gauge 2\n",
		"t_seconds_bucket{r=\"a\",le=\"1\"} 1\n",
		"t_seconds_bucket{r=\"a\",le=\"2\"} 2\n",
		"t_seconds_bucket{r=\"a\",le=\"+Inf\"} 3\n",
		"t_seconds_sum{r=\"a\"} 11\n",
		"t_seconds_count{r=\"a\"} 3\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCardinalityGuard(t *testing.T) {
	r := NewRegistry(3)
	c := r.NewCounter("g_total", "h", "id")
	for i := 0; i < 10; i++ {
		c.Inc(fmt.Sprintf("v%d", i))
	}
	c.Inc("v0") // an existing series still counts
	out := render(t, r)
	if n := strings.Count(out, "g_total{"); n != 3 {
		t.Errorf("kept %d series, want 3:\n%s", n, out)
	}
	if !strings.Contains(out, "g_total{id=\"v0\"} 2\n") {
		t.Errorf("existing series stopped counting:\n%s", out)
	}
	if !strings.Contains(out, "ledger_metrics_dropped_series_total{metric=\"g_total\"} 7\n") {
		t.Errorf("drops not reported:\n%s", out)
	}
}

func TestLongLabelValuesAreCut(t *testing.T) {
	r := NewRegistry(0)
	r.NewCounter("l_total", "h", "v").Inc(strings.Repeat("a", 500))
	if strings.Contains(render(t, r), strings.Repeat("a", 65)) {
		t.Error("label value was not cut")
	}
}

func TestAppMetricsAndJobs(t *testing.T) {
	a := NewApp("v1.2.3")
	a.JobDone("rate-refresh", true, time.Second)
	a.JobDone("rate-refresh", false, time.Second)
	out := render(t, a.Registry)
	for _, want := range []string{
		"ledger_build_info{version=\"v1.2.3\",go_version=\"go",
		"ledger_job_runs_total{job=\"rate-refresh\",outcome=\"success\"} 1\n",
		"ledger_job_runs_total{job=\"rate-refresh\",outcome=\"failure\"} 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "job_id") {
		t.Error("job_id must not be a label")
	}
}

func TestHandlerContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRegistry(0).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q", ct)
	}
}

func TestLabelValueCutKeepsWholeRunes(t *testing.T) {
	r := NewRegistry(0)
	// The e-acute is two bytes and straddles byte 64, so the cut must back off to 63.
	r.NewCounter("c_total", "h", "v").Inc(strings.Repeat("a", 63) + "é" + "z")
	out := render(t, r)
	if want := "c_total{v=\"" + strings.Repeat("a", 63) + "\"} 1\n"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("output is not valid UTF-8")
	}
}

func TestInvalidUTF8LabelValueIsReplaced(t *testing.T) {
	r := NewRegistry(0)
	r.NewCounter("c_total", "h", "v").Inc("a\xffb")
	out := render(t, r)
	if want := "c_total{v=\"a" + "\uFFFD" + "b\"} 1\n"; !strings.Contains(out, want) {
		t.Errorf("invalid byte not replaced; output:\n%s", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("output is not valid UTF-8")
	}
}

func TestLongValuesWithSharedPrefixShareASeries(t *testing.T) {
	r := NewRegistry(0)
	c := r.NewCounter("p_total", "h", "v")
	c.Inc(strings.Repeat("a", 64) + "one")
	c.Inc(strings.Repeat("a", 64) + "two")
	if out := render(t, r); !strings.Contains(out, "p_total{v=\""+strings.Repeat("a", 64)+"\"} 2\n") {
		t.Errorf("values sharing their first 64 bytes did not merge:\n%s", out)
	}
}

func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	f()
}

func TestInvalidNamesPanic(t *testing.T) {
	cases := []struct {
		name string
		f    func(*Registry)
	}{
		{"metric name with a dash", func(r *Registry) { r.NewCounter("bad-name", "h") }},
		{"empty metric name", func(r *Registry) { r.NewCounter("", "h") }},
		{"metric name starting with a digit", func(r *Registry) { r.NewCounter("9lives", "h") }},
		{"reserved metric prefix", func(r *Registry) { r.NewCounter("__x", "h") }},
		{"label with a space", func(r *Registry) { r.NewCounter("c_total", "h", "bad label") }},
		{"reserved label prefix", func(r *Registry) { r.NewCounter("c_total", "h", "__x") }},
		{"le on a histogram", func(r *Registry) { r.NewHistogram("h_seconds", "h", nil, "le") }},
		{"duplicate label", func(r *Registry) { r.NewCounter("c_total", "h", "a", "a") }},
	}
	for _, tc := range cases {
		mustPanic(t, func() { tc.f(NewRegistry(0)) })
	}
}

func TestValidNamesAccepted(t *testing.T) {
	r := NewRegistry(0)
	r.NewCounter("ledger:colon_ok_1", "h", "route_1")
	r.NewCounter("counter_le_ok", "h", "le") // le is only reserved on histograms
	r.NewHistogram("h_seconds", "h", nil, "route")
}
