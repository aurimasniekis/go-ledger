package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

type enrolled struct {
	secret []byte
	codes  []string
}

// enrol enables TOTP for the signed-in browser b and returns the secret and
// the recovery codes.
func (e *signInEnv) enrol(b *browser) enrolled {
	e.t.Helper()
	w := e.req("POST", "/api/v1/auth/two-factor/totp", "", b, nil)
	if w.Code != 201 {
		e.t.Fatalf("start enrolment: %d %s", w.Code, w.Body.String())
	}
	var en struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &en)
	secret, err := auth.DecodeTOTPSecret(en.Secret)
	if err != nil || !strings.HasPrefix(en.URI, "otpauth://totp/Ledger:") || !strings.Contains(en.URI, "secret="+en.Secret) {
		e.t.Fatalf("enrolment %s: %v", w.Body.String(), err)
	}
	w = e.req("POST", "/api/v1/auth/two-factor/totp/confirm", fmt.Sprintf(`{"code":%q}`, auth.TOTPCode(secret, e.clock.now())), b, nil)
	if w.Code != 200 {
		e.t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var rc struct {
		Codes []string `json:"recovery_codes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rc)
	if len(rc.Codes) != auth.RecoveryCodeCount {
		e.t.Fatalf("recovery codes: %s", w.Body.String())
	}
	return enrolled{secret: secret, codes: rc.Codes}
}

// passwordStep signs in with the password and expects the 202 challenge.
func (e *signInEnv) passwordStep(user string) string {
	e.t.Helper()
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody(user, testPassword), nil, nil)
	if w.Code != 202 {
		e.t.Fatalf("password step %s: %d %s", user, w.Code, w.Body.String())
	}
	if w.Header().Get("Set-Cookie") != "" {
		e.t.Fatal("the password step set a cookie")
	}
	var ch struct {
		Required  bool     `json:"second_factor_required"`
		Challenge string   `json:"challenge"`
		Methods   []string `json:"methods"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ch)
	if !ch.Required || ch.Challenge == "" || strings.Join(ch.Methods, ",") != "totp,recovery_code" {
		e.t.Fatalf("challenge %s", w.Body.String())
	}
	return ch.Challenge
}

func (e *signInEnv) secondStep(challenge, field, code string) *browserResult {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"challenge": challenge, field: code})
	w := e.req("POST", "/api/v1/auth/sign-in/second-factor", string(body), nil, nil)
	res := &browserResult{code: w.Code, body: w.Body.String()}
	for _, c := range w.Result().Cookies() {
		if c.Name == httpapi.SessionCookie && c.Value != "" {
			var cs currentSession
			_ = json.Unmarshal(w.Body.Bytes(), &cs)
			res.b = &browser{cookie: c.Value, csrf: cs.CSRFToken}
		}
	}
	return res
}

type browserResult struct {
	code int
	body string
	b    *browser
}

func errCode(body string) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Code
}

func TestTOTPEnrolmentAndTwoStepSignIn(t *testing.T) {
	e := newSignInEnv(t, nil)
	other, _ := e.signIn("alice")
	b, _ := e.signIn("alice")

	// A wrong code does not enable anything.
	if w := e.req("POST", "/api/v1/auth/two-factor/totp/confirm", `{"code":"123456"}`, b, nil); w.Code != 409 {
		t.Fatalf("confirm without enrolment: %d %s", w.Code, w.Body.String())
	}
	w := e.req("POST", "/api/v1/auth/two-factor/totp", "", b, nil)
	var en struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &en)
	secret, _ := auth.DecodeTOTPSecret(en.Secret)
	wrong := auth.TOTPCode(secret, e.clock.now().Add(-5*time.Minute))
	if w := e.req("POST", "/api/v1/auth/two-factor/totp/confirm", fmt.Sprintf(`{"code":%q}`, wrong), b, nil); w.Code != 400 || errCode(w.Body.String()) != "invalid_second_factor" {
		t.Fatalf("wrong confirm: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("GET", "/api/v1/auth/two-factor", "", b, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) ||
		strings.Contains(w.Body.String(), `"pending_enrollment_until":null`) {
		t.Fatalf("status while pending: %s", w.Body.String())
	}
	// An enrolment expires.
	e.clock.advance(auth.EnrollmentTTL)
	if w := e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`"}`, b, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/two-factor/totp/confirm", fmt.Sprintf(`{"code":%q}`, auth.TOTPCode(secret, e.clock.now())), b, nil); w.Code != 409 {
		t.Fatalf("expired enrolment: %d %s", w.Code, w.Body.String())
	}

	en2 := e.enrol(b)
	// Enabling TOTP ended alice's other session, kept this one.
	if w := e.req("GET", "/api/v1/me", "", other, nil); w.Code != 401 {
		t.Errorf("other session after enabling: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 200 {
		t.Errorf("current session after enabling: %d", w.Code)
	}
	if w := e.req("GET", "/api/v1/auth/two-factor", "", b, nil); !strings.Contains(w.Body.String(), `"enabled":true`) ||
		!strings.Contains(w.Body.String(), `"recovery_codes_remaining":10`) || !strings.Contains(w.Body.String(), `"method":"totp"`) {
		t.Errorf("status: %s", w.Body.String())
	}
	ev := e.auditActions(audit.TwoFactorEnabled)
	if len(ev) != 1 || ev[0].Actor.UserID != e.ids["alice"] || ev[0].Details["replaced"] != "false" || ev[0].Details["sessions_ended"] != "1" {
		t.Errorf("audit %+v", ev)
	}

	// The secret is stored sealed, the recovery codes only as hashes.
	if err := e.db.ReadTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		var sealed string
		if err := tx.QueryRowContext(ctx, "SELECT secret FROM user_totp WHERE user_id = ?", e.ids["alice"]).Scan(&sealed); err != nil {
			return err
		}
		if !strings.HasPrefix(sealed, "v1.") || strings.Contains(sealed, auth.EncodeTOTPSecret(en2.secret)) {
			t.Errorf("stored secret %q", sealed)
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM user_recovery_codes WHERE code_hash = ? OR code_hash = ?",
			en2.codes[0], strings.ReplaceAll(en2.codes[0], "-", "")).Scan(&n); err != nil || n != 0 {
			t.Errorf("a recovery code is stored in clear: %d %v", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Sign-in is now two steps. The confirmation's code cannot be replayed.
	ch := e.passwordStep("alice")
	if r := e.secondStep(ch, "code", auth.TOTPCode(en2.secret, e.clock.now())); r.code != 401 || errCode(r.body) != "invalid_second_factor" || r.b != nil {
		t.Fatalf("replayed code: %d %s", r.code, r.body)
	}
	e.clock.advance(auth.TOTPPeriod)
	code := auth.TOTPCode(en2.secret, e.clock.now())
	r := e.secondStep(ch, "code", code)
	if r.code != 200 || r.b == nil {
		t.Fatalf("second step: %d %s", r.code, r.body)
	}
	if w := e.req("GET", "/api/v1/me", "", r.b, nil); w.Code != 200 {
		t.Fatalf("new session: %d", w.Code)
	}
	// The challenge is used up; the code cannot be used in a new sign-in.
	if r := e.secondStep(ch, "code", code); r.code != 401 || errCode(r.body) != "unauthenticated" {
		t.Errorf("used challenge: %d %s", r.code, r.body)
	}
	if r := e.secondStep(e.passwordStep("alice"), "code", code); r.code != 401 || errCode(r.body) != "invalid_second_factor" {
		t.Errorf("code reused in another sign-in: %d %s", r.code, r.body)
	}
	// One step late is inside the window, but that step was used above, so
	// its code is refused.
	e.clock.advance(auth.TOTPPeriod)
	if r := e.secondStep(e.passwordStep("alice"), "code", auth.TOTPCode(en2.secret, e.clock.now().Add(-auth.TOTPPeriod))); r.code != 401 {
		t.Errorf("previous, used step: %d", r.code)
	}
	e.clock.advance(auth.TOTPPeriod)
	if r := e.secondStep(e.passwordStep("alice"), "code", auth.TOTPCode(en2.secret, e.clock.now().Add(auth.TOTPPeriod))); r.code != 200 {
		t.Errorf("next step: %d %s", r.code, r.body)
	}
	e.clock.advance(3 * auth.TOTPPeriod)
	if r := e.secondStep(e.passwordStep("alice"), "code", auth.TOTPCode(en2.secret, e.clock.now().Add(-2*auth.TOTPPeriod))); r.code != 401 {
		t.Errorf("two steps old: %d", r.code)
	}
	// A challenge expires.
	ch = e.passwordStep("alice")
	e.clock.advance(auth.ChallengeTTL)
	if r := e.secondStep(ch, "code", auth.TOTPCode(en2.secret, e.clock.now())); r.code != 401 || errCode(r.body) != "unauthenticated" {
		t.Errorf("expired challenge: %d %s", r.code, r.body)
	}
	// Exactly one of code and recovery_code.
	body := fmt.Sprintf(`{"challenge":%q,"code":"123456","recovery_code":"AAAA-AAAA-AAAA-AAAA"}`, e.passwordStep("alice"))
	if w := e.req("POST", "/api/v1/auth/sign-in/second-factor", body, nil, nil); w.Code != 400 {
		t.Errorf("both codes: %d", w.Code)
	}
	// Audit: TOTP successes and failures with their method.
	var ok, failed int
	for _, x := range e.auditActions(audit.SignInSucceeded) {
		if x.Details["method"] == "totp" {
			ok++
		}
	}
	for _, x := range e.auditActions(audit.SignInFailed) {
		if x.Details["method"] == "totp" && x.Details["reason"] == "invalid_second_factor" && x.Actor.UserID == e.ids["alice"] {
			failed++
		}
	}
	if ok != 2 || failed != 4 {
		t.Errorf("audit: %d totp successes, %d failures", ok, failed)
	}
	// Secrets never reach the log.
	for _, s := range append([]string{auth.EncodeTOTPSecret(en2.secret), en.Secret}, en2.codes...) {
		if strings.Contains(e.log.String(), s) {
			t.Fatal("a secret or recovery code is in the log")
		}
	}
}

func TestRecoveryCodesWorkOnceAndCanBeReplaced(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("alice")
	en := e.enrol(b)
	r := e.secondStep(e.passwordStep("alice"), "recovery_code", strings.ToLower(en.codes[3]))
	if r.code != 200 || r.b == nil {
		t.Fatalf("recovery code: %d %s", r.code, r.body)
	}
	if r := e.secondStep(e.passwordStep("alice"), "recovery_code", en.codes[3]); r.code != 401 || errCode(r.body) != "invalid_second_factor" {
		t.Fatalf("used recovery code: %d %s", r.code, r.body)
	}
	var found bool
	for _, x := range e.auditActions(audit.SignInSucceeded) {
		found = found || (x.Details["method"] == "recovery_code" && x.Details["recovery_codes_remaining"] == "9")
	}
	if !found {
		t.Error("recovery-code sign-in not audited with the remaining count")
	}
	// Regenerating needs recent authentication and replaces every code.
	e.clock.advance(11 * time.Minute)
	if w := e.req("POST", "/api/v1/auth/two-factor/recovery-codes", "", r.b, nil); w.Code != 403 || errCode(w.Body.String()) != "reauthentication_required" {
		t.Fatalf("without recent auth: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`"}`, r.b, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := e.req("POST", "/api/v1/auth/two-factor/recovery-codes", "", r.b, nil)
	var rc struct {
		Codes []string `json:"recovery_codes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &rc)
	if w.Code != 200 || len(rc.Codes) != 10 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("regenerate: %d %s", w.Code, w.Body.String())
	}
	if r := e.secondStep(e.passwordStep("alice"), "recovery_code", en.codes[0]); r.code != 401 {
		t.Errorf("old code after regeneration: %d", r.code)
	}
	if r := e.secondStep(e.passwordStep("alice"), "recovery_code", rc.Codes[0]); r.code != 200 {
		t.Errorf("new code: %d %s", r.code, r.body)
	}
	if got := e.auditActions(audit.RecoveryCodesReset); len(got) != 1 || got[0].Details["count"] != "10" {
		t.Errorf("audit %+v", got)
	}
}

// Wrong second factors count towards the per-username limit, and a right
// password does not clear it: only a complete sign-in does.
func TestSecondFactorFailuresAreRateLimited(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("alice")
	en := e.enrol(b)
	bad := auth.TOTPCode(en.secret, e.clock.now().Add(-time.Hour))
	ch := e.passwordStep("alice")
	for i := range auth.ChallengeMaxFailures {
		if r := e.secondStep(ch, "code", bad); r.code != 401 || errCode(r.body) != "invalid_second_factor" {
			t.Fatalf("wrong code %d: %d %s", i, r.code, r.body)
		}
	}
	// The challenge is used up after ChallengeMaxFailures wrong codes.
	e.clock.advance(auth.TOTPPeriod)
	if r := e.secondStep(ch, "code", auth.TOTPCode(en.secret, e.clock.now())); r.code != 401 || errCode(r.body) != "unauthenticated" {
		t.Fatalf("used-up challenge: %d %s", r.code, r.body)
	}
	// The right password again: the failures stay counted.
	ch = e.passwordStep("alice")
	for range 10 - auth.ChallengeMaxFailures {
		if r := e.secondStep(ch, "code", bad); r.code != 401 {
			t.Fatalf("wrong code: %d", r.code)
		}
	}
	// Ten failures: the username is blocked, for the password step too.
	w := e.req("POST", "/api/v1/auth/sign-in", signInBody("alice", testPassword), nil, nil)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("after ten wrong codes: %d %s", w.Code, w.Body.String())
	}
	locked := 0
	for _, x := range e.auditActions(audit.SignInFailed) {
		if x.Details["reason"] == "locked_out" {
			locked++
		}
	}
	if locked != 1 {
		t.Errorf("locked_out audited %d times", locked)
	}
	// After the window, a complete sign-in works and clears the count.
	e.clock.advance(15 * time.Minute)
	ch = e.passwordStep("alice")
	if r := e.secondStep(ch, "code", auth.TOTPCode(en.secret, e.clock.now())); r.code != 200 {
		t.Fatalf("after the window: %d %s", r.code, r.body)
	}
}

func TestRequired2FAConfinesSessionsToEnrolment(t *testing.T) {
	e := newSignInEnv(t, func(c *config.Auth) { c.Require2FA = true })
	b, _ := e.signIn("bob")
	w := e.req("GET", "/api/v1/auth/session", "", b, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"two_factor":{"enabled":false,"required":true,"enrollment_required":true}`) {
		t.Fatalf("session: %d %s", w.Code, w.Body.String())
	}
	// Every protected route is refused with a structured error until
	// enrolment, reads included.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/me", ""},
		{"GET", "/api/v1/commitments", ""},
		{"GET", "/api/v1/occurrences?month=2026-11", ""},
		{"GET", "/api/v1/users", ""},
		{"GET", "/api/v1/auth/sessions", ""},
		{"PUT", "/api/v1/auth/password", `{"password":"another long password"}`},
		{"DELETE", "/api/v1/auth/two-factor", ""},
		{"POST", "/api/v1/auth/two-factor/recovery-codes", ""},
	} {
		w := e.req(c.method, c.path, c.body, b, nil)
		if w.Code != 403 || errCode(w.Body.String()) != "two_factor_enrollment_required" {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	// Enrolment is reachable, and lifts the confinement at once.
	if w := e.req("GET", "/api/v1/auth/two-factor", "", b, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"required":true`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	e.enrol(b)
	if w := e.req("GET", "/api/v1/me", "", b, nil); w.Code != 200 {
		t.Fatalf("after enrolment: %d %s", w.Code, w.Body.String())
	}
	// It cannot be disabled while required.
	if w := e.req("DELETE", "/api/v1/auth/two-factor", "", b, nil); w.Code != 409 {
		t.Errorf("disable while required: %d %s", w.Code, w.Body.String())
	}
	// Sign-out is reachable while confined.
	b2, _ := e.signIn("alice")
	if w := e.req("GET", "/api/v1/me", "", b2, nil); w.Code != 403 {
		t.Fatalf("alice confined: %d", w.Code)
	}
	if w := e.req("POST", "/api/v1/auth/sign-out", "", b2, nil); w.Code != 204 {
		t.Errorf("sign-out while confined: %d %s", w.Code, w.Body.String())
	}
}

func TestDisableTwoFactor(t *testing.T) {
	e := newSignInEnv(t, nil)
	b, _ := e.signIn("alice")
	e.enrol(b)
	if w := e.req("DELETE", "/api/v1/auth/two-factor", "", b, nil); w.Code != 204 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if w := e.req("DELETE", "/api/v1/auth/two-factor", "", b, nil); w.Code != 409 {
		t.Errorf("disable again: %d", w.Code)
	}
	e.signIn("alice") // password only again
	got := e.auditActions(audit.TwoFactorDisabled)
	if len(got) != 1 || got[0].Actor.UserID != e.ids["alice"] || got[0].Details["by"] != "self" {
		t.Errorf("audit %+v", got)
	}
}

// A changed key-encryption key makes the authenticator unusable (500, never
// a silent pass), recovery codes still work, and the host command removes the
// second factor.
func TestKEKChangeAndHostReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	ctx := context.Background()
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	al := store.NewAuditLog(db)
	clk := &clock{t: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
	parts, err := buildAPI(db, al, testAuthConfig(), testKEK, auth.NewArgon2id(cheapParams, 4), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	u, err := parts.users.Create(ctx, audit.System, users.New{Username: "carol", Role: authz.RoleAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parts.auth.SetPassword(ctx, authz.Principal{}, true, u.ID, testPassword, auth.ByHost); err != nil {
		t.Fatal(err)
	}
	p := authz.UserPrincipal(u.ID, authz.RoleAdministrator)
	en, err := parts.auth.BeginTOTP(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := auth.DecodeTOTPSecret(en.Secret)
	codes, _, err := parts.auth.ConfirmTOTP(ctx, p, auth.TOTPCode(secret, clk.now()))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db, err = store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := buildAPI(db, store.NewAuditLog(db), testAuthConfig(), "a different key-encryption key", auth.NewArgon2id(cheapParams, 4), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	signIn := func(field, code string) error {
		iss, err := other.auth.SignIn(ctx, auth.SignInInput{Username: "carol", Password: testPassword, Address: "192.0.2.1"})
		if err != nil || iss.SecondFactor == nil {
			t.Fatalf("password step: %+v %v", iss, err)
		}
		in := auth.SecondFactorInput{Challenge: iss.SecondFactor.Token, Address: "192.0.2.1"}
		if field == "code" {
			in.Code = code
		} else {
			in.RecoveryCode = code
		}
		_, err = other.auth.CompleteSignIn(ctx, in)
		return err
	}
	if err := signIn("code", auth.TOTPCode(secret, clk.now())); err == nil || !strings.Contains(err.Error(), "key-encryption key") {
		t.Fatalf("TOTP with another KEK: %v", err)
	}
	if err := signIn("recovery_code", codes[0]); err != nil {
		t.Fatalf("recovery code with another KEK: %v", err)
	}
	_ = db.Close()

	// The host removes the second factor; the user signs in with the password.
	env := opsEnv{Options: config.Options{
		LookupEnv: func(k string) (string, bool) {
			switch k {
			case "LEDGER_DATABASE_PATH":
				return path, true
			case "LEDGER_KEK_VALUE":
				return "a different key-encryption key", true
			}
			return "", false
		},
		Environ: func() []string { return nil },
	}}
	var out, errb bytes.Buffer
	if code := runAuth([]string{"reset-2fa", "Carol"}, &out, &errb, env); code != 0 || !strings.Contains(out.String(), "removed for Carol") {
		t.Fatalf("reset-2fa: %d %s %s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := runAuth([]string{"reset-2fa", "carol"}, &out, &errb, env); code != 0 || !strings.Contains(out.String(), "nothing to reset") {
		t.Fatalf("reset-2fa again: %d %s", code, out.String())
	}
	if code := runAuth([]string{"reset-2fa", "nobody"}, &out, &errb, env); code != 1 {
		t.Errorf("unknown user: %d", code)
	}
	if code := runAuth([]string{"reset-2fa"}, &out, &errb, env); code != 2 {
		t.Errorf("no username: %d", code)
	}
	db, err = store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	al = store.NewAuditLog(db)
	final, _ := buildAPI(db, al, testAuthConfig(), testKEK, auth.NewArgon2id(cheapParams, 4), clk.now)
	if iss, err := final.auth.SignIn(ctx, auth.SignInInput{Username: "carol", Password: testPassword, Address: "192.0.2.1"}); err != nil || iss.SecondFactor != nil || iss.Token == "" {
		t.Fatalf("password-only after reset: %+v %v", iss.SecondFactor, err)
	}
	got, _ := al.List(ctx, audit.Filter{Actions: []audit.Action{audit.TwoFactorDisabled}, Limit: 10})
	if len(got) != 1 || got[0].Actor.Kind != audit.ActorSystem || got[0].Details["by"] != "host" || got[0].TargetID != u.ID {
		t.Errorf("audit %+v", got)
	}
}
