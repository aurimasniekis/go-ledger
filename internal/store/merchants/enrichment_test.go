package merchants_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/enrichment"
)

func newEnricher(f *fixture, r *enrichment.Remote) *enrichment.Service {
	return &enrichment.Service{Store: f.ms, Curated: enrichment.NewCurated(enrichment.DefaultCurated()), Learned: enrichment.NewLearned(),
		Remote: r, Now: func() time.Time { return f.now }}
}

// TestEnrichmentRunOverStoredTransactions runs the service over imported
// transactions: local answers, the person's confirmation reused as a
// suggestion only, decisions untouched, and an idempotent second pass.
func TestEnrichmentRunOverStoredTransactions(t *testing.T) {
	f := newFixture(t)
	svc := newEnricher(f, nil)
	rep, err := svc.Run(f.ctx)
	if err != nil || rep.Looked == 0 || rep.Failed != 0 {
		t.Fatalf("first pass: %+v %v", rep, err)
	}
	// Everything has an answer now: a second pass looks up nothing.
	if rep2, err := svc.Run(f.ctx); err != nil || rep2.Looked != 0 {
		t.Fatalf("second pass: %+v %v", rep2, err)
	}
	coffee := f.tx("COFFEE KIOSK")
	m, err := f.ms.Get(f.ctx, coffee.ID)
	if err != nil || m.State != merchants.StateNone || !m.Fallback() {
		t.Fatalf("unknown merchant: %+v %v", m, err)
	}

	// The person corrects it; a similar future transaction gets a suggestion,
	// not an identity, and the decision on the first stays.
	f.review(coffee.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Corner Kiosk"})
	cs, err := f.ms.Confirmations(f.ctx)
	if err != nil || len(cs) != 1 || cs[0].Name != "Corner Kiosk" || cs[0].Subject.Description != "COFFEE KIOSK" {
		t.Fatalf("confirmations: %+v %v", cs, err)
	}
	f.now = f.now.Add(48 * time.Hour) // none answers are rechecked after a day
	other := f.tx("RENT 2026-10")
	if err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET raw_description = 'COFFEE KIOSK', counterparty_name = '' WHERE id = ?", string(other.ID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(f.ctx); err != nil {
		t.Fatal(err)
	}
	m, _ = f.ms.Get(f.ctx, other.ID)
	if m.State != merchants.StateUncertain || !m.NeedsReview || !m.Fallback() || m.Enrichment == nil || m.Enrichment.Name != "Corner Kiosk" || m.Enrichment.Source != "local_mapping" {
		t.Fatalf("suggestion for a similar transaction: %+v", m)
	}
	if m, _ = f.ms.Get(f.ctx, coffee.ID); m.State != merchants.StateCorrected || m.Name != "Corner Kiosk" {
		t.Fatalf("the decision stands: %+v", m)
	}
}

// TestRemoteOutageLeavesImportedDataAndEarlierAnswers: a remote source that
// fails is recorded as unavailable and never replaces what is known.
func TestRemoteOutageLeavesImportedDataAndEarlierAnswers(t *testing.T) {
	f := newFixture(t)
	down := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"identified","name":"Far Away Shop","confidence_permille":900}`))
	}))
	defer srv.Close()
	r, err := enrichment.NewRemote(enrichment.RemoteOptions{URL: srv.URL, Licence: enrichment.Licence{Retention: 720 * time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	svc := newEnricher(f, r)
	if rep, err := svc.Run(f.ctx); err != nil || rep.Failed != 0 {
		t.Fatalf("up: %+v %v", rep, err)
	}
	id := f.tx("SALARY OCTOBER").ID
	if m, _ := f.ms.Get(f.ctx, id); m.State != merchants.StateReliable || m.Name != "Far Away Shop" {
		t.Fatalf("remote answer: %+v", m)
	}
	// Retention: after the licence's limit the answer is deleted, then fetched
	// again; when the source is down meanwhile the merchant is simply unknown.
	down = true
	f.now = f.now.Add(721 * time.Hour)
	rep, err := svc.Run(f.ctx)
	if err != nil || rep.Expired == 0 || rep.Failed == 0 {
		t.Fatalf("outage after expiry: %+v %v", rep, err)
	}
	if m, _ := f.ms.Get(f.ctx, id); m.State != merchants.StateUnavailable || !m.Fallback() {
		t.Fatalf("after expiry and outage: %+v", m)
	}
	// The source is back: the transaction is looked up again after the recheck
	// delay, and an identified answer within the retention is kept without
	// asking again, outage or not.
	down = false
	f.now = f.now.Add(25 * time.Hour)
	if _, err := svc.Run(f.ctx); err != nil {
		t.Fatal(err)
	}
	down = true
	f.now = f.now.Add(48 * time.Hour)
	if rep, err := svc.Run(f.ctx); err != nil || rep.Failed != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if m, _ := f.ms.Get(f.ctx, id); m.Name != "Far Away Shop" || m.State != merchants.StateReliable {
		t.Fatalf("earlier answer kept: %+v", m)
	}
	// Bank facts are untouched by any of this.
	if f.tx("SALARY OCTOBER").ID != id {
		t.Fatal("transaction changed")
	}
}

func TestExpiryNeverDeletesADecision(t *testing.T) {
	f := newFixture(t)
	id := f.tx("COFFEE KIOSK").ID
	f.record(id, merchants.Answer{Status: merchants.StatusIdentified, Name: "Shop", Source: "remote", Confidence: 900})
	f.review(id, merchants.Request{Decision: merchants.DecisionConfirmed})
	f.now = f.now.Add(1000 * time.Hour)
	n, err := f.ms.DeleteAnswersBefore(f.ctx, "remote", f.now)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	m, _ := f.ms.Get(f.ctx, id)
	if m.Review == nil || m.Review.Decision != merchants.DecisionConfirmed || m.State != merchants.StateNotLookedUp && m.State != merchants.StateConfirmed {
		t.Fatalf("%+v", m)
	}
}

// Finding 2: never-looked-up rows come first, rechecks by oldest attempt, and a
// batch of one reaches every row over several passes.
func TestPendingOrdersNewRowsThenOldestRechecks(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx
	far := f.now.Add(1000 * time.Hour)
	all, err := f.ms.Pending(ctx, 100, far, true, time.Time{})
	if err != nil || len(all) < 4 {
		t.Fatalf("%d %v", len(all), err)
	}
	ids := func(cs []enrichment.Candidate) []banking.TransactionID {
		var out []banking.TransactionID
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	// all[0] was tried an hour ago-ish later than all[1]; the rest are untouched.
	none := merchants.Answer{Status: merchants.StatusNone, Source: "local"}
	f.now = t0.Add(2 * time.Hour)
	f.record(all[0].ID, none)
	f.now = t0.Add(1 * time.Hour)
	f.record(all[1].ID, none)
	f.now = t0.Add(72 * time.Hour)
	got, err := f.ms.Pending(ctx, 100, f.now.Add(-24*time.Hour), true, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := append(ids(all[2:]), all[1].ID, all[0].ID)
	if g := ids(got); !reflect.DeepEqual(sortedTail(g, 2), sortedTail(want, 2)) || g[len(g)-2] != all[1].ID || g[len(g)-1] != all[0].ID {
		t.Fatalf("order\n got %v\nwant never-looked-up first, then %v, %v", g, all[1].ID, all[0].ID)
	}
	// Without a reason to recheck none, only the new rows are listed.
	if got, _ = f.ms.Pending(ctx, 100, f.now.Add(-24*time.Hour), false, time.Time{}); len(got) != len(all)-2 {
		t.Fatalf("%d", len(got))
	}
	// Batch of one, no remote source: every new row is reached over several
	// passes, and so are the two older none answers, once, because the first
	// pass after a start is a reason to recheck them (the curated list may
	// have changed) and the watermark it sets holds over later passes instead
	// of covering one batch only (PR #124 review). Rows answered since are
	// not asked about again.
	svc := newEnricher(f, nil)
	svc.Batch = 1
	looked := 0
	for i := 0; i < len(all)+2; i++ {
		rep, err := svc.Run(ctx)
		if err != nil || rep.Looked > 1 {
			t.Fatalf("%+v %v", rep, err)
		}
		looked += rep.Looked
	}
	if looked != len(all) {
		t.Fatalf("a batch of one reached %d of %d rows (new rows and the older none answers)", looked, len(all))
	}
	// With a remote source the due rechecks are reached too, oldest first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"none"}`))
	}))
	defer srv.Close()
	r, err := enrichment.NewRemote(enrichment.RemoteOptions{URL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(48 * time.Hour)
	due, _ := f.ms.Pending(ctx, 100, f.now.Add(-24*time.Hour), true, time.Time{})
	if len(due) < 2 {
		t.Fatalf("%d rechecks due", len(due))
	}
	svc = newEnricher(f, r)
	svc.Batch = 1
	looked = 0
	for i := 0; i < len(due)+2; i++ {
		rep, err := svc.Run(ctx)
		if err != nil || rep.Looked > 1 {
			t.Fatalf("%+v %v", rep, err)
		}
		looked += rep.Looked
	}
	if looked != len(due) {
		t.Fatalf("a batch of one reached %d of %d due rows", looked, len(due))
	}
}

// A none answer older than the watermark is listed once; a failed re-lookup
// backs it off for the recheck interval instead of listing it again at once.
func TestWatermarkRecheckBacksOffAfterAFailedLookup(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx
	id := f.tx("COFFEE KIOSK").ID
	f.now = t0
	f.record(id, merchants.Answer{Status: merchants.StatusNone, Source: "local"})
	watermark := t0.Add(time.Hour)
	cutoff := func(now time.Time) time.Time { return now.Add(-24 * time.Hour) }
	has := func(now time.Time, recheckNone bool) bool {
		got, err := f.ms.Pending(ctx, 100, cutoff(now), recheckNone, watermark)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range got {
			if c.ID == id {
				return true
			}
		}
		return false
	}
	if !has(t0.Add(2*time.Hour), false) {
		t.Fatal("a none answer older than the watermark is not listed")
	}
	f.now = t0.Add(2 * time.Hour)
	f.record(id, merchants.Answer{Status: merchants.StatusUnavailable, Source: "remote"})
	f.now = t0.Add(2*time.Hour + time.Minute)
	if has(f.now, false) || has(f.now, true) {
		t.Fatal("the row is listed again a minute after its failed re-lookup")
	}
	if has(t0.Add(25*time.Hour), true) {
		t.Fatal("the row is listed before the recheck interval passed")
	}
	if !has(t0.Add(27*time.Hour), true) {
		t.Fatal("the row is not listed once the recheck interval passed")
	}
}

func sortedTail(ids []banking.TransactionID, n int) []banking.TransactionID {
	head := append([]banking.TransactionID(nil), ids[:len(ids)-n]...)
	sort.Slice(head, func(i, j int) bool { return head[i] < head[j] })
	return head
}

// Finding 4 against the real store: remote answers are purged by retention
// after the remote source is switched off.
func TestRemoteAnswersArePurgedAfterTheSourceIsOff(t *testing.T) {
	f := newFixture(t)
	id := f.tx("COFFEE KIOSK").ID
	f.record(id, merchants.Answer{Status: merchants.StatusIdentified, Name: "Shop", Source: "remote", Confidence: 900})
	svc := newEnricher(f, nil)
	svc.RemoteRetention = 720 * time.Hour
	f.now = f.now.Add(721 * time.Hour)
	rep, err := svc.Run(f.ctx)
	if err != nil || rep.Expired != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if m, _ := f.ms.Get(f.ctx, id); m.Enrichment != nil && m.Enrichment.Source == "remote" {
		t.Fatalf("remote answer survived: %+v", m.Enrichment)
	}
}

// Finding 1 against the real store: a correction beats the curated list for
// the next transaction with the same key.
func TestCorrectionBeatsCuratedOverStoredTransactions(t *testing.T) {
	f := newFixture(t)
	svc := newEnricher(f, nil)
	if _, err := svc.Run(f.ctx); err != nil {
		t.Fatal(err)
	}
	a, b := f.tx("COFFEE KIOSK"), f.tx("CARD PURCHASE BOOKSHOP")
	set := func(id banking.TransactionID, text string) {
		if err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET raw_description = ?, counterparty_name = '' WHERE id = ?", text, string(id))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	set(a.ID, "BOLT HARDWARE STORE 12")
	f.review(a.ID, merchants.Request{Decision: merchants.DecisionCorrected, Name: "Bolt Hardware Store"})
	set(b.ID, "BOLT HARDWARE STORE 99")
	if err := f.db.WriteTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM merchant_enrichments WHERE transaction_id = ?", string(b.ID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(f.ctx); err != nil {
		t.Fatal(err)
	}
	m, _ := f.ms.Get(f.ctx, b.ID)
	if m.Enrichment == nil || m.Enrichment.Name != "Bolt Hardware Store" || m.Enrichment.Source != "local_mapping" || m.State != merchants.StateUncertain {
		t.Fatalf("%+v", m)
	}
}
