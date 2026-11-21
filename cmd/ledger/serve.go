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
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/health"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/mcpserver"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
	"github.com/aurimasniekis/go-ledger/internal/outbox"
	"github.com/aurimasniekis/go-ledger/internal/pulsebus"
	"github.com/aurimasniekis/go-ledger/internal/redact"
	"github.com/aurimasniekis/go-ledger/internal/reporting"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
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
	eb := cfg.Banking.EnableBanking
	redact.Default().Add(cfg.KEK.Value, eb.App.PrivateKey, eb.Developer.PrivateKey, eb.App.AppID, eb.Developer.AppID, cfg.Auth.OIDC.ClientSecret)
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

	// Banking-data providers (ADR-0023). The keys are parsed now so a bad key
	// stops startup; nothing connects to Enable Banking until a connection is
	// made or refreshed. The connection API (B4) and the scheduler (B6) use
	// these providers.
	bankProviders, err := bankingProviders(cfg.Banking.EnableBanking)
	if err != nil {
		log.Error("invalid Enable Banking configuration", "error", err.Error())
		return 1
	}
	for _, p := range bankProviders {
		c := p.Capabilities()
		log.Info("Enable Banking application configured", "application", c.Application, "mode", string(c.Mode), "environment", string(c.Environment), "account_access", string(c.AccountAccess()))
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
	parts, err := buildAPI(db, auditLog, cfg.Auth, cfg.KEK.Value, auth.NewArgon2id(auth.DefaultParams, hashConcurrency), nil, log)
	if err == nil {
		parts.oidc, err = oidcDeps(cfg.Auth.OIDC, nil)
	}
	if err != nil {
		log.Error("invalid sign-in settings", "error", err.Error())
		_ = ln.Close()
		return 1
	}
	if parts.oidc != nil {
		log.Info("single sign-on is enabled; the provider is contacted at the first sign-in", "provider", cfg.Auth.OIDC.Name,
			"provisioning", cfg.Auth.OIDC.Provisioning.Enabled, "password_sign_in", cfg.Auth.Password.Enabled)
	}
	// Bearer API tokens are tried before the session cookie (ADR-0029); every
	// state-changing token request is audited through the recorder.
	srv := httpapi.New(cfg.HTTP, log, httpapi.WithMetrics(app), httpapi.WithUI(web.Handler(ui)), httpapi.WithAuditRecorder(auditLog, nil),
		httpapi.WithAuthenticators(httpapi.NewTokenAuth(parts.tokens), httpapi.NewSessionAuth(parts.auth)), httpapi.WithRecentAuth(parts.auth.Recent))
	// Automatic payment matching (X2, ADR-0039): after every bank refresh that
	// stored something, and on request through the API.
	parts.matching = newMatcher(db, auditLog, parts.matches, zone.Zone(), nil, log)
	if cfg.MCP.Enabled {
		// The MCP tools run through the API's own routes, so they share its
		// validation, grants and token-write audit (ADR-0038).
		msrv, err := mcpserver.NewServer(mcpserver.Options{Caller: mcpserver.InProcess(srv.Handler()), Audit: auditLog, Version: version, Log: log})
		if err != nil {
			log.Error("cannot build the MCP server", "error", err.Error())
			_ = ln.Close()
			return 1
		}
		parts.mcp = mcpserver.Handler(msrv, mcpserver.HandlerOptions{
			Limiter:         mcpserver.NewLimiter(cfg.MCP.RequestsPerMinute, cfg.MCP.Burst, nil),
			MaxRequestBytes: cfg.MCP.MaxRequestBytes,
		})
		log.Info("MCP endpoint enabled", "path", httpapi.MCPPath, "requests_per_minute", cfg.MCP.RequestsPerMinute)
	} else {
		log.Info("MCP endpoint disabled")
	}
	// Bank connections (B4, ADR-0040) and the background refresh (B6, ADR-0028)
	// share one engine, so one connection is never refreshed twice at once.
	facts := bankfacts.New(db, nil).WithAudit(auditLog)
	engine := bankimport.New(facts, bankimport.Options{Log: log, Meter: app, AfterRefresh: func(ctx context.Context, _ bankimport.Result) {
		// A failed run is retried after the next refresh, or on request.
		if _, err := parts.matching.Run(ctx); err != nil && ctx.Err() == nil {
			log.Warn("automatic payment matching after a bank refresh failed", "error", err.Error())
		}
	}})
	var sched *bankimport.Scheduler
	if cfg.Banking.Refresh.Enabled {
		sched = bankimport.NewScheduler(facts, engine, providerMap(bankProviders), bankimport.SchedulerOptions{
			Log: log, Meter: app, Interval: cfg.Banking.Refresh.Interval, MaxBackoff: cfg.Banking.Refresh.MaxBackoff, StaleAfter: cfg.Banking.StaleAfter,
		})
	}
	connSvc := &bankconnect.Service{Facts: facts, Providers: providerMap(bankProviders), Engine: engine, Audit: auditLog, RedirectURL: cfg.Banking.RedirectURL,
		ConsentTTL: cfg.Banking.ConsentTTL, StaleAfter: cfg.Banking.StaleAfter, Log: log, RefreshTimeout: refreshTimeout(cfg.HTTP.WriteTimeout)}
	if sched != nil {
		connSvc.Schedule = sched.Status
	}
	parts.banking = connSvc
	if cfg.Reporting.Currency != "" {
		// Validated with the configuration.
		parts.reportingCurrency, _ = moneypkg.ParseCurrency(cfg.Reporting.Currency)
	}
	registerAPI(srv, auditLog, parts, zone.Zone(), nil)

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
	// Background refresh of bank connections (B6, ADR-0028). One engine is shared
	// with every other trigger of a refresh (the connection API, B4), so one
	// connection is never refreshed twice at once. It stops with rctx and is
	// waited for before the database closes.
	var refreshDone chan struct{}
	if sched != nil {
		refreshDone = make(chan struct{})
		go func() { defer close(refreshDone); sched.Run(rctx) }()
		log.Info("background bank refresh is enabled", "interval", cfg.Banking.Refresh.Interval.String(), "applications", len(bankProviders))
	} else {
		log.Info("background bank refresh is disabled; connections are refreshed on request only")
	}
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
	// A refresh on request that outlived its request is waited for too (bounded,
	// so a stuck provider cannot hold the shutdown): its audit entry needs the
	// database.
	wctx, wcancel := context.WithTimeout(context.Background(), refreshShutdownWait)
	if err := connSvc.Wait(wctx); err != nil {
		// Past the bound: cancel the runs so the engine records them as
		// interrupted (and audits them) while the database is still open.
		log.Error("a bank refresh was still running at shutdown; cancelling it", "error", err.Error())
		connSvc.CancelRefreshes()
		cctx, ccancel := context.WithTimeout(context.Background(), refreshCancelWait)
		if err := connSvc.Wait(cctx); err != nil {
			log.Error("a cancelled bank refresh did not end in time; its run stays open until its lease expires", "error", err.Error())
		}
		ccancel()
	}
	wcancel()
	if refreshDone != nil {
		<-refreshDone
	}
	if pulseDone != nil {
		<-pulseDone
	}
	return code
}

// refreshShutdownWait bounds how long shutdown waits for a bank refresh that
// outlived its request before the database closes.
const refreshShutdownWait = 30 * time.Second

// refreshCancelWait bounds the second wait, for cancelled runs to record
// their interruption.
const refreshCancelWait = 5 * time.Second

// refreshTimeout bounds a refresh on request below the HTTP write timeout
// (Go does not cancel a request when its response deadline passes, so an
// unbounded run would finish with nobody to answer); 5 seconds are left to
// write the answer.
func refreshTimeout(write time.Duration) time.Duration {
	if write <= 0 {
		return 0
	}
	return max(write-5*time.Second, time.Second)
}

// auditLog is what the API needs of the audit log: a writer for changes and a
// reader for GET /audit. store.AuditLog implements it.
type auditLog interface {
	store.AuditWriter
	audit.Reader
}

// registerAPI registers the Management API with its dependencies (see
// buildAPI). Changes record their audit entries in the same transaction
// through al (ADR-0025). zone is the reporting time zone; now defaults to
// time.Now.
func registerAPI(srv *httpapi.Server, al auditLog, parts apiParts, zone calendar.Zone, now func() time.Time) {
	// The reporting zone dates commitment creation (D38 1a, ADR-0042).
	parts.payments = parts.payments.WithZone(zone)
	parts.matches = parts.matches.WithZone(zone)
	matching := parts.matching
	if matching == nil {
		matching = newMatcher(parts.db, al, parts.matches, zone, now, nil)
	}
	httpapi.RegisterAPI(srv, httpapi.Deps{
		Users:       parts.users,
		Commitments: parts.commitments,
		Occurrences: parts.commitments.Occurrences(),
		Payments:    parts.payments,
		Matches:     parts.matches,
		Banking:     parts.banking,
		Matching:    matching,
		Audit:       al,
		Auth:        parts.auth,
		Tokens:      parts.tokens,
		OIDC:        parts.oidc,
		MCP:         parts.mcp,
		// The outlook (ADR-0044) reads stored bank facts. No rate service is
		// wired until the rate provider adapter (FX2) exists: only amounts
		// already in the reporting currency convert, the rest are excluded
		// and flagged.
		Banks:             bankfacts.New(parts.db, nil),
		ReportingCurrency: parts.reportingCurrency,
		Zone:              zone,
		Now:               now,
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
