package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/metrics"
)

// routeUnmatched is the route label for requests no pattern matched. Raw
// paths are never labels: they are unbounded and can carry IDs.
const routeUnmatched = "unmatched"

// reqInfo is filled in while a request is served and read by the metrics
// middleware afterwards.
type reqInfo struct {
	route string // the registered pattern, without its method
	kind  string // the error code of an error envelope, if one was sent
}

type reqInfoKey struct{}

func infoFrom(ctx context.Context) *reqInfo {
	i, _ := ctx.Value(reqInfoKey{}).(*reqInfo)
	return i
}

// routeOf strips the method and host from a ServeMux pattern: "GET /api/x/{id}"
// becomes "/api/x/{id}".
func routeOf(pattern string) string {
	if _, rest, ok := strings.Cut(pattern, " "); ok {
		pattern = strings.TrimSpace(rest)
	}
	if i := strings.Index(pattern, "/"); i > 0 {
		pattern = pattern[i:] // drop a host part
	}
	return pattern
}

// knownMethods bounds the method label; anything else is "OTHER".
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

func methodLabel(m string) string {
	if knownMethods[m] {
		return m
	}
	return "OTHER"
}

// metricsMiddleware counts requests by route template, method and status,
// times them, tracks in-flight requests and counts error envelopes by code.
// It sits inside the access log so it shares that middleware's status writer,
// and outside panic recovery so a recovered panic is counted as a 500.
func metricsMiddleware(m *metrics.App) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw, ok := w.(*statusWriter)
			if !ok {
				sw = &statusWriter{ResponseWriter: w}
			}
			info := &reqInfo{route: routeUnmatched}
			start := time.Now()
			m.InFlight.Add(1)
			defer func() {
				m.InFlight.Add(-1)
				status := "aborted" // the handler panicked with http.ErrAbortHandler
				if sw.status != 0 {
					status = strconv.Itoa(sw.status)
				} else if rec := recover(); rec == nil {
					status = "200"
				} else {
					defer panic(rec)
				}
				method := methodLabel(r.Method)
				m.Requests.Inc(info.route, method, status)
				m.Duration.Observe(time.Since(start).Seconds(), info.route, method)
				if info.kind != "" {
					m.Errors.Inc(info.kind)
				}
			}()
			next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, info)))
		})
	}
}

// routed wraps h so the request's route label is its registered pattern.
func routed(pattern string, h http.Handler) http.Handler {
	route := routeOf(pattern)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if i := infoFrom(r.Context()); i != nil {
			i.route = route
		}
		h.ServeHTTP(w, r)
	})
}
