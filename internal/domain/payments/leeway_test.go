package payments_test

import (
	"context"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// An overdue occurrence reports what the user recorded about leeway; unknown
// and not recorded are distinct from no grace and never produce a date.
func TestLeewayStateIsDistinct(t *testing.T) {
	until := d("2026-03-25")
	for _, c := range []struct {
		name   string
		grace  payments.GraceFunc
		leeway string
		date   bool
	}{
		{"no source", nil, "", false},
		{"not recorded", func(context.Context, occurrences.Occurrence) payments.Grace { return payments.Grace{} }, payments.LeewayNotRecorded, false},
		{"unknown", func(context.Context, occurrences.Occurrence) payments.Grace {
			return payments.Grace{Recorded: true, Kind: commitments.LeewayUnknown}
		}, "unknown", false},
		{"none", func(context.Context, occurrences.Occurrence) payments.Grace {
			return payments.Grace{Recorded: true, Kind: commitments.LeewayNone}
		}, "none", false},
		{"period", func(context.Context, occurrences.Occurrence) payments.Grace {
			return payments.Grace{Recorded: true, Kind: commitments.LeewayPeriod, Until: &until}
		}, "period", true},
	} {
		st := payments.Derive(context.Background(), occ, payments.Facts{}, d("2026-03-20"), c.grace)
		if st.Status != payments.StatusOverdue && c.grace != nil || st.Leeway != c.leeway || (st.GraceUntil != nil) != c.date || st.WithinGrace != c.date {
			t.Errorf("%s: status %s leeway %q until %v within %v", c.name, st.Status, st.Leeway, st.GraceUntil, st.WithinGrace)
		}
	}
	// Not overdue: nothing is said about leeway.
	st := payments.Derive(context.Background(), occ, payments.Facts{}, d("2026-03-10"), func(context.Context, occurrences.Occurrence) payments.Grace { return payments.Grace{} })
	if st.Leeway != "" {
		t.Errorf("leeway %q before the due date", st.Leeway)
	}
}
