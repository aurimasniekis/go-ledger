package main

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisiontasks"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	alertstore "github.com/aurimasniekis/go-ledger/internal/store/alerts"
	decstore "github.com/aurimasniekis/go-ledger/internal/store/decisions"
	organisationstore "github.com/aurimasniekis/go-ledger/internal/store/organisation"
)

// TestDecisionTasksAPI drives the suggestion and alert routes (L6c, ADR-0065)
// as ledger serve wires them: grants (the decisions grants are administrator
// only and accept, correct and revert also need organisation:write), listing
// with filters and pages, provenance, the review actions and their error
// mapping, the audit trail and the alert lifecycle. Suggestions are seeded
// straight into the database, as the decision layer would store them: no
// model, bank or network is involved.
func TestDecisionTasksAPI(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	admin := bankCall{t: t, f: f, hdr: f.signIn(t, "alice")}
	reader := bankCall{t: t, f: f, hdr: f.signIn(t, "bob")}
	tok := func(grants string) bankCall {
		return bankCall{t: t, f: f, hdr: map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": grants}}
	}
	aw := store.NewAuditLog(f.db)
	sugg := decstore.New(f.db, aw, nil, nil)
	org := organisationstore.New(f.db, aw, nil, nil)
	alertSt := alertstore.New(f.db, aw, nil, nil)

	create := func(path, body string) string {
		t.Helper()
		code, m, _ := admin.do("POST", path, body)
		if code != 201 {
			t.Fatalf("create %s: %d %v", path, code, m)
		}
		return m["id"].(string)
	}
	food := create("/api/v1/categories", `{"name":"Food"}`)
	rent := create("/api/v1/categories", `{"name":"Rent"}`)
	l1 := create("/api/v1/labels", `{"name":"Fixed"}`)
	l2 := create("/api/v1/labels", `{"name":"Annual"}`)
	newCommitment := func(name string) string {
		return create("/api/v1/commitments", `{"name":"`+name+`","kind":"service","amount":{"amount":"500.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":1},"start_date":"2026-01-01"}`)
	}
	record := func(task decisions.TaskName, objType, objID string, d decisions.Decision, auto bool) decisions.Suggestion {
		t.Helper()
		sg, err := sugg.Record(ctx, decisions.NewSuggestion{Task: task, Provider: "openai", Model: "gpt-test", ResponseID: "resp_1",
			Object: decisions.ObjectRef{Type: objType, ID: objID}, Decision: d, AutoApply: auto, CreatedAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		return sg
	}
	choice := func(id string) decisions.Decision { return decisions.Decision{Choice: &id, Confidence: "0.9"} }
	// autoApplied stores what the task service stores for an auto-applied
	// categorisation: a model-origin assignment and an accepted suggestion.
	autoApplied := func(objID, category string) decisions.Suggestion {
		t.Helper()
		sg := record(decisions.TaskCategorisation, "commitment", objID, choice(category), true)
		if _, err := org.ApplyAutomatic(ctx, decisiontasks.SystemActor, organisation.ObjectRef{Type: organisation.ObjectCommitment, ID: objID},
			organisation.Automatic{Origin: organisation.OriginModel, Source: decisiontasks.SourceOf(sg), CategoryID: &category}); err != nil {
			t.Fatal(err)
		}
		if _, err := sugg.Decide(ctx, decisiontasks.SystemActor, sg.ID, decisions.StateAccepted, "decision_suggestion.auto_applied"); err != nil {
			t.Fatal(err)
		}
		return sg
	}
	category := func(objType, objID string) (string, string) {
		t.Helper()
		_, m, _ := admin.do("GET", "/api/v1/organisation/"+objType+"/"+objID, "")
		c, _ := m["category"].(map[string]any)
		if c == nil {
			return "", ""
		}
		d, _ := c["category"].(map[string]any)
		id, _ := d["id"].(string)
		return id, c["origin"].(string)
	}

	c1, c2, c3 := newCommitment("One"), newCommitment("Two"), newCommitment("Three")
	s1 := record(decisions.TaskCategorisation, "commitment", c1, choice(food), false)
	s2 := record(decisions.TaskLabelSuggestion, "commitment", c2, decisions.Decision{Choices: []string{l1, l2}}, false)
	s3 := record(decisions.TaskCategorisation, "commitment", c3, decisions.Decision{}, false) // the model chose none

	// Grants: administrator only.
	for _, c := range []struct {
		who  bankCall
		name string
	}{{reader, "read-only role"}, {tok("commitments:write"), "token without decisions grants"}} {
		if code, _, _ := c.who.do("GET", "/api/v1/suggestions", ""); code != 403 {
			t.Errorf("%s listing suggestions: %d", c.name, code)
		}
		if code, _, _ := c.who.do("GET", "/api/v1/alerts", ""); code != 403 {
			t.Errorf("%s listing alerts: %d", c.name, code)
		}
	}
	readTok := tok("decisions:read")
	if code, _, _ := readTok.do("GET", "/api/v1/suggestions/"+s1.ID, ""); code != 200 {
		t.Errorf("decisions:read token reading: %d", code)
	}
	for _, p := range []string{"accept", "correct", "reject", "revert"} {
		if code, _, _ := readTok.do("POST", "/api/v1/suggestions/"+s1.ID+"/"+p, `{"category_id":null}`); code != 403 {
			t.Errorf("decisions:read token %s: %d", p, code)
		}
	}

	// List: provenance, filters, pages and filter errors.
	code, page, _ := admin.do("GET", "/api/v1/suggestions", "")
	items := asList(t, page["items"])
	if code != 200 || len(items) != 3 || page["next_cursor"] != nil {
		t.Fatalf("list: %d %v", code, page)
	}
	first := asMap(t, items[0])
	by := asMap(t, first["suggested_by"])
	if first["id"] != s1.ID || first["state"] != "pending" || first["task"] != "categorisation" || first["object_type"] != "commitment" || first["object_id"] != c1 ||
		by["provider"] != "openai" || by["model"] != "gpt-test" || by["label"] != "openai/gpt-test" || first["provenance"] != "suggested by openai/gpt-test" ||
		asMap(t, first["decision"])["choice"] != food || asMap(t, first["decision"])["confidence"] != "0.9" || first["auto_apply"] != false || first["reversal"] != nil || first["response_id"] != "resp_1" {
		t.Errorf("suggestion: %v", first)
	}
	q := func(v url.Values) (int, map[string]any) {
		code, m, _ := admin.do("GET", "/api/v1/suggestions?"+v.Encode(), "")
		return code, m
	}
	if code, m := q(url.Values{"task": {"label_suggestion"}}); code != 200 || len(asList(t, m["items"])) != 1 || asMap(t, asList(t, m["items"])[0])["id"] != s2.ID {
		t.Errorf("task filter: %d %v", code, m)
	}
	if code, m := q(url.Values{"object_type": {"commitment"}, "object_id": {c3}}); code != 200 || len(asList(t, m["items"])) != 1 {
		t.Errorf("object filter: %d %v", code, m)
	}
	if code, m := q(url.Values{"object_type": {"merchant"}}); code != 200 || len(asList(t, m["items"])) != 0 {
		t.Errorf("object type filter: %d %v", code, m)
	}
	for name, v := range map[string]url.Values{"state": {"state": {"done"}}, "task": {"task": {"alerts"}}, "object_type": {"object_type": {"alert"}},
		"object_id": {"object_id": {"x"}}, "unknown": {"colour": {"red"}}, "operator": {"state[prefix]": {"p"}}} {
		if code, m := q(v); code != 400 {
			t.Errorf("%s filter: %d %v", name, code, m)
		}
	}
	code, p1 := q(url.Values{"limit": {"2"}})
	if code != 200 || len(asList(t, p1["items"])) != 2 || p1["next_cursor"] == nil {
		t.Fatalf("page 1: %d %v", code, p1)
	}
	if code, p2 := q(url.Values{"limit": {"2"}, "cursor": {p1["next_cursor"].(string)}}); code != 200 || len(asList(t, p2["items"])) != 1 || p2["next_cursor"] != nil {
		t.Errorf("page 2: %d %v", code, p2)
	}
	if code, _ := q(url.Values{"limit": {"2"}, "cursor": {p1["next_cursor"].(string)}, "state": {"pending"}}); code != 400 {
		t.Errorf("a cursor with other filters: %d", code)
	}
	if code, _, _ := admin.do("GET", "/api/v1/suggestions/"+resource.NewID(), ""); code != 404 {
		t.Errorf("unknown suggestion: %d", code)
	}

	// Accept: a person's own (user-origin) assignment; the suggestion keeps who said it.
	if code, m, _ := tok("decisions:write").do("POST", "/api/v1/suggestions/"+s1.ID+"/accept", "{}"); code != 403 {
		t.Errorf("accept with decisions:write alone: %d %v", code, m)
	}
	if id, _ := category("commitment", c1); id != "" {
		t.Fatalf("category before accepting: %s", id)
	}
	code, m, hdr := admin.do("POST", "/api/v1/suggestions/"+s1.ID+"/accept", "{}")
	if code != 200 || m["state"] != "accepted" || m["decided_at"] == nil || hdr.Get("ETag") == "" || asMap(t, m["suggested_by"])["label"] != "openai/gpt-test" {
		t.Fatalf("accept: %d %v", code, m)
	}
	if id, origin := category("commitment", c1); id != food || origin != "user" {
		t.Errorf("accepted category: %s origin %s", id, origin)
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s1.ID+"/accept", "{}"); code != 409 || m["code"] != "suggestion_not_pending" {
		t.Errorf("accept twice: %d %v", code, m)
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s1.ID+"/reject", "{}"); code != 409 || m["code"] != "suggestion_not_pending" {
		t.Errorf("reject an accepted suggestion: %d %v", code, m)
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s1.ID+"/revert", "{}"); code != 409 || m["code"] != "not_reversible" {
		t.Errorf("revert a suggestion that was not auto-applied: %d %v", code, m)
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s3.ID+"/accept", "{}"); code != 409 || m["code"] != "nothing_to_apply" {
		t.Errorf("accept a suggestion with no choice: %d %v", code, m)
	}
	if code, _, _ := admin.do("POST", "/api/v1/suggestions/"+resource.NewID()+"/accept", "{}"); code != 404 {
		t.Errorf("accept an unknown suggestion: %d", code)
	}

	// Correct: labels all or nothing; the body must fit the task.
	for name, body := range map[string]string{"empty": `{}`, "category for labels": `{"category_id":"` + food + `"}`, "no labels": `{"label_ids":[]}`, "bad id": `{"label_ids":["x"]}`, "null": `{"label_ids":null}`} {
		if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s2.ID+"/correct", body); code != 400 {
			t.Errorf("correct labels with %s: %d %v", name, code, m)
		}
	}
	if code, m, _ := admin.do("POST", "/api/v1/labels/"+l2+"/archive", "{}", "If-Match", `"1"`); code != 200 {
		t.Fatalf("archive label: %d %v", code, m)
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s2.ID+"/accept", "{}"); code != 409 || m["code"] != "definition_archived" {
		t.Errorf("accept with an archived label: %d %v", code, m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/suggestions/"+s2.ID, ""); m["state"] != "pending" {
		t.Errorf("a refused accept leaves the suggestion pending: %v", m["state"])
	}
	if _, m, _ := admin.do("GET", "/api/v1/organisation/commitment/"+c2, ""); len(asList(t, m["labels"])) != 0 {
		t.Errorf("a refused accept added labels: %v", m["labels"])
	}
	code, m, _ = admin.do("POST", "/api/v1/suggestions/"+s2.ID+"/correct", `{"label_ids":["`+l1+`"]}`)
	if code != 200 || m["state"] != "rejected" {
		t.Fatalf("correct labels: %d %v", code, m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/organisation/commitment/"+c2, ""); len(asList(t, m["labels"])) != 1 || asMap(t, asList(t, m["labels"])[0])["origin"] != "user" {
		t.Errorf("corrected labels: %v", m["labels"])
	}
	// A correction to another category, then "no category".
	s4 := record(decisions.TaskCategorisation, "commitment", newCommitment("Four"), choice(food), false)
	for name, body := range map[string]string{"empty": `{}`, "labels for a category": `{"label_ids":["` + l1 + `"]}`, "bad id": `{"category_id":"x"}`, "number": `{"category_id":5}`} {
		if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s4.ID+"/correct", body); code != 400 {
			t.Errorf("correct a category with %s: %d %v", name, code, m)
		}
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s4.ID+"/correct", `{"category_id":"`+rent+`"}`); code != 200 || m["state"] != "rejected" {
		t.Fatalf("correct category: %d %v", code, m)
	}
	if id, origin := category("commitment", s4.Object.ID); id != rent || origin != "user" {
		t.Errorf("corrected category: %s %s", id, origin)
	}
	s5 := record(decisions.TaskCategorisation, "commitment", newCommitment("Five"), choice(food), false)
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+s5.ID+"/correct", `{"category_id":null}`); code != 200 || m["state"] != "rejected" {
		t.Fatalf("correct to no category: %d %v", code, m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/organisation/commitment/"+s5.Object.ID, ""); asMap(t, m["category"])["category"] != nil {
		t.Errorf("no category chosen: %v", m["category"])
	}

	// Reject assigns nothing and needs no organisation grant.
	s6 := record(decisions.TaskCategorisation, "commitment", newCommitment("Six"), choice(food), false)
	if code, m, _ := tok("decisions:write").do("POST", "/api/v1/suggestions/"+s6.ID+"/reject", "{}"); code != 200 || m["state"] != "rejected" {
		t.Errorf("reject: %d %v", code, m)
	}
	if id, _ := category("commitment", s6.Object.ID); id != "" {
		t.Errorf("reject assigned %s", id)
	}

	// A merchant's assignments need merchants:write as well.
	sm := record(decisions.TaskCategorisation, "merchant", resource.NewID(), choice(food), false)
	if code, m, _ := tok("decisions:write,organisation:write").do("POST", "/api/v1/suggestions/"+sm.ID+"/accept", "{}"); code != 403 {
		t.Errorf("accept a merchant suggestion without merchants:write: %d %v", code, m)
	}

	// Revert: only while the category is still the one the model applied.
	c7, c8 := newCommitment("Seven"), newCommitment("Eight")
	a1 := autoApplied(c7, food)
	if id, origin := category("commitment", c7); id != food || origin != "model" {
		t.Fatalf("auto-applied: %s %s", id, origin)
	}
	if code, m, _ := tok("decisions:write").do("POST", "/api/v1/suggestions/"+a1.ID+"/revert", "{}"); code != 403 {
		t.Errorf("revert with decisions:write alone: %d %v", code, m)
	}
	code, m, _ = admin.do("POST", "/api/v1/suggestions/"+a1.ID+"/revert", "{}")
	rev, _ := m["reversal"].(map[string]any)
	if code != 200 || m["state"] != "reverted" || rev == nil || rev["at"] == nil || asMap(t, rev["by"])["kind"] != "user" || m["auto_apply"] != true {
		t.Fatalf("revert: %d %v", code, m)
	}
	if id, _ := category("commitment", c7); id != "" {
		t.Errorf("revert left category %s", id)
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+a1.ID+"/revert", "{}"); code != 409 || m["code"] != "not_reversible" {
		t.Errorf("revert twice: %d %v", code, m)
	}
	a2 := autoApplied(c8, food)
	if code, m, _ := admin.do("PUT", "/api/v1/organisation/commitment/"+c8+"/category", `{"category_id":"`+rent+`"}`); code != 200 {
		t.Fatalf("person's own category: %d %v", code, m)
	}
	if code, m, _ := admin.do("POST", "/api/v1/suggestions/"+a2.ID+"/revert", "{}"); code != 409 || m["code"] != "not_reversible" {
		t.Errorf("revert after the person decided: %d %v", code, m)
	}
	if id, origin := category("commitment", c8); id != rent || origin != "user" {
		t.Errorf("the person's category survived: %s %s", id, origin)
	}

	// The state filter: accepted excludes reverted ones.
	for state, want := range map[string]int{"pending": 2, "rejected": 4, "reverted": 1, "accepted": 2, "superseded": 0} {
		if code, m := q(url.Values{"state": {state}}); code != 200 || len(asList(t, m["items"])) != want {
			t.Errorf("state %s: %d, %d items, want %d", state, code, len(asList(t, m["items"])), want)
		}
	}
	if code, m := q(url.Values{"state": {"pending", "reverted"}}); code != 200 || len(asList(t, m["items"])) != 3 {
		t.Errorf("two states: %d %v", code, m)
	}

	// Every action is audited, without the model's answer.
	for _, action := range []string{"decision_suggestion.accepted", "decision_suggestion.corrected", "decision_suggestion.rejected", "decision_suggestion.reverted"} {
		code, m, _ := admin.do("GET", "/api/v1/audit?action="+action, "")
		if code != 200 || len(asList(t, m["items"])) == 0 {
			t.Errorf("audit %s: %d %v", action, code, m)
		}
	}

	// Alerts: raised by Ledger, closed once, a dismissal sticks.
	raise := func(key, objID string) alerts.Alert {
		t.Helper()
		a, created, err := alertSt.Raise(ctx, decisiontasks.SystemActor, alerts.Condition{Kind: alerts.KindUnmatchedOverdue, ObjectType: alerts.ObjectOccurrence, ObjectID: objID, Key: key,
			Facts: map[string]string{"due_date": "2026-11-01", "outstanding": "500.00", "currency": "EUR"}, Message: "Overdue since 2026-11-01 with no matching payment recorded: 500.00 EUR still due."})
		if err != nil || !created {
			t.Fatal(created, err)
		}
		return a
	}
	al1, al2 := raise("k1", c1), raise("k2", c2)
	if err := alertSt.SetWording(ctx, al1.ID, alerts.Wording{Text: "Rent is late.", Provider: "openai", Model: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	code, am, _ := admin.do("GET", "/api/v1/alerts", "")
	list := asList(t, am["items"])
	if code != 200 || len(list) != 2 {
		t.Fatalf("alerts: %d %v", code, am)
	}
	a := asMap(t, list[0])
	mt := asMap(t, a["model_text"])
	if a["id"] != al1.ID || a["state"] != "open" || a["closed_at"] != nil || asMap(t, a["facts"])["outstanding"] != "500.00" || asMap(t, a["facts"])["currency"] != "EUR" ||
		mt["text"] != "Rent is late." || mt["label"] != "openai/gpt-test" || mt["provenance"] != "worded by openai/gpt-test" || asMap(t, list[1])["model_text"] != nil {
		t.Errorf("alert: %v", a)
	}
	for name, v := range map[string]string{"state": "state=gone", "kind": "kind=x", "object_type": "object_type=x", "unknown": "colour=red"} {
		if code, _, _ := admin.do("GET", "/api/v1/alerts?"+v, ""); code != 400 {
			t.Errorf("alert filter %s: %d", name, code)
		}
	}
	if code, m, _ := tok("decisions:read").do("POST", "/api/v1/alerts/"+al1.ID+"/dismiss", "{}"); code != 403 {
		t.Errorf("dismiss with decisions:read: %d %v", code, m)
	}
	code, m, hdr = tok("decisions:write").do("POST", "/api/v1/alerts/"+al1.ID+"/dismiss", "{}")
	if code != 200 || m["state"] != "dismissed" || m["closed_at"] == nil || hdr.Get("ETag") == "" {
		t.Fatalf("dismiss: %d %v", code, m)
	}
	if code, m, _ := admin.do("POST", "/api/v1/alerts/"+al1.ID+"/dismiss", "{}"); code != 409 || m["code"] != "alert_not_open" {
		t.Errorf("dismiss twice: %d %v", code, m)
	}
	if code, _, _ := admin.do("POST", "/api/v1/alerts/"+resource.NewID()+"/dismiss", "{}"); code != 404 {
		t.Errorf("dismiss an unknown alert: %d", code)
	}
	if _, m, _ := admin.do("GET", "/api/v1/alerts?state=open", ""); len(asList(t, m["items"])) != 1 || asMap(t, asList(t, m["items"])[0])["id"] != al2.ID {
		t.Errorf("open alerts: %v", m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/alerts?state=dismissed&kind=unmatched_overdue&object_type=occurrence&object_id="+c1, ""); len(asList(t, m["items"])) != 1 {
		t.Errorf("dismissed alerts: %v", m)
	}
	if _, m, _ := admin.do("GET", "/api/v1/alerts/"+al1.ID, ""); m["state"] != "dismissed" {
		t.Errorf("get dismissed alert: %v", m)
	}
	// A dismissed condition is not raised again.
	if _, created, err := alertSt.Raise(ctx, decisiontasks.SystemActor, alerts.Condition{Kind: alerts.KindUnmatchedOverdue, ObjectType: alerts.ObjectOccurrence, ObjectID: c1, Key: "k1",
		Facts: map[string]string{}, Message: "again"}); err != nil || created {
		t.Errorf("a dismissed alert was raised again: %v %v", created, err)
	}
	if code, m, _ := admin.do("GET", "/api/v1/audit?action=alert.closed", ""); code != 200 || len(asList(t, m["items"])) != 1 {
		t.Errorf("audit alert.closed: %d %v", code, m)
	}

	// Every action that can change an assignment is gated alike: decisions:write
	// alone is not enough, and a merchant also needs merchants:write.
	for _, tc := range []struct{ name, scopes, object string }{
		{"decisions:write alone, commitment", "decisions:write", "commitment"},
		{"decisions:write alone, merchant", "decisions:write", "merchant"},
		{"merchant without merchants:write", "decisions:write,organisation:write", "merchant"},
	} {
		for _, action := range []struct{ path, body string }{
			{"accept", "{}"}, {"correct", `{"category_id":"` + food + `"}`}, {"revert", "{}"},
		} {
			obj := resource.NewID()
			if tc.object == "commitment" {
				obj = newCommitment("Gate " + action.path)
			}
			sg := record(decisions.TaskCategorisation, tc.object, obj, choice(food), action.path == "revert")
			if code, m, _ := tok(tc.scopes).do("POST", "/api/v1/suggestions/"+sg.ID+"/"+action.path, action.body); code != 403 {
				t.Errorf("%s %s: %d %v", action.path, tc.name, code, m)
			}
			if got, err := sugg.Suggestion(ctx, sg.ID); err != nil || got.State != decisions.StatePending {
				t.Errorf("%s %s changed the suggestion: %v %v", action.path, tc.name, got.State, err)
			}
			if id, _ := category(tc.object, obj); id != "" {
				t.Errorf("%s %s assigned %s", action.path, tc.name, id)
			}
		}
	}
}
