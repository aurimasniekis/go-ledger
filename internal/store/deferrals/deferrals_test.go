package deferrals_test

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
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/monthview"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	deferralstore "github.com/aurimasniekis/go-ledger/internal/store/deferrals"
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

func (r *recorder) actions(prefix string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if strings.HasPrefix(string(e.Action), prefix) {
			out = append(out, string(e.Action))
		}
	}
	return strings.Join(out, " ")
}

var alice = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

// t0 is the fake provider's "now": its standard data is in October 2026.
var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

var today = d("2026-11-01")

type fixture struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	facts  *bankfacts.Store
	cs     *commitmentstore.Store
	ps     *paymentstore.Store
	ms     *matchstore.Store
	ds     *deferralstore.Store
	rec    *recorder
	p      *bankingtest.Provider
	engine *bankimport.Engine
	conn   banking.Connection
}

// newFixture opens a database and imports the fake provider's standard data
// through the real import engine.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := func() time.Time { return t0 }
	rec := &recorder{}
	f := &fixture{t: t, ctx: ctx, db: db, rec: rec, facts: bankfacts.New(db, nil),
		cs: commitmentstore.New(db, rec, func() time.Time { return t0.AddDate(0, -3, 0) }, nil), ps: paymentstore.New(db, rec, now, nil),
		ms: matchstore.New(db, rec, now, nil), ds: deferralstore.New(db, rec, now, nil)}
	f.engine = bankimport.New(f.facts, bankimport.Options{Now: now, Sleep: func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d }})
	f.p = bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: now, PageSize: 3}, bankingtest.Standard())
	inst := bankingtest.Standard().Institutions[0]
	if _, err := f.p.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: "s", PSUType: banking.PSUPersonal}); err != nil {
		t.Fatal(err)
	}
	conn, err := f.facts.CreateConnection(ctx, banking.NewConnection{Capabilities: f.p.Capabilities(), Institution: inst, ConsentState: "s"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := f.p.CompleteConsent(ctx, banking.ConsentCompletion{State: "s", Code: bankingtest.AuthorizationCode("s")})
	if err != nil {
		t.Fatal(err)
	}
	if f.conn, err = f.facts.ActivateConnection(ctx, conn.ID, sess, t0); err != nil {
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

func text(s string) *string { return &s }

// commitment creates a monthly commitment due on day from January 2026.
func (f *fixture) commitment(amount string, day int) commitments.Commitment {
	f.t.Helper()
	c, err := f.cs.Create(f.ctx, alice, commitments.Fields{Name: "Commitment " + amount, Kind: commitments.KindService,
		Amount: money.MustParse(amount, "EUR"), Schedule: commitments.Schedule{Version: 1, Frequency: commitments.FrequencyMonthly, Interval: 1, Day: day},
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

func (f *fixture) state(id string) payments.State {
	f.t.Helper()
	o, err := f.cs.Occurrences().Get(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	facts, err := f.ps.Facts(f.ctx, []string{id})
	if err != nil {
		f.t.Fatal(err)
	}
	return payments.Derive(o, facts[id], today, nil)
}

func (f *fixture) plan(o occurrences.Occurrence, planned string, typ deferrals.PlanType) deferrals.Plan {
	f.t.Helper()
	p, err := f.ds.Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: o.ID, Fields: deferrals.Fields{Type: typ, PlannedDate: d(planned), Reason: text("waiting for salary")}}, today)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) totals(m string) monthview.CurrencyTotals {
	f.t.Helper()
	s, err := monthview.Service{Occurrences: f.cs.Occurrences(), Payments: f.ps, Deferrals: f.ds}.Month(f.ctx, ym(m), today)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, tot := range s.Totals {
		if tot.Currency.Code() == "EUR" {
			return tot
		}
	}
	f.t.Fatalf("no EUR totals in %s", m)
	return monthview.CurrencyTotals{}
}

// TestDeferralPlansOnceSurvivesReimportAndUndoRestores is the DF1 acceptance
// path against the real stores: two outstanding 50 EUR occurrences plan 100
// EUR in the later month, the deferral never marks anything paid, a
// re-import changes nothing, and undo restores the plain view.
func TestDeferralPlansOnceSurvivesReimportAndUndoRestores(t *testing.T) {
	f := newFixture(t)
	gym := f.commitment("50.00", 15)
	oct, nov := f.occurrence(gym.ID, "2026-10"), f.occurrence(gym.ID, "2026-11")
	beforeOct, beforeNov := f.totals("2026-10"), f.totals("2026-11")
	if beforeOct.Planned.String() != "50.00 EUR" || beforeNov.Planned.String() != "50.00 EUR" {
		t.Fatalf("before: October planned %s, November %s", beforeOct.Planned, beforeNov.Planned)
	}

	p := f.plan(oct, "2026-11-20", deferrals.TypePersonal)
	if p.Version != 1 || !p.Active() || p.OriginalDueDate != d("2026-10-15") || p.OriginalAmount.String() != "50.00 EUR" ||
		p.OutstandingWhenPlanned.String() != "50.00 EUR" || p.CommitmentID != gym.ID || *p.Reason != "waiting for salary" {
		t.Fatalf("created: %+v", p)
	}
	st := f.state(oct.ID)
	if st.Status != payments.StatusOverdue || st.Outstanding.String() != "50.00 EUR" || st.EvidenceCount != 0 {
		t.Fatalf("a deferral changed the payment state: %+v", st)
	}
	after, err := f.cs.Occurrences().Get(f.ctx, oct.ID)
	if err != nil || after.Version != oct.Version || after.DueDate != oct.DueDate {
		t.Fatalf("a deferral changed the occurrence: %+v %v", after, err)
	}
	check := func(when string) {
		t.Helper()
		o, n := f.totals("2026-10"), f.totals("2026-11")
		if o.Planned.String() != "0.00 EUR" || o.Outstanding.String() != "50.00 EUR" || o.Overdue.String() != "50.00 EUR" || o.DeferredOut.String() != "50.00 EUR" {
			t.Errorf("%s: October planned %s outstanding %s overdue %s deferred %s", when, o.Planned, o.Outstanding, o.Overdue, o.DeferredOut)
		}
		if n.Planned.String() != "100.00 EUR" || n.Outstanding.String() != "50.00 EUR" || n.CarriedIn.String() != "50.00 EUR" {
			t.Errorf("%s: November planned %s outstanding %s carried in %s, want 100.00, 50.00, 50.00", when, n.Planned, n.Outstanding, n.CarriedIn)
		}
	}
	check("after deferral")

	f.refresh() // a re-import of the same bank data
	check("after re-import")
	if got, err := f.ds.Get(f.ctx, p.ID); err != nil || got.Version != p.Version || !got.Active() {
		t.Fatalf("plan after re-import: %+v %v", got, err)
	}

	u, err := f.ds.Undo(f.ctx, alice, p.ID, p.Version, text("paid it from savings after all"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Active() || u.State() != deferrals.StateUndone || u.Version != 2 {
		t.Fatalf("undone: %+v", u)
	}
	if o, n := f.totals("2026-10"), f.totals("2026-11"); o.Planned.String() != beforeOct.Planned.String() || n.Planned.String() != beforeNov.Planned.String() || n.CarriedIn.String() != "0.00 EUR" {
		t.Errorf("undo did not restore: October planned %s, November planned %s carried %s", o.Planned, n.Planned, n.CarriedIn)
	}
	if got := f.state(oct.ID); got.Status != st.Status || got.Outstanding.String() != st.Outstanding.String() {
		t.Errorf("state after undo %+v, want %+v", got, st)
	}
	_ = nov

	// The history keeps both steps; the undone plan cannot change again, and
	// the occurrence can be deferred anew.
	h, err := f.ds.History(f.ctx, p.ID)
	if err != nil || len(h) != 2 || h[0].Action != deferrals.ActionCreated || h[1].Action != deferrals.ActionUndone || h[1].Version != 2 ||
		*h[1].Reason != "paid it from savings after all" || h[1].By.UserID != alice.UserID || h[1].By.Via != audit.ViaSession {
		t.Fatalf("history: %+v %v", h, err)
	}
	if _, err := f.ds.Revise(f.ctx, alice, p.ID, 2, deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2026-11-25")}, today); !errors.Is(err, deferrals.ErrUndone) {
		t.Errorf("revise an undone plan: %v", err)
	}
	if _, err := f.ds.Undo(f.ctx, alice, p.ID, 2, nil); !errors.Is(err, deferrals.ErrUndone) {
		t.Errorf("undo twice: %v", err)
	}
	again := f.plan(oct, "2026-11-25", deferrals.TypeCreditorAgreed)
	if again.ID == p.ID {
		t.Error("a new plan reused the undone plan's identifier")
	}
	if got := f.rec.actions("deferral_plan."); got != "deferral_plan.created deferral_plan.undone deferral_plan.created" {
		t.Errorf("audit: %s", got)
	}
}

// TestDeferredOccurrenceStillTakesItsPayment: matching after a deferral pays
// the occurrence through the usual evidence; the plan stays as history and
// never becomes the payment. Unmatching reopens it, still planned.
func TestDeferredOccurrenceStillTakesItsPayment(t *testing.T) {
	f := newFixture(t)
	rent := f.commitment("800.00", 2)
	oct := f.occurrence(rent.ID, "2026-10")
	p := f.plan(oct, "2026-11-10", deferrals.TypeCreditorAgreed)
	if n := f.totals("2026-11"); n.CarriedIn.String() != "800.00 EUR" {
		t.Fatalf("November carried in %s", n.CarriedIn)
	}
	ch, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: oct.ID, Amount: money.MustParse("800.00", "EUR"),
		Origin: matches.OriginUser, State: matches.StateConfirmed})
	if err != nil {
		t.Fatal(err)
	}
	if st := f.state(oct.ID); st.Status != payments.StatusPaid || st.BankEvidenceCount != 1 || st.ManualEvidenceCount != 0 {
		t.Fatalf("after match: %+v", st)
	}
	if n := f.totals("2026-11"); n.CarriedIn.String() != "0.00 EUR" || n.Planned.String() != "800.00 EUR" {
		t.Errorf("November after payment: carried %s planned %s, want 0.00 and only November's own 800.00", n.CarriedIn, n.Planned)
	}
	got, err := f.ds.Get(f.ctx, p.ID)
	if err != nil || !got.Active() || got.Version != p.Version {
		t.Fatalf("the plan changed when the occurrence was paid: %+v %v", got, err)
	}
	// A paid occurrence cannot be deferred or re-planned.
	if _, err := f.ds.Revise(f.ctx, alice, p.ID, p.Version, deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2026-11-12")}, today); !errors.Is(err, deferrals.ErrNothingOutstanding) {
		t.Errorf("revise a paid occurrence's plan: %v", err)
	}
	if _, err := f.ms.Unmatch(f.ctx, alice, ch.Match.ID, ch.Match.Version, nil); err != nil {
		t.Fatal(err)
	}
	if n := f.totals("2026-11"); n.CarriedIn.String() != "800.00 EUR" {
		t.Errorf("after unmatch carried in %s, want 800.00 again", n.CarriedIn)
	}
	f.refresh()
	if n := f.totals("2026-11"); n.CarriedIn.String() != "800.00 EUR" {
		t.Errorf("after re-import carried in %s, want 800.00", n.CarriedIn)
	}
}

func TestCreateRefusals(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("50.00", 15)
	oct := f.occurrence(c.ID, "2026-10")
	var ve *commitments.ValidationError
	if _, err := f.ds.Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: oct.ID, Fields: deferrals.Fields{Type: "later", PlannedDate: d("2026-11-20"), Reason: text("  ")}}, today); !errors.As(err, &ve) || len(ve.Errors) != 2 {
		t.Errorf("bad fields: %v", err)
	}
	if _, err := f.ds.Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: oct.ID, Fields: deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2026-10-15")}}, today); !errors.As(err, &ve) || ve.Errors[0].Field != "planned_date" {
		t.Errorf("planned on the due date: %v", err)
	}
	if _, err := f.ds.Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: oct.ID, Fields: deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2032-01-01")}}, today); !errors.As(err, &ve) {
		t.Errorf("planned beyond the horizon: %v", err)
	}
	if _, err := f.ds.Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: "01JD3Y4C8V9M2Q7W5R6T0XZBNB", Fields: deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2026-11-20")}}, today); !errors.Is(err, deferrals.ErrOccurrenceNotFound) {
		t.Errorf("unknown occurrence: %v", err)
	}
	f.plan(oct, "2026-11-20", deferrals.TypePersonal)
	if _, err := f.ds.Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: oct.ID, Fields: deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2026-11-21")}}, today); !errors.Is(err, deferrals.ErrActivePlan) {
		t.Errorf("second active plan: %v", err)
	}
	sep := f.occurrence(c.ID, "2026-09")
	if _, _, err := f.ps.RecordManual(f.ctx, alice, sep.ID, payments.NewManual{Amount: money.MustParse("50.00", "EUR"), PaidOn: d("2026-09-15")}, today); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ds.Create(f.ctx, alice, deferrals.NewPlan{OccurrenceID: sep.ID, Fields: deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2026-11-20")}}, today); !errors.Is(err, deferrals.ErrNothingOutstanding) {
		t.Errorf("paid occurrence: %v", err)
	}
	if got := f.rec.actions("deferral_plan."); got != "deferral_plan.created" {
		t.Errorf("refusals were audited: %s", got)
	}
}

func TestReviseAndAdjustmentsKeepHistory(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("50.00", 15)
	oct := f.occurrence(c.ID, "2026-10")
	p := f.plan(oct, "2026-11-20", deferrals.TypePersonal)
	if _, err := f.ds.Revise(f.ctx, alice, p.ID, p.Version+1, deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2026-12-01")}, today); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Errorf("stale revise: %v", err)
	}
	if _, err := f.ds.Revise(f.ctx, alice, p.ID, p.Version, deferrals.Fields{Type: p.Type, PlannedDate: p.PlannedDate, Reason: p.Reason}, today); !errors.Is(err, deferrals.ErrNoChange) {
		t.Errorf("empty revise: %v", err)
	}
	r, err := f.ds.Revise(f.ctx, alice, p.ID, p.Version, deferrals.Fields{Type: deferrals.TypeDocumentedGrace, PlannedDate: d("2026-12-01")}, today)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 2 || r.Type != deferrals.TypeDocumentedGrace || r.PlannedDate != d("2026-12-01") || r.Reason != nil || r.OriginalDueDate != p.OriginalDueDate {
		t.Fatalf("revised: %+v", r)
	}
	if until, ok := r.GraceUntil(); !ok || until != d("2026-12-01") {
		t.Errorf("grace until %v %v", until, ok)
	}
	if n, dec := f.totals("2026-11"), f.totals("2026-12"); n.CarriedIn.String() != "0.00 EUR" || dec.CarriedIn.String() != "50.00 EUR" {
		t.Errorf("cross-month revision: November carried %s, December %s", n.CarriedIn, dec.CarriedIn)
	}

	var ve *commitments.ValidationError
	if _, _, err := f.ds.AddAdjustment(f.ctx, alice, p.ID, r.Version, deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee, Amount: money.MustParse("5", "USD")}); !errors.As(err, &ve) || ve.Errors[0].Field != "amount.currency" {
		t.Errorf("adjustment in another currency: %v", err)
	}
	r, fee, err := f.ds.AddAdjustment(f.ctx, alice, p.ID, r.Version, deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee, Amount: money.MustParse("4.90", "EUR"), Note: text("late fee per terms")})
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 3 || len(r.Adjustments) != 1 || r.AdjustmentTotal().String() != "4.90 EUR" {
		t.Fatalf("after fee: %+v", r)
	}
	if dec := f.totals("2026-12"); dec.Planned.String() != "104.90 EUR" || dec.Adjustments.String() != "4.90 EUR" || dec.Outstanding.String() != "50.00 EUR" {
		t.Errorf("December planned %s adjustments %s outstanding %s", dec.Planned, dec.Adjustments, dec.Outstanding)
	}
	if st := f.state(oct.ID); st.Outstanding.String() != "50.00 EUR" {
		t.Errorf("a fee changed the obligation: %s", st.Outstanding)
	}
	r, err = f.ds.RemoveAdjustment(f.ctx, alice, p.ID, fee.ID, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 4 || r.AdjustmentTotal().String() != "0.00 EUR" || r.Adjustments[0].RemovedAt == nil {
		t.Fatalf("after removal: %+v", r)
	}
	if _, err := f.ds.RemoveAdjustment(f.ctx, alice, p.ID, fee.ID, r.Version); !errors.Is(err, deferrals.ErrAdjustmentRemoved) {
		t.Errorf("remove twice: %v", err)
	}
	if _, err := f.ds.RemoveAdjustment(f.ctx, alice, p.ID, "01JD3Y4C8V9M2Q7W5R6T0XZBNB", r.Version); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("remove unknown: %v", err)
	}
	h, err := f.ds.History(f.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range h {
		got = append(got, string(e.Action)+"@"+e.PlannedDate.String())
	}
	if strings.Join(got, " ") != "created@2026-11-20 revised@2026-12-01 adjustment_added@2026-12-01 adjustment_removed@2026-12-01" || h[2].AdjustmentID != fee.ID {
		t.Errorf("history: %v", got)
	}
	if got := f.rec.actions("deferral_plan."); got != "deferral_plan.created deferral_plan.revised deferral_plan.adjustment_added deferral_plan.adjustment_removed" {
		t.Errorf("audit: %s", got)
	}
	for _, e := range f.rec.events {
		if e.Action == audit.DeferralPlanRevised && e.Details["fields"] != "plan_type,planned_date,reason" {
			t.Errorf("revised fields %q", e.Details["fields"])
		}
		if _, ok := e.Details["amount"]; ok {
			t.Errorf("%s carries an amount", e.Action)
		}
	}

	list, err := f.ds.List(f.ctx, deferrals.ListFilter{CommitmentIDs: []string{c.ID}, States: []deferrals.State{deferrals.StateActive}, PlannedMonth: ym("2026-12"), Limit: 10})
	if err != nil || len(list) != 1 || list[0].ID != p.ID {
		t.Errorf("list: %v %v", list, err)
	}
	if list, _ := f.ds.List(f.ctx, deferrals.ListFilter{States: []deferrals.State{deferrals.StateUndone}, Limit: 10}); len(list) != 0 {
		t.Errorf("undone list: %v", list)
	}
}

// TestCurrencyChangeWithActivePlanIsRefused: a terms edit that would change
// the currency of an occurrence with an active plan is refused whole (the
// plan's fees are in the occurrence's currency), and nothing changes.
func TestCurrencyChangeWithActivePlanIsRefused(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("50.00", 15)
	dec := f.occurrence(c.ID, "2026-12")
	p := f.plan(dec, "2027-01-15", deferrals.TypePersonal)
	if _, _, err := f.ds.AddAdjustment(f.ctx, alice, p.ID, p.Version, deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee, Amount: money.MustParse("5.00", "EUR")}); err != nil {
		t.Fatal(err)
	}
	sig := func(m string) string {
		x := f.totals(m)
		return strings.Join([]string{x.Due.String(), x.Outstanding.String(), x.Planned.String(), x.DeferredOut.String(), x.CarriedIn.String(), x.Adjustments.String()}, "|")
	}
	beforeDec, beforeJan := sig("2026-12"), sig("2027-01")
	fields := c.Fields
	fields.Amount = money.MustParse("60.00", "USD")
	_, err := f.cs.Update(f.ctx, alice, c.ID, c.Version, fields, d("2026-12-01"), today)
	var cc *occurrences.CurrencyChangePlanError
	if !errors.As(err, &cc) || cc.PlanID != p.ID || cc.OccurrenceID != dec.ID {
		t.Fatalf("currency change with an active plan: %v, want a CurrencyChangePlanError naming the plan", err)
	}
	if got, err := f.cs.Get(f.ctx, c.ID); err != nil || got.Version != c.Version || got.Amount.String() != "50.00 EUR" {
		t.Errorf("the refused edit changed the commitment: %+v %v", got, err)
	}
	if o := f.occurrence(c.ID, "2026-12"); o.Amount.String() != "50.00 EUR" || o.Version != dec.Version {
		t.Errorf("the refused edit changed the occurrence: %+v", o)
	}
	if got := sig("2026-12"); got != beforeDec {
		t.Errorf("December totals changed: %s, want %s", got, beforeDec)
	}
	if got := sig("2027-01"); got != beforeJan || !strings.Contains(got, "|105.00 EUR|") || !strings.HasSuffix(got, "|5.00 EUR") {
		t.Errorf("January totals %s, want planned 105.00 EUR with the 5.00 fee", got)
	}
	// Undoing the plan lets the currency change.
	if _, err := f.ds.Undo(f.ctx, alice, p.ID, p.Version+1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cs.Update(f.ctx, alice, c.ID, c.Version, fields, d("2026-12-01"), today); err != nil {
		t.Errorf("currency change after undo: %v", err)
	}
}

// TestPlanKeepsItsOccurrenceAndHistory: the schema keeps plans, their
// history and their occurrence; a terms edit that no longer schedules a
// deferred occurrence cancels it instead of deleting it.
func TestPlanKeepsItsOccurrenceAndHistory(t *testing.T) {
	f := newFixture(t)
	c := f.commitment("50.00", 15)
	dec := f.occurrence(c.ID, "2026-12")
	p := f.plan(dec, "2027-01-10", deferrals.TypePersonal)
	err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, q := range []string{
			"DELETE FROM deferral_plans",
			"DELETE FROM deferral_plan_events",
			"UPDATE deferral_plan_events SET reason = NULL",
			"UPDATE deferral_plans SET original_amount = '1.00'",
		} {
			if _, err := tx.ExecContext(ctx, q); err == nil {
				return errors.New(q + " was allowed")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	end := d("2026-11-30")
	fields := c.Fields
	fields.EndDate = &end
	if _, err := f.cs.Update(f.ctx, alice, c.ID, c.Version, fields, today, today); err != nil {
		t.Fatal(err)
	}
	o, err := f.cs.Occurrences().Get(f.ctx, dec.ID)
	if err != nil || o.CancelledAt == nil {
		t.Fatalf("the deferred occurrence after an end-date edit: %+v %v", o, err)
	}
	if got, err := f.ds.Get(f.ctx, p.ID); err != nil || got.ID != p.ID {
		t.Fatalf("plan after cancellation: %v", err)
	}
	if _, err := f.ds.Revise(f.ctx, alice, p.ID, p.Version, deferrals.Fields{Type: deferrals.TypePersonal, PlannedDate: d("2027-01-11")}, today); !errors.Is(err, occurrences.ErrCancelled) {
		t.Errorf("revise for a cancelled occurrence: %v", err)
	}
	if _, err := f.ds.Undo(f.ctx, alice, p.ID, p.Version, nil); err != nil {
		t.Errorf("undo for a cancelled occurrence: %v", err)
	}
}
