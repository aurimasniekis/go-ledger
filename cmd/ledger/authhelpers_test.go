package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

// testPassword is the password the fixtures set for every user.
const testPassword = "correct horse battery staple"

// testKEK is the key-encryption key of test API stacks (synthetic).
const testKEK = "test-kek-0123456789abcdef0123456789abcdef"

// cheapParams keep Argon2id fast in tests; production uses auth.DefaultParams.
var cheapParams = auth.Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func testAuthConfig() config.Auth {
	return config.Auth{
		Password: config.AuthPassword{Enabled: true, MinLength: 12},
		Session:  config.AuthSession{IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 12 * time.Hour, ReauthWindow: 10 * time.Minute},
		SignIn:   config.AuthSignIn{AccountMaxFailures: 10, AddressMaxFailures: 1000, Window: 15 * time.Minute, DeviceCookieTTL: auth.DefaultDeviceCookieTTL},
	}
}

func mustParts(t *testing.T, db *store.DB, al auditLog) apiParts {
	t.Helper()
	p, err := buildAPI(db, al, testAuthConfig(), testKEK, auth.NewArgon2id(cheapParams, 4), nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testAuthn is a test authenticator whose user principals are trusted as if
// they had passed the CSRF check; only principals without a session (which
// real sign-ins always have) count as recently authenticated.
type testAuthn struct{ httpapi.AuthenticatorFunc }

func (testAuthn) VerifyCSRF(*http.Request, authz.Principal) bool { return true }

func testRecent(svc *auth.Service) func(authz.Principal) bool {
	return func(p authz.Principal) bool { return p.SessionID == "" || svc.Recent(p) }
}
