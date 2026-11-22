package ledgerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestDecisionsRequestShape(t *testing.T) {
	var got []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		raw, _ := json.Marshal(b)
		got = append(got, r.Method+" "+r.URL.Path+" ?"+r.URL.RawQuery+" "+string(raw))
		if r.Method == http.MethodGet && (r.URL.Path == "/api/v1/suggestions" || r.URL.Path == "/api/v1/alerts") {
			writeJSON(w, 200, map[string]any{"items": []any{}, "next_cursor": nil})
			return
		}
		writeJSON(w, 200, map[string]any{})
	})
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.ListSuggestions(ctx, SuggestionFilter{ListOptions: ListOptions{Limit: 5}, States: []string{"pending", "reverted"}, Tasks: []string{"categorisation"}, ObjectType: "commitment"})
	must(err)
	_, err = c.GetSuggestion(ctx, "s1")
	must(err)
	_, err = c.AcceptSuggestion(ctx, "s1")
	must(err)
	cat := "c1"
	_, err = c.CorrectSuggestion(ctx, "s1", SuggestionCorrection{CategoryID: &cat})
	must(err)
	_, err = c.CorrectSuggestion(ctx, "s1", SuggestionCorrection{SetCategory: true})
	must(err)
	_, err = c.CorrectSuggestion(ctx, "s2", SuggestionCorrection{LabelIDs: []string{"l1", "l2"}})
	must(err)
	_, err = c.RejectSuggestion(ctx, "s1")
	must(err)
	_, err = c.RevertSuggestion(ctx, "s1")
	must(err)
	_, err = c.ListAlerts(ctx, AlertFilter{States: []string{"open"}, Kinds: []string{"consent_expiring", "unmatched_overdue"}})
	must(err)
	_, err = c.GetAlert(ctx, "a1")
	must(err)
	_, err = c.DismissAlert(ctx, "a1")
	must(err)
	want := []string{
		"GET /api/v1/suggestions ?limit=5&object_type=commitment&state=pending&state=reverted&task=categorisation null",
		"GET /api/v1/suggestions/s1 ? null",
		"POST /api/v1/suggestions/s1/accept ? {}",
		"POST /api/v1/suggestions/s1/correct ? {\"category_id\":\"c1\"}",
		"POST /api/v1/suggestions/s1/correct ? {\"category_id\":null}",
		"POST /api/v1/suggestions/s2/correct ? {\"label_ids\":[\"l1\",\"l2\"]}",
		"POST /api/v1/suggestions/s1/reject ? {}",
		"POST /api/v1/suggestions/s1/revert ? {}",
		"GET /api/v1/alerts ?kind=consent_expiring&kind=unmatched_overdue&state=open null",
		"GET /api/v1/alerts/a1 ? null",
		"POST /api/v1/alerts/a1/dismiss ? {}",
	}
	if len(got) != len(want) {
		t.Fatalf("requests: %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d:\n got %s\nwant %s", i, got[i], want[i])
		}
	}
}
