package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/redact"
	"github.com/aurimasniekis/go-ledger/internal/reporting"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// serveEnv carries what tests replace: the environment and file system, and a
// callback that learns the address the server bound.
type serveEnv struct {
	Options   config.Options
	Listening func(net.Addr)
	// Reporting replaces the reporting-zone host lookup in tests.
	Reporting reporting.Options
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

	zone, err := reporting.Resolve(ctx, db, cfg.Reporting.Timezone, log, env.Reporting)
	if err != nil {
		log.Error("cannot determine the reporting time zone", "error", err.Error())
		return 1
	}
	// TODO: hand zone to the services that need it (monthly view, recurrence).
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

	srv := httpapi.New(cfg.HTTP, log)
	srv.Handle("GET /readyz", srv.ReadyHandler(httpapi.ReadyCheck{Name: "database", Check: db.Ping}))
	// TODO(F4): /healthz, metrics and the shared readiness checker list.
	if err := srv.Run(ctx, ln); err != nil {
		log.Error("server stopped with an error", "error", err.Error())
		return 1
	}
	return 0
}
