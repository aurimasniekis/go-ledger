package money

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseCurrency(t *testing.T) {
	for _, ok := range []string{"EUR", "USD", "JPY", "XAU", "QQQ"} {
		if _, err := ParseCurrency(ok); err != nil {
			t.Errorf("ParseCurrency(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "EU", "EURO", "eur", "Eur", "E1R", "€", "EÜR", " EU"} {
		if _, err := ParseCurrency(bad); !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("ParseCurrency(%q) = %v, want ErrInvalidCurrency", bad, err)
		}
	}
}

func TestCurrencyTable(t *testing.T) {
	for code := range iso4217 {
		if _, err := ParseCurrency(code); err != nil {
			t.Errorf("table entry %q is not a well-formed code", code)
		}
	}
	for _, tc := range []struct {
		code   string
		digits int32
		ok     bool
	}{
		{"EUR", 2, true}, {"USD", 2, true}, {"JPY", 0, true}, {"KRW", 0, true},
		{"KWD", 3, true}, {"BHD", 3, true}, {"CLF", 4, true},
		{"XAU", 0, false}, {"XXX", 0, false}, {"QQQ", 0, false},
	} {
		d, ok := MustParseCurrency(tc.code).MinorUnits()
		if d != tc.digits || ok != tc.ok {
			t.Errorf("MinorUnits(%s) = %d, %v; want %d, %v", tc.code, d, ok, tc.digits, tc.ok)
		}
	}
	if !MustParseCurrency("XAU").Known() || MustParseCurrency("QQQ").Known() {
		t.Error("Known() is wrong for XAU or QQQ")
	}
}

func TestNewRequiresCurrency(t *testing.T) {
	if _, err := New(MustParseDecimal("1"), Currency{}); !errors.Is(err, ErrNoCurrency) {
		t.Fatalf("New without currency: %v", err)
	}
	var zero Money
	if _, err := zero.Add(MustParse("1", "EUR")); !errors.Is(err, ErrNoCurrency) {
		t.Fatalf("zero Money Add: %v", err)
	}
	if _, err := MustParse("1", "EUR").Cmp(zero); !errors.Is(err, ErrNoCurrency) {
		t.Fatalf("Cmp with zero Money: %v", err)
	}
}

func TestMoneyArithmetic(t *testing.T) {
	a, b := MustParse("10.10", "EUR"), MustParse("0.205", "EUR")
	sum, err := a.Add(b)
	if err != nil || sum.String() != "10.305 EUR" {
		t.Fatalf("Add = %s, %v", sum, err)
	}
	diff, err := a.Sub(b)
	if err != nil || diff.String() != "9.895 EUR" {
		t.Fatalf("Sub = %s, %v", diff, err)
	}
	if c, err := a.Cmp(b); err != nil || c != 1 {
		t.Fatalf("Cmp = %d, %v", c, err)
	}
	_, err = a.Add(MustParse("1", "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) || !strings.Contains(err.Error(), "EUR and USD") {
		t.Fatalf("EUR + USD = %v", err)
	}
	if MustParse("1.5", "EUR").Equal(MustParse("1.5", "USD")) {
		t.Fatal("1.5 EUR must not equal 1.5 USD")
	}
	if !MustParse("1.5", "EUR").Equal(MustParse("1.50", "EUR")) || MustParse("1.5", "EUR").Identical(MustParse("1.50", "EUR")) {
		t.Fatal("Equal/Identical wrong for 1.5 and 1.50 EUR")
	}
}

func TestForDisplay(t *testing.T) {
	for _, tc := range []struct {
		amount, code, want string
		rounded            bool
	}{
		{"12.5", "EUR", "12.50", false},
		{"12.345", "EUR", "12.34", true},
		{"12.355", "EUR", "12.36", true},
		{"-0.125", "EUR", "-0.12", true},
		{"1500", "JPY", "1500", false},
		{"1500.5", "JPY", "1500", true},
		{"1.2345", "KWD", "1.234", true},
		{"1.23456789", "XAU", "1.23456789", false}, // no minor units: own scale
		{"1.23456789", "QQQ", "1.23456789", false}, // unknown code: own scale
	} {
		got, rounded := MustParse(tc.amount, tc.code).ForDisplay()
		if got.String() != tc.want || rounded != tc.rounded {
			t.Errorf("ForDisplay(%s %s) = %s, %v; want %s, %v", tc.amount, tc.code, got, rounded, tc.want, tc.rounded)
		}
	}
}

func TestMoneyJSON(t *testing.T) {
	m := MustParse("-1234.500", "EUR")
	b, err := json.Marshal(m)
	if err != nil || string(b) != `{"amount":"-1234.500","currency":"EUR"}` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var back Money
	if err := json.Unmarshal(b, &back); err != nil || !back.Identical(m) {
		t.Fatalf("Unmarshal = %s, %v", back, err)
	}
	for _, bad := range []string{
		`{"amount":12.5,"currency":"EUR"}`,
		`{"amount":"12.5"}`,
		`{"currency":"EUR"}`,
		`{"amount":"12.5","currency":"eur"}`,
		`{"amount":"1e3","currency":"EUR"}`,
		`{"amount":"12.5","currency":"EUR","rate":"1"}`,
		`"12.5 EUR"`,
	} {
		if err := json.Unmarshal([]byte(bad), &back); err == nil {
			t.Errorf("Unmarshal(%s) succeeded, want error", bad)
		}
	}
	if _, err := json.Marshal(Money{}); err == nil {
		t.Error("Marshal of the zero Money must fail")
	}
}

func TestSumRejectsMixedCurrencies(t *testing.T) {
	_, err := Sum(MustParse("1.00", "EUR"), MustParse("2.00", "USD"), MustParse("3", "EUR"), MustParse("1", "GBP"))
	if !errors.Is(err, ErrMixedCurrencies) {
		t.Fatalf("Sum = %v, want ErrMixedCurrencies", err)
	}
	var mixed *MixedCurrencyError
	if !errors.As(err, &mixed) {
		t.Fatalf("Sum error %T is not a MixedCurrencyError", err)
	}
	var codes []string
	for _, c := range mixed.Currencies {
		codes = append(codes, c.Code())
	}
	if strings.Join(codes, ",") != "EUR,GBP,USD" {
		t.Fatalf("currencies = %v", codes)
	}
	if !strings.Contains(err.Error(), "EUR, GBP, USD") {
		t.Fatalf("message = %q", err)
	}
}

func TestSum(t *testing.T) {
	got, err := Sum(MustParse("1.10", "EUR"), MustParse("2.205", "EUR"), MustParse("-0.3", "EUR"))
	if err != nil || got.String() != "3.005 EUR" {
		t.Fatalf("Sum = %s, %v", got, err)
	}
	if _, err := Sum(); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty Sum = %v", err)
	}
	if _, err := Sum(MustParse("1", "EUR"), Money{}); !errors.Is(err, ErrNoCurrency) {
		t.Fatalf("Sum with zero Money = %v", err)
	}
}

func TestTotalsPerCurrency(t *testing.T) {
	var tot Totals
	for _, m := range []Money{
		MustParse("10.00", "USD"), MustParse("5.5", "EUR"), MustParse("1000", "JPY"),
		MustParse("0.50", "EUR"), MustParse("-2.25", "USD"),
	} {
		if err := tot.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, m := range tot.List() {
		got = append(got, m.String())
	}
	if strings.Join(got, "; ") != "6.00 EUR; 1000 JPY; 7.75 USD" {
		t.Fatalf("List = %v", got)
	}
	if eur, ok := tot.Get(MustParseCurrency("EUR")); !ok || eur.String() != "6.00 EUR" {
		t.Fatalf("Get(EUR) = %s, %v", eur, ok)
	}
	if _, ok := tot.Get(MustParseCurrency("GBP")); ok {
		t.Fatal("Get(GBP) found a total")
	}
	if _, err := tot.Single(); !errors.Is(err, ErrMixedCurrencies) {
		t.Fatalf("Single = %v", err)
	}
	if tot.Len() != 3 {
		t.Fatalf("Len = %d", tot.Len())
	}
}
