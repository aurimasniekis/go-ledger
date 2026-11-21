package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type outlookMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m *outlookMoney) String() string {
	if m == nil {
		return "<nil>"
	}
	return m.Amount + " " + m.Currency
}

type outlookResponse struct {
	AsOf   string `json:"as_of"`
	Notice string `json:"notice"`
	Window struct {
		From      *string `json:"from"`
		Through   string  `json:"through"`
		Truncated bool    `json:"truncated"`
	} `json:"window"`
	Currencies []struct {
		Currency            string        `json:"currency"`
		StillDue            outlookMoney  `json:"still_due"`
		Overdue             outlookMoney  `json:"overdue"`
		Upcoming            outlookMoney  `json:"upcoming"`
		Balance             *outlookMoney `json:"balance"`
		BalanceLessStillDue *outlookMoney `json:"balance_less_still_due"`
		AccountCount        int           `json:"account_count"`
		StaleAccountCount   int           `json:"stale_account_count"`
	} `json:"currencies"`
	Balances struct {
		Shown bool   `json:"shown"`
		Note  string `json:"note"`
	} `json:"balances"`
	Accounts []struct {
		Name             string  `json:"name"`
		Counted          bool    `json:"counted"`
		NotCountedReason *string `json:"not_counted_reason"`
		Freshness        struct {
			State string `json:"state"`
		} `json:"freshness"`
	} `json:"accounts"`
	Converted *struct {
		Currency string        `json:"currency"`
		Complete bool          `json:"complete"`
		StillDue outlookMoney  `json:"still_due"`
		Balance  *outlookMoney `json:"balance"`
		Included []struct {
			Currency string           `json:"currency"`
			Rate     *json.RawMessage `json:"rate"`
		} `json:"included"`
		Excluded []struct {
			Currency string `json:"currency"`
			Status   string `json:"status"`
			Reason   string `json:"reason"`
		} `json:"excluded"`
	} `json:"converted"`
	PlannedOutflows struct {
		Included bool `json:"included"`
	} `json:"planned_outflows"`
}

func (f *apiFixture) outlook(t *testing.T, query string, hdr map[string]string) (outlookResponse, string) {
	t.Helper()
	rec := f.do("GET", "/api/v1/outlook"+query, "", hdr)
	if rec.Code != 200 {
		t.Fatalf("GET outlook%s: %d %s", query, rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	var o outlookResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	return o, rec.Body.String()
}

// currenciesOf renders the per-currency lines for comparison.
func currenciesOf(o outlookResponse) string {
	var b strings.Builder
	for _, l := range o.Currencies {
		b.WriteString(l.Currency + ": due " + l.StillDue.String() + " overdue " + l.Overdue.String() + " upcoming " + l.Upcoming.String() +
			" balance " + l.Balance.String() + " less " + l.BalanceLessStillDue.String() + "\n")
	}
	return b.String()
}

// TestOutlookAPI walks the outlook through the API with bank facts imported
// by the real engine: per-currency lines are never mixed, balances need
// connections:read, the converted aggregate excludes and flags currencies
// without a rate, a recorded payment changes what is still due, and the
// figures are the same after a re-import and on a later read.
func TestOutlookAPI(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	reader := map[string]string{"X-Test-User": f.ids["bob"]}

	// Commitments are recorded on 1 October, so the 5 October occurrence is
	// tracked (not "not recorded") and overdue by November.
	f.now = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	f.createCommitment(t, `{"name":"Rent","kind":"subscription","amount":{"amount":"100.00","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-10-01"}`)
	f.createCommitment(t, `{"name":"Phone","kind":"subscription","amount":{"amount":"25.00","currency":"GBP"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":20},"start_date":"2026-11-01"}`)
	b := importFakeBank(t, f) // refreshed 2026-11-01 09:00 UTC
	f.now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	o, _ := f.outlook(t, "?currency=EUR", admin)
	if !strings.Contains(o.Notice, "Outlook against recorded commitments") || strings.Contains(strings.ToLower(o.Notice), "safe to spend") && !strings.Contains(o.Notice, "does not tell you how much money is safe to spend") {
		t.Errorf("notice %q", o.Notice)
	}
	if o.AsOf != "2026-11-01" || o.Window.From == nil || *o.Window.From != "2026-10" || o.Window.Through != "2026-11" || o.PlannedOutflows.Included {
		t.Errorf("as of %s window %v..%s planned %v", o.AsOf, o.Window.From, o.Window.Through, o.PlannedOutflows.Included)
	}
	want := "EUR: due 200.00 EUR overdue 100.00 EUR upcoming 100.00 EUR balance 1250.40 EUR less 1050.40 EUR\n" +
		"GBP: due 25.00 GBP overdue 0.00 GBP upcoming 25.00 GBP balance 300.00 GBP less 275.00 GBP\n"
	if got := currenciesOf(o); got != want {
		t.Errorf("lines:\n%s\nwant:\n%s", got, want)
	}
	if !o.Balances.Shown || len(o.Accounts) != 3 {
		t.Fatalf("balances %+v accounts %+v", o.Balances, o.Accounts)
	}
	for _, a := range o.Accounts {
		if a.Freshness.State != "fresh" {
			t.Errorf("%s freshness %s", a.Name, a.Freshness.State)
		}
		if a.Name == "Multi-currency wallet" && (a.Counted || a.NotCountedReason == nil || *a.NotCountedReason != "no_balance") {
			t.Errorf("wallet without balances: %+v", a)
		}
	}
	c := o.Converted
	if c == nil || c.Currency != "EUR" || c.Complete || c.StillDue.String() != "200.00 EUR" || c.Balance.String() != "1250.40 EUR" ||
		len(c.Included) != 1 || c.Included[0].Currency != "EUR" || c.Included[0].Rate != nil ||
		len(c.Excluded) != 1 || c.Excluded[0].Currency != "GBP" || c.Excluded[0].Status != "missing" || c.Excluded[0].Reason != "no_provider" {
		t.Errorf("converted %+v", c)
	}

	// Without connections:read only commitments are compared.
	for name, hdr := range map[string]map[string]string{
		"read-only user":                 reader,
		"token without connections:read": {"X-Test-User": f.ids["alice"], "X-Test-Token-Grants": "commitments:read"},
	} {
		r, _ := f.outlook(t, "", hdr)
		if r.Balances.Shown || len(r.Accounts) != 0 || !strings.Contains(r.Balances.Note, "connections:read") || r.Converted != nil {
			t.Errorf("%s: balances %+v accounts %d converted %v", name, r.Balances, len(r.Accounts), r.Converted)
		}
		for _, l := range r.Currencies {
			if l.Balance != nil || l.BalanceLessStillDue != nil || l.AccountCount != 0 {
				t.Errorf("%s sees a %s balance", name, l.Currency)
			}
		}
	}

	// A re-import appends the same balance snapshots again; a later read
	// gives the same figures.
	before := currenciesOf(o)
	b.now = b.now.Add(time.Hour)
	b.refresh(t)
	f.now = f.now.Add(time.Hour)
	again, _ := f.outlook(t, "", admin)
	if got := currenciesOf(again); got != before {
		t.Errorf("after re-import:\n%s\nbefore:\n%s", got, before)
	}

	// Paying October's rent (C3, derived status) takes it off what is due.
	occ := f.occurrences(t, "month=2026-10", "alice")
	if len(occ.Items) != 1 {
		t.Fatalf("October %+v", occ)
	}
	rec := f.do("POST", "/api/v1/occurrences/"+occ.Items[0].ID+"/manual-payments", `{"amount":{"amount":"100.00","currency":"EUR"},"paid_on":"2026-10-30"}`, admin)
	if rec.Code != 201 {
		t.Fatalf("record payment: %d %s", rec.Code, rec.Body.String())
	}
	paid, _ := f.outlook(t, "", admin)
	if l := paid.Currencies[0]; l.Currency != "EUR" || l.StillDue.String() != "100.00 EUR" || l.Overdue.String() != "0.00 EUR" || l.BalanceLessStillDue.String() != "1150.40 EUR" {
		t.Errorf("after paying October: %s", currenciesOf(paid))
	}

	// Two days without a refresh: balances stay, marked stale.
	f.now = f.now.Add(48 * time.Hour)
	stale, _ := f.outlook(t, "?through=2026-12", admin)
	if stale.Window.Through != "2026-12" || stale.Currencies[0].StillDue.String() != "200.00 EUR" || stale.Currencies[0].StaleAccountCount != 1 || stale.Currencies[1].StaleAccountCount != 1 {
		t.Errorf("stale read: %s %+v", currenciesOf(stale), stale.Currencies)
	}
	for _, a := range stale.Accounts {
		if a.Freshness.State != "stale" {
			t.Errorf("%s freshness %s after two days", a.Name, a.Freshness.State)
		}
	}
}

func TestOutlookAPIRefusesBadParameters(t *testing.T) {
	f := newAPIFixture(t)
	admin := map[string]string{"X-Test-User": f.ids["alice"]}
	for _, q := range []string{"?through=2026-13", "?through=2026-11&through=2026-12", "?currency=eur", "?currency=EUR&currency=USD", "?month=2026-11", "?through=2099-01"} {
		rec := f.do("GET", "/api/v1/outlook"+q, "", admin)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "validation") {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	if rec := f.do("GET", "/api/v1/outlook", "", nil); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
}
