// Command ledger is the Ledger personal-finance application: one process that
// serves the Management API and the embedded web UI.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/aurimasniekis/go-ledger/internal/config"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line and returns the process exit code. It takes its
// writers as arguments so it can be tested without touching process state.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		say(stdout, "ledger", version)
		return 0
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		// Once shutdown starts, restore default signal handling so a second
		// Ctrl-C ends the process instead of being swallowed during the drain.
		go func() { <-ctx.Done(); stop() }()
		return runServe(ctx, args[1:], stderr, serveEnv{Options: config.Options{}})
	case "backup":
		return runBackup(args[1:], stdout, stderr, opsEnv{})
	case "restore":
		return runRestore(args[1:], stdout, stderr, opsEnv{})
	case "config":
		return runConfig(args[1:], stdout, stderr, config.Options{})
	case "auth":
		return runAuth(args[1:], stdout, stderr, opsEnv{Stdin: os.Stdin})
	case "me", "grants", "users", "commitments", "occurrences", "audit":
		return runMgmt(args, stdout, stderr, mgmtEnv{})
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		say(stderr, fmt.Sprintf("ledger: unknown command %q", args[0]))
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	say(w, "usage: ledger <command>")
	say(w)
	say(w, "commands:")
	say(w, "  serve     start the HTTP server")
	say(w, "  backup    write a verified copy of the database (backup, backup prune)")
	say(w, "  restore   replace the database with a backup (stop serve first)")
	say(w, "  config    check or document the configuration (config check, config env)")
	say(w, "  auth      set a user's password from standard input (auth set-password; stop serve first)")
	say(w, "  me, grants, users, commitments, occurrences, audit")
	say(w, "            manage a running server through its API with a token (ledger <command> help)")
	say(w, "  version   print the build version")
	say(w, "  help      print this message")
}

// say writes one line. A failed write to the terminal has no useful recovery
// for a CLI, so the error is discarded explicitly.
func say(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}
