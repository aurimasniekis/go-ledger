package money

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestParseDecimalKeepsSourceForm(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		scale    int32
	}{
		{"0", "0", 0},
		{"12.50", "12.50", 2},
		{"-12.50", "-12.50", 2},
		{"+7", "7", 0},
		{"0.0001", "0.0001", 4},
		{"-0.001", "-0.001", 3},
		{"000123.4", "123.4", 1},
		{"100.000", "100.000", 3},
		{"-0.00", "0.00", 2}, // negative zero has no sign
		{"0.000000000000000000000001", "0.000000000000000000000001", 24},
		{strings.Repeat("9", 60), strings.Repeat("9", 60), 0},
	} {
		d, err := ParseDecimal(tc.in)
		if err != nil {
			t.Fatalf("ParseDecimal(%q): %v", tc.in, err)
		}
		if d.String() != tc.want || d.Scale() != tc.scale {
			t.Errorf("ParseDecimal(%q) = %q scale %d, want %q scale %d", tc.in, d, d.Scale(), tc.want, tc.scale)
		}
	}
}

func TestParseDecimalRejects(t *testing.T) {
	for _, in := range []string{
		"", "-", "+", ".", ".5", "5.", "-.5", "1.2.3", "1e5", "1E-2", "0x10", "1,000.00", "1 000",
		" 1", "1 ", "--1", "+-1", "1-", "NaN", "Inf", "١٢", "1_000",
		"0." + strings.Repeat("1", MaxScale+1),
		strings.Repeat("9", MaxDigits+1),
	} {
		if d, err := ParseDecimal(in); err == nil {
			t.Errorf("ParseDecimal(%q) = %s, want error", in, d)
		} else if !errors.Is(err, ErrInvalidDecimal) {
			t.Errorf("ParseDecimal(%q) error %v does not wrap ErrInvalidDecimal", in, err)
		}
	}
}

func TestZeroValueDecimal(t *testing.T) {
	var d Decimal
	if d.String() != "0" || !d.IsZero() || d.Scale() != 0 || d.Sign() != 0 {
		t.Fatalf("zero value = %q", d)
	}
	if got := d.Add(MustParseDecimal("1.5")); got.String() != "1.5" {
		t.Fatalf("0 + 1.5 = %s", got)
	}
}

func TestEqualVersusIdentical(t *testing.T) {
	a, b := MustParseDecimal("1.5"), MustParseDecimal("1.50")
	if !a.Equal(b) || a.Cmp(b) != 0 {
		t.Error("1.5 and 1.50 must be numerically equal")
	}
	if a.Identical(b) {
		t.Error("1.5 and 1.50 must not be identical: the scale is part of the source form")
	}
}

func TestRoundHalfEven(t *testing.T) {
	for _, tc := range []struct {
		in    string
		scale int32
		want  string
	}{
		{"2.5", 0, "2"},
		{"3.5", 0, "4"},
		{"-2.5", 0, "-2"},
		{"-3.5", 0, "-4"},
		{"2.45", 1, "2.4"},
		{"2.55", 1, "2.6"},
		{"2.451", 1, "2.5"},
		{"-2.451", 1, "-2.5"},
		{"2.449", 1, "2.4"},
		{"0.005", 2, "0.00"},
		{"0.015", 2, "0.02"},
		{"-0.005", 2, "0.00"},
		{"1.005", 2, "1.00"},
		{"1.0050001", 2, "1.01"},
		{"9.995", 2, "10.00"},
		{"-9.995", 2, "-10.00"},
		{"1.5", 3, "1.500"},
		{"12", 2, "12.00"},
		{"0.12345", 4, "0.1234"},
		{"0.12355", 4, "0.1236"},
	} {
		got := MustParseDecimal(tc.in).RoundHalfEven(tc.scale)
		if got.String() != tc.want {
			t.Errorf("RoundHalfEven(%s, %d) = %s, want %s", tc.in, tc.scale, got, tc.want)
		}
	}
}

func TestWithScaleNeverRounds(t *testing.T) {
	d := MustParseDecimal("1.50")
	if got, err := d.WithScale(4); err != nil || got.String() != "1.5000" {
		t.Errorf("WithScale(4) = %s, %v", got, err)
	}
	if got, err := d.WithScale(1); err != nil || got.String() != "1.5" {
		t.Errorf("WithScale(1) = %s, %v", got, err)
	}
	if _, err := MustParseDecimal("1.55").WithScale(1); err == nil {
		t.Error("WithScale(1) of 1.55 must fail rather than round")
	}
	if _, err := d.WithScale(MaxScale + 1); err == nil {
		t.Error("WithScale above MaxScale must fail")
	}
}

func TestMulIsExact(t *testing.T) {
	got := MustParseDecimal("19.99").Mul(MustParseDecimal("1.0825"))
	if got.String() != "21.639175" {
		t.Fatalf("19.99 × 1.0825 = %s", got)
	}
	if r := got.RoundHalfEven(2); r.String() != "21.64" {
		t.Fatalf("rounded = %s", r)
	}
}

func TestDecimalJSONIsAString(t *testing.T) {
	b, err := json.Marshal(MustParseDecimal("10.10"))
	if err != nil || string(b) != `"10.10"` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var d Decimal
	if err := json.Unmarshal([]byte(`"0.30"`), &d); err != nil || d.String() != "0.30" {
		t.Fatalf("Unmarshal = %s, %v", d, err)
	}
	if err := json.Unmarshal([]byte(`0.30`), &d); err == nil {
		t.Fatal("a JSON number must be rejected: it would pass through float64")
	}
}

func FuzzParseDecimal(f *testing.F) {
	for _, s := range []string{"0", "-1.50", "+3", "0.001", "1e3", ".5", "99999999999999999999.99", "-0.0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDecimal(s)
		if err != nil {
			return
		}
		// Round trip is identical.
		back, err := ParseDecimal(d.String())
		if err != nil || !back.Identical(d) {
			t.Fatalf("round trip of %q: %q, %v", s, d.String(), err)
		}
		// The value matches an exact rational parse of the input.
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("big.Rat rejects %q that ParseDecimal accepted", s)
		}
		if toRat(d).Cmp(r) != 0 {
			t.Fatalf("%q parsed as %s", s, d)
		}
	})
}

func toRat(d Decimal) *big.Rat {
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d.Scale())), nil)
	return new(big.Rat).SetFrac(d.Coefficient(), den)
}
