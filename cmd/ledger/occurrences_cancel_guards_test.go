package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

// TestDateCorrectionKeepsMatchedOccurrencesAPI: the X1 evidence through the
// D23 removal. A date correction keeps the occurrences a confirmed or a
// reversed match refers to, and removes the ones that only hold a suggestion
// or a rejection without a foreign-key error; the rows that stay are the only
// payment matches left.
func TestDateCorrectionKeepsMatchedOccurrencesAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	bank := importFakeBank(t, f)
	rentTx := bank.tx(t, "RENT 2026-10")
	holdTx := bank.tx(t, "HOTEL HOLD")
	id := f.createCommitment(t, `{"name":"Rent","kind":"service","amount":{"amount":"100.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":2},"start_date":"2026-01-01"}`)
	f.occurrences(t, "month=2026-12&commitment_id="+id, "alice")
	occ := func(month string) string {
		return f.occurrences(t, "month="+month+"&commitment_id="+id, "alice").Items[0].ID
	}
	aug, sep, oct, jul := occ("2026-08"), occ("2026-09"), occ("2026-10"), occ("2026-07")
	match := func(occID string) matchJSON {
		body := fmt.Sprintf(`{"transaction_id":%q,"occurrence_id":%q,"amount":{"amount":"100.00","currency":"EUR"}}`, string(rentTx.ID), occID)
		rec := f.do("POST", "/api/v1/payment-matches", body, admin)
		if rec.Code != 201 {
			t.Fatalf("match %s: %d %s", occID, rec.Code, rec.Body.String())
		}
		return decodeMatch(t, rec.Body.Bytes())
	}
	match(sep)
	augMatch := match(aug)
	if rec := f.do("POST", "/api/v1/payment-matches/"+augMatch.ID+"/reverse", `{"reason":"returned","version":1}`, admin); rec.Code != 200 {
		t.Fatalf("reverse: %d %s", rec.Code, rec.Body.String())
	}
	ms := matchstore.New(f.db, store.NewAuditLog(f.db), func() time.Time { return f.now }, nil)
	suggest := func(occID string) string {
		s, err := ms.Create(context.Background(), audit.System, matches.New{TransactionID: holdTx.ID, OccurrenceID: occID, Amount: moneypkg.MustParse("30.00", "EUR"),
			Origin: matches.OriginModel, State: matches.StateSuggested})
		if err != nil {
			t.Fatal(err)
		}
		return s.Match.ID
	}
	suggest(jul)
	rejected := suggest(oct)
	if rec := f.do("POST", "/api/v1/payment-matches/"+rejected+"/reject", "", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": `"1"`}); rec.Code != 200 {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}

	rec := f.do("PATCH", "/api/v1/commitments/"+id, `{"start_date":"2026-11-01","effective_from":"2026-08-15","version":1}`, admin)
	if rec.Code != 200 {
		t.Fatalf("date correction: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.getOccurrence(t, sep); got.Payment.Status != "paid" {
		t.Errorf("September (confirmed match): %+v", got.Payment)
	}
	if rec := f.do("GET", "/api/v1/occurrences/"+aug, "", admin); rec.Code != 200 {
		t.Errorf("August (reversed match) was removed: %d %s", rec.Code, rec.Body.String())
	}
	for name, gone := range map[string]string{"July (suggestion)": jul, "October (rejection)": oct} {
		if rec := f.do("GET", "/api/v1/occurrences/"+gone, "", admin); rec.Code != 404 {
			t.Errorf("%s was not removed: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	d := f.auditAction(t, "occurrences.regenerated")
	if d["removed_out_of_range"] != "7" || d["removed"] != "1" || d["kept_paid"] != "2" || d["cancelled_referenced"] != "0" {
		t.Errorf("audit %v", d)
	}
	rec = f.do("GET", "/api/v1/payment-matches?limit=100", "", admin)
	var left struct {
		Items []matchJSON `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &left); err != nil || rec.Code != 200 {
		t.Fatalf("matches left: %d %v %s", rec.Code, err, rec.Body.String())
	}
	var states []string
	for _, m := range left.Items {
		states = append(states, m.State)
	}
	slices.Sort(states)
	if strings.Join(states, ",") != "confirmed,reversed" {
		t.Errorf("matches left: %v", states)
	}
}
