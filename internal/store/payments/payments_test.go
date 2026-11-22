package payments_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	org "github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	commitmentstore "github.com/aurimasniekis/go-ledger/internal/store/commitments"
	occstore "github.com/aurimasniekis/go-ledger/internal/store/occurrences"
	orgstore "github.com/aurimasniekis/go-ledger/internal/store/organisation"
	paymentstore "github.com/aurimasniekis/go-ledger/internal/store/payments"
)

type recorder struct{ events []audit.Event }

func (r *recorder) WriteAudit(_ context.Context, tx *sql.Tx, e audit.Event) error {
	if tx == nil {
		return errors.New("no transaction")
	}
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.events = append(r.events, e)
	return nil
}

// actions lists the recorded actions with the given details, as
// "action k=v,k=v".
func (r *recorder) actions(keys ...string) []string {
	var out []string
	for _, e := range r.events {
		s := string(e.Action)
		var kv []string
		for _, k := range keys {
			if v, ok := e.Details[k]; ok {
				kv = append(kv, k+"="+v)
			}
		}
		if len(kv) > 0 {
			s += " " + strings.Join(kv, ",")
		}
		out = append(out, s)
	}
	return out
}

var alice = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

// clock is when the fixtures' commitments are created: before the occurrences
// the tests read, so none of them is "not recorded" (D38 1a) unless a test
// says so.
var clock = time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)

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

var today = d("2026-03-15")

type fixture struct {
	path string
	db   *store.DB
	cs   *commitmentstore.Store
	ps   *paymentstore.Store
	rec  *recorder
}

func open(t *testing.T, path string) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := func() time.Time { return clock }
	rec := &recorder{}
	return &fixture{path: path, db: db, cs: commitmentstore.New(db, rec, now, nil), ps: paymentstore.New(db, rec, now, nil), rec: rec}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return open(t, filepath.Join(t.TempDir(), "ledger.db"))
}

// commitment creates a monthly commitment due on the 10th from January 2026
// and returns its occurrence in month m.
func (f *fixture) commitment(t *testing.T, amount, m string) (commitments.Commitment, occurrences.Occurrence) {
	t.Helper()
	c, err := f.cs.Create(context.Background(), alice, commitments.Fields{Name: "Hosting", Kind: commitments.KindServer,
		Amount: money.MustParse(amount, "EUR"), Schedule: commitments.Schedule{Version: 1, Frequency: commitments.FrequencyMonthly, Interval: 1, Day: 10},
		StartDate: d("2026-01-01")})
	if err != nil {
		t.Fatal(err)
	}
	return c, f.occurrence(t, c.ID, m)
}

func (f *fixture) occurrence(t *testing.T, commitmentID, m string) occurrences.Occurrence {
	t.Helper()
	list, err := f.cs.Occurrences().List(context.Background(), occurrences.ListFilter{Month: ym(m), CommitmentIDs: []string{commitmentID}, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("occurrence of %s: %v %v", m, list, err)
	}
	return list[0]
}

func (f *fixture) state(t *testing.T, o occurrences.Occurrence) payments.State {
	t.Helper()
	facts, err := f.ps.Facts(context.Background(), []string{o.ID})
	if err != nil {
		t.Fatal(err)
	}
	return payments.Derive(context.Background(), o, facts[o.ID], today, nil)
}

func (f *fixture) record(t *testing.T, occurrenceID, amount, paidOn string) (payments.ManualPayment, payments.Change) {
	t.Helper()
	p, ch, err := f.ps.RecordManual(context.Background(), alice, occurrenceID, payments.NewManual{Amount: money.MustParse(amount, "EUR"), PaidOn: d(paidOn)}, today)
	if err != nil {
		t.Fatal(err)
	}
	return p, ch
}

func TestManualPaymentsDecideStatusAndSurviveReload(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, o := f.commitment(t, "50.00", "2026-03")
	if st := f.state(t, o); st.Status != payments.StatusOverdue || st.EvidenceCount != 0 {
		t.Fatalf("before any payment: %+v", st)
	}
	note := "paid at the counter"
	p1, ch, err := f.ps.RecordManual(ctx, alice, o.ID, payments.NewManual{Amount: money.MustParse("20.00", "EUR"), PaidOn: d("2026-03-12"), Note: &note}, today)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Occurrence.Version != o.Version+1 || p1.Version != 1 || *p1.Note != note {
		t.Fatalf("after recording: occurrence v%d, payment %+v", ch.Occurrence.Version, p1)
	}
	if st := payments.Derive(context.Background(), ch.Occurrence, ch.Facts, today, nil); st.Status != payments.StatusPartiallyPaid || st.Outstanding.String() != "30.00 EUR" || st.Timing != payments.StatusOverdue {
		t.Fatalf("after 20.00: %+v", st)
	}
	f.record(t, o.ID, "30.00", "2026-03-15")
	if _, err := f.ps.SetOverride(ctx, alice, o.ID, o.Version+2, payments.OverrideUnpaid, "the second transfer bounced"); err != nil {
		t.Fatal(err)
	}

	// Reload: a new process on the same database file sees the same state.
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	g := open(t, f.path)
	o2 := g.occurrence(t, o.CommitmentID, "2026-03")
	st := g.state(t, o2)
	if o2.Version != o.Version+3 || st.Status != payments.StatusOverdue || st.Derived != payments.StatusPaid || st.Paid.String() != "50.00 EUR" ||
		st.Override == nil || st.Override.Reason != "the second transfer bounced" || !st.OverrideApplied {
		t.Fatalf("after reload: v%d %+v", o2.Version, st)
	}
	list, err := g.ps.Manual(ctx, o.ID)
	if err != nil || len(list) != 2 || list[0].ID != p1.ID || list[0].Amount.String() != "20.00 EUR" {
		t.Fatalf("manual payments after reload: %+v %v", list, err)
	}

	// Deleting needs the payment's version; the status is derived again.
	if _, err := g.ps.DeleteManual(ctx, alice, o.ID, list[1].ID, 2); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Fatalf("stale delete: %v", err)
	}
	if _, err := g.ps.DeleteManual(ctx, alice, "01JD3Y4C8V9M2Q7W5R6T0XZBN9", list[1].ID, 1); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("delete through another occurrence: %v", err)
	}
	ch, err = g.ps.DeleteManual(ctx, alice, o.ID, list[1].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st := payments.Derive(context.Background(), ch.Occurrence, ch.Facts, today, nil); st.Derived != payments.StatusPartiallyPaid || ch.Occurrence.Version != o2.Version+1 {
		t.Fatalf("after delete: v%d %+v", ch.Occurrence.Version, st)
	}
	if _, err := g.ps.GetManual(ctx, o.ID, list[1].ID); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("deleted payment still readable: %v", err)
	}
	if want := "manual_payment.deleted currency=EUR"; strings.Join(g.rec.actions("currency"), "|") != want {
		t.Errorf("audit after reload: %v", g.rec.actions("currency"))
	}
	if want := "manual_payment.recorded currency=EUR|manual_payment.recorded currency=EUR|occurrence.status_overridden status=unpaid,previous=none"; strings.Join(f.rec.actions("currency", "status", "previous")[1:], "|") != want {
		t.Errorf("audit: %v", f.rec.actions("currency", "status", "previous"))
	}
}

func TestRecordManualRefusals(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, o := f.commitment(t, "50.00", "2026-03")
	before := len(f.rec.events)
	_, _, err := f.ps.RecordManual(ctx, alice, o.ID, payments.NewManual{Amount: money.MustParse("50.00", "USD"), PaidOn: today}, today)
	var ve *commitments.ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Field != "amount.currency" {
		t.Fatalf("other currency: %v", err)
	}
	if _, _, err := f.ps.RecordManual(ctx, alice, o.ID, payments.NewManual{Amount: money.MustParse("1", "EUR"), PaidOn: d("2026-03-16")}, today); !errors.As(err, &ve) {
		t.Fatalf("future payment date: %v", err)
	}
	if _, _, err := f.ps.RecordManual(ctx, alice, "01JD3Y4C8V9M2Q7W5R6T0XZBN9", payments.NewManual{Amount: money.MustParse("1", "EUR"), PaidOn: today}, today); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unknown occurrence: %v", err)
	}
	if _, err := f.ps.Manual(ctx, "01JD3Y4C8V9M2Q7W5R6T0XZBN9"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("list of unknown occurrence: %v", err)
	}
	if len(f.rec.events) != before {
		t.Errorf("refused writes were audited: %v", f.rec.actions())
	}
	if o2 := f.occurrence(t, o.CommitmentID, "2026-03"); o2.Version != o.Version {
		t.Errorf("refused writes changed the version: %d", o2.Version)
	}
}

func TestOverrideUndoRestoresPriorState(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, o := f.commitment(t, "50.00", "2026-03")
	v := o.Version
	f.rec.events = nil

	// Nothing becomes paid without evidence.
	if _, err := f.ps.SetOverride(ctx, alice, o.ID, v, payments.OverridePaid, "paid in cash"); !errors.Is(err, payments.ErrNoEvidence) {
		t.Fatalf("paid without evidence: %v", err)
	}
	if _, err := f.ps.UndoOverride(ctx, alice, o.ID, v); !errors.Is(err, payments.ErrNoOverride) {
		t.Fatalf("undo with nothing to undo: %v", err)
	}
	if _, err := f.ps.SetOverride(ctx, alice, o.ID, v, "waived", "x"); !errors.As(err, new(*commitments.ValidationError)) {
		t.Fatalf("unknown status: %v", err)
	}
	ch, err := f.ps.SetOverride(ctx, alice, o.ID, v, payments.OverrideUnpaid, "not settled yet")
	if err != nil {
		t.Fatal(err)
	}
	v++
	if ch.Occurrence.Version != v || ch.Facts.Override == nil || ch.Facts.Override.Previous != nil {
		t.Fatalf("first override: %+v", ch)
	}
	if _, err := f.ps.SetOverride(ctx, alice, o.ID, v-1, payments.OverrideUnpaid, "stale"); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Fatalf("stale override: %v", err)
	}
	_, ch = f.record(t, o.ID, "45.00", "2026-03-14") // the creditor accepted 45.00
	v++
	if st := payments.Derive(context.Background(), ch.Occurrence, ch.Facts, today, nil); st.Status != payments.StatusOverdue || st.Derived != payments.StatusPartiallyPaid {
		t.Fatalf("unpaid override over partial evidence: %+v", st)
	}
	ch, err = f.ps.SetOverride(ctx, alice, o.ID, v, payments.OverridePaid, "creditor accepted 45.00")
	if err != nil {
		t.Fatal(err)
	}
	v++
	if st := payments.Derive(context.Background(), ch.Occurrence, ch.Facts, today, nil); st.Status != payments.StatusPaid || *st.Override.Previous != payments.OverrideUnpaid {
		t.Fatalf("paid override: %+v", st)
	}

	// Undo restores the override it replaced, with its reason.
	ch, err = f.ps.UndoOverride(ctx, alice, o.ID, v)
	if err != nil {
		t.Fatal(err)
	}
	v++
	if ov := ch.Facts.Override; ov == nil || ov.Status != payments.OverrideUnpaid || ov.Reason != "not settled yet" || ov.Previous != nil || ch.Occurrence.Version != v {
		t.Fatalf("after the first undo: %+v", ch)
	}
	// Undo again: no override, the derived status applies.
	ch, err = f.ps.UndoOverride(ctx, alice, o.ID, v)
	if err != nil {
		t.Fatal(err)
	}
	v++
	if st := payments.Derive(context.Background(), ch.Occurrence, ch.Facts, today, nil); ch.Facts.Override != nil || st.Status != payments.StatusPartiallyPaid {
		t.Fatalf("after the second undo: %+v", st)
	}
	if _, err := f.ps.UndoOverride(ctx, alice, o.ID, v); !errors.Is(err, payments.ErrNoOverride) {
		t.Fatalf("third undo: %v", err)
	}
	want := []string{
		"occurrence.status_overridden status=unpaid,previous=none",
		"manual_payment.recorded",
		"occurrence.status_overridden status=paid,previous=unpaid",
		"occurrence.status_override_undone undone=paid,restored=unpaid",
		"occurrence.status_override_undone undone=unpaid,restored=none",
	}
	if got := f.rec.actions("status", "previous", "undone", "restored"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("audit\n got %v\nwant %v", got, want)
	}
	// The history is kept: two overrides, both undone.
	var states string
	if err := f.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT group_concat(status || ':' || state, ' ') FROM (SELECT status, state FROM occurrence_status_overrides ORDER BY set_at, status)").Scan(&states)
	}); err != nil {
		t.Fatal(err)
	}
	if states != "paid:undone unpaid:undone" {
		t.Errorf("override history = %q", states)
	}
}

// TestEvidenceKeepsHistoryThroughEditsAndDeletes checks C2's hooks with real
// evidence: an edit never rewrites an occurrence with a manual payment
// (partial payments included), a commitment with one cannot be deleted, and
// an override alone is not evidence.
func TestEvidenceKeepsHistoryThroughEditsAndDeletes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c, mar := f.commitment(t, "50.00", "2026-03")
	apr := f.occurrence(t, c.ID, "2026-04")
	f.record(t, apr.ID, "10.00", "2026-03-14") // partial, paid early
	f.rec.events = nil
	next := c.Fields
	next.Amount = money.MustParse("60.00", "EUR")
	if _, err := f.cs.Update(ctx, alice, c.ID, c.Version, next, d("2026-03-01"), today); err != nil {
		t.Fatal(err)
	}
	if got := f.rec.events[len(f.rec.events)-1].Details; got["kept_paid"] != "1" || got["updated"] != "1" {
		t.Fatalf("regeneration: %v", got)
	}
	if o := f.occurrence(t, c.ID, "2026-04"); o.Amount.String() != "50.00 EUR" || o.Version != apr.Version+1 {
		t.Errorf("paid April occurrence changed: %+v", o)
	}
	if o := f.occurrence(t, c.ID, "2026-03"); o.Amount.String() != "60.00 EUR" || o.ID != mar.ID {
		t.Errorf("unpaid March occurrence not rewritten: %+v", o)
	}
	cur, err := f.cs.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cs.Delete(ctx, alice, c.ID, cur.Version); !errors.Is(err, commitments.ErrInUse) {
		t.Fatalf("delete with payment evidence: %v", err)
	}

	// An override is not evidence: the commitment and its overrides go.
	c2, o2 := f.commitment(t, "5.00", "2026-03")
	if _, err := f.ps.SetOverride(ctx, alice, o2.ID, o2.Version, payments.OverrideUnpaid, "disputed"); err != nil {
		t.Fatal(err)
	}
	if err := f.cs.Delete(ctx, alice, c2.ID, c2.Version); err != nil {
		t.Fatalf("delete with an override only: %v", err)
	}
	var n int
	if err := f.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM occurrence_status_overrides WHERE occurrence_id = ?", o2.ID).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("overrides of a deleted occurrence: %d %v", n, err)
	}
}

// TestEvidenceSourcesMatchDefaultEvidence: every table that yields payment
// evidence also makes occurrences paid (immutable) in C2's regeneration, so
// the two lists cannot drift apart when X1 adds bank allocations.
func TestEvidenceSourcesMatchDefaultEvidence(t *testing.T) {
	from := regexp.MustCompile(`\bFROM\s+([a-z_]+)`)
	if len(paymentstore.EvidenceSources) != len(occstore.DefaultEvidence) {
		t.Fatalf("%d evidence sources, %d DefaultEvidence expressions", len(paymentstore.EvidenceSources), len(occstore.DefaultEvidence))
	}
	for _, src := range paymentstore.EvidenceSources {
		m := from.FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("no table in %q", src)
		}
		found := false
		for _, e := range occstore.DefaultEvidence {
			found = found || strings.Contains(e, "FROM "+m[1]+" ")
		}
		if !found {
			t.Errorf("evidence table %s is not in occurrences.DefaultEvidence", m[1])
		}
	}
	// And the paid flag C2 reads agrees with the facts read here.
	ctx := context.Background()
	f := newFixture(t)
	c, o := f.commitment(t, "50.00", "2026-03")
	f.record(t, o.ID, "1.00", "2026-03-01")
	var existing []occurrences.Existing
	if err := f.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		existing, err = f.cs.Occurrences().ExistingTx(ctx, tx, c.ID, ym("2026-03"), ym("2026-03"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(existing) != 1 || !existing[0].Paid {
		t.Errorf("occurrence with a manual payment is not paid for C2: %+v", existing)
	}
}

// TestConcurrentOverridesOneWins: writers based on the same occurrence
// version race; exactly one wins and the rest get a version conflict.
func TestConcurrentOverridesOneWins(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, o := f.commitment(t, "50.00", "2026-03")
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := f.ps.SetOverride(ctx, alice, o.ID, o.Version, payments.OverrideUnpaid, "race")
			errs <- err
		}()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		err := <-errs
		switch {
		case err == nil:
			wins++
		case !errors.As(err, new(*resource.VersionConflictError)):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("%d writers won", wins)
	}
}

// TestDeleteManualKeepsTheRow: deleting is a soft delete (D26-3a). The row
// stays, marked, out of the evidence, the lists and the totals, and the audit
// event carries the amount; the occurrence stays protected from removal.
func TestDeleteManualKeepsTheRow(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c, o := f.commitment(t, "50.00", "2026-03")
	p1, _ := f.record(t, o.ID, "20.00", "2026-03-12")
	p2, _ := f.record(t, o.ID, "30.00", "2026-03-15")
	if _, err := f.ps.DeleteManual(ctx, alice, o.ID, p2.ID, p2.Version+1); !errors.As(err, new(*resource.VersionConflictError)) {
		t.Fatalf("stale delete: %v", err)
	}
	ch, err := f.ps.DeleteManual(ctx, alice, o.ID, p2.ID, p2.Version)
	if err != nil {
		t.Fatal(err)
	}
	st := payments.Derive(context.Background(), ch.Occurrence, ch.Facts, today, nil)
	if st.Paid.String() != "20.00 EUR" || st.EvidenceCount != 1 || st.Derived != payments.StatusPartiallyPaid {
		t.Errorf("totals still count the deleted payment: %+v", st)
	}
	list, err := f.ps.Manual(ctx, o.ID)
	if err != nil || len(list) != 1 || list[0].ID != p1.ID {
		t.Errorf("list: %+v %v", list, err)
	}
	if _, err := f.ps.GetManual(ctx, o.ID, p2.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
	if _, err := f.ps.DeleteManual(ctx, alice, o.ID, p2.ID, p2.Version); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	var at, by sql.NullString
	var amount string
	if err := f.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT amount, deleted_at, deleted_by FROM manual_payments WHERE id = ?", p2.ID).Scan(&amount, &at, &by)
	}); err != nil {
		t.Fatalf("the row is gone: %v", err)
	}
	if amount != "30.00" || !at.Valid || by.String != "user:"+alice.UserID {
		t.Errorf("row: %q %v %v", amount, at, by)
	}
	last := f.rec.events[len(f.rec.events)-1]
	if last.Action != audit.ManualPaymentDeleted || last.Details["amount"] != "30.00" || last.Details["currency"] != "EUR" {
		t.Errorf("audit: %+v", last)
	}
	// An override and its undo keep working after a delete.
	set, err := f.ps.SetOverride(ctx, alice, o.ID, ch.Occurrence.Version, payments.OverridePaid, "creditor accepted less")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ps.UndoOverride(ctx, alice, o.ID, set.Occurrence.Version); err != nil {
		t.Fatalf("undo after a delete: %v", err)
	}
	// With every payment deleted, nothing is live evidence: a commitment delete
	// cancels the occurrence (D31) and keeps its history instead of failing.
	if _, err := f.ps.DeleteManual(ctx, alice, o.ID, p1.ID, 1); err != nil {
		t.Fatal(err)
	}
	cur := f.occurrence(t, c.ID, "2026-03")
	got, err := f.cs.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st := f.state(t, cur); st.EvidenceCount != 0 || st.OverrideApplied {
		t.Errorf("deleted payments still evidence: %+v", st)
	}
	if err := f.cs.Delete(ctx, alice, c.ID, got.Version); err != nil {
		t.Fatalf("commitment delete after deleting every payment: %v (occurrence v%d)", err, cur.Version)
	}
	kept, err := f.cs.Occurrences().Get(ctx, o.ID)
	if err != nil || kept.CancelledAt == nil || kept.CancelledReason != occurrences.CancelCommitmentDelete {
		t.Errorf("occurrence after the commitment delete: %+v, %v", kept, err)
	}
	if _, err := f.cs.Get(ctx, c.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("deleted commitment: %v", err)
	}
}

// TestPaidOverrideNeedsAnyEvidenceWhateverItsCurrency: ADR-0031 point 6
// (decision D26-4a). Manual payments are always in the occurrence's currency,
// so the other-currency evidence is inserted directly, as a bank allocation
// would be.
func TestPaidOverrideNeedsAnyEvidenceWhateverItsCurrency(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, o := f.commitment(t, "50.00", "2026-03")
	if _, err := f.ps.SetOverride(ctx, alice, o.ID, o.Version, payments.OverridePaid, "settled"); !errors.Is(err, payments.ErrNoEvidence) {
		t.Fatalf("no evidence: %v", err)
	}
	if err := f.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO manual_payments (id, occurrence_id, amount, currency, paid_on, created_at, updated_at) VALUES ('01JZ0000000000000000000900', ?, '60.00', 'USD', '2026-03-10', '2026-03-10T00:00:00.000000000Z', '2026-03-10T00:00:00.000000000Z')", o.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ch, err := f.ps.SetOverride(ctx, alice, o.ID, o.Version, payments.OverridePaid, "settled in dollars")
	if err != nil {
		t.Fatalf("paid override with other-currency evidence: %v", err)
	}
	if st := payments.Derive(context.Background(), ch.Occurrence, ch.Facts, today, nil); st.Status != payments.StatusPaid || st.Paid.String() != "0.00 EUR" || !st.OverrideApplied {
		t.Errorf("state: %+v", st)
	}
}

// TestManualPaymentTriggers: the database itself keeps a deleted payment
// deleted, deleted with both marks, and never removed (D26-3a).
func TestManualPaymentTriggers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, o := f.commitment(t, "50.00", "2026-03")
	live, _ := f.record(t, o.ID, "20.00", "2026-03-12")
	gone, _ := f.record(t, o.ID, "30.00", "2026-03-15")
	if _, err := f.ps.DeleteManual(ctx, alice, o.ID, gone.ID, gone.Version); err != nil {
		t.Fatal(err)
	}
	stamp := "2026-03-20T00:00:00.000000000Z"
	for _, c := range []struct{ name, stmt string }{
		{"undelete", "UPDATE manual_payments SET deleted_at = NULL, deleted_by = NULL WHERE id = '" + gone.ID + "'"},
		{"redate a deleted payment", "UPDATE manual_payments SET deleted_at = '" + stamp + "' WHERE id = '" + gone.ID + "'"},
		{"mark without an actor", "UPDATE manual_payments SET deleted_at = '" + stamp + "' WHERE id = '" + live.ID + "'"},
		{"actor without a mark", "UPDATE manual_payments SET deleted_by = 'system' WHERE id = '" + live.ID + "'"},
		{"hard delete a live payment", "DELETE FROM manual_payments WHERE id = '" + live.ID + "'"},
		{"hard delete a deleted payment", "DELETE FROM manual_payments WHERE id = '" + gone.ID + "'"},
	} {
		err := f.db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, c.stmt)
			return err
		})
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// TestNotRecordedFollowsCommitmentCreation pins D38 1a through the store: the
// commitment is created on 2026-02-15 (the fixture clock), so the 10 February
// occurrence is not recorded and the 10 March one is overdue; the first can
// still be paid.
func TestNotRecordedFollowsCommitmentCreation(t *testing.T) {
	f := newFixture(t)
	c, march := f.commitment(t, "50.00", "2026-03")
	feb := f.occurrence(t, c.ID, "2026-02")
	if st := f.state(t, feb); st.Status != payments.StatusNotRecorded || st.Timing != payments.StatusNotRecorded {
		t.Fatalf("february: %+v", st)
	}
	if st := f.state(t, march); st.Status != payments.StatusOverdue {
		t.Fatalf("march: %+v", st)
	}
	f.record(t, feb.ID, "50.00", "2026-02-12")
	if st := f.state(t, feb); st.Status != payments.StatusPaid {
		t.Fatalf("february paid: %+v", st)
	}
}

// TestNotRecordedUsesTheInjectedReportingZone (review of C3i, F4 and F5): the
// reporting zone is a property of the store, not a global. A commitment
// created at 2026-03-10T23:30Z is created on 11 March in Europe/Vilnius
// (UTC+2), so its 10 March occurrence is not recorded there, while the same
// data read in UTC dates the creation 10 March and the occurrence is overdue.
func TestNotRecordedUsesTheInjectedReportingZone(t *testing.T) {
	saved := clock
	t.Cleanup(func() { clock = saved })
	clock = time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC)
	f := newFixture(t)
	_, march := f.commitment(t, "50.00", "2026-03")
	read := func(s *paymentstore.Store) payments.State {
		t.Helper()
		facts, err := s.Facts(context.Background(), []string{march.ID})
		if err != nil {
			t.Fatal(err)
		}
		return payments.Derive(context.Background(), march, facts[march.ID], today, nil)
	}
	if st := read(f.ps); st.Status != payments.StatusOverdue {
		t.Fatalf("UTC: %+v", st)
	}
	vilnius := f.ps.WithZone(calendar.MustLoadZone("Europe/Vilnius"))
	st := read(vilnius)
	if st.Status != payments.StatusNotRecorded {
		t.Fatalf("Vilnius: %+v", st)
	}
	if got := read(f.ps); got.Status != payments.StatusOverdue {
		t.Fatalf("WithZone must not change the original store: %+v", got)
	}
}

// TestKeptCommitmentRowDropsItsOrganisation: when a commitment delete keeps the
// row (D31: only deleted payments refer to an occurrence), the buckets,
// category and labels it was filed under go with it, in the same transaction.
// Otherwise a bucket would list, and a category refuse deletion over, a
// commitment no read shows.
func TestKeptCommitmentRowDropsItsOrganisation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	c, o := f.commitment(t, "50.00", "2026-03")
	p, _ := f.record(t, o.ID, "50.00", "2026-03-12")
	os := orgstore.New(f.db, f.rec, func() time.Time { return clock }, nil)
	mk := func(k org.Kind, name string) org.Definition {
		t.Helper()
		def, err := os.CreateDefinition(ctx, alice, k, org.Fields{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return def
	}
	cat, lbl, bucket := mk(org.KindCategory, "Housing"), mk(org.KindLabel, "Fixed"), mk(org.KindBucket, "Essentials")
	obj := org.ObjectRef{Type: org.ObjectCommitment, ID: c.ID}
	if _, err := os.SetCategory(ctx, alice, obj, &cat.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.AddLabel(ctx, alice, obj, lbl.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.AddToBucket(ctx, alice, bucket.ID, obj); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ps.DeleteManual(ctx, alice, o.ID, p.ID, p.Version); err != nil {
		t.Fatal(err)
	}
	got, err := f.cs.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cs.Delete(ctx, alice, c.ID, got.Version); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := f.db.ReadTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM commitments WHERE id = ? AND deleted_at IS NOT NULL", c.ID).Scan(&kept)
	}); err != nil || kept != 1 {
		t.Fatalf("the commitment row was not kept (the path under test): %d %v", kept, err)
	}
	members, err := os.ListMembers(ctx, bucket.ID, org.MemberFilter{Limit: 10})
	if err != nil || len(members) != 0 {
		t.Errorf("a deleted commitment is still a bucket member: %+v %v", members, err)
	}
	for _, d := range []org.Definition{cat, lbl, bucket} {
		if _, err := os.DeleteDefinition(ctx, alice, d.Kind, d.ID, d.Version, ""); err != nil {
			t.Errorf("a %s is still held by the deleted commitment: %v", d.Kind, err)
		}
	}
}
