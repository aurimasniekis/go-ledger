package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	authstore "github.com/aurimasniekis/go-ledger/internal/store/auth"
)

// errField returns the field of an error envelope.
func errField(body string) string {
	var e struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Field
}

// twoFactorState reads a user's stored second factor.
func (e *signInEnv) twoFactorState(user string) auth.TwoFactorState {
	e.t.Helper()
	st, err := authstore.New(e.db, e.al).TwoFactor(context.Background(), e.ids[user])
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

// startEnrolment starts a new enrolment and returns its secret.
func (e *signInEnv) startEnrolment(b *browser) []byte {
	e.t.Helper()
	w := e.req("POST", "/api/v1/auth/two-factor/totp", "", b, nil)
	var en struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &en)
	secret, err := auth.DecodeTOTPSecret(en.Secret)
	if w.Code != 201 || err != nil {
		e.t.Fatalf("start enrolment: %d %s", w.Code, w.Body.String())
	}
	return secret
}

func jsonBody(kv ...string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// TestTwoFactorChangesNeedTheCurrentFactor is decision D26-1a: with TOTP
// enabled, replacing the authenticator, disabling 2FA and regenerating
// recovery codes need a current TOTP or recovery code in the request, on top
// of recent re-authentication. A missing, wrong, reused or doubled factor is
// refused and changes nothing.
func TestTwoFactorChangesNeedTheCurrentFactor(t *testing.T) {
	type op struct {
		name, method, path string
		withNewCode        bool // the confirmation also carries the new authenticator's code
	}
	ops := []op{
		{"replace", "POST", "/api/v1/auth/two-factor/totp/confirm", true},
		{"regenerate", "POST", "/api/v1/auth/two-factor/recovery-codes", false},
		{"disable", "DELETE", "/api/v1/auth/two-factor", false},
	}
	// Each operation gets its own instance: the wrong factors below count
	// towards the sign-in limit (ten per username).
	for _, o := range ops {
		t.Run(o.name, func(t *testing.T) {
			e := newSignInEnv(t, nil)
			b, _ := e.signIn("alice")
			en := e.enrol(b)
			e.clock.advance(auth.TOTPPeriod)
			before := e.twoFactorState("alice")
			used := auth.TOTPCode(en.secret, e.clock.now().Add(-auth.TOTPPeriod)) // the confirmation's code
			wrong := auth.TOTPCode(en.secret, e.clock.now().Add(-time.Hour))
			var extra []string
			if o.withNewCode {
				extra = []string{"code", auth.TOTPCode(e.startEnrolment(b), e.clock.now())}
			}
			refusals := []struct {
				name        string
				body        []string
				code, field string
			}{
				{"no current factor", nil, "validation_failed", "current_code"},
				{"empty current code", []string{"current_code", ""}, "validation_failed", "current_code"},
				{"both a code and a recovery code", []string{"current_code", auth.TOTPCode(en.secret, e.clock.now()), "current_recovery_code", en.codes[0]}, "validation_failed", "current_code"},
				{"wrong code", []string{"current_code", wrong}, "invalid_second_factor", "current_code"},
				{"reused code", []string{"current_code", used}, "invalid_second_factor", "current_code"},
				{"malformed code", []string{"current_code", "12ab"}, "invalid_second_factor", "current_code"},
				{"wrong recovery code", []string{"current_recovery_code", "AAAA-BBBB-CCCC-DDDD"}, "invalid_second_factor", "current_recovery_code"},
			}
			failedAudits := len(e.auditActions(audit.ReauthenticationFailed))
			wrongs := 0
			for _, r := range refusals {
				w := e.req(o.method, o.path, jsonBody(append(append([]string{}, extra...), r.body...)...), b, nil)
				if w.Code != 400 || errCode(w.Body.String()) != r.code || errField(w.Body.String()) != r.field {
					t.Errorf("%s: %d %s", r.name, w.Code, w.Body.String())
				}
				if r.code == "invalid_second_factor" {
					wrongs++
				}
			}
			if st := e.twoFactorState("alice"); st.Secret != before.Secret || st.RecoveryCodes != auth.RecoveryCodeCount {
				t.Fatalf("a refused change changed the second factor: %+v", st)
			}
			for a, want := range map[audit.Action]int{audit.TwoFactorEnabled: 1, audit.TwoFactorDisabled: 0, audit.RecoveryCodesReset: 0} {
				if n := len(e.auditActions(a)); n != want {
					t.Errorf("%s audited %d times after refusals", a, n)
				}
			}
			failed := e.auditActions(audit.ReauthenticationFailed)[failedAudits:]
			if len(failed) != wrongs {
				t.Fatalf("%d wrong factors, %d auth.reauthentication_failed entries", wrongs, len(failed))
			}
			for _, f := range failed {
				if f.Details["reason"] != "invalid_second_factor" || (f.Details["method"] != "totp" && f.Details["method"] != "recovery_code") {
					t.Errorf("failure entry %+v", f.Details)
				}
			}
			// The right current code passes.
			w := e.req(o.method, o.path, jsonBody(append(append([]string{}, extra...), "current_code", auth.TOTPCode(en.secret, e.clock.now()))...), b, nil)
			if w.Code != 200 && w.Code != 204 {
				t.Fatalf("with the current code: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestTwoFactorChangeDetails: the order of checks, recovery-code use,
// replacement and recent re-authentication alongside the current factor.
func TestTwoFactorChangeDetails(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("alice")
	en := e.enrol(b)
	e.clock.advance(auth.TOTPPeriod)
	wrong := auth.TOTPCode(en.secret, e.clock.now().Add(-time.Hour))
	newSecret := e.startEnrolment(b)
	newCode := auth.TOTPCode(newSecret, e.clock.now())

	// A wrong new code is reported on "code" and consumes no recovery code.
	w := e.req("POST", "/api/v1/auth/two-factor/totp/confirm", jsonBody("code", wrong, "current_recovery_code", en.codes[1]), b, nil)
	if w.Code != 400 || errCode(w.Body.String()) != "invalid_second_factor" || errField(w.Body.String()) != "code" {
		t.Fatalf("wrong new code: %d %s", w.Code, w.Body.String())
	}
	if n := e.twoFactorState("alice").RecoveryCodes; n != auth.RecoveryCodeCount {
		t.Fatalf("a wrong new code consumed a recovery code: %d left", n)
	}

	// Regenerating with a recovery code consumes it and replaces them all.
	w = e.req("POST", "/api/v1/auth/two-factor/recovery-codes", jsonBody("current_recovery_code", strings.ToLower(en.codes[1])), b, nil)
	if w.Code != 200 {
		t.Fatalf("regenerate with a recovery code: %d %s", w.Code, w.Body.String())
	}
	var rc struct {
		Codes []string `json:"recovery_codes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rc)
	if len(rc.Codes) != auth.RecoveryCodeCount {
		t.Fatalf("regenerated codes: %s", w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/two-factor/recovery-codes", jsonBody("current_recovery_code", en.codes[2]), b, nil); w.Code != 400 {
		t.Fatalf("an old recovery code after regeneration: %d %s", w.Code, w.Body.String())
	}

	// Replacing with the current authenticator's code works; that code is
	// then used up.
	cur := auth.TOTPCode(en.secret, e.clock.now())
	w = e.req("POST", "/api/v1/auth/two-factor/totp/confirm", jsonBody("code", newCode, "current_code", cur), b, nil)
	if w.Code != 200 {
		t.Fatalf("replace with the current code: %d %s", w.Code, w.Body.String())
	}
	enabled := e.auditActions(audit.TwoFactorEnabled)
	if last := enabled[len(enabled)-1]; last.Details["replaced"] != "true" || last.Details["verified_with"] != "totp" {
		t.Fatalf("replacement audit %+v", last.Details)
	}
	if first := enabled[0]; first.Details["verified_with"] != "" {
		t.Fatalf("the first enrolment needs no current factor: %+v", first.Details)
	}
	// The old authenticator no longer proves anything; the new one does.
	e.clock.advance(auth.TOTPPeriod)
	if w := e.req("DELETE", "/api/v1/auth/two-factor", jsonBody("current_code", auth.TOTPCode(en.secret, e.clock.now())), b, nil); w.Code != 400 {
		t.Fatalf("disable with the replaced authenticator: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("DELETE", "/api/v1/auth/two-factor", jsonBody("current_code", auth.TOTPCode(newSecret, e.clock.now())), b, nil); w.Code != 204 {
		t.Fatalf("disable with the new authenticator: %d %s", w.Code, w.Body.String())
	}
	// Recent re-authentication is still needed as well.
	en = e.enrol(b)
	e.clock.advance(11 * time.Minute)
	if w := e.req("DELETE", "/api/v1/auth/two-factor", jsonBody("current_code", auth.TOTPCode(en.secret, e.clock.now())), b, nil); w.Code != 403 || errCode(w.Body.String()) != "reauthentication_required" {
		t.Fatalf("a current code without recent re-authentication: %d %s", w.Code, w.Body.String())
	}
}

// TestCurrentFactorFailuresAreRateLimited: wrong current factors count in
// the same per-username limit as sign-in, so a stolen session cannot guess
// the code.
func TestCurrentFactorFailuresAreRateLimited(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("alice")
	en := e.enrol(b)
	e.clock.advance(auth.TOTPPeriod)
	wrong := auth.TOTPCode(en.secret, e.clock.now().Add(-time.Hour))
	for i := range 10 {
		if w := e.req("POST", "/api/v1/auth/two-factor/recovery-codes", jsonBody("current_code", wrong), b, nil); w.Code != 400 {
			t.Fatalf("wrong code %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := e.req("POST", "/api/v1/auth/two-factor/recovery-codes", jsonBody("current_code", auth.TOTPCode(en.secret, e.clock.now())), b, nil)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("after ten wrong codes: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil); w.Code != 429 {
		t.Fatalf("sign-in after ten wrong current codes: %d", w.Code)
	}
}

// TestOIDCSessionsNeedTheCurrentFactorToo: an OIDC session (trusted by
// default, so it skipped Ledger's second step) and OIDC re-authentication
// still need the current TOTP or recovery code for a 2FA change.
func TestOIDCSessionsNeedTheCurrentFactorToo(t *testing.T) {
	e := newOIDCEnv(t, nil)
	if err := authstore.New(e.db, e.al).CreateOIDCLink(context.Background(), auth.OIDCLink{ID: "01JD3Y4C8V9M2Q7W5R6T0XZBN4", UserID: e.ids["alice"],
		Issuer: e.op.Issuer, Subject: "subject-0001", CreatedAt: e.clock.now()}); err != nil {
		t.Fatal(err)
	}
	pw, _ := e.signIn("alice")
	en := e.enrol(pw)
	e.clock.advance(auth.TOTPPeriod)
	// With TOTP enabled, OIDC sign-in still asks for the second factor
	// unless Ledger trusts the provider; here it does (the default).
	w := e.signInOIDC()
	b := e.sessionFrom(w)
	if r := e.req("GET", "/api/v1/auth/session", "", b, nil); !strings.Contains(r.Body.String(), `"method":"oidc"`) {
		t.Fatalf("not an OIDC session: %s", r.Body.String())
	}
	if w := e.req("DELETE", "/api/v1/auth/two-factor", "", b, nil); w.Code != 400 || errField(w.Body.String()) != "current_code" {
		t.Fatalf("OIDC session without a current code: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/two-factor/recovery-codes", jsonBody("current_code", "000000"), b, nil); w.Code != 400 || errCode(w.Body.String()) != "invalid_second_factor" {
		t.Fatalf("OIDC session with a wrong code: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("DELETE", "/api/v1/auth/two-factor", jsonBody("current_code", auth.TOTPCode(en.secret, e.clock.now())), b, nil); w.Code != 204 {
		t.Fatalf("OIDC session with the current code: %d %s", w.Code, w.Body.String())
	}
	if d := e.auditActions(audit.TwoFactorDisabled); len(d) != 1 || d[0].Details["verified_with"] != "totp" {
		t.Fatalf("audit %+v", d)
	}
}
