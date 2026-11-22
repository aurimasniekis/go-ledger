package commitments_test

import (
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func leewayBase(l *commitments.Leeway) commitments.Fields {
	return commitments.Fields{Name: "VPS", Kind: commitments.KindServer, Amount: money.MustParse("12.50", "EUR"),
		Schedule: commitments.Monthly(1, 5), StartDate: commitments.MinStartDate, Leeway: l}
}

func intp(n int) *int       { return &n }
func strp(s string) *string { return &s }

func TestLeewayValidation(t *testing.T) {
	until := calendar.MustDate(2026, 12, 1)
	long := strings.Repeat("x", commitments.MaxLeewayText+1)
	for name, c := range map[string]struct {
		l     *commitments.Leeway
		field string
	}{
		"not recorded":         {nil, ""},
		"unknown":              {&commitments.Leeway{Kind: commitments.LeewayUnknown}, ""},
		"none":                 {&commitments.Leeway{Kind: commitments.LeewayNone, Source: strp("contract 4.2")}, ""},
		"period":               {&commitments.Leeway{Kind: commitments.LeewayPeriod, Days: intp(5), Conditions: strp("if reported")}, ""},
		"deadline":             {&commitments.Leeway{Kind: commitments.LeewayDeadline, Until: &until}, ""},
		"bad kind":             {&commitments.Leeway{Kind: "soon"}, "leeway.kind"},
		"empty kind":           {&commitments.Leeway{}, "leeway.kind"},
		"period without days":  {&commitments.Leeway{Kind: commitments.LeewayPeriod}, "leeway.days"},
		"period zero days":     {&commitments.Leeway{Kind: commitments.LeewayPeriod, Days: intp(0)}, "leeway.days"},
		"period too long":      {&commitments.Leeway{Kind: commitments.LeewayPeriod, Days: intp(366)}, "leeway.days"},
		"unknown with days":    {&commitments.Leeway{Kind: commitments.LeewayUnknown, Days: intp(3)}, "leeway.days"},
		"none with a date":     {&commitments.Leeway{Kind: commitments.LeewayNone, Until: &until}, "leeway.until"},
		"deadline without day": {&commitments.Leeway{Kind: commitments.LeewayDeadline}, "leeway.until"},
		"blank source":         {&commitments.Leeway{Kind: commitments.LeewayUnknown, Source: strp("  ")}, "leeway.source"},
		"long conditions":      {&commitments.Leeway{Kind: commitments.LeewayUnknown, Conditions: &long}, "leeway.conditions"},
	} {
		errs := leewayBase(c.l).Validate()
		switch {
		case c.field == "" && len(errs) != 0:
			t.Errorf("%s: %v", name, errs)
		case c.field != "" && (len(errs) != 1 || errs[0].Field != c.field):
			t.Errorf("%s: %v, want %s", name, errs, c.field)
		}
	}
}

// Unknown, none and nothing recorded are three different states, and none of
// them yields a grace date.
func TestLeewayGraceUntil(t *testing.T) {
	due := calendar.MustDate(2026, 1, 28)
	until := calendar.MustDate(2026, 2, 10)
	for name, c := range map[string]struct {
		l    *commitments.Leeway
		want string
	}{
		"nil":              {nil, ""},
		"unknown":          {&commitments.Leeway{Kind: commitments.LeewayUnknown}, ""},
		"none":             {&commitments.Leeway{Kind: commitments.LeewayNone}, ""},
		"period crosses":   {&commitments.Leeway{Kind: commitments.LeewayPeriod, Days: intp(5)}, "2026-02-02"},
		"deadline covers":  {&commitments.Leeway{Kind: commitments.LeewayDeadline, Until: &until}, "2026-02-10"},
		"deadline already": {&commitments.Leeway{Kind: commitments.LeewayDeadline, Until: &due}, "2026-01-28"},
	} {
		got, ok := c.l.GraceUntil(due)
		if (c.want != "") != ok || (ok && got.String() != c.want) {
			t.Errorf("%s: %s %v, want %q", name, got, ok, c.want)
		}
	}
	// A deadline does not cover an occurrence due after it.
	if _, ok := (&commitments.Leeway{Kind: commitments.LeewayDeadline, Until: &due}).GraceUntil(until); ok {
		t.Error("a deadline covers a later occurrence")
	}
}

func TestLeewayChangedFields(t *testing.T) {
	a := leewayBase(&commitments.Leeway{Kind: commitments.LeewayUnknown})
	if got := commitments.ChangedFields(a, a); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}
	// Not recorded and unknown are different values.
	if got := commitments.ChangedFields(leewayBase(nil), a); len(got) != 1 || got[0] != "leeway" {
		t.Errorf("nil to unknown: %v", got)
	}
	if got := commitments.ChangedFields(a, leewayBase(&commitments.Leeway{Kind: commitments.LeewayNone})); len(got) != 1 || got[0] != "leeway" {
		t.Errorf("unknown to none: %v", got)
	}
}
