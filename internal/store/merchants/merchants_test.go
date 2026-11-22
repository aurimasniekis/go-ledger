package merchants_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	merchantstore "github.com/aurimasniekis/go-ledger/internal/store/merchants"
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

var alice = audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNA"}

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

type fixture struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	now    time.Time
	facts  *bankfacts.Store
	ms     *merchantstore.Store
	rec    *recorder
	p      *bankingtest.Provider
	engine *bankimport.Engine
	conn   banking.Connection
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{t: t, ctx: ctx, db: db, now: t0, rec: &recorder{}, facts: bankfacts.New(db, nil)}
	f.ms = merchantstore.New(db, f.rec, func() time.Time { return f.now })
	f.p = bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: func() time.Time { return f.now }, PageSize: 3}, bankingtest.Standard())
	f.engine = bankimport.New(f.facts, bankimport.Options{Now: func() time.Time { return f.now }, Sleep: func(context.Context, time.Duration) error { return nil },
		Jitter: func(d time.Duration) time.Duration { return d }})
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

func identified(name string, conf merchants.Confidence) merchants.Answer {
	return merchants.Answer{Status: merchants.StatusIdentified, Name: name, LogoRef: "logo:" + strings.ToLower(name), Source: "curated", Confidence: conf}
}

func (f *fixture) record(id banking.TransactionID, a merchants.Answer) merchants.Merchant {
	f.t.Helper()
	m, err := f.ms.RecordAnswer(f.ctx, id, a)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) review(id banking.TransactionID, r merchants.Request) merchants.Merchant {
	f.t.Helper()
	m, err := f.ms.Review(f.ctx, alice, id, r)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) events() []string {
	var out []string
	for _, e := range f.rec.events {
		out = append(out, string(e.Action)+" from="+e.Details["from"]+" via="+e.Details["via"])
	}
	return out
}

func TestReliableAmbiguousNoneAndNotLookedUp(t *testing.T) {
	f := newFixture(t)
	streaming, coffee, salary, rent := f.tx("STREAMING SERVICE"), f.tx("COFFEE KIOSK"), f.tx("SALARY OCTOBER"), f.tx("RENT 2026-10")

	if m, err := f.ms.Get(f.ctx, rent.ID); err != nil || m.State != merchants.StateNotLookedUp || !m.Fallback() || m.NeedsReview {
		t.Fatalf("before any lookup: %+v %v", m, err)
	}

	m := f.record(streaming.ID, identified("Streamly", 950))
	if m.State != merchants.StateReliable || m.Name != "Streamly" || m.LogoRef != "logo:streamly" || m.IdentitySource != "curated" || m.Enrichment.RetrievedAt != t0 || m.Enrichment.Confidence != 950 {
		t.Fatalf("reliable: %+v", m)
	}

	m = f.record(coffee.ID, merchants.Answer{Status: merchants.StatusAmbiguous, Source: "curated", Candidates: []string{"Kiosk B", "Kiosk A"}})
	if m.State != merchants.StateAmbiguous || !m.Fallback() || !m.NeedsReview || !reflect.DeepEqual(m.Enrichment.Candidates, []string{"Kiosk A", "Kiosk B"}) {
		t.Fatalf("ambiguous: %+v", m)
	}

	m = f.record(salary.ID, merchants.Answer{Status: merchants.StatusNone, Source: "curated"})
	if m.State != merchants.StateNone || !m.Fallback() || m.NeedsReview {
		t.Fatalf("none: %+v", m)
	}

	m = f.record(rent.ID, identified("Landlord", 300))
	if m.State != merchants.StateUncertain || !m.Fallback() || !m.NeedsReview || m.Enrichment.Name != "Landlord" {
		t.Fatalf("uncertain: %+v", m)
	}
	// The same data resolves differently under another threshold: the store takes it as an option.
	if m, err := f.ms.WithThreshold(300).Get(f.ctx, rent.ID); err != nil || m.State != merchants.StateReliable {
		t.Fatalf("threshold option: %+v %v", m, err)
	}
}

func TestOutageNeverErasesWhatIsKnown(t *testing.T) {
	f := newFixture(t)
	a, b := f.tx("STREAMING SERVICE"), f.tx("COFFEE KIOSK")
	down := merchants.Answer{Status: merchants.StatusUnavailable, Source: "curated"}

	// Nothing known: the outage is explicit and shows no identity.
	f.now = t0.Add(time.Hour)
	m := f.record(a.ID, down)
	if m.State != merchants.StateUnavailable || !m.Fallback() || m.Enrichment.LastFailedAt == nil || !m.Enrichment.RetrievedAt.IsZero() {
		t.Fatalf("outage before any answer: %+v", m)
	}

	// An earlier answer survives a later outage and only records the failure.
	f.record(b.ID, identified("Kiosk", 900))
	f.now = t0.Add(2 * time.Hour)
	m = f.record(b.ID, down)
	if m.State != merchants.StateReliable || m.Name != "Kiosk" || !m.Enrichment.RetrievedAt.Equal(t0.Add(time.Hour)) || m.Enrichment.LastFailedAt == nil || !m.Enrichment.LastFailedAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("outage after an answer: %+v", m)
	}

	// A fresh answer after the outage replaces the unavailable row and clears the failure.
	f.now = t0.Add(3 * time.Hour)
	m = f.record(a.ID, identified("Streamly", 900))
	if m.State != merchants.StateReliable || m.Enrichment.LastFailedAt != nil || m.Enrichment.RetrievedAt != t0.Add(3*time.Hour) {
		t.Fatalf("answer after outage: %+v", m)
	}
}

func TestConfirmCorrectRejectAndClear(t *testing.T) {
	f := newFixture(t)
	id := f.tx("STREAMING SERVICE").ID

	if _, err := f.ms.Review(f.ctx, alice, id, merchants.Request{Decision: merchants.DecisionConfirmed}); !errors.Is(err, merchants.ErrNothingToConfirm) {
		t.Fatalf("confirm without enrichment: %v", err)
	}
	f.record(id, merchants.Answer{Status: merchants.StatusAmbiguous, Source: "curated", Candidates: []string{"A", "B"}})
	if _, err := f.ms.Review(f.ctx, alice, id, merchants.Request{Decision: merchants.DecisionConfirmed}); !errors.Is(err, merchants.ErrNothingToConfirm) {
		t.Fatalf("confirm an ambiguous answer: %v", err)
	}

	// An ambiguous answer is resolved by correcting it.
	f.now = t0.Add(time.Minute)
	m := f.review(id, merchants.Request{Decision: merchants.DecisionCorrected, Name: "  My   Streamer "})
	if m.State != merchants.StateCorrected || m.Name != "My Streamer" || m.LogoRef != "" || m.IdentitySource != "user" || m.NeedsReview || m.Review.Version != 1 || m.Review.DecidedAt != t0.Add(time.Minute) || m.Review.By.UserID != alice.UserID {
		t.Fatalf("correct: %+v", m)
	}

	// The enrichment is still the source's own, unchanged by the correction.
	if m.Enrichment.Status != merchants.StatusAmbiguous || len(m.Enrichment.Candidates) != 2 {
		t.Fatalf("enrichment altered: %+v", m.Enrichment)
	}

	// A new lookup answer does not undo the correction.
	m = f.record(id, identified("Streamly", 990))
	if m.State != merchants.StateCorrected || m.Name != "My Streamer" {
		t.Fatalf("correction after a new answer: %+v", m)
	}

	// Decisions replace each other and count versions.
	m = f.review(id, merchants.Request{Decision: merchants.DecisionConfirmed})
	if m.State != merchants.StateConfirmed || m.Name != "Streamly" || m.Review.Name != "Streamly" || m.Review.Version != 2 {
		t.Fatalf("confirm: %+v", m)
	}
	m = f.review(id, merchants.Request{Decision: merchants.DecisionRejected})
	if m.State != merchants.StateRejected || !m.Fallback() || m.Review.Version != 3 || m.Review.Name != "" {
		t.Fatalf("reject: %+v", m)
	}

	// Clearing goes back to the enrichment alone.
	m, err := f.ms.ClearReview(f.ctx, alice, id)
	if err != nil || m.State != merchants.StateReliable || m.Review != nil {
		t.Fatalf("clear: %+v %v", m, err)
	}
	if _, err := f.ms.ClearReview(f.ctx, alice, id); !errors.Is(err, merchants.ErrNoReview) {
		t.Fatalf("clear twice: %v", err)
	}

	want := []string{
		"merchant.corrected from=none via=session", "merchant.confirmed from=corrected via=session",
		"merchant.rejected from=confirmed via=session", "merchant.review_cleared from=rejected via=session",
	}
	if got := f.events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	for _, e := range f.rec.events {
		for k, v := range e.Details {
			if strings.Contains(v, "Streamer") || strings.Contains(v, "Streamly") {
				t.Errorf("audit detail %s carries a merchant name: %q", k, v)
			}
		}
	}
}

func TestStaleConfirmationDoesNotApply(t *testing.T) {
	f := newFixture(t)
	id := f.tx("STREAMING SERVICE").ID
	f.record(id, identified("Streamly", 400))
	m := f.review(id, merchants.Request{Decision: merchants.DecisionConfirmed})
	if m.State != merchants.StateConfirmed || m.Name != "Streamly" {
		t.Fatalf("a low-confidence name the person confirmed is shown: %+v", m)
	}
	m = f.record(id, identified("Streamly Media", 400))
	if m.State != merchants.StateUncertain || !m.ConfirmationOutdated || !m.NeedsReview || !m.Fallback() {
		t.Fatalf("the source now says another name: %+v", m)
	}
}

func TestCorrectionSurvivesReimport(t *testing.T) {
	f := newFixture(t)
	pending := f.tx("CARD PURCHASE BOOKSHOP")
	plain := f.tx("STREAMING SERVICE")
	f.record(pending.ID, identified("Bookshop Ltd", 900))
	f.record(plain.ID, identified("Streamly", 900))
	f.review(pending.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Corner Books"})
	f.review(plain.ID, merchants.Request{Decision: merchants.DecisionRejected})

	// A plain re-import, then the pending item booking in place: the bank
	// transaction row is updated, not replaced, and the decisions stay.
	f.now = t0.Add(24 * time.Hour)
	f.refresh()
	f.p.Mutate(bankingtest.BookPending)
	f.now = t0.Add(48 * time.Hour)
	f.refresh()

	after := f.tx("CARD PURCHASE BOOKSHOP")
	if after.ID != pending.ID || after.Status != banking.StatusBooked || after.Amount.String() != pending.Amount.String() || !after.ImportedAt.Equal(pending.ImportedAt) {
		t.Fatalf("the re-import did not update the row in place: before %+v after %+v", pending, after)
	}
	m, err := f.ms.Get(f.ctx, pending.ID)
	if err != nil || m.State != merchants.StateCorrected || m.Name != "Corner Books" || m.Review.DecidedAt != t0 {
		t.Fatalf("correction after re-import: %+v %v", m, err)
	}
	if m, err := f.ms.Get(f.ctx, plain.ID); err != nil || m.State != merchants.StateRejected || !m.Fallback() {
		t.Fatalf("rejection after re-import: %+v %v", m, err)
	}
	// Raw bank text is untouched by anything merchants did.
	if after.Description != "CARD PURCHASE BOOKSHOP" || f.tx("STREAMING SERVICE").Description != "STREAMING SERVICE" {
		t.Fatal("bank description changed")
	}
}

// link makes old the superseded pending row of booked, as the bank-facts
// refresh does when a booking arrives under a new key.
func (f *fixture) link(old, booked banking.TransactionID) {
	f.t.Helper()
	err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", string(booked), string(old))
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func TestDecisionSurvivesSupersededPending(t *testing.T) {
	f := newFixture(t)
	pending := f.tx("CARD PURCHASE BOOKSHOP")
	booked := f.tx("STREAMING SERVICE")
	other := f.tx("SALARY ACME")
	f.record(pending.ID, identified("Bookshop Ltd", 900))
	f.record(booked.ID, identified("Streamly", 900))
	f.record(other.ID, identified("Acme Payroll", 900))

	// Nothing is inherited before the link, or from an undecided row.
	f.link(pending.ID, booked.ID)
	if m, err := f.ms.Get(f.ctx, booked.ID); err != nil || m.ReviewInherited || m.Review != nil || m.State != merchants.StateReliable {
		t.Fatalf("no decision to inherit: %+v %v", m, err)
	}

	// A correction on the pending row stands for its booking.
	f.review(pending.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Corner Books"})
	m, err := f.ms.Get(f.ctx, booked.ID)
	if err != nil || m.State != merchants.StateCorrected || m.Name != "Corner Books" || !m.ReviewInherited || m.Review == nil || m.Review.TransactionID != pending.ID {
		t.Fatalf("inherited correction: %+v %v", m, err)
	}
	if m, _ := f.ms.Get(f.ctx, pending.ID); m.ReviewInherited || m.State != merchants.StateCorrected {
		t.Fatalf("the superseded row's own decision is not inherited: %+v", m)
	}

	// A rejection is inherited too, and the booking's own decision wins.
	f.review(pending.ID, merchants.Request{Decision: merchants.DecisionRejected})
	if m, _ := f.ms.Get(f.ctx, booked.ID); m.State != merchants.StateRejected || !m.ReviewInherited || !m.Fallback() {
		t.Fatalf("inherited rejection: %+v", m)
	}
	own := f.review(booked.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Streamly Media"})
	if own.State != merchants.StateCorrected || own.Name != "Streamly Media" || own.ReviewInherited {
		t.Fatalf("own decision: %+v", own)
	}
	if _, err := f.ms.ClearReview(f.ctx, alice, booked.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.ms.Get(f.ctx, booked.ID); m.State != merchants.StateRejected || !m.ReviewInherited {
		t.Fatalf("after clearing the booking's own decision the inherited one applies again: %+v", m)
	}

	// A chain: other supersedes booked, which supersedes pending.
	f.link(booked.ID, other.ID)
	if m, _ := f.ms.Get(f.ctx, other.ID); m.State != merchants.StateRejected || !m.ReviewInherited {
		t.Fatalf("inherited along a chain: %+v", m)
	}
	// Nothing about the bank rows changed.
	if f.tx("SALARY ACME").Description != "SALARY ACME" {
		t.Fatal("bank description changed")
	}
}

func TestUnknownTransactionAndInvalidInput(t *testing.T) {
	f := newFixture(t)
	const missing = banking.TransactionID("01JZ0000000000000000009999")
	if _, err := f.ms.Get(f.ctx, missing); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	if _, err := f.ms.RecordAnswer(f.ctx, missing, identified("X", 900)); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if _, err := f.ms.Review(f.ctx, alice, missing, merchants.Request{Decision: merchants.DecisionRejected}); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("Review: %v", err)
	}
	if _, err := f.ms.ClearReview(f.ctx, alice, missing); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("ClearReview: %v", err)
	}
	id := f.tx("COFFEE KIOSK").ID
	var ve *commitments.ValidationError
	if _, err := f.ms.Review(f.ctx, alice, id, merchants.Request{Decision: merchants.DecisionCorrected}); !errors.As(err, &ve) {
		t.Fatalf("empty correction: %v", err)
	}
	if _, err := f.ms.RecordAnswer(f.ctx, id, merchants.Answer{Status: merchants.StatusIdentified, Source: "curated"}); !errors.As(err, &ve) {
		t.Fatalf("nameless identified answer: %v", err)
	}
	if len(f.rec.events) != 0 {
		t.Fatalf("a refused change was audited: %v", f.events())
	}
}

// TestAuditFailureRollsBackTheDecision pins that a decision and its audit
// event are one transaction.
func TestAuditFailureRollsBackTheDecision(t *testing.T) {
	f := newFixture(t)
	id := f.tx("COFFEE KIOSK").ID
	bad := merchantstore.New(f.db, failingAudit{}, func() time.Time { return t0 })
	if _, err := bad.Review(f.ctx, alice, id, merchants.Request{Decision: merchants.DecisionRejected}); err == nil {
		t.Fatal("review succeeded although the audit write failed")
	}
	if m, err := f.ms.Get(f.ctx, id); err != nil || m.Review != nil {
		t.Fatalf("decision kept after a failed audit: %+v %v", m, err)
	}
}

type failingAudit struct{}

func (failingAudit) WriteAudit(context.Context, *sql.Tx, audit.Event) error {
	return errors.New("audit down")
}

func TestBankFactsAreNotWritten(t *testing.T) {
	f := newFixture(t)
	tx := f.tx("STREAMING SERVICE")
	snapshot := func() string {
		var s string
		err := f.db.ReadTx(f.ctx, func(ctx context.Context, q *sql.Tx) error {
			return q.QueryRowContext(ctx, "SELECT group_concat(id || '|' || source_amount || '|' || source_currency || '|' || raw_description || '|' || counterparty_name || '|' || ifnull(entry_reference,'') || '|' || booking_status || '|' || last_seen_at, ';') FROM (SELECT * FROM bank_transactions ORDER BY id)").Scan(&s)
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	f.record(tx.ID, identified("Streamly", 900))
	f.review(tx.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Mine"})
	if _, err := f.ms.ClearReview(f.ctx, alice, tx.ID); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); after != before {
		t.Fatal("bank transactions changed")
	}
}

func TestReviewBlocksPurgeButEnrichmentDoesNot(t *testing.T) {
	f := newFixture(t)
	id := f.tx("STREAMING SERVICE").ID
	f.record(id, identified("Streamly", 900))
	conn, err := f.facts.Connection(f.ctx, f.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Enrichment alone is derived data and goes with the transactions.
	if _, err := f.facts.PurgeConnection(f.ctx, f.conn.ID, conn.Version); err != nil {
		t.Fatalf("purge with enrichment only: %v", err)
	}
}

func TestPurgeBlockedByDecision(t *testing.T) {
	f := newFixture(t)
	id := f.tx("STREAMING SERVICE").ID
	f.review(id, merchants.Request{Decision: merchants.DecisionRejected})
	conn, err := f.facts.Connection(f.ctx, f.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.facts.PurgeConnection(f.ctx, f.conn.ID, conn.Version); !errors.Is(err, banking.ErrPurgeBlocked) {
		t.Fatalf("purge with a decision: %v", err)
	}
}
