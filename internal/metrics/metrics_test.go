package metrics

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
