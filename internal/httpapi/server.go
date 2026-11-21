package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
)

// Server is Ledger's HTTP server: one mux behind the standard middleware
// (request ID, access log, panic recovery, body limit).
type Server struct {
	cfg  config.HTTP
	log  *slog.Logger
	mux  *http.ServeMux
	http *http.Server
	m    *metrics.App
	ui   http.Handler
	// api holds every route under /api. It is separate from mux so its own
	// 404 and 405 answers can be turned into the error envelope (ADR-0011).
	api       *http.ServeMux
	apiMW     []func(http.Handler) http.Handler
	apiRoutes []Route
	authn     []Authenticator
	// recentAuth checks recent authentication ([WithRecentAuth]).
	recentAuth func(authz.Principal) bool
	// trusted are the reverse proxies whose X-Forwarded-For is believed.
	trusted []netip.Prefix
	// warnedForwarding is set once the untrusted-proxy warning was logged
	// ([Server.forwardingWarning]).
	warnedForwarding atomic.Bool
	// auditTokenWrites records a token's state-changing request ([WithAuditRecorder]).
	auditTokenWrites func(r *http.Request, p authz.Principal, pattern string) error
}

// Route is a registered API route: an HTTP method, a ServeMux path pattern
// such as "/api/v1/examples/{id}", and who may call it.
type Route struct {
	Method string
	Path   string
	Access Access
}

func (r Route) String() string { return r.Method + " " + r.Path }

// Option configures a Server.
type Option func(*Server)

// WithMetrics records request metrics in m.
func WithMetrics(m *metrics.App) Option { return func(s *Server) { s.m = m } }

// WithUI serves h for every path that no registered route and no /api path
// claims (the embedded single-page application). Without it such paths are 404.
func WithUI(h http.Handler) Option { return func(s *Server) { s.ui = h } }

// WithAPIMiddleware wraps every handler registered under /api with mw, first
// one outermost. It runs after route matching (r.Pattern is set) and never for
// the 404 and 405 answers of unmatched API requests.
//
// Authentication and grant checks are not middleware: they come from the
// access rule of each route ([Server.HandleAPI], [WithAuthenticators]) and run
// before these, so middleware only sees allowed requests and can rely on the
// principal in the context (except on [Public] routes). Requests refused with
// 401 or 403 never reach it.
func WithAPIMiddleware(mw ...func(http.Handler) http.Handler) Option {
	return func(s *Server) { s.apiMW = append(s.apiMW, mw...) }
}

// New builds a server from the HTTP section of the configuration. Register
// routes with Handle before calling Run.
func New(cfg config.HTTP, log *slog.Logger, opts ...Option) *Server {
	s := &Server{cfg: cfg, log: log, mux: http.NewServeMux(), api: http.NewServeMux(), trusted: parseTrusted(cfg.TrustedProxies)}
	for _, o := range opts {
		o(s)
	}
	// API tokens skip interactive steps, so every state-changing token
	// request must be audited (ADR-0029): a server that accepts bearer tokens
	// needs the recorder.
	for _, a := range s.authn {
		if _, ok := a.(TokenAuth); ok && s.auditTokenWrites == nil {
			panic("httpapi: the API token authenticator needs WithAuditRecorder")
		}
	}
	// The catch-all is labelled "unmatched", not "/", in metrics.
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if i := infoFrom(r.Context()); i != nil {
			i.route = routeUnmatched
		}
		WriteError(w, r, NotFound("no such resource"))
	})
	// Every /api path goes to the API mux and never to the UI: unmatched ones
	// get the JSON envelope (404, or 405 with Allow). "/api" is registered
	// exactly too: ServeMux would otherwise redirect it to "/api/".
	s.mux.Handle("/api", http.HandlerFunc(s.serveAPI))
	s.mux.Handle("/api/", http.HandlerFunc(s.serveAPI))
	if s.ui != nil {
		s.Handle("/", s.ui)
	} else {
		s.Handle("/", notFound)
	}
	s.http = &http.Server{
		Handler:           s.chain(s.mux),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		// Errors from net/http itself (bad TLS, bad requests) go through slog.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	return s
}

// chain wraps h in the middleware, outermost first: request ID, access log,
// metrics (when enabled), panic recovery, the untrusted-proxy warning, body
// limit.
func (s *Server) chain(h http.Handler) http.Handler {
	h = bodyLimitMiddleware(s.cfg.MaxBodyBytes)(h)
	h = s.forwardingWarning(h)
	h = recoverMiddleware(h)
	if s.m != nil {
		h = metricsMiddleware(s.m)(h)
	}
	h = accessLogMiddleware(s.log)(h)
	return requestIDMiddleware(h)
}

// Handle registers a route pattern (net/http ServeMux syntax) outside /api.
// API routes are registered with [Server.HandleAPI]; Handle panics on them.
func (s *Server) Handle(pattern string, h http.Handler) {
	path := routeOf(pattern)
	if path == "/api" || strings.HasPrefix(path, "/api/") {
		panic(fmt.Sprintf("httpapi: API route %q must be registered with HandleAPI and an access rule", pattern))
	}
	s.mux.Handle(pattern, routed(pattern, h))
}

// HandleAPI registers an API route: the pattern must be "METHOD /api/..."
// without a host. Its handler runs only for callers that a allows:
// authentication and grant checks run first, then the API middleware
// (ADR-0021). A state-changing method guarded by a read grant panics.
func (s *Server) HandleAPI(pattern string, a Access, h http.Handler) {
	path := routeOf(pattern)
	if path != "/api" && !strings.HasPrefix(path, "/api/") {
		panic(fmt.Sprintf("httpapi: HandleAPI route %q is not under /api", pattern))
	}
	method, rest, ok := strings.Cut(strings.TrimSpace(pattern), " ")
	if !ok || strings.TrimSpace(rest) != path {
		panic(fmt.Sprintf("httpapi: API route %q must be \"METHOD /api/...\" without a host", pattern))
	}
	checkAccess(method, a)
	for i := len(s.apiMW) - 1; i >= 0; i-- {
		h = s.apiMW[i](h)
	}
	h = s.authorize(pattern, a, h) // outermost: authentication and grants come first
	s.api.Handle(pattern, routed(pattern, h))
	s.apiRoutes = append(s.apiRoutes, Route{Method: method, Path: path, Access: a})
}

// APIRoutes returns the registered routes under prefix (for example
// [APIPrefix]), sorted by path then method.
func (s *Server) APIRoutes(prefix string) []Route {
	var out []Route
	for _, r := range s.apiRoutes {
		if r.Path == prefix || strings.HasPrefix(r.Path, prefix+"/") {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// serveAPI dispatches an /api request. A matched route is served as is. For
// an unmatched one, ServeMux's own plain-text 404 or 405 (which carries the
// Allow header) is replaced by the error envelope.
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	if i := infoFrom(r.Context()); i != nil {
		i.route = routeUnmatched
	}
	if _, pattern := s.api.Handler(r); pattern != "" {
		s.api.ServeHTTP(w, r)
		return
	}
	ew := &envelopeWriter{ResponseWriter: w, r: r}
	s.api.ServeHTTP(ew, r)
}

// envelopeWriter turns ServeMux's plain-text 404 and 405 into the error
// envelope and drops their bodies. Other statuses (a path-cleaning redirect)
// pass through.
type envelopeWriter struct {
	http.ResponseWriter
	r        *http.Request
	replaced bool
}

func (e *envelopeWriter) WriteHeader(code int) {
	switch code {
	case http.StatusNotFound:
		e.replaced = true
		WriteError(e.ResponseWriter, e.r, NotFound("no such resource"))
	case http.StatusMethodNotAllowed:
		e.replaced = true
		WriteError(e.ResponseWriter, e.r, MethodNotAllowed(e.ResponseWriter.Header().Get("Allow")))
	default:
		e.ResponseWriter.WriteHeader(code)
	}
}

func (e *envelopeWriter) Write(p []byte) (int, error) {
	if e.replaced {
		return len(p), nil
	}
	return e.ResponseWriter.Write(p)
}

// Handler returns the full handler, for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run serves on ln until ctx is cancelled, then shuts down gracefully:
// it stops accepting connections and waits up to the configured shutdown
// timeout for in-flight requests to finish before closing the rest. It returns
// nil after a clean shutdown.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	s.log.Info("shutting down", "timeout", s.cfg.ShutdownTimeout.String())
	sctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(sctx); err != nil {
		_ = s.http.Close()
		s.log.Warn("shutdown timed out; open connections were closed", "error", err.Error())
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	s.log.Info("shutdown complete")
	return nil
}
