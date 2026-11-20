package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// MaxScale is the largest scale [ParseDecimal] accepts and storage writes.
// ISO 4217 currencies use at most 4 minor digits; the headroom keeps source
// amounts with more digits (and exchange rates) exact.
const MaxScale = 24

// MaxDigits is the largest number of digits (integer and fraction together)
// [ParseDecimal] accepts. It bounds the work an untrusted input can cause.
const MaxDigits = 60

// ErrInvalidDecimal is wrapped by every decimal parsing error.
var ErrInvalidDecimal = errors.New("money: invalid decimal")

// Decimal is an exact decimal number: coef × 10^(-scale).
//
// The zero value is 0 with scale 0. A Decimal is immutable: every operation
// returns a new value. Compare values with [Decimal.Cmp] or [Decimal.Equal],
// never with ==, which the compiler rejects: 1.00 and 1.00 are
// separate allocations, so == could never answer correctly.
type Decimal struct {
	_     [0]func() // makes the type non-comparable: == is a compile error
	coef  *big.Int  // nil means zero; never mutated after construction
	scale int32
}

var (
	bigZero = big.NewInt(0)
	bigTen  = big.NewInt(10)
)

func (d Decimal) c() *big.Int {
	if d.coef == nil {
		return bigZero
	}
	return d.coef
}

// NewDecimal returns coef × 10^(-scale). It panics if scale is negative or
// above [MaxScale]; use it for constants and tests, and [ParseDecimal] for
// input.
func NewDecimal(coef int64, scale int32) Decimal {
	if scale < 0 || scale > MaxScale {
		panic(fmt.Sprintf("money: scale %d out of range 0..%d", scale, MaxScale))
	}
	return Decimal{coef: big.NewInt(coef), scale: scale}
}

// ParseDecimal parses a plain decimal string: an optional sign, digits, and
// an optional fraction ("-12.50", "+3", "0.0001"). The scale is the number of
// fraction digits as written, so trailing zeros are kept.
//
// It rejects empty input, exponents, thousands separators, spaces, a leading
// or trailing point (".5", "5."), more than [MaxScale] fraction digits and more
// than [MaxDigits] digits. Locale-aware input belongs to the UI.
func ParseDecimal(s string) (Decimal, error) {
	orig := s
	if s == "" {
		return Decimal{}, fmt.Errorf("%w: empty", ErrInvalidDecimal)
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	intPart, frac, hasPoint := strings.Cut(s, ".")
	if intPart == "" || (hasPoint && frac == "") {
		return Decimal{}, fmt.Errorf("%w: %q", ErrInvalidDecimal, orig)
	}
	if !allDigits(intPart) || !allDigits(frac) {
		return Decimal{}, fmt.Errorf("%w: %q", ErrInvalidDecimal, orig)
	}
	if len(frac) > MaxScale {
		return Decimal{}, fmt.Errorf("%w: more than %d fraction digits", ErrInvalidDecimal, MaxScale)
	}
	if len(intPart)+len(frac) > MaxDigits {
		return Decimal{}, fmt.Errorf("%w: more than %d digits", ErrInvalidDecimal, MaxDigits)
	}
	coef, ok := new(big.Int).SetString(intPart+frac, 10)
	if !ok {
		return Decimal{}, fmt.Errorf("%w: %q", ErrInvalidDecimal, orig)
	}
	if neg {
		coef.Neg(coef)
	}
	return Decimal{coef: coef, scale: int32(len(frac))}, nil
}

// MustParseDecimal is [ParseDecimal] that panics on error, for constants and
// tests.
func MustParseDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Scale is the number of digits after the decimal point.
func (d Decimal) Scale() int32 { return d.scale }

// Coefficient returns a copy of the unscaled integer value.
func (d Decimal) Coefficient() *big.Int { return new(big.Int).Set(d.c()) }

// Sign returns -1, 0 or +1.
func (d Decimal) Sign() int { return d.c().Sign() }

// IsZero reports whether d is numerically zero, at any scale.
func (d Decimal) IsZero() bool { return d.Sign() == 0 }

// Neg returns -d with the same scale.
func (d Decimal) Neg() Decimal {
	return Decimal{coef: new(big.Int).Neg(d.c()), scale: d.scale}
}

// Abs returns |d| with the same scale.
func (d Decimal) Abs() Decimal {
	return Decimal{coef: new(big.Int).Abs(d.c()), scale: d.scale}
}

// rescaled returns the coefficient of d at a scale >= d.scale.
func (d Decimal) rescaled(scale int32) *big.Int {
	if scale == d.scale {
		return d.c()
	}
	f := new(big.Int).Exp(bigTen, big.NewInt(int64(scale-d.scale)), nil)
	return f.Mul(f, d.c())
}

func align(a, b Decimal) (x, y *big.Int, scale int32) {
	scale = max(a.scale, b.scale)
	return a.rescaled(scale), b.rescaled(scale), scale
}

// Add returns d + o exactly, at the larger of the two scales.
func (d Decimal) Add(o Decimal) Decimal {
	x, y, s := align(d, o)
	return Decimal{coef: new(big.Int).Add(x, y), scale: s}
}

// Sub returns d - o exactly, at the larger of the two scales.
func (d Decimal) Sub(o Decimal) Decimal {
	x, y, s := align(d, o)
	return Decimal{coef: new(big.Int).Sub(x, y), scale: s}
}

// Mul returns d × o exactly; the scale is the sum of the scales. Round the
// result with [Decimal.RoundHalfEven] where a policy says so (conversion).
func (d Decimal) Mul(o Decimal) Decimal {
	return Decimal{coef: new(big.Int).Mul(d.c(), o.c()), scale: d.scale + o.scale}
}

// Cmp compares numerically: 1.5 and 1.50 are equal.
func (d Decimal) Cmp(o Decimal) int {
	x, y, _ := align(d, o)
	return x.Cmp(y)
}

// Equal reports numeric equality (1.5 equals 1.50).
func (d Decimal) Equal(o Decimal) bool { return d.Cmp(o) == 0 }

// Identical reports equality of value and scale (1.5 is not identical to
// 1.50). Use it where source exactness matters, such as provenance checks.
func (d Decimal) Identical(o Decimal) bool {
	return d.scale == o.scale && d.c().Cmp(o.c()) == 0
}

// WithScale returns d at a larger or equal scale, padding with zeros. It
// returns an error if scale is out of range or would drop non-zero digits;
// it never rounds.
func (d Decimal) WithScale(scale int32) (Decimal, error) {
	if scale < 0 || scale > MaxScale {
		return Decimal{}, fmt.Errorf("money: scale %d out of range 0..%d", scale, MaxScale)
	}
	if scale >= d.scale {
		return Decimal{coef: d.rescaled(scale), scale: scale}, nil
	}
	r := d.RoundHalfEven(scale)
	if !r.Equal(d) {
		return Decimal{}, fmt.Errorf("money: %s cannot be written with %d fraction digits without rounding", d, scale)
	}
	return r, nil
}

// RoundHalfEven returns d rounded to scale digits after the point, with ties
// going to the even digit (banker's rounding, decision D5). A scale at or
// above d's scale pads with zeros. Use it only for presentation and
// conversion; stored amounts are never rounded.
func (d Decimal) RoundHalfEven(scale int32) Decimal {
	if scale < 0 {
		panic(fmt.Sprintf("money: RoundHalfEven scale %d is negative", scale))
	}
	if scale >= d.scale {
		return Decimal{coef: d.rescaled(scale), scale: scale}
	}
	div := new(big.Int).Exp(bigTen, big.NewInt(int64(d.scale-scale)), nil)
	q, r := new(big.Int).QuoRem(d.c(), div, new(big.Int)) // truncates toward zero
	// Compare 2|r| with div to decide the rounding direction.
	twice := new(big.Int).Abs(r)
	twice.Lsh(twice, 1)
	switch c := twice.Cmp(div); {
	case c > 0, c == 0 && q.Bit(0) == 1:
		if d.c().Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return Decimal{coef: q, scale: scale}
}

// String formats d exactly, with its own scale: "-12.50", "0.001", "7".
// [ParseDecimal] reads it back to an identical value.
func (d Decimal) String() string {
	c := d.c()
	digits := new(big.Int).Abs(c).String()
	var b strings.Builder
	if c.Sign() < 0 {
		b.WriteByte('-')
	}
	if d.scale == 0 {
		b.WriteString(digits)
		return b.String()
	}
	if pad := int(d.scale) + 1 - len(digits); pad > 0 {
		digits = strings.Repeat("0", pad) + digits
	}
	cut := len(digits) - int(d.scale)
	b.WriteString(digits[:cut])
	b.WriteByte('.')
	b.WriteString(digits[cut:])
	return b.String()
}

// MarshalText implements encoding.TextMarshaler with [Decimal.String]. In
// JSON a Decimal is therefore a string, never a JSON number.
func (d Decimal) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalJSON implements json.Unmarshaler: only a JSON string is accepted.
// Without it, encoding/json would silently turn null into 0 for a plain
// Decimal field.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fmt.Errorf("%w: a decimal must be a JSON string, got %s", ErrInvalidDecimal, truncate(string(b)))
	}
	return d.UnmarshalText([]byte(s))
}

func truncate(s string) string {
	if len(s) > 20 {
		return s[:20] + "..."
	}
	return s
}

// UnmarshalText implements encoding.TextUnmarshaler with [ParseDecimal].
// encoding/json calls it only for JSON strings, so JSON numbers are rejected.
func (d *Decimal) UnmarshalText(b []byte) error {
	v, err := ParseDecimal(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}
