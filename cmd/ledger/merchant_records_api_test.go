package main

import (
	"context"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/merchants"
	"github.com/aurimasniekis/go-ledger/internal/store"
	merchantstore "github.com/aurimasniekis/go-ledger/internal/store/merchants"
)

// TestMerchantRecordsAPI drives the merchant record routes (task M1,
// ADR-0056, D-MER 1a) as ledger serve wires them: a correction creates a
// record, an organisation assignment keys on its id, a rename and a new
// spelling keep it, a merge moves everything with If-Match, the read-only role
// is refused, and the audit never carries a name.
func TestMerchantRecordsAPI(t *testing.T) {
	f := newAPIFixture(t)
	b := importFakeBank(t, f)
	ms := merchantstore.New(f.db, store.NewAuditLog(f.db), func() time.Time { return b.now })
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	reader := bankCall{t: t, f: f, hdr: f.signIn(t, "bob")}
	streaming, coffee := b.tx(t, "STREAMING SERVICE"), b.tx(t, "COFFEE KIOSK")
	sid, cid := string(streaming.ID), string(coffee.ID)

	// A correction makes the merchant record; the merchant read names it.
	code, m, _ := admin.do("PUT", "/api/v1/transactions/"+sid+"/merchant/review", `{"decision":"corrected","name":"Streamly"}`)
	if code != 200 || m["merchant_id"] == nil || asMap(t, m["review"])["merchant_id"] != m["merchant_id"] {
		t.Fatalf("correction: %d %v", code, m)
	}
	mid := m["merchant_id"].(string)
	code, rec, hdr := admin.do("GET", "/api/v1/merchants/"+mid, "")
	if code != 200 || rec["display_name"] != "Streamly" || len(asList(t, rec["aliases"])) != 1 || hdr.Get("ETag") != `"1"` {
		t.Fatalf("record: %d %v %v", code, rec, hdr)
	}
	if code, _, _ := reader.do("GET", "/api/v1/merchants", ""); code != 403 {
		t.Errorf("read-only role listing merchants: %d, want 403", code)
	}

	// An organisation assignment keys on the id.
	cat := admin.create(t, "/api/v1/categories", `{"name":"Subscriptions"}`)
	org := "/api/v1/organisation/merchant/" + mid
	if code, m, _ = admin.do("PUT", org+"/category", `{"category_id":"`+cat["id"].(string)+`"}`); code != 200 {
		t.Fatalf("assign: %d %v", code, m)
	}

	// Rename (If-Match) and a new spelling keep the assignment and the decision.
	if code, rec, _ = admin.do("PATCH", "/api/v1/merchants/"+mid, `{"display_name":"Streamly Plus"}`, "If-Match", `"1"`, "Content-Type", "application/merge-patch+json"); code != 200 || rec["display_name"] != "Streamly Plus" || rec["version"] != float64(2) {
		t.Fatalf("rename: %d %v", code, rec)
	}
	if code, rec, _ = admin.do("POST", "/api/v1/merchants/"+mid+"/aliases", `{"name":"STREAMLY*NL"}`); code != 200 || len(asList(t, rec["aliases"])) != 3 {
		t.Fatalf("alias: %d %v", code, rec)
	}
	if _, err := ms.RecordAnswer(context.Background(), banking.TransactionID(cid), merchants.Answer{Status: merchants.StatusIdentified, Name: "streamly*nl", Source: "curated", Confidence: 950}); err != nil {
		t.Fatal(err)
	}
	if code, m, _ = admin.do("GET", "/api/v1/transactions/"+cid+"/merchant", ""); code != 200 || m["merchant_id"] != mid || m["name"] != "Streamly Plus" || asMap(t, m["enrichment"])["merchant_id"] != mid {
		t.Fatalf("new spelling: %d %v", code, m)
	}
	if code, m, _ = admin.do("GET", "/api/v1/transactions/"+sid+"/merchant", ""); m["name"] != "Streamly Plus" || m["state"] != "corrected" {
		t.Fatalf("decision after the rename: %d %v", code, m)
	}
	if code, m, _ = admin.do("GET", org, ""); code != 200 || asMap(t, asMap(t, m["category"])["category"])["id"] != cat["id"] {
		t.Fatalf("assignment after rename and alias: %d %v", code, m)
	}

	// A second merchant, merged into the first.
	other := admin.create(t, "/api/v1/merchants", `{"display_name":"Streamly Duplicate","aliases":["Streamly Dup"]}`)
	oid := other["id"].(string)
	if code, m, _ = admin.do("POST", "/api/v1/merchants", `{"display_name":"streamly plus"}`); code != 409 || m["code"] != "merchant_alias_taken" {
		t.Errorf("duplicate alias: %d %v", code, m)
	}
	if code, m, _ = admin.do("POST", "/api/v1/merchants/"+mid+"/merge", `{"merchant_id":"`+oid+`"}`); code != 428 {
		t.Errorf("merge without If-Match: %d %v", code, m)
	}
	if code, m, _ = admin.do("POST", "/api/v1/merchants/"+mid+"/merge", `{"merchant_id":"`+oid+`"}`, "If-Match", `"1"`); code != 409 || m["code"] != "version_conflict" {
		t.Errorf("stale merge: %d %v", code, m)
	}
	code, m, _ = admin.do("POST", "/api/v1/merchants/"+mid+"/merge", `{"merchant_id":"`+oid+`"}`, "If-Match", `"3"`)
	if code != 200 || asMap(t, m["moved"])["aliases"] != float64(2) || len(asList(t, asMap(t, m["merchant"])["aliases"])) != 5 {
		t.Fatalf("merge: %d %v", code, m)
	}
	if code, m, _ = admin.do("GET", "/api/v1/merchants/"+oid, ""); code != 200 || m["merged_into"] != mid || m["archived"] != true {
		t.Fatalf("merged merchant: %d %v", code, m)
	}
	if code, m, _ = admin.do("PUT", "/api/v1/organisation/merchant/"+oid+"/category", `{"category_id":null}`); code != 409 || m["code"] != "merchant_archived" {
		t.Errorf("assigning to a merged merchant: %d %v", code, m)
	}
	_, list, _ := admin.do("GET", "/api/v1/merchants", "")
	_, all, _ := admin.do("GET", "/api/v1/merchants?archived=true", "")
	if len(asList(t, list["items"])) != 1 || len(asList(t, all["items"])) != 2 {
		t.Fatalf("lists: %v / %v", list, all)
	}
	if code, m, _ = admin.do("DELETE", "/api/v1/merchants/"+mid, "", "If-Match", `"4"`); code != 409 || m["code"] != "merchant_in_use" {
		t.Errorf("deleting a used merchant: %d %v", code, m)
	}

	// The audit: merchant ids, counts, never a name.
	if e := f.auditAction(t, "merchant.merged"); e["merged_from"] != oid || e["aliases"] != "2" {
		t.Errorf("merge audit: %v", e)
	}
	for _, e := range f.auditList(t, "?limit=200", "alice", 200).Items {
		for _, v := range e.Details {
			if v == "Streamly" || v == "Streamly Plus" || v == "STREAMLY*NL" {
				t.Errorf("%s audit holds a name: %v", e.Action, e.Details)
			}
		}
	}
}
