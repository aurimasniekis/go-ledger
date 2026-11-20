// Package metrics is a small Prometheus text-format registry: counters,
// gauges and histograms with labels, and a guard that keeps label cardinality
// bounded (ADR-0009). It has no dependencies beyond the standard library.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// DefaultMaxSeries is the most label combinations one metric keeps.
	DefaultMaxSeries = 500
	// maxLabelValue is the longest label value kept; longer ones are cut.
	maxLabelValue = 64
)

// The name grammars of the Prometheus text format.
var (
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// DefaultBuckets are the histogram bounds, in seconds, for request durations.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type kind int

const (
	kindCounter kind = iota
	kindGauge
	kindHistogram
)

func (k kind) String() string {
	return [...]string{"counter", "gauge", "histogram"}[k]
}

// Registry holds metric families and renders them in the Prometheus text
// exposition format.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
	maxSer   int
	dropped  *Counter
}

// NewRegistry returns an empty registry. Series beyond maxSeries per metric
// (DefaultMaxSeries when <= 0) are dropped and counted in
// ledger_metrics_dropped_series_total.
func NewRegistry(maxSeries int) *Registry {
	if maxSeries <= 0 {
		maxSeries = DefaultMaxSeries
	}
	r := &Registry{families: map[string]*family{}, maxSer: maxSeries}
	r.dropped = r.NewCounter("ledger_metrics_dropped_series_total",
		"Label combinations refused because a metric reached its series limit.", "metric")
	return r
}

type family struct {
	reg     *Registry
	name    string
	help    string
	kind    kind
	labels  []string
	buckets []float64
	mu      sync.Mutex
	series  map[string]*series
}

type series struct {
	values []string
	value  float64   // counter, gauge
	counts []uint64  // histogram, per bucket (not cumulative)
	sum    float64   // histogram
	total  uint64    // histogram observations
	bounds []float64 // histogram, shared
}

func (r *Registry) add(name, help string, k kind, buckets []float64, labels []string) *family {
	checkNames(name, k, labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.families[name]; dup {
		panic("metrics: duplicate metric " + name)
	}
	f := &family{reg: r, name: name, help: help, kind: k, labels: labels, buckets: buckets, series: map[string]*series{}}
	r.families[name] = f
	return f
}

// checkNames panics on a name the exposition format cannot carry, and on a
// label set that would render a series twice. Like a duplicate metric, these
// are programmer errors found at start-up.
func checkNames(name string, k kind, labels []string) {
	if !metricNameRE.MatchString(name) || strings.HasPrefix(name, "__") {
		panic("metrics: invalid metric name " + strconv.Quote(name))
	}
	seen := make(map[string]bool, len(labels))
	for _, l := range labels {
		switch {
		case !labelNameRE.MatchString(l) || strings.HasPrefix(l, "__"):
			panic("metrics: " + name + ": invalid label name " + strconv.Quote(l))
		case k == kindHistogram && l == "le":
			panic("metrics: " + name + ": label le is reserved for histogram buckets")
		case seen[l]:
			panic("metrics: " + name + ": duplicate label " + strconv.Quote(l))
		}
		seen[l] = true
	}
}

// labelValue makes v safe to render. Invalid UTF-8 becomes U+FFFD, since
// Prometheus rejects a scrape with an invalid label value. A value longer than
// maxLabelValue bytes is cut back to a whole rune at or before the limit.
func labelValue(v string) string {
	v = strings.ToValidUTF8(v, "\uFFFD")
	if len(v) <= maxLabelValue {
		return v
	}
	cut := maxLabelValue
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut]
}

// get returns the series for the label values, or nil when the cardinality
// limit refuses a new one.
func (f *family) get(values []string) *series {
	if len(values) != len(f.labels) {
		panic(fmt.Sprintf("metrics: %s takes %d label values, got %d", f.name, len(f.labels), len(values)))
	}
	clean := make([]string, len(values))
	for i, v := range values {
		clean[i] = labelValue(v)
	}
	key := strings.Join(clean, "\xff")
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.series[key]; ok {
		return s
	}
	if len(f.series) >= f.reg.maxSer {
		if f.reg.dropped != nil && f != f.reg.dropped.f {
			f.reg.dropped.Inc(f.name)
		}
		return nil
	}
	s := &series{values: clean}
	if f.kind == kindHistogram {
		s.counts = make([]uint64, len(f.buckets))
		s.bounds = f.buckets
	}
	f.series[key] = s
	return s
}

// Counter is a monotonically increasing value per label combination.
type Counter struct{ f *family }

// NewCounter registers a counter with the given label names.
func (r *Registry) NewCounter(name, help string, labels ...string) *Counter {
	return &Counter{f: r.add(name, help, kindCounter, nil, labels)}
}

// Inc adds one. Label values must be bounded: route templates, enum names,
// never IDs, raw paths or free text.
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Add adds v (ignored when negative).
func (c *Counter) Add(v float64, values ...string) {
	if v < 0 {
		return
	}
	if s := c.f.get(values); s != nil {
		c.f.mu.Lock()
		s.value += v
		c.f.mu.Unlock()
	}
}

// Gauge is a value that goes up and down.
type Gauge struct{ f *family }

// NewGauge registers a gauge with the given label names.
func (r *Registry) NewGauge(name, help string, labels ...string) *Gauge {
	return &Gauge{f: r.add(name, help, kindGauge, nil, labels)}
}

// Add adds v (which may be negative).
func (g *Gauge) Add(v float64, values ...string) {
	if s := g.f.get(values); s != nil {
		g.f.mu.Lock()
		s.value += v
		g.f.mu.Unlock()
	}
}

// Set sets the value.
func (g *Gauge) Set(v float64, values ...string) {
	if s := g.f.get(values); s != nil {
		g.f.mu.Lock()
		s.value = v
		g.f.mu.Unlock()
	}
}

// Histogram counts observations in buckets.
type Histogram struct{ f *family }

// NewHistogram registers a histogram; buckets must be ascending (nil uses
// DefaultBuckets).
func (r *Registry) NewHistogram(name, help string, buckets []float64, labels ...string) *Histogram {
	if buckets == nil {
		buckets = DefaultBuckets
	}
	return &Histogram{f: r.add(name, help, kindHistogram, buckets, labels)}
}

// Observe records one value.
func (h *Histogram) Observe(v float64, values ...string) {
	s := h.f.get(values)
	if s == nil {
		return
	}
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	s.sum += v
	s.total++
	for i, b := range s.bounds {
		if v <= b {
			s.counts[i]++
			break
		}
	}
}

// Write renders every metric in the Prometheus text format, sorted by name
// and label values so the output is stable.
func (r *Registry) Write(w io.Writer) error {
	r.mu.Lock()
	names := make([]string, 0, len(r.families))
	for n := range r.families {
		names = append(names, n)
	}
	r.mu.Unlock()
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		r.mu.Lock()
		f := r.families[n]
		r.mu.Unlock()
		f.render(&b)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (f *family) render(b *strings.Builder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", f.name, escapeHelp(f.help), f.name, f.kind)
	keys := make([]string, 0, len(f.series))
	for k := range f.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := f.series[k]
		switch f.kind {
		case kindHistogram:
			var cum uint64
			for i, bound := range s.bounds {
				cum += s.counts[i]
				fmt.Fprintf(b, "%s_bucket%s %d\n", f.name, labelText(f.labels, s.values, "le", formatFloat(bound)), cum)
			}
			fmt.Fprintf(b, "%s_bucket%s %d\n", f.name, labelText(f.labels, s.values, "le", "+Inf"), s.total)
			fmt.Fprintf(b, "%s_sum%s %s\n", f.name, labelText(f.labels, s.values, "", ""), formatFloat(s.sum))
			fmt.Fprintf(b, "%s_count%s %d\n", f.name, labelText(f.labels, s.values, "", ""), s.total)
		default:
			fmt.Fprintf(b, "%s%s %s\n", f.name, labelText(f.labels, s.values, "", ""), formatFloat(s.value))
		}
	}
}

func labelText(names, values []string, extraName, extraValue string) string {
	if len(names) == 0 && extraName == "" {
		return ""
	}
	parts := make([]string, 0, len(names)+1)
	for i, n := range names {
		parts = append(parts, n+"=\""+escapeLabel(values[i])+"\"")
	}
	if extraName != "" {
		parts = append(parts, extraName+"=\""+extraValue+"\"")
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(v string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n").Replace(v)
}

func escapeHelp(v string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n").Replace(v)
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// Handler serves the registry in the Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = r.Write(w)
	})
}
