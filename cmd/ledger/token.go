package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
)

// tokenCreateUsage is the help text of "ledger token create".
const tokenCreateUsage = `usage: ledger token create [flags] <username>
  Creates an API token owned by <username> and prints it once on standard
  output; nothing else is printed there, so it can be piped into a secret
  manager. Give --grant (repeatable, or comma-separated) or --root. The token
  never holds more than the owner's role allows. Flags go before the
  username; stop ledger serve first.
  On a fresh database, --create administrator also creates the owner (who
  then has no password until "ledger auth set-password").`

// grantList collects --grant values.
type grantList []string

func (g *grantList) String() string { return strings.Join(*g, ",") }

func (g *grantList) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*g = append(*g, s)
		}
	}
	return nil
}

// parseExpiry reads --expires: an RFC 3339 time or a duration from now.
func parseExpiry(v string, now time.Time) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		t := now.Add(d)
		return &t, nil
	}
	return nil, errors.New("--expires must be an RFC 3339 time (2027-01-01T00:00:00Z) or a positive duration (2160h)")
}

// runToken implements "ledger token create": the bootstrap path for the first
// token, working on the database without the web UI or a running server.
func runToken(args []string, stdout, stderr io.Writer, env opsEnv) int {
	if len(args) == 0 || args[0] != "create" {
		say(stderr, tokenCreateUsage)
		return 2
	}
	fs := flag.NewFlagSet("ledger token create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, tokenCreateUsage)
		fs.PrintDefaults()
	}
	fl := config.RegisterFlags(fs)
	name := fs.String("name", "bootstrap", "the token's name, shown in token lists")
	var grants grantList
	fs.Var(&grants, "grant", "a grant the token holds, resource:action (repeatable or comma-separated); see GET /api/v1/grants")
	root := fs.Bool("root", false, "create a root token: every grant the owner's role holds, now and in future versions")
	expires := fs.String("expires", "", "when the token expires: an RFC 3339 time or a duration such as 2160h (default: never)")
	create := fs.String("create", "", "create the owner with this role (administrator or read-only) when it does not exist")
	cfg, code := loadDBConfig(fs, fl, args[1:], stderr, env)
	if cfg == nil {
		return code
	}
	if fs.NArg() != 1 {
		say(stderr, tokenCreateUsage)
		return 2
	}
	username := fs.Arg(0)
	if *create != "" && !authz.Role(*create).Valid() {
		say(stderr, "ledger token: --create must be administrator or read-only")
		return 2
	}
	var gs authz.Grants
	for _, v := range grants {
		g, err := authz.ParseGrant(v)
		if err != nil {
			say(stderr, fmt.Sprintf("ledger token: unknown grant %q", v))
			return 2
		}
		gs = append(gs, g)
	}
	expiry, err := parseExpiry(*expires, env.now())
	if err != nil {
		say(stderr, "ledger token: "+err.Error())
		return 2
	}
	if expiry != nil {
		if problem := tokens.ExpiryProblem(*expiry, env.now()); problem != "" {
			say(stderr, "ledger token: --expires "+problem)
			return 2
		}
	}
	if err := tokens.ValidateName(*name); err != nil {
		say(stderr, "ledger token: --"+err.Error())
		return 2
	}
	if *root == (len(gs) > 0) {
		say(stderr, "ledger token: give --grant (one or more) or --root, not both")
		return 2
	}
	if err := ensureDir(cfg.Database.Path); err != nil {
		say(stderr, "ledger token: "+err.Error())
		return 1
	}
	lock, err := store.LockInstance(cfg.Database.Path)
	if err != nil {
		say(stderr, "ledger token: "+err.Error()+"; stop it first")
		return 1
	}
	defer func() { _ = lock.Close() }()
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.Database.Path, store.Options{BusyTimeout: cfg.Database.BusyTimeout})
	if err != nil {
		say(stderr, "ledger token: "+err.Error())
		return 1
	}
	defer func() { _ = db.Close() }()
	al := store.NewAuditLog(db)
	parts, err := buildAPI(db, al, cfg.Auth, cfg.KEK.Value, auth.NewArgon2id(auth.DefaultParams, 1), env.Now, nil)
	if err != nil {
		say(stderr, "ledger token: "+err.Error())
		return 1
	}
	cred, err := authstore.New(db, al).CredentialByUsername(ctx, username)
	switch {
	case errors.Is(err, resource.ErrNotFound) && *create != "":
		// Check the grants against the new role first, so a refused token
		// does not leave a new user behind.
		if limit := authz.Role(*create).Grants(); *root && !limit.Covers(authz.AllGrants()) || !limit.Covers(gs) {
			say(stderr, "ledger token: "+tokens.ErrEscalation.Error()+": the "+*create+" role does not hold them")
			return 1
		}
		u, err := parts.users.Create(ctx, audit.System, users.New{Username: username, Role: authz.Role(*create)})
		if err != nil {
			say(stderr, "ledger token: "+err.Error())
			return 1
		}
		cred.UserID = u.ID
		say(stderr, fmt.Sprintf("created user %s (%s); it has no password until ledger auth set-password", username, *create))
	case errors.Is(err, resource.ErrNotFound):
		say(stderr, fmt.Sprintf("ledger token: no user %q; pass --create administrator to create it", username))
		return 1
	case err != nil:
		say(stderr, "ledger token: "+err.Error())
		return 1
	}
	// The host acts for the owner: the token is bounded by the owner's own
	// role, exactly as if they had created it themselves.
	owner, err := parts.users.Principal(ctx, cred.UserID)
	if errors.Is(err, users.ErrDisabled) {
		say(stderr, fmt.Sprintf("ledger token: user %q is disabled", username))
		return 1
	}
	if err != nil {
		say(stderr, "ledger token: "+err.Error())
		return 1
	}
	iss, err := parts.tokens.Create(ctx, audit.System, owner, tokens.New{Name: *name, Root: *root, Grants: gs, ExpiresAt: expiry})
	if err != nil {
		var fe *tokens.FieldError
		if errors.As(err, &fe) && fe.Field == "grants" {
			say(stderr, "ledger token: "+fe.Message+" (--grant or --root)")
			return 2
		}
		say(stderr, "ledger token: "+err.Error())
		return 1
	}
	expiryText := "never expires"
	if iss.Token.ExpiresAt != nil {
		expiryText = "expires " + iss.Token.ExpiresAt.Format(time.RFC3339)
	}
	what := "grants " + strings.Join(iss.Token.Grants.Strings(), ", ")
	if iss.Token.Root {
		what = "root"
	}
	say(stderr, fmt.Sprintf("created API token %s (%s) for %s: %s; %s", iss.Token.ID, iss.Token.Hint(), username, what, expiryText))
	if iss.Token.Root {
		say(stderr, "WARNING: "+tokens.RootWarning)
	}
	say(stderr, "The token is printed once, below; it cannot be shown again. Send it as: Authorization: Bearer <token>")
	say(stdout, iss.Secret)
	return 0
}
