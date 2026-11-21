package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/tokens"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
)

// A user disabled between the password step and the second step gets no
// session and no sign-in success in the audit log.
func TestSecondStepRefusesAUserDisabledMeanwhile(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("bob")
	en := e.enrol(b)
	e.clock.advance(time.Minute) // the enrolment's step must not be replayed
	challenge := e.passwordStep("bob")
	if _, err := e.parts.users.Update(context.Background(), audit.System, e.ids["bob"], 1, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	res := e.secondStep(challenge, "code", auth.TOTPCode(en.secret, e.clock.now()))
	if res.code != 401 || errCode(res.body) != "unauthenticated" || res.b != nil {
		t.Fatalf("disabled user at the second step: %d %s", res.code, res.body)
	}
	for _, r := range e.auditActions(audit.SignInSucceeded) {
		if r.Actor.UserID == e.ids["bob"] && r.Details["method"] == auth.MethodTOTP {
			t.Errorf("a disabled user's second step was audited as a success: %+v", r)
		}
	}
	// The challenge is gone: enabling the user again does not revive it.
	if _, err := e.parts.users.Update(context.Background(), audit.System, e.ids["bob"], 2, users.Patch{Disabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if res := e.secondStep(challenge, "code", auth.TOTPCode(en.secret, e.clock.now())); res.code != 401 {
		t.Errorf("challenge of a disabled user survived: %d %s", res.code, res.body)
	}
}

// Concurrent requests on one challenge with different valid recovery codes:
// exactly one signs in and only that one code is consumed.
func TestConcurrentRecoveryCodesOnOneChallengeBurnOnlyOne(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("alice")
	en := e.enrol(b)
	challenge := e.passwordStep("alice")
	const n = 6
	var wg sync.WaitGroup
	results := make([]*browserResult, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = e.secondStep(challenge, "recovery_code", en.codes[i])
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, r := range results {
		switch {
		case r.code == 200 && r.b != nil:
			ok++
		case r.code != 401:
			t.Errorf("unexpected %d %s", r.code, r.body)
		}
	}
	if ok != 1 {
		t.Fatalf("%d requests signed in, want exactly 1", ok)
	}
	w := e.req("GET", "/api/v1/auth/two-factor", "", b, nil)
	if !strings.Contains(w.Body.String(), `"recovery_codes_remaining":9`) {
		t.Errorf("a code was consumed without a sign-in: %s", w.Body.String())
	}
}

// The host reset ends every session of the user; the user's own disable keeps
// only the session it was made with.
func TestDisablingTwoFactorEndsSessions(t *testing.T) {
	e := newSignInEnv(t, nil)
	ctx := context.Background()
	b, _ := e.signIn("alice")
	en := e.enrol(b) // ends other sessions, keeps b
	e.clock.advance(time.Minute)
	res := e.secondStep(e.passwordStep("alice"), "code", auth.TOTPCode(en.secret, e.clock.now()))
	if res.code != 200 || res.b == nil {
		t.Fatalf("second session: %d %s", res.code, res.body)
	}
	other := res.b
	if s, _ := e.parts.auth.Sessions(ctx, e.ids["alice"]); len(s) != 2 {
		t.Fatalf("sessions before: %d", len(s))
	}
	if w := e.req("DELETE", "/api/v1/auth/two-factor", "", b, nil); w.Code != 204 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/me", "", other, nil); w.Code != 401 {
		t.Errorf("other session after disable: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 200 {
		t.Errorf("current session after disable: %d", w.Code)
	}
	got := e.auditActions(audit.TwoFactorDisabled)
	if len(got) != 1 || got[0].Details["sessions_ended"] != "1" {
		t.Errorf("audit %+v", got)
	}

	// Host reset: every session ends.
	e.clock.advance(time.Minute)
	b2, _ := e.signIn("alice") // password only again
	en = e.enrol(b2)
	b3 := e.secondStepSession(t, "alice", en)
	if err := e.parts.auth.DisableTwoFactor(ctx, authz.Principal{}, true, e.ids["alice"], auth.ByHost); err != nil {
		t.Fatal(err)
	}
	for name, x := range map[string]*browser{"enrolling": b2, "second": b3} {
		if w := e.req("GET", "/api/v1/me", "", x, nil); w.Code != 401 {
			t.Errorf("%s session after host reset: %d", name, w.Code)
		}
	}
	if s, _ := e.parts.auth.Sessions(ctx, e.ids["alice"]); len(s) != 0 {
		t.Errorf("sessions after host reset: %d", len(s))
	}
	if got := e.auditActions(audit.TwoFactorDisabled); len(got) != 2 || got[1].Details["by"] != "host" || got[1].Details["sessions_ended"] != "2" || got[0].Details["by"] != "self" {
		t.Errorf("audit %+v", got)
	}
}

// secondStepSession opens another session of user with a TOTP code.
func (e *signInEnv) secondStepSession(t *testing.T, user string, en enrolled) *browser {
	t.Helper()
	e.clock.advance(time.Minute)
	res := e.secondStep(e.passwordStep(user), "code", auth.TOTPCode(en.secret, e.clock.now()))
	if res.code != 200 || res.b == nil {
		t.Fatalf("second step: %d %s", res.code, res.body)
	}
	return res.b
}

// With auth.require_2fa on, an API token of a user without TOTP is not
// confined, and the token routes are closed to a confined session, so it
// cannot mint a token to step around the requirement.
func TestRequired2FADoesNotConfineTokensAndBlocksTokenRoutesForConfinedSessions(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.Require2FA = true })
	ctx := context.Background()
	// bob has no TOTP; his token is made by the host path.
	gs, err := authz.NewGrants(authz.Grant{Resource: authz.ResourceCommitments, Action: authz.ActionRead})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := e.parts.tokens.Create(ctx, audit.System, authz.UserPrincipal(e.ids["bob"], authz.RoleReadOnly),
		tokens.New{Name: "reader", Grants: gs})
	if err != nil {
		t.Fatal(err)
	}
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(iss.Secret)); w.Code != 200 {
		t.Fatalf("token of a user without TOTP: %d %s", w.Code, w.Body.String())
	}
	b, _ := e.signIn("bob")
	if w := e.req("GET", "/api/v1/commitments", "", b, nil); w.Code != 403 || errCode(w.Body.String()) != "two_factor_enrollment_required" {
		t.Fatalf("session must be confined: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/auth/tokens", `{"name":"ci","grants":["commitments:read"]}`},
		{"GET", "/api/v1/auth/tokens", ""},
		{"DELETE", "/api/v1/auth/tokens/" + iss.Token.ID, ""},
	} {
		w := e.req(c.method, c.path, c.body, b, nil)
		if w.Code != 403 || errCode(w.Body.String()) != "two_factor_enrollment_required" {
			t.Errorf("%s %s while confined: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	// The refused revoke left the token working.
	if w := e.req("GET", "/api/v1/commitments", "", nil, bearer(iss.Secret)); w.Code != 200 {
		t.Errorf("token after refused revoke: %d", w.Code)
	}
}
