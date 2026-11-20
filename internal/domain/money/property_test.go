package money

import (
	"errors"
	"math/big"
	"math/rand/v2"
	"testing"
)

// The property tests compare Decimal and Money against math/big.Rat, an exact
// rational reference, over random values of mixed sign, size and scale. The
// seed is fixed so that a failure reproduces; the iteration count keeps the
// race-enabled run short.
const propertyRuns = 4000

func randDecimal(r *rand.Rand) Decimal {
	scale := int32(r.IntN(9))
	var coef *big.Int
	switch r.IntN(4) {
	case 0: // small
		coef = big.NewInt(r.Int64N(2000) - 1000)
	case 1: // typical amounts
		coef = big.NewInt(r.Int64N(2_000_000_000) - 1_000_000_000)
	case 2: // beyond int64
		coef = new(big.Int).Lsh(big.NewInt(r.Int64N(1<<40)+1), 64)
		coef.Add(coef, big.NewInt(r.Int64()))
		if r.IntN(2) == 0 {
			coef.Neg(coef)
		}
	default:
		coef = big.NewInt(0)
	}
	return Decimal{coef: coef, scale: scale}
}

func newRand() *rand.Rand { return rand.New(rand.NewPCG(20261120, 9)) }

func TestPropertyAddSubAgainstExactReference(t *testing.T) {
	r := newRand()
	for range propertyRuns {
		a, b := randDecimal(r), randDecimal(r)
		sum, diff := a.Add(b), a.Sub(b)
		if toRat(sum).Cmp(new(big.Rat).Add(toRat(a), toRat(b))) != 0 {
			t.Fatalf("%s + %s = %s", a, b, sum)
		}
		if toRat(diff).Cmp(new(big.Rat).Sub(toRat(a), toRat(b))) != 0 {
			t.Fatalf("%s - %s = %s", a, b, diff)
		}
		if want := max(a.Scale(), b.Scale()); sum.Scale() != want || diff.Scale() != want {
			t.Fatalf("scale of %s ± %s: %d/%d, want %d", a, b, sum.Scale(), diff.Scale(), want)
		}
	}
}

func TestPropertyAlgebra(t *testing.T) {
	r := newRand()
	for range propertyRuns {
		a, b, c := randDecimal(r), randDecimal(r), randDecimal(r)
		if !a.Add(b).Identical(b.Add(a)) {
			t.Fatalf("not commutative: %s, %s", a, b)
		}
		if !a.Add(b).Add(c).Identical(a.Add(b.Add(c))) {
			t.Fatalf("not associative: %s, %s, %s", a, b, c)
		}
		if !a.Add(b).Sub(b).Equal(a) {
			t.Fatalf("(a+b)-b != a: %s, %s", a, b)
		}
		if !a.Sub(b).Identical(a.Add(b.Neg())) {
			t.Fatalf("a-b != a+(-b): %s, %s", a, b)
		}
		if !a.Add(Decimal{}).Identical(a) {
			t.Fatalf("a+0 != a: %s", a)
		}
		if !a.Sub(a).IsZero() {
			t.Fatalf("a-a != 0: %s", a)
		}
	}
}

func TestPropertyCompare(t *testing.T) {
	r := newRand()
	for range propertyRuns {
		a, b, c := randDecimal(r), randDecimal(r), randDecimal(r)
		if got, want := a.Cmp(b), toRat(a).Cmp(toRat(b)); got != want {
			t.Fatalf("Cmp(%s, %s) = %d, want %d", a, b, got, want)
		}
		if a.Cmp(b) != -b.Cmp(a) {
			t.Fatalf("Cmp not antisymmetric: %s, %s", a, b)
		}
		if a.Cmp(b) <= 0 && b.Cmp(c) <= 0 && a.Cmp(c) > 0 {
			t.Fatalf("Cmp not transitive: %s, %s, %s", a, b, c)
		}
		// Rescaling never changes the value or the order.
		up, err := a.WithScale(a.Scale() + 3)
		if err != nil || !up.Equal(a) || up.Cmp(b) != a.Cmp(b) {
			t.Fatalf("rescale changed %s: %s, %v", a, up, err)
		}
		// Adding a positive amount always increases.
		if b.Sign() > 0 && a.Add(b).Cmp(a) <= 0 {
			t.Fatalf("%s + %s not greater than %s", a, b, a)
		}
	}
}

func TestPropertyStringRoundTrip(t *testing.T) {
	r := newRand()
	for range propertyRuns {
		a := randDecimal(r)
		back, err := ParseDecimal(a.String())
		if err != nil || !back.Identical(a) {
			t.Fatalf("round trip of %s: %s, %v", a, back, err)
		}
	}
}

func TestPropertyRoundHalfEvenAgainstReference(t *testing.T) {
	r := newRand()
	for range propertyRuns {
		a := randDecimal(r)
		scale := int32(r.IntN(int(a.Scale()) + 2))
		got := a.RoundHalfEven(scale)
		// Reference: the rounding error is at most half a unit, and an exact
		// half rounds to an even last digit.
		unit := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
		errAbs := new(big.Rat).Abs(new(big.Rat).Sub(toRat(got), toRat(a)))
		half := new(big.Rat).Mul(unit, big.NewRat(1, 2))
		switch errAbs.Cmp(half) {
		case 1:
			t.Fatalf("RoundHalfEven(%s, %d) = %s: off by more than half a unit", a, scale, got)
		case 0:
			if got.Coefficient().Bit(0) != 0 {
				t.Fatalf("RoundHalfEven(%s, %d) = %s: a tie must round to even", a, scale, got)
			}
		}
		if got.Scale() != scale {
			t.Fatalf("RoundHalfEven(%s, %d) has scale %d", a, scale, got.Scale())
		}
	}
}

func TestPropertyMoneyNeverCombinesCurrencies(t *testing.T) {
	r := newRand()
	codes := []Currency{MustParseCurrency("EUR"), MustParseCurrency("USD"), MustParseCurrency("JPY")}
	for range propertyRuns {
		ca, cb := codes[r.IntN(3)], codes[r.IntN(3)]
		a, _ := New(randDecimal(r), ca)
		b, _ := New(randDecimal(r), cb)
		sum, errAdd := a.Add(b)
		_, errSub := a.Sub(b)
		_, errCmp := a.Cmp(b)
		if ca == cb {
			if errAdd != nil || errSub != nil || errCmp != nil {
				t.Fatalf("same currency %s failed: %v %v %v", ca, errAdd, errSub, errCmp)
			}
			if sum.Currency() != ca || !sum.Amount().Equal(a.Amount().Add(b.Amount())) {
				t.Fatalf("%s + %s = %s", a, b, sum)
			}
			continue
		}
		for _, err := range []error{errAdd, errSub, errCmp} {
			if !errors.Is(err, ErrCurrencyMismatch) {
				t.Fatalf("%s with %s: %v, want ErrCurrencyMismatch", a, b, err)
			}
		}
	}
}
