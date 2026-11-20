// Command ledger is the Ledger personal-finance application: one process that
// serves the Management API and the embedded web UI.
package main

import (
	"fmt"
	"io"
	"os"

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
	case "config":
		return runConfig(args[1:], stdout, stderr, config.Options{})
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
	say(w, "  config    check or document the configuration (config check, config env)")
	say(w, "  version   print the build version")
	say(w, "  help      print this message")
}

// say writes one line. A failed write to the terminal has no useful recovery
// for a CLI, so the error is discarded explicitly.
func say(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}
