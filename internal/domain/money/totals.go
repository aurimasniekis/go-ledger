package money

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrMixedCurrencies is matched (errors.Is) by every [MixedCurrencyError].
var ErrMixedCurrencies = errors.New("money: amounts are in more than one currency")

// ErrEmpty is returned for a total over no amounts: without an amount there
// is no currency to give the result.
var ErrEmpty = errors.New("money: no amounts to total")

// MixedCurrencyError is the explicit result of totalling amounts in several
// currencies without a conversion. It lists the currencies, sorted.
type MixedCurrencyError struct {
	Currencies []Currency
}

func (e *MixedCurrencyError) Error() string {
	codes := make([]string, len(e.Currencies))
	for i, c := range e.Currencies {
		codes[i] = c.Code()
	}
	return fmt.Sprintf("money: cannot total amounts in %s without a conversion", strings.Join(codes, ", "))
}

// Is makes errors.Is(err, ErrMixedCurrencies) true.
func (e *MixedCurrencyError) Is(target error) bool { return target == ErrMixedCurrencies }

// Sum totals amounts that must share one currency. Several currencies return
// a [*MixedCurrencyError] (never a converted figure); no amounts return
// [ErrEmpty]. Use [Totals] to keep one total per currency instead.
func Sum(items ...Money) (Money, error) {
	var t Totals
	for _, m := range items {
		if err := t.Add(m); err != nil {
			return Money{}, err
		}
	}
	return t.Single()
}

// Totals keeps one exact total per currency: the primary figure for a set of
// amounts (decision D5). The zero value is empty and ready to use.
type Totals struct {
	by map[Currency]Decimal
}

// Add adds m to its currency's total. It returns [ErrNoCurrency] for a Money
// without a currency.
func (t *Totals) Add(m Money) error {
	if !m.IsValid() {
		return ErrNoCurrency
	}
	if t.by == nil {
		t.by = map[Currency]Decimal{}
	}
	if cur, ok := t.by[m.currency]; ok {
		t.by[m.currency] = cur.Add(m.amount)
	} else {
		t.by[m.currency] = m.amount
	}
	return nil
}

// Len returns the number of currencies.
func (t *Totals) Len() int { return len(t.by) }

// Get returns the total in c, if any amount in c was added.
func (t *Totals) Get(c Currency) (Money, bool) {
	d, ok := t.by[c]
	if !ok {
		return Money{}, false
	}
	return Money{amount: d, currency: c}, true
}

// Currencies returns the currencies present, sorted by code.
func (t *Totals) Currencies() []Currency {
	cs := make([]Currency, 0, len(t.by))
	for c := range t.by {
		cs = append(cs, c)
	}
	slices.SortFunc(cs, func(a, b Currency) int { return strings.Compare(a.code, b.code) })
	return cs
}

// List returns one total per currency, sorted by currency code.
func (t *Totals) List() []Money {
	cs := t.Currencies()
	out := make([]Money, len(cs))
	for i, c := range cs {
		out[i] = Money{amount: t.by[c], currency: c}
	}
	return out
}

// Single returns the total when exactly one currency is present. Several
// currencies return a [*MixedCurrencyError]; none returns [ErrEmpty].
func (t *Totals) Single() (Money, error) {
	switch len(t.by) {
	case 0:
		return Money{}, ErrEmpty
	case 1:
		return t.List()[0], nil
	default:
		return Money{}, &MixedCurrencyError{Currencies: t.Currencies()}
	}
}
