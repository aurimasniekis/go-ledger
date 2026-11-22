package payments_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

func tol(s string) *money.Money { m := money.MustParse(s, "EUR"); return &m }

// TestNotRecordedBoundary pins D38 1a at the creation date: an unpaid
// occurrence due before the commitment's creation date is not_recorded, one
// due on or after it keeps its date-based status.
func TestNotRecordedBoundary(t *testing.T) {
	cases := []struct {
		name    string
		created string
		today   string
		status  payments.Status
	}{
		{"due the day before creation", "2026-03-16", "2026-03-20", payments.StatusNotRecorded},
		{"due on the creation date", "2026-03-15", "2026-03-20", payments.StatusOverdue},
		{"due the day after creation", "2026-03-14", "2026-03-20", payments.StatusOverdue},
		{"unknown creation date", "", "2026-03-20", payments.StatusOverdue},
		{"upcoming is never not_recorded", "2026-03-01", "2026-03-10", payments.StatusUpcoming},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := payments.Facts{}
			if c.created != "" {
				f.CommitmentCreatedOn = d(c.created)
			}
			st := payments.Derive(context.Background(), occ, f, d(c.today), nil)
			if st.Status != c.status || st.Derived != c.status || st.Timing != c.status {
				t.Fatalf("%+v", st)
			}
		})
	}
}

// TestNotRecordedIsNotOverdueAndCanBePaid: the grace hook is not consulted,
// a payment or a paid override still settles it, and an unpaid override keeps
// it not_recorded.
func TestNotRecordedIsNotOverdueAndCanBePaid(t *testing.T) {
	created := d("2026-04-01")
	until := d("2026-04-30")
	grace := func(ctx context.Context, _ occurrences.Occurrence) payments.Grace {
		return payments.Grace{Recorded: true, Kind: "deadline", Until: &until}
	}
	st := payments.Derive(context.Background(), occ, payments.Facts{CommitmentCreatedOn: created}, d("2026-05-01"), grace)
	if st.GraceUntil != nil || st.Status != payments.StatusNotRecorded || st.Outstanding.String() != "50.00 EUR" {
		t.Fatalf("unpaid: %+v", st)
	}
	st = payments.Derive(context.Background(), occ, payments.Facts{CommitmentCreatedOn: created, Evidence: []payments.Evidence{ev("50.00", "EUR")}}, d("2026-05-01"), nil)
	if st.Status != payments.StatusPaid || st.Timing != "" {
		t.Fatalf("paid: %+v", st)
	}
	st = payments.Derive(context.Background(), occ, payments.Facts{CommitmentCreatedOn: created, Evidence: []payments.Evidence{ev("20.00", "EUR")}}, d("2026-05-01"), nil)
	if st.Status != payments.StatusPartiallyPaid || st.Timing != payments.StatusNotRecorded {
		t.Fatalf("partial: %+v", st)
	}
	st = payments.Derive(context.Background(), occ, payments.Facts{CommitmentCreatedOn: created, Override: ov(payments.OverrideUnpaid), Evidence: []payments.Evidence{ev("20.00", "EUR")}}, d("2026-05-01"), nil)
	if st.Status != payments.StatusNotRecorded {
		t.Fatalf("unpaid override: %+v", st)
	}
}

// TestToleranceBoundaries pins D39 5a: a confirmed bank payment short of the
// amount by no more than the tolerance is paid, with the shortfall as a
// detail and nothing outstanding; a larger shortfall and a payment above the
// amount are not changed by the tolerance.
func TestToleranceBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		evidence  []payments.Evidence
		tolerance *money.Money
		status    payments.Status
		shortfall string
		out       string
		overpaid  string
	}{
		{"exact", []payments.Evidence{bank("50.00", "EUR")}, tol("0.50"), payments.StatusPaid, "0.00 EUR", "0.00 EUR", "0.00 EUR"},
		{"short within", []payments.Evidence{bank("49.70", "EUR")}, tol("0.50"), payments.StatusPaid, "0.30 EUR", "0.00 EUR", "0.00 EUR"},
		{"short by exactly the tolerance", []payments.Evidence{bank("49.50", "EUR")}, tol("0.50"), payments.StatusPaid, "0.50 EUR", "0.00 EUR", "0.00 EUR"},
		{"short beyond", []payments.Evidence{bank("49.49", "EUR")}, tol("0.50"), payments.StatusPartiallyPaid, "0.00 EUR", "0.51 EUR", "0.00 EUR"},
		{"above", []payments.Evidence{bank("50.01", "EUR")}, tol("0.50"), payments.StatusPaid, "0.00 EUR", "0.00 EUR", "0.01 EUR"},
		{"no tolerance", []payments.Evidence{bank("49.70", "EUR")}, nil, payments.StatusPartiallyPaid, "0.00 EUR", "0.30 EUR", "0.00 EUR"},
		{"zero tolerance", []payments.Evidence{bank("49.70", "EUR")}, tol("0.00"), payments.StatusPartiallyPaid, "0.00 EUR", "0.30 EUR", "0.00 EUR"},
		{"manual partial payment keeps today's behaviour", []payments.Evidence{ev("49.70", "EUR")}, tol("0.50"), payments.StatusPartiallyPaid, "0.00 EUR", "0.30 EUR", "0.00 EUR"},
		{"manual short plus bank in another currency stays partial", []payments.Evidence{ev("49.70", "EUR"), bank("10.00", "USD")}, tol("0.50"), payments.StatusPartiallyPaid, "0.00 EUR", "0.30 EUR", "0.00 EUR"},
		{"manual plus bank short within tolerance stays partial", []payments.Evidence{ev("39.70", "EUR"), bank("10.00", "EUR")}, tol("0.50"), payments.StatusPartiallyPaid, "0.00 EUR", "0.30 EUR", "0.00 EUR"},
		{"bank only, two matches short within", []payments.Evidence{bank("30.00", "EUR"), bank("19.80", "EUR")}, tol("0.50"), payments.StatusPaid, "0.20 EUR", "0.00 EUR", "0.00 EUR"},
		{"bank in another currency alone is not counted", []payments.Evidence{bank("10.00", "USD")}, tol("0.50"), payments.StatusOverdue, "0.00 EUR", "50.00 EUR", "0.00 EUR"},
		{"no evidence", nil, tol("0.50"), payments.StatusOverdue, "0.00 EUR", "50.00 EUR", "0.00 EUR"},
		{"tolerance in another currency is ignored", []payments.Evidence{bank("49.70", "EUR")}, func() *money.Money { m := money.MustParse("0.50", "USD"); return &m }(), payments.StatusPartiallyPaid, "0.00 EUR", "0.30 EUR", "0.00 EUR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := slices.Clone(c.evidence)
			for i := range ev {
				if ev[i].Kind == payments.EvidenceBankTransaction {
					ev[i].ConfirmedWithin = c.tolerance // the tolerance recorded at confirmation
				}
			}
			st := payments.Derive(context.Background(), occ, payments.Facts{Evidence: ev}, d("2026-03-20"), nil)
			if st.Status != c.status || st.Shortfall.String() != c.shortfall || st.Outstanding.String() != c.out || st.Overpaid.String() != c.overpaid {
				t.Fatalf("%+v", st)
			}
		})
	}
}

// TestToleranceDoesNotOverridePaidStatusRules: an unpaid override still wins
// over a tolerance-confirmed payment, and the shortfall stays visible.
func TestToleranceUnpaidOverrideWins(t *testing.T) {
	b := bank("49.70", "EUR")
	b.ConfirmedWithin = tol("0.50")
	st := payments.Derive(context.Background(), occ, payments.Facts{Evidence: []payments.Evidence{b}, Override: ov(payments.OverrideUnpaid)}, d("2026-03-20"), nil)
	if st.Status != payments.StatusOverdue || st.Derived != payments.StatusPaid || st.Outstanding.String() != "50.00 EUR" || st.Shortfall.String() != "0.30 EUR" {
		t.Fatalf("%+v", st)
	}
}
