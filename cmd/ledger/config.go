package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
)

// runConfig implements "ledger config <check|env>". base supplies the
// environment and file system so tests can replace them; its other fields are
// filled from the command line.
func runConfig(args []string, stdout, stderr io.Writer, base config.Options) int {
	if len(args) == 0 {
		say(stderr, "usage: ledger config <check|env>")
		return 2
	}
	switch args[0] {
	case "env":
		if len(args) > 1 {
			say(stderr, "ledger config env: takes no arguments")
			return 2
		}
		_, _ = io.WriteString(stdout, config.ExampleEnv())
		return 0
	case "check":
		return configCheck(args[1:], stdout, stderr, base)
	default:
		say(stderr, fmt.Sprintf("ledger config: unknown subcommand %q (want check or env)", args[0]))
		return 2
	}
}

func configCheck(args []string, stdout, stderr io.Writer, base config.Options) int {
	fs := flag.NewFlagSet("ledger config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fl := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		say(stderr, "ledger config check: unexpected argument "+fmt.Sprintf("%q", fs.Arg(0)))
		return 2
	}
	opts := fl.Options()
	opts.LookupEnv, opts.ReadFile, opts.Environ = base.LookupEnv, base.ReadFile, base.Environ
	res, err := config.Load(opts)
	if err != nil {
		// Warn before the error: a typo in a variable name is often why
		// validation failed.
		var ce *config.Error
		if errors.As(err, &ce) {
			warnUnknown(stderr, ce.UnknownEnv)
		}
		say(stderr, "ledger: "+err.Error())
		return 1
	}
	reportSources(stdout, res)
	return 0
}

// reportSources prints how the configuration was assembled and the effective
// values. Secrets show only whether they are set; names, never values, of .env
// variables are listed.
func reportSources(w io.Writer, res *config.Result) {
	say(w, "configuration OK")
	switch {
	case res.ConfigFile == "":
		say(w, "config file: none named (defaults and environment only)")
	case res.ConfigFound:
		say(w, "config file:", res.ConfigFile)
	default:
		say(w, "config file:", res.ConfigFile, "(not found; using environment and defaults)")
	}
	if res.EnvFileFound {
		say(w, fmt.Sprintf("env file: %s (%d variables loaded: %s)", res.EnvFile, len(res.EnvLoaded), strings.Join(res.EnvLoaded, ", ")))
	} else {
		say(w, "env file:", res.EnvFile, "(not found)")
	}
	warnUnknown(w, res.UnknownEnv)
	say(w)
	for _, f := range config.Fields() {
		say(w, fmt.Sprintf("%s = %s  [%s]", f.Path, display(f, &res.Config), res.Sources[f.Path]))
	}
}

// warnUnknown prints one warning per unrecognised LEDGER_ variable name.
func warnUnknown(w io.Writer, names []string) {
	for _, n := range names {
		say(w, "warning: unknown variable "+n+" is ignored (check the spelling)")
	}
}

func display(f config.Field, c *config.Config) string {
	v := f.Value(c)
	if f.Secret {
		if s, _ := v.(string); s != "" {
			return "<set>"
		}
		return "<unset>"
	}
	switch x := v.(type) {
	case time.Duration:
		return x.String()
	case []string:
		return "[" + strings.Join(x, ",") + "]"
	case string:
		return fmt.Sprintf("%q", x)
	}
	return fmt.Sprint(v)
}
