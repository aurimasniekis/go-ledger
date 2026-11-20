package bankingtest_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
)

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func connect(t *testing.T, p *bankingtest.Provider, state string) banking.Session {
	t.Helper()
	ctx := context.Background()
	insts, err := p.Institutions(ctx, bankingtest.InstitutionCountry)
	if err != nil || len(insts) != 1 {
		t.Fatalf("Institutions = %v, %v", insts, err)
	}
	start, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: insts[0], RedirectURL: "https://ledger.example/callback", State: state, PSUType: banking.PSUPersonal, ValidUntil: t0.Add(90 * 24 * time.Hour)})
	if err != nil || start.AuthorizationURL == "" {
		t.Fatalf("StartConsent = %+v, %v", start, err)
	}
	s, err := p.CompleteConsent(ctx, banking.ConsentCompletion{State: state, Code: bankingtest.AuthorizationCode(state)})
	if err != nil {
		t.Fatalf("CompleteConsent: %v", err)
	}
	return s
}

func externalIDs(accs []banking.Account) []string {
	var out []string
	for _, a := range accs {
		out = append(out, a.ExternalID)
	}
	return out
}

func TestModesDifferInAccountAccess(t *testing.T) {
	c := &clock{t0}
	app := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: c.Now}, bankingtest.Standard())
	dev := bankingtest.New(bankingtest.Options{Mode: banking.ModeDeveloper, Now: c.Now}, bankingtest.Standard())
	if app.Capabilities().AccountAccess() != banking.AccountAccessAll || dev.Capabilities().AccountAccess() != banking.AccountAccessLinkedOnly {
		t.Fatal("capabilities do not reflect the mode")
	}
	if app.Capabilities().Application == dev.Capabilities().Application {
		t.Error("the two modes should be distinct applications")
	}
	appIDs := externalIDs(connect(t, app, "s-app").Accounts)
	devIDs := externalIDs(connect(t, dev, "s-dev").Accounts)
	if !slices.Contains(appIDs, bankingtest.AccountGBP) {
		t.Errorf("app mode hides the unlinked account: %v", appIDs)
	}
	if slices.Contains(devIDs, bankingtest.AccountGBP) || !slices.Contains(devIDs, bankingtest.AccountEUR) {
		t.Errorf("developer mode accounts = %v, want only linked ones", devIDs)
	}
}

func TestDeveloperModeWithNothingLinkedIsEmptyNotError(t *testing.T) {
	data := bankingtest.Standard()
	for i := range data.Accounts {
		data.Accounts[i].Linked = false
	}
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeDeveloper, Now: (&clock{t0}).Now}, data)
	s := connect(t, p, "s")
	accs, err := p.Accounts(context.Background(), s.Reference)
	if err != nil || len(accs) != 0 || accs == nil {
		t.Fatalf("Accounts = %v, %v; want an empty list", accs, err)
	}
}

func allPages(t *testing.T, p *bankingtest.Provider, s banking.Session, acc banking.Account, q banking.TransactionQuery) ([]banking.Transaction, int) {
	t.Helper()
	var out []banking.Transaction
	pages := 0
	for {
		page, err := p.Transactions(context.Background(), s.Reference, acc.Handle, q)
		if err != nil {
			t.Fatalf("Transactions: %v", err)
		}
		pages++
		out = append(out, page.Transactions...)
		if page.Next == "" {
			return out, pages
		}
		q.Continuation = page.Next
	}
}

func TestPagingWithContinuationKeys(t *testing.T) {
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: (&clock{t0}).Now, PageSize: 3, EmptyFirstPage: true}, bankingtest.Standard())
	s := connect(t, p, "s")
	eur := s.Accounts[0]
	txs, pages := allPages(t, p, s, eur, bankingtest.October2026())
	if len(txs) != 8 {
		t.Errorf("got %d transactions, want 8", len(txs))
	}
	if pages != 4 { // empty first page, then 3+3+2
		t.Errorf("got %d pages, want 4", pages)
	}
	// A window filters by date.
	q := bankingtest.October2026()
	q.From = q.To
	few, _ := allPages(t, p, s, eur, q)
	if len(few) != 0 {
		t.Errorf("31 October holds %d transactions in the fixture, want 0 (pending items are dated 29 and 30)", len(few))
	}
}

func TestContinuationKeyIsBoundToSessionAndWindow(t *testing.T) {
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: (&clock{t0}).Now, PageSize: 2}, bankingtest.Standard())
	s := connect(t, p, "s")
	ctx := context.Background()
	q := bankingtest.October2026()
	page, err := p.Transactions(ctx, s.Reference, s.Accounts[0].Handle, q)
	if err != nil || page.Next == "" {
		t.Fatalf("first page: %v, next %q", err, page.Next)
	}
	q.Continuation = page.Next
	q.From = q.From.YearMonth().First()
	other := q
	other.To = q.From
	if _, err := p.Transactions(ctx, s.Reference, s.Accounts[0].Handle, other); !errors.Is(err, banking.ErrPermanent) {
		t.Errorf("a key reused with another window: %v, want permanent", err)
	}
	s2 := connect(t, p, "s2")
	if _, err := p.Transactions(ctx, s2.Reference, s2.Accounts[0].Handle, q); !errors.Is(err, banking.ErrPermanent) {
		t.Errorf("a key reused in another session: %v, want permanent", err)
	}
}

func TestReauthorisationChangesHandlesNotExternalIDs(t *testing.T) {
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: (&clock{t0}).Now}, bankingtest.Standard())
	a := connect(t, p, "first")
	b := connect(t, p, "second")
	if a.Reference.Equal(b.Reference) {
		t.Error("session reference did not change")
	}
	if a.Accounts[0].ExternalID != b.Accounts[0].ExternalID || a.Accounts[0].Handle.Equal(b.Accounts[0].Handle) {
		t.Error("want same external id, new handle")
	}
	// The old session's handle is not valid in the new session.
	if _, err := p.Balances(context.Background(), b.Reference, a.Accounts[0].Handle); !errors.Is(err, banking.ErrPermanent) {
		t.Errorf("old handle in new session: %v", err)
	}
}

func TestConsentExpiryRevocationAndEnd(t *testing.T) {
	c := &clock{t0}
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: c.Now, ConsentValidity: 24 * time.Hour}, bankingtest.Standard())
	ctx := context.Background()
	s := connect(t, p, "s")
	if !s.ValidUntil.Equal(t0.Add(24 * time.Hour)) {
		t.Errorf("ValidUntil = %v", s.ValidUntil)
	}
	c.now = s.ValidUntil
	_, err := p.Accounts(ctx, s.Reference)
	if !errors.Is(err, banking.ErrConsentExpired) || banking.Classify(err) != banking.KindReconnectRequired {
		t.Errorf("expired session: %v", err)
	}
	c.now = t0
	s2 := connect(t, p, "s2")
	p.Revoke()
	if _, err := p.Accounts(ctx, s2.Reference); !errors.Is(err, banking.ErrReconnectRequired) || errors.Is(err, banking.ErrConsentExpired) {
		t.Errorf("revoked session: %v", err)
	}
	s3 := connect(t, p, "s3")
	if err := p.EndSession(ctx, s3.Reference); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Accounts(ctx, s3.Reference); !errors.Is(err, banking.ErrReconnectRequired) {
		t.Errorf("ended session: %v", err)
	}
}

func TestConsentFailures(t *testing.T) {
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp}, bankingtest.Standard())
	ctx := context.Background()
	inst := bankingtest.Standard().Institutions[0]
	if _, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: banking.Institution{ID: "nope", Country: "FI"}, RedirectURL: "https://x.example", State: "s"}); !errors.Is(err, banking.ErrPermanent) {
		t.Errorf("unknown institution: %v", err)
	}
	if _, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://x.example", State: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CompleteConsent(ctx, banking.ConsentCompletion{State: "s", Code: banking.NewSensitive("forged")}); !errors.Is(err, banking.ErrPermanent) {
		t.Errorf("forged code: %v", err)
	}
	// A state is single use.
	if _, err := p.CompleteConsent(ctx, banking.ConsentCompletion{State: "s", Code: bankingtest.AuthorizationCode("s")}); !errors.Is(err, banking.ErrPermanent) {
		t.Errorf("reused state: %v", err)
	}
	if _, err := p.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://x.example", State: "r"}); err != nil {
		t.Fatal(err)
	}
	_, err := p.CompleteConsent(ctx, banking.ConsentCompletion{State: "r", Error: "access_denied"})
	var be *banking.Error
	if !errors.As(err, &be) || be.Code != "consent_refused" {
		t.Errorf("refused consent: %v", err)
	}
}

func TestInjectedFailuresAndContext(t *testing.T) {
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: (&clock{t0}).Now}, bankingtest.Standard())
	s := connect(t, p, "s")
	ctx := context.Background()
	p.FailNext(bankingtest.OpBalances,
		bankingtest.Failure(bankingtest.OpBalances, banking.KindRateLimited, "", "ASPSP_RATE_LIMIT_EXCEEDED", 6*time.Hour),
		bankingtest.Failure(bankingtest.OpBalances, banking.KindRetryable, "", "", 0))
	_, err := p.Balances(ctx, s.Reference, s.Accounts[0].Handle)
	if d, ok := banking.RetryAfter(err); !errors.Is(err, banking.ErrRateLimited) || !ok || d != 6*time.Hour {
		t.Errorf("first call: %v", err)
	}
	if _, err := p.Balances(ctx, s.Reference, s.Accounts[0].Handle); !errors.Is(err, banking.ErrRetryable) {
		t.Errorf("second call: %v", err)
	}
	bals, err := p.Balances(ctx, s.Reference, s.Accounts[0].Handle)
	if err != nil || len(bals) != 2 {
		t.Errorf("third call: %v, %d balances", err, len(bals))
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Accounts(cctx, s.Reference); banking.Classify(err) != banking.KindCanceled {
		t.Errorf("cancelled context: %v", err)
	}
	if calls := p.Calls(); calls[len(calls)-1] != bankingtest.OpAccounts {
		t.Errorf("calls = %v", calls)
	}
}

func TestMutateAndBookPending(t *testing.T) {
	p := bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: (&clock{t0}).Now}, bankingtest.Standard())
	s := connect(t, p, "s")
	p.Mutate(bankingtest.BookPending)
	txs, _ := allPages(t, p, s, s.Accounts[0], bankingtest.October2026())
	for _, tx := range txs {
		if tx.EntryReference == bankingtest.RefPendingThenBooks && (tx.Status != banking.StatusBooked || tx.ProviderStatus != "BOOK") {
			t.Errorf("pending item not booked: %+v", tx.Status)
		}
	}
}

func TestStandardFixtureIsValid(t *testing.T) {
	for _, a := range bankingtest.Standard().Accounts {
		if err := a.Account.Validate(); err != nil {
			t.Error(err)
		}
		for _, b := range a.Balances {
			if err := b.Validate(); err != nil {
				t.Error(err)
			}
		}
		if _, err := banking.KeyTransactions(a.Transactions); err != nil {
			t.Error(err)
		}
	}
}
