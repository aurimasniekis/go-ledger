package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/health"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
	"github.com/aurimasniekis/go-ledger/internal/outbox"
	"github.com/aurimasniekis/go-ledger/internal/pulsebus"
	"github.com/aurimasniekis/go-ledger/internal/redact"
	"github.com/aurimasniekis/go-ledger/internal/reporting"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/users"
	"github.com/aurimasniekis/go-ledger/internal/web"
)

// serveEnv carries what tests replace: the environment and file system, and a
// callback that learns the address the server bound.
type serveEnv struct {
	Options   config.Options
	Listening func(net.Addr)
	// Reporting replaces the reporting-zone host lookup in tests.
	Reporting reporting.Options
	// AfterOpen, in tests, receives the open store (for example to break it).
	AfterOpen func(*store.DB)
	// UI replaces the embedded frontend; nil means web.Assets().
	UI fs.FS
	// ManagementListening learns the address of the management listener, when
	// there is one.
	ManagementListening func(net.Addr)
	// PulseDial replaces the Pulse connection in tests.
	PulseDial outbox.Dialer
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

	// Open the database first: a store or zone problem must stop startup
	// before the port is bound.
	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o700); err != nil {
		log.Error("cannot create the database directory", "path", cfg.Database.Path, "error", err.Error())
		return 1
	}
	// One Ledger process per database: held until exit, released by the kernel
	// if the process dies (ADR-0019).
	lock, err := store.LockInstance(cfg.Database.Path)
	if err != nil {
		log.Error("cannot use the database", "path", cfg.Database.Path, "error", err.Error())
		return 1
	}
	defer func() { _ = lock.Close() }()
	for _, w := range store.ModeWarnings(cfg.Database.Path) {
		log.Warn("database file permissions", "detail", w)
	}
	db, err := store.Open(ctx, cfg.Database.Path, store.Options{BusyTimeout: cfg.Database.BusyTimeout})
	if err != nil {
		log.Error("cannot open the database", "path", cfg.Database.Path, "error", err.Error())
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Warn("closing the database failed", "error", err.Error())
		}
	}()
	log.Info("database ready", "path", db.Path(), "schema_version", db.SchemaVersion())

	if env.AfterOpen != nil {
		env.AfterOpen(db)
	}

	auditLog := store.NewAuditLog(db)
	zoneOpts := env.Reporting
	zoneOpts.Audit = auditLog
	zone, err := reporting.Resolve(ctx, db, cfg.Reporting.Timezone, log, zoneOpts)
	if err != nil {
		log.Error("cannot determine the reporting time zone", "error", err.Error())
		return 1
	}
	// The outbox writer takes the events.pulse switch and source (N3). No feature
	// registers events yet, so nothing calls it; features add definitions to the
	// registry in their own change (docs/events.md).
	ob, err := outbox.New(outbox.Config{Registry: outbox.NewRegistry(), Enabled: cfg.Events.Pulse.Enabled, Source: cfg.Events.Pulse.Source})
	if err != nil {
		log.Error("invalid event settings", "error", err.Error())
		return 1
	}
	_ = ob
	// TODO(monthly-view task): hand zone to the services that need it (monthly view, recurrence).
	_ = zone

	ln, err := net.Listen("tcp", cfg.HTTP.Listen)
	if err != nil {
		log.Error("cannot listen", "address", cfg.HTTP.Listen, "error", err.Error())
		return 1
	}
	log.Info("listening", "address", ln.Addr().String())
	if env.Listening != nil {
		env.Listening(ln.Addr())
	}

	ui := env.UI
	if ui == nil {
		ui = web.Assets()
	}
	app := metrics.NewApp(version)
	ready := health.NewReadiness(log)
	// Per D17, readiness gates only on the database; optional integrations belong on a
	// status endpoint, never here.
	ready.Add(health.Func("database", db.Ping))
	srv := httpapi.New(cfg.HTTP, log, httpapi.WithMetrics(app), httpapi.WithUI(web.Handler(ui)), httpapi.WithAuditRecorder(auditLog, nil))
	registerAPI(srv, db, auditLog)
	// Sign-in sessions and API tokens arrive with their own tasks; until an
	// authenticator is installed, every non-public API route answers 401.
	log.Warn("no API authentication method is available yet; API routes other than the API description answer 401")

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
	// Pulse publishing is optional: disabled means no worker and no connection,
	// and a Pulse problem never affects startup or readiness (D17); the worker
	// retries and reports through logs and metrics.
	// Outbox retention (expiry and purge) runs whether or not publishing is on,
	// so history is bounded and enabling later never replays older rows.
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		outbox.NewRetention(outbox.RetentionConfig{DB: db, Log: log, Meter: app, Publishing: cfg.Events.Pulse.Enabled}).Run(rctx)
	}()
	var pulseDone chan struct{}
	if cfg.Events.Pulse.Enabled {
		dial := env.PulseDial
		if dial == nil {
			dial = pulsebus.Dialer(pulsebus.Config{
				URL: cfg.Events.Pulse.URL, CredsFile: cfg.Events.Pulse.CredsFile, CAFile: cfg.Events.Pulse.CAFile,
				Name: cfg.Events.Pulse.Name, Source: cfg.Events.Pulse.Source,
			})
		}
		pub, err := outbox.NewPublisher(outbox.PublisherConfig{DB: db, Dial: dial, Log: log, Meter: app})
		if err != nil {
			log.Error("cannot start the event publisher", "error", err.Error())
		} else {
			pulseDone = make(chan struct{})
			go func() { defer close(pulseDone); pub.Run(rctx) }()
			log.Info("event publishing to Pulse is enabled; events leave this machine", "source", cfg.Events.Pulse.Source)
		}
	}
	code := 0
	for i := 0; i < n; i++ {
		if err := <-errc; err != nil {
			log.Error("server stopped with an error", "error", err.Error())
			code = 1
		}
		stop()
	}
	<-retentionDone // before the database closes
	if pulseDone != nil {
		<-pulseDone
	}
	return code
}

// auditLog is what the API needs of the audit log: a writer for changes and a
// reader for GET /audit. store.AuditLog implements it.
type auditLog interface {
	store.AuditWriter
	audit.Reader
}

// registerAPI registers the Management API with its dependencies. Changes
// record their audit entries in the same transaction through al (ADR-0025).
func registerAPI(srv *httpapi.Server, db *store.DB, al auditLog) {
	httpapi.RegisterAPI(srv, httpapi.Deps{
		Users: users.New(db, al, nil, nil),
		Audit: al,
	})
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
