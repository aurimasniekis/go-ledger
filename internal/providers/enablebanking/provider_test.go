package enablebanking_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/providers/enablebanking"
)

var bothModes = []banking.Mode{banking.ModeApp, banking.ModeDeveloper}

type env struct {
	t     *testing.T
	ctx   context.Context
	clock *clock
	f     *fakeEB
	p     *enablebanking.Provider
}

func newTestEnv(t *testing.T, mode banking.Mode) *env {
	t.Helper()
	c := &clock{now: t0}
	f := newFake(t, mode, banking.EnvironmentProduction, c)
	return &env{t: t, ctx: context.Background(), clock: c, f: f, p: f.provider(c)}
}

// connect runs the consent flow and returns the session.
func (e *env) connect() banking.Session {
	e.t.Helper()
	insts, err := e.p.Institutions(e.ctx, "fi")
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.p.StartConsent(e.ctx, banking.ConsentRequest{
		Institution: insts[0], ValidUntil: e.clock.Now().Add(90 * 24 * time.Hour),
		RedirectURL: "https://ledger.example/callback", State: "state-1", PSUType: banking.PSUPersonal,
	}); err != nil {
		e.t.Fatal(err)
	}
	s, err := e.p.CompleteConsent(e.ctx, banking.ConsentCompletion{State: "state-1", Code: banking.NewSensitive(syntheticCode)})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func handleOf(t *testing.T, accs []banking.Account, ext string) banking.Sensitive {
	t.Helper()
	for _, a := range accs {
		if a.ExternalID == ext {
			return a.Handle
		}
	}
	t.Fatalf("account %s not reported", ext)
	return banking.Sensitive{}
}

func externalIDs(accs []banking.Account) []string {
	out := []string{}
	for _, a := range accs {
		out = append(out, a.ExternalID)
	}
	return out
}

func TestCapabilitiesPerMode(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newTestEnv(t, mode)
			c := e.p.Capabilities()
			want := map[banking.Mode]banking.AccountAccess{banking.ModeApp: banking.AccountAccessAll, banking.ModeDeveloper: banking.AccountAccessLinkedOnly}[mode]
			if c.Provider != enablebanking.ProviderName || c.Mode != mode || c.AccountAccess() != want || c.Environment != banking.EnvironmentProduction {
				t.Fatalf("capabilities %+v", c)
			}
			if c.Application != "eb-"+string(mode) || !c.PendingTransactions || c.BackgroundFetchesPerDay != 4 || c.MaxHistoryDays != 0 {
				t.Fatalf("capabilities %+v", c)
			}
		})
	}
}

func TestConsentAndFetchBothModes(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newTestEnv(t, mode)
			insts, err := e.p.Institutions(e.ctx, "FI")
			if err != nil {
				t.Fatal(err)
			}
			if len(insts) != 2 || insts[0].ID != "Example Bank" || insts[0].MaxConsentValidity != 180*24*time.Hour {
				t.Fatalf("institutions %+v", insts)
			}
			// A validity beyond the bank's maximum is capped before sending.
			start, err := e.p.StartConsent(e.ctx, banking.ConsentRequest{
				Institution: insts[0], ValidUntil: t0.Add(400 * 24 * time.Hour),
				RedirectURL: "https://ledger.example/callback", State: "state-1", PSUType: banking.PSUPersonal,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(start.AuthorizationURL, "https://auth.bank.example/") || start.Reference == "" {
				t.Fatalf("start %+v", start)
			}
			access := e.f.authBody["access"].(map[string]any)
			vu, err := time.Parse(time.RFC3339, access["valid_until"].(string))
			if err != nil || vu.After(t0.Add(180*24*time.Hour)) || vu.Before(t0.Add(179*24*time.Hour)) {
				t.Fatalf("valid_until %v (%v)", access["valid_until"], err)
			}
			if e.f.authBody["psu_type"] != "personal" {
				t.Fatalf("psu_type %v", e.f.authBody["psu_type"])
			}

			s, err := e.p.CompleteConsent(e.ctx, banking.ConsentCompletion{State: "state-1", Code: banking.NewSensitive(syntheticCode)})
			if err != nil {
				t.Fatal(err)
			}
			if s.Reference.IsZero() || !s.ValidUntil.Equal(t0.Add(90*24*time.Hour)) {
				t.Fatalf("session %+v valid until %v", s, s.ValidUntil)
			}
			wantIDs := map[banking.Mode]string{banking.ModeApp: hashEUR + "," + hashGBP, banking.ModeDeveloper: hashEUR}[mode]
			if got := strings.Join(externalIDs(s.Accounts), ","); got != wantIDs {
				t.Fatalf("session accounts %s, want %s", got, wantIDs)
			}

			accs, err := e.p.Accounts(e.ctx, s.Reference)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(externalIDs(accs), ","); got != wantIDs {
				t.Fatalf("accounts %s, want %s", got, wantIDs)
			}
			// Details were cached from the authorize response: no extra bank call.
			if n := e.f.count("GET /accounts/"); n != 0 {
				t.Fatalf("%d account calls for cached details", n)
			}
			eur := accs[0]
			if eur.Identifiers.IBAN.Reveal() != syntheticIBAN1 || eur.Identifiers.MaskedPAN.Reveal() != "****1111" ||
				eur.Name != "Everyday account" || eur.Product != "Current account" || eur.Currency.Code() != "EUR" {
				t.Fatalf("eur account %+v / %s / %s", eur, eur.Identifiers.IBAN.Reveal(), eur.Identifiers.MaskedPAN.Reveal())
			}

			bals, err := e.p.Balances(e.ctx, s.Reference, eur.Handle)
			if err != nil {
				t.Fatal(err)
			}
			if len(bals) != 4 || bals[0].Type != banking.BalanceBooked || bals[0].ProviderType != "CLBD" ||
				!bals[0].Amount.Identical(money.MustParse("1250.40", "EUR")) || bals[0].ReferenceDate != calendar.MustDate(2026, 10, 31) ||
				bals[1].Type != banking.BalanceAvailable || bals[2].Type != banking.BalanceExpected {
				t.Fatalf("balances %+v", bals)
			}
			if bals[3].Validate() == nil {
				t.Fatal("a balance without a currency must not validate (it is dropped, never stored as zero)")
			}

			txs := fetchAll(t, e, s.Reference, eur.Handle, banking.TransactionQuery{From: calendar.MustDate(2026, 10, 1), To: calendar.MustDate(2026, 10, 31)})
			if len(txs) != 7 {
				t.Fatalf("%d transactions", len(txs))
			}
		})
	}
}

func fetchAll(t *testing.T, e *env, sess, acct banking.Sensitive, q banking.TransactionQuery) []banking.Transaction {
	t.Helper()
	var out []banking.Transaction
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("paging did not end")
		}
		page, err := e.p.Transactions(e.ctx, sess, acct, q)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, page.Transactions...)
		if page.Next == "" {
			return out
		}
		q.Continuation = page.Next
	}
}

func TestTransactionsPagingAndStrategy(t *testing.T) {
	e := newTestEnv(t, banking.ModeDeveloper)
	s := e.connect()
	h := handleOf(t, s.Accounts, hashEUR)

	// First page is empty with a key: keep going.
	page, err := e.p.Transactions(e.ctx, s.Reference, h, banking.TransactionQuery{From: calendar.MustDate(2026, 10, 1), To: calendar.MustDate(2026, 10, 15)})
	if err != nil || len(page.Transactions) != 0 || page.Next == "" {
		t.Fatalf("first page %+v, %v", page, err)
	}
	txs := fetchAll(t, e, s.Reference, h, banking.TransactionQuery{From: calendar.MustDate(2026, 10, 1), To: calendar.MustDate(2026, 10, 15)})
	if len(txs) != 4 {
		t.Fatalf("window: %d transactions", len(txs))
	}
	last := e.f.requests()[len(e.f.requests())-1]
	if !strings.Contains(last, "date_from=2026-10-01") || !strings.Contains(last, "date_to=2026-10-15") || strings.Contains(last, "strategy") {
		t.Fatalf("query %s", last)
	}

	// A zero From asks for the longest history; To is applied locally.
	txs = fetchAll(t, e, s.Reference, h, banking.TransactionQuery{To: calendar.MustDate(2026, 10, 15)})
	last = e.f.requests()[len(e.f.requests())-1]
	if !strings.Contains(last, "strategy=longest") || strings.Contains(last, "date_") {
		t.Fatalf("query %s", last)
	}
	if len(txs) != 4 {
		t.Fatalf("longest up to Oct 15: %d transactions", len(txs))
	}
}

func TestTransactionMapping(t *testing.T) {
	e := newTestEnv(t, banking.ModeApp)
	s := e.connect()
	txs := fetchAll(t, e, s.Reference, handleOf(t, s.Accounts, hashEUR), banking.TransactionQuery{From: calendar.MustDate(2026, 10, 1), To: calendar.MustDate(2026, 10, 31)})
	by := map[string]banking.Transaction{}
	for _, tx := range txs {
		by[strings.SplitN(tx.Description, "\n", 2)[len(strings.SplitN(tx.Description, "\n", 2))-1]] = tx
	}
	check := func(name string, ok bool) {
		t.Helper()
		if !ok {
			t.Errorf("%s: %+v", name, by)
		}
	}
	sal := by["SALARY OCTOBER"]
	check("salary", sal.Amount.Identical(money.MustParse("2500.00", "EUR")) && sal.Status == banking.StatusBooked &&
		sal.ProviderStatus == "BOOK" && sal.EntryReference == "synthetic-ref-salary" && sal.Counterparty == "Example Employer" &&
		sal.ProviderTransactionID == "detail-1" && sal.Validate() == nil)
	rent := by["RENT 2026-10"]
	check("rent (debit, multi-line remittance)", rent.Amount.Identical(money.MustParse("-800.00", "EUR")) &&
		rent.Description == "RF18539007547034\nRENT 2026-10" && rent.Counterparty == "Example Landlord")
	check("already signed debit", by["STREAMING SERVICE"].Amount.Identical(money.MustParse("-12.99", "EUR")))
	card := by["CARD PURCHASE"]
	check("card abroad", card.Amount.Identical(money.MustParse("-23.17", "EUR")) &&
		card.InstructedAmount.Identical(money.MustParse("-20.00", "GBP")) &&
		card.TransactionDate == calendar.MustDate(2026, 10, 10) && card.BookingDate == calendar.MustDate(2026, 10, 12))
	contra := by["CONTRADICTORY SIGN"]
	check("contradictory sign is rejected, not guessed", contra.Validate() != nil && !contra.Amount.IsValid())
	hold := by["HOTEL DEPOSIT"]
	check("hold is pending", hold.Status == banking.StatusPending && hold.ProviderStatus == "HOLD" && hold.Date() == calendar.MustDate(2026, 10, 29))
	pdng := by["COFFEE KIOSK"]
	check("pending without booking date", pdng.Status == banking.StatusPending && pdng.BookingDate.IsZero() && pdng.Date() == calendar.MustDate(2026, 10, 30))
}

func TestDeveloperModeUnlinkedAccountIsEmptyList(t *testing.T) {
	e := newTestEnv(t, banking.ModeDeveloper)
	e.f.linked[accountEUR] = false
	s := e.connect()
	if len(s.Accounts) != 0 {
		t.Fatalf("session accounts %v", externalIDs(s.Accounts))
	}
	accs, err := e.p.Accounts(e.ctx, s.Reference)
	if err != nil || accs == nil || len(accs) != 0 {
		t.Fatalf("accounts %v, %v: want an empty list and no error", accs, err)
	}
}

func TestAccountsFetchesDetailsOnce(t *testing.T) {
	e := newTestEnv(t, banking.ModeApp)
	s := e.connect()
	// A new adapter (a restart) has no cache: details are fetched once per
	// account and session, then reused.
	p := e.f.provider(e.clock)
	for range 2 {
		accs, err := p.Accounts(e.ctx, s.Reference)
		if err != nil || len(accs) != 2 {
			t.Fatalf("accounts %v, %v", accs, err)
		}
		if accs[1].Identifiers.BBAN.Reveal() != "SYNTHETIC-BBAN-0002" || accs[1].Name != "Savings" || accs[1].Currency.Code() != "GBP" {
			t.Fatalf("gbp account %+v", accs[1])
		}
	}
	if n := e.f.count("GET /accounts/"); n != 2 {
		t.Fatalf("%d details calls, want 2", n)
	}
}

func TestReauthorisationChangesHandlesNotExternalIDs(t *testing.T) {
	e := newTestEnv(t, banking.ModeApp)
	s1 := e.connect()
	s2 := e.connect()
	if s1.Reference.Equal(s2.Reference) {
		t.Fatal("same session reference")
	}
	if handleOf(t, s1.Accounts, hashEUR).Equal(handleOf(t, s2.Accounts, hashEUR)) {
		t.Fatal("same handle across sessions")
	}
}

func TestExpiredConsent(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newTestEnv(t, mode)
			s := e.connect()
			h := handleOf(t, s.Accounts, hashEUR)
			e.clock.Add(91 * 24 * time.Hour)

			_, err := e.p.Transactions(e.ctx, s.Reference, h, banking.TransactionQuery{From: calendar.MustDate(2027, 1, 1)})
			assertKind(t, err, banking.KindReconnectRequired, banking.ReasonConsentExpired, "EXPIRED_SESSION")
			if !errors.Is(err, banking.ErrConsentExpired) {
				t.Fatalf("errors.Is(ErrConsentExpired) false for %v", err)
			}
			if st, ok := banking.StatusAfter(err); !ok || st != banking.ConnectionReconnectRequired {
				t.Fatalf("status after %v %v", st, ok)
			}
			_, err = e.p.Balances(e.ctx, s.Reference, h)
			assertKind(t, err, banking.KindReconnectRequired, banking.ReasonConsentExpired, "EXPIRED_SESSION")
			// The session status says so too.
			_, err = e.p.Accounts(e.ctx, s.Reference)
			assertKind(t, err, banking.KindReconnectRequired, banking.ReasonConsentExpired, "session_expired")
		})
	}
}

func TestSessionStatuses(t *testing.T) {
	for _, tc := range []struct {
		status string
		reason banking.Reason
		code   string
	}{
		{"REVOKED", banking.ReasonConsentRevoked, "session_revoked"},
		{"CLOSED", banking.ReasonSessionInvalid, "session_closed"},
		{"CANCELLED", banking.ReasonSessionInvalid, "session_cancelled"},
		{"PENDING_AUTHORIZATION", banking.ReasonSessionInvalid, "session_pending_authorization"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			e := newTestEnv(t, banking.ModeApp)
			s := e.connect()
			e.f.setSession(tc.status)
			_, err := e.p.Accounts(e.ctx, s.Reference)
			assertKind(t, err, banking.KindReconnectRequired, tc.reason, tc.code)
		})
	}
	e := newTestEnv(t, banking.ModeApp)
	_, err := e.p.Accounts(e.ctx, banking.NewSensitive("00000000-0000-4000-9000-999999999999"))
	assertKind(t, err, banking.KindReconnectRequired, banking.ReasonSessionInvalid, "SESSION_DOES_NOT_EXIST")
}

func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ft         fault
		kind       banking.ErrorKind
		reason     banking.Reason
		code       string
		retryAfter time.Duration
	}{
		{"revoked", fault{status: 401, file: "error_revoked_session.json"}, banking.KindReconnectRequired, banking.ReasonConsentRevoked, "REVOKED_SESSION", 0},
		{"closed", fault{status: 400, file: "error_closed_session.json"}, banking.KindReconnectRequired, banking.ReasonSessionInvalid, "CLOSED_SESSION", 0},
		{"bank rate limit, no header: EB's 6 h guidance", fault{status: 429, file: "error_rate_limit.json"}, banking.KindRateLimited, "", "ASPSP_RATE_LIMIT_EXCEEDED", 6 * time.Hour},
		{"bank rate limit with Retry-After", fault{status: 429, file: "error_rate_limit.json", header: http.Header{"Retry-After": {"120"}}}, banking.KindRateLimited, "", "ASPSP_RATE_LIMIT_EXCEEDED", 2 * time.Minute},
		{"bare 429 with HTTP-date", fault{status: 429, raw: "slow down", header: http.Header{"Retry-After": {t0.Add(30 * time.Second).Format(http.TimeFormat)}}}, banking.KindRateLimited, "", "http_429", 30 * time.Second},
		{"bare 429 without Retry-After", fault{status: 429, raw: "{}"}, banking.KindRateLimited, "", "http_429", 0},
		{"bank error", fault{status: 500, file: "error_aspsp.json"}, banking.KindRetryable, "", "ASPSP_ERROR", 0},
		{"outage page", fault{status: 503, raw: "<html>maintenance SECRET-ECHO</html>", header: http.Header{"Retry-After": {"60"}}}, banking.KindRetryable, "", "http_503", time.Minute},
		{"gateway timeout", fault{status: 504, raw: ""}, banking.KindRetryable, "", "http_504", 0},
		{"unauthorized application", fault{status: 401, file: "error_unauthorized.json"}, banking.KindPermanent, "", "UNAUTHORIZED_ACCESS", 0},
		{"wrong period", fault{status: 422, file: "error_wrong_period.json"}, banking.KindPermanent, "", "WRONG_TRANSACTIONS_PERIOD", 0},
		{"garbage code is not echoed", fault{status: 400, raw: `{"error":"<script>SECRET-ECHO"}`}, banking.KindPermanent, "", "http_400", 0},
		{"undecodable success", fault{status: 200, raw: "not json SECRET-ECHO"}, banking.KindPermanent, "", "invalid_response", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, banking.ModeApp)
			s := e.connect()
			ft := tc.ft
			ft.match = "/transactions"
			e.f.fail(ft)
			_, err := e.p.Transactions(e.ctx, s.Reference, handleOf(t, s.Accounts, hashEUR), banking.TransactionQuery{From: calendar.MustDate(2026, 10, 1)})
			assertKind(t, err, tc.kind, tc.reason, tc.code)
			if got, _ := banking.RetryAfter(err); got != tc.retryAfter {
				t.Fatalf("retry after %v, want %v", got, tc.retryAfter)
			}
			assertClean(t, err, s)
		})
	}
}

func TestOutage(t *testing.T) {
	c := &clock{now: t0}
	f := newFake(t, banking.ModeApp, banking.EnvironmentProduction, c)
	p := f.provider(c)
	e := &env{t: t, ctx: context.Background(), clock: c, f: f, p: p}
	s := e.connect()
	h := handleOf(t, s.Accounts, hashEUR)

	t.Run("server unreachable", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		url := dead.URL
		dead.Close()
		p := f.provider(c, func(cfg *enablebanking.Config) { cfg.BaseURL = url; cfg.HTTPClient = nil })
		_, err := p.Balances(e.ctx, s.Reference, h)
		assertKind(t, err, banking.KindRetryable, "", "")
		assertClean(t, err, s)
	})
	t.Run("timeout", func(t *testing.T) {
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}))
		defer slow.Close()
		p := f.provider(c, func(cfg *enablebanking.Config) {
			cfg.BaseURL = slow.URL
			cfg.HTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
		})
		_, err := p.Balances(e.ctx, s.Reference, h)
		assertKind(t, err, banking.KindRetryable, "", "")
		assertClean(t, err, s)
	})
	t.Run("cancelled by Ledger", func(t *testing.T) {
		ctx, cancel := context.WithCancel(e.ctx)
		cancel()
		_, err := p.Balances(ctx, s.Reference, h)
		assertKind(t, err, banking.KindCanceled, "", "")
		if banking.Classify(err) != banking.KindCanceled {
			t.Fatal("not classified as canceled")
		}
	})
}

func TestEndSession(t *testing.T) {
	e := newTestEnv(t, banking.ModeApp)
	s := e.connect()
	if err := e.p.EndSession(e.ctx, s.Reference); err != nil {
		t.Fatal(err)
	}
	// Ending it again is not an error.
	if err := e.p.EndSession(e.ctx, s.Reference); err != nil {
		t.Fatal(err)
	}
	if err := e.p.EndSession(e.ctx, banking.NewSensitive("00000000-0000-4000-9000-999999999999")); err != nil {
		t.Fatal(err)
	}
	e.f.fail(fault{match: "DELETE /sessions", status: 503, raw: ""})
	assertKind(t, e.p.EndSession(e.ctx, s.Reference), banking.KindRetryable, "", "http_503")
}

func TestCompleteConsentFailures(t *testing.T) {
	e := newTestEnv(t, banking.ModeApp)
	_, err := e.p.CompleteConsent(e.ctx, banking.ConsentCompletion{State: "s", Error: "access_denied"})
	assertKind(t, err, banking.KindPermanent, "", "consent_refused")
	if n := e.f.count("POST /sessions"); n != 0 {
		t.Fatalf("refused consent made %d calls", n)
	}
	_, err = e.p.CompleteConsent(e.ctx, banking.ConsentCompletion{State: "s", Code: banking.NewSensitive("forged-code")})
	assertKind(t, err, banking.KindPermanent, "", "WRONG_AUTHORIZATION_CODE")
	if strings.Contains(err.Error(), "forged-code") {
		t.Fatal("authorisation code leaked into the error")
	}
}

func TestVerify(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(f *fakeEB)
		code string
	}{
		{"ok", func(*fakeEB) {}, ""},
		{"environment mismatch", func(f *fakeEB) { f.env = "SANDBOX" }, "environment_mismatch"},
		{"other application", func(f *fakeEB) {
			f.fail(fault{match: "/application", status: 200, raw: `{"kid":"11111111-1111-4111-8111-111111111111","environment":"PRODUCTION","active":true}`})
		}, "application_mismatch"},
		{"inactive", func(f *fakeEB) {
			f.fail(fault{match: "/application", status: 200, raw: fmt.Sprintf(`{"kid":%q,"environment":"PRODUCTION","active":false}`, testAppID)})
		}, "application_inactive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, banking.ModeDeveloper)
			tc.mut(e.f)
			err := e.p.Verify(e.ctx)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertKind(t, err, banking.KindPermanent, "", tc.code)
		})
	}
}

func TestSandboxEnvironmentIsNotAMode(t *testing.T) {
	for _, mode := range bothModes {
		c := &clock{now: t0}
		f := newFake(t, mode, banking.EnvironmentSandbox, c)
		p := f.provider(c)
		if err := p.Verify(context.Background()); err != nil {
			t.Fatal(err)
		}
		if caps := p.Capabilities(); caps.Environment != banking.EnvironmentSandbox || caps.Mode != mode {
			t.Fatalf("caps %+v", caps)
		}
	}
}

func TestJWTReuseAndRenewal(t *testing.T) {
	e := newTestEnv(t, banking.ModeApp)
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := e.f.checkJWT(r); err != nil {
			t.Errorf("jwt: %v", err)
		}
		tokens = append(tokens, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"aspsps":[]}`))
	}))
	defer srv.Close()
	p := e.f.provider(e.clock, func(c *enablebanking.Config) { c.BaseURL = srv.URL; c.HTTPClient = srv.Client() })
	for _, step := range []time.Duration{0, 10 * time.Minute, 50 * time.Minute} {
		e.clock.Add(step)
		if _, err := p.Institutions(e.ctx, "FI"); err != nil {
			t.Fatal(err)
		}
	}
	if tokens[0] != tokens[1] {
		t.Fatal("token not reused within its lifetime")
	}
	if tokens[1] == tokens[2] {
		t.Fatal("token not renewed near expiry")
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	key, err := enablebanking.ParsePrivateKey(testKeyPEM())
	if err != nil {
		t.Fatal(err)
	}
	good := enablebanking.Config{Application: "main", AppID: testAppID, Mode: banking.ModeDeveloper, Environment: banking.EnvironmentProduction, PrivateKey: key}
	if _, err := enablebanking.New(good); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*enablebanking.Config){
		"app id":      func(c *enablebanking.Config) { c.AppID = "not-a-uuid" },
		"mode":        func(c *enablebanking.Config) { c.Mode = "sandbox" },
		"environment": func(c *enablebanking.Config) { c.Environment = "dev" },
		"key":         func(c *enablebanking.Config) { c.PrivateKey = nil },
		"name":        func(c *enablebanking.Config) { c.Application = "Has Spaces" },
		"ttl":         func(c *enablebanking.Config) { c.TokenTTL = 25 * time.Hour },
		"base url":    func(c *enablebanking.Config) { c.BaseURL = "ftp://x" },
	} {
		c := good
		mut(&c)
		if _, err := enablebanking.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), testAppID) {
			t.Errorf("%s: error names the app id", name)
		}
	}
	for name, data := range map[string]string{
		"not pem": "hello",
		"cert":    "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
		"garbage": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
	} {
		if _, err := enablebanking.ParsePrivateKey([]byte(data)); err == nil {
			t.Errorf("%s: parsed", name)
		} else if strings.Contains(err.Error(), "AAAA") {
			t.Errorf("%s: error echoes key material", name)
		}
	}
}

func assertKind(t *testing.T, err error, kind banking.ErrorKind, reason banking.Reason, code string) {
	t.Helper()
	var be *banking.Error
	if !errors.As(err, &be) {
		t.Fatalf("error %v (%T) is not a *banking.Error", err, err)
	}
	if be.Kind != kind || be.Reason != reason || be.Code != code || be.Provider != enablebanking.ProviderName {
		t.Fatalf("got kind=%s reason=%s code=%s provider=%s, want %s/%s/%s", be.Kind, be.Reason, be.Code, be.Provider, kind, reason, code)
	}
}

// assertClean checks an error carries no credential, identifier or payload.
func assertClean(t *testing.T, err error, s banking.Session) {
	t.Helper()
	msg := fmt.Sprintf("%v | %+v | %#v", err, err, err)
	for e := errors.Unwrap(err); e != nil; e = errors.Unwrap(e) {
		msg += " | " + e.Error()
	}
	forbidden := []string{echoMarker, testAppID, syntheticIBAN1, s.Reference.Reveal(), "uid-", "Bearer", "BEGIN", "SALARY", "127.0.0.1", "http://"}
	for _, a := range s.Accounts {
		forbidden = append(forbidden, a.Handle.Reveal())
	}
	for _, f := range forbidden {
		if f != "" && strings.Contains(msg, f) {
			t.Fatalf("error text contains %q: %s", f, msg)
		}
	}
}
