package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
)

// orgFixture creates a definition through the API and returns its ID.
func (f *apiFixture) orgCreate(t *testing.T, path, body string) string {
	t.Helper()
	rec := f.do("POST", "/api/v1/"+path, body, map[string]string{"X-Test-User": f.ids["alice"]})
	if rec.Code != 201 {
		t.Fatalf("create %s %s: %d %s", path, body, rec.Code, rec.Body.String())
	}
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func (f *apiFixture) orgSetCategory(t *testing.T, objType, id, category string) {
	t.Helper()
	rec := f.do("PUT", "/api/v1/organisation/"+objType+"/"+id+"/category", `{"category_id":"`+category+`"}`, map[string]string{"X-Test-User": f.ids["alice"]})
	if rec.Code != 200 {
		t.Fatalf("set category: %d %s", rec.Code, rec.Body.String())
	}
}

func idsOf(t *testing.T, body []byte, key string) []string {
	t.Helper()
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	var out []string
	for _, it := range page.Items {
		out = append(out, fmt.Sprint(it[key]))
	}
	return out
}

// TestOutlookBucketSlice slices the outlook by bucket through the API: only
// the bucket's commitments and accounts are counted, per currency and never
// mixed, the response says it is a slice, the whole outlook says it is not,
// conversion follows the usual policy, and the slice needs the organisation
// read grant.
func TestOutlookBucketSlice(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	f.now = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	rent := f.createCommitment(t, `{"name":"Rent","kind":"subscription","amount":{"amount":"100.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-10-01"}`)
	phone := f.createCommitment(t, `{"name":"Phone","kind":"subscription","amount":{"amount":"25.00","currency":"GBP"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":20},"start_date":"2026-11-01"}`)
	b := importFakeBank(t, f)
	f.now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	accts, err := b.facts.Accounts(context.Background(), b.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	byCur := map[string]banking.AccountID{}
	for _, a := range accts {
		if _, seen := byCur[a.Currency.Code()]; !seen && a.Name != "Multi-currency wallet" {
			byCur[a.Currency.Code()] = a.ID
		}
	}
	eurAcct, gbpAcct := string(byCur["EUR"]), string(byCur["GBP"])
	if eurAcct == "" || gbpAcct == "" {
		t.Fatalf("accounts %+v", accts)
	}
	member := func(bucket, typ, id string) {
		t.Helper()
		if rec := f.do("PUT", "/api/v1/buckets/"+bucket+"/members/"+typ+"/"+id, "", admin); rec.Code != 204 {
			t.Fatalf("add member: %d %s", rec.Code, rec.Body.String())
		}
	}

	whole, _ := f.outlook(t, "?currency=EUR", admin)
	if whole.Slice != nil {
		t.Errorf("the whole outlook claims to be a slice: %+v", whole.Slice)
	}

	// Rent and the euro account.
	home := f.orgCreate(t, "buckets", `{"name":"Home"}`)
	member(home, "commitment", rent)
	member(home, "account", eurAcct)
	o, _ := f.outlook(t, "?currency=EUR&bucket="+home, admin)
	want := "EUR: due 200.00 EUR overdue 100.00 EUR upcoming 100.00 EUR balance 1250.40 EUR less 1050.40 EUR\n"
	if got := currenciesOf(o); got != want {
		t.Errorf("home slice:\n%s\nwant:\n%s", got, want)
	}
	if o.Slice == nil || o.Slice.Bucket.ID != home || o.Slice.Bucket.Name != "Home" || o.Slice.CommitmentCount != 1 || o.Slice.AccountCount != 1 ||
		!strings.Contains(o.Slice.Note, "allocates no money") || !strings.Contains(o.Slice.Note, "safe to spend") {
		t.Errorf("slice %+v", o.Slice)
	}
	if !strings.Contains(o.Notice, "does not tell you how much money is safe to spend") || len(o.Accounts) != 1 || !o.Balances.Shown {
		t.Errorf("notice %q accounts %d", o.Notice, len(o.Accounts))
	}
	if c := o.Converted; c == nil || !c.Complete || c.StillDue.String() != "200.00 EUR" || c.Balance.String() != "1250.40 EUR" || len(c.Excluded) != 0 {
		t.Errorf("converted %+v", c)
	}

	// The phone bill alone: no account, so no balance, and the pound line is
	// excluded from a euro conversion exactly as in the whole outlook.
	bills := f.orgCreate(t, "buckets", `{"name":"Phone bills"}`)
	member(bills, "commitment", phone)
	p, _ := f.outlook(t, "?currency=EUR&bucket="+bills, admin)
	if got := currenciesOf(p); got != "GBP: due 25.00 GBP overdue 0.00 GBP upcoming 25.00 GBP balance <nil> less <nil>\n" || len(p.Accounts) != 0 {
		t.Errorf("phone slice: %q accounts %d", got, len(p.Accounts))
	}
	if c := p.Converted; c == nil || c.Complete || len(c.Excluded) != 1 || c.Excluded[0].Currency != "GBP" || c.Balance != nil {
		t.Errorf("phone converted %+v", c)
	}

	// Currencies stay apart: rent with only the pound account.
	mixed := f.orgCreate(t, "buckets", `{"name":"Mixed"}`)
	member(mixed, "commitment", rent)
	member(mixed, "account", gbpAcct)
	m, _ := f.outlook(t, "?bucket="+mixed, admin)
	if got := currenciesOf(m); got != "EUR: due 200.00 EUR overdue 100.00 EUR upcoming 100.00 EUR balance <nil> less <nil>\n"+
		"GBP: due 0.00 GBP overdue 0.00 GBP upcoming 0.00 GBP balance 300.00 GBP less 300.00 GBP\n" {
		t.Errorf("mixed slice:\n%s", got)
	}

	// An empty bucket counts nothing; an archived one still slices.
	empty := f.orgCreate(t, "buckets", `{"name":"Empty"}`)
	e, _ := f.outlook(t, "?bucket="+empty, admin)
	if len(e.Currencies) != 0 || e.Slice == nil || e.Slice.CommitmentCount != 0 || e.Window.From != nil {
		t.Errorf("empty slice %+v from %v", e.Slice, e.Window.From)
	}
	var d struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(f.do("GET", "/api/v1/buckets/"+home, "", admin).Body.Bytes(), &d)
	if rec := f.do("POST", "/api/v1/buckets/"+home+"/archive", "{}", map[string]string{"X-Test-User": f.ids["alice"], "If-Match": fmt.Sprintf("\"%d\"", d.Version)}); rec.Code != 200 {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := f.outlook(t, "?bucket="+home, admin); a.Slice == nil || !strings.Contains(currenciesOf(a), "EUR: due 200.00 EUR") {
		t.Errorf("archived bucket: %+v", a.Slice)
	}

	// Refusals: an unknown bucket, a malformed one, a repeated one, and a
	// caller without organisation:read.
	for _, q := range []string{"?bucket=01JZ0000000000000000000999", "?bucket=nope", "?bucket=" + home + "&bucket=" + bills} {
		if rec := f.do("GET", "/api/v1/outlook"+q, "", admin); rec.Code != 400 || !strings.Contains(rec.Body.String(), "bucket") {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	noOrg := map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "commitments:read,connections:read"}
	if rec := f.do("GET", "/api/v1/outlook?bucket="+home, "", noOrg); rec.Code != 403 {
		t.Errorf("slice without organisation:read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("GET", "/api/v1/outlook", "", noOrg); rec.Code != 200 {
		t.Errorf("whole outlook without organisation:read: %d", rec.Code)
	}
	// A slice without connections:read still shows no balance.
	cOnly := map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "commitments:read,organisation:read"}
	if s, _ := f.outlook(t, "?bucket="+home, cOnly); s.Balances.Shown || len(s.Accounts) != 0 || s.Slice == nil || s.Currencies[0].Balance != nil {
		t.Errorf("slice without balances grant: %+v", s.Balances)
	}
}

// TestCategoryAndLabelFilters filters the lists that show organised objects
// by category and label: commitments, a month's occurrences and its summary
// (totals included), and payment matches by their transaction.
func TestCategoryAndLabelFilters(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	earlier := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	rent := f.createCommitmentAt(t, earlier, `{"name":"Rent","kind":"service","amount":{"amount":"800.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":2},"start_date":"2026-01-01"}`)
	phone := f.createCommitmentAt(t, earlier, `{"name":"Phone","kind":"subscription","amount":{"amount":"25.00","currency":"GBP"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":20},"start_date":"2026-01-01"}`)
	gym := f.createCommitmentAt(t, earlier, `{"name":"Gym","kind":"subscription","amount":{"amount":"30.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":9},"start_date":"2026-01-01"}`)

	home := f.orgCreate(t, "categories", `{"name":"Home"}`)
	housing := f.orgCreate(t, "categories", `{"name":"Housing","parent_id":"`+home+`"}`)
	fun := f.orgCreate(t, "categories", `{"name":"Fun"}`)
	shared := f.orgCreate(t, "labels", `{"name":"Shared"}`)
	f.orgSetCategory(t, "commitment", rent, housing)
	f.orgSetCategory(t, "commitment", gym, fun)
	if rec := f.do("PUT", "/api/v1/organisation/commitment/"+rent+"/labels/"+shared, "", admin); rec.Code != 200 {
		t.Fatalf("label: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("PUT", "/api/v1/organisation/commitment/"+phone+"/labels/"+shared, "", admin); rec.Code != 200 {
		t.Fatalf("label: %d %s", rec.Code, rec.Body.String())
	}

	list := func(path string) []string {
		t.Helper()
		rec := f.do("GET", path, "", admin)
		if rec.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		return idsOf(t, rec.Body.Bytes(), "id")
	}
	same := func(what string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v, want %v", what, got, want)
		}
	}
	// Commitments are in ID (creation) order: rent, phone, gym.
	same("by child category", list("/api/v1/commitments?category_id="+housing), rent)
	same("a parent matches its children", list("/api/v1/commitments?category_id="+home), rent)
	same("any of two categories", list("/api/v1/commitments?category_id="+home+"&category_id="+fun), rent, gym)
	same("by label", list("/api/v1/commitments?label_id="+shared), rent, phone)
	same("category and label both hold", list("/api/v1/commitments?category_id="+home+"&label_id="+shared), rent)
	same("nothing matches", list("/api/v1/commitments?category_id="+fun+"&label_id="+shared))
	same("no filter lists all", list("/api/v1/commitments"), rent, phone, gym)

	// A month's occurrences are those of the matching commitments.
	nov := list("/api/v1/occurrences?month=2026-11&label_id=" + shared)
	if len(nov) != 2 {
		t.Errorf("November by label: %v", nov)
	}
	if got := list("/api/v1/occurrences?month=2026-11&label_id=" + shared + "&commitment_id=" + gym); len(got) != 0 {
		t.Errorf("label and a commitment outside it: %v", got)
	}

	// The summary covers only the matching commitments, totals included, and
	// says so; the unfiltered one says nothing.
	type summary struct {
		Items []struct {
			CommitmentName string `json:"commitment_name"`
		} `json:"items"`
		Totals []struct {
			Currency string       `json:"currency"`
			Due      outlookMoney `json:"due"`
		} `json:"totals"`
		Filter *struct {
			CategoryIDs []string `json:"category_ids"`
			LabelIDs    []string `json:"label_ids"`
			Note        string   `json:"note"`
		} `json:"filter"`
	}
	get := func(q string) summary {
		t.Helper()
		rec := f.do("GET", "/api/v1/occurrences/summary?month=2026-11"+q, "", admin)
		if rec.Code != 200 {
			t.Fatalf("summary%s: %d %s", q, rec.Code, rec.Body.String())
		}
		var s summary
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	all := get("")
	if all.Filter != nil || len(all.Items) != 3 || len(all.Totals) != 2 {
		t.Errorf("whole month: filter %+v items %d totals %+v", all.Filter, len(all.Items), all.Totals)
	}
	if all.Totals[0].Due.String() != "830.00 EUR" {
		t.Errorf("whole month EUR due %s", all.Totals[0].Due.String())
	}
	part := get("&category_id=" + home)
	if part.Filter == nil || len(part.Filter.CategoryIDs) != 1 || !strings.Contains(part.Filter.Note, "Filtered") || len(part.Items) != 1 || part.Items[0].CommitmentName != "Rent" ||
		len(part.Totals) != 1 || part.Totals[0].Due.String() != "800.00 EUR" {
		t.Errorf("filtered month: %+v totals %+v", part.Filter, part.Totals)
	}
	if none := get("&category_id=" + fun + "&label_id=" + shared); len(none.Items) != 0 || len(none.Totals) != 0 || none.Filter == nil {
		t.Errorf("no match: %+v", none)
	}

	// Refusals: an unknown or malformed definition, and no organisation:read.
	for _, q := range []string{"/api/v1/commitments?category_id=01JZ0000000000000000000999", "/api/v1/commitments?label_id=01JZ0000000000000000000999", "/api/v1/commitments?category_id=nope",
		"/api/v1/occurrences/summary?month=2026-11&label_id=nope", "/api/v1/occurrences/summary?month=2026-11&bucket=x"} {
		if rec := f.do("GET", q, "", admin); rec.Code != 400 {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	noOrg := map[string]string{"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "commitments:read"}
	for _, q := range []string{"/api/v1/commitments?label_id=" + shared, "/api/v1/occurrences?month=2026-11&category_id=" + home, "/api/v1/occurrences/summary?month=2026-11&category_id=" + home, "/api/v1/payment-matches?label_id=" + shared} {
		if rec := f.do("GET", q, "", noOrg); rec.Code != 403 {
			t.Errorf("%s without organisation:read: %d", q, rec.Code)
		}
	}
	if rec := f.do("GET", "/api/v1/commitments", "", noOrg); rec.Code != 200 {
		t.Errorf("unfiltered list without organisation:read: %d", rec.Code)
	}

	// Payment matches filter by their bank transaction's category and label.
	bank := importFakeBank(t, f)
	rentTx, holdTx := bank.tx(t, "RENT 2026-10"), bank.tx(t, "HOTEL HOLD")
	oct := f.occurrences(t, "month=2026-10&commitment_id="+rent, "alice").Items[0].ID
	rec := f.do("POST", "/api/v1/payment-matches", fmt.Sprintf(`{"transaction_id":%q,"occurrence_id":%q,"amount":{"amount":"800.00","currency":"EUR"}}`, rentTx.ID, oct), admin)
	if rec.Code != 201 {
		t.Fatalf("match: %d %s", rec.Code, rec.Body.String())
	}
	matchID := decodeMatch(t, rec.Body.Bytes()).ID
	_ = holdTx
	same("matches before the transaction is organised", list("/api/v1/payment-matches?category_id="+fun))
	f.orgSetCategory(t, "transaction", string(rentTx.ID), fun)
	same("matches by transaction category", list("/api/v1/payment-matches?category_id="+fun), matchID)
	same("matches by another category", list("/api/v1/payment-matches?category_id="+home))
	same("matches, category and transaction id", list("/api/v1/payment-matches?category_id="+fun+"&transaction_id="+string(holdTx.ID)))
}
