package commitments

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

func TestMatchToleranceRoundTripAndSchema(t *testing.T) {
	ctx := context.Background()
	st, db, _ := newStore(t)
	f := vps()
	tol := money.MustParse("0.50", "EUR")
	f.MatchTolerance = &tol
	c, err := st.Create(ctx, alice, f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, c.ID)
	if err != nil || got.MatchTolerance == nil || !got.MatchTolerance.Identical(tol) {
		t.Fatalf("get: %+v %v", got.MatchTolerance, err)
	}
	f.MatchTolerance = nil
	if got, err = st.Update(ctx, alice, c.ID, 1, f, calendar.MustDate(2026, time.January, 5), calendar.MustDate(2026, time.January, 5)); err != nil || got.MatchTolerance != nil {
		t.Fatalf("clear: %+v %v", got.MatchTolerance, err)
	}
	// The database keeps the pair together and in the commitment's currency.
	exec := func(q string, args ...any) error {
		return db.WriteTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		})
	}
	for q, want := range map[string]string{
		"UPDATE commitments SET match_tolerance_amount = '1.00', match_tolerance_currency = 'USD' WHERE id = ?":  "match tolerance",
		"UPDATE commitments SET match_tolerance_amount = '1.00' WHERE id = ?":                                    "match tolerance",
		"UPDATE commitments SET match_tolerance_amount = '-1.00', match_tolerance_currency = 'EUR' WHERE id = ?": "CHECK",
	} {
		if err := exec(q, c.ID); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", q, err)
		}
	}
	if err := exec("UPDATE commitments SET match_tolerance_amount = '1.00', match_tolerance_currency = 'EUR' WHERE id = ?", c.ID); err != nil {
		t.Fatal(err)
	}
	if err := exec("UPDATE commitments SET currency = 'USD' WHERE id = ?", c.ID); err == nil || !strings.Contains(err.Error(), "match tolerance") {
		t.Errorf("currency change under a tolerance: %v", err)
	}
}
