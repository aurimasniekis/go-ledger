package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisions"
	"github.com/aurimasniekis/go-ledger/internal/domain/decisiontasks"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/store"
	alertstore "github.com/aurimasniekis/go-ledger/internal/store/alerts"
	decstore "github.com/aurimasniekis/go-ledger/internal/store/decisions"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

func seedSuggestion(t *testing.T, st *decstore.Store, task decisions.TaskName, objID string, d decisions.Decision) decisions.Suggestion {
	t.Helper()
	sg, err := st.Record(context.Background(), decisions.NewSuggestion{Task: task, Provider: "openai", Model: "gpt-test",
		Object: decisions.ObjectRef{Type: "commitment", ID: objID}, Decision: d, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

func seedAlert(t *testing.T, st *alertstore.Store, key string) alerts.Alert {
	t.Helper()
	a, _, err := st.Raise(context.Background(), decisiontasks.SystemActor, alerts.Condition{Kind: alerts.KindUnmatchedOverdue, ObjectType: alerts.ObjectOccurrence, ObjectID: resource.NewID(), Key: key,
		Facts: map[string]string{"outstanding": "10.00", "currency": "EUR"}, Message: "Overdue with no matching payment recorded: 10.00 EUR still due."})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestCLIDecisions drives the suggestions and alerts commands (L6c) against the
// real API: listing with filters, the review actions, error mapping and grants.
func TestCLIDecisions(t *testing.T) {
	c := newCLIFixture(t)
	aw := store.NewAuditLog(c.api.db)
	sugg, al := decstore.New(c.api.db, aw, nil, nil), alertstore.New(c.api.db, aw, nil, nil)
	var food, cm idVersion
	c.json(adminToken, &food, "categories", "create", "--name", "Food")
	var cat struct{ ID string }
	c.json(adminToken, &cat, "categories", "create", "--name", "Home")
	c.json(adminToken, &cm, "commitments", "create", "--name", "Rent", "--kind", "subscription", "--amount", "900.00", "--currency", "EUR",
		"--frequency", "monthly", "--day", "1", "--start-date", "2026-01-01")
	s1 := seedSuggestion(t, sugg, decisions.TaskCategorisation, cm.ID, decisions.Decision{Choice: &food.ID})
	var cm2, cm3 idVersion
	for i, out := range []*idVersion{&cm2, &cm3} {
		c.json(adminToken, out, "commitments", "create", "--name", "Other"+string(rune('A'+i)), "--kind", "subscription", "--amount", "9.00", "--currency", "EUR",
			"--frequency", "monthly", "--day", "1", "--start-date", "2026-01-01")
	}
	s2 := seedSuggestion(t, sugg, decisions.TaskCategorisation, cm2.ID, decisions.Decision{Choice: &cat.ID})

	var page struct {
		Items []ledgerclient.Suggestion `json:"items"`
	}
	c.json(adminToken, &page, "suggestions", "list", "--state", "pending", "--all")
	if len(page.Items) != 2 {
		t.Fatalf("pending: %+v", page.Items)
	}
	if rc, out, _ := c.run(adminToken, "suggestions", "list"); rc != 0 || !strings.Contains(out, "openai/gpt-test") || !strings.Contains(out, "pending") {
		t.Fatalf("list text: %d %s", rc, out)
	}
	c.fail(adminToken, 1, "validation_failed", "suggestions", "list", "--state", "nope")
	var got ledgerclient.Suggestion
	c.json(adminToken, &got, "suggestions", "get", s1.ID)
	if got.Provenance != "suggested by openai/gpt-test" || got.Decision.Choice == nil || *got.Decision.Choice != food.ID {
		t.Fatalf("get: %+v", got)
	}
	if rc, out, _ := c.run(adminToken, "suggestions", "get", s1.ID); rc != 0 || !strings.Contains(out, "suggested by:") {
		t.Fatalf("get text: %d %s", rc, out)
	}

	// Accept, then the state has moved: a structured conflict.
	c.json(adminToken, &got, "suggestions", "accept", s1.ID)
	if got.State != "accepted" {
		t.Fatalf("accept: %+v", got)
	}
	c.fail(adminToken, 1, "suggestion_not_pending", "suggestions", "accept", s1.ID)
	c.fail(adminToken, 1, "not_reversible", "suggestions", "revert", s1.ID)
	// Correct and reject.
	c.fail(adminToken, 2, "exactly one of", "suggestions", "correct", s2.ID)
	c.fail(adminToken, 2, "exactly one of", "suggestions", "correct", s2.ID, "--none", "--category", cat.ID)
	c.json(adminToken, &got, "suggestions", "correct", s2.ID, "--none")
	if got.State != "rejected" {
		t.Fatalf("correct: %+v", got)
	}
	s3 := seedSuggestion(t, sugg, decisions.TaskCategorisation, cm3.ID, decisions.Decision{Choice: &food.ID})
	c.json(adminToken, &got, "suggestions", "reject", s3.ID)
	if got.State != "rejected" {
		t.Fatalf("reject: %+v", got)
	}
	// Grants: the read-only role holds no decisions grant.
	c.fail(roToken, 1, "forbidden", "suggestions", "list")
	c.fail(roToken, 1, "forbidden", "alerts", "list")

	// Alerts.
	a := seedAlert(t, al, "k1")
	var ap struct {
		Items []ledgerclient.Alert `json:"items"`
	}
	c.json(adminToken, &ap, "alerts", "list", "--state", "open", "--kind", "unmatched_overdue")
	if len(ap.Items) != 1 || ap.Items[0].ID != a.ID {
		t.Fatalf("alerts: %+v", ap.Items)
	}
	var ga ledgerclient.Alert
	c.json(adminToken, &ga, "alerts", "get", a.ID)
	if ga.State != "open" || ga.Facts["currency"] != "EUR" || ga.ModelText != nil {
		t.Fatalf("alert: %+v", ga)
	}
	c.json(adminToken, &ga, "alerts", "dismiss", a.ID)
	if ga.State != "dismissed" || ga.ClosedAt == nil {
		t.Fatalf("dismiss: %+v", ga)
	}
	c.fail(adminToken, 1, "alert_not_open", "alerts", "dismiss", a.ID)
	if rc, out, _ := c.run(adminToken, "alerts", "list", "--state", "dismissed"); rc != 0 || !strings.Contains(out, "dismissed") {
		t.Fatalf("alerts text: %d %s", rc, out)
	}
}

// TestMCPDecisions walks the suggestion and alert tools over real MCP and the
// real API: the HTTP validation, grants (accept also needs organisation:write)
// and error codes apply, and the provenance is in the answer.
func TestMCPDecisions(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	sugg, al := f.parts.suggestions, f.parts.alerts
	secret, _ := f.token(t, f.admin, "decisions:write", "organisation:write", "commitments:write")
	c := f.connect(t, secret)
	cat := decodeJSONText[ledgerclient.Definition](t, call(t, c, "create_category", map[string]any{"name": "Food"}))
	cm := decodeJSONText[struct{ ID string }](t, call(t, c, "create_commitment", newCommitmentArgs("Rent")))
	s1 := seedSuggestion(t, sugg, decisions.TaskCategorisation, cm.ID, decisions.Decision{Choice: &cat.ID})

	r := call(t, c, "list_suggestions", map[string]any{"state": []string{"pending"}, "object_type": "commitment"})
	if r.IsError || !strings.Contains(resultText(r), s1.ID) || !strings.Contains(resultText(r), "suggested by openai/gpt-test") {
		t.Fatalf("list_suggestions: %s", resultText(r))
	}
	if r := call(t, c, "list_suggestions", map[string]any{"state": []string{"nope"}}); !r.IsError {
		t.Errorf("an unknown state must be refused: %s", resultText(r))
	}
	got := decodeJSONText[ledgerclient.Suggestion](t, call(t, c, "accept_suggestion", map[string]any{"id": s1.ID}))
	if got.State != "accepted" || got.SuggestedBy.Label != "openai/gpt-test" {
		t.Fatalf("accept: %+v", got)
	}
	if r := call(t, c, "accept_suggestion", map[string]any{"id": s1.ID}); !r.IsError || !strings.Contains(resultText(r), "suggestion_not_pending") {
		t.Errorf("accept twice: %s", resultText(r))
	}
	if r := call(t, c, "revert_suggestion", map[string]any{"id": s1.ID}); !r.IsError || !strings.Contains(resultText(r), "not_reversible") {
		t.Errorf("revert: %s", resultText(r))
	}
	cm2 := decodeJSONText[struct{ ID string }](t, call(t, c, "create_commitment", newCommitmentArgs("Water")))
	cm3 := decodeJSONText[struct{ ID string }](t, call(t, c, "create_commitment", newCommitmentArgs("Gas")))
	cm4 := decodeJSONText[struct{ ID string }](t, call(t, c, "create_commitment", newCommitmentArgs("Phone")))
	s2 := seedSuggestion(t, sugg, decisions.TaskCategorisation, cm2.ID, decisions.Decision{Choice: &cat.ID})
	if r := call(t, c, "correct_suggestion", map[string]any{"id": s2.ID}); !r.IsError {
		t.Errorf("a correction without an answer: %s", resultText(r))
	}
	if r := call(t, c, "correct_suggestion", map[string]any{"id": s2.ID, "category_id": nil}); r.IsError || !strings.Contains(resultText(r), `"state":"rejected"`) {
		t.Errorf("correct: %s", resultText(r))
	}
	s3 := seedSuggestion(t, sugg, decisions.TaskCategorisation, cm3.ID, decisions.Decision{Choice: &cat.ID})
	if r := call(t, c, "reject_suggestion", map[string]any{"id": s3.ID}); r.IsError || !strings.Contains(resultText(r), `"state":"rejected"`) {
		t.Errorf("reject: %s", resultText(r))
	}

	a := seedAlert(t, al, "k1")
	if r := call(t, c, "list_alerts", map[string]any{"state": []string{"open"}}); r.IsError || !strings.Contains(resultText(r), a.ID) {
		t.Errorf("list_alerts: %s", resultText(r))
	}
	if r := call(t, c, "get_alert", map[string]any{"id": a.ID}); r.IsError || !strings.Contains(resultText(r), "still due") {
		t.Errorf("get_alert: %s", resultText(r))
	}
	if r := call(t, c, "dismiss_alert", map[string]any{"id": a.ID}); r.IsError || !strings.Contains(resultText(r), `"state":"dismissed"`) {
		t.Errorf("dismiss_alert: %s", resultText(r))
	}
	if r := call(t, c, "dismiss_alert", map[string]any{"id": a.ID}); !r.IsError || !strings.Contains(resultText(r), "alert_not_open") {
		t.Errorf("dismiss twice: %s", resultText(r))
	}

	// Without the write grant a tool names it; accepting also names organisation:write.
	ws, _ := f.token(t, f.admin, "decisions:write")
	wc := f.connect(t, ws)
	s4 := seedSuggestion(t, sugg, decisions.TaskCategorisation, cm4.ID, decisions.Decision{Choice: &cat.ID})
	if r := call(t, wc, "accept_suggestion", map[string]any{"id": s4.ID}); !r.IsError || !strings.Contains(resultText(r), "organisation:write") {
		t.Errorf("accept without organisation:write: %s", resultText(r))
	}
	rs, _ := f.token(t, f.admin, "decisions:read")
	rc := f.connect(t, rs)
	if r := call(t, rc, "reject_suggestion", map[string]any{"id": s4.ID}); !r.IsError || !strings.Contains(resultText(r), "decisions:write") {
		t.Errorf("reject with a read grant: %s", resultText(r))
	}
}
