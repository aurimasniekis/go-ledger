package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/users"
	"github.com/aurimasniekis/go-ledger/internal/store"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
)

type provenanceJSON struct {
	ID        string  `json:"id"`
	State     string  `json:"state"`
	Version   int64   `json:"version"`
	Reason    *string `json:"reason"`
	CreatedBy struct {
		Via *string `json:"via"`
	} `json:"created_by"`
	DecidedBy *struct {
		Kind   string  `json:"kind"`
		UserID *string `json:"user_id"`
		Via    *string `json:"via"`
	} `json:"decided_by"`
	History []struct {
		Action string `json:"action"`
		By     struct {
			Kind string  `json:"kind"`
			Via  *string `json:"via"`
		} `json:"by"`
		Reason *string `json:"reason"`
	} `json:"history"`
}

func via(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

// TestPaymentMatchUnconfirmAPI: POST /payment-matches/{id}/unconfirm (D42 1a,
// ADR-0045) through the API. Only the person who confirmed may unconfirm, by
// session here (tokens and MCP: TestMCPUnconfirmAndProvenance); the window
// closes with a correction; the occurrence's payment state is restored; stale
// versions get 409 and missing ones 428; every event says how it came (D42 3a).
func TestPaymentMatchUnconfirmAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	erin, err := f.users.Create(ctx, audit.System, users.New{Username: "erin", Role: authz.RoleAdministrator})
	if err != nil {
		t.Fatal(err)
	}
	alice := map[string]string{"X-Test-User": f.ids["alice"]}
	other := map[string]string{"X-Test-User": erin.ID}
	reader := map[string]string{"X-Test-User": f.ids["bob"]}
	with := func(h map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for a, b := range h {
			out[a] = b
		}
		return out
	}
	decode := func(body []byte) provenanceJSON {
		t.Helper()
		var m provenanceJSON
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return m
	}

	bank := importFakeBank(t, f)
	streamID := f.createCommitmentAt(t, earlier, `{"name":"Streaming","kind":"subscription","amount":{"amount":"13.49","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-01-01","match_tolerance":{"amount":"0.50","currency":"EUR"}}`)
	oct := f.occurrences(t, "month=2026-10&commitment_id="+streamID, "alice").Items[0].ID
	ms := matchstore.New(f.db, store.NewAuditLog(f.db), func() time.Time { return f.now }, nil)
	why := "description similarity 80/100 (a hint only)"
	sug, err := ms.Create(ctx, audit.System, matches.New{TransactionID: bank.tx(t, "STREAMING SERVICE").ID, OccurrenceID: oct, Amount: moneypkg.MustParse("12.99", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateSuggested, Reason: &why})
	if err != nil {
		t.Fatal(err)
	}
	before := f.getOccurrence(t, oct).Payment
	base := "/api/v1/payment-matches/" + sug.Match.ID

	// Confirmed in a session.
	rec := f.do("POST", base+"/confirm", `{"version":1}`, alice)
	if rec.Code != 200 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	c := decode(rec.Body.Bytes())
	if c.DecidedBy == nil || via(c.DecidedBy.Via) != "session" || via(c.CreatedBy.Via) != "system" || via(c.History[0].By.Via) != "system" || via(c.History[1].By.Via) != "session" {
		t.Fatalf("provenance after confirm: %s", rec.Body.String())
	}
	if p := f.getOccurrence(t, oct).Payment; p.Status != "paid" {
		t.Fatalf("after confirm: %+v", p)
	}

	// Refusals: no grant, another person, no version, a stale version.
	if rec := f.do("POST", base+"/unconfirm", `{"version":2}`, reader); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Errorf("read-only: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", base+"/unconfirm", `{"version":2}`, other); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"code":"match_undo_not_decider"`) {
		t.Errorf("another administrator: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", base+"/unconfirm", "", alice); rec.Code != 428 {
		t.Errorf("no version: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", base+"/unconfirm", "", with(alice, "If-Match", `"1"`)); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"version_conflict"`) ||
		!strings.Contains(rec.Body.String(), `"current_version":2`) {
		t.Errorf("stale version: %d %s", rec.Code, rec.Body.String())
	}

	// The person who confirmed may: the suggestion is back as it was.
	rec = f.do("POST", base+"/unconfirm", `{"version":2,"reason":"wrong month"}`, alice)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("unconfirm: %d %s", rec.Code, rec.Body.String())
	}
	u := decode(rec.Body.Bytes())
	last := u.History[len(u.History)-1]
	if u.State != "suggested" || u.DecidedBy != nil || u.Reason == nil || *u.Reason != why || last.Action != "unconfirmed" || via(last.By.Via) != "session" || last.By.Kind != "user" ||
		last.Reason == nil || *last.Reason != "wrong month" {
		t.Fatalf("unconfirmed: %s", rec.Body.String())
	}
	if p := f.getOccurrence(t, oct).Payment; p.Status != before.Status || p.Outstanding.Amount != before.Outstanding.Amount || p.EvidenceCount != before.EvidenceCount {
		t.Errorf("payment state not restored: before %+v after %+v", before, p)
	}
	recs, err := store.NewAuditLog(f.db).List(ctx, audit.Filter{Actions: []audit.Action{audit.PaymentMatchUnconfirmed}, Limit: 10})
	if err != nil || len(recs) != 1 || recs[0].Details["via"] != "session" || recs[0].Details["from"] != "confirmed" || recs[0].Actor.Kind != audit.ActorUser {
		t.Errorf("audit: %+v %v", recs, err)
	}

	// A correction closes the window: unmatch is the way back then.
	if rec := f.do("POST", base+"/confirm", `{"version":3}`, alice); rec.Code != 200 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", base+"/correct", `{"version":4,"amount":{"amount":"12.50","currency":"EUR"}}`, alice); rec.Code != 200 {
		t.Fatalf("correct: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", base+"/unconfirm", `{"version":5}`, alice); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"match_undo_window_closed"`) {
		t.Errorf("after a correction: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", base+"/unmatch", `{"version":5}`, alice); rec.Code != 200 {
		t.Errorf("unmatch after the window: %d %s", rec.Code, rec.Body.String())
	}
}
