// Package sqlvalue encodes Ledger's money and calendar primitives for SQLite.
//
// Every value is stored as TEXT in its exact canonical form, so a round trip
// through the database gives back an identical value:
//
//   - an amount as the decimal string with its own scale ("-12.50"),
//   - a currency as its ISO 4217 code ("EUR"),
//   - a date as "YYYY-MM-DD".
//
// Amount columns are TEXT, never INTEGER, REAL or NUMERIC: SQLite's numeric
// affinities and its SUM/total() functions go through binary floating point
// and would drop the scale. Arithmetic on amounts happens in Go with
// internal/domain/money, never in SQL. Tables should be STRICT and carry the
// CHECK expressions this package returns. See ADR-0005.
//
// The scanners accept only text: an INTEGER or REAL coming back from a column
// means the schema is wrong, and they return an error instead of converting.
// Write amounts only through [Amount]: even a STRICT TEXT column converts a
// numeric argument to text, so the column cannot stop a float written by
// mistake; the Valuer and the float ban (ADR-0005) do.
package sqlvalue

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// AmountCheck returns the CHECK expression for an amount column: non-empty
// text of at most 1+MaxDigits+1 characters made of digits, at most one '-'
// in front and at most one '.', at most MaxDigits digits, no leading zeros
// ("007") and no negative zero ("-0.00"), the canonical form the Valuer
// writes. The Go scanner validates fully on read.
func AmountCheck(column string) string {
	return fmt.Sprintf("length(%[1]s) BETWEEN 1 AND %[2]d"+
		" AND length(replace(replace(%[1]s, '-', ''), '.', '')) BETWEEN 1 AND %[3]d"+
		" AND %[1]s NOT GLOB '*[^0-9.-]*'"+
		" AND %[1]s NOT GLOB '0[0-9]*' AND %[1]s NOT GLOB '-0[0-9]*'"+
		" AND replace(replace(%[1]s, '0', ''), '.', '') <> '-'"+
		" AND %[1]s NOT GLOB '?*-*'"+
		" AND %[1]s NOT GLOB '*.*.*'"+
		" AND %[1]s NOT GLOB '*.' AND %[1]s NOT GLOB '.*' AND %[1]s NOT GLOB '-.*'"+
		" AND %[1]s GLOB '*[0-9]*'",
		column, money.MaxDigits+2, money.MaxDigits)
}

// CurrencyCheck returns the CHECK expression for a currency column: three
// upper-case ASCII letters.
func CurrencyCheck(column string) string {
	return fmt.Sprintf("length(%[1]s) = 3 AND %[1]s GLOB '[A-Z][A-Z][A-Z]'", column)
}

// DateCheck returns the CHECK expression for a date column: "YYYY-MM-DD"
// naming a day that exists (SQLite's date() normalises 2026-02-30 away).
func DateCheck(column string) string {
	return fmt.Sprintf("length(%[1]s) = 10 AND %[1]s GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND date(%[1]s) = %[1]s", column)
}

func text(src any, what string) (string, error) {
	switch v := src.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return "", fmt.Errorf("sqlvalue: cannot scan %T into %s; the column must be TEXT", src, what)
	}
}

// Amount is a [money.Decimal] as a query argument or Scan destination.
type Amount struct {
	money.Decimal
}

// Value implements driver.Valuer. It refuses a scale above money.MaxScale and
// more than money.MaxDigits digits, which [money.ParseDecimal] could not read
// back.
func (a Amount) Value() (driver.Value, error) {
	if a.Scale() > money.MaxScale {
		return nil, fmt.Errorf("sqlvalue: amount scale %d exceeds %d; round it before storing", a.Scale(), money.MaxScale)
	}
	s := a.String()
	digits := len(s) - strings.Count(s, "-") - strings.Count(s, ".")
	if digits > money.MaxDigits {
		return nil, fmt.Errorf("sqlvalue: amount has %d digits, more than %d; it could not be read back", digits, money.MaxDigits)
	}
	return s, nil
}

// Scan implements sql.Scanner. It accepts only decimal text.
func (a *Amount) Scan(src any) error {
	s, err := text(src, "an amount")
	if err != nil {
		return err
	}
	d, err := money.ParseDecimal(s)
	if err != nil {
		return fmt.Errorf("sqlvalue: stored amount: %w", err)
	}
	a.Decimal = d
	return nil
}

// Currency is a [money.Currency] as a query argument or Scan destination.
type Currency struct {
	money.Currency
}

// Value implements driver.Valuer. The zero currency is an error, not NULL.
func (c Currency) Value() (driver.Value, error) {
	if c.IsZero() {
		return nil, money.ErrNoCurrency
	}
	return c.Code(), nil
}

// Scan implements sql.Scanner.
func (c *Currency) Scan(src any) error {
	s, err := text(src, "a currency")
	if err != nil {
		return err
	}
	v, err := money.ParseCurrency(s)
	if err != nil {
		return fmt.Errorf("sqlvalue: stored currency: %w", err)
	}
	c.Currency = v
	return nil
}

// Date is a [calendar.Date] as a query argument or Scan destination.
type Date struct {
	calendar.Date
}

// Value implements driver.Valuer. The zero date is an error, not NULL.
func (d Date) Value() (driver.Value, error) {
	if d.IsZero() {
		return nil, fmt.Errorf("sqlvalue: zero date")
	}
	return d.String(), nil
}

// Scan implements sql.Scanner.
func (d *Date) Scan(src any) error {
	s, err := text(src, "a date")
	if err != nil {
		return err
	}
	v, err := calendar.ParseDate(s)
	if err != nil {
		return fmt.Errorf("sqlvalue: stored date: %w", err)
	}
	d.Date = v
	return nil
}

// Money returns the Money for an amount and currency read from two columns.
func Money(a Amount, c Currency) (money.Money, error) {
	return money.New(a.Decimal, c.Currency)
}
