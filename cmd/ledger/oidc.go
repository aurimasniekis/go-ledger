package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/oidc"
	"github.com/aurimasniekis/go-ledger/internal/store"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
)

// oidcDeps builds single sign-on from the configuration (ADR-0033); nil when
// it is off. Nothing is fetched from the provider until the first sign-in, so
// an unreachable provider never stops ledger serve from starting.
func oidcDeps(c config.AuthOIDC, now func() time.Time) (*httpapi.OIDCDeps, error) {
	if !c.Enabled {
		return nil, nil
	}
	cl, err := oidc.New(oidc.Config{Issuer: c.Issuer, ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL,
		Scopes: c.Scopes, ClockSkew: c.ClockSkew, Now: now})
	if err != nil {
		return nil, err
	}
	return &httpapi.OIDCDeps{Client: httpapi.RelyingParty(cl), Transactions: oidc.NewTransactions(now), Name: c.Name,
		AuthorizeLimit: auth.NewLimiter(httpapi.DefaultAuthorizeLimit, auth.DefaultLimiterKeys, now)}, nil
}

// recoverySignInUsage is the help text of "ledger auth recovery-sign-in".
const recoverySignInUsage = `usage: ledger auth recovery-sign-in [flags] <username>
  Break-glass: issues a one-time sign-in link for the user, for when the
  identity provider is unavailable or password sign-in is disabled. It
  replaces any earlier unused link, skips the second factor, works once and
  expires after --ttl (default 15m, at most 1h). Start ledger serve and open
  the printed link in a browser within that time. The link is printed only
  here: treat it like a password. Flags go before the username; stop ledger
  serve first. Audited as the host.`

// unlinkOIDCUsage is the help text of "ledger auth unlink-oidc".
const unlinkOIDCUsage = `usage: ledger auth unlink-oidc [flags] <username>
  Removes the user's single-sign-on link, for example after the identity
  provider account was replaced. The user can link a new identity once signed
  in (with a password, or a recovery sign-in). Flags go before the username;
  stop ledger serve first. Audited as the host.`

// hostCommand opens the database for a host command on one user: it parses
// the flags, takes the instance lock and builds the API services. done must
// be called when code is -1 (success so far).
func hostCommand(name, usage string, args []string, stderr io.Writer, env opsEnv, extra func(*flag.FlagSet)) (parts apiParts, db *store.DB, cfg *config.Config, cred auth.Credential, done func(), code int) {
	fs := flag.NewFlagSet("ledger auth "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		say(stderr, usage)
		fs.PrintDefaults()
	}
	fl := config.RegisterFlags(fs)
	if extra != nil {
		extra(fs)
	}
	cfg, code = loadDBConfig(fs, fl, args, stderr, env)
	if cfg == nil {
		return apiParts{}, nil, nil, auth.Credential{}, nil, code
	}
	if fs.NArg() != 1 {
		say(stderr, usage)
		return apiParts{}, nil, nil, auth.Credential{}, nil, 2
	}
	username := fs.Arg(0)
	if err := ensureDir(cfg.Database.Path); err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return apiParts{}, nil, nil, auth.Credential{}, nil, 1
	}
	lock, err := store.LockInstance(cfg.Database.Path)
	if err != nil {
		say(stderr, "ledger auth: "+err.Error()+"; stop it first")
		return apiParts{}, nil, nil, auth.Credential{}, nil, 1
	}
	ctx := context.Background()
	db, err = store.Open(ctx, cfg.Database.Path, store.Options{BusyTimeout: cfg.Database.BusyTimeout})
	if err != nil {
		_ = lock.Close()
		say(stderr, "ledger auth: "+err.Error())
		return apiParts{}, nil, nil, auth.Credential{}, nil, 1
	}
	done = func() { _ = db.Close(); _ = lock.Close() }
	al := store.NewAuditLog(db)
	parts, err = buildAPI(db, al, cfg.Auth, cfg.KEK.Value, auth.NewArgon2id(auth.DefaultParams, 1), env.Now, nil)
	if err == nil {
		cred, err = authstore.New(db, al).CredentialByUsername(ctx, strings.ToLower(username))
		if errors.Is(err, resource.ErrNotFound) {
			err = fmt.Errorf("no user %q", username)
		}
	}
	if err != nil {
		done()
		say(stderr, "ledger auth: "+err.Error())
		return apiParts{}, nil, nil, auth.Credential{}, nil, 1
	}
	return parts, db, cfg, cred, done, -1
}

// runRecoverySignIn implements "ledger auth recovery-sign-in" (break-glass,
// decision D16).
func runRecoverySignIn(args []string, stdout, stderr io.Writer, env opsEnv) int {
	var ttl *time.Duration
	parts, _, cfg, cred, done, code := hostCommand("recovery-sign-in", recoverySignInUsage, args, stderr, env, func(fs *flag.FlagSet) {
		ttl = fs.Duration("ttl", auth.DefaultRecoveryTTL, "how long the link stays valid (at most 1h)")
	})
	if code >= 0 {
		return code
	}
	defer done()
	tok, exp, err := parts.auth.IssueRecoverySignIn(context.Background(), cred.UserID, *ttl)
	if errors.Is(err, users.ErrDisabled) {
		say(stderr, fmt.Sprintf("ledger auth: %s is disabled; enable the account first", cred.Username))
		return 1
	}
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	link := httpapi.AuthPath + "/recovery-sign-in?token=" + url.QueryEscape(tok)
	if u, err := url.Parse(cfg.Auth.OIDC.RedirectURL); err == nil && u.Host != "" {
		link = u.Scheme + "://" + u.Host + link
	} else {
		link = "<Ledger's address>" + link
	}
	say(stdout, fmt.Sprintf("one-time sign-in for %s, valid until %s:", cred.Username, exp.UTC().Format(time.RFC3339)))
	say(stdout, link)
	return 0
}

// runUnlinkOIDC implements "ledger auth unlink-oidc".
func runUnlinkOIDC(args []string, stdout, stderr io.Writer, env opsEnv) int {
	parts, _, _, cred, done, code := hostCommand("unlink-oidc", unlinkOIDCUsage, args, stderr, env, nil)
	if code >= 0 {
		return code
	}
	defer done()
	err := parts.auth.UnlinkOIDC(context.Background(), authz.Principal{}, true, cred.UserID)
	if errors.Is(err, resource.ErrNotFound) {
		say(stdout, fmt.Sprintf("%s has no single-sign-on link; nothing to remove", cred.Username))
		return 0
	}
	if err != nil {
		say(stderr, "ledger auth: "+err.Error())
		return 1
	}
	say(stdout, fmt.Sprintf("single-sign-on link removed for %s", cred.Username))
	return 0
}
