package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrCurrencyMismatch is returned when an operation combines two currencies.
var ErrCurrencyMismatch = errors.New("money: currencies differ")

// ErrNoCurrency is returned when an operation gets a Money without a
// currency (the zero value).
var ErrNoCurrency = errors.New("money: amount has no currency")

// Money is an exact amount in one currency. The zero value has no currency
// and is invalid; every operation on it returns [ErrNoCurrency].
type Money struct {
	amount   Decimal
	currency Currency
}

// New returns amount in currency. It returns [ErrNoCurrency] for the zero
// currency.
func New(amount Decimal, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrNoCurrency
	}
	return Money{amount: amount, currency: currency}, nil
}

// Parse parses an amount with [ParseDecimal] and a code with
// [ParseCurrency], keeping the amount's scale as written.
func Parse(amount, currency string) (Money, error) {
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	d, err := ParseDecimal(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{amount: d, currency: c}, nil
}

// MustParse is [Parse] that panics on error, for constants and tests.
func MustParse(amount, currency string) Money {
	m, err := Parse(amount, currency)
	if err != nil {
		panic(err)
	}
	return m
}

// Zero returns 0 in currency at the given scale.
func Zero(currency Currency, scale int32) (Money, error) {
	d, err := Decimal{}.WithScale(scale)
	if err != nil {
		return Money{}, err
	}
	return New(d, currency)
}

// Amount returns the exact amount.
func (m Money) Amount() Decimal { return m.amount }

// Currency returns the currency.
func (m Money) Currency() Currency { return m.currency }

// IsValid reports whether m has a currency.
func (m Money) IsValid() bool { return !m.currency.IsZero() }

// IsZero reports whether the amount is numerically zero.
func (m Money) IsZero() bool { return m.amount.IsZero() }

// Sign returns -1, 0 or +1.
func (m Money) Sign() int { return m.amount.Sign() }

// Neg returns -m.
func (m Money) Neg() Money { return Money{amount: m.amount.Neg(), currency: m.currency} }

// Abs returns |m|.
func (m Money) Abs() Money { return Money{amount: m.amount.Abs(), currency: m.currency} }

func (m Money) check(o Money) error {
	if m.currency.IsZero() || o.currency.IsZero() {
		return ErrNoCurrency
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}

// Add returns m + o. Both must have the same currency; otherwise it returns
// [ErrCurrencyMismatch] and never converts.
func (m Money) Add(o Money) (Money, error) {
	if err := m.check(o); err != nil {
		return Money{}, err
	}
	return Money{amount: m.amount.Add(o.amount), currency: m.currency}, nil
}

// Sub returns m - o, under the same rule as [Money.Add].
func (m Money) Sub(o Money) (Money, error) {
	if err := m.check(o); err != nil {
		return Money{}, err
	}
	return Money{amount: m.amount.Sub(o.amount), currency: m.currency}, nil
}

// Cmp compares two amounts of the same currency numerically. Different
// currencies have no order and return [ErrCurrencyMismatch].
func (m Money) Cmp(o Money) (int, error) {
	if err := m.check(o); err != nil {
		return 0, err
	}
	return m.amount.Cmp(o.amount), nil
}

// Equal reports whether m and o have the same currency and numerically
// equal amounts (1.5 EUR equals 1.50 EUR).
func (m Money) Equal(o Money) bool {
	return m.currency == o.currency && m.amount.Equal(o.amount)
}

// Identical reports whether m and o have the same currency, amount and scale.
func (m Money) Identical(o Money) bool {
	return m.currency == o.currency && m.amount.Identical(o.amount)
}

// String formats m exactly as "<amount> <code>", for logs and tests: the
// amount keeps its own scale and is never rounded. Use [Money.ForDisplay]
// for what a person reads.
func (m Money) String() string {
	return m.amount.String() + " " + m.currency.String()
}

// ForDisplay returns the amount at the currency's ISO 4217 minor units, the
// display precision: padded with zeros when the amount has fewer digits, and
// rounded half-even when it has more (decision D5). rounded reports whether
// digits were dropped, so a view can mark the figure as approximate. A
// currency without known minor units keeps the amount's own scale.
//
// Grouping, symbols and locale are the UI's job; this only fixes the digits.
func (m Money) ForDisplay() (amount Decimal, rounded bool) {
	digits, ok := m.currency.MinorUnits()
	if !ok {
		return m.amount, false
	}
	r := m.amount.RoundHalfEven(digits)
	return r, !r.Equal(m.amount)
}

type moneyJSON struct {
	Amount   Decimal  `json:"amount"`
	Currency Currency `json:"currency"`
}

// MarshalJSON encodes {"amount":"12.50","currency":"EUR"}. The amount is a
// JSON string with its exact scale, never a JSON number.
func (m Money) MarshalJSON() ([]byte, error) {
	if m.currency.IsZero() {
		return nil, ErrNoCurrency
	}
	return json.Marshal(moneyJSON{Amount: m.amount, Currency: m.currency})
}

// UnmarshalJSON decodes the form [Money.MarshalJSON] writes. A JSON number as
// the amount, an unknown field or a missing field is an error.
func (m *Money) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("money: %w", err)
	}
	for k := range raw {
		if k != "amount" && k != "currency" {
			return fmt.Errorf("money: unknown field %q", k)
		}
	}
	a, okA := raw["amount"]
	c, okC := raw["currency"]
	if !okA || !okC {
		return errors.New("money: amount and currency are both required")
	}
	if bytes.Equal(bytes.TrimSpace(a), []byte("null")) || bytes.Equal(bytes.TrimSpace(c), []byte("null")) {
		return errors.New("money: amount and currency must not be null")
	}
	var v moneyJSON
	if err := json.Unmarshal(a, &v.Amount); err != nil {
		return fmt.Errorf("money: amount must be a decimal string: %w", err)
	}
	if err := json.Unmarshal(c, &v.Currency); err != nil {
		return fmt.Errorf("money: currency: %w", err)
	}
	*m = Money{amount: v.Amount, currency: v.Currency}
	return nil
}
