package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/mcpserver"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

// TestMCPRateTools walks the exchange-rate read tools over real MCP and the
// real API: they are reads only (no refresh tool), need rates:read, show the
// exact value, provenance and staleness, and never call the rate provider.
func TestMCPRateTools(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	for _, name := range []string{"list_exchange_rates", "get_exchange_rate_status"} {
		tl, ok := mcpserver.ToolByName(name)
		if !ok || tl.Kind != mcpserver.KindRead || tl.Grant != authz.G(authz.ResourceRates, authz.ActionRead) {
			t.Errorf("%s: %+v", name, tl)
		}
	}
	for _, tl := range mcpserver.Tools() {
		if strings.Contains(tl.Name, "rate") && tl.Kind != mcpserver.KindRead {
			t.Errorf("%s must not be a write tool: refreshing is done with the CLI or the web UI", tl.Name)
		}
	}
	// Rates cached by one refresh the way the daily job makes it; the
	// administrator runs it, since a refresh is not an MCP tool.
	f.job.Target, f.job.Currencies = eur(), func(context.Context) ([]moneypkg.Currency, error) {
		return []moneypkg.Currency{moneypkg.MustParseCurrency("USD")}, nil
	}
	if st, err := f.job.Request(context.Background()); err != nil || st.Outcome != "succeeded" {
		t.Fatalf("refresh: %+v %v", st, err)
	}
	calls := f.ratesDouble.count()

	secret, _ := f.token(t, f.admin, "rates:read")
	reader := f.connect(t, secret)
	page := decodeJSONText[ledgerclient.Page[ledgerclient.CachedRate]](t, call(t, reader, "list_exchange_rates", map[string]any{"base": "USD", "date[gte]": "2026-11-01"}))
	if len(page.Items) != 1 || page.Items[0].Value != "0.90992" || page.Items[0].Source != "ECB" || page.Items[0].Provider != "frankfurter" || page.Items[0].Date != "2026-11-20" {
		t.Fatalf("list: %+v", page)
	}
	// The rates' clock is 2026-11-21, the day of the retrieval: not stale.
	if page.Items[0].Stale {
		t.Errorf("a freshly retrieved rate is stale: %+v", page.Items[0])
	}
	st := decodeJSONText[ledgerclient.RatesStatus](t, call(t, reader, "get_exchange_rate_status", nil))
	if !st.Enabled || st.StaleAfterDays != 5 || st.LastRun == nil || st.LastRun.Outcome != "succeeded" {
		t.Errorf("status: %+v", st)
	}
	if r := call(t, reader, "list_exchange_rates", map[string]any{"base": "usd"}); !r.IsError || !strings.Contains(resultText(r), "invalid arguments") {
		t.Errorf("a lower-case currency must be refused by the schema: %s", resultText(r))
	}
	other, _ := f.token(t, f.admin, "commitments:read")
	if r := call(t, f.connect(t, other), "list_exchange_rates", nil); !r.IsError || !strings.Contains(resultText(r), "rates:read") {
		t.Errorf("a token without rates:read: %s", resultText(r))
	}
	if f.ratesDouble.count() != calls {
		t.Error("a read tool called the rate provider")
	}
}
