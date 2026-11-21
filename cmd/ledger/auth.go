package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/automatch"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/outlook"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/store"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
	paymentstore "github.com/aurimasniekis/go-ledger/internal/store/payments"
	tokenstore "github.com/aurimasniekis/go-ledger/internal/store/tokens"
	userstore "github.com/aurimasniekis/go-ledger/internal/store/users"
)

// hashConcurrency is how many Argon2id hashes run at once (each takes
// auth.DefaultParams.MemoryKiB of memory).
const hashConcurrency = 2

// authConfig converts the auth configuration section.
func authConfig(c config.Auth) auth.Config {
	return auth.Config{
		Policy:          auth.PasswordPolicy{MinBytes: c.Password.MinLength},
		IdleTimeout:     c.Session.IdleTimeout,
		AbsoluteTimeout: c.Session.AbsoluteTimeout,
		ReauthWindow:    c.Session.ReauthWindow,
		AccountLimit:    auth.Limit{Max: c.SignIn.AccountMaxFailures, Window: c.SignIn.Window},
		AddressLimit:    auth.Limit{Max: c.SignIn.AddressMaxFailures, Window: c.SignIn.Window},
		DeviceCookieTTL: c.SignIn.DeviceCookieTTL,
		Require2FA:      c.Require2FA,
		// Linking an identity never disables passwords: only this setting does.
		PasswordSignInDisabled: !c.Password.Enabled,
		OIDC: auth.OIDCPolicy{Enabled: c.OIDC.Enabled, Issuer: c.OIDC.Issuer, Provider: c.OIDC.Name, Require2FA: c.OIDC.Require2FA,
			Provisioning: c.OIDC.Provisioning.Enabled, ProvisionRole: authz.Role(c.OIDC.Provisioning.Role)},
	}
}

// apiParts are the stores and services behind the Management API.
type apiParts struct {
	users       *userstore.Store
	commitments *commitmentstore.Store
	payments    *paymentstore.Store
	matches     *matchstore.Store
	// matching is the automatic payment matching (ADR-0039); registerAPI
	// builds one when ledger serve has not.
	matching *automatch.Engine
	db       *store.DB
	auth     *auth.Service
	tokens   *tokens.Service
	// oidc is single sign-on, set by ledger serve when it is configured.
	oidc *httpapi.OIDCDeps
	// mcp serves the MCP endpoint, set by ledger serve when mcp.enabled.
	mcp http.Handler
	// banking is the bank connection service, set by ledger serve.
	banking *bankconnect.Service
	// facts is the bank-facts store (with audit) that ledger serve shares
	// with the connection service and the outlook; nil makes registerAPI
	// open a read-only one. bankStaleAfter is banking.stale_after.
	facts          *bankfacts.Store
	bankStaleAfter time.Duration
	// convertTimeout bounds the outlook's rate conversion on a request,
	// below http.write_timeout (see refreshTimeout); zero is unbounded.
	convertTimeout time.Duration
	// reportingCurrency is reporting.currency: the default currency of the
	// outlook's converted aggregate (ADR-0044); zero for none.
	reportingCurrency moneypkg.Currency
	// rates converts for the outlook (FX2, ADR-0046); nil leaves only amounts
	// already in the reporting currency convertible.
	rates outlook.Converter
	// rateCache serves the rates routes (FX2c, ADR-0047); nil answers them
	// with 503.
	rateCache *httpapi.RatesDeps
}

// buildAPI builds the API's stores and services on db. Changes record their
// audit entries through al in the same transaction (ADR-0025). kek is the
// key-encryption key that seals TOTP secrets (ADR-0030). log receives the
// authentication service's warnings; nil means slog's default logger.
func buildAPI(db *store.DB, al auditLog, c config.Auth, kek string, hasher auth.Hasher, now func() time.Time, log *slog.Logger) (apiParts, error) {
	us := userstore.New(db, al, now, nil)
	sealer, err := auth.NewKEKSealer(kek, auth.TOTPPurpose)
	if err != nil {
		return apiParts{}, err
	}
	svc, err := auth.NewService(authConfig(c), authstore.New(db, al), us, hasher, recorder{db: db, w: al}, auth.Options{Now: now, Sealer: sealer, Users: us, Logger: log})
	if err != nil {
		return apiParts{}, err
	}
	return apiParts{users: us, commitments: commitmentstore.New(db, al, now, nil), payments: paymentstore.New(db, al, now, nil),
		matches: matchstore.New(db, al, now, nil), auth: svc, db: db,
		tokens: tokens.NewService(tokenstore.New(db, al), us, tokens.Options{Now: now})}, nil
}

// recorder records one audit event in a transaction of its own through w,
// so tests that tee the writer see failed sign-ins too.
type recorder struct {
	db *store.DB
	w  store.AuditWriter
}

func (r recorder) Record(ctx context.Context, e audit.Event) error {
	return r.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error { return r.w.WriteAudit(ctx, tx, e) })
}

// setPasswordUsage is the help text of "ledger auth set-password".
const setPasswordUsage = `usage: ledger auth set-password [flags] <username>
  Reads the new password from the first line of standard input. Flags go
  before the username; stop ledger serve first.
  Standard input is read as it is: on a terminal the password is echoed as
  you type it. Pipe it in instead (for example from a password manager), or
  make sure nobody can see the screen and clear the terminal afterwards.`

// reset2FAUsage is the help text of "ledger auth reset-2fa".
const reset2FAUsage = `usage: ledger auth reset-2fa [flags] <username>
  Removes the user's TOTP authenticator and recovery codes, for a lost
  authenticator without recovery codes or a changed key-encryption key.
  The user signs in with the password alone afterwards (and must enrol again
  first when auth.require_2fa is on). Flags go before the username; stop
  ledger serve first. Audited as the host.`

// runAuth implements "ledger auth set-password" and "ledger auth reset-2fa".
func runAuth(args []string, stdout, stderr io.Writer, env opsEnv) int {
	if len(args) > 0 {
		switch args[0] {
		case "reset-2fa":
			return runReset2FA(args[1:], stdout, stderr, env)
		case "recovery-sign-in":
			return runRecoverySignIn(args[1:], stdout, stderr, env)
		case "unlink-oidc":
			return runUnlinkOIDC(args[1:], stdout, stderr, env)
		}
	}
	if len(args) == 0 || args[0] != "set-password" {
		say(stderr, setPasswordUsage)
		say(stderr, reset2FAUsage)
		say(stderr, recoverySignInUsage)
		say(stderr, unlinkOIDCUsage)
		return 2
	}
	fs := flag.NewFlagSet("ledger auth set-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, setPasswordUsage)
		fs.PrintDefaults()
	}
	fl := config.RegisterFlags(fs)
	create := fs.String("create", "", "create the user with this role (administrator or read-only) when it does not exist")
	cfg, code := loadDBConfig(fs, fl, args[1:], stderr, env)
	if cfg == nil {
		return code
	}
	if fs.NArg() != 1 {
		say(stderr, setPasswordUsage)
		return 2
	}
	username := fs.Arg(0)
	if *create != "" && !authz.Role(*create).Valid() {
		say(stderr, "ledger auth: --create must be administrator or read-only")
		return 2
	}
	stdin := env.Stdin
	if stdin == nil {
		say(stderr, "ledger auth: no standard input")
		return 1
	}
	line, err := bufio.NewReaderSize(stdin, auth.MaxPasswordBytes+2).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		say(stderr, "ledger auth: cannot read the password from standard input")
		return 1
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		say(stderr, "ledger auth: no password on standard input")
		return 1
	}
	if err := authConfig(cfg.Auth).Policy.Check(password); err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	if err := ensureDir(cfg.Database.Path); err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	lock, err := store.LockInstance(cfg.Database.Path)
	if err != nil {
		say(stderr, "ledger auth: "+err.Error()+"; stop it first")
		return 1
	}
	defer func() { _ = lock.Close() }()
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.Database.Path, store.Options{BusyTimeout: cfg.Database.BusyTimeout})
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	defer func() { _ = db.Close() }()
	al := store.NewAuditLog(db)
	parts, err := buildAPI(db, al, cfg.Auth, cfg.KEK.Value, auth.NewArgon2id(auth.DefaultParams, 1), env.Now, nil)
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	cred, err := authstore.New(db, al).CredentialByUsername(ctx, username)
	switch {
	case errors.Is(err, resource.ErrNotFound) && *create != "":
		u, err := parts.users.Create(ctx, audit.System, users.New{Username: username, Role: authz.Role(*create)})
		if err != nil {
			say(stderr, "ledger auth: "+err.Error())
			return 1
		}
		cred.UserID = u.ID
		say(stdout, fmt.Sprintf("created user %s (%s)", username, *create))
	case errors.Is(err, resource.ErrNotFound):
		say(stderr, fmt.Sprintf("ledger auth: no user %q; pass --create administrator to create it", username))
		return 1
	case err != nil:
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	n, err := parts.auth.SetPassword(ctx, authz.Principal{}, true, cred.UserID, password, auth.ByHost)
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	say(stdout, fmt.Sprintf("password set for %s; %d session(s) ended", username, n))
	return 0
}

// runReset2FA implements "ledger auth reset-2fa": the host's recovery path
// when a user has lost their authenticator and recovery codes (ADR-0030).
func runReset2FA(args []string, stdout, stderr io.Writer, env opsEnv) int {
	fs := flag.NewFlagSet("ledger auth reset-2fa", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, reset2FAUsage)
		fs.PrintDefaults()
	}
	fl := config.RegisterFlags(fs)
	cfg, code := loadDBConfig(fs, fl, args, stderr, env)
	if cfg == nil {
		return code
	}
	if fs.NArg() != 1 {
		say(stderr, reset2FAUsage)
		return 2
	}
	username := fs.Arg(0)
	if err := ensureDir(cfg.Database.Path); err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	lock, err := store.LockInstance(cfg.Database.Path)
	if err != nil {
		say(stderr, "ledger auth: "+err.Error()+"; stop it first")
		return 1
	}
	defer func() { _ = lock.Close() }()
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.Database.Path, store.Options{BusyTimeout: cfg.Database.BusyTimeout})
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	defer func() { _ = db.Close() }()
	al := store.NewAuditLog(db)
	parts, err := buildAPI(db, al, cfg.Auth, cfg.KEK.Value, auth.NewArgon2id(auth.DefaultParams, 1), env.Now, nil)
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	cred, err := authstore.New(db, al).CredentialByUsername(ctx, strings.ToLower(username))
	if errors.Is(err, resource.ErrNotFound) {
		say(stderr, fmt.Sprintf("ledger auth: no user %q", username))
		return 1
	}
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	err = parts.auth.DisableTwoFactor(ctx, authz.Principal{}, true, cred.UserID, auth.ByHost, auth.CurrentFactor{})
	if errors.Is(err, auth.ErrTwoFactorNotEnabled) {
		say(stdout, fmt.Sprintf("%s has no two-factor authentication; nothing to reset", username))
		return 0
	}
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	say(stdout, fmt.Sprintf("two-factor authentication removed for %s", username))
	return 0
}
