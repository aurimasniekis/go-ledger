package matches_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/automatch"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	deferralstore "github.com/aurimasniekis/go-ledger/internal/store/deferrals"
)

// named creates a monthly commitment due on day from January 2026, with an
// optional match tolerance.
func (f *fixture) named(name, amount string, day int, tolerance string) commitments.Commitment {
	f.t.Helper()
	fields := commitments.Fields{Name: name, Kind: commitments.KindSubscription, Amount: money.MustParse(amount, "EUR"),
		Schedule: commitments.Schedule{Version: 1, Frequency: commitments.FrequencyMonthly, Interval: 1, Day: day}, StartDate: d("2026-01-01")}
	if tolerance != "" {
		t := money.MustParse(tolerance, "EUR")
		fields.MatchTolerance = &t
	}
	c, err := f.cs.Create(f.ctx, alice, fields)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) matcher() *automatch.Engine {
	return automatch.New(f.ms, automatch.Options{Now: f.clock.Now, Today: func() (calendar.Date, error) {
		return calendar.MustLoadZone("UTC").Today(f.clock.Now())
	}})
}

func (f *fixture) run(e *automatch.Engine) automatch.Result {
	f.t.Helper()
	res, err := e.Run(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// matchesOf lists the matches of an occurrence, oldest first.
func (f *fixture) matchesOf(o occurrences.Occurrence) []matches.Match {
	f.t.Helper()
	ms, err := f.ms.List(f.ctx, matches.ListFilter{OccurrenceIDs: []string{o.ID}})
	if err != nil {
		f.t.Fatal(err)
	}
	return ms
}

func (f *fixture) countMatches() int {
	f.t.Helper()
	ms, err := f.ms.List(f.ctx, matches.ListFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	return len(ms)
}

func states(ms []matches.Match) string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Origin)+"/"+string(m.State))
	}
	return strings.Join(out, " ")
}

// TestAutomaticMatchingOnImportedData runs the engine over the fake
// provider's October data imported through the real engine: an exact match is
// confirmed, a near amount, two identical transactions and a pending one are
// suggested, a second run writes nothing, and every write is the system's.
func TestAutomaticMatchingOnImportedData(t *testing.T) {
	f := newFixture(t)
	rent := f.occurrence(f.named("Rent", "800.00", 2, "").ID, "2026-10")
	streaming := f.occurrence(f.named("Streaming service", "13.49", 5, "").ID, "2026-10")
	coffee := f.occurrence(f.named("Coffee", "3.50", 7, "").ID, "2026-10")
	books := f.occurrence(f.named("Books", "45.00", 29, "").ID, "2026-10")
	e := f.matcher()

	res := f.run(e)
	if res != (automatch.Result{Candidates: 5, Confirmed: 1, Suggested: 4}) {
		t.Fatalf("first run: %+v", res)
	}
	// Exact match: confirmed by the system as auto_rule, paying the
	// occurrence.
	got := f.matchesOf(rent)
	if states(got) != "auto_rule/confirmed" {
		t.Fatalf("rent: %s", states(got))
	}
	m := got[0]
	if m.CreatedBy.Kind != audit.ActorSystem || m.DecidedBy == nil || m.DecidedBy.Kind != audit.ActorSystem || m.Amount.String() != "800.00 EUR" ||
		m.Reason == nil || !strings.HasPrefix(*m.Reason, "automatic match confirmed: exact amount, booked, 0 days") {
		t.Errorf("rent match: %+v", m)
	}
	if st := f.state(rent); st.Status != payments.StatusPaid || st.BankEvidenceCount != 1 {
		t.Errorf("rent state: %+v", st)
	}
	// Near amount: a suggestion with the failed safeguard named, never bank
	// text.
	got = f.matchesOf(streaming)
	if states(got) != "auto_rule/suggested" || got[0].Reason == nil || !strings.Contains(*got[0].Reason, "amount_differs (by 0.50 EUR)") ||
		!strings.Contains(*got[0].Reason, "description similarity 100/100 (a hint only)") || strings.Contains(*got[0].Reason, "STREAMING") {
		t.Errorf("streaming: %s %v", states(got), got)
	}
	if st := f.state(streaming); st.Status == payments.StatusPaid {
		t.Errorf("a suggestion paid the occurrence: %+v", st)
	}
	// Two identical transactions: both suggested (ambiguous).
	got = f.matchesOf(coffee)
	if states(got) != "auto_rule/suggested auto_rule/suggested" || !strings.Contains(*got[0].Reason, "several_transactions (2") {
		t.Errorf("coffee: %s", states(got))
	}
	// Pending: suggested only.
	got = f.matchesOf(books)
	if states(got) != "auto_rule/suggested" || !strings.Contains(*got[0].Reason, "pending") {
		t.Errorf("books: %s", states(got))
	}
	for _, e := range f.rec.events {
		if strings.HasPrefix(string(e.Action), "payment_match.") && e.Actor != audit.System {
			t.Errorf("not the system: %+v", e)
		}
	}
	if got := f.rec.actions("payment_match."); len(got) != 5 || got[0] != "payment_match.confirmed from=none" {
		t.Errorf("audit %v", got)
	}

	// Idempotent: nothing changes on a second run, or after a re-import.
	n := f.countMatches()
	if res := f.run(e); res.Changed() || res.Skipped != 0 {
		t.Errorf("second run: %+v", res)
	}
	f.refresh()
	if res := f.run(e); res.Changed() || f.countMatches() != n {
		t.Errorf("after a re-import: %+v, %d matches", res, f.countMatches())
	}
}

// TestPendingBecomesBookedThroughImportHook: the engine runs after every
// refresh; when the pending transaction books in place, the engine confirms
// its own suggestion.
func TestPendingBecomesBookedThroughImportHook(t *testing.T) {
	f := newFixture(t)
	books := f.occurrence(f.named("Books", "45.00", 29, "").ID, "2026-10")
	e := f.matcher()
	runs := 0
	hooked := bankimport.New(f.facts, bankimport.Options{Now: f.clock.Now, Sleep: func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d }, AfterRefresh: func(ctx context.Context, _ bankimport.Result) {
			runs++
			if _, err := e.Run(ctx); err != nil {
				t.Error(err)
			}
		}})
	refresh := func() {
		t.Helper()
		if res, err := hooked.Refresh(f.ctx, f.conn.ID, f.p); err != nil || res.Run.Outcome != banking.RefreshSucceeded {
			t.Fatalf("refresh: %v %+v", err, res.Run)
		}
	}
	refresh()
	if got := f.matchesOf(books); runs != 1 || states(got) != "auto_rule/suggested" {
		t.Fatalf("after the first refresh: runs %d, %s", runs, states(got))
	}
	f.clock.set(t0.Add(24 * time.Hour))
	f.p.Mutate(bankingtest.BookPending)
	refresh()
	got := f.matchesOf(books)
	if runs != 2 || states(got) != "auto_rule/confirmed" || len(got[0].History) != 2 || got[0].History[1].By.Kind != audit.ActorSystem {
		t.Fatalf("after booking: runs %d, %s %+v", runs, states(got), got)
	}
	if st := f.state(books); st.Status != payments.StatusPaid {
		t.Errorf("state %+v", st)
	}
}

// TestImportHookSkipsFailedRefreshes: a refresh that stored nothing (the
// provider failed) does not run the matching.
func TestImportHookSkipsFailedRefreshes(t *testing.T) {
	f := newFixture(t)
	runs := 0
	hooked := bankimport.New(f.facts, bankimport.Options{Now: f.clock.Now, Sleep: func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d }, AfterRefresh: func(context.Context, bankimport.Result) { runs++ }})
	f.p.FailNext(bankingtest.OpAccounts, bankingtest.Failure(bankingtest.OpAccounts, banking.KindPermanent, "", "bad_request", 0))
	res, err := hooked.Refresh(f.ctx, f.conn.ID, f.p)
	if err == nil || res.Run.Outcome != banking.RefreshFailed || runs != 0 {
		t.Fatalf("failed refresh: %v %s, hook ran %d times", err, res.Run.Outcome, runs)
	}
	if _, err := hooked.Refresh(f.ctx, f.conn.ID, f.p); err != nil || runs != 1 {
		t.Fatalf("successful refresh: %v, hook ran %d times", err, runs)
	}
}

// TestRejectionIsNeverSuggestedAgain: a person's rejection closes the pair
// for good, even when the transaction later passes every safeguard.
func TestRejectionIsNeverSuggestedAgain(t *testing.T) {
	f := newFixture(t)
	books := f.occurrence(f.named("Books", "45.00", 29, "").ID, "2026-10")
	e := f.matcher()
	f.run(e)
	s := f.matchesOf(books)[0]
	if _, err := f.ms.Reject(f.ctx, alice, s.ID, s.Version, reason("not this one")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if res := f.run(e); res.Changed() {
			t.Fatalf("after the rejection: %+v", res)
		}
	}
	f.p.Mutate(bankingtest.BookPending)
	f.refresh()
	if res := f.run(e); res.Changed() || states(f.matchesOf(books)) != "auto_rule/rejected" {
		t.Fatalf("after booking: %+v %s", res, states(f.matchesOf(books)))
	}
}

// TestEngineDropsOnlyItsOwnStaleSuggestions (decision D33): when a person
// confirms one of two ambiguous suggestions, the engine drops the other (its
// own, now stale); a suggestion a person corrected is never touched.
func TestEngineDropsOnlyItsOwnStaleSuggestions(t *testing.T) {
	f := newFixture(t)
	coffee := f.occurrence(f.named("Coffee", "3.50", 7, "").ID, "2026-10")
	streaming := f.occurrence(f.named("Streaming service", "13.49", 5, "").ID, "2026-10")
	e := f.matcher()
	f.run(e)
	cs := f.matchesOf(coffee)
	if _, err := f.ms.Confirm(f.ctx, alice, cs[0].ID, cs[0].Version, false, nil); err != nil {
		t.Fatal(err)
	}
	ss := f.matchesOf(streaming)[0]
	if _, err := f.ms.Correct(f.ctx, alice, ss.ID, ss.Version, money.MustParse("12.99", "EUR"), false, reason("the price went down")); err != nil {
		t.Fatal(err)
	}
	// Make the streaming suggestion stale too: a manual payment pays the
	// occurrence.
	if _, _, err := f.ps.RecordManual(f.ctx, alice, streaming.ID, payments.NewManual{Amount: money.MustParse("13.49", "EUR"), PaidOn: d("2026-10-05")}, d("2026-11-01")); err != nil {
		t.Fatal(err)
	}
	res := f.run(e)
	if res != (automatch.Result{Dropped: 1}) {
		t.Fatalf("run: %+v", res)
	}
	cs = f.matchesOf(coffee)
	if states(cs) != "auto_rule/confirmed auto_rule/rejected" || cs[1].DecidedBy.Kind != audit.ActorSystem || !strings.HasPrefix(*cs[1].Reason, "automatic suggestion dropped") {
		t.Errorf("coffee: %s %+v", states(cs), cs)
	}
	if got := f.matchesOf(streaming); states(got) != "auto_rule/suggested" || got[0].Version != ss.Version+1 {
		t.Errorf("corrected suggestion touched: %s", states(got))
	}
	// The person's confirmation stands: automation cannot undo it.
	if _, err := f.ms.Unmatch(f.ctx, audit.System, cs[0].ID, cs[0].Version, nil); err == nil {
		t.Error("automation unmatched a person's confirmation")
	}
	if res := f.run(e); res.Changed() {
		t.Errorf("third run: %+v", res)
	}
}

// TestToleranceConfirmsThroughTheStore: a payment short of the amount by no
// more than the commitment's tolerance is confirmed; the store applies the
// same tolerance to the engine's confirmation of its own suggestion.
func TestToleranceConfirmsThroughTheStore(t *testing.T) {
	f := newFixture(t)
	streaming := f.occurrence(f.named("Streaming service", "13.49", 5, "0.50").ID, "2026-10")
	e := f.matcher()
	if res := f.run(e); res.Confirmed != 1 {
		t.Fatalf("run: %+v", res)
	}
	got := f.matchesOf(streaming)
	if states(got) != "auto_rule/confirmed" || got[0].Amount.String() != "12.99 EUR" || !strings.Contains(*got[0].Reason, "within the commitment's tolerance (short by 0.50 EUR)") {
		t.Fatalf("streaming: %s %+v", states(got), got)
	}
	// Within the tolerance counts as paid; the shortfall is a detail (D39 5a).
	if st := f.state(streaming); st.Status != payments.StatusPaid || !st.Outstanding.IsZero() || st.Shortfall.String() != "0.50 EUR" {
		t.Errorf("state: %+v", st)
	}

	// ConfirmAuto reads the tolerance from the commitment inside its own
	// transaction: a tolerance narrowed after the suggestion was made (here
	// to 0.49) refuses the confirmation, and the stored tolerance (0.50)
	// accepts it.
	g := newFixture(t)
	o := g.occurrence(g.named("Books", "45.50", 29, "0.50").ID, "2026-10")
	s, err := g.ms.Create(g.ctx, audit.System, matches.New{TransactionID: g.tx("CARD PURCHASE BOOKSHOP").ID, OccurrenceID: o.ID, Amount: money.MustParse("45.00", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	g.p.Mutate(bankingtest.BookPending)
	g.refresh()
	g.mustExec("UPDATE commitments SET match_tolerance_amount = '0.49'")
	if _, err := g.ms.ConfirmAuto(g.ctx, s.Match.ID, s.Match.Version, nil); !errors.Is(err, matches.ErrAutoAmount) {
		t.Errorf("beyond the tolerance: %v", err)
	}
	g.mustExec("UPDATE commitments SET match_tolerance_amount = '0.50'")
	if _, err := g.ms.ConfirmAuto(g.ctx, s.Match.ID, s.Match.Version, nil); err != nil {
		t.Errorf("within the tolerance: %v", err)
	}
}

// TestAutoConfirmReadsToleranceInTheTransaction (review of X2, F2): the
// store, not the caller, decides the tolerance. A system confirmation short
// of a commitment without tolerance is refused, and so is one after the
// tolerance was cleared since the plan.
func TestAutoConfirmReadsToleranceInTheTransaction(t *testing.T) {
	f := newFixture(t)
	streaming := f.occurrence(f.named("Streaming", "13.49", 5, "").ID, "2026-10")
	tx := f.tx("STREAMING SERVICE")
	if _, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: tx.ID, OccurrenceID: streaming.ID, Amount: money.MustParse("12.99", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateConfirmed}); !errors.Is(err, matches.ErrAutoAmount) {
		t.Errorf("no tolerance, short payment: %v", err)
	}
	f.mustExec("UPDATE commitments SET match_tolerance_amount = '0.50', match_tolerance_currency = 'EUR'")
	if _, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: tx.ID, OccurrenceID: streaming.ID, Amount: money.MustParse("12.99", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateConfirmed}); err != nil {
		t.Errorf("within the stored tolerance: %v", err)
	}
	// Cleared after the plan was made (a stale tolerance in the plan).
	g := newFixture(t)
	o := g.occurrence(g.named("Streaming", "13.49", 15, "2.00").ID, "2026-10")
	g.mustExec("UPDATE commitments SET match_tolerance_amount = NULL, match_tolerance_currency = NULL")
	if _, err := g.ms.Create(g.ctx, audit.System, matches.New{TransactionID: g.tx("STREAMING SERVICE").ID, OccurrenceID: o.ID, Amount: money.MustParse("12.99", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateConfirmed}); !errors.Is(err, matches.ErrAutoAmount) {
		t.Errorf("tolerance cleared since the plan: %v", err)
	}
}

// TestConfirmAutoRefusesModelSuggestions (decision D9): model output is only
// ever a suggestion; automation cannot confirm it.
func TestConfirmAutoRefusesModelSuggestions(t *testing.T) {
	f := newFixture(t)
	o := f.occurrence(f.named("Rent", "800.00", 2, "").ID, "2026-10")
	s, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: o.ID, Amount: money.MustParse("800.00", "EUR"),
		Origin: matches.OriginModel, State: matches.StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.ConfirmAuto(f.ctx, s.Match.ID, s.Match.Version, nil); !errors.Is(err, matches.ErrAutoModel) {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := f.ms.Confirm(f.ctx, audit.System, s.Match.ID, s.Match.Version, false, nil); !errors.Is(err, matches.ErrAutoModel) {
		t.Fatalf("confirm as the system: %v", err)
	}
	// The engine leaves it alone, and a person may still confirm it.
	if res := f.run(f.matcher()); res.Changed() {
		t.Errorf("run: %+v", res)
	}
	if _, err := f.ms.Confirm(f.ctx, alice, s.Match.ID, s.Match.Version, false, nil); err != nil {
		t.Error(err)
	}
}

// TestSnapshotLeavesOutClosedFacts: cancelled occurrences, occurrences with
// evidence, incoming, superseded and unreported transactions are not offered
// to the planner.
func TestSnapshotLeavesOutClosedFacts(t *testing.T) {
	f := newFixture(t)
	o := f.occurrence(f.named("Rent", "800.00", 2, "").ID, "2026-10")
	snap, err := f.ms.Snapshot(f.ctx, ym("2026-11"))
	if err != nil {
		t.Fatal(err)
	}
	var txs []string
	for _, tx := range snap.Transactions {
		if tx.Amount.Sign() >= 0 {
			t.Errorf("incoming transaction %s", tx.ID)
		}
		txs = append(txs, string(tx.Status)+" "+tx.Amount.String())
	}
	if got := strings.Join(txs, ", "); strings.Count(got, "booked") != 5 || strings.Count(got, "pending") != 2 {
		t.Errorf("transactions: %s", got)
	}
	count := func() int {
		snap, err := f.ms.Snapshot(f.ctx, ym("2026-11"))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, so := range snap.Occurrences {
			if so.ID == o.ID {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatal("open occurrence missing")
	}
	f.markGone(f.tx("RENT 2026-10"))
	if _, _, err := f.ps.RecordManual(f.ctx, alice, o.ID, payments.NewManual{Amount: money.MustParse("1.00", "EUR"), PaidOn: d("2026-10-02")}, d("2026-11-01")); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Error("a partly paid occurrence is offered for automatic matching")
	}
	snap, err = f.ms.Snapshot(f.ctx, ym("2026-11"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range snap.Transactions {
		if tx.Amount.String() == "-800.00 EUR" {
			t.Error("an unreported transaction is offered")
		}
	}
	// November occurrences are materialised for early payments.
	f.occurrence(f.named("Phone", "20.00", 3, "").ID, "2026-11")
}

// planFor records an active deferral plan on o with the planned date.
func (f *fixture) planFor(o occurrences.Occurrence, planned string) deferrals.Plan {
	f.t.Helper()
	why := "waiting for salary"
	p, err := deferralstore.New(f.db, f.rec, f.clock.Now, nil).Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: o.ID,
		Fields: deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d(planned), Reason: &why}}, d("2026-11-01"))
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// D-DF 7: the September rent (due 20 September) was deferred to 2 October,
// the day the fake bank booked 800.00 EUR. The engine confirms it through the
// planned-date window; the store's own guard agrees.
func TestAutomaticMatchingAcceptsActivePlannedDate(t *testing.T) {
	f := newFixture(t)
	rent := f.occurrence(f.named("Rent", "800.00", 20, "").ID, "2026-09")
	f.planFor(rent, "2026-10-02")
	res := f.run(f.matcher())
	if res.Confirmed != 1 {
		t.Fatalf("run: %+v", res)
	}
	got := f.matchesOf(rent)
	if states(got) != "auto_rule/confirmed" || got[0].Reason == nil || !strings.Contains(*got[0].Reason, "0 days from the planned date") {
		t.Fatalf("rent: %s %+v", states(got), got)
	}
	if st := f.state(rent); st.Status != payments.StatusPaid {
		t.Errorf("state %+v", st)
	}
}

// An undone plan no longer widens the window: the same pair is not even a
// candidate (12 days from the due date), and the store refuses an automatic
// confirmation there.
func TestUndonePlanDoesNotWidenAutomaticWindow(t *testing.T) {
	f := newFixture(t)
	rent := f.occurrence(f.named("Rent", "800.00", 20, "").ID, "2026-09")
	plan := f.planFor(rent, "2026-10-02")
	if _, err := deferralstore.New(f.db, f.rec, f.clock.Now, nil).Undo(f.ctx, alice, plan.ID, plan.Version, nil); err != nil {
		t.Fatal(err)
	}
	if res := f.run(f.matcher()); res.Changed() || res.Candidates != 0 || f.countMatches() != 0 {
		t.Fatalf("run: %+v", res)
	}
	_, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: rent.ID,
		Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginAutoRule, State: matches.StateConfirmed})
	if !errors.Is(err, matches.ErrAutoDate) {
		t.Errorf("store after undo: %v", err)
	}
}

// The store's guard accepts the planned window only while the plan is active.
func TestStoreAutoGuardUsesActivePlan(t *testing.T) {
	f := newFixture(t)
	rent := f.occurrence(f.named("Rent", "800.00", 20, "").ID, "2026-09")
	n := matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: rent.ID, Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginAutoRule, State: matches.StateConfirmed}
	if _, err := f.ms.Create(f.ctx, audit.System, n); !errors.Is(err, matches.ErrAutoDate) {
		t.Fatalf("no plan: %v", err)
	}
	f.planFor(rent, "2026-10-02")
	if _, err := f.ms.Create(f.ctx, audit.System, n); err != nil {
		t.Errorf("active plan: %v", err)
	}
}

// withRent is a fake-bank change that adds a rent-sized EUR card payment, on
// the given date, to the everyday account before the first import. A pending
// payment has no booking date yet, as in the fixtures.
func withRent(description string, status banking.BookingStatus, on string) func(*bankingtest.Dataset) {
	return func(ds *bankingtest.Dataset) {
		tx := banking.Transaction{Status: status, Amount: money.MustParse("-800.00", "EUR"), TransactionDate: d(on), Description: description}
		if status == banking.StatusBooked {
			tx.EntryReference, tx.ProviderStatus = "fake-ref-"+description, "BOOK"
			tx.BookingDate, tx.ValueDate = d(on), d(on)
		} else {
			tx.ProviderStatus = "PDNG"
		}
		ds.Accounts[0].Transactions = append(ds.Accounts[0].Transactions, tx)
	}
}

// A revision moves the automatic-confirmation window with the planned date:
// before the revision the new date is refused, after it the old date is
// refused and the new date accepted (the store reads the revised date inside
// its write transaction).
func TestRevisedPlanMovesAutomaticWindow(t *testing.T) {
	f := newFixtureWith(t, withRent("RENT 2026-10 LATE", banking.StatusBooked, "2026-10-20"))
	rent := f.occurrence(f.named("Rent", "800.00", 20, "").ID, "2026-09")
	early, late := f.tx("RENT 2026-10"), f.tx("RENT 2026-10 LATE")
	confirm := func(t banking.StoredTransaction) matches.New {
		return matches.New{TransactionID: t.ID, OccurrenceID: rent.ID, Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginAutoRule, State: matches.StateConfirmed}
	}
	plan := f.planFor(rent, "2026-10-02")
	if _, err := f.ms.Create(f.ctx, audit.System, confirm(late)); !errors.Is(err, matches.ErrAutoDate) {
		t.Fatalf("new date before the revision: %v", err)
	}
	revised, err := deferralstore.New(f.db, f.rec, f.clock.Now, nil).Revise(f.ctx, alice, plan.ID, plan.Version,
		deferrals.Fields{Type: plan.Type, PlannedDate: d("2026-10-20"), Reason: plan.Reason}, d("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	if revised.PlannedDate.String() != "2026-10-20" || revised.Version != plan.Version+1 {
		t.Fatalf("revised plan: %+v", revised)
	}
	if _, err := f.ms.Create(f.ctx, audit.System, confirm(early)); !errors.Is(err, matches.ErrAutoDate) {
		t.Errorf("old date after the revision: %v", err)
	}
	if _, err := f.ms.Create(f.ctx, audit.System, confirm(late)); err != nil {
		t.Fatalf("new date after the revision: %v", err)
	}
	if st := f.state(rent); st.Status != payments.StatusPaid {
		t.Errorf("state %+v", st)
	}
}

// A suggestion made through the planned-date window is dropped once the plan
// is undone: the engine drops its own stale suggestion, and the store refuses
// the same transaction as an automatic confirmation.
func TestUndoDropsPlannedWindowSuggestion(t *testing.T) {
	f := newFixtureWith(t, withRent("RENT PENDING", banking.StatusPending, "2026-10-12"))
	rent := f.occurrence(f.named("Rent", "800.00", 20, "").ID, "2026-09")
	e := f.matcher()
	// 22 days from the due date: not a candidate without the plan.
	if res := f.run(e); res.Candidates != 0 || f.countMatches() != 0 {
		t.Fatalf("without a plan: %+v", res)
	}
	plan := f.planFor(rent, "2026-10-12")
	if res := f.run(e); res != (automatch.Result{Candidates: 1, Suggested: 1}) {
		t.Fatalf("with the plan: %+v", res)
	}
	if got := f.matchesOf(rent); states(got) != "auto_rule/suggested" || got[0].Reason == nil || !strings.Contains(*got[0].Reason, "pending") {
		t.Fatalf("suggestion: %s %+v", states(got), got)
	}
	if _, err := deferralstore.New(f.db, f.rec, f.clock.Now, nil).Undo(f.ctx, alice, plan.ID, plan.Version, nil); err != nil {
		t.Fatal(err)
	}
	res := f.run(e)
	if res.Dropped != 1 || res.Confirmed != 0 || res.Suggested != 0 {
		t.Fatalf("after the undo: %+v", res)
	}
	got := f.matchesOf(rent)
	if states(got) != "auto_rule/rejected" || got[0].DecidedBy == nil || got[0].DecidedBy.Kind != audit.ActorSystem ||
		got[0].Reason == nil || !strings.HasPrefix(*got[0].Reason, "automatic suggestion dropped") {
		t.Errorf("dropped suggestion: %s %+v", states(got), got)
	}
	if st := f.state(rent); st.Status == payments.StatusPaid {
		t.Errorf("state %+v", st)
	}
	if _, err := f.ms.Create(f.ctx, audit.System, matches.New{TransactionID: f.tx("RENT PENDING").ID, OccurrenceID: rent.ID,
		Amount: money.MustParse("800.00", "EUR"), Origin: matches.OriginAutoRule, State: matches.StateConfirmed}); !errors.Is(err, matches.ErrAutoDate) {
		t.Errorf("store after the undo: %v", err)
	}
}
