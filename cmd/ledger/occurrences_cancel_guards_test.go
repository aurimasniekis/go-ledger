package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/store"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
)

// Guards of the D31 cancellation that the first tests left uncovered.

// TestArchivedCommitmentGeneratesNothingAPI: a soft-deleted commitment is not
// materialised again as time passes (its cancelled history stays). A second
// live commitment keeps the materialisation pre-check busy, so the filter in
// MaterialiseAllTx (the deleted_at guard) is what keeps the archived one out.
func TestArchivedCommitmentGeneratesNothingAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	id := f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":10`, 1))
	f.occurrences(t, "month=2026-03", "alice")
	f.payAndDelete(t, "2026-01")
	live := f.createCommitment(t, strings.Replace(monthEndBody, `"VPS"`, `"Hosting"`, 1))
	if rec := f.do("DELETE", "/api/v1/commitments/"+id, "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`}); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	f.now = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	items := f.listMonth(t, "2026-08", true)
	if !slicesContainsCommitment(items, live) {
		t.Fatalf("the live commitment was not generated for August: %+v", items)
	}
	for _, it := range items {
		if it.CommitmentID == id {
			t.Errorf("an archived commitment generated occurrence %s for August", it.ID)
		}
	}
	rec := f.do("GET", "/api/v1/occurrences/summary?month=2026-08", "", admin)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"commitment_id":"`+id+`"`) {
		t.Errorf("summary shows the archived commitment: %d %s", rec.Code, rec.Body.String())
	}
}

// TestCancelledOccurrenceRefusesOverrideAndMatchAPI: an override and a match
// on a cancelled occurrence are 409 occurrence_cancelled, not 500 from the
// database trigger; a cancellation drops the occurrence's suggestions.
func TestCancelledOccurrenceRefusesOverrideAndMatchAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	bank := importFakeBank(t, f)
	holdTx := bank.tx(t, "HOTEL HOLD")
	rentTx := bank.tx(t, "RENT 2026-10")
	id := f.createCommitment(t, strings.Replace(monthEndBody, `"day":31`, `"day":10`, 1))
	f.occurrences(t, "month=2026-05", "alice")
	jan := f.payAndDelete(t, "2026-01")

	// A model suggestion on January (M6): cancelling drops it.
	ms := matchstore.New(f.db, store.NewAuditLog(f.db), func() time.Time { return f.now }, nil)
	sug, err := ms.Create(context.Background(), audit.System, matches.New{TransactionID: holdTx.ID, OccurrenceID: jan, Amount: moneypkg.MustParse("10.00", "EUR"),
		Origin: matches.OriginModel, State: matches.StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.do("GET", "/api/v1/payment-matches/"+sug.Match.ID, "", admin); rec.Code != 200 {
		t.Fatalf("suggestion before: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"start_date":"2026-03-01","version":1}`, admin); rec.Code != 200 {
		t.Fatalf("start date correction: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/api/v1/payment-matches/"+sug.Match.ID, "", admin); rec.Code != 404 {
		t.Errorf("a suggestion on a cancelled occurrence survives: %d %s", rec.Code, rec.Body.String())
	}

	// M4: override.
	v := f.getOccurrence(t, jan).Version
	rec := f.do("PUT", "/api/v1/occurrences/"+jan+"/status-override", `{"status":"unpaid","reason":"no longer due","version":`+strconv.FormatInt(v, 10)+`}`, admin)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "occurrence_cancelled") {
		t.Errorf("override on a cancelled occurrence: %d %s", rec.Code, rec.Body.String())
	}
	// M5: match.
	body := `{"transaction_id":"` + string(rentTx.ID) + `","occurrence_id":"` + jan + `","amount":{"amount":"1.00","currency":"EUR"}}`
	rec = f.do("POST", "/api/v1/payment-matches", body, admin)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "occurrence_cancelled") {
		t.Errorf("match on a cancelled occurrence: %d %s", rec.Code, rec.Body.String())
	}
}

func slicesContainsCommitment(items []cancelledOcc, commitmentID string) bool {
	for _, it := range items {
		if it.CommitmentID == commitmentID {
			return true
		}
	}
	return false
}
