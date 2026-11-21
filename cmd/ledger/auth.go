package main

import (
	"bufio"
	"context"
	"database/sql"
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
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
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
	}
}

// apiParts are the stores and services behind the Management API.
type apiParts struct {
	users *userstore.Store
	auth  *auth.Service
}

// buildAPI builds the API's stores and services on db. Changes record their
// audit entries through al in the same transaction (ADR-0025).
func buildAPI(db *store.DB, al auditLog, c config.Auth, hasher auth.Hasher, now func() time.Time) (apiParts, error) {
	us := userstore.New(db, al, now, nil)
	svc, err := auth.NewService(authConfig(c), authstore.New(db, al), us, hasher, recorder{db: db, w: al}, auth.Options{Now: now})
	if err != nil {
		return apiParts{}, err
	}
	return apiParts{users: us, auth: svc}, nil
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

// runAuth implements "ledger auth set-password".
func runAuth(args []string, stdout, stderr io.Writer, env opsEnv) int {
	if len(args) == 0 || args[0] != "set-password" {
		say(stderr, "usage: ledger auth set-password [flags] <username>   (reads the new password from the first line of standard input)")
		return 2
	}
	fs := flag.NewFlagSet("ledger auth set-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fl := config.RegisterFlags(fs)
	create := fs.String("create", "", "create the user with this role (administrator or read-only) when it does not exist")
	cfg, code := loadDBConfig(fs, fl, args[1:], stderr, env)
	if cfg == nil {
		return code
	}
	if fs.NArg() != 1 {
		say(stderr, "usage: ledger auth set-password [flags] <username>   (flags go before the username; stop ledger serve first)")
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
	parts, err := buildAPI(db, al, cfg.Auth, auth.NewArgon2id(auth.DefaultParams, 1), env.Now)
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
