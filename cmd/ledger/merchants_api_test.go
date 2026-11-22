package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/store"
	merchantstore "github.com/aurimasniekis/go-ledger/internal/store/merchants"
)

// TestMerchantAPI drives the merchant routes (E1, ADR-0051) as ledger serve
// wires them: the grants, the explicit states, decisions that survive a
// re-import and a new lookup, audit without the name, and bank text and
// amounts left alone. Lookup answers are recorded through the store the way
// the enrichment adapter (E2) will; no live source is involved.
func TestMerchantAPI(t *testing.T) {
	f := newAPIFixture(t)
	b := importFakeBank(t, f)
	ms := merchantstore.New(f.db, store.NewAuditLog(f.db), func() time.Time { return b.now })
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	reader := bankCall{t: t, f: f, hdr: f.signIn(t, "bob")}
	streaming, coffee, rent := b.tx(t, "STREAMING SERVICE"), b.tx(t, "COFFEE KIOSK"), b.tx(t, "RENT 2026-10")
	path := func(id string) string { return "/api/v1/transactions/" + id + "/merchant" }
	answer := func(id string, a merchants.Answer) {
		t.Helper()
		if _, err := ms.RecordAnswer(context.Background(), banking.TransactionID(id), a); err != nil {
			t.Fatal(err)
		}
	}
	sid, cid, rid := string(streaming.ID), string(coffee.ID), string(rent.ID)

	// Never looked up: explicit, neutral fallback, no identity.
	code, m, _ := admin.do("GET", path(sid), "")
	if code != 200 || m["state"] != "not_looked_up" || m["fallback"] != true || m["name"] != nil || m["enrichment"] != nil || m["review"] != nil {
		t.Fatalf("not looked up: %d %v", code, m)
	}
	if code, _, _ = admin.do("GET", path("01JZ0000000000000000009999"), ""); code != 404 {
		t.Errorf("unknown transaction: %d", code)
	}

	// Reliable, ambiguous, outage.
	answer(sid, merchants.Answer{Status: merchants.StatusIdentified, Name: "Streamly", LogoRef: "logo:streamly", Source: "curated", Confidence: 950})
	answer(cid, merchants.Answer{Status: merchants.StatusAmbiguous, Source: "curated", Candidates: []string{"Kiosk B", "Kiosk A"}})
	answer(rid, merchants.Answer{Status: merchants.StatusUnavailable, Source: "curated"})
	code, m, _ = admin.do("GET", path(sid), "")
	enr := asMap(t, m["enrichment"])
	if code != 200 || m["state"] != "reliable" || m["name"] != "Streamly" || m["logo_ref"] != "logo:streamly" || m["identity_source"] != "curated" || m["fallback"] != false ||
		enr["confidence_permille"] != float64(950) || enr["source"] != "curated" || enr["retrieved_at"] != "2026-11-01T09:00:00Z" {
		t.Fatalf("reliable: %d %v", code, m)
	}
	code, m, _ = admin.do("GET", path(cid), "")
	enr = asMap(t, m["enrichment"])
	if m["state"] != "ambiguous" || m["name"] != nil || m["fallback"] != true || m["needs_review"] != true || len(asList(t, enr["candidates"])) != 2 || asList(t, enr["candidates"])[0] != "Kiosk A" || enr["confidence_permille"] != nil {
		t.Fatalf("ambiguous: %d %v", code, m)
	}
	if _, m, _ = admin.do("GET", path(rid), ""); m["state"] != "unavailable" || m["fallback"] != true || asMap(t, m["enrichment"])["retrieved_at"] != nil {
		t.Fatalf("outage: %v", m)
	}

	// Grants: merchant names derive from bank text, so administrators only.
	if code, _, _ = reader.do("GET", path(sid), ""); code != 403 {
		t.Errorf("read-only role read a merchant: %d", code)
	}
	if code, _, _ = reader.do("PUT", path(sid)+"/review", `{"decision":"rejected"}`); code != 403 {
		t.Errorf("read-only role decided: %d", code)
	}
	tok := bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "merchants:read"}}
	if code, _, _ = tok.do("GET", path(sid), ""); code != 200 {
		t.Errorf("merchants:read token read: %d", code)
	}
	if code, _, _ = tok.do("PUT", path(sid)+"/review", `{"decision":"rejected"}`); code != 403 {
		t.Errorf("merchants:read token decided: %d", code)
	}
	if code, _, _ = tok.do("DELETE", path(sid)+"/review", ""); code != 403 {
		t.Errorf("merchants:read token cleared: %d", code)
	}
	wtok := bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "merchants:write"}}
	if code, _, _ = wtok.do("GET", path(sid), ""); code != 200 {
		t.Errorf("merchants:write implies read: %d", code)
	}

	// Validation and conflicts.
	for name, body := range map[string]string{"no decision": `{}`, "bad decision": `{"decision":"maybe"}`, "correct without a name": `{"decision":"corrected"}`,
		"name on reject": `{"decision":"rejected","name":"x"}`, "unknown field": `{"decision":"rejected","amount":"1"}`, "name not a string": `{"decision":"corrected","name":5}`,
		"control character": `{"decision":"corrected","name":"a\u0007b"}`, "too long": `{"decision":"corrected","name":"` + strings.Repeat("x", 201) + `"}`} {
		if code, _, _ = admin.do("PUT", path(sid)+"/review", body); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, body, _ := admin.do("PUT", path(cid)+"/review", `{"decision":"confirmed"}`); code != 409 || body["code"] != "nothing_to_confirm" {
		t.Errorf("confirming an ambiguous answer: %d %v", code, body)
	}
	if code, _, _ = admin.do("PUT", path("01JZ0000000000000000009999")+"/review", `{"decision":"rejected"}`); code != 404 {
		t.Errorf("deciding an unknown transaction: %d", code)
	}
	if code, _, _ = admin.do("DELETE", path(sid)+"/review", ""); code != 404 {
		t.Errorf("clearing a missing decision: %d", code)
	}

	// Correct the ambiguous one; confirm the reliable one; reject the outage one.
	code, m, _ = admin.do("PUT", path(cid)+"/review", `{"decision":"corrected","name":"  Corner   Kiosk "}`)
	rev := asMap(t, m["review"])
	if code != 200 || m["state"] != "corrected" || m["name"] != "Corner Kiosk" || m["logo_ref"] != nil || m["identity_source"] != "user" || m["needs_review"] != false ||
		rev["decision"] != "corrected" || rev["version"] != float64(1) || asMap(t, rev["by"])["via"] != "session" || asMap(t, rev["by"])["user_id"] != f.ids["alice"] {
		t.Fatalf("correct: %d %v", code, m)
	}
	if code, m, _ = admin.do("PUT", path(sid)+"/review", `{"decision":"confirmed"}`); code != 200 || m["state"] != "confirmed" || asMap(t, m["review"])["name"] != "Streamly" {
		t.Fatalf("confirm: %d %v", code, m)
	}
	if code, m, _ = admin.do("PUT", path(rid)+"/review", `{"decision":"rejected"}`); code != 200 || m["state"] != "rejected" || m["fallback"] != true {
		t.Fatalf("reject: %d %v", code, m)
	}

	// Decisions survive a re-import (the pending item books in place) and a new lookup.
	bookshop := b.tx(t, "CARD PURCHASE BOOKSHOP")
	answer(string(bookshop.ID), merchants.Answer{Status: merchants.StatusIdentified, Name: "Bookshop Ltd", Source: "curated", Confidence: 900})
	if code, _, _ = admin.do("PUT", path(string(bookshop.ID))+"/review", `{"decision":"corrected","name":"Corner Books"}`); code != 200 {
		t.Fatal(code)
	}
	b.refresh(t)
	answer(cid, merchants.Answer{Status: merchants.StatusIdentified, Name: "Kiosk A", Source: "curated", Confidence: 990})
	for id, want := range map[string]string{string(bookshop.ID): "Corner Books", cid: "Corner Kiosk"} {
		if code, m, _ = admin.do("GET", path(id), ""); code != 200 || m["state"] != "corrected" || m["name"] != want {
			t.Errorf("after re-import and a new lookup: %d %v", code, m)
		}
	}

	// Clearing goes back to the enrichment alone.
	if code, m, _ = admin.do("DELETE", path(sid)+"/review", ""); code != 200 || m["state"] != "reliable" || m["review"] != nil {
		t.Fatalf("clear: %d %v", code, m)
	}

	// Audit: who, what and how, never the name.
	for _, action := range []string{"merchant.confirmed", "merchant.corrected", "merchant.rejected", "merchant.review_cleared"} {
		e := f.auditAction(t, action)
		if e["via"] != "session" || e["from"] == "" || len(e) != 2 {
			t.Errorf("%s audit entry: %v", action, e)
		}
	}
	if log := f.log.String(); strings.Contains(log, "Corner Kiosk") || strings.Contains(log, "Streamly") {
		t.Error("a merchant name reached the logs")
	}

	// Bank facts are untouched: the transaction still reads as the bank reported it.
	if got := b.tx(t, "STREAMING SERVICE"); got.Amount.String() != streaming.Amount.String() || got.Description != streaming.Description {
		t.Errorf("transaction changed: %+v", got)
	}
}
