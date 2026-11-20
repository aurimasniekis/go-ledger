// Package health serves /healthz (the process is up) and /readyz (the
// dependencies Ledger cannot work without are reachable).
//
// Readiness gates only on required dependencies, which is the database (D17).
// Optional integrations (a bank provider, OIDC, Pulse, a model) must never be
// registered here: they are reported on a status endpoint, so an outage or a
// disabled integration cannot take Ledger out of rotation.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// CheckTimeout bounds one readiness check.
const CheckTimeout = 3 * time.Second

// Checker reports whether one required dependency is reachable.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

type funcChecker struct {
	name string
	fn   func(context.Context) error
}

func (c funcChecker) Name() string                    { return c.name }
func (c funcChecker) Check(ctx context.Context) error { return c.fn(ctx) }

// Func adapts a function, such as (*sql.DB).PingContext, to a Checker.
func Func(name string, fn func(context.Context) error) Checker { return funcChecker{name, fn} }

// Readiness is the list of required-dependency checks behind /readyz.
type Readiness struct {
	mu       sync.RWMutex
	checkers []Checker
	log      *slog.Logger
}

// NewReadiness returns an empty list. Register the database check with Add
// once the store is open.
func NewReadiness(log *slog.Logger) *Readiness { return &Readiness{log: log} }

// Add registers a required dependency.
func (r *Readiness) Add(c Checker) {
	r.mu.Lock()
	r.checkers = append(r.checkers, c)
	r.mu.Unlock()
}

type report struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Ready runs every check and reports the outcome by check name ("ok" or
// "failed"). Error text is logged, never returned: it may name paths.
func (r *Readiness) Ready(ctx context.Context) (bool, map[string]string) {
	r.mu.RLock()
	cs := append([]Checker(nil), r.checkers...)
	r.mu.RUnlock()
	out := map[string]string{}
	ok := true
	for _, c := range cs {
		cctx, cancel := context.WithTimeout(ctx, CheckTimeout)
		err := c.Check(cctx)
		cancel()
		if err != nil {
			ok = false
			out[c.Name()] = "failed"
			if r.log != nil {
				r.log.WarnContext(ctx, "readiness check failed", "check", c.Name(), "error", err.Error())
			}
			continue
		}
		out[c.Name()] = "ok"
	}
	return ok, out
}

// Names lists the registered check names, sorted.
func (r *Readiness) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := make([]string, 0, len(r.checkers))
	for _, c := range r.checkers {
		n = append(n, c.Name())
	}
	sort.Strings(n)
	return n
}

func write(w http.ResponseWriter, code int, v report) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Healthz answers 200 whenever the process can serve a request.
func Healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, report{Status: "ok", Checks: map[string]string{}})
	})
}

// Handler answers /readyz: 200 when every check passes, else 503.
func (r *Readiness) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ok, checks := r.Ready(req.Context())
		if ok {
			write(w, http.StatusOK, report{Status: "ready", Checks: checks})
			return
		}
		write(w, http.StatusServiceUnavailable, report{Status: "not_ready", Checks: checks})
	})
}
