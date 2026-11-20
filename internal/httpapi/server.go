package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"github.com/aurimasniekis/go-ledger/internal/config"
)

// Server is Ledger's HTTP server: one mux behind the standard middleware
// (request ID, access log, panic recovery, body limit).
type Server struct {
	cfg  config.HTTP
	log  *slog.Logger
	mux  *http.ServeMux
	http *http.Server
}

// New builds a server from the HTTP section of the configuration. Register
// routes with Handle before calling Run.
func New(cfg config.HTTP, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, NotFound("no such resource"))
	})
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
// panic recovery, body limit.
func (s *Server) chain(h http.Handler) http.Handler {
	h = bodyLimitMiddleware(s.cfg.MaxBodyBytes)(h)
	h = recoverMiddleware(h)
	h = accessLogMiddleware(s.log)(h)
	return requestIDMiddleware(h)
}

// Handle registers a route pattern (net/http ServeMux syntax).
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

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
