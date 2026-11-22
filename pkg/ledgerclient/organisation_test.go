package ledgerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestOrganisationRequestShape(t *testing.T) {
	var got []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		raw, _ := json.Marshal(b)
		got = append(got, r.Method+" "+r.URL.Path+" ?"+r.URL.RawQuery+" if-match="+r.Header.Get("If-Match")+" "+string(raw))
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/labels" || r.URL.Path == "/api/v1/buckets/b1/members"):
			writeJSON(w, 200, Page[Definition]{})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/members/"), r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/members/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			writeJSON(w, 200, map[string]any{})
		}
	})
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.ListDefinitions(ctx, KindLabel, DefinitionFilter{Archived: "false", NamePrefix: "ta"})
	must(err)
	_, err = c.CreateDefinition(ctx, KindCategory, NewDefinition{Name: "Food"})
	must(err)
	_, err = c.GetDefinition(ctx, KindBucket, "b1")
	must(err)
	_, err = c.ArchiveDefinition(ctx, KindLabel, "l1", 3)
	must(err)
	_, err = c.DeleteDefinition(ctx, KindCategory, "c1", 4, "c2")
	must(err)
	_, err = c.DeleteDefinition(ctx, KindBucket, "b1", 5, "")
	must(err)
	_, err = c.ListBucketMembers(ctx, "b1", ListOptions{})
	must(err)
	must(c.AddBucketMember(ctx, "b1", "commitment", "c-1"))
	must(c.RemoveBucketMember(ctx, "b1", "account", "a-1"))
	_, err = c.GetObjectOrganisation(ctx, "transaction", "t-1")
	must(err)
	cat := "c9"
	_, err = c.SetObjectCategory(ctx, "commitment", "c-1", &cat)
	must(err)
	_, err = c.SetObjectCategory(ctx, "commitment", "c-1", nil)
	must(err)
	_, err = c.ResetObjectCategory(ctx, "commitment", "c-1")
	must(err)
	_, err = c.AddObjectLabel(ctx, "account", "a-1", "l-7")
	must(err)
	_, err = c.RemoveObjectLabel(ctx, "account", "a-1", "l-7")
	must(err)

	want := []string{
		"GET /api/v1/labels ?archived=false&name%5Bprefix%5D=ta if-match= null",
		"POST /api/v1/categories ? if-match= {\"name\":\"Food\"}",
		"GET /api/v1/buckets/b1 ? if-match= null",
		"POST /api/v1/labels/l1/archive ? if-match=",
		"DELETE /api/v1/categories/c1 ?reassign_to=c2 if-match=",
		"DELETE /api/v1/buckets/b1 ? if-match=",
		"GET /api/v1/buckets/b1/members ? if-match= null",
		"PUT /api/v1/buckets/b1/members/commitment/c-1 ? if-match= null",
		"DELETE /api/v1/buckets/b1/members/account/a-1 ? if-match= null",
		"GET /api/v1/organisation/transaction/t-1 ? if-match= null",
		"PUT /api/v1/organisation/commitment/c-1/category ? if-match= {\"category_id\":\"c9\"}",
		"PUT /api/v1/organisation/commitment/c-1/category ? if-match= {\"category_id\":null}", // explicit null: the person chose none
		"DELETE /api/v1/organisation/commitment/c-1/category ? if-match= null",
		"PUT /api/v1/organisation/account/a-1/labels/l-7 ? if-match= null",
		"DELETE /api/v1/organisation/account/a-1/labels/l-7 ? if-match= null",
	}
	if len(got) != len(want) {
		t.Fatalf("%d requests, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("request %d:\n got  %s\n want %s…", i, got[i], want[i])
		}
	}
	// Versioned operations send the version as If-Match.
	for _, i := range []int{3, 4, 5} {
		if !strings.Contains(got[i], "if-match=") || strings.Contains(got[i], "if-match= ") {
			t.Errorf("request %d has no If-Match: %s", i, got[i])
		}
	}
}

func TestOrganisationUnknownKindIsAnError(t *testing.T) {
	called := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	ctx := context.Background()
	if _, err := c.ListDefinitions(ctx, "buckets", DefinitionFilter{}); err == nil {
		t.Error("ListDefinitions accepted an unknown kind")
	}
	if _, err := c.CreateDefinition(ctx, "", NewDefinition{Name: "x"}); err == nil {
		t.Error("CreateDefinition accepted an empty kind")
	}
	if _, err := c.DeleteDefinition(ctx, "tag", "x", 1, ""); err == nil {
		t.Error("DeleteDefinition accepted an unknown kind")
	}
	if called {
		t.Error("an unknown kind must not reach the server")
	}
}
