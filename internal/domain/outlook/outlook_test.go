package outlook_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx"
	"github.com/aurimasniekis/go-ledger/internal/domain/fx/fxtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/outlook"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// t0 is "now": Monday 2026-11-16 09:00 UTC; today is 2026-11-16.
var t0 = fxtest.T0

func m(t *testing.T, amount, cur string) money.Money {
	t.Helper()
	v, err := money.Parse(amount, cur)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func d(t *testing.T, s string) calendar.Date {
	t.Helper()
	v, err := calendar.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ym(t *testing.T, s string) calendar.YearMonth {
	t.Helper()
	v, err := calendar.ParseYearMonth(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type occReader struct {
	occurrences.Reader
	items []occurrences.Occurrence
}

func (f occReader) List(_ context.Context, fl occurrences.ListFilter) ([]occurrences.Occurrence, error) {
	var out []occurrences.Occurrence
	for _, o := range f.items {
		if o.Month == fl.Month && (fl.After == "" || o.ID > fl.After) {
			out = append(out, o)
		}
	}
	if len(out) > fl.Limit+1 {
		out = out[:fl.Limit+1]
	}
	return out, nil
}

type factStore struct {
	payments.Store
	by map[string]payments.Facts
}

func (f factStore) Facts(_ context.Context, _ []string) (map[string]payments.Facts, error) {
	return f.by, nil
}

type commitmentList []commitments.Commitment

func (c commitmentList) List(_ context.Context, f commitments.ListFilter) ([]commitments.Commitment, error) {
	var out []commitments.Commitment
	for _, x := range c {
		if f.After == "" || x.ID > f.After {
			out = append(out, x)
		}
	}
	if len(out) > f.Limit+1 {
		out = out[:f.Limit+1]
	}
	return out, nil
}

type banks struct {
	conns    []banking.Connection
	accounts map[banking.ConnectionID][]banking.StoredAccount
	balances map[banking.AccountID][]banking.StoredBalance
	sync     map[banking.AccountID]banking.AccountSync
}

func (b *banks) Connections(context.Context) ([]banking.Connection, error) { return b.conns, nil }
func (b *banks) Accounts(_ context.Context, id banking.ConnectionID) ([]banking.StoredAccount, error) {
	return b.accounts[id], nil
}
func (b *banks) Balances(_ context.Context, id banking.AccountID) ([]banking.StoredBalance, error) {
	return b.balances[id], nil
}
func (b *banks) AccountSync(_ context.Context, id banking.AccountID) (banking.AccountSync, error) {
	return b.sync[id], nil
}

func bal(t *testing.T, id string, typ banking.BalanceType, amount, cur string, at time.Time) banking.StoredBalance {
	return banking.StoredBalance{ID: id, Balance: banking.Balance{Type: typ, ProviderType: "X", Amount: m(t, amount, cur)}, ObtainedAt: at}
}

type env struct {
	occ   []occurrences.Occurrence
	facts map[string]payments.Facts
	cs    commitmentList
	banks *banks
	cache *fxtest.Cache
}

// newEnv is the reference data set.
//
//	EUR: 100.00 due 09-05 unpaid (overdue), 100.00 due 10-05 with 40.00 paid
//	     (60.00 overdue), 100.00 due 11-05 paid, 100.00 due 11-25 (upcoming),
//	     50.00 due 12-03 (outside the default window);
//	USD: 20.00 due 11-20 (upcoming); GBP: 7.50 due 11-10 (overdue).
//
// Balances: EUR account 1200.50 booked (newest fetch; an older fetch and an
// available figure are ignored), USD account 10.00 expected but stale, an EUR
// account no longer reported, an account without balances, and a GBP
// account with only an "other" balance on a connection that needs
// reconnecting.
func newEnv(t *testing.T) *env {
	occ := func(id, cid, due, amount, cur string) occurrences.Occurrence {
		dd := d(t, due)
		return occurrences.Occurrence{ID: id, CommitmentID: cid, Month: dd.YearMonth(), DueDate: dd, Amount: m(t, amount, cur), Version: 1, UpdatedAt: t0.Add(-time.Hour)}
	}
	manual := func(id, occID, amount, cur string) payments.Evidence {
		return payments.Evidence{Kind: payments.EvidenceManual, ID: id, OccurrenceID: occID, Amount: m(t, amount, cur), Date: d(t, "2026-11-01")}
	}
	e := &env{
		occ: []occurrences.Occurrence{
			occ("o1", "ceur", "2026-09-05", "100.00", "EUR"),
			occ("o2", "ceur", "2026-10-05", "100.00", "EUR"),
			occ("o3", "ceur", "2026-11-05", "100.00", "EUR"),
			occ("o4", "ceur", "2026-11-25", "100.00", "EUR"),
			occ("o5", "cusd", "2026-11-20", "20.00", "USD"),
			occ("o6", "cgbp", "2026-11-10", "7.50", "GBP"),
			occ("o7", "ceur", "2026-12-03", "50.00", "EUR"),
		},
		facts: map[string]payments.Facts{
			"o2": {Evidence: []payments.Evidence{manual("p2", "o2", "40.00", "EUR")}},
			"o3": {Evidence: []payments.Evidence{manual("p3", "o3", "100.00", "EUR")}},
		},
		cs: commitmentList{
			{ID: "ceur", Fields: commitments.Fields{StartDate: d(t, "2026-09-01")}},
			{ID: "cgbp", Fields: commitments.Fields{StartDate: d(t, "2026-11-01")}},
			{ID: "cusd", Fields: commitments.Fields{StartDate: d(t, "2026-10-01")}},
		},
		cache: fxtest.NewCache(),
	}
	ok := banking.AccountSync{LastSuccessAt: t0.Add(-time.Hour), LastAttemptAt: t0.Add(-time.Hour)}
	e.banks = &banks{
		conns: []banking.Connection{
			{ID: "c1", Status: banking.ConnectionActive, Institution: banking.Institution{Name: "Bank One"}},
			{ID: "c2", Status: banking.ConnectionReconnectRequired, Institution: banking.Institution{Name: "Bank Two"}},
		},
		accounts: map[banking.ConnectionID][]banking.StoredAccount{
			"c1": {
				{ID: "a1", Account: banking.Account{Name: "Main", Currency: money.MustParseCurrency("EUR")}},
				{ID: "a2", Account: banking.Account{Name: "Dollars", Currency: money.MustParseCurrency("USD")}},
				{ID: "a3", Account: banking.Account{Name: "Closed", Currency: money.MustParseCurrency("EUR")}, NoLongerReportedAt: t0.Add(-24 * time.Hour)},
				{ID: "a4", Account: banking.Account{Name: "Empty", Currency: money.MustParseCurrency("EUR")}},
			},
			"c2": {{ID: "a5", Account: banking.Account{Name: "Pounds", Currency: money.MustParseCurrency("GBP")}}},
		},
		balances: map[banking.AccountID][]banking.StoredBalance{
			"a1": {
				bal(t, "b1", banking.BalanceBooked, "1000.00", "EUR", t0.Add(-48*time.Hour)),
				bal(t, "b2", banking.BalanceAvailable, "1500.00", "EUR", t0.Add(-time.Hour)),
				bal(t, "b3", banking.BalanceBooked, "1200.50", "EUR", t0.Add(-time.Hour)),
			},
			"a2": {bal(t, "b4", banking.BalanceExpected, "10.00", "USD", t0.Add(-72*time.Hour))},
			"a3": {bal(t, "b5", banking.BalanceBooked, "999.00", "EUR", t0.Add(-48*time.Hour))},
			"a5": {bal(t, "b6", banking.BalanceOther, "5.00", "GBP", t0.Add(-time.Hour))},
		},
		sync: map[banking.AccountID]banking.AccountSync{
			"a1": ok,
			"a2": {LastSuccessAt: t0.Add(-72 * time.Hour), LastAttemptAt: t0.Add(-72 * time.Hour)},
			"a3": ok, "a4": ok, "a5": ok,
		},
	}
	return e
}

// seedRate caches a latest rate retrieved at "at".
func (e *env) seedRate(t *testing.T, pair, value, date string, at time.Time) {
	t.Helper()
	r := fx.NewRate(fx.Rate{Provider: "frankfurter", Source: "ECB", Pair: fxtest.Pair(pair[:3], pair[4:]), Value: money.MustParseDecimal(value), Date: d(t, date), RetrievedAt: at,
		ConfirmedThrough: d(t, at.UTC().Format("2006-01-02"))})
	if _, err := e.cache.Save(context.Background(), []fx.Rate{r}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) service(t *testing.T, withRates bool) outlook.Service {
	t.Helper()
	view := monthview.Service{Occurrences: occReader{items: e.occ}, Payments: factStore{by: e.facts}}
	s := outlook.Service{Months: view, Commitments: e.cs, Banks: e.banks, Now: func() time.Time { return t0 },
		Today: func() (calendar.Date, error) { return d(t, "2026-11-16"), nil }}
	if withRates {
		svc, err := fx.NewService(e.cache, nil, fx.Options{Now: func() time.Time { return t0 }, ProviderName: "frankfurter", Source: "ECB"})
		if err != nil {
			t.Fatal(err)
		}
		s.Rates = svc
	}
	return s
}

func lineOf(t *testing.T, o outlook.Outlook, cur string) outlook.CurrencyLine {
	t.Helper()
	for _, l := range o.Currencies {
		if l.Currency.Code() == cur {
			return l
		}
	}
	t.Fatalf("no %s line in %+v", cur, o.Currencies)
	return outlook.CurrencyLine{}
}

func str(p *money.Money) string {
	if p == nil {
		return "<nil>"
	}
	return p.String()
}

func TestPerCurrencyLinesAreExactAndNeverMixed(t *testing.T) {
	e := newEnv(t)
	o, err := e.service(t, false).Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.From != ym(t, "2026-09") || o.Through != ym(t, "2026-11") || o.Truncated || o.AsOf != d(t, "2026-11-16") {
		t.Fatalf("window %s..%s truncated %v as of %s", o.From, o.Through, o.Truncated, o.AsOf)
	}
	if len(o.Currencies) != 3 {
		t.Fatalf("currencies %+v", o.Currencies)
	}
	if got := []string{o.Currencies[0].Currency.Code(), o.Currencies[1].Currency.Code(), o.Currencies[2].Currency.Code()}; got[0] != "EUR" || got[1] != "GBP" || got[2] != "USD" {
		t.Fatalf("order %v", got)
	}
	eur := lineOf(t, o, "EUR")
	if eur.StillDue.String() != "260.00 EUR" || eur.Overdue.String() != "160.00 EUR" || eur.Upcoming.String() != "100.00 EUR" || eur.OverdueCount != 2 {
		t.Errorf("EUR commitments: still due %s overdue %s upcoming %s (%d overdue)", eur.StillDue, eur.Overdue, eur.Upcoming, eur.OverdueCount)
	}
	if str(eur.Balance) != "1200.50 EUR" || str(eur.BalanceLessStillDue) != "940.50 EUR" || eur.Accounts != 1 || eur.StaleAccounts != 0 {
		t.Errorf("EUR balance %s less %s accounts %d stale %d", str(eur.Balance), str(eur.BalanceLessStillDue), eur.Accounts, eur.StaleAccounts)
	}
	usd := lineOf(t, o, "USD")
	if usd.StillDue.String() != "20.00 USD" || usd.Overdue.String() != "0.00 USD" || str(usd.Balance) != "10.00 USD" || str(usd.BalanceLessStillDue) != "-10.00 USD" || usd.StaleAccounts != 1 {
		t.Errorf("USD line %+v", usd)
	}
	gbp := lineOf(t, o, "GBP")
	if gbp.StillDue.String() != "7.50 GBP" || gbp.Overdue.String() != "7.50 GBP" || gbp.Balance != nil || gbp.BalanceLessStillDue != nil {
		t.Errorf("GBP: an account whose only balance is of an unknown type must not count: %+v", gbp)
	}
	if o.Converted != nil {
		t.Error("a converted aggregate was built without a reporting currency")
	}
}

func TestAccountsShowWhyTheyAreNotCountedAndHowFreshTheyAre(t *testing.T) {
	e := newEnv(t)
	o, err := e.service(t, false).Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	if !o.BalancesShown || len(o.Accounts) != 5 {
		t.Fatalf("accounts %+v", o.Accounts)
	}
	want := map[banking.AccountID]struct {
		nc    outlook.NotCounted
		state banking.FreshnessState
		bal   string
	}{
		"a1": {"", banking.FreshnessFresh, "1200.50 EUR"},
		"a2": {"", banking.FreshnessStale, "10.00 USD"},
		"a3": {outlook.NotCountedNoLongerReported, banking.FreshnessFresh, "999.00 EUR"},
		"a4": {outlook.NotCountedNoBalance, banking.FreshnessFresh, ""},
		"a5": {outlook.NotCountedOtherType, banking.FreshnessReconnectRequired, ""},
	}
	for _, a := range o.Accounts {
		w := want[a.ID]
		got := ""
		if len(a.Balances) > 0 {
			got = a.Balances[0].Amount.String()
		}
		if a.NotCounted != w.nc || a.Freshness.State != w.state || got != w.bal {
			t.Errorf("%s: not counted %q state %s balance %q, want %q %s %q", a.ID, a.NotCounted, a.Freshness.State, got, w.nc, w.state, w.bal)
		}
	}
	if o.Accounts[0].Balances[0].Type != banking.BalanceBooked || !o.Accounts[0].Balances[0].ObtainedAt.Equal(t0.Add(-time.Hour)) {
		t.Errorf("a1 counted %+v; want the booked figure of the newest fetch", o.Accounts[0].Balances[0])
	}
}

func TestConvertedAggregateIsSecondaryAndFlagsStaleAndMissingRates(t *testing.T) {
	e := newEnv(t)
	e.seedRate(t, "USD/EUR", "0.9200", "2026-11-13", t0.Add(-2*time.Hour))
	e.seedRate(t, "GBP/EUR", "1.1500", "2026-11-09", t0.Add(-6*24*time.Hour))
	o, err := e.service(t, true).Build(context.Background(), outlook.Request{Balances: true, Currency: money.MustParseCurrency("EUR")})
	if err != nil {
		t.Fatal(err)
	}
	c := o.Converted
	if c == nil {
		t.Fatal("no converted aggregate")
	}
	if c.StillDue.String() != "278.40 EUR" || c.Overdue.String() != "160.00 EUR" || str(c.Balance) != "1209.70 EUR" || str(c.BalanceLessStillDue) != "931.30 EUR" {
		t.Errorf("converted still due %s overdue %s balance %s less %s", c.StillDue, c.Overdue, str(c.Balance), str(c.BalanceLessStillDue))
	}
	if c.Complete() || len(c.Excluded) != 1 || c.Excluded[0].Currency.Code() != "GBP" || c.Excluded[0].Status != fx.StatusStale ||
		c.Excluded[0].Reason != string(fx.ReasonNotRetrievedRecently) || c.Excluded[0].Rate == nil {
		t.Errorf("excluded %+v", c.Excluded)
	}
	if len(c.Included) != 2 || c.Included[0].Currency.Code() != "EUR" || c.Included[0].Rate != nil || c.Included[1].Currency.Code() != "USD" ||
		c.Included[1].Rate == nil || c.Included[1].Rate.Value.String() != "0.9200" || c.Included[1].Rate.Date != d(t, "2026-11-13") || c.Included[1].Age != 2*time.Hour {
		t.Errorf("included %+v", c.Included)
	}
	if c.BalanceAccounts != 2 || c.StaleBalanceAccounts != 1 {
		t.Errorf("balance accounts %d, stale %d", c.BalanceAccounts, c.StaleBalanceAccounts)
	}
	// The per-currency lines are untouched by the conversion.
	if lineOf(t, o, "USD").StillDue.String() != "20.00 USD" {
		t.Error("conversion changed a per-currency line")
	}
}

func TestWithoutARateServiceOnlyTheReportingCurrencyIsCounted(t *testing.T) {
	e := newEnv(t)
	o, err := e.service(t, false).Build(context.Background(), outlook.Request{Balances: true, Currency: money.MustParseCurrency("EUR")})
	if err != nil {
		t.Fatal(err)
	}
	c := o.Converted
	if c.StillDue.String() != "260.00 EUR" || str(c.Balance) != "1200.50 EUR" || len(c.Included) != 1 || len(c.Excluded) != 2 {
		t.Fatalf("converted %+v", c)
	}
	for _, x := range c.Excluded {
		if x.Status != fx.StatusMissing || x.Reason != string(fx.ReasonNoProvider) || x.Rate != nil {
			t.Errorf("excluded %+v", x)
		}
	}
}

func TestMissingRateInACachedServiceExcludesTheCurrency(t *testing.T) {
	e := newEnv(t)
	o, err := e.service(t, true).Build(context.Background(), outlook.Request{Balances: true, Currency: money.MustParseCurrency("USD")})
	if err != nil {
		t.Fatal(err)
	}
	c := o.Converted
	// Only USD itself converts: EUR and GBP have no cached rate to USD.
	if c.StillDue.String() != "20.00 USD" || str(c.Balance) != "10.00 USD" || len(c.Excluded) != 2 {
		t.Fatalf("converted %+v", c)
	}
	for _, x := range c.Excluded {
		if x.Status != fx.StatusMissing {
			t.Errorf("excluded %+v", x)
		}
	}
}

func TestBalancesHiddenFromCallersWithoutTheGrant(t *testing.T) {
	e := newEnv(t)
	o, err := e.service(t, false).Build(context.Background(), outlook.Request{Currency: money.MustParseCurrency("EUR")})
	if err != nil {
		t.Fatal(err)
	}
	if o.BalancesShown || len(o.Accounts) != 0 || len(o.Currencies) != 3 {
		t.Fatalf("balances shown %v accounts %d currencies %d", o.BalancesShown, len(o.Accounts), len(o.Currencies))
	}
	for _, l := range o.Currencies {
		if l.Balance != nil || l.BalanceLessStillDue != nil || l.Accounts != 0 {
			t.Errorf("%s has balance data: %+v", l.Currency, l)
		}
	}
	if o.Converted.Balance != nil || o.Converted.BalanceLessStillDue != nil {
		t.Error("converted balance shown without the grant")
	}
}

func TestThroughExtendsTheWindowWithinTheHorizon(t *testing.T) {
	e := newEnv(t)
	s := e.service(t, false)
	o, err := s.Build(context.Background(), outlook.Request{Through: ym(t, "2026-12")})
	if err != nil {
		t.Fatal(err)
	}
	if eur := lineOf(t, o, "EUR"); eur.StillDue.String() != "310.00 EUR" || eur.Upcoming.String() != "150.00 EUR" {
		t.Errorf("EUR through December: %+v", eur)
	}
	if _, err := s.Build(context.Background(), outlook.Request{Through: ym(t, "2031-12")}); !errors.Is(err, outlook.ErrBeyondHorizon) {
		t.Errorf("beyond the horizon: %v", err)
	}
}

func TestReimportedSnapshotsAreCountedOnce(t *testing.T) {
	e := newEnv(t)
	s := e.service(t, false)
	before, err := s.Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	// A second refresh reports the same figures again: balances are
	// append-only snapshots, so a1 now has two identical newest fetches.
	e.banks.balances["a1"] = append(e.banks.balances["a1"],
		bal(t, "b7", banking.BalanceBooked, "1200.50", "EUR", t0),
		bal(t, "b8", banking.BalanceAvailable, "1500.00", "EUR", t0))
	after, err := s.Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	if str(lineOf(t, before, "EUR").Balance) != str(lineOf(t, after, "EUR").Balance) || str(lineOf(t, after, "EUR").Balance) != "1200.50 EUR" {
		t.Errorf("EUR balance %s then %s", str(lineOf(t, before, "EUR").Balance), str(lineOf(t, after, "EUR").Balance))
	}
}

func TestEmptyAndTruncatedWindows(t *testing.T) {
	today := func() (calendar.Date, error) { return d(t, "2026-11-16"), nil }
	view := monthview.Service{Occurrences: occReader{}, Payments: factStore{}}
	o, err := outlook.Service{Months: view, Commitments: commitmentList{}, Today: today}.Build(context.Background(), outlook.Request{Balances: true, Currency: money.MustParseCurrency("EUR")})
	if err != nil {
		t.Fatal(err)
	}
	if !o.From.IsZero() || o.Through != ym(t, "2026-11") || len(o.Currencies) != 0 || o.BalancesShown || o.Converted == nil || o.Converted.StillDue.String() != "0.00 EUR" || !o.Converted.Complete() {
		t.Fatalf("empty outlook %+v", o)
	}
	old := commitmentList{{ID: "c", Fields: commitments.Fields{StartDate: d(t, "1990-01-01")}}}
	o, err = outlook.Service{Months: view, Commitments: old, Today: today}.Build(context.Background(), outlook.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated || o.From != ym(t, "2006-12") || o.Through != ym(t, "2026-11") {
		t.Errorf("truncated window %s..%s %v", o.From, o.Through, o.Truncated)
	}
}

func TestLatestPrefersTheNewestFetchThenBooked(t *testing.T) {
	at := t0
	got := outlook.Latest([]banking.StoredBalance{
		bal(t, "1", banking.BalanceBooked, "1.00", "EUR", at.Add(-time.Hour)),
		bal(t, "2", banking.BalanceAvailable, "2.00", "EUR", at),
		bal(t, "3", banking.BalanceExpected, "3.00", "EUR", at),
		bal(t, "4", banking.BalanceOther, "4.00", "USD", at),
		bal(t, "5", banking.BalanceAvailable, "5.00", "CHF", at),
	})
	if len(got) != 2 || got[0].Amount.String() != "5.00 CHF" || got[1].Amount.String() != "3.00 EUR" {
		t.Fatalf("latest %+v", got)
	}
}

func TestLatestIsDeterministicAmongBookedTypesWhateverTheIDs(t *testing.T) {
	at := t0
	mk := func(id, code, amount string, ref string) banking.StoredBalance {
		b := bal(t, id, banking.BalanceBooked, amount, "EUR", at)
		b.ProviderType = code
		if ref != "" {
			b.ReferenceDate = d(t, ref)
		}
		return b
	}
	for i := 0; i < 200; i++ {
		id := func() string { return resource.NewID() }
		in := []banking.StoredBalance{
			mk(id(), "OPBD", "100.00", "2026-11-15"),
			mk(id(), "PRCD", "90.00", "2026-11-14"),
			mk(id(), "ITBD", "250.00", "2026-11-16"),
			mk(id(), "CLBD", "260.00", "2026-11-15"),
		}
		rand.Shuffle(len(in), func(a, b int) { in[a], in[b] = in[b], in[a] })
		if got := outlook.Latest(in); len(got) != 1 || got[0].Amount.String() != "250.00 EUR" {
			t.Fatalf("round %d: counted %+v, want ITBD 250.00", i, got)
		}
	}
	// The same code twice: the newest reference date wins, never the ID.
	for i := 0; i < 50; i++ {
		in := []banking.StoredBalance{mk(resource.NewID(), "CLBD", "10.00", "2026-11-14"), mk(resource.NewID(), "CLBD", "20.00", "2026-11-15")}
		rand.Shuffle(len(in), func(a, b int) { in[a], in[b] = in[b], in[a] })
		if got := outlook.Latest(in); got[0].Amount.String() != "20.00 EUR" {
			t.Fatalf("round %d: counted %+v, want the newest reference date", i, got)
		}
	}
	// CLBD over OPBD over PRCD when ITBD is absent.
	in := []banking.StoredBalance{mk("z", "PRCD", "1.00", ""), mk("y", "OPBD", "2.00", ""), mk("x", "CLBD", "3.00", "")}
	if got := outlook.Latest(in); got[0].Amount.String() != "3.00 EUR" {
		t.Errorf("counted %+v, want CLBD", got)
	}
}

func TestAPastThroughNeverDropsOverdue(t *testing.T) {
	e := newEnv(t)
	s := e.service(t, false)
	base, err := s.Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	past, err := s.Build(context.Background(), outlook.Request{Balances: true, Through: ym(t, "2026-09")})
	if err != nil {
		t.Fatal(err)
	}
	if len(past.Currencies) != len(base.Currencies) || past.Through != ym(t, "2026-11") {
		t.Fatalf("past through changed the window: %s, %d lines", past.Through, len(past.Currencies))
	}
	for i, l := range base.Currencies {
		p := past.Currencies[i]
		if p.StillDue.String() != l.StillDue.String() || p.Overdue.String() != l.Overdue.String() || str(p.BalanceLessStillDue) != str(l.BalanceLessStillDue) {
			t.Errorf("%s: past through %+v, current %+v", l.Currency.Code(), p, l)
		}
	}
	if eur := lineOf(t, past, "EUR"); eur.Overdue.String() != "160.00 EUR" || lineOf(t, past, "GBP").Overdue.String() != "7.50 GBP" {
		t.Errorf("overdue dropped: %+v", eur)
	}
}

func TestTheBackBoundIsAnchoredToTodayNotThrough(t *testing.T) {
	today := func() (calendar.Date, error) { return d(t, "2026-11-16"), nil }
	view := monthview.Service{Occurrences: occReader{}, Payments: factStore{}}
	old := commitmentList{{ID: "c", Fields: commitments.Fields{StartDate: d(t, "1990-01-01")}}}
	o, err := outlook.Service{Months: view, Commitments: old, Today: today}.Build(context.Background(), outlook.Request{Through: ym(t, "2031-11")})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated || o.From != ym(t, "2006-12") || o.Through != ym(t, "2031-11") {
		t.Errorf("window %s..%s truncated %v", o.From, o.Through, o.Truncated)
	}
}

// driftRates returns a different rate on every call, as when a refresh lands
// between two conversions; the reporting currency itself is an identity.
type driftRates struct{ n int }

func (r *driftRates) Convert(_ context.Context, from money.Money, to money.Currency) (fx.Conversion, error) {
	if from.Currency() == to {
		return fx.Conversion{Status: fx.StatusOK, From: from, To: to, Value: from, Identity: true}, nil
	}
	r.n++
	rate := fx.Rate{Provider: "p", Source: "s", Pair: fxtest.Pair(from.Currency().Code(), to.Code()), Value: money.MustParseDecimal(fmt.Sprintf("1.%d0", r.n)), Date: calendar.Date{}}
	val, err := money.New(from.Amount().Mul(rate.Value).RoundHalfEven(2), to)
	if err != nil {
		return fx.Conversion{}, err
	}
	return fx.Conversion{Status: fx.StatusOK, From: from, To: to, Value: val, Rate: &rate}, nil
}

func TestEveryFigureOfACurrencyUsesTheSingleReportedRate(t *testing.T) {
	e := newEnv(t)
	s := e.service(t, false)
	s.Rates = &driftRates{}
	o, err := s.Build(context.Background(), outlook.Request{Balances: true, Currency: money.MustParseCurrency("EUR")})
	if err != nil {
		t.Fatal(err)
	}
	var use outlook.RateUse
	for _, u := range o.Converted.Included {
		if u.Currency.Code() == "USD" {
			use = u
		}
	}
	if use.Rate == nil {
		t.Fatalf("no USD rate in %+v", o.Converted.Included)
	}
	// The USD balance (10.00 USD) and still-due (20.00 USD) are both
	// converted at the reported rate; EUR counts as is.
	eur := lineOf(t, o, "EUR")
	gbp := lineOf(t, o, "GBP")
	var gbpUse outlook.RateUse
	for _, u := range o.Converted.Included {
		if u.Currency.Code() == "GBP" {
			gbpUse = u
		}
	}
	conv := func(a money.Money, rate money.Decimal) money.Decimal { return a.Amount().Mul(rate).RoundHalfEven(2) }
	eurCur := money.MustParseCurrency("EUR")
	wantBalance, _ := money.New(eur.Balance.Amount().Add(conv(m(t, "10.00", "USD"), use.Rate.Value)), eurCur)
	if str(o.Converted.Balance) != wantBalance.String() {
		t.Errorf("balance %s, want %s (10.00 USD at the reported %s)", str(o.Converted.Balance), wantBalance, use.Rate.Value)
	}
	wantDue := eur.StillDue.Amount().Add(conv(m(t, "20.00", "USD"), use.Rate.Value)).Add(conv(gbp.StillDue, gbpUse.Rate.Value))
	if o.Converted.StillDue.Amount().String() != wantDue.String() {
		t.Errorf("still due %s, want %s", o.Converted.StillDue, wantDue)
	}
}

func TestOverdueMonthsListWhereTheOverdueComes(t *testing.T) {
	e := newEnv(t)
	o, err := e.service(t, false).Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	eur := lineOf(t, o, "EUR")
	if len(eur.OverdueMonths) != 2 || eur.OverdueMonths[0].Month != ym(t, "2026-09") || eur.OverdueMonths[0].Amount.String() != "100.00 EUR" ||
		eur.OverdueMonths[1].Month != ym(t, "2026-10") || eur.OverdueMonths[1].Amount.String() != "60.00 EUR" || eur.OverdueMonths[1].Count != 1 {
		t.Errorf("EUR overdue months %+v", eur.OverdueMonths)
	}
	if usd := lineOf(t, o, "USD"); len(usd.OverdueMonths) != 0 {
		t.Errorf("USD has no overdue, got %+v", usd.OverdueMonths)
	}
	var n int
	for _, m := range eur.OverdueMonths {
		n += m.Count
	}
	if n != eur.OverdueCount {
		t.Errorf("months count %d, line count %d", n, eur.OverdueCount)
	}
}

func TestPaymentsAfterTheBalanceDateAreACaveatNeverNettedIn(t *testing.T) {
	e := newEnv(t)
	// The EUR balance is for 2026-11-01. Evidence dated that day is in it;
	// two payments dated later are not. A USD payment after the USD balance
	// is counted only in USD.
	for i, b := range e.banks.balances["a1"] {
		if b.ID == "b3" {
			e.banks.balances["a1"][i].ReferenceDate = d(t, "2026-11-01")
		}
	}
	e.facts["o4"] = payments.Facts{Evidence: []payments.Evidence{
		{Kind: payments.EvidenceManual, ID: "p4", OccurrenceID: "o4", Amount: m(t, "25.00", "EUR"), Date: d(t, "2026-11-03")},
		{Kind: payments.EvidenceBankTransaction, ID: "p5", OccurrenceID: "o4", Amount: m(t, "10.00", "EUR"), Date: d(t, "2026-11-04")},
		{Kind: payments.EvidenceManual, ID: "p6", OccurrenceID: "o4", Amount: m(t, "9.00", "USD"), Date: d(t, "2026-11-04")},
	}}
	e.facts["o5"] = payments.Facts{Evidence: []payments.Evidence{{Kind: payments.EvidenceManual, ID: "p7", OccurrenceID: "o5", Amount: m(t, "5.00", "USD"), Date: d(t, "2026-11-14")}}}
	o, err := e.service(t, false).Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	eur := lineOf(t, o, "EUR")
	if eur.BalanceAsOf != d(t, "2026-11-01") || eur.PaymentsAfterBalance == nil || eur.PaymentsAfterBalance.Count != 2 || eur.PaymentsAfterBalance.Amount.String() != "35.00 EUR" {
		t.Errorf("EUR caveat: as of %s, %+v", eur.BalanceAsOf, eur.PaymentsAfterBalance)
	}
	if str(eur.Balance) != "1200.50 EUR" {
		t.Errorf("the balance was adjusted: %s", str(eur.Balance))
	}
	// USD has no reference date: the date it was obtained (three days before t0) stands in, and only the USD payment after it counts: the USD evidence on a EUR occurrence is never converted, so it is in no line.
	usd := lineOf(t, o, "USD")
	if usd.BalanceAsOf.IsZero() || usd.PaymentsAfterBalance == nil || usd.PaymentsAfterBalance.Amount.String() != "5.00 USD" || usd.PaymentsAfterBalance.Count != 1 {
		t.Errorf("USD caveat: as of %s, %+v", usd.BalanceAsOf, usd.PaymentsAfterBalance)
	}
	// A line without a balance has no caveat.
	if gbp := lineOf(t, o, "GBP"); gbp.PaymentsAfterBalance != nil || !gbp.BalanceAsOf.IsZero() {
		t.Errorf("GBP has no counted balance: %+v", gbp)
	}
}

// A balance with no reference date is dated by the day it was obtained in the
// reporting zone, not in UTC; a payment dated on that day is not after it.
func TestBalanceDateWithoutAReferenceUsesTheReportingZone(t *testing.T) {
	e := newEnv(t)
	// The newest EUR fetch (b3) was obtained 21:00 on 2026-11-01 in Honolulu (UTC-10): 07:00 UTC on 2026-11-02.
	// The older fetch (b1) is before it, and no balance has a reference date.
	for i, b := range e.banks.balances["a1"] {
		e.banks.balances["a1"][i].ReferenceDate = calendar.Date{}
		switch b.ID {
		case "b1":
			e.banks.balances["a1"][i].ObtainedAt = time.Date(2026, 10, 31, 7, 0, 0, 0, time.UTC)
		case "b2", "b3":
			e.banks.balances["a1"][i].ObtainedAt = time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
		}
	}
	e.facts = map[string]payments.Facts{"o4": {Evidence: []payments.Evidence{
		{Kind: payments.EvidenceManual, ID: "p4", OccurrenceID: "o4", Amount: m(t, "10.00", "EUR"), Date: d(t, "2026-11-01")},
		{Kind: payments.EvidenceManual, ID: "p5", OccurrenceID: "o4", Amount: m(t, "25.00", "EUR"), Date: d(t, "2026-11-02")},
	}}}
	s := e.service(t, false)
	s.Zone = calendar.MustLoadZone("Pacific/Honolulu")
	o, err := s.Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	eur := lineOf(t, o, "EUR")
	if eur.BalanceAsOf != d(t, "2026-11-01") {
		t.Errorf("balance date: got %s, want 2026-11-01 (the day it was obtained in the reporting zone)", eur.BalanceAsOf)
	}
	// Only the payment strictly after the balance date is a caveat.
	if eur.PaymentsAfterBalance == nil || eur.PaymentsAfterBalance.Count != 1 || eur.PaymentsAfterBalance.Amount.String() != "25.00 EUR" {
		t.Errorf("caveat: %+v", eur.PaymentsAfterBalance)
	}
	// The zero zone means UTC: the same fetch is then dated 2026-11-02.
	o, err = e.service(t, false).Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := lineOf(t, o, "EUR").BalanceAsOf; got != d(t, "2026-11-02") {
		t.Errorf("UTC default: balance date %s, want 2026-11-02", got)
	}
}

func TestNotRecordedOccurrencesAreCountedNotSummed(t *testing.T) {
	e := newEnv(t)
	// o1 fell due before its commitment was created: it owes nothing here.
	e.facts["o1"] = payments.Facts{CommitmentCreatedOn: d(t, "2026-09-10")}
	o, err := e.service(t, false).Build(context.Background(), outlook.Request{Balances: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.NotRecordedCount != 1 {
		t.Errorf("not recorded %d, want 1", o.NotRecordedCount)
	}
	if eur := lineOf(t, o, "EUR"); eur.Overdue.String() != "60.00 EUR" || len(eur.OverdueMonths) != 1 {
		t.Errorf("EUR %+v", eur)
	}
}

func TestBankStaleAfterIsReportedWithBalances(t *testing.T) {
	e := newEnv(t)
	s := e.service(t, false)
	o, err := s.Build(context.Background(), outlook.Request{Balances: true})
	if err != nil || o.BankStaleAfter != banking.DefaultStaleAfter {
		t.Fatalf("default: %v %v", o.BankStaleAfter, err)
	}
	s.BankStaleAfter = 2 * time.Hour
	o, err = s.Build(context.Background(), outlook.Request{Balances: true})
	if err != nil || o.BankStaleAfter != 2*time.Hour {
		t.Fatalf("configured: %v %v", o.BankStaleAfter, err)
	}
	// The 1 h old EUR refresh is fresh within 2 h but the 72 h old USD one is not.
	if lineOf(t, o, "EUR").StaleAccounts != 0 || lineOf(t, o, "USD").StaleAccounts != 1 {
		t.Errorf("stale accounts with a 2 h threshold: %+v", o.Currencies)
	}
	if o, _ = s.Build(context.Background(), outlook.Request{}); o.BankStaleAfter != 0 {
		t.Errorf("without balances the threshold is not reported: %v", o.BankStaleAfter)
	}
}
