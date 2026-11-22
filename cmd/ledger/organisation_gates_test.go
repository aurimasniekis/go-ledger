package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// assignmentChanges counts the organisation.assignment_changed audit entries:
// a refused write must leave none.
func (f *apiFixture) assignmentChanges(t *testing.T) int {
	t.Helper()
	n := 0
	for _, e := range f.auditList(t, "?limit=200", "alice", 200).Items {
		if e.Action == "organisation.assignment_changed" {
			n++
		}
	}
	return n
}

// TestMerchantOrganisationWritesNeedMerchantsWrite drives the merchant-keyed
// organisation write routes (ADR-0051): the key is bank-derived text, so a
// token needs merchants:write on top of organisation:write to set or reset a
// merchant's category or labels. Without it the write is refused (403) and
// leaves no trace; with it the write is recorded in the audit log.
func TestMerchantOrganisationWritesNeedMerchantsWrite(t *testing.T) {
	f := newAPIFixture(t)
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	cat := admin.create(t, "/api/v1/categories", `{"name":"Groceries"}`)
	lbl := admin.create(t, "/api/v1/labels", `{"name":"Regular"}`)
	catID, lblID := cat["id"].(string), lbl["id"].(string)
	mid := admin.create(t, "/api/v1/merchants", `{"display_name":"Acme Shop"}`)["id"].(string)
	merchant := "/api/v1/organisation/merchant/" + mid
	catPath, lblPath := merchant+"/category", merchant+"/labels/"+lblID
	tok := func(grants string) bankCall {
		return bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": grants}}
	}
	orgOnly, mergOnly, both := tok("organisation:write"), tok("merchants:write"), tok("organisation:write,merchants:write")

	// assigned reads the merchant's category and labels as the administrator sees them.
	assigned := func() (string, int) {
		t.Helper()
		code, m, _ := admin.do("GET", merchant, "")
		if code != 200 {
			t.Fatalf("read merchant: %d %v", code, m)
		}
		c := ""
		if cc, ok := m["category"].(map[string]any); ok {
			if d, ok := cc["category"].(map[string]any); ok {
				c = d["id"].(string)
			}
		}
		return c, len(asList(t, m["labels"]))
	}
	noMerchant := func() {
		t.Helper()
		if c, n := assigned(); c != "" || n != 0 {
			t.Fatalf("merchant has assignments: category %q, %d labels", c, n)
		}
	}
	noMerchant()
	before := f.assignmentChanges(t)

	// Refused without merchants:write, whatever organisation:write allows.
	for _, who := range []struct {
		name string
		c    bankCall
	}{{"organisation:write token", orgOnly}, {"merchants:write token", mergOnly}} {
		if code, m, _ := who.c.do("PUT", catPath, `{"category_id":"`+catID+`"}`); code != 403 {
			t.Errorf("%s setting a merchant's category: %d %v, want 403", who.name, code, m)
		}
		if code, m, _ := who.c.do("PUT", lblPath, ""); code != 403 {
			t.Errorf("%s adding a merchant's label: %d %v, want 403", who.name, code, m)
		}
	}
	noMerchant()
	if got := f.assignmentChanges(t); got != before {
		t.Fatalf("refused merchant writes were audited: %d -> %d", before, got)
	}

	// Allowed with both grants: set a category and a label.
	if code, m, _ := both.do("PUT", catPath, `{"category_id":"`+catID+`"}`); code != 200 || asMap(t, asMap(t, m["category"])["category"])["id"] != catID {
		t.Fatalf("set merchant category with both grants: %d %v", code, m)
	}
	if code, m, _ := both.do("PUT", lblPath, ""); code != 200 || len(asList(t, m["labels"])) != 1 {
		t.Fatalf("add merchant label with both grants: %d %v", code, m)
	}
	if c, n := assigned(); c != catID || n != 1 {
		t.Fatalf("after the allowed writes: category %q, %d labels", c, n)
	}
	if got := f.assignmentChanges(t); got != before+2 {
		t.Errorf("allowed merchant writes audited %d times, want 2", got-before)
	}

	// Removal is gated the same way: organisation:write alone cannot clear them.
	if code, m, _ := orgOnly.do("DELETE", catPath, ""); code != 403 {
		t.Errorf("organisation:write token clearing a merchant's category: %d %v, want 403", code, m)
	}
	if code, m, _ := orgOnly.do("DELETE", lblPath, ""); code != 403 {
		t.Errorf("organisation:write token removing a merchant's label: %d %v, want 403", code, m)
	}
	if c, n := assigned(); c != catID || n != 1 {
		t.Fatalf("a refused removal changed the merchant: category %q, %d labels", c, n)
	}
	if code, m, _ := both.do("DELETE", catPath, ""); code != 200 || m["category"] != nil {
		t.Fatalf("clear merchant category with both grants: %d %v", code, m)
	}
	if code, m, _ := both.do("DELETE", lblPath, ""); code != 200 || len(asList(t, m["removed_labels"])) != 1 {
		t.Fatalf("remove merchant label with both grants: %d %v", code, m)
	}
	noMerchant()
}

// create posts body to path as the caller and returns the created definition.
func (c bankCall) create(t *testing.T, path, body string) map[string]any {
	t.Helper()
	code, m, _ := c.do("POST", path, body)
	if code != 201 {
		t.Fatalf("create %s: %d %v", path, code, m)
	}
	return m
}

// TestDeleteCategoryOfInheritedBookingIsReviewInherited: a booking that only
// inherits its category from the pending transaction it replaces (ADR-0051,
// the organisation model's lineage) cannot reset it: 409 review_inherited,
// nothing is removed, and the same reset on the pending row clears it.
func TestDeleteCategoryOfInheritedBookingIsReviewInherited(t *testing.T) {
	f := newAPIFixture(t)
	b := importFakeBank(t, f)
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	pending, booked := string(b.tx(t, "COFFEE KIOSK").ID), string(b.tx(t, "RENT 2026-10").ID)
	cat := admin.create(t, "/api/v1/categories", `{"name":"Dining"}`)
	catID := cat["id"].(string)
	obj := func(id string) string { return "/api/v1/organisation/transaction/" + id }
	catPath := func(id string) string { return obj(id) + "/category" }

	if err := f.db.WriteTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE bank_transactions SET superseded_by = ? WHERE id = ?", booked, pending)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if code, m, _ := admin.do("PUT", catPath(pending), `{"category_id":"`+catID+`"}`); code != 200 {
		t.Fatalf("categorise the pending row: %d %v", code, m)
	}
	code, m, _ := admin.do("GET", obj(booked), "")
	c, _ := m["category"].(map[string]any)
	if code != 200 || c["inherited"] != true || c["origin"] != "user" || asMap(t, c["category"])["id"] != catID {
		t.Fatalf("booking does not inherit the category: %d %v", code, m)
	}

	before := f.assignmentChanges(t)
	code, body, _ := admin.do("DELETE", catPath(booked), "")
	if code != 409 || body["code"] != "review_inherited" || !strings.Contains(fmt.Sprint(body["message"]), "reset it there") {
		t.Fatalf("resetting an inherited category: %d %v, want 409 review_inherited", code, body)
	}
	if got := f.assignmentChanges(t); got != before {
		t.Errorf("a refused reset was audited")
	}
	code, m, _ = admin.do("GET", obj(booked), "")
	if c, _ := m["category"].(map[string]any); code != 200 || c["inherited"] != true || asMap(t, c["category"])["id"] != catID {
		t.Fatalf("a refused reset removed the inherited category: %d %v", code, m)
	}

	// Reset on the pending row, where the choice was made, and the booking loses it.
	if code, _, _ = admin.do("DELETE", catPath(pending), ""); code != 200 {
		t.Fatalf("reset the pending row: %d", code)
	}
	if code, m, _ = admin.do("GET", obj(booked), ""); code != 200 || m["category"] != nil {
		t.Fatalf("the inherited category outlived its source: %d %v", code, m)
	}
	if code, _, _ = admin.do("DELETE", catPath(booked), ""); code != 404 {
		t.Errorf("nothing left to reset on the booking: %d, want 404", code)
	}
}
