package ledgerclient

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestMerchantRequestShape(t *testing.T) {
	var got []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		raw, _ := json.Marshal(b)
		got = append(got, r.Method+" "+r.URL.EscapedPath()+" ?"+r.URL.RawQuery+" if-match="+r.Header.Get("If-Match")+" "+string(raw))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/merchants":
			writeJSON(w, 200, Page[MerchantRecord]{})
		case r.URL.Path == "/api/v1/merchants/sources":
			writeJSON(w, 200, map[string]any{"sources": []MerchantSource{{Name: "curated", Kind: "local"}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/merchants/m1":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/merge"):
			writeJSON(w, 200, MerchantMerge{Merchant: MerchantRecord{ID: "m1"}, Moved: MerchantMoved{Aliases: 2, Decisions: 1}, Kept: 3, Replaced: 1})
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
	_, err := c.ListMerchants(ctx, MerchantFilter{Archived: "true"})
	must(err)
	_, err = c.CreateMerchant(ctx, NewMerchant{DisplayName: "Lidl", Aliases: []string{"Lidl LT"}})
	must(err)
	_, err = c.GetMerchant(ctx, "m1")
	must(err)
	_, err = c.UpdateMerchant(ctx, "m1", 2, MergePatch{"display_name": "Lidl Lietuva"})
	must(err)
	_, err = c.ArchiveMerchant(ctx, "m1", 3)
	must(err)
	_, err = c.UnarchiveMerchant(ctx, "m1", 4)
	must(err)
	must(c.DeleteMerchant(ctx, "m1", 5))
	_, err = c.AddMerchantAlias(ctx, "m1", "Lidl Vilnius")
	must(err)
	_, err = c.RemoveMerchantAlias(ctx, "m1", "a/1")
	must(err)
	merge, err := c.MergeMerchants(ctx, "m1", 6, "m2")
	must(err)
	if merge.Moved.Aliases != 2 || merge.Moved.Decisions != 1 || merge.Kept != 3 || merge.Replaced != 1 {
		t.Errorf("merge answer decoded as %+v", merge)
	}
	_, err = c.GetTransactionMerchant(ctx, "tx1")
	must(err)
	_, err = c.ReviewTransactionMerchant(ctx, "tx1", MerchantDecision{Decision: MerchantCorrected, MerchantID: "m1"})
	must(err)
	_, err = c.ReviewTransactionMerchant(ctx, "tx1", MerchantDecision{Decision: MerchantRejected})
	must(err)
	_, err = c.ClearTransactionMerchantReview(ctx, "tx1")
	must(err)
	srcs, err := c.MerchantSources(ctx)
	must(err)
	if len(srcs) != 1 || srcs[0].Name != "curated" {
		t.Errorf("sources decoded as %+v", srcs)
	}

	want := []string{
		"GET /api/v1/merchants ?archived=true if-match= null",
		"POST /api/v1/merchants ? if-match= {\"aliases\":[\"Lidl LT\"],\"display_name\":\"Lidl\"}",
		"GET /api/v1/merchants/m1 ? if-match= null",
		"PATCH /api/v1/merchants/m1 ? if-match=\"2\" {\"display_name\":\"Lidl Lietuva\"}",
		"POST /api/v1/merchants/m1/archive ? if-match=\"3\" {}",
		"POST /api/v1/merchants/m1/unarchive ? if-match=\"4\" {}",
		"DELETE /api/v1/merchants/m1 ? if-match=\"5\" null",
		"POST /api/v1/merchants/m1/aliases ? if-match= {\"name\":\"Lidl Vilnius\"}",
		"DELETE /api/v1/merchants/m1/aliases/a%2F1 ? if-match= null",
		"POST /api/v1/merchants/m1/merge ? if-match=\"6\" {\"merchant_id\":\"m2\"}",
		"GET /api/v1/transactions/tx1/merchant ? if-match= null",
		"PUT /api/v1/transactions/tx1/merchant/review ? if-match= {\"decision\":\"corrected\",\"merchant_id\":\"m1\"}",
		"PUT /api/v1/transactions/tx1/merchant/review ? if-match= {\"decision\":\"rejected\"}",
		"DELETE /api/v1/transactions/tx1/merchant/review ? if-match= null",
		"GET /api/v1/merchants/sources ? if-match= null",
	}
	if !slices.Equal(got, want) {
		t.Errorf("requests:\n got  %q\n want %q", got, want)
	}
}
