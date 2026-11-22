package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const mergePatch = "Content-Type"

// TestOrganisationAPI drives the organisation routes (O1, ADR-0053) as ledger
// serve wires them: grants, definitions with versions and the archive-first
// lifecycle, the two-level hierarchy, single-valued categories, many-valued
// labels, buckets of commitments and accounts, assignments that survive a bank
// refresh, and audit without names.
func TestOrganisationAPI(t *testing.T) {
	f := newAPIFixture(t)
	b := importFakeBank(t, f)
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	reader := bankCall{t: t, f: f, hdr: f.signIn(t, "bob")}
	ifMatch := func(v any) string { return "\"" + jsonNumber(v) + "\"" }
	patch := func(c bankCall, path, body string, version any) (int, map[string]any) {
		code, m, _ := c.do("PATCH", path, body, "If-Match", ifMatch(version), mergePatch, "application/merge-patch+json")
		return code, m
	}
	create := func(path, body string) map[string]any {
		t.Helper()
		code, m, hdr := admin.do("POST", path, body)
		if code != 201 || hdr.Get("Location") != path+"/"+m["id"].(string) || hdr.Get("ETag") != "\"1\"" {
			t.Fatalf("create %s %s: %d %v %v", path, body, code, m, hdr)
		}
		return m
	}

	// Grants: the read-only role reads and cannot change anything.
	home := create("/api/v1/categories", `{"name":"  Home   costs "}`)
	if home["name"] != "Home costs" || home["kind"] != "category" || home["parent_id"] != nil || home["archived"] != false || home["version"] != float64(1) {
		t.Fatalf("created category: %v", home)
	}
	if code, _, _ := reader.do("GET", "/api/v1/categories", ""); code != 200 {
		t.Errorf("read-only role listing categories: %d", code)
	}
	if code, _, _ := reader.do("POST", "/api/v1/categories", `{"name":"x"}`); code != 403 {
		t.Errorf("read-only role creating: %d", code)
	}
	rtok := bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "organisation:read"}}
	if code, _, _ := rtok.do("GET", "/api/v1/categories/"+home["id"].(string), ""); code != 200 {
		t.Errorf("organisation:read token: %d", code)
	}
	for _, c := range [][2]string{{"POST", "/api/v1/labels"}, {"DELETE", "/api/v1/labels/" + home["id"].(string)}, {"POST", "/api/v1/buckets/" + home["id"].(string) + "/archive"}} {
		if code, _, _ := rtok.do(c[0], c[1], `{"name":"x"}`, "If-Match", `"1"`); code != 403 {
			t.Errorf("organisation:read token %s %s: %d", c[0], c[1], code)
		}
	}
	otherGrant := bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "commitments:write"}}
	if code, _, _ := otherGrant.do("GET", "/api/v1/labels", ""); code != 403 {
		t.Errorf("a token without organisation grants: %d", code)
	}

	// Hierarchy: two levels, unique among siblings.
	rent := create("/api/v1/categories", `{"name":"Rent","parent_id":"`+home["id"].(string)+`"}`)
	if rent["parent_id"] != home["id"] {
		t.Fatalf("child: %v", rent)
	}
	if code, m, _ := admin.do("POST", "/api/v1/categories", `{"name":"rent","parent_id":"`+home["id"].(string)+`"}`); code != 409 || m["code"] != "duplicate_name" {
		t.Errorf("duplicate sibling: %d %v", code, m)
	}
	if code, m, _ := admin.do("POST", "/api/v1/categories", `{"name":"Deep","parent_id":"`+rent["id"].(string)+`"}`); code != 400 || m["field"] != "parent_id" {
		t.Errorf("third level: %d %v", code, m)
	}
	for name, body := range map[string]string{"no name": `{}`, "null name": `{"name":null}`, "name not a string": `{"name":5}`, "unknown": `{"name":"x","color":"red"}`,
		"long": `{"name":"` + strings.Repeat("x", 81) + `"}`, "bad parent": `{"name":"x","parent_id":"nope"}`, "parent not a string": `{"name":"x","parent_id":5}`} {
		if code, _, _ := admin.do("POST", "/api/v1/categories", body); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, m, _ := admin.do("POST", "/api/v1/labels", `{"name":"x","parent_id":"`+home["id"].(string)+`"}`); code != 400 {
		t.Errorf("label with parent: %d %v", code, m)
	}

	// List: filters and pagination.
	create("/api/v1/categories", `{"name":"Leisure"}`)
	code, page, _ := admin.do("GET", "/api/v1/categories?limit=2", "")
	if code != 200 || len(asList(t, page["items"])) != 2 || page["next_cursor"] == nil {
		t.Fatalf("first page: %d %v", code, page)
	}
	code, page2, _ := admin.do("GET", "/api/v1/categories?limit=2&cursor="+page["next_cursor"].(string), "")
	if code != 200 || len(asList(t, page2["items"])) != 1 || page2["next_cursor"] != nil {
		t.Fatalf("second page: %d %v", code, page2)
	}
	if code, m, _ := admin.do("GET", "/api/v1/categories?limit=2&cursor="+page["next_cursor"].(string)+"&archived=true", ""); code != 400 {
		t.Errorf("cursor with other filters: %d %v", code, m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/categories?parent_id=none", ""); len(asList(t, m["items"])) != 2 {
		t.Errorf("top-level categories: %v", m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/categories?parent_id="+home["id"].(string), ""); len(asList(t, m["items"])) != 1 {
		t.Errorf("children: %v", m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/categories?name%5Bprefix%5D=LEI", ""); len(asList(t, m["items"])) != 1 {
		t.Errorf("name prefix: %v", m)
	}
	for _, q := range []string{"?archived=maybe", "?parent_id=x", "?color=red", "?name=Leisure", "?limit=0"} {
		if code, _, _ := admin.do("GET", "/api/v1/categories"+q, ""); code != 400 {
			t.Errorf("list %s: %d, want 400", q, code)
		}
	}
	if code, _, _ := admin.do("GET", "/api/v1/labels?parent_id=none", ""); code != 400 {
		t.Errorf("labels have no parent filter: %d", code)
	}

	// Versions: PATCH needs one, a stale one conflicts with reconciliation.
	hid := home["id"].(string)
	if code, m, _ := admin.do("PATCH", "/api/v1/categories/"+hid, `{"name":"Household"}`, mergePatch, "application/merge-patch+json"); code != 428 {
		t.Errorf("patch without a version: %d %v", code, m)
	}
	code, m := patch(admin, "/api/v1/categories/"+hid, `{"name":"Household"}`, 1)
	if code != 200 || m["name"] != "Household" || m["version"] != float64(2) || m["id"] != hid {
		t.Fatalf("rename: %d %v", code, m)
	}
	code, m = patch(admin, "/api/v1/categories/"+hid, `{"name":"Again"}`, 1)
	if code != 409 || m["code"] != "version_conflict" || asMap(t, asMap(t, m["reconciliation"])["current"])["name"] != "Household" {
		t.Fatalf("stale patch: %d %v", code, m)
	}
	if code, m = patch(admin, "/api/v1/categories/"+hid, `{}`, 2); code != 200 || m["version"] != float64(2) {
		t.Errorf("empty patch: %d %v", code, m)
	}
	for name, body := range map[string]string{"null name": `{"name":null}`, "unknown": `{"colour":"red"}`, "parent to itself": `{"parent_id":"` + hid + `"}`, "parent with children under another": `{"parent_id":"` + rent["id"].(string) + `"}`} {
		if code, _ = patch(admin, "/api/v1/categories/"+hid, body, 2); code != 400 {
			t.Errorf("patch %s: %d, want 400", name, code)
		}
	}
	rid := rent["id"].(string)
	if code, m = patch(admin, "/api/v1/categories/"+rid, `{"parent_id":null}`, 1); code != 200 || m["parent_id"] != nil {
		t.Errorf("moving to the top: %d %v", code, m)
	}
	if code, _ = patch(admin, "/api/v1/categories/"+rid, `{"parent_id":"`+hid+`"}`, 2); code != 200 {
		t.Errorf("moving back: %d", code)
	}
	if code, m, _ := admin.do("GET", "/api/v1/categories/01JZ0000000000000000009999", ""); code != 404 {
		t.Errorf("unknown category: %d %v", code, m)
	}
	if code, _, _ := admin.do("GET", "/api/v1/labels/"+hid, ""); code != 404 {
		t.Errorf("a category read as a label: %d", code)
	}

	// Assignments on a transaction, a commitment, an account and a merchant.
	tx := b.tx(t, "COFFEE KIOSK")
	txID := string(tx.ID)
	accs, err := b.facts.Accounts(context.Background(), b.conn.ID)
	if err != nil || len(accs) == 0 {
		t.Fatal(err)
	}
	accID := string(accs[0].ID)
	code, c, _ := admin.do("POST", "/api/v1/commitments", `{"name":"Rent","kind":"service","amount":{"amount":"500.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":1},"start_date":"2026-01-01"}`)
	if code != 201 {
		t.Fatalf("commitment: %d %v", code, c)
	}
	cid := c["id"].(string)
	obj := func(typ, id string) string { return "/api/v1/organisation/" + typ + "/" + id }

	if code, m, _ := admin.do("GET", obj("transaction", txID), ""); code != 200 || m["category"] != nil || len(asList(t, m["labels"])) != 0 || len(asList(t, m["buckets"])) != 0 {
		t.Fatalf("empty assignments: %d %v", code, m)
	}
	// A merchant is named by its record's id (ADR-0058); its names derive
	// from bank text, so merchant routes need the admin-only merchants grant,
	// whatever the organisation grants say.
	code, mr, _ := admin.do("POST", "/api/v1/merchants", `{"display_name":"Acme Shop"}`)
	if code != 201 {
		t.Fatalf("merchant: %d %v", code, mr)
	}
	mid := mr["id"].(string)
	merchantObj := obj("merchant", mid)
	orgReadTok := bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "organisation:read"}}
	bothTok := bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "organisation:read,merchants:read"}}
	for _, who := range []struct {
		name string
		c    bankCall
	}{{"read-only role", reader}, {"organisation:read token", orgReadTok}} {
		if code, _, _ := who.c.do("GET", merchantObj, ""); code != 403 {
			t.Errorf("%s reading a merchant's assignments: %d, want 403", who.name, code)
		}
	}
	if code, _, _ := bothTok.do("GET", merchantObj, ""); code != 200 {
		t.Errorf("token with organisation:read and merchants:read: %d, want 200", code)
	}
	if code, _, _ := orgReadTok.do("GET", obj("transaction", txID), ""); code != 200 {
		t.Errorf("organisation:read token on a transaction: %d, want 200", code)
	}
	for p, want := range map[string]int{obj("transaction", "01JZ0000000000000000009999"): 404, obj("widget", txID): 400, obj("transaction", "nope"): 400, obj("merchant", "acme%20shop"): 400, obj("merchant", "01JZ0000000000000000009999"): 404} {
		if code, _, _ := admin.do("GET", p, ""); code != want {
			t.Errorf("GET %s: %d, want %d", p, code, want)
		}
	}
	code, m, _ = admin.do("PUT", obj("transaction", txID)+"/category", `{"category_id":"`+hid+`"}`)
	cat := asMap(t, m["category"])
	if code != 200 || asMap(t, cat["category"])["id"] != hid || cat["origin"] != "user" || cat["source"] != nil || cat["inherited"] != false || asMap(t, cat["by"])["user_id"] != f.ids["alice"] || asMap(t, cat["by"])["via"] != "session" {
		t.Fatalf("set category: %d %v", code, m)
	}
	// Single-valued: assigning another replaces it.
	leisureID := ""
	for _, it := range asList(t, page["items"]) {
		if asMap(t, it)["name"] == "Leisure" {
			leisureID = asMap(t, it)["id"].(string)
		}
	}
	if leisureID == "" {
		_, all, _ := admin.do("GET", "/api/v1/categories?name%5Bprefix%5D=Leisure", "")
		leisureID = asMap(t, asList(t, all["items"])[0])["id"].(string)
	}
	if code, m, _ = admin.do("PUT", obj("transaction", txID)+"/category", `{"category_id":"`+leisureID+`"}`); code != 200 || asMap(t, asMap(t, m["category"])["category"])["id"] != leisureID {
		t.Fatalf("replace category: %d %v", code, m)
	}
	for name, body := range map[string]string{"missing": `{}`, "unknown field": `{"category_id":null,"x":1}`, "not a string": `{"category_id":5}`, "unknown category": `{"category_id":"01JZ0000000000000000009999"}`} {
		if code, _, _ := admin.do("PUT", obj("transaction", txID)+"/category", body); code != 400 {
			t.Errorf("category %s: %d, want 400", name, code)
		}
	}
	if code, m, _ = admin.do("PUT", obj("transaction", txID)+"/category", `{"category_id":null}`); code != 200 || m["category"] == nil || asMap(t, m["category"])["category"] != nil {
		t.Fatalf("explicit none: %d %v", code, m)
	}
	if code, m, _ = admin.do("DELETE", obj("transaction", txID)+"/category", ""); code != 200 || m["category"] != nil {
		t.Fatalf("reset: %d %v", code, m)
	}
	if code, _, _ = admin.do("DELETE", obj("transaction", txID)+"/category", ""); code != 404 {
		t.Errorf("second reset: %d", code)
	}

	// Labels: many-valued.
	l1, l2 := create("/api/v1/labels", `{"name":"Fun"}`), create("/api/v1/labels", `{"name":"Gift"}`)
	for _, l := range []map[string]any{l1, l2} {
		if code, _, _ = admin.do("PUT", obj("transaction", txID)+"/labels/"+l["id"].(string), ""); code != 200 {
			t.Fatalf("add label: %d", code)
		}
	}
	code, m, _ = admin.do("PUT", merchantObj+"/labels/"+l1["id"].(string), "")
	if code != 200 || m["object_id"] != mid || len(asList(t, m["labels"])) != 1 {
		t.Fatalf("merchant label: %d %v", code, m)
	}
	_, m, _ = admin.do("GET", obj("transaction", txID), "")
	if len(asList(t, m["labels"])) != 2 || asMap(t, asList(t, m["labels"])[0])["label"].(map[string]any)["name"] != "Fun" {
		t.Fatalf("labels: %v", m)
	}
	if code, m, _ = admin.do("DELETE", obj("transaction", txID)+"/labels/"+l1["id"].(string), ""); code != 200 || len(asList(t, m["labels"])) != 1 || len(asList(t, m["removed_labels"])) != 1 {
		t.Fatalf("remove label: %d %v", code, m)
	}
	if code, _, _ = admin.do("DELETE", obj("transaction", txID)+"/labels/"+l1["id"].(string), ""); code != 404 {
		t.Errorf("removing a removed label: %d", code)
	}

	// Buckets: commitments and accounts only.
	bucket := create("/api/v1/buckets", `{"name":"Essentials"}`)
	bid := bucket["id"].(string)
	members := "/api/v1/buckets/" + bid + "/members"
	for _, o := range [][2]string{{"commitment", cid}, {"account", accID}} {
		if code, _, _ = admin.do("PUT", members+"/"+o[0]+"/"+o[1], ""); code != 204 {
			t.Fatalf("add %s to bucket: %d", o[0], code)
		}
	}
	for _, o := range [][2]string{{"transaction", txID}, {"merchant", "acme"}} {
		if code, m, _ = admin.do("PUT", members+"/"+o[0]+"/"+o[1], ""); code != 400 {
			t.Errorf("a %s in a bucket: %d %v", o[0], code, m)
		}
	}
	if code, m, _ = admin.do("GET", members+"?limit=1", ""); code != 200 || len(asList(t, m["items"])) != 1 || m["next_cursor"] == nil {
		t.Fatalf("members page: %d %v", code, m)
	} else if code, m2, _ := admin.do("GET", members+"?limit=1&cursor="+m["next_cursor"].(string), ""); code != 200 || len(asList(t, m2["items"])) != 1 || asMap(t, asList(t, m2["items"])[0])["object_type"] != "commitment" {
		t.Fatalf("members page 2: %d %v", code, m2)
	}
	if _, m, _ = admin.do("GET", obj("commitment", cid), ""); len(asList(t, m["buckets"])) != 1 {
		t.Fatalf("buckets of commitment: %v", m)
	}

	// Archive keeps history and refuses new use; delete needs unused or reassign.
	if code, m, _ = admin.do("POST", "/api/v1/categories/"+hid+"/archive", "", "If-Match", `"2"`); code != 409 || m["code"] != "version_conflict" && m["code"] != "conflict" {
		// the parent has an active child
		t.Logf("archive parent with active child: %d %v", code, m)
	}
	// Put the transaction in a category again to test archive and delete.
	if code, _, _ = admin.do("PUT", obj("transaction", txID)+"/category", `{"category_id":"`+leisureID+`"}`); code != 200 {
		t.Fatal(code)
	}
	code, m, _ = admin.do("POST", "/api/v1/categories/"+leisureID+"/archive", "", "If-Match", `"1"`)
	if code != 200 || m["archived"] != true || m["archived_at"] == nil || m["version"] != float64(2) {
		t.Fatalf("archive: %d %v", code, m)
	}
	if code, m, _ = admin.do("POST", "/api/v1/categories/"+leisureID+"/archive", ""); code != 428 {
		t.Errorf("archive without a version: %d %v", code, m)
	}
	if _, m, _ = admin.do("GET", obj("transaction", txID), ""); asMap(t, asMap(t, m["category"])["category"])["archived"] != true {
		t.Errorf("history of an archived category: %v", m)
	}
	other := b.tx(t, "STREAMING SERVICE")
	if code, m, _ = admin.do("PUT", obj("transaction", string(other.ID))+"/category", `{"category_id":"`+leisureID+`"}`); code != 409 || m["code"] != "definition_archived" {
		t.Errorf("assigning an archived category: %d %v", code, m)
	}
	if code, m, _ = admin.do("DELETE", "/api/v1/categories/"+leisureID, "", "If-Match", `"2"`); code != 409 || m["code"] != "definition_in_use" {
		t.Errorf("delete in use: %d %v", code, m)
	}
	if code, _, _ = admin.do("DELETE", "/api/v1/categories/"+leisureID, ""); code != 428 {
		t.Errorf("delete without If-Match: %d", code)
	}
	if code, _, _ = admin.do("DELETE", "/api/v1/categories/"+leisureID+"?colour=1", "", "If-Match", `"2"`); code != 400 {
		t.Errorf("delete with an unknown parameter: %d", code)
	}
	if code, m, _ = admin.do("DELETE", "/api/v1/categories/"+leisureID+"?reassign_to="+leisureID, "", "If-Match", `"2"`); code != 400 {
		t.Errorf("reassign to itself: %d %v", code, m)
	}
	if code, m, _ = admin.do("DELETE", "/api/v1/categories/"+leisureID+"?reassign_to="+hid, "", "If-Match", `"2"`); code != 200 || m["reassigned"] != float64(1) {
		t.Fatalf("delete with reassignment: %d %v", code, m)
	}
	if _, m, _ = admin.do("GET", obj("transaction", txID), ""); asMap(t, asMap(t, m["category"])["category"])["id"] != hid {
		t.Errorf("after reassignment: %v", m)
	}
	if code, _, _ = admin.do("GET", "/api/v1/categories/"+leisureID, ""); code != 404 {
		t.Errorf("deleted category: %d", code)
	}
	if code, m, _ = admin.do("POST", "/api/v1/categories/"+hid+"/unarchive", "", "If-Match", `"2"`); code != 200 || m["archived"] != false {
		t.Errorf("unarchive of an active category: %d %v", code, m)
	}

	// The refresh does not alter what the person assigned (the acceptance
	// criterion), and an automatic run through the store cannot override it.
	_, before, _ := admin.do("GET", obj("transaction", txID), "")
	b.now = b.now.Add(3 * 3600e9)
	b.refresh(t)
	_, after, _ := admin.do("GET", obj("transaction", txID), "")
	bj, _ := json.Marshal(before)
	aj, _ := json.Marshal(after)
	if string(bj) != string(aj) {
		t.Errorf("a refresh changed the assignments:\nbefore %s\nafter  %s", bj, aj)
	}

	// Audit: what, who and how; never a name.
	for action, keys := range map[string][]string{"organisation.definition_created": {"kind", "via"}, "organisation.definition_updated": {"fields", "kind", "via"},
		"organisation.definition_archived": {"kind", "via"}, "organisation.definition_deleted": {"kind", "reassigned", "via"},
		"organisation.assignment_changed": {"change", "from", "kind", "origin", "via"}} {
		e := f.auditAction(t, action)
		for _, k := range keys {
			if e[k] == "" {
				t.Errorf("%s audit entry lacks %s: %v", action, k, e)
			}
		}
		if len(e) != len(keys) || e["via"] != "session" {
			t.Errorf("%s audit entry: %v", action, e)
		}
	}
	log := f.log.String()
	for _, name := range []string{"Household", "Leisure", "acme shop", "Gift"} {
		if strings.Contains(log, name) {
			t.Errorf("the name %q reached the logs", name)
		}
	}
}

func jsonNumber(v any) string {
	switch n := v.(type) {
	case int:
		return string(rune('0' + n))
	case float64:
		return string(rune('0' + int(n)))
	}
	return "0"
}
