package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/health"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
	"github.com/aurimasniekis/go-ledger/internal/redact"
)

// serveEnv carries what tests replace: the environment and file system, and a
// callback that learns the address the server bound.
type serveEnv struct {
	Options   config.Options
	Listening func(net.Addr)
	// ManagementListening learns the address of the management listener, when
	// there is one.
	ManagementListening func(net.Addr)
}

// runServe implements "ledger serve": load the configuration, set up logging,
// listen and serve until ctx is cancelled (SIGINT or SIGTERM in production).
// Logs go to stderr.
func runServe(ctx context.Context, args []string, stderr io.Writer, env serveEnv) int {
	fs := flag.NewFlagSet("ledger serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fl := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		say(stderr, fmt.Sprintf("ledger serve: unexpected argument %q", fs.Arg(0)))
		return 2
	}
	opts := fl.Options()
	opts.LookupEnv, opts.ReadFile, opts.Environ = env.Options.LookupEnv, env.Options.ReadFile, env.Options.Environ
	res, err := config.Load(opts)
	if err != nil {
		// The logger needs a valid configuration, so this is plain text.
		var ce *config.Error
		if errors.As(err, &ce) {
			warnUnknown(stderr, ce.UnknownEnv)
		}
		say(stderr, "ledger: "+err.Error())
		return 1
	}
	cfg := res.Config

	// Everything that is logged or returned in an error from here on passes
	// through the redactor, which knows the configured secrets.
	redact.Default().Add(cfg.KEK.Value)
	log, err := logging.New(stderr, cfg.Log.Level, cfg.Log.Format, redact.Default())
	if err != nil {
		say(stderr, "ledger: "+err.Error())
		return 1
	}

	log.Info("starting", "version", version)
	if res.ConfigFound {
		log.Info("config file loaded", "path", res.ConfigFile)
	}
	if res.EnvFileFound {
		// Names only, never values.
		log.Info("env file loaded", "path", res.EnvFile, "variables", res.EnvLoaded)
	}
	for _, n := range res.UnknownEnv {
		log.Warn("unknown LEDGER_ variable is ignored; check the spelling", "variable", n)
	}

	ln, err := net.Listen("tcp", cfg.HTTP.Listen)
	if err != nil {
		log.Error("cannot listen", "address", cfg.HTTP.Listen, "error", err.Error())
		return 1
	}
	log.Info("listening", "address", ln.Addr().String())
	if env.Listening != nil {
		env.Listening(ln.Addr())
	}

	app := metrics.NewApp(version)
	ready := health.NewReadiness(log)
	// TODO(F11): open the SQLite store before this point and register its
	// check: ready.Add(health.Func("database", db.PingContext)). Per D17,
	// readiness gates only on the database; optional integrations belong on a
	// status endpoint, never here. Until then the list is empty and /readyz
	// reports ready with no checks.
	srv := httpapi.New(cfg.HTTP, log, httpapi.WithMetrics(app))

	// Metrics and health are unauthenticated. They go on a separate
	// management listener (loopback by default), or on the main one when
	// http.management_listen is empty.
	var mgmt *httpapi.Server
	var mln net.Listener
	if cfg.HTTP.ManagementListen == "" {
		registerOps(srv, app, ready)
		log.Warn("metrics and health are served on the main listener, unauthenticated; set http.management_listen to separate them")
	} else {
		mln, err = net.Listen("tcp", cfg.HTTP.ManagementListen)
		if err != nil {
			log.Error("cannot listen on the management address", "address", cfg.HTTP.ManagementListen, "error", err.Error())
			_ = ln.Close()
			return 1
		}
		log.Info("management listening", "address", mln.Addr().String())
		if !isLoopback(mln.Addr()) {
			log.Warn("the management listener is unauthenticated and is not on a loopback address; keep it off public interfaces",
				"address", mln.Addr().String())
		}
		if env.ManagementListening != nil {
			env.ManagementListening(mln.Addr())
		}
		mgmt = httpapi.New(cfg.HTTP, log, httpapi.WithMetrics(app))
		registerOps(mgmt, app, ready)
	}

	// Run both servers; when one stops, stop the other.
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	errc := make(chan error, 2)
	n := 1
	go func() { errc <- srv.Run(rctx, ln) }()
	if mgmt != nil {
		n++
		go func() { errc <- mgmt.Run(rctx, mln) }()
	}
	code := 0
	for i := 0; i < n; i++ {
		if err := <-errc; err != nil {
			log.Error("server stopped with an error", "error", err.Error())
			code = 1
		}
		stop()
	}
	return code
}

// registerOps adds /metrics, /healthz and /readyz.
func registerOps(s *httpapi.Server, app *metrics.App, ready *health.Readiness) {
	s.Handle("GET /metrics", app.Registry.Handler())
	s.Handle("GET /healthz", health.Healthz())
	s.Handle("GET /readyz", ready.Handler())
}

// isLoopback reports whether a is a loopback address.
func isLoopback(a net.Addr) bool {
	t, ok := a.(*net.TCPAddr)
	return ok && t.IP.IsLoopback()
}
