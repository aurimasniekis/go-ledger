package main

import (
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/mcpserver"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// TestMCPOrganisationJourney walks the organisation tools over real MCP and
// the real API (task O2): the validation, archive and reassign rules are the
// HTTP API's own, assignments are the person's, and a grant is named when
// missing. The grant parity of every tool is TestMCPHTTPGrantParity.
func TestMCPOrganisationJourney(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	secret, _ := f.token(t, f.admin, "organisation:write", "commitments:write")
	c := f.connect(t, secret)
	def := func(name string, args map[string]any) ledgerclient.Definition {
		t.Helper()
		r := call(t, c, name, args)
		if r.IsError {
			t.Fatalf("%s: %s", name, resultText(r))
		}
		return decodeJSONText[ledgerclient.Definition](t, r)
	}
	food := def("create_category", map[string]any{"name": "Food"})
	groceries := def("create_category", map[string]any{"name": "Groceries", "parent_id": food.ID})
	other := def("create_category", map[string]any{"name": "Home"})
	label := def("create_label", map[string]any{"name": "Tax"})
	bucket := def("create_bucket", map[string]any{"name": "Household"})

	// The same validation as HTTP: a duplicate name, a third level and an unknown member.
	if r := call(t, c, "create_category", map[string]any{"name": " food "}); !r.IsError || !strings.Contains(resultText(r), "duplicate_name") {
		t.Errorf("duplicate: %s", resultText(r))
	}
	if r := call(t, c, "create_category", map[string]any{"name": "Deep", "parent_id": groceries.ID}); !r.IsError || !strings.Contains(resultText(r), "two levels") {
		t.Errorf("a third level: %s", resultText(r))
	}
	if r := call(t, c, "create_label", map[string]any{"name": "X", "parent_id": food.ID}); !r.IsError || !strings.Contains(resultText(r), "invalid arguments") {
		t.Errorf("a label with a parent must be refused by the schema: %s", resultText(r))
	}
	if r := call(t, c, "set_object_category", map[string]any{"object_type": "merchant", "object_id": fakeID, "category_id": nil}); !r.IsError || !strings.Contains(resultText(r), "invalid arguments") {
		t.Errorf("a merchant is not an MCP object type: %s", resultText(r))
	}

	cm := decodeJSONText[struct{ ID string }](t, call(t, c, "create_commitment", newCommitmentArgs("Rent")))
	a := decodeJSONText[ledgerclient.ObjectAssignments](t, call(t, c, "set_object_category", map[string]any{"object_type": "commitment", "object_id": cm.ID, "category_id": groceries.ID}))
	if a.Category == nil || a.Category.Category == nil || a.Category.Category.ID != groceries.ID || a.Category.Origin != "user" {
		t.Fatalf("category: %+v", a.Category)
	}
	if r := call(t, c, "add_object_label", map[string]any{"object_type": "commitment", "object_id": cm.ID, "label_id": label.ID}); r.IsError {
		t.Fatal(resultText(r))
	}
	if r := call(t, c, "add_bucket_member", map[string]any{"id": bucket.ID, "object_type": "commitment", "object_id": cm.ID}); r.IsError {
		t.Fatal(resultText(r))
	}
	got := decodeJSONText[ledgerclient.ObjectAssignments](t, call(t, c, "get_object_organisation", map[string]any{"object_type": "commitment", "object_id": cm.ID}))
	if len(got.Labels) != 1 || len(got.Buckets) != 1 {
		t.Fatalf("assignments: %+v", got)
	}

	// Archive first: history is kept and nothing new may use it.
	food = def("get_category", map[string]any{"id": food.ID})
	if r := call(t, c, "archive_category", map[string]any{"id": food.ID, "version": food.Version}); !r.IsError || !strings.Contains(resultText(r), "conflict") {
		t.Errorf("a parent is archived after its children: %s", resultText(r))
	}
	groceries = def("get_category", map[string]any{"id": groceries.ID})
	groceries = def("archive_category", map[string]any{"id": groceries.ID, "version": groceries.Version})
	if !groceries.Archived {
		t.Fatalf("not archived: %+v", groceries)
	}
	if r := call(t, c, "set_object_category", map[string]any{"object_type": "commitment", "object_id": cm.ID, "category_id": groceries.ID}); !r.IsError || !strings.Contains(resultText(r), "definition_archived") {
		t.Errorf("an archived category must be refused: %s", resultText(r))
	}
	// Delete: refused while assigned, moved with reassign_to.
	if r := call(t, c, "delete_category", map[string]any{"id": groceries.ID, "version": groceries.Version}); !r.IsError || !strings.Contains(resultText(r), "definition_in_use") {
		t.Errorf("in use: %s", resultText(r))
	}
	r := call(t, c, "delete_category", map[string]any{"id": groceries.ID, "version": groceries.Version, "reassign_to": other.ID})
	if r.IsError || decodeJSONText[ledgerclient.DefinitionDeleted](t, r).Reassigned != 1 {
		t.Fatalf("reassign: %s", resultText(r))
	}
	got = decodeJSONText[ledgerclient.ObjectAssignments](t, call(t, c, "get_object_organisation", map[string]any{"object_type": "commitment", "object_id": cm.ID}))
	if got.Category == nil || got.Category.Category == nil || got.Category.Category.ID != other.ID {
		t.Errorf("after reassign: %+v", got.Category)
	}
	page := decodeJSONText[ledgerclient.Page[ledgerclient.Definition]](t, call(t, c, "list_categories", map[string]any{"parent_id": "none", "name[prefix]": "ho"}))
	if len(page.Items) != 1 || page.Items[0].ID != other.ID {
		t.Errorf("filtered list: %+v", page.Items)
	}

	// A token without the write grant names it.
	ro, _ := f.token(t, f.admin, "organisation:read")
	rc := f.connect(t, ro)
	if r := call(t, rc, "create_label", map[string]any{"name": "Nope"}); !r.IsError || !strings.Contains(resultText(r), "organisation:write") {
		t.Errorf("missing grant: %s", resultText(r))
	}
	if r := call(t, rc, "list_labels", nil); r.IsError {
		t.Errorf("read: %s", resultText(r))
	}
	// Every tool that removes, deletes or resets organisation data is marked destructive.
	byName := map[string]mcpserver.Tool{}
	for _, tl := range mcpserver.Tools() {
		byName[tl.Name] = tl
	}
	for _, name := range []string{"delete_category", "delete_label", "delete_bucket", "remove_bucket_member", "reset_object_category", "remove_object_label"} {
		tl, ok := byName[name]
		if !ok {
			t.Errorf("%s: no such tool", name)
		} else if tl.Kind != mcpserver.KindDestructive {
			t.Errorf("%s: kind %v, want destructive", name, tl.Kind)
		}
	}
}
