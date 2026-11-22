package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// The management commands (ledger me, grants, users, commitments, occurrences,
// matching, bank, rates, tokens, audit) talk to a running server through the public Management API with an
// API token, using pkg/ledgerclient. They never open the database and have no
// other way in (ADR-0032): what a token may do is exactly what the server's
// grants allow.

const defaultServerURL = "http://127.0.0.1:8080"

// mgmtEnv supplies the process state the management commands read, so tests
// can replace it. Zero values mean the real process.
type mgmtEnv struct {
	Getenv     func(string) string
	ReadFile   func(string) ([]byte, error)
	Stat       func(string) (fs.FileInfo, error)
	HTTPClient *http.Client
}

func (e mgmtEnv) getenv(k string) string {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return os.Getenv(k)
}

func (e mgmtEnv) readFile(p string) ([]byte, error) {
	if e.ReadFile != nil {
		return e.ReadFile(p)
	}
	return os.ReadFile(p)
}

func (e mgmtEnv) stat(p string) (fs.FileInfo, error) {
	if e.Stat != nil {
		return e.Stat(p)
	}
	return os.Stat(p)
}

type mgmt struct {
	stdout, stderr io.Writer
	env            mgmtEnv
	// per-command common flags
	url, tokenFile, cfgFile, output string
	insecure                        bool
	timeout                         time.Duration
}

// clientConfig is the optional client configuration file (YAML).
type clientConfig struct {
	URL       string `yaml:"url"`
	TokenFile string `yaml:"token_file"`
	Token     string `yaml:"token"`
	Output    string `yaml:"output"`
}

type verb struct {
	usage string
	run   func(m *mgmt, ctx context.Context, args []string) int
}

var mgmtVerbs = map[string]map[string]verb{
	"users": {
		"list":   {"list [--role R]... [--disabled true|false] [--username-prefix P] [--limit N] [--cursor C] [--all]", usersList},
		"get":    {"get <id>", usersGet},
		"create": {"create --username U --role R [--display-name N]", usersCreate},
		"update": {"update <id> [--version N] [--username U] [--display-name N | --clear-display-name] [--role R] [--disabled true|false]", usersUpdate},
	},
	"commitments": {
		"list":   {"list [--kind K]... [--priority P]... [--currency C]... [--limit N] [--cursor C] [--all]", commitmentsList},
		"get":    {"get <id>", commitmentsGet},
		"create": {"create --name N --kind K --amount 12.50 --currency EUR --frequency monthly|annual [--interval N] [--month M] --day D --start-date YYYY-MM-DD [--end-date D] [--priority P] [--notes T] [--match-tolerance 0.50] [--leeway unknown|none|period|deadline] [--leeway-days N] [--leeway-until D] [--leeway-conditions T] [--leeway-consequences T] [--leeway-source T]", commitmentsCreate},
		"update": {"update <id> [--version N] [--effective-from D] [--name N] [--kind K] [--amount A] [--currency C] [--frequency F] [--interval N] [--month M] [--day D] [--start-date D] [--end-date D | --clear-end-date] [--priority P | --clear-priority] [--notes T | --clear-notes] [--match-tolerance A | --clear-match-tolerance] [--leeway KIND] [--leeway-days N] [--leeway-until D] [--leeway-conditions T] [--leeway-consequences T] [--leeway-source T] [--clear-leeway]", commitmentsUpdate},
		"delete": {"delete <id> [--version N]", commitmentsDelete},
	},
	"occurrences": {
		"list":           {"list --month YYYY-MM [--commitment-id ID]... [--limit N] [--cursor C] [--all]", occurrencesList},
		"get":            {"get <id>", occurrencesGet},
		"payments":       {"payments <id> [--limit N] [--cursor C] [--all]   (manual payments of the occurrence)", occurrencesPayments},
		"pay":            {"pay <id> --amount 12.50 --currency EUR --paid-on YYYY-MM-DD [--note T]   (record a manual payment)", occurrencesPay},
		"payment":        {"payment <id> <payment-id>   (get a manual payment)", occurrencesPayment},
		"delete-payment": {"delete-payment <id> <payment-id> [--version N]   (--version is the payment's)", occurrencesDeletePayment},
		"override":       {"override <id> --status paid|unpaid --reason T [--version N]", occurrencesOverride},
		"undo-override":  {"undo-override <id> [--version N]", occurrencesUndoOverride},
	},
	"matching": {
		"run": {"run   (run the automatic payment matching once; needs commitments:write)", matchingRun},
	},
	"bank": {
		"applications": {"applications   (configured provider applications; needs connections:read)", bankApplications},
		"institutions": {"institutions <application> --country DE   (banks an application offers)", bankInstitutions},
		"list":         {"list [--limit N] [--cursor C] [--all]   (connections with account freshness)", bankList},
		"get":          {"get <id>", bankGet},
		"connect":      {"connect --application A --institution-id I --country DE [--psu-type personal|business] [--valid-for-days N]   (prints the bank's consent page; needs connections:write)", bankConnect},
		"reconnect":    {"reconnect <id> [--valid-for-days N]   (new consent for an existing connection)", bankReconnect},
		"refresh":      {"refresh <id>   (fetch now; a failed refresh keeps stored data and is shown in the answer)", bankRefresh},
		"disconnect":   {"disconnect <id> [--version N]   (ends the provider session; stored data stays as history)", bankDisconnect},
	},
	"rates": {
		"list":    {"list [--provider P] [--source S] [--base CUR] [--quote CUR] [--from YYYY-MM-DD] [--until YYYY-MM-DD] [--limit N] [--cursor C] [--all]   (cached exchange rates with date, retrieval time and staleness; administrator, needs rates:read)", ratesList},
		"status":  {"status   (rate provider, freshness policy and the last refresh; administrator, needs rates:read)", ratesStatus},
		"refresh": {"refresh   (ask the rate provider for fresh rates now; administrator, needs rates:write; at most one a minute)", ratesRefresh},
	},
	"tokens": {
		"list":   {"list [--owner-id ID] [--limit N] [--cursor C] [--all]   (every user's tokens; needs tokens:read)", tokensList},
		"get":    {"get <id>", tokensGet},
		"revoke": {"revoke <id>   (needs tokens:write)", tokensRevoke},
	},
	"audit": {
		"list": {"list [--action A]... [--action-prefix P] [--actor-kind K]... [--actor-user-id ID] [--actor-token-id ID] [--target-type T] [--target-id ID] [--since TIME] [--until TIME] [--limit N] [--cursor C] [--all]", auditList},
	},
}

const mgmtCommonUsage = `
Common flags (after the command):
  --url URL          Ledger base URL (env LEDGER_URL; default ` + defaultServerURL + `)
  --token-file PATH  file holding the API token (env LEDGER_TOKEN holds the token itself)
  --client-config P  client config file (env LEDGER_CLIENT_CONFIG; default ~/.config/ledger/client.yaml)
  --output FMT       text (default) or json
  --insecure         allow plain http:// to a non-loopback host
  --timeout D        per-request timeout, greater than zero (default 30s); a bank refresh
                     (bank refresh, MCP refresh_bank_connection) waits at least 90s, above
                     the server's own bound (write_timeout minus 5s), so a slow refresh
                     is reported as running, not as a timeout. Raise --timeout above
                     write_timeout if the server's was raised.
The token comes from --token-file, then LEDGER_TOKEN, then the config file's
token_file, then its token (the file must not be readable by others). It is
never printed.`

// runMgmt dispatches one management command.
func runMgmt(args []string, stdout, stderr io.Writer, env mgmtEnv) int {
	m := &mgmt{stdout: stdout, stderr: stderr, env: env}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "me", "grants":
		return m.runLeaf(cmd, rest, func(ctx context.Context, c *ledgerclient.Client) error {
			if cmd == "me" {
				p, err := c.Me(ctx)
				if err != nil {
					return err
				}
				return m.emit(p, func(w io.Writer) {
					kv(w, "kind", p.Kind, "user", p.UserID, "token", deref(p.TokenID), "role", p.Role, "grants", strings.Join(p.Grants, ", "))
				})
			}
			g, err := c.Grants(ctx)
			if err != nil {
				return err
			}
			return m.emit(g, func(w io.Writer) {
				tw := table(w, "RESOURCE", "ACTIONS", "ADMIN-ONLY", "DESCRIPTION")
				for _, r := range g.Resources {
					row(tw, r.Name, strings.Join(r.Actions, ","), fmt.Sprint(r.AdminOnly), r.Description)
				}
				_ = tw.Flush()
				_, _ = fmt.Fprintln(w)
				for _, r := range g.Roles {
					_, _ = fmt.Fprintf(w, "role %s: %s\n", printable(r.Name), strings.Join(r.Grants, ", "))
				}
			})
		})
	}
	vs := mgmtVerbs[cmd]
	if len(rest) == 0 || rest[0] == "-h" || rest[0] == "--help" || rest[0] == "help" {
		mgmtUsage(stderr, cmd)
		if len(rest) > 0 {
			return 0
		}
		return 2
	}
	v, ok := vs[rest[0]]
	if !ok {
		say(stderr, fmt.Sprintf("ledger %s: unknown subcommand %q", cmd, rest[0]))
		mgmtUsage(stderr, cmd)
		return 2
	}
	ctx := context.Background()
	return v.run(m, ctx, rest[1:])
}

func mgmtUsage(w io.Writer, cmd string) {
	say(w, "usage: ledger "+cmd+" <subcommand> [flags]")
	var names []string
	for n := range mgmtVerbs[cmd] {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		say(w, "  ledger "+cmd+" "+mgmtVerbs[cmd][n].usage)
	}
	say(w, mgmtCommonUsage)
}

// flags returns a flag set with the common flags registered.
func (m *mgmt) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("ledger "+name, flag.ContinueOnError)
	fs.SetOutput(m.stderr)
	fs.StringVar(&m.url, "url", "", "")
	fs.StringVar(&m.tokenFile, "token-file", "", "")
	fs.StringVar(&m.cfgFile, "client-config", "", "")
	fs.StringVar(&m.output, "output", "", "")
	fs.BoolVar(&m.insecure, "insecure", false, "")
	fs.Func("timeout", "", func(s string) error {
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		// 0 or less would disable the HTTP timeout and let a stalled server
		// hang the command forever.
		if d <= 0 {
			return errors.New("must be greater than zero")
		}
		m.timeout = d
		return nil
	})
	m.timeout = 30 * time.Second
	return fs
}

// parse parses args; with wantID the first argument is the object ID (so flags
// may follow it). It returns the ID and whether the command should continue.
func (m *mgmt) parse(fs *flag.FlagSet, args []string, wantID bool) (string, int) {
	id := ""
	if wantID {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			say(m.stderr, fs.Name()+": an object ID is required as the first argument")
			return "", 2
		}
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", -1
		}
		return "", 2
	}
	if fs.NArg() > 0 {
		say(m.stderr, fmt.Sprintf("%s: unexpected argument %q", fs.Name(), fs.Arg(0)))
		return "", 2
	}
	return id, 0
}

// runLeaf runs a command with no arguments besides the common flags.
func (m *mgmt) runLeaf(name string, args []string, f func(context.Context, *ledgerclient.Client) error) int {
	fs := m.flags(name)
	if _, rc := m.parse(fs, args, false); rc != 0 {
		return max(rc, 0)
	}
	return m.call(f)
}

// call builds the client from the common flags and runs f, reporting errors.
func (m *mgmt) call(f func(context.Context, *ledgerclient.Client) error) int {
	c, err := m.client()
	if err != nil {
		say(m.stderr, "ledger: "+printable(err.Error()))
		return 2
	}
	// No overall deadline: --timeout bounds each request (the client's HTTP
	// timeout), so a long --all walk is not cut short as a whole.
	if err := f(context.Background(), c); err != nil {
		m.report(err)
		return 1
	}
	return 0
}

// resolve reads the client configuration file and decides URL, token, output.
func (m *mgmt) resolve() (url, token, output string, err error) {
	var cfg clientConfig
	path, explicit := m.cfgFile, m.cfgFile != ""
	if !explicit {
		path = m.env.getenv("LEDGER_CLIENT_CONFIG")
		explicit = path != ""
	}
	if path == "" {
		dir := m.env.getenv("XDG_CONFIG_HOME")
		if dir == "" && m.env.getenv("HOME") != "" {
			dir = filepath.Join(m.env.getenv("HOME"), ".config")
		}
		if dir != "" {
			path = filepath.Join(dir, "ledger", "client.yaml")
		}
	}
	var cfgDir string
	if path != "" {
		data, rerr := m.env.readFile(path)
		switch {
		case rerr == nil:
			dec := yaml.NewDecoder(strings.NewReader(string(data)))
			dec.KnownFields(true)
			if derr := dec.Decode(&cfg); derr != nil && !errors.Is(derr, io.EOF) {
				return "", "", "", fmt.Errorf("client config %s: %w", path, derr)
			}
			cfgDir = filepath.Dir(path)
			if cfg.Token != "" {
				if fi, serr := m.env.stat(path); serr == nil && fi.Mode().Perm()&0o077 != 0 {
					return "", "", "", fmt.Errorf("client config %s holds a token but is readable by others: chmod 600 it, or use token_file", path)
				}
			}
		case explicit || !errors.Is(rerr, fs.ErrNotExist):
			return "", "", "", fmt.Errorf("client config %s: %w", path, rerr)
		}
	}
	url = firstNonEmpty(m.url, m.env.getenv("LEDGER_URL"), cfg.URL, defaultServerURL)
	output = firstNonEmpty(m.output, cfg.Output, "text")
	if output != "text" && output != "json" {
		return "", "", "", fmt.Errorf("--output must be text or json, not %q", output)
	}
	switch {
	case m.tokenFile != "":
		token, err = m.readToken(m.tokenFile)
	case m.env.getenv("LEDGER_TOKEN") != "":
		token = strings.TrimSpace(m.env.getenv("LEDGER_TOKEN"))
	case cfg.TokenFile != "":
		p := cfg.TokenFile
		if !filepath.IsAbs(p) && cfgDir != "" {
			p = filepath.Join(cfgDir, p)
		}
		token, err = m.readToken(p)
	case cfg.Token != "":
		token = strings.TrimSpace(cfg.Token)
	default:
		err = errors.New("no API token: use --token-file, LEDGER_TOKEN or the client config's token_file")
	}
	return url, token, output, err
}

func (m *mgmt) readToken(path string) (string, error) {
	if fi, err := m.env.stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		say(m.stderr, "ledger: warning: token file "+path+" is readable by other users")
	}
	data, err := m.env.readFile(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}

func (m *mgmt) client() (*ledgerclient.Client, error) {
	url, token, output, err := m.resolve()
	if err != nil {
		return nil, err
	}
	m.output = output
	opts := []ledgerclient.Option{ledgerclient.WithTimeout(m.timeout), ledgerclient.WithUserAgent("ledger-cli/" + version)}
	if m.insecure {
		opts = append(opts, ledgerclient.WithInsecureHTTP())
	}
	if m.env.HTTPClient != nil {
		opts = append(opts, ledgerclient.WithHTTPClient(m.env.HTTPClient))
	}
	return ledgerclient.New(url, token, opts...)
}

// report prints an error without ever including the token (the client never
// puts it in one).
func (m *mgmt) report(err error) {
	var ae *ledgerclient.APIError
	if !errors.As(err, &ae) {
		say(m.stderr, "ledger: "+printable(err.Error()))
		return
	}
	say(m.stderr, "ledger: "+printable(ae.Error()))
	switch {
	case ledgerclient.IsVersionConflict(err) && ae.Reconciliation != nil:
		say(m.stderr, fmt.Sprintf("someone else changed this object: its version is now %d. Read it again (get), check the change, and retry with --version %d.", ae.Reconciliation.CurrentVersion, ae.Reconciliation.CurrentVersion))
	case ae.Code == ledgerclient.CodePrecondition:
		say(m.stderr, "the change must name the version it is based on: pass --version")
	case ledgerclient.IsUnauthenticated(err):
		say(m.stderr, "the token was refused: it may be wrong, revoked or expired")
	case ledgerclient.IsForbidden(err):
		say(m.stderr, "the token lacks the grant for this operation (see: ledger me)")
	}
	if ae.RequestID != "" {
		say(m.stderr, "request id: "+printable(ae.RequestID))
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// emit prints v as JSON, or with text in the default format.
func (m *mgmt) emit(v any, text func(io.Writer)) error {
	if m.output == "json" {
		enc := json.NewEncoder(m.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text(m.stdout)
	return nil
}
