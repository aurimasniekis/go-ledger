package httpapi

import (
	"context"
	"net/http"
	"time"
)

// ReadyCheck reports whether one dependency is usable. Per D17, readiness
// gates only on the database; optional integrations belong on a status
// endpoint instead.
type ReadyCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// readyTimeout bounds one readiness probe.
const readyTimeout = 2 * time.Second

// ReadyHandler returns the /readyz handler: 200 {"status":"ok"} when every
// check passes, else 503 {"status":"unavailable","failing":[names]}. Error
// details are logged, never sent. The F4 health task may replace this with its
// own checker list; the checks themselves carry over.
func (s *Server) ReadyHandler(checks ...ReadyCheck) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		var failing []string
		for _, c := range checks {
			if err := c.Check(ctx); err != nil {
				s.log.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "error", err.Error())
				failing = append(failing, c.Name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if len(failing) == 0 {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		out := `{"status":"unavailable","failing":[`
		for i, n := range failing {
			if i > 0 {
				out += ","
			}
			out += `"` + n + `"`
		}
		_, _ = w.Write([]byte(out + "]}"))
	})
}
