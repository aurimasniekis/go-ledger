package deferrals_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/deferrals"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// TestFeeStaysPlannedAfterPrincipalIsPaidUntilMarkedPaidOrRemoved is the DF1c
// acceptance path (D-DF 2) against the real stores: the fee counts in the
// planned month, paying the principal does not settle it, marking it paid
// ends it (audited, with who and when), clearing the marker restores it, and
// the fee is never evidence for the occurrence.
func TestFeeStaysPlannedAfterPrincipalIsPaidUntilMarkedPaidOrRemoved(t *testing.T) {
	f := newFixture(t)
	rent := f.commitment("800.00", 2)
	oct := f.occurrence(rent.ID, "2026-10")
	p := f.plan(oct, "2026-11-10", deferrals.TypeCreditorAgreed)
	p, fee, err := f.ds.AddAdjustment(f.ctx, alice, p.ID, p.Version, deferrals.NewAdjustment{Kind: deferrals.AdjustmentFee, Amount: money.MustParse("12.50", "EUR")})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.totals("2026-11"); n.Planned.String() != "1612.50 EUR" || n.Adjustments.String() != "12.50 EUR" {
		t.Fatalf("November before payment (800 own + 800 carried in + the fee): planned %s adjustments %s", n.Planned, n.Adjustments)
	}

	// The principal is paid through a bank match: the fee stays planned.
	if _, err := f.ms.Create(f.ctx, alice, matches.New{TransactionID: f.tx("RENT 2026-10").ID, OccurrenceID: oct.ID, Amount: money.MustParse("800.00", "EUR"),
		Origin: matches.OriginUser, State: matches.StateConfirmed}); err != nil {
		t.Fatal(err)
	}
	if st := f.state(oct.ID); st.Status != payments.StatusPaid || st.Outstanding.Sign() != 0 {
		t.Fatalf("after match: %+v", st)
	}
	if n := f.totals("2026-11"); n.Planned.String() != "812.50 EUR" || n.Adjustments.String() != "12.50 EUR" || n.CarriedIn.String() != "0.00 EUR" {
		t.Errorf("November after the principal is paid: planned %s adjustments %s carried in %s, want the fee to stay planned", n.Planned, n.Adjustments, n.CarriedIn)
	}
	// Undoing the plan would drop the fee from the totals: refused until it is settled.
	if _, err := f.ds.Undo(f.ctx, alice, p.ID, p.Version, nil); !errors.Is(err, deferrals.ErrUnpaidAdjustments) {
		t.Errorf("undo with a planned fee: %v", err)
	}

	paidBy := audit.Actor{Kind: audit.ActorUser, UserID: "01JD3Y4C8V9M2Q7W5R6T0XZBNC"}
	if _, err := f.ds.MarkAdjustmentPaid(f.ctx, alice, p.ID, "01JD3Y4C8V9M2Q7W5R6T0XZBNB", p.Version); err == nil {
		t.Error("marking an unknown adjustment paid succeeded")
	}
	paid, err := f.ds.MarkAdjustmentPaid(f.ctx, paidBy, p.ID, fee.ID, p.Version)
	if err != nil {
		t.Fatal(err)
	}
	a := paid.Adjustments[0]
	if paid.Version != p.Version+1 || !a.Paid() || a.PaidAt == nil || !a.PaidAt.Equal(t0) || a.PaidBy == nil || a.PaidBy.UserID != paidBy.UserID || a.PaidBy.Kind != audit.ActorUser {
		t.Fatalf("after marking paid: %+v", paid)
	}
	if paid.AdjustmentTotal().String() != "12.50 EUR" || paid.UnpaidAdjustmentTotal().String() != "0.00 EUR" {
		t.Errorf("totals of the plan: all %s unpaid %s", paid.AdjustmentTotal(), paid.UnpaidAdjustmentTotal())
	}
	if n := f.totals("2026-11"); n.Planned.String() != "800.00 EUR" || n.Adjustments.String() != "0.00 EUR" {
		t.Errorf("November after marking the fee paid: planned %s adjustments %s", n.Planned, n.Adjustments)
	}
	if st := f.state(oct.ID); st.EvidenceCount != 1 || st.Outstanding.Sign() != 0 {
		t.Errorf("the fee changed the occurrence's evidence: %+v", st)
	}
	if _, err := f.ds.MarkAdjustmentPaid(f.ctx, alice, p.ID, fee.ID, paid.Version); !errors.Is(err, deferrals.ErrAdjustmentPaid) {
		t.Errorf("mark twice: %v", err)
	}

	// Reversible: the fee is planned again, and the plan can be undone only once it is settled.
	back, err := f.ds.UnmarkAdjustmentPaid(f.ctx, alice, p.ID, fee.ID, paid.Version)
	if err != nil {
		t.Fatal(err)
	}
	if back.Adjustments[0].Paid() || back.Adjustments[0].PaidBy != nil || back.UnpaidAdjustmentTotal().String() != "12.50 EUR" {
		t.Fatalf("after clearing the marker: %+v", back)
	}
	if n := f.totals("2026-11"); n.Adjustments.String() != "12.50 EUR" {
		t.Errorf("November after clearing the marker: adjustments %s, want 12.50", n.Adjustments)
	}
	if _, err := f.ds.UnmarkAdjustmentPaid(f.ctx, alice, p.ID, fee.ID, back.Version); !errors.Is(err, deferrals.ErrAdjustmentNotPaid) {
		t.Errorf("unmark an unpaid fee: %v", err)
	}

	// A stale version is refused like every plan write.
	if _, err := f.ds.MarkAdjustmentPaid(f.ctx, alice, p.ID, fee.ID, p.Version); err == nil {
		t.Error("marking paid with a stale version succeeded")
	}

	// Removal ends it too, and a removed fee can no longer change.
	removed, err := f.ds.RemoveAdjustment(f.ctx, alice, p.ID, fee.ID, back.Version)
	if err != nil {
		t.Fatal(err)
	}
	if n := f.totals("2026-11"); n.Adjustments.String() != "0.00 EUR" {
		t.Errorf("November after removal: adjustments %s", n.Adjustments)
	}
	if _, err := f.ds.MarkAdjustmentPaid(f.ctx, alice, p.ID, fee.ID, removed.Version); !errors.Is(err, deferrals.ErrAdjustmentRemoved) {
		t.Errorf("mark a removed fee: %v", err)
	}
	if _, err := f.ds.Undo(f.ctx, alice, p.ID, removed.Version, nil); err != nil {
		t.Errorf("undo once nothing is planned: %v", err)
	}

	h, err := f.ds.History(f.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range h {
		got = append(got, string(e.Action))
	}
	if want := "created adjustment_added adjustment_paid adjustment_unpaid adjustment_removed undone"; strings.Join(got, " ") != want {
		t.Errorf("history %v, want %s", got, want)
	}
	if h[2].AdjustmentID != fee.ID || h[2].By.UserID != paidBy.UserID {
		t.Errorf("the paid event does not say which fee and who: %+v", h[2])
	}
	if a := f.rec.actions("deferral_plan."); a != "deferral_plan.created deferral_plan.adjustment_added deferral_plan.adjustment_paid deferral_plan.adjustment_unpaid deferral_plan.adjustment_removed deferral_plan.undone" {
		t.Errorf("audit: %s", a)
	}
	for _, e := range f.rec.events {
		if e.Action == audit.DeferralAdjustmentPaid && (e.Details["adjustment_id"] != fee.ID || e.Details["kind"] != "fee" || strings.Contains(strings.Join(mapValues(e.Details), " "), "12.50")) {
			t.Errorf("paid audit details %v", e.Details)
		}
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
