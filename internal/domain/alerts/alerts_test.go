package alerts_test

import (
	"slices"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/alerts"
	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func TestConditions(t *testing.T) {
	today := calendar.MustDate(2026, 11, 1)
	if _, ok := alerts.ConsentExpiring("c", calendar.MustDate(2026, 12, 25), today, 14); ok {
		t.Error("a consent far from expiry raised")
	}
	if c, ok := alerts.ConsentExpiring("c", calendar.MustDate(2026, 11, 8), today, 14); !ok || c.Kind != alerts.KindConsentExpiring || c.Facts["expires_on"] != "2026-11-08" {
		t.Errorf("%+v %v", c, ok)
	}
	if c, ok := alerts.ConsentExpiring("c", calendar.MustDate(2026, 10, 30), today, 14); !ok || c.Message == "" {
		t.Errorf("an expired consent: %+v %v", c, ok)
	}
	// A new expiry date (after reconnecting) is a new situation.
	a, _ := alerts.ConsentExpiring("c", calendar.MustDate(2026, 11, 8), today, 14)
	b, _ := alerts.ConsentExpiring("c", calendar.MustDate(2026, 11, 9), today, 14)
	if a.Key == b.Key {
		t.Error("the key ignores the expiry date")
	}

	eur := money.MustParse("10.00", "EUR")
	if _, ok := alerts.AmountChanged("m", "o", eur, eur); ok {
		t.Error("an unchanged amount raised")
	}
	c, ok := alerts.AmountChanged("m", "o", eur, money.MustParse("10.00", "USD"))
	if !ok || c.Facts["expected_currency"] != "EUR" || c.Facts["paid_currency"] != "USD" || c.Facts["currency_differs"] != "true" {
		t.Errorf("a currency change must be shown as itself, never converted: %+v %v", c, ok)
	}

	due := calendar.MustDate(2026, 10, 20)
	if _, ok := alerts.UnmatchedOverdue("o", due, today, money.MustParse("0.00", "EUR")); ok {
		t.Error("nothing outstanding raised")
	}
	if _, ok := alerts.UnmatchedOverdue("o", today, today, eur); ok {
		t.Error("due today is not overdue")
	}
	if c, ok := alerts.UnmatchedOverdue("o", due, today, eur); !ok || c.Facts["outstanding"] != "10.00" || c.Facts["currency"] != "EUR" {
		t.Errorf("%+v %v", c, ok)
	}
}

// Amounts compare numerically and the key ignores scale: "15" equals "15.00",
// but a different numeric amount or a different currency is still a change.
func TestAmountChangedIsNumericAndScaleFree(t *testing.T) {
	m := money.MustParse
	if _, ok := alerts.AmountChanged("c", "o", m("15", "EUR"), m("15.00", "EUR")); ok {
		t.Error("15 EUR and 15.00 EUR are the same amount")
	}
	a, okA := alerts.AmountChanged("c", "o", m("15", "EUR"), m("12.5", "EUR"))
	b, okB := alerts.AmountChanged("c", "o", m("15.00", "EUR"), m("12.50", "EUR"))
	if !okA || !okB || a.Key != b.Key {
		t.Errorf("the same situation at another scale has another key: %q %q", a.Key, b.Key)
	}
	if c, ok := alerts.AmountChanged("c", "o", m("100", "EUR"), m("99.99", "EUR")); !ok || c.Key == a.Key {
		t.Errorf("a different amount has the same key: %+v", c)
	}
	if c, ok := alerts.AmountChanged("c", "o", m("15.000", "EUR"), m("12.500", "EUR")); !ok || c.Key != a.Key {
		t.Errorf("trailing zeros changed the key: %q", c.Key)
	}
	if c, ok := alerts.AmountChanged("c", "o", m("15", "EUR"), m("12.50", "USD")); !ok || c.Facts["currency_differs"] != "true" {
		t.Errorf("a currency difference was lost: %+v", c)
	}
}

// The audit package keeps its own copy of the alert kinds.
func TestAuditKindsMatch(t *testing.T) {
	want := []string{string(alerts.KindConsentExpiring), string(alerts.KindAmountChanged), string(alerts.KindUnmatchedOverdue)}
	for _, a := range []audit.Action{audit.AlertRaised, audit.AlertClosed} {
		for _, f := range audit.Definitions[a] {
			if f.Name == "kind" && !slices.Equal(f.Enum, want) {
				t.Errorf("%s kind %v, want %v", a, f.Enum, want)
			}
		}
	}
}
