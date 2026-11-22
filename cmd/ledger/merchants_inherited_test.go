package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// TestMerchantInheritedReviewIsClearedOnThePendingRow drives an inherited
// decision through the routes (ADR-0051): the booking reads the pending row's
// decision, cannot clear it (409 review_inherited; the message says the
// decision is inherited and clear it there, while the pending row is named by
// review.transaction_id of the merchant read), and loses it when the pending row's decision is cleared.
func TestMerchantInheritedReviewIsClearedOnThePendingRow(t *testing.T) {
	f := newAPIFixture(t)
	b := importFakeBank(t, f)
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	pending, booked := string(b.tx(t, "COFFEE KIOSK").ID), string(b.tx(t, "RENT 2026-10").ID)
	path := func(id string) string { return "/api/v1/transactions/" + id + "/merchant" }

	if err := f.db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", booked, pending)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if code, m, _ := admin.do("PUT", path(pending)+"/review", `{"decision":"corrected","name":"Corner Kiosk"}`); code != 200 {
		t.Fatalf("decide the pending row: %d %v", code, m)
	}
	code, m, _ := admin.do("GET", path(booked), "")
	rev := asMap(t, m["review"])
	if code != 200 || m["review_inherited"] != true || rev["transaction_id"] != pending || rev["decision"] != "corrected" {
		t.Fatalf("inherited decision: %d %v", code, m)
	}

	code, body, _ := admin.do("DELETE", path(booked)+"/review", "")
	if code != 409 || body["code"] != "review_inherited" || !strings.Contains(fmt.Sprint(body["message"]), "clear it there") {
		t.Fatalf("clearing an inherited decision: %d %v", code, body)
	}
	if code, m, _ = admin.do("GET", path(booked), ""); code != 200 || m["review_inherited"] != true {
		t.Fatalf("a refused clear removed the inherited decision: %d %v", code, m)
	}

	if code, _, _ = admin.do("DELETE", path(pending)+"/review", ""); code != 200 {
		t.Fatalf("clear the pending row: %d", code)
	}
	if code, m, _ = admin.do("GET", path(booked), ""); code != 200 || m["review_inherited"] != false || m["review"] != nil {
		t.Fatalf("the inherited decision outlived its source: %d %v", code, m)
	}
	if code, _, _ = admin.do("DELETE", path(booked)+"/review", ""); code != 404 {
		t.Fatalf("nothing left to clear: %d", code)
	}

	// A decision on the booking over an inherited one is audited as from=inherited.
	if code, _, _ = admin.do("PUT", path(pending)+"/review", `{"decision":"corrected","name":"Corner Kiosk"}`); code != 200 {
		t.Fatalf("decide the pending row again: %d", code)
	}
	if code, m, _ = admin.do("PUT", path(booked)+"/review", `{"decision":"rejected"}`); code != 200 || m["review_inherited"] != false || asMap(t, m["review"])["transaction_id"] != booked {
		t.Fatalf("decide the booking: %d %v", code, m)
	}
	if got := f.auditAction(t, "merchant.rejected")["from"]; got != "inherited" {
		t.Fatalf("merchant.rejected from = %q, want inherited", got)
	}
}
