package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/bankimport"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking/bankingtest"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/store"
	"github.com/aurimasniekis/go-ledger/internal/store/bankfacts"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
)

// bankFixture imports the fake provider's standard data (October 2026) into
// the API fixture's database through the real import engine.
type bankFixture struct {
	facts  *bankfacts.Store
	p      *bankingtest.Provider
	engine *bankimport.Engine
	conn   banking.Connection
	now    time.Time
}

func importFakeBank(t *testing.T, f *apiFixture) *bankFixture {
	t.Helper()
	ctx := context.Background()
	b := &bankFixture{facts: bankfacts.New(f.db, nil), now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return b.now }
	b.p = bankingtest.New(bankingtest.Options{Mode: banking.ModeApp, Now: clock, PageSize: 3}, bankingtest.Standard())
	b.engine = bankimport.New(b.facts, bankimport.Options{Now: clock, Sleep: func(context.Context, time.Duration) error { return nil }})
	inst := bankingtest.Standard().Institutions[0]
	if _, err := b.p.StartConsent(ctx, banking.ConsentRequest{Institution: inst, RedirectURL: "https://ledger.example/cb", State: "s", PSUType: banking.PSUPersonal}); err != nil {
		t.Fatal(err)
	}
	conn, err := b.facts.CreateConnection(ctx, banking.NewConnection{Capabilities: b.p.Capabilities(), Institution: inst, ConsentState: "s"}, b.now)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := b.p.CompleteConsent(ctx, banking.ConsentCompletion{State: "s", Code: bankingtest.AuthorizationCode("s")})
	if err != nil {
		t.Fatal(err)
	}
	if b.conn, err = b.facts.ActivateConnection(ctx, conn.ID, sess, b.now); err != nil {
		t.Fatal(err)
	}
	b.refresh(t)
	return b
}

func (b *bankFixture) refresh(t *testing.T) {
	t.Helper()
	res, err := b.engine.Refresh(context.Background(), b.conn.ID, b.p)
	if err != nil || res.Run.Outcome != banking.RefreshSucceeded {
		t.Fatalf("refresh: %v %+v", err, res.Run)
	}
}

func (b *bankFixture) tx(t *testing.T, description string) banking.StoredTransaction {
	t.Helper()
	accs, err := b.facts.Accounts(context.Background(), b.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accs {
		txs, _ := b.facts.Transactions(context.Background(), a.ID)
		for _, x := range txs {
			if x.Description == description {
				return x
			}
		}
	}
	t.Fatalf("no transaction %q", description)
	return banking.StoredTransaction{}
}

type matchJSON struct {
	ID                string  `json:"id"`
	State             string  `json:"state"`
	Origin            string  `json:"origin"`
	CountsAsEvidence  bool    `json:"counts_as_evidence"`
	NeedsReview       bool    `json:"needs_review"`
	Version           int64   `json:"version"`
	OccurrenceVersion int64   `json:"occurrence_version"`
	Reason            *string `json:"reason"`
	Amount            struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"amount"`
	DecidedBy *struct {
		Kind   string  `json:"kind"`
		UserID *string `json:"user_id"`
	} `json:"decided_by"`
	Transaction struct {
		Source        string `json:"source"`
		ID            string `json:"id"`
		BookingStatus string `json:"booking_status"`
		Description   string `json:"description"`
		Countable     bool   `json:"countable"`
		Amount        struct {
			Amount string `json:"amount"`
		} `json:"amount"`
	} `json:"transaction"`
	History []struct {
		Action string `json:"action"`
		Amount struct {
			Amount string `json:"amount"`
		} `json:"amount"`
	} `json:"history"`
}

func decodeMatch(t *testing.T, body []byte) matchJSON {
	t.Helper()
	var m matchJSON
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return m
}

// TestPaymentMatchAPI walks the matching journey through the API: a user's
// match pays an occurrence, duplicates and over-allocations are refused, a
// model suggestion stays distinguishable and cannot be confirmed on a
// pending transaction, a correction and a reversal change the occurrence's
// state, and everything survives a re-import.
func TestPaymentMatchAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	reader := map[string]string{"X-Test-User": f.ids["bob"]}
	with := func(h map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for a, b := range h {
			out[a] = b
		}
		return out
	}
	bank := importFakeBank(t, f)
	rentTx := bank.tx(t, "RENT 2026-10")
	holdTx := bank.tx(t, "HOTEL HOLD")

	rentID := f.createCommitment(t, `{"name":"Rent","kind":"service","amount":{"amount":"800.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":2},"start_date":"2026-01-01"}`)
	oct := f.occurrences(t, "month=2026-10&commitment_id="+rentID, "alice").Items[0].ID
	sep := f.occurrences(t, "month=2026-09&commitment_id="+rentID, "alice").Items[0].ID
	if o := f.getOccurrence(t, oct); o.Payment.Status != "overdue" {
		t.Fatalf("before: %+v", o.Payment)
	}

	body := func(occ, txID, amount string) string {
		return fmt.Sprintf(`{"transaction_id":%q,"occurrence_id":%q,"amount":{"amount":%q,"currency":"EUR"}}`, txID, occ, amount)
	}
	// Refusals.
	if rec := f.do("POST", "/api/v1/payment-matches", body(oct, string(rentTx.ID), "800.00"), reader); rec.Code != 403 {
		t.Errorf("read-only match: %d", rec.Code)
	}
	for _, c := range []struct {
		body, field string
	}{
		{`{"occurrence_id":"` + oct + `","amount":{"amount":"1.00","currency":"EUR"}}`, "transaction_id"},
		{body(oct, string(rentTx.ID), "0"), "amount.amount"},
		{strings.Replace(body(oct, string(rentTx.ID), "800.00"), "EUR", "USD", 1), "amount.currency"},
		{body(oct, "01JZ0000000000000000000999", "1.00"), "transaction_id"},
		{`{"transaction_id":"x","occurrence_id":"` + oct + `","amount":{"amount":"1.00","currency":"EUR"}}`, "transaction_id"},
		{strings.Replace(body(oct, string(rentTx.ID), "1.00"), "}}", `},"origin":"model_suggestion"}`, 1), "origin"},
	} {
		rec := f.do("POST", "/api/v1/payment-matches", c.body, admin)
		if rec.Code != 400 || errFields(t, rec.Body.Bytes()) != c.field {
			t.Errorf("%s: %d %s", c.body, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do("POST", "/api/v1/payment-matches", body(oct, string(holdTx.ID), "70.00"), admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"transaction_not_countable"`) {
		t.Errorf("pending transaction: %d %s", rec.Code, rec.Body.String())
	}

	// A partial match, then a correction to the full amount.
	rec := f.do("POST", "/api/v1/payment-matches", strings.Replace(body(oct, string(rentTx.ID), "500.00"), "}}", `},"reason":"first part"}`, 1), admin)
	if rec.Code != 201 || rec.Header().Get("ETag") != `"1"` || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v1/payment-matches/") {
		t.Fatalf("match: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	m := decodeMatch(t, rec.Body.Bytes())
	if m.State != "confirmed" || m.Origin != "user" || !m.CountsAsEvidence || m.DecidedBy == nil || m.DecidedBy.Kind != "user" || *m.DecidedBy.UserID != f.ids["alice"] ||
		m.Transaction.Source != "bank" || m.Transaction.Amount.Amount != "-800.00" || m.Transaction.BookingStatus != "booked" || *m.Reason != "first part" {
		t.Fatalf("created: %+v", m)
	}
	if m.OccurrenceVersion != 2 {
		t.Errorf("occurrence_version after create: %d", m.OccurrenceVersion)
	}
	if o := f.getOccurrence(t, oct); o.Payment.Status != "partially_paid" || o.Payment.Outstanding.Amount != "300.00" || o.Version != 2 {
		t.Fatalf("after 500: v%d %+v", o.Version, o.Payment)
	}
	// Duplicate assignment: the same pair again, and more than the transaction.
	if rec := f.do("POST", "/api/v1/payment-matches", body(oct, string(rentTx.ID), "100.00"), admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"duplicate_match"`) {
		t.Errorf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/api/v1/payment-matches", body(sep, string(rentTx.ID), "300.01"), admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"transaction_over_allocated"`) {
		t.Errorf("over-allocation: %d %s", rec.Code, rec.Body.String())
	}
	base := "/api/v1/payment-matches/" + m.ID
	if rec := f.do("POST", base+"/correct", `{"amount":{"amount":"800.00","currency":"EUR"}}`, admin); rec.Code != 428 {
		t.Errorf("correct without a version: %d", rec.Code)
	}
	if rec := f.do("POST", base+"/correct", `{"amount":{"amount":"800.00","currency":"EUR"}}`, with(admin, "If-Match", `"7"`)); rec.Code != 409 ||
		!strings.Contains(rec.Body.String(), `"current_version":1`) || !strings.Contains(rec.Body.String(), m.ID) {
		t.Errorf("stale correct: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", base+"/correct", `{"amount":{"amount":"900.00","currency":"EUR"},"version":1}`, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "transaction_over_allocated") {
		t.Errorf("correct past the transaction: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do("POST", base+"/correct", `{"amount":{"amount":"800.00","currency":"EUR"},"reason":"one payment"}`, with(admin, "If-Match", `"1"`))
	if rec.Code != 200 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("correct: %d %s", rec.Code, rec.Body.String())
	}
	if m = decodeMatch(t, rec.Body.Bytes()); len(m.History) != 2 || m.History[0].Amount.Amount != "500.00" || m.History[1].Action != "corrected" {
		t.Fatalf("history: %+v", m.History)
	}
	if o := f.getOccurrence(t, oct); o.Payment.Status != "paid" || o.Payment.EvidenceCount != 1 {
		t.Fatalf("after correction: %+v", o.Payment)
	}

	// A model suggestion on the pending hold, created as task X2/X3 would.
	ms := matchstore.New(f.db, store.NewAuditLog(f.db), func() time.Time { return f.now }, nil)
	hotelID := f.createCommitment(t, `{"name":"Hotel","kind":"service","amount":{"amount":"70.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":30},"start_date":"2026-01-01"}`)
	hotelOct := f.occurrences(t, "month=2026-10&commitment_id="+hotelID, "alice").Items[0].ID
	sug, err := ms.Create(context.Background(), audit.System, matches.New{TransactionID: holdTx.ID, OccurrenceID: hotelOct, Amount: moneypkg.MustParse("70.00", "EUR"),
		Origin: matches.OriginModel, State: matches.StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	rec = f.do("GET", "/api/v1/payment-matches/"+sug.Match.ID, "", reader)
	if s := decodeMatch(t, rec.Body.Bytes()); rec.Code != 200 || s.State != "suggested" || s.Origin != "model_suggestion" || s.CountsAsEvidence || s.DecidedBy != nil ||
		s.Transaction.BookingStatus != "pending" || s.Transaction.Countable {
		t.Fatalf("suggestion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/api/v1/payment-matches/"+sug.Match.ID+"/confirm", `{"version":1}`, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "transaction_not_countable") {
		t.Errorf("confirm on a pending transaction: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/api/v1/payment-matches/"+sug.Match.ID+"/reverse", "", with(admin, "If-Match", `"1"`)); rec.Code != 409 || !strings.Contains(rec.Body.String(), "invalid_match_state") {
		t.Errorf("reverse a suggestion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/api/v1/payment-matches/"+sug.Match.ID+"/reject", "", with(admin, "If-Match", `"1"`)); rec.Code != 200 || decodeMatch(t, rec.Body.Bytes()).State != "rejected" {
		t.Errorf("reject: %d %s", rec.Code, rec.Body.String())
	}
	// Filters tell user decisions from suggestions.
	rec = f.do("GET", "/api/v1/payment-matches?origin=user&state=confirmed", "", reader)
	if rec.Code != 200 || strings.Count(rec.Body.String(), `"origin":"user"`) != 1 || strings.Contains(rec.Body.String(), "model_suggestion") {
		t.Errorf("filtered list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/api/v1/payment-matches?state=maybe", "", reader); rec.Code != 400 {
		t.Errorf("bad filter: %d", rec.Code)
	}

	// Re-import with a rewritten description: the match keeps its transaction.
	bank.now = bank.now.Add(24 * time.Hour)
	if err := f.db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE bank_account_sync SET transactions_through = '2026-09-30'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	bank.p.Mutate(func(d *bankingtest.Dataset) {
		for i, x := range d.Accounts[0].Transactions {
			if x.EntryReference == bankingtest.RefRent {
				d.Accounts[0].Transactions[i].Description = "RENT OCTOBER"
			}
		}
	})
	bank.refresh(t)
	rec = f.do("GET", base, "", reader)
	if got := decodeMatch(t, rec.Body.Bytes()); got.Transaction.Description != "RENT OCTOBER" || got.Transaction.ID != string(rentTx.ID) || got.Version != 2 || got.State != "confirmed" {
		t.Fatalf("after re-import: %s", rec.Body.String())
	}
	if o := f.getOccurrence(t, oct); o.Payment.Status != "paid" {
		t.Fatalf("status after re-import: %+v", o.Payment)
	}

	// Reverse: the occurrence reopens with its earlier status.
	rec = f.do("POST", base+"/reverse", `{"reason":"returned by the bank","version":2}`, admin)
	if rec.Code != 200 || decodeMatch(t, rec.Body.Bytes()).State != "reversed" {
		t.Fatalf("reverse: %d %s", rec.Code, rec.Body.String())
	}
	if o := f.getOccurrence(t, oct); o.Payment.Status != "overdue" || o.Payment.Outstanding.Amount != "800.00" || o.Payment.EvidenceCount != 0 {
		t.Fatalf("after reverse: %+v", o.Payment)
	}
	if rec := f.do("POST", base+"/unmatch", `{"version":3}`, admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "invalid_match_state") {
		t.Errorf("unmatch a reversed match: %d %s", rec.Code, rec.Body.String())
	}
	// The transaction is free again: a new match pays September.
	rec = f.do("POST", "/api/v1/payment-matches", body(sep, string(rentTx.ID), "800.00"), admin)
	if rec.Code != 201 {
		t.Fatalf("match September: %d %s", rec.Code, rec.Body.String())
	}
	sepMatch := decodeMatch(t, rec.Body.Bytes())
	if rec := f.do("POST", "/api/v1/payment-matches/"+sepMatch.ID+"/unmatch", `{"reason":"wrong month"}`, with(admin, "If-Match", `"1"`)); rec.Code != 200 ||
		decodeMatch(t, rec.Body.Bytes()).State != "rejected" {
		t.Errorf("unmatch: %d %s", rec.Code, rec.Body.String())
	}
	if o := f.getOccurrence(t, sep); o.Payment.Status != "overdue" {
		t.Errorf("September after unmatch: %+v", o.Payment)
	}

	// The audit trail names each change, never an amount, reason or bank text.
	var got []string
	for _, l := range auditLines(t, f.log.String()) {
		a, _ := l["action"].(string)
		if strings.HasPrefix(a, "payment_match.") {
			got = append(got, a)
		}
	}
	want := "payment_match.confirmed payment_match.corrected payment_match.rejected payment_match.reversed payment_match.confirmed payment_match.unmatched"
	if strings.Join(got, " ") != want {
		t.Errorf("audit: %v", got)
	}
	for _, secret := range []string{"returned by the bank", "wrong month", "800.00", "RENT"} {
		for _, l := range auditLines(t, f.log.String()) {
			if a, _ := l["action"].(string); strings.HasPrefix(a, "payment_match.") && strings.Contains(fmt.Sprint(l), secret) {
				t.Errorf("audit line carries %q: %v", secret, l)
			}
		}
	}
}
