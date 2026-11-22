package main

import (
	"strings"
	"testing"
)

// TestCLIOrganisation drives the organisation commands (O4, ADR-0053) against
// the real API: definitions with versions and the archive-first lifecycle,
// delete with --reassign-to, bucket members, assignments and their grants.
func TestCLIOrganisation(t *testing.T) {
	c := newCLIFixture(t)
	type def struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		ParentID *string `json:"parent_id"`
		Archived bool    `json:"archived"`
		Version  int64   `json:"version"`
	}
	var cm idVersion
	c.json(adminToken, &cm, "commitments", "create", "--name", "Rent", "--kind", "subscription", "--amount", "900.00", "--currency", "EUR",
		"--frequency", "monthly", "--day", "1", "--start-date", "2026-01-01")

	var home, rent, leisure def
	c.json(adminToken, &home, "categories", "create", "--name", "Home")
	c.json(adminToken, &rent, "categories", "create", "--name", "Rent", "--parent-id", home.ID)
	c.json(adminToken, &leisure, "categories", "create", "--name", "Leisure")
	if rent.ParentID == nil || *rent.ParentID != home.ID || home.Version != 1 {
		t.Fatalf("categories: %+v %+v", home, rent)
	}
	c.fail(adminToken, 2, "--name is required", "categories", "create")
	c.fail(adminToken, 1, "duplicate_name", "categories", "create", "--name", "home")

	// rename with an implicit version (reported) and a stale one.
	var renamed def
	c.json(adminToken, &renamed, "categories", "rename", home.ID, "--name", "Household")
	if renamed.Name != "Household" || renamed.Version != 2 {
		t.Fatalf("rename: %+v", renamed)
	}
	c.fail(adminToken, 1, "conflict", "categories", "rename", home.ID, "--name", "x", "--version", "1")

	// move: to top level, back under a parent; exactly one of the two flags.
	var moved def
	c.json(adminToken, &moved, "categories", "move", rent.ID, "--top-level")
	if moved.ParentID != nil {
		t.Fatalf("move to top level: %+v", moved)
	}
	c.json(adminToken, &moved, "categories", "move", rent.ID, "--parent", home.ID)
	if moved.ParentID == nil || *moved.ParentID != home.ID {
		t.Fatalf("move under: %+v", moved)
	}
	c.fail(adminToken, 2, "exactly one of", "categories", "move", rent.ID)
	c.fail(adminToken, 2, "exactly one of", "categories", "move", rent.ID, "--parent", home.ID, "--top-level")

	// list: text, filters and JSON.
	if rc, out, _ := c.run(adminToken, "categories", "list"); rc != 0 || !strings.Contains(out, "Household") || !strings.Contains(out, "Leisure") {
		t.Fatalf("list: %d %s", rc, out)
	}
	var page struct {
		Items []def `json:"items"`
	}
	c.json(adminToken, &page, "categories", "list", "--parent-id", "none", "--all")
	if len(page.Items) != 2 {
		t.Fatalf("top-level categories: %+v", page.Items)
	}
	c.fail(adminToken, 2, "--archived must be", "labels", "list", "--archived", "maybe")
	c.fail(adminToken, 2, "flag provided but not defined", "labels", "list", "--parent-id", "none")

	// labels and buckets, archive and unarchive.
	var fixed, shared, bucket def
	c.json(adminToken, &fixed, "labels", "create", "--name", "Fixed")
	c.json(adminToken, &shared, "labels", "create", "--name", "Shared")
	c.json(adminToken, &bucket, "buckets", "create", "--name", "Joint")
	var arch def
	c.json(adminToken, &arch, "labels", "archive", shared.ID)
	if !arch.Archived {
		t.Fatalf("archive: %+v", arch)
	}
	c.json(adminToken, &page, "labels", "list", "--archived", "false")
	if len(page.Items) != 1 || page.Items[0].ID != fixed.ID {
		t.Fatalf("active labels: %+v", page.Items)
	}
	c.json(adminToken, &arch, "labels", "unarchive", shared.ID)
	if arch.Archived {
		t.Fatalf("unarchive: %+v", arch)
	}

	// bucket members.
	if rc, out, errb := c.run(adminToken, "buckets", "add-member", bucket.ID, "--type", "commitment", "--object", cm.ID); rc != 0 || !strings.Contains(out, "added commitment") {
		t.Fatalf("add-member: %d %s %s", rc, out, errb)
	}
	if rc, _, _ := c.run(adminToken, "buckets", "add-member", bucket.ID, "--type", "commitment", "--object", cm.ID); rc != 0 {
		t.Fatalf("add-member is idempotent: %d", rc)
	}
	var members struct {
		Items []struct {
			ObjectType string `json:"object_type"`
			ObjectID   string `json:"object_id"`
		} `json:"items"`
	}
	c.json(adminToken, &members, "buckets", "members", bucket.ID)
	if len(members.Items) != 1 || members.Items[0].ObjectID != cm.ID {
		t.Fatalf("members: %+v", members)
	}
	if rc, out, _ := c.run(adminToken, "buckets", "members", bucket.ID); rc != 0 || !strings.Contains(out, cm.ID) {
		t.Fatalf("members text: %d %s", rc, out)
	}
	c.fail(adminToken, 2, "--type must be", "buckets", "add-member", bucket.ID, "--type", "transaction", "--object", cm.ID)
	c.fail(adminToken, 2, "--object is required", "buckets", "remove-member", bucket.ID, "--type", "account")

	// assignments.
	var a struct {
		ObjectType string `json:"object_type"`
		Category   *struct {
			Category *def   `json:"category"`
			Origin   string `json:"origin"`
		} `json:"category"`
		Labels []struct {
			Label def `json:"label"`
		} `json:"labels"`
		RemovedLabels []struct {
			Label def `json:"label"`
		} `json:"removed_labels"`
		Buckets []def `json:"buckets"`
	}
	c.json(adminToken, &a, "organisation", "get", "commitment", cm.ID)
	if a.Category != nil || len(a.Buckets) != 1 || a.Buckets[0].ID != bucket.ID {
		t.Fatalf("get: %+v", a)
	}
	c.json(adminToken, &a, "organisation", "set-category", "commitment", cm.ID, "--category", rent.ID)
	if a.Category == nil || a.Category.Category == nil || a.Category.Category.ID != rent.ID || a.Category.Origin != "user" {
		t.Fatalf("set-category: %+v", a)
	}
	c.json(adminToken, &a, "organisation", "set-category", "commitment", cm.ID, "--none")
	if a.Category == nil || a.Category.Category != nil {
		t.Fatalf("set-category none: %+v", a)
	}
	c.json(adminToken, &a, "organisation", "set-category", "commitment", cm.ID, "--category", rent.ID)
	c.json(adminToken, &a, "organisation", "add-label", "commitment", cm.ID, "--label", fixed.ID)
	if len(a.Labels) != 1 {
		t.Fatalf("add-label: %+v", a)
	}
	c.json(adminToken, &a, "organisation", "remove-label", "commitment", cm.ID, "--label", fixed.ID)
	if len(a.Labels) != 0 || len(a.RemovedLabels) != 1 {
		t.Fatalf("remove-label: %+v", a)
	}
	c.fail(adminToken, 1, "not_found", "organisation", "remove-label", "commitment", cm.ID, "--label", shared.ID)
	rc, out, errb := c.run(adminToken, "organisation", "get", "commitment", cm.ID)
	if rc != 0 || !strings.Contains(out, "Rent [") || !strings.Contains(out, "removed label:") || !strings.Contains(out, "Joint [") {
		t.Fatalf("get text: %d %s %s", rc, out, errb)
	}
	c.json(adminToken, &a, "organisation", "reset-category", "commitment", cm.ID)
	if a.Category != nil {
		t.Fatalf("reset-category: %+v", a)
	}
	c.fail(adminToken, 2, "exactly one of", "organisation", "set-category", "commitment", cm.ID)
	c.fail(adminToken, 2, "--label is required", "organisation", "add-label", "commitment", cm.ID)
	c.fail(adminToken, 2, "object type must be", "organisation", "get", "bucket", cm.ID)
	c.fail(adminToken, 2, "object type", "organisation", "get")

	// remove-member, then delete: --reassign-to moves what is assigned.
	if rc, out, _ := c.run(adminToken, "buckets", "remove-member", bucket.ID, "--type", "commitment", "--object", cm.ID); rc != 0 || !strings.Contains(out, "removed commitment") {
		t.Fatalf("remove-member: %d %s", rc, out)
	}
	c.json(adminToken, &a, "organisation", "set-category", "commitment", cm.ID, "--category", leisure.ID)
	c.fail(adminToken, 1, "", "categories", "delete", leisure.ID)
	var del struct {
		Deleted    string `json:"deleted"`
		Reassigned int    `json:"reassigned"`
	}
	c.json(adminToken, &del, "categories", "delete", leisure.ID, "--reassign-to", rent.ID)
	if del.Deleted != leisure.ID || del.Reassigned != 1 {
		t.Fatalf("delete: %+v", del)
	}
	c.json(adminToken, &a, "organisation", "get", "commitment", cm.ID)
	if a.Category == nil || a.Category.Category == nil || a.Category.Category.ID != rent.ID {
		t.Fatalf("reassigned: %+v", a)
	}
	c.json(adminToken, &del, "buckets", "delete", bucket.ID)
	if del.Reassigned != 0 {
		t.Fatalf("delete bucket: %+v", del)
	}

	// help names every subcommand; the server enforces the grants.
	for _, cmd := range []string{"categories", "labels", "buckets", "organisation"} {
		rc, _, errb := c.run(adminToken, cmd, "help")
		if rc != 0 || !strings.Contains(errb, "ledger "+cmd) {
			t.Errorf("%s help: %d %s", cmd, rc, errb)
		}
	}
	if _, _, errb := c.run(adminToken, "buckets", "help"); !strings.Contains(errb, "add-member") {
		t.Errorf("buckets help: %s", errb)
	}
	if _, _, errb := c.run(adminToken, "categories", "help"); !strings.Contains(errb, "move") || strings.Contains(errb, "add-member") {
		t.Errorf("categories help: %s", errb)
	}
	if rc, out, _ := c.run(roToken, "categories", "list"); rc != 0 || !strings.Contains(out, "Household") {
		t.Errorf("read-only reads: %d %s", rc, out)
	}
	c.fail(roToken, 1, "forbidden", "labels", "create", "--name", "x")
	c.fail(roToken, 1, "forbidden", "organisation", "set-category", "commitment", cm.ID, "--none")
	c.fail(scopedTok, 1, "forbidden", "buckets", "list")
}
