package matches_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

func (f *fixture) scanRow(query string, args []any, dest ...any) error {
	return f.db.ReadTx(f.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, query, args...).Scan(dest...)
	})
}

// TestToleranceIsRecordedAtConfirmation (review of C3i, F3): whether a match
// was confirmed within the tolerance is recorded on the match when it is
// confirmed, so editing or clearing the commitment's tolerance later does not
// change a past occurrence, and a tolerance set later does not rescue a past
// short match.
func TestToleranceIsRecordedAtConfirmation(t *testing.T) {
	f := newFixture(t)
	within := f.occurrence(f.named("Streaming service", "13.49", 5, "0.50").ID, "2026-10")
	m := f.mustMatch(f.tx("STREAMING SERVICE"), within, "12.99") // short by 0.50, the tolerance
	if st := f.state(within); st.Status != payments.StatusPaid || st.Shortfall.String() != "0.50 EUR" {
		t.Fatalf("confirmed within the tolerance: %+v", st)
	}
	var amt, cur string
	if err := f.scanRow("SELECT tolerance_amount, tolerance_currency FROM payment_matches WHERE id = ?", []any{m.ID}, &amt, &cur); err != nil || amt != "0.50" || cur != "EUR" {
		t.Fatalf("recorded tolerance: %q %q %v", amt, cur, err)
	}
	// Lowering and clearing the tolerance leave the past occurrence paid.
	f.mustExec("UPDATE commitments SET match_tolerance_amount = '0.10'")
	if st := f.state(within); st.Status != payments.StatusPaid || st.Shortfall.String() != "0.50 EUR" {
		t.Errorf("tolerance lowered: %+v", st)
	}
	f.mustExec("UPDATE commitments SET match_tolerance_amount = NULL, match_tolerance_currency = NULL")
	if st := f.state(within); st.Status != payments.StatusPaid {
		t.Errorf("tolerance cleared: %+v", st)
	}

	// A short match confirmed with no tolerance stays partially paid when a
	// tolerance is set afterwards.
	g := newFixture(t)
	none := g.occurrence(g.named("Streaming service", "13.49", 5, "").ID, "2026-10")
	g.mustMatch(g.tx("STREAMING SERVICE"), none, "12.99")
	g.mustExec("UPDATE commitments SET match_tolerance_amount = '5.00', match_tolerance_currency = 'EUR'")
	if st := g.state(none); st.Status != payments.StatusPartiallyPaid || !st.Shortfall.IsZero() {
		t.Errorf("tolerance raised later: %+v", st)
	}
}

// TestToleranceNeedsBankEvidenceAlone (review of C3i, F2): a manual payment
// in the occurrence's evidence keeps it partially paid even when a bank match
// falls within the tolerance.
func TestToleranceNeedsBankEvidenceAlone(t *testing.T) {
	f := newFixture(t)
	o := f.occurrence(f.named("Streaming service", "13.49", 5, "0.50").ID, "2026-10")
	if _, _, err := f.ps.RecordManual(f.ctx, alice, o.ID, payments.NewManual{Amount: money.MustParse("0.49", "EUR"), PaidOn: d("2026-10-06")}, d("2026-11-01")); err != nil {
		t.Fatal(err)
	}
	m, err := f.match(f.tx("STREAMING SERVICE"), o, "12.99", matches.OriginUser, matches.StateConfirmed)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.scanRow("SELECT count(*) FROM payment_matches WHERE id = ? AND tolerance_amount IS NOT NULL", []any{m.Match.ID}, &n); err != nil || n != 0 {
		t.Fatalf("a tolerance was recorded beside manual evidence: %d %v", n, err)
	}
	if st := f.state(o); st.Status != payments.StatusPartiallyPaid || st.Outstanding.String() != "0.01 EUR" {
		t.Fatalf("manual plus bank: %+v", st)
	}
}
