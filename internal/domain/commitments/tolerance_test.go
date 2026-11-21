package commitments_test

import (
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func TestMatchToleranceValidation(t *testing.T) {
	base := func(tol *money.Money) commitments.Fields {
		return commitments.Fields{Name: "VPS", Kind: commitments.KindServer, Amount: money.MustParse("12.50", "EUR"),
			Schedule: commitments.Monthly(1, 5), StartDate: commitments.MinStartDate, MatchTolerance: tol}
	}
	tol := func(s, c string) *money.Money { m := money.MustParse(s, c); return &m }
	for name, c := range map[string]struct {
		tol   *money.Money
		field string
	}{
		"none":           {nil, ""},
		"zero":           {tol("0", "EUR"), ""},
		"positive":       {tol("0.50", "EUR"), ""},
		"other currency": {tol("0.50", "USD"), "match_tolerance.currency"},
		"negative":       {tol("-0.01", "EUR"), "match_tolerance.amount"},
		"no currency":    {&money.Money{}, "match_tolerance.currency"},
	} {
		errs := base(c.tol).Validate()
		switch {
		case c.field == "" && len(errs) != 0:
			t.Errorf("%s: %v", name, errs)
		case c.field != "" && (len(errs) != 1 || errs[0].Field != c.field):
			t.Errorf("%s: %v, want %s", name, errs, c.field)
		}
	}
	a, b := base(tol("0.5", "EUR")), base(tol("0.50", "EUR"))
	if got := commitments.ChangedFields(a, b); len(got) != 1 || got[0] != "match_tolerance" {
		t.Errorf("scale change: %v", got)
	}
	if got := commitments.ChangedFields(base(nil), base(nil)); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}
	if got := commitments.ChangedFields(base(nil), b); len(got) != 1 {
		t.Errorf("set: %v", got)
	}
}
