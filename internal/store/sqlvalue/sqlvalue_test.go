package sqlvalue

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/sqlvalue.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf(`CREATE TABLE payments (
		id INTEGER PRIMARY KEY,
		amount TEXT NOT NULL CHECK (%s),
		currency TEXT NOT NULL CHECK (%s),
		due_on TEXT NOT NULL CHECK (%s)
	) STRICT`, AmountCheck("amount"), CurrencyCheck("currency"), DateCheck("due_on"))
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// The amounts include values beyond float64's 15-17 significant digits,
// beyond int64, at every scale up to MaxScale, with trailing zeros, and
// classic binary-float traps (0.1, 0.3).
var roundTripAmounts = []string{
	"0", "0.00", "-0.10", "0.1", "0.3", "1.50", "100", "-12345.6789",
	"0.000000000000000000000001", "-0.000000000000000000000001",
	"9007199254740993", // 2^53 + 1: not representable in float64
	"92233720368547758070.01",
	"123456789012345678901234567890123456.123456789012345678901234",
	"-" + strings.Repeat("9", 36) + "." + strings.Repeat("9", 24),
	"1.10", "1.100", "1.1000",
}

func TestMoneyRoundTripIsExact(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	due := calendar.MustDate(2024, time.February, 29)
	for i, s := range roundTripAmounts {
		for _, code := range []string{"EUR", "JPY", "KWD", "XAU", "QQQ"} {
			m := money.MustParse(s, code)
			if _, err := db.ExecContext(ctx, "INSERT INTO payments (amount, currency, due_on) VALUES (?, ?, ?)",
				Amount{m.Amount()}, Currency{m.Currency()}, Date{due}); err != nil {
				t.Fatalf("insert %d %s: %v", i, m, err)
			}
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT amount, currency, due_on, typeof(amount) FROM payments ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var a Amount
		var c Currency
		var d Date
		var typ string
		if err := rows.Scan(&a, &c, &d, &typ); err != nil {
			t.Fatal(err)
		}
		got, err := Money(a, c)
		if err != nil {
			t.Fatal(err)
		}
		want := money.MustParse(roundTripAmounts[n/5], c.Code())
		if !got.Identical(want) || typ != "text" {
			t.Errorf("round trip: got %s (%s), want %s", got, typ, want)
		}
		if d.Date != due {
			t.Errorf("date round trip: %s", d.Date)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != len(roundTripAmounts)*5 {
		t.Fatalf("read %d rows", n)
	}
}

func TestChecksRejectMalformedText(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	insert := func(amount, currency, due any) error {
		_, err := db.ExecContext(ctx, "INSERT INTO payments (amount, currency, due_on) VALUES (?, ?, ?)", amount, currency, due)
		return err
	}
	if err := insert("1.50", "EUR", "2026-01-31"); err != nil {
		t.Fatalf("valid row rejected: %v", err)
	}
	for _, bad := range []string{"", "-", ".", "1.", ".5", "-.5", "1.2.3", "1e5", "1-2", "--1", "+1", " 1", "1,00", "abc"} {
		if err := insert(bad, "EUR", "2026-01-31"); err == nil {
			t.Errorf("amount %q accepted by CHECK", bad)
		}
	}
	for _, bad := range []string{"eur", "EU", "EURO", "E1R"} {
		if err := insert("1", bad, "2026-01-31"); err == nil {
			t.Errorf("currency %q accepted by CHECK", bad)
		}
	}
	for _, bad := range []string{"2026-02-30", "2026-1-5", "2026-01-05T00:00:00Z", "05/01/2026"} {
		if err := insert("1", "EUR", bad); err == nil {
			t.Errorf("date %q accepted by CHECK", bad)
		}
	}
}

// TestNumericAffinityWouldLoseScale documents why amounts are TEXT: the same
// value in a NUMERIC column comes back as a float without its scale.
func TestNumericAffinityWouldLoseScale(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE loose (amount NUMERIC)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO loose VALUES (?)", Amount{money.MustParseDecimal("1.50")}); err != nil {
		t.Fatal(err)
	}
	var a Amount
	err := db.QueryRowContext(ctx, "SELECT amount FROM loose").Scan(&a)
	if err == nil {
		t.Fatalf("scanning a NUMERIC column succeeded with %s; the scanner must refuse non-text", a.Decimal)
	}
	if !strings.Contains(err.Error(), "must be TEXT") {
		t.Fatalf("error = %v", err)
	}
}

func TestScannersRefuseNonText(t *testing.T) {
	var a Amount
	var c Currency
	var d Date
	for _, src := range []any{int64(1), nil, true, time.Now()} {
		if a.Scan(src) == nil || c.Scan(src) == nil || d.Scan(src) == nil {
			t.Errorf("Scan(%T) succeeded", src)
		}
	}
	if err := a.Scan([]byte("2.50")); err != nil || a.String() != "2.50" {
		t.Errorf("Scan([]byte) = %s, %v", a.Decimal, err)
	}
	if a.Scan("1e3") == nil || c.Scan("eur") == nil || d.Scan("2026-02-30") == nil {
		t.Error("malformed stored text accepted")
	}
}

func TestValuersRefuseZeroAndOversizedValues(t *testing.T) {
	if _, err := (Currency{}).Value(); err == nil {
		t.Error("zero currency stored")
	}
	if _, err := (Date{}).Value(); err == nil {
		t.Error("zero date stored")
	}
	tiny := money.MustParseDecimal("0." + strings.Repeat("0", money.MaxScale-1) + "1")
	if _, err := (Amount{tiny.Mul(tiny)}).Value(); err == nil {
		t.Error("an amount above MaxScale was stored; it could not be read back")
	}
}
