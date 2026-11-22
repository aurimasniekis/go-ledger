package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankconnect"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
	"github.com/aurimasniekis/go-ledger/internal/redact"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
	paymentstore "github.com/aurimasniekis/go-ledger/internal/store/payments"
	userstore "github.com/aurimasniekis/go-ledger/internal/store/users"
)

// permissionMatrix is the access every /api route must have (ADR-0021). It is
// written out by hand, so adding a route or changing its access needs a
// deliberate change here; TestPermissionMatrixCoversEveryRoute fails otherwise.
var permissionMatrix = map[string]string{
	"GET /api/v1/openapi.json":          "public",
	"POST /api/v1/mcp":                  "token",
	"GET /api/v1/openapi.yaml":          "public",
	"GET /api/v1/me":                    "authenticated",
	"GET /api/v1/grants":                "authenticated",
	"GET /api/v1/audit":                 "audit:read",
	"GET /api/v1/users":                 "users:read",
	"POST /api/v1/users":                "users:write+recent",
	"GET /api/v1/users/{id}":            "users:read",
	"PATCH /api/v1/users/{id}":          "users:write+recent",
	"PUT /api/v1/users/{id}/password":   "users:write+recent",
	"POST /api/v1/auth/sign-in":         "public",
	"POST /api/v1/auth/sign-out":        "session+enroll",
	"GET /api/v1/auth/session":          "session+enroll",
	"GET /api/v1/auth/sessions":         "session",
	"DELETE /api/v1/auth/sessions/{id}": "session",
	"POST /api/v1/auth/reauthenticate":  "session+enroll",
	"PUT /api/v1/auth/password":         "session+recent",
	"GET /api/v1/auth/tokens":           "session",
	"POST /api/v1/auth/tokens":          "session-or-root-token+recent",
	"GET /api/v1/auth/tokens/{id}":      "session",
	"DELETE /api/v1/auth/tokens/{id}":   "session",
	"GET /api/v1/tokens":                "tokens:read",
	"GET /api/v1/tokens/{id}":           "tokens:read",
	"DELETE /api/v1/tokens/{id}":        "tokens:write",

	"POST /api/v1/auth/sign-in/second-factor":     "public",
	"GET /api/v1/auth/two-factor":                 "session+enroll",
	"DELETE /api/v1/auth/two-factor":              "session+recent",
	"POST /api/v1/auth/two-factor/totp":           "session+recent+enroll",
	"POST /api/v1/auth/two-factor/totp/confirm":   "session+recent+enroll",
	"POST /api/v1/auth/two-factor/recovery-codes": "session+recent",

	"GET /api/v1/auth/methods":              "public",
	"GET /api/v1/auth/oidc/authorize":       "public",
	"GET /api/v1/auth/oidc/callback":        "public",
	"GET /api/v1/auth/oidc/link":            "session",
	"POST /api/v1/auth/oidc/link":           "session+recent",
	"DELETE /api/v1/auth/oidc/link":         "session+recent",
	"POST /api/v1/auth/oidc/reauthenticate": "session+enroll",
	"GET /api/v1/auth/recovery-sign-in":     "public",
	"POST /api/v1/auth/recovery-sign-in":    "public",

	"GET /api/v1/commitments":         "commitments:read",
	"POST /api/v1/commitments":        "commitments:write",
	"GET /api/v1/commitments/{id}":    "commitments:read",
	"PATCH /api/v1/commitments/{id}":  "commitments:write",
	"DELETE /api/v1/commitments/{id}": "commitments:write",

	"GET /api/v1/occurrences":         "commitments:read",
	"GET /api/v1/occurrences/summary": "commitments:read",
	"GET /api/v1/outlook":             "commitments:read",
	"GET /api/v1/occurrences/{id}":    "commitments:read",

	"GET /api/v1/occurrences/{id}/manual-payments":                 "commitments:read",
	"POST /api/v1/occurrences/{id}/manual-payments":                "commitments:write",
	"GET /api/v1/occurrences/{id}/manual-payments/{payment_id}":    "commitments:read",
	"DELETE /api/v1/occurrences/{id}/manual-payments/{payment_id}": "commitments:write",
	"PUT /api/v1/occurrences/{id}/status-override":                 "commitments:write",
	"DELETE /api/v1/occurrences/{id}/status-override":              "commitments:write",

	"GET /api/v1/payment-matches":                                         "commitments:read",
	"POST /api/v1/payment-matches":                                        "commitments:write",
	"GET /api/v1/payment-matches/{id}":                                    "commitments:read",
	"POST /api/v1/payment-matches/{id}/confirm":                           "commitments:write",
	"POST /api/v1/payment-matches/{id}/reject":                            "commitments:write",
	"POST /api/v1/payment-matches/{id}/unmatch":                           "commitments:write",
	"POST /api/v1/payment-matches/{id}/correct":                           "commitments:write",
	"POST /api/v1/payment-matches/{id}/reverse":                           "commitments:write",
	"POST /api/v1/payment-matches/{id}/unconfirm":                         "commitments:write",
	"POST /api/v1/payment-matches/{id}/confirm-replacing":                 "commitments:write",
	"GET /api/v1/deferral-plans":                                          "commitments:read",
	"POST /api/v1/deferral-plans":                                         "commitments:write",
	"GET /api/v1/deferral-plans/{id}":                                     "commitments:read",
	"PATCH /api/v1/deferral-plans/{id}":                                   "commitments:write",
	"POST /api/v1/deferral-plans/{id}/undo":                               "commitments:write",
	"GET /api/v1/deferral-plans/{id}/history":                             "commitments:read",
	"POST /api/v1/deferral-plans/{id}/adjustments":                        "commitments:write",
	"DELETE /api/v1/deferral-plans/{id}/adjustments/{adjustment_id}":      "commitments:write",
	"POST /api/v1/deferral-plans/{id}/adjustments/{adjustment_id}/paid":   "commitments:write",
	"POST /api/v1/deferral-plans/{id}/adjustments/{adjustment_id}/unpaid": "commitments:write",

	"GET /api/v1/bank-applications":                                           "connections:read",
	"GET /api/v1/bank-applications/{application}/institutions":                "connections:read",
	"GET /api/v1/bank-connections":                                            "connections:read",
	"POST /api/v1/bank-connections":                                           "connections:write",
	"POST /api/v1/bank-connections/complete":                                  "connections:write",
	"GET /api/v1/bank-connections/{id}":                                       "connections:read",
	"POST /api/v1/bank-connections/{id}/reconnect":                            "connections:write",
	"POST /api/v1/bank-connections/{id}/refresh":                              "connections:write",
	"POST /api/v1/bank-connections/{id}/disconnect":                           "connections:write",
	"POST /api/v1/payment-matching/runs":                                      "commitments:write",
	"GET /api/v1/transactions/{id}/merchant":                                  "merchants:read",
	"PUT /api/v1/transactions/{id}/merchant/review":                           "merchants:write",
	"DELETE /api/v1/transactions/{id}/merchant/review":                        "merchants:write",
	"GET /api/v1/categories":                                                  "organisation:read",
	"POST /api/v1/categories":                                                 "organisation:write",
	"GET /api/v1/categories/{id}":                                             "organisation:read",
	"PATCH /api/v1/categories/{id}":                                           "organisation:write",
	"DELETE /api/v1/categories/{id}":                                          "organisation:write",
	"POST /api/v1/categories/{id}/archive":                                    "organisation:write",
	"POST /api/v1/categories/{id}/unarchive":                                  "organisation:write",
	"GET /api/v1/labels":                                                      "organisation:read",
	"POST /api/v1/labels":                                                     "organisation:write",
	"GET /api/v1/labels/{id}":                                                 "organisation:read",
	"PATCH /api/v1/labels/{id}":                                               "organisation:write",
	"DELETE /api/v1/labels/{id}":                                              "organisation:write",
	"POST /api/v1/labels/{id}/archive":                                        "organisation:write",
	"POST /api/v1/labels/{id}/unarchive":                                      "organisation:write",
	"GET /api/v1/buckets":                                                     "organisation:read",
	"POST /api/v1/buckets":                                                    "organisation:write",
	"GET /api/v1/buckets/{id}":                                                "organisation:read",
	"PATCH /api/v1/buckets/{id}":                                              "organisation:write",
	"DELETE /api/v1/buckets/{id}":                                             "organisation:write",
	"POST /api/v1/buckets/{id}/archive":                                       "organisation:write",
	"POST /api/v1/buckets/{id}/unarchive":                                     "organisation:write",
	"GET /api/v1/buckets/{id}/members":                                        "organisation:read",
	"PUT /api/v1/buckets/{id}/members/{object_type}/{object_id}":              "organisation:write",
	"DELETE /api/v1/buckets/{id}/members/{object_type}/{object_id}":           "organisation:write",
	"GET /api/v1/organisation/{object_type}/{object_id}":                      "organisation:read",
	"PUT /api/v1/organisation/{object_type}/{object_id}/category":             "organisation:write",
	"DELETE /api/v1/organisation/{object_type}/{object_id}/category":          "organisation:write",
	"PUT /api/v1/organisation/{object_type}/{object_id}/labels/{label_id}":    "organisation:write",
	"DELETE /api/v1/organisation/{object_type}/{object_id}/labels/{label_id}": "organisation:write",
	"GET /api/v1/rates":                                                       "rates:read",
	"GET /api/v1/rates/status":                                                "rates:read",
	"POST /api/v1/rates/refreshes":                                            "rates:write",
}

// matrixAccess is a route's access as the matrix writes it: the access rule,
// with "+recent" when it needs recent authentication and "+enroll" when a
// session confined to two-factor enrolment may reach it (ADR-0030).
func matrixAccess(a httpapi.Access) string {
	s := a.String()
	if a.RecentAuth() {
		s += "+recent"
	}
	if a.AllowedDuringEnrollment() {
		s += "+enroll"
	}
	return s
}

// apiFixture is the API as ledger serve registers it, with a test
// authenticator: X-Test-User names the user, and X-Test-Token-Grants (comma
// separated) makes the request an API token of that user with those grants.
type apiFixture struct {
	srv   *httpapi.Server
	users *userstore.Store
	auth  *auth.Service
	log   *bytes.Buffer
	ids   map[string]string // subject name -> user ID
	// now is the API's clock (zero: the real time); zone the reporting zone.
	now  time.Time
	zone calendar.Zone
	// daveSession is a session of dave from before he was disabled.
	daveSession map[string]string
	// db is the fixture's database (for importing bank facts in tests).
	db *store.DB
	// bank is the fake banking provider behind the bank connection routes.
	bank *bankingtest.Provider
	// devBank is the developer-mode application beside it.
	devBank *bankingtest.Provider
	// connections is the bank connection service behind those routes (tests
	// close it to simulate shutdown).
	connections *bankconnect.Service
	// ratesDouble is the Frankfurter test server behind the rates routes, job
	// the refresh job and rateDeps the routes' wiring (a test may change it);
	// metrics receives the job's run metrics. No live service is contacted.
	ratesDouble *frankfurterDouble
	job         *fx.RefreshJob
	rateDeps    *httpapi.RatesDeps
	metrics     *metrics.App
}

// addAdmin creates one more administrator with the fixture password, for the
// tests that need a second person.
func (f *apiFixture) addAdmin(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	u, err := f.users.Create(ctx, audit.System, users.New{Username: name, Role: authz.RoleAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.SetPassword(ctx, authz.Principal{}, true, u.ID, testPassword, auth.ByHost); err != nil {
		t.Fatal(err)
	}
	f.ids[name] = u.ID
}

// signIn signs name in with the fixture password and returns the headers of
// a request made with that session: the cookie and the CSRF token.
func (f *apiFixture) signIn(t *testing.T, name string) map[string]string {
	t.Helper()
	iss, err := f.auth.SignIn(context.Background(), auth.SignInInput{Username: name, Password: testPassword, Address: "192.0.2.10"})
	if err != nil {
		t.Fatalf("sign in %s: %v", name, err)
	}
	return map[string]string{"Cookie": httpapi.SessionCookie + "=" + iss.Token, httpapi.CSRFHeader: iss.CSRFToken}
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	return newAPIFixtureIn(t, calendar.MustLoadZone("UTC"))
}

// newAPIFixtureIn is [newAPIFixture] with the given reporting time zone.
func newAPIFixtureIn(t *testing.T, zone calendar.Zone) *apiFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	buf := &bytes.Buffer{}
	log, err := logging.New(buf, "info", "json", redact.New())
	if err != nil {
		t.Fatal(err)
	}
	// Audit entries go to the real table and, so these tests can assert on them
	// line by line, to the log as well.
	al := teeAudit{AuditLog: store.NewAuditLog(db), lines: store.LogAudit(log)}
	parts := mustParts(t, db, al)
	f := &apiFixture{users: parts.users, auth: parts.auth, log: buf, ids: map[string]string{}, zone: zone}
	// Commitments, their occurrences and "today" follow the fixture's clock.
	clock := func() time.Time {
		if f.now.IsZero() {
			return time.Now()
		}
		return f.now
	}
	parts.commitments = commitmentstore.New(db, al, clock, nil)
	parts.payments = paymentstore.New(db, al, clock, nil)
	parts.matches = matchstore.New(db, al, clock, nil)
	// Bank connections run against the in-memory fake provider (B4): no live
	// bank is ever contacted.
	f.bank, f.devBank, parts.banking = newFakeBanking(db, al, clock)
	f.db = db
	f.connections = parts.banking
	// The rates routes (FX2c) use their own rate service on a Frankfurter test
	// double; the outlook's stays unset so its tests never reach it.
	f.ratesDouble, f.job, f.metrics, f.rateDeps = newRatesRig(t, db, al, clock)
	parts.rateCache = f.rateDeps
	authn := httpapi.AuthenticatorFunc(func(r *http.Request) (authz.Principal, error) {
		id := r.Header.Get("X-Test-User")
		if id == "" {
			return authz.Principal{}, httpapi.ErrNoCredentials
		}
		p, err := f.users.Principal(r.Context(), id)
		if errors.Is(err, users.ErrDisabled) || errors.Is(err, resource.ErrNotFound) {
			return authz.Principal{}, httpapi.ErrInvalidCredentials
		}
		if err != nil {
			return authz.Principal{}, err
		}
		if tg := r.Header.Get("X-Test-Token-Grants"); tg != "" {
			var gs authz.Grants
			for _, s := range strings.Split(tg, ",") {
				g, err := authz.ParseGrant(s)
				if err != nil {
					return authz.Principal{}, err
				}
				gs = append(gs, g)
			}
			return authz.TokenPrincipal("01JD3Y4C8V9M2Q7W5R6T0XZBNT", p.UserID, p.Role, gs), nil
		}
		return p, nil
	})
	// Real session cookies first, then the test header.
	f.srv = httpapi.New(config.HTTP{MaxBodyBytes: 1 << 16, ShutdownTimeout: time.Second}, log,
		httpapi.WithAuthenticators(httpapi.NewSessionAuth(parts.auth), testAuthn{authn}), httpapi.WithRecentAuth(testRecent(parts.auth)))
	registerAPI(f.srv, al, parts, zone, clock)

	mk := func(name string, role authz.Role) users.User {
		u, err := f.users.Create(ctx, audit.System, users.New{Username: name, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.auth.SetPassword(ctx, authz.Principal{}, true, u.ID, testPassword, auth.ByHost); err != nil {
			t.Fatal(err)
		}
		f.ids[name] = u.ID
		return u
	}
	mk("alice", authz.RoleAdministrator)
	mk("bob", authz.RoleReadOnly)
	mk("carol", authz.RoleReadOnly) // the target of /users/{id}; never changed by the matrix
	d := mk("dave", authz.RoleAdministrator)
	f.daveSession = f.signIn(t, "dave")
	if _, err := f.users.Update(ctx, audit.System, d.ID, 1, users.Patch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	return f
}

// teeAudit writes each event to the audit table and to a log line.
type teeAudit struct {
	*store.AuditLog
	lines store.AuditWriter
}

func (t teeAudit) WriteAudit(ctx context.Context, tx *sql.Tx, e audit.Event) error {
	if err := t.AuditLog.WriteAudit(ctx, tx, e); err != nil {
		return err
	}
	return t.lines.WriteAudit(ctx, tx, e)
}

func ptr[T any](v T) *T { return &v }

func (f *apiFixture) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if method == "PATCH" {
		req.Header.Set("Content-Type", httpapi.MergePatchType)
	} else if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestPermissionMatrixCoversEveryRoute(t *testing.T) {
	f := newAPIFixture(t)
	seen := map[string]bool{}
	for _, r := range f.srv.APIRoutes(httpapi.APIPrefix) {
		key := r.Method + " " + r.Path
		seen[key] = true
		want, ok := permissionMatrix[key]
		if !ok {
			t.Errorf("%s is registered but has no row in permissionMatrix", key)
			continue
		}
		if got := matrixAccess(r.Access); got != want {
			t.Errorf("%s requires %q, the matrix says %q", key, got, want)
		}
	}
	for key := range permissionMatrix {
		if !seen[key] {
			t.Errorf("%s is in permissionMatrix but not registered", key)
		}
	}
}

func safe(method string) bool { return method == "GET" || method == "HEAD" }

// TestPermissionMatrix calls every route as every kind of subject and checks
// the outcome: 401 without valid credentials, 403 without the grant, and
// neither when allowed. Read-only subjects never pass a state-changing route.
func TestPermissionMatrix(t *testing.T) {
	f := newAPIFixture(t)
	adminOnly := map[authz.Resource]bool{}
	for _, ri := range authz.Resources() {
		adminOnly[ri.Name] = ri.AdminOnly
	}
	type subject struct {
		name     string
		hdr      func() map[string]string
		valid    bool
		readOnly bool // must never pass a state-changing route that needs a grant
		session  bool // signed in with a session cookie
		token    bool // an API token
		allows   func(g authz.Grant) bool
	}
	static := func(h map[string]string) func() map[string]string { return func() map[string]string { return h } }
	// Signed-in subjects get a fresh session for every request, so a
	// sign-out or session termination in one row does not affect the next.
	session := func(name string) func() map[string]string {
		return func() map[string]string { return f.signIn(t, name) }
	}
	subjects := []subject{
		{name: "anonymous", hdr: static(nil), allows: func(authz.Grant) bool { return false }},
		{name: "disabled administrator's session", hdr: static(f.daveSession), allows: func(authz.Grant) bool { return false }},
		{name: "disabled administrator", hdr: static(map[string]string{"X-Test-User": f.ids["dave"]}), allows: func(authz.Grant) bool { return false }},
		{name: "unknown user", hdr: static(map[string]string{"X-Test-User": "01JD3Y4C8V9M2Q7W5R6T0XZBNZ"}), allows: func(authz.Grant) bool { return false }},
		{name: "unknown session", hdr: static(map[string]string{"Cookie": httpapi.SessionCookie + "=not-a-session", httpapi.CSRFHeader: "x"}), allows: func(authz.Grant) bool { return false }},
		{name: "administrator", hdr: session("alice"), valid: true, session: true, allows: func(authz.Grant) bool { return true }},
		{name: "read-only user", hdr: session("bob"), valid: true, readOnly: true, session: true,
			allows: func(g authz.Grant) bool { return g.Action == authz.ActionRead && !adminOnly[g.Resource] }},
		{name: "administrator's users:read token", hdr: static(map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "users:read"}), valid: true, readOnly: true, token: true,
			allows: func(g authz.Grant) bool { return g == authz.G(authz.ResourceUsers, authz.ActionRead) }},
		// The owner's read-only role does not allow users:write (nor users:read),
		// so the token holds no effective grant at all.
		{name: "read-only user's users:write token", hdr: static(map[string]string{"X-Test-User": f.ids["bob"], "X-Test-Token-Grants": "users:write"}), valid: true, readOnly: true, token: true,
			allows: func(authz.Grant) bool { return false }},
	}
	n := 0
	for _, r := range f.srv.APIRoutes(httpapi.APIPrefix) {
		path := strings.ReplaceAll(r.Path, "{id}", f.ids["carol"])
		for _, s := range subjects {
			n++
			body, hdr := "", map[string]string{}
			for k, v := range s.hdr() {
				hdr[k] = v
			}
			switch {
			case r.Method == "POST":
				body = fmt.Sprintf(`{"username":"new%d","role":"read-only"}`, n)
			case r.Method == "PATCH":
				body, hdr["If-Match"] = "{}", `"1"`
			case !safe(r.Method):
				hdr["If-Match"] = `"1"`
			}
			rec := f.do(r.Method, path, body, hdr)
			var allowed bool
			switch a := r.Access; a.String() {
			case "public":
				allowed = true
			case "authenticated":
				allowed = s.valid
			case "session":
				allowed = s.valid && s.session
			case "token":
				// The MCP endpoint takes any valid API token and checks grants
				// per tool (TestMCPHTTPGrantParity); sessions are refused.
				allowed = s.valid && s.token
			case "session-or-root-token":
				// No matrix subject is a root token of the root account (the
				// test tokens are not stored), so only sessions pass.
				allowed = s.valid && s.session
			default:
				g, _ := a.Grant()
				allowed = s.valid && s.allows(g)
			}
			name := fmt.Sprintf("%s %s as %s", r.Method, r.Path, s.name)
			switch {
			case allowed && (rec.Code == 401 || rec.Code == 403):
				t.Errorf("%s: %d, want allowed (%s)", name, rec.Code, rec.Body.String())
			case !allowed && !s.valid && rec.Code != 401:
				t.Errorf("%s: %d, want 401", name, rec.Code)
			case !allowed && s.valid && rec.Code != 403:
				t.Errorf("%s: %d, want 403", name, rec.Code)
			}
			_, needsGrant := r.Access.Grant()
			if s.readOnly && needsGrant && !safe(r.Method) && rec.Code != 403 {
				t.Errorf("%s: a read-only subject got %d on a state-changing route, want 403", name, rec.Code)
			}
			if allowed && rec.Code >= 500 {
				t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
			}
		}
	}
	// Forbidden and unauthenticated attempts changed nothing and audited nothing.
	if strings.Contains(f.log.String(), `"msg":"audit"`) {
		// Only the administrator's POSTs may have been audited.
		for _, line := range strings.Split(strings.TrimSpace(f.log.String()), "\n") {
			// Sign-ins, sign-outs and session ends are each subject's own.
			if strings.Contains(line, `"action":"auth.`) {
				continue
			}
			if strings.Contains(line, `"msg":"audit"`) && !strings.Contains(line, `"actor_user_id":"`+f.ids["alice"]+`"`) {
				t.Errorf("audit line from a subject that was not allowed: %s", line)
			}
		}
	}
	if u, _ := f.users.Get(context.Background(), f.ids["carol"]); u.Version != 1 {
		t.Errorf("carol was changed: %+v", u)
	}
}

func auditLines(t *testing.T, log string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "audit" {
			out = append(out, m)
		}
	}
	return out
}

// TestUserManagementIsAudited runs the user API as ledger serve wires it
// (audit events through the interim log writer) and checks the trail.
func TestUserManagementIsAudited(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}

	rec := f.do("POST", "/api/v1/users", `{"username":"erin","display_name":"Erin","role":"read-only"}`, admin)
	if rec.Code != 201 || rec.Header().Get("ETag") != `"1"` || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v1/users/") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var erin struct {
		ID       string `json:"id"`
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
		Version  int64  `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &erin); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/users/" + erin.ID
	steps := []struct {
		body, ifMatch string
		status        int
	}{
		{`{"role":"administrator"}`, `"1"`, 200},
		{`{"disabled":true,"version":2}`, "", 200},
		{`{"disabled":false,"role":"read-only","display_name":null}`, `"3"`, 200},
	}
	for i, s := range steps {
		hdr := map[string]string{"X-Test-User": f.ids["alice"]}
		if s.ifMatch != "" {
			hdr["If-Match"] = s.ifMatch
		}
		if rec := f.do("PATCH", path, s.body, hdr); rec.Code != s.status {
			t.Fatalf("step %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	var got []string
	for _, l := range auditLines(t, f.log.String()) {
		if l["target_id"] != erin.ID {
			continue
		}
		if l["actor_user_id"] != f.ids["alice"] || l["actor_kind"] != "user" {
			t.Errorf("actor: %v", l)
		}
		s := fmt.Sprint(l["action"])
		if l["detail_from"] != nil {
			s += fmt.Sprintf(" %v->%v", l["detail_from"], l["detail_to"])
		}
		if l["detail_fields"] != nil {
			s += fmt.Sprintf(" %v", l["detail_fields"])
		}
		got = append(got, s)
	}
	want := "user.created|user.role_changed read-only->administrator|user.disabled|user.role_changed administrator->read-only|user.enabled|user.updated display_name"
	if strings.Join(got, "|") != want {
		t.Fatalf("audit trail\n got %s\nwant %s", strings.Join(got, "|"), want)
	}

	// The last active administrator cannot be demoted, and the refusal is not audited.
	f.log.Reset()
	rec = f.do("PATCH", "/api/v1/users/"+f.ids["alice"], `{"role":"read-only"}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"conflict"`) {
		t.Fatalf("last admin: %d %s", rec.Code, rec.Body.String())
	}
	if len(auditLines(t, f.log.String())) != 0 {
		t.Fatalf("refused change audited: %s", f.log.String())
	}
	// A role change is effective on the next request: demote erin's promoter path.
	rec = f.do("GET", "/api/v1/users", "", map[string]string{"X-Test-User": erin.ID})
	if rec.Code != 403 {
		t.Fatalf("read-only erin listing users: %d", rec.Code)
	}
}

func TestUserAPIValidationAndConflicts(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	for _, c := range []struct {
		method, path, body string
		hdr                map[string]string
		status             int
		field              string
	}{
		{"POST", "/api/v1/users", `{"role":"read-only"}`, nil, 400, "username"},
		{"POST", "/api/v1/users", `{"username":"x"}`, nil, 400, "role"},
		{"POST", "/api/v1/users", `{"username":"X","role":"read-only"}`, nil, 400, "username"},
		{"POST", "/api/v1/users", `{"username":"x","role":"owner"}`, nil, 400, "role"},
		{"POST", "/api/v1/users", `{"username":"x","role":"read-only","password":"p"}`, nil, 400, ""},
		{"POST", "/api/v1/users", `{"username":"bob","role":"read-only"}`, nil, 409, "username"},
		{"PATCH", "/api/v1/users/{carol}", `{"role":null}`, map[string]string{"If-Match": `"1"`}, 400, "role"},
		{"PATCH", "/api/v1/users/{carol}", `{"disabled":"yes"}`, map[string]string{"If-Match": `"1"`}, 400, ""},
		{"PATCH", "/api/v1/users/{carol}", `{"role":"administrator"}`, nil, 428, ""},
		{"PATCH", "/api/v1/users/{carol}", `{"role":"administrator"}`, map[string]string{"If-Match": `"7"`}, 409, ""},
		{"PATCH", "/api/v1/users/{carol}", `{"username":"bob"}`, map[string]string{"If-Match": `"1"`}, 409, "username"},
		{"PATCH", "/api/v1/users/{carol}", `{"secret":1}`, map[string]string{"If-Match": `"1"`}, 400, ""},
		{"GET", "/api/v1/users/01JD3Y4C8V9M2Q7W5R6T0XZBNZ", "", nil, 404, ""},
		{"GET", "/api/v1/users?role=owner", "", nil, 400, ""},
		{"GET", "/api/v1/users?disabled=maybe", "", nil, 400, ""},
	} {
		hdr := map[string]string{}
		for k, v := range admin {
			hdr[k] = v
		}
		for k, v := range c.hdr {
			hdr[k] = v
		}
		path := strings.ReplaceAll(c.path, "{carol}", f.ids["carol"])
		rec := f.do(c.method, path, c.body, hdr)
		if rec.Code != c.status {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, rec.Code, rec.Body.String())
			continue
		}
		if c.field != "" && !strings.Contains(rec.Body.String(), `"field":"`+c.field+`"`) {
			t.Errorf("%s %s: field missing: %s", c.method, c.body, rec.Body.String())
		}
	}
	// Version conflict carries the current user for reconciliation.
	rec := f.do("PATCH", "/api/v1/users/"+f.ids["carol"], `{"role":"administrator"}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"7"`})
	if !strings.Contains(rec.Body.String(), `"current_version":1`) || !strings.Contains(rec.Body.String(), `"username":"carol"`) {
		t.Errorf("reconciliation: %s", rec.Body.String())
	}
	// Lists filter and page.
	rec = f.do("GET", "/api/v1/users?role=administrator&disabled=false", "", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"username":"alice"`) || strings.Contains(rec.Body.String(), "dave") || strings.Contains(rec.Body.String(), "bob") {
		t.Errorf("filtered list: %s", rec.Body.String())
	}
	rec = f.do("GET", "/api/v1/users?username[prefix]=ca&limit=1", "", admin)
	var page struct {
		Items []struct {
			Username string `json:"username"`
		} `json:"items"`
		Next *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].Username != "carol" || page.Next != nil {
		t.Errorf("prefix list: %s", rec.Body.String())
	}
	rec = f.do("GET", "/api/v1/users?limit=2", "", admin)
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.Next == nil {
		t.Errorf("paged list: %s", rec.Body.String())
	}
	// An empty patch is a no-op and is not audited.
	f.log.Reset()
	rec = f.do("PATCH", "/api/v1/users/"+f.ids["carol"], `{}`, map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"1"` || len(auditLines(t, f.log.String())) != 0 {
		t.Errorf("empty patch: %d %s", rec.Code, rec.Header().Get("ETag"))
	}
}

// newFakeBanking builds the bank connection service over two in-memory fake
// provider applications: app mode and, beside it, developer mode (D10: modes
// coexist per connection), which sees only the accounts linked to it. No live
// bank is ever contacted.
func newFakeBanking(db *store.DB, al interface {
	store.AuditWriter
	audit.Recorder
}, clock func() time.Time) (app, dev *bankingtest.Provider, svc *bankconnect.Service) {
	app = bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: clock}, bankingtest.Standard())
	dev = bankingtest.New(bankingtest.Options{Mode: banking.ModeDeveloper, Now: clock}, bankingtest.Standard())
	facts := bankfacts.New(db, nil).WithAudit(al)
	svc = &bankconnect.Service{Facts: facts, Providers: map[string]banking.Provider{app.Capabilities().Application: app, dev.Capabilities().Application: dev},
		Engine: bankimport.New(facts, bankimport.Options{Now: clock, Sleep: func(context.Context, time.Duration) error { return nil }}), Audit: al, RedirectURL: "https://ledger.example/connections/callback", Now: clock}
	return app, dev, svc
}
