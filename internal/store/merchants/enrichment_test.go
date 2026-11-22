package merchants_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	id := f.tx("COFFEE KIOSK").ID
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
	if f.tx("COFFEE KIOSK").ID != id {
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
