package matches_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
	paymentstore "github.com/aurimasniekis/go-ledger/internal/store/payments"
)

type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) WriteAudit(_ context.Context, tx *sql.Tx, e audit.Event) error {
	if tx == nil {
		return errors.New("no transaction")
	}
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) actions(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if strings.HasPrefix(string(e.Action), prefix) {
			out = append(out, string(e.Action)+" from="+e.Details["from"])
		}
	}
	return out
}

var alice = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	path   string
	db     *store.DB
	clock  *clock
	facts  *bankfacts.Store
	cs     *commitmentstore.Store
	ps     *paymentstore.Store
	ms     *matchstore.Store
	rec    *recorder
	p      *bankingtest.Provider
	engine *bankimport.Engine
	conn   banking.Connection
}

func open(t *testing.T, path string, c *clock) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := &recorder{}
	f := &fixture{t: t, ctx: ctx, path: path, db: db, clock: c, rec: rec, facts: bankfacts.New(db, nil),
		cs: commitmentstore.New(db, rec, func() time.Time { return t0.AddDate(0, -3, 0) }, nil), ps: paymentstore.New(db, rec, c.Now, nil), ms: matchstore.New(db, rec, c.Now, nil)}
	f.engine = bankimport.New(f.facts, bankimport.Options{Now: c.Now, Sleep: func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d }})
	return f
}

// newFixture opens a database and imports the fake provider's standard data
// (October 2026) through the real import engine.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, nil)
}

// newFixtureWith is [newFixture] with extra changes to the fake bank's data,
// applied before the first import (later refreshes only see a window of
// recent days).
func newFixtureWith(t *testing.T, extra func(*bankingtest.Dataset)) *fixture {
	t.Helper()
	f := open(t, filepath.Join(t.TempDir(), "ledger.db"), &clock{now: t0})
	data := bankingtest.Standard()
	if extra != nil {
		extra(&data)
	}
	f.p = bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: f.clock.Now, PageSize: 3}, data)
	inst := bankingtest.Standard().Institutions[0]
	if _, err := f.p.StartConsent(f.ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: "s", PSUType: banking.PSUPersonal}); err != nil {
		t.Fatal(err)
	}
	conn, err := f.facts.CreateConnection(f.ctx, banking.NewConnection{Capabilities: f.p.Capabilities(), Institution: inst, ConsentState: "s"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := f.p.CompleteConsent(f.ctx, banking.ConsentCompletion{State: "s", Code: bankingtest.AuthorizationCode("s")})
	if err != nil {
		t.Fatal(err)
	}
	if f.conn, err = f.facts.ActivateConnection(f.ctx, conn.ID, sess, t0); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	return f
}

func (f *fixture) refresh() {
	f.t.Helper()
	res, err := f.engine.Refresh(f.ctx, f.conn.ID, f.p)
	if err != nil || res.Run.Outcome != banking.RefreshSucceeded {
		f.t.Fatalf("refresh: %v %+v", err, res.Run)
	}
}

// tx returns the EUR account's stored transaction with the description.
func (f *fixture) tx(description string) banking.StoredTransaction {
	f.t.Helper()
	accs, err := f.facts.Accounts(f.ctx, f.conn.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, a := range accs {
		txs, err := f.facts.Transactions(f.ctx, a.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, t := range txs {
			if t.Description == description {
				return t
			}
		}
	}
	f.t.Fatalf("no transaction %q", description)
	return banking.StoredTransaction{}
}

func d(s string) calendar.Date {
	v, err := calendar.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func ym(s string) calendar.YearMonth {
	v, err := calendar.ParseYearMonth(s)
	if err != nil {
		panic(err)
	}
	return v
}

// commitment creates a monthly commitment due on day from January 2026.
func (f *fixture) commitment(amount, currency string, day int) commitments.Commitment {
	f.t.Helper()
	c, err := f.cs.Create(f.ctx, alice, commitments.Fields{Name: "Commitment " + amount, Kind: commitments.KindService,
		Amount: money.MustParse(amount, currency), Schedule: commitments.Schedule{Version: 1, Frequency: commitments.FrequencyMonthly, Interval: 1, Day: day},
		StartDate: d("2026-01-01")})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) occurrence(commitmentID, m string) occurrences.Occurrence {
	f.t.Helper()
	list, err := f.cs.Occurrences().List(f.ctx, occurrences.ListFilter{Month: ym(m), CommitmentIDs: []string{commitmentID}, Limit: 10})
	if err != nil || len(list) != 1 {
		f.t.Fatalf("occurrence of %s: %v %v", m, list, err)
	}
	return list[0]
}

func (f *fixture) state(o occurrences.Occurrence) payments.State {
	f.t.Helper()
	got, err := f.cs.Occurrences().Get(f.ctx, o.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	facts, err := f.ps.Facts(f.ctx, []string{o.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	return payments.Derive(context.Background(), got, facts[o.ID], d("2026-11-01"), nil)
}

func (f *fixture) match(t banking.StoredTransaction, o occurrences.Occurrence, amount string, origin matches.Origin, st matches.State) (matches.Change, error) {
	return f.ms.Create(f.ctx, alice, matches.New{TransactionID: t.ID, OccurrenceID: o.ID, Amount: money.MustParse(amount, o.Amount.Currency().Code()), Origin: origin, State: st})
}

func (f *fixture) mustMatch(t banking.StoredTransaction, o occurrences.Occurrence, amount string) matches.Match {
	f.t.Helper()
	ch, err := f.match(t, o, amount, matches.OriginUser, matches.StateConfirmed)
	if err != nil {
		f.t.Fatalf("match %s: %v", amount, err)
	}
	return ch.Match
}

func reason(s string) *string { return &s }

// TestConfirmReverseRestoresOccurrenceState: a user's match pays the
// occurrence; reversing it reopens the occurrence exactly as it was before,
// with the history and audit trail of both steps.
func TestConfirmReverseRestoresOccurrenceState(t *testing.T) {
	f := newFixture(t)
	rent := f.commitment("800.00", "EUR", 2)
	o := f.occurrence(rent.ID, "2026-10")
	before := f.state(o)
	if before.Status != payments.StatusOverdue {
		t.Fatalf("before: %+v", before)
	}
	tx := f.tx("RENT 2026-10")
	ch, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: tx.ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"),
		Origin: matches.OriginUser, State: matches.StateConfirmed, Reason: reason("rent paid by transfer")})
	if err != nil {
		t.Fatal(err)
	}
	m := ch.Match
	if m.State != matches.StateConfirmed || m.Origin != matches.OriginUser || !m.CountsAsEvidence() || m.DecidedBy == nil || m.DecidedBy.UserID != alice.UserID ||
		m.Version != 1 || len(m.History) != 1 || m.History[0].Action != matches.ActionConfirmed || m.Transaction.Amount.String() != "-800.00 EUR" {
		t.Fatalf("created: %+v", m)
	}
	if ch.Result.Occurrence.Version != o.Version+1 {
		t.Errorf("occurrence version %d, want %d", ch.Result.Occurrence.Version, o.Version+1)
	}
	paid := f.state(o)
	if paid.Status != payments.StatusPaid || paid.EvidenceCount != 1 {
		t.Fatalf("after match: %+v", paid)
	}
	facts, _ := f.ps.Facts(f.ctx, []string{o.ID})
	if e := facts[o.ID].Evidence[0]; e.Kind != payments.EvidenceBankTransaction || e.ID != m.ID || e.Date != d("2026-10-02") {
		t.Errorf("evidence: %+v", e)
	}

	// A stale version is refused; the right one reverses.
	if _, err := f.ms.Reverse(f.ctx, alice, m.ID, m.Version+1, nil); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Fatalf("stale reverse: %v", err)
	}
	rch, err := f.ms.Reverse(f.ctx, alice, m.ID, m.Version, reason("the transfer was returned"))
	if err != nil {
		t.Fatal(err)
	}
	if rch.Match.State != matches.StateReversed || rch.Match.Version != 2 || len(rch.Match.History) != 2 || *rch.Match.Reason != "the transfer was returned" {
		t.Fatalf("reversed: %+v", rch.Match)
	}
	after := f.state(o)
	if after.Status != before.Status || after.Paid.String() != before.Paid.String() || after.Outstanding.String() != before.Outstanding.String() || after.EvidenceCount != 0 {
		t.Fatalf("reverse did not restore the state: before %+v after %+v", before, after)
	}
	// Reversed is final.
	for _, op := range []func() error{
		func() error { _, err := f.ms.Reverse(f.ctx, alice, m.ID, 2, nil); return err },
		func() error { _, err := f.ms.Unmatch(f.ctx, alice, m.ID, 2, nil); return err },
		func() error { _, err := f.ms.Confirm(f.ctx, alice, m.ID, 2, false, nil); return err },
		func() error {
			_, err := f.ms.Correct(f.ctx, alice, m.ID, 2, money.MustParse("1.00", "EUR"), false, nil)
			return err
		},
	} {
		if err := op(); !errors.Is(err, matches.ErrTransition) {
			t.Errorf("change of a reversed match: %v", err)
		}
	}
	// The occurrence keeps its payment history: C2 treats it as kept.
	if err := f.cs.Delete(f.ctx, alice, rent.ID, rent.Version); !errors.Is(err, commitments.ErrInUse) {
		t.Errorf("delete a commitment with a reversed match: %v", err)
	}
	if got := f.rec.actions("payment_match."); strings.Join(got, ";") != "payment_match.confirmed from=none;payment_match.reversed from=confirmed" {
		t.Errorf("audit: %v", got)
	}
}

// TestDuplicateAssignmentRefused: a second open match of the same pair, and
// allocations past the transaction amount, are refused by the store and by
// the database itself.
func TestDuplicateAssignmentRefused(t *testing.T) {
	f := newFixture(t)
	streaming := f.commitment("12.99", "EUR", 5)
	oct, nov := f.occurrence(streaming.ID, "2026-10"), f.occurrence(streaming.ID, "2026-11")
	tx := f.tx("STREAMING SERVICE")
	m := f.mustMatch(tx, oct, "12.99")
	if _, err := f.match(tx, oct, "12.99", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrDuplicate) {
		t.Errorf("same pair again: %v", err)
	}
	if _, err := f.match(tx, oct, "1.00", matches.OriginAutoRule, matches.StateSuggested); !errors.Is(err, matches.ErrDuplicate) {
		t.Errorf("suggestion for a matched pair: %v", err)
	}
	// The whole transaction is allocated: nothing more of it can be confirmed.
	if _, err := f.match(tx, nov, "0.01", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrOverAllocated) {
		t.Errorf("allocation past the transaction: %v", err)
	}
	// A suggestion does not allocate anything, but cannot be confirmed past it.
	s, err := f.match(tx, nov, "12.99", matches.OriginAutoRule, matches.StateSuggested)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.Confirm(f.ctx, alice, s.Match.ID, 1, false, nil); !errors.Is(err, matches.ErrOverAllocated) {
		t.Errorf("confirm past the transaction: %v", err)
	}

	// Database level: the partial unique index and the triggers.
	err = f.db.WriteTx(f.ctx, func(ctx context.Context, sqlTx *sql.Tx) error {
		_, err := sqlTx.ExecContext(ctx, "INSERT INTO payment_matches (id, transaction_id, occurrence_id, amount, currency, origin, state, created_by_kind, decided_by_kind, decided_at, created_at, updated_at)"+
			" VALUES ('01JZ0000000000000000000900', ?, ?, '12.99', 'EUR', 'user', 'confirmed', 'user', 'user', '2026-11-01T00:00:00.000000000Z', '2026-11-01T00:00:00.000000000Z', '2026-11-01T00:00:00.000000000Z')",
			string(tx.ID), oct.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("database accepted a duplicate open pair: %v", err)
	}
	for name, q := range map[string]string{
		"another currency":   "UPDATE payment_matches SET currency = 'USD' WHERE id = ?",
		"another occurrence": "UPDATE payment_matches SET occurrence_id = '" + nov.ID + "' WHERE id = ?",
		"back to suggested":  "UPDATE payment_matches SET state = 'suggested' WHERE id = ?",
		"deleted":            "DELETE FROM payment_matches WHERE id = ?",
	} {
		err := f.db.WriteTx(f.ctx, func(ctx context.Context, sqlTx *sql.Tx) error {
			_, err := sqlTx.ExecContext(ctx, q, m.ID)
			return err
		})
		if err == nil {
			t.Errorf("database allowed: %s", name)
		}
	}
	if got, err := f.ms.Get(f.ctx, m.ID); err != nil || got.State != matches.StateConfirmed || got.Version != 1 {
		t.Errorf("match changed: %+v %v", got, err)
	}
}

// TestSplitPartialAndCorrections: one transaction pays two occurrences in
// parts (D8 a); corrections change allocations within the transaction and
// occurrence amounts; overpayment needs explicit consent.
func TestSplitPartialAndCorrections(t *testing.T) {
	f := newFixture(t)
	a := f.commitment("500.00", "EUR", 2)
	b := f.commitment("500.00", "EUR", 3)
	oa, ob := f.occurrence(a.ID, "2026-10"), f.occurrence(b.ID, "2026-10")
	tx := f.tx("RENT 2026-10") // -800.00
	ma := f.mustMatch(tx, oa, "500.00")
	mb := f.mustMatch(tx, ob, "300.00")
	if st := f.state(oa); st.Status != payments.StatusPaid {
		t.Errorf("a: %+v", st)
	}
	if st := f.state(ob); st.Status != payments.StatusPartiallyPaid || st.Outstanding.String() != "200.00 EUR" {
		t.Errorf("b: %+v", st)
	}
	// b cannot take more of the transaction until a gives some back.
	if _, err := f.ms.Correct(f.ctx, alice, mb.ID, mb.Version, money.MustParse("400.00", "EUR"), false, nil); !errors.Is(err, matches.ErrOverAllocated) {
		t.Errorf("correct past the transaction: %v", err)
	}
	ca, err := f.ms.Correct(f.ctx, alice, ma.ID, ma.Version, money.MustParse("400.00", "EUR"), false, reason("split was 400/400"))
	if err != nil {
		t.Fatal(err)
	}
	if ca.Match.Amount.String() != "400.00 EUR" || ca.Match.State != matches.StateConfirmed || len(ca.Match.History) != 2 ||
		ca.Match.History[0].Amount.String() != "500.00 EUR" || ca.Match.History[1].Action != matches.ActionCorrected {
		t.Fatalf("corrected: %+v", ca.Match)
	}
	if _, err := f.ms.Correct(f.ctx, alice, mb.ID, mb.Version, money.MustParse("400.00", "EUR"), false, nil); err != nil {
		t.Fatal(err)
	}
	if st := f.state(oa); st.Status != payments.StatusPartiallyPaid || st.Paid.String() != "400.00 EUR" {
		t.Errorf("a after correction: %+v", st)
	}
	// Overpaying an occurrence needs explicit consent and then shows.
	small := f.commitment("10.00", "EUR", 5)
	os := f.occurrence(small.ID, "2026-10")
	stream := f.tx("STREAMING SERVICE") // -12.99
	if _, err := f.match(stream, os, "12.99", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrOverpaid) {
		t.Errorf("overpayment without consent: %v", err)
	}
	if _, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: stream.ID, OccurrenceID: os.ID, Amount: money.MustParse("12.99", "EUR"),
		Origin: matches.OriginUser, State: matches.StateConfirmed, AllowOverpayment: true}); err != nil {
		t.Fatal(err)
	}
	if st := f.state(os); st.Status != payments.StatusPaid || st.Overpaid.String() != "2.99 EUR" {
		t.Errorf("overpaid: %+v", st)
	}
	// A manual payment counts towards the overpayment check too.
	c := f.commitment("3.50", "EUR", 7)
	oc := f.occurrence(c.ID, "2026-10")
	if _, _, err := f.ps.RecordManual(f.ctx, alice, oc.ID, payments.NewManual{Amount: money.MustParse("3.50", "EUR"), PaidOn: d("2026-10-07")}, d("2026-11-01")); err != nil {
		t.Fatal(err)
	}
	coffee := f.tx("COFFEE KIOSK")
	if _, err := f.match(coffee, oc, "3.50", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrOverpaid) {
		t.Errorf("bank match on a manually paid occurrence: %v", err)
	}
}

// TestTargetsAndPendingRules: currencies, incoming transactions, pending
// transactions and origins.
func TestTargetsAndPendingRules(t *testing.T) {
	f := newFixture(t)
	usd := f.commitment("800.00", "USD", 2)
	ou := f.occurrence(usd.ID, "2026-10")
	rent := f.tx("RENT 2026-10")
	if _, err := f.match(rent, ou, "800.00", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrCurrency) {
		t.Errorf("USD occurrence, EUR transaction: %v", err)
	}
	eur := f.commitment("70.00", "EUR", 30)
	oe := f.occurrence(eur.ID, "2026-10")
	if _, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: rent.ID, OccurrenceID: oe.ID, Amount: money.MustParse("70.00", "USD"),
		Origin: matches.OriginUser, State: matches.StateConfirmed}); !errors.Is(err, matches.ErrCurrency) {
		t.Errorf("allocation in another currency: %v", err)
	}
	if _, err := f.match(f.tx("SALARY OCTOBER"), oe, "70.00", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrNotDebit) {
		t.Errorf("incoming transaction: %v", err)
	}
	if _, err := f.match(rent, oe, "900.00", matches.OriginAutoRule, matches.StateSuggested); !errors.Is(err, matches.ErrOverAllocated) {
		t.Errorf("suggestion larger than the transaction: %v", err)
	}
	if _, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: "01JZ0000000000000000000999", OccurrenceID: oe.ID, Amount: money.MustParse("1.00", "EUR"),
		Origin: matches.OriginUser, State: matches.StateConfirmed}); !errors.Is(err, matches.ErrTransactionNotFound) {
		t.Errorf("unknown transaction: %v", err)
	}
	if _, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: rent.ID, OccurrenceID: "01JZ0000000000000000000999", Amount: money.MustParse("1.00", "EUR"),
		Origin: matches.OriginUser, State: matches.StateConfirmed}); !errors.Is(err, matches.ErrOccurrenceNotFound) {
		t.Errorf("unknown occurrence: %v", err)
	}

	// Pending: suggestions only (D8 a).
	hold := f.tx("HOTEL HOLD")
	if hold.Status != banking.StatusPending {
		t.Fatalf("hold: %+v", hold)
	}
	if _, err := f.match(hold, oe, "70.00", matches.OriginUser, matches.StateConfirmed); !errors.Is(err, matches.ErrNotCountable) {
		t.Errorf("confirm a pending transaction: %v", err)
	}
	if _, err := f.match(hold, oe, "70.00", matches.OriginAutoRule, matches.StateConfirmed); !errors.Is(err, matches.ErrNotCountable) {
		t.Errorf("rule-confirm a pending transaction: %v", err)
	}
	s, err := f.match(hold, oe, "70.00", matches.OriginModel, matches.StateSuggested)
	if err != nil {
		t.Fatal(err)
	}
	if s.Match.CountsAsEvidence() || s.Match.DecidedBy != nil || f.state(oe).EvidenceCount != 0 {
		t.Errorf("a suggestion counted: %+v", s.Match)
	}
	if _, err := f.ms.Confirm(f.ctx, alice, s.Match.ID, 1, false, nil); !errors.Is(err, matches.ErrNotCountable) {
		t.Errorf("confirm a suggestion on a pending transaction: %v", err)
	}
	err = f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE payment_matches SET state = 'confirmed', decided_by_kind = 'user', decided_at = updated_at WHERE id = ?", s.Match.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "booked") {
		t.Errorf("database confirmed a pending transaction: %v", err)
	}
	// Model output is never confirmed by its origin; users only confirm.
	for _, n := range []matches.New{
		{TransactionID: rent.ID, OccurrenceID: oe.ID, Amount: money.MustParse("70.00", "EUR"), Origin: matches.OriginModel, State: matches.StateConfirmed},
		{TransactionID: rent.ID, OccurrenceID: oe.ID, Amount: money.MustParse("70.00", "EUR"), Origin: matches.OriginUser, State: matches.StateSuggested},
		{TransactionID: rent.ID, OccurrenceID: oe.ID, Amount: money.MustParse("70.00", "EUR"), Origin: matches.OriginUser, State: matches.StateRejected},
	} {
		var ve *commitments.ValidationError
		if _, err := f.ms.Create(f.ctx, alice, n); !errors.As(err, &ve) {
			t.Errorf("%s/%s: %v", n.Origin, n.State, err)
		}
	}
	// A rejected suggestion is final and kept; the pair can be suggested again.
	r, err := f.ms.Reject(f.ctx, alice, s.Match.ID, 1, reason("not this hotel"))
	if err != nil || r.Match.State != matches.StateRejected || r.Match.DecidedBy == nil {
		t.Fatalf("reject: %+v %v", r.Match, err)
	}
	if _, err := f.ms.Unmatch(f.ctx, alice, s.Match.ID, 2, nil); !errors.Is(err, matches.ErrTransition) {
		t.Errorf("unmatch a rejected match: %v", err)
	}
	if _, err := f.match(hold, oe, "70.00", matches.OriginModel, matches.StateSuggested); err != nil {
		t.Errorf("suggest again after a rejection: %v", err)
	}
	list, err := f.ms.List(f.ctx, matches.ListFilter{OccurrenceIDs: []string{oe.ID}})
	if err != nil || len(list) != 2 || list[0].State != matches.StateRejected || list[1].State != matches.StateSuggested {
		t.Errorf("list: %+v %v", list, err)
	}
	if list, _ := f.ms.List(f.ctx, matches.ListFilter{States: []matches.State{matches.StateSuggested}, Origins: []matches.Origin{matches.OriginModel}}); len(list) != 1 {
		t.Errorf("filtered list: %+v", list)
	}
}

// TestFullChainSurvivesReimport: matches, decisions and corrections keep
// their transactions through re-imports by provider identity, through a
// rewritten description, a transaction the provider stops reporting (the
// match stays visible but stops counting) and a reload of the database.
func TestFullChainSurvivesReimport(t *testing.T) {
	f := newFixture(t)
	rent := f.commitment("800.00", "EUR", 2)
	o := f.occurrence(rent.ID, "2026-10")
	tx := f.tx("RENT 2026-10")
	m := f.mustMatch(tx, o, "700.00")
	c, err := f.ms.Correct(f.ctx, alice, m.ID, m.Version, money.MustParse("800.00", "EUR"), false, reason("the full rent"))
	if err != nil {
		t.Fatal(err)
	}
	stream := f.commitment("12.99", "EUR", 5)
	os := f.occurrence(stream.ID, "2026-10")
	s, err := f.match(f.tx("STREAMING SERVICE"), os, "12.99", matches.OriginAutoRule, matches.StateSuggested)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.Reject(f.ctx, alice, s.Match.ID, 1, nil); err != nil {
		t.Fatal(err)
	}

	resume := func() {
		t.Helper()
		// Let the next refresh reach early October again (ADR-0020 resumes a
		// week before the cursor).
		if err := f.db.WriteTx(f.ctx, func(ctx context.Context, sqlTx *sql.Tx) error {
			_, err := sqlTx.ExecContext(ctx, "UPDATE bank_account_sync SET transactions_through = '2026-09-30'")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	setRent := func(fn func(*banking.Transaction) bool) {
		f.p.Mutate(func(ds *bankingtest.Dataset) {
			txs := ds.Accounts[0].Transactions
			for i := range txs {
				if txs[i].EntryReference == bankingtest.RefRent {
					if !fn(&txs[i]) {
						ds.Accounts[0].Transactions = append(txs[:i:i], txs[i+1:]...)
					}
					return
				}
			}
		})
	}

	// 1. Re-import with a rewritten description: same transaction, same match.
	f.clock.set(t0.Add(24 * time.Hour))
	resume()
	setRent(func(x *banking.Transaction) bool { x.Description = "RENT OCTOBER 2026"; return true })
	f.refresh()
	got, err := f.ms.Get(f.ctx, m.ID)
	if err != nil || got.TransactionID != tx.ID || got.Transaction.Description != "RENT OCTOBER 2026" || got.Amount.String() != "800.00 EUR" ||
		got.Version != c.Match.Version || !got.CountsAsEvidence() || len(got.History) != 2 {
		t.Fatalf("after re-import: %+v %v", got, err)
	}
	if st := f.state(o); st.Status != payments.StatusPaid {
		t.Fatalf("status after re-import: %+v", st)
	}

	// 2. The provider stops reporting it: still listed and confirmed, but not
	// evidence; the occurrence is kept (not regenerated away).
	f.clock.set(t0.Add(48 * time.Hour))
	resume()
	var saved banking.Transaction
	setRent(func(x *banking.Transaction) bool { saved = *x; return false })
	f.refresh()
	got, _ = f.ms.Get(f.ctx, m.ID)
	if got.State != matches.StateConfirmed || got.Transaction.NoLongerReportedAt.IsZero() || got.CountsAsEvidence() {
		t.Fatalf("unreported: %+v", got)
	}
	if st := f.state(o); st.Status != payments.StatusOverdue || st.EvidenceCount != 0 {
		t.Fatalf("status while unreported: %+v", st)
	}
	if err := f.cs.Delete(f.ctx, alice, rent.ID, rent.Version); !errors.Is(err, commitments.ErrInUse) {
		t.Errorf("commitment with a confirmed match deleted: %v", err)
	}

	// 3. Reported again: it counts again.
	f.clock.set(t0.Add(72 * time.Hour))
	resume()
	f.p.Mutate(func(ds *bankingtest.Dataset) {
		ds.Accounts[0].Transactions = append(ds.Accounts[0].Transactions, saved)
	})
	f.refresh()
	if got, _ = f.ms.Get(f.ctx, m.ID); !got.CountsAsEvidence() {
		t.Fatalf("reported again: %+v", got)
	}

	// 4. Reload: a new process sees the same chain.
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	g := open(t, f.path, f.clock)
	all, err := g.ms.List(g.ctx, matches.ListFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("after reload: %+v %v", all, err)
	}
	if all[0].ID != m.ID || all[0].State != matches.StateConfirmed || all[0].Amount.String() != "800.00 EUR" || *all[0].Reason != "the full rent" ||
		all[1].State != matches.StateRejected || all[1].Origin != matches.OriginAutoRule {
		t.Errorf("after reload: %+v", all)
	}
	if st := g.state(o); st.Status != payments.StatusPaid {
		t.Errorf("status after reload: %+v", st)
	}
	// Bank facts with user decisions on them are never purged.
	if _, err := g.facts.PurgeConnection(g.ctx, f.conn.ID, f.conn.Version); !errors.Is(err, banking.ErrPurgeBlocked) {
		t.Errorf("purge with matches: %v", err)
	}
}

// TestRegenerationDropsOnlySuggestions: a suggestion does not pin an unpaid
// occurrence; deleting its commitment drops the suggestion with it.
func TestRegenerationDropsOnlySuggestions(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("12.99", "EUR", 5)
	o := f.occurrence(c.ID, "2026-10")
	s, err := f.match(f.tx("STREAMING SERVICE"), o, "12.99", matches.OriginAutoRule, matches.StateSuggested)
	if err != nil {
		t.Fatal(err)
	}
	c, err = f.cs.Get(f.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cs.Delete(f.ctx, alice, c.ID, c.Version); err != nil {
		t.Fatalf("delete a commitment whose occurrence has only a suggestion: %v", err)
	}
	if _, err := f.ms.Get(f.ctx, s.Match.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("suggestion of a deleted occurrence: %v", err)
	}
}

// TestConcurrentAllocationsNeverExceedTheTransaction: writers race to
// allocate the whole transaction to different occurrences; exactly one wins.
// Writers racing on one match version: exactly one wins.
func TestConcurrentAllocationsNeverExceedTheTransaction(t *testing.T) {
	f := newFixture(t)
	rent := f.commitment("800.00", "EUR", 2)
	months := []string{"2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07", "2026-08"}
	tx := f.tx("RENT 2026-10")
	errs := make(chan error, len(months))
	for _, m := range months {
		o := f.occurrence(rent.ID, m)
		go func() {
			_, err := f.match(tx, o, "800.00", matches.OriginUser, matches.StateConfirmed)
			errs <- err
		}()
	}
	wins := 0
	for range months {
		err := <-errs
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, matches.ErrOverAllocated):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d allocations of one transaction won", wins)
	}

	list, _ := f.ms.List(f.ctx, matches.ListFilter{TransactionIDs: []string{string(tx.ID)}})
	m := list[0]
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			var err error
			if i%2 == 0 {
				_, err = f.ms.Unmatch(f.ctx, alice, m.ID, m.Version, nil)
			} else {
				_, err = f.ms.Reverse(f.ctx, alice, m.ID, m.Version, nil)
			}
			results <- err
		}()
	}
	wins = 0
	for i := 0; i < 8; i++ {
		err := <-results
		switch {
		case err == nil:
			wins++
		case !errors.As(err, new(*resource.VersionConflictError)):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("%d decisions on one version won", wins)
	}
}
