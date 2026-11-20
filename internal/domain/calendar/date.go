package calendar

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidDate is wrapped by every date and month parsing error.
var ErrInvalidDate = errors.New("calendar: invalid date")

// Date is a calendar date without a time of day or a zone, such as a due
// date. The zero value is not a valid date ([Date.IsZero]). Dates are
// comparable with ==.
type Date struct {
	year  int
	month time.Month
	day   int
}

// DateLayout is the ISO 8601 form dates are parsed from and written in.
const DateLayout = "2006-01-02"

// NewDate returns the date, or an error if it does not exist (30 February)
// or the year is outside 1..9999.
func NewDate(year int, month time.Month, day int) (Date, error) {
	if year < 1 || year > 9999 || month < time.January || month > time.December ||
		day < 1 || day > DaysIn(year, month) {
		return Date{}, fmt.Errorf("%w: %04d-%02d-%02d does not exist", ErrInvalidDate, year, int(month), day)
	}
	return Date{year: year, month: month, day: day}, nil
}

// MustDate is [NewDate] that panics on error, for constants and tests.
func MustDate(year int, month time.Month, day int) Date {
	d, err := NewDate(year, month, day)
	if err != nil {
		panic(err)
	}
	return d
}

// ParseDate parses "YYYY-MM-DD" exactly (no time, no zone).
func ParseDate(s string) (Date, error) {
	if len(s) != len(DateLayout) {
		return Date{}, fmt.Errorf("%w: %q is not YYYY-MM-DD", ErrInvalidDate, s)
	}
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("%w: %q is not YYYY-MM-DD", ErrInvalidDate, s)
	}
	return NewDate(t.Year(), t.Month(), t.Day())
}

// DaysIn returns the number of days in the month.
func DaysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ClampedDate returns day N of the month, or the last day if the month is
// shorter (decision D6): day 31 of April is 30 April, day 29 of February
// 2027 is 28 February.
func ClampedDate(year int, month time.Month, day int) (Date, error) {
	if day < 1 {
		return Date{}, fmt.Errorf("%w: day %d", ErrInvalidDate, day)
	}
	return NewDate(year, month, min(day, DaysIn(year, month)))
}

// Year returns the year.
func (d Date) Year() int { return d.year }

// Month returns the month.
func (d Date) Month() time.Month { return d.month }

// Day returns the day of the month.
func (d Date) Day() int { return d.day }

// IsZero reports whether d is the zero value.
func (d Date) IsZero() bool { return d == Date{} }

// YearMonth returns the month d falls in.
func (d Date) YearMonth() YearMonth { return YearMonth{year: d.year, month: d.month} }

// Weekday returns the day of the week.
func (d Date) Weekday() time.Weekday { return d.midnightUTC().Weekday() }

func (d Date) midnightUTC() time.Time {
	return time.Date(d.year, d.month, d.day, 0, 0, 0, 0, time.UTC)
}

// maxDaySpan is the number of days between 0001-01-01 and 9999-12-31.
const maxDaySpan = 3652059

// AddDays returns d plus n days (n may be negative). It returns
// ErrInvalidDate if the result falls outside years 1..9999.
func (d Date) AddDays(n int) (Date, error) {
	// Years 1..9999 span 3,652,059 days; anything larger cannot land in range
	// and would overflow inside time.AddDate.
	if n > maxDaySpan || n < -maxDaySpan {
		return Date{}, fmt.Errorf("%w: %d days out of range", ErrInvalidDate, n)
	}
	t := d.midnightUTC().AddDate(0, 0, n)
	return NewDate(t.Year(), t.Month(), t.Day())
}

// AddMonthsClamped returns the date n months later (or earlier), with the day
// clamped to the end of the target month (decision D6). It does not
// accumulate: 31 Jan + 1 month is 28/29 Feb, but 31 Jan + 2 months is 31 Mar.
// Schedules should therefore always step from the anchor date. It returns
// ErrInvalidDate if the result falls outside years 1..9999.
func (d Date) AddMonthsClamped(n int) (Date, error) {
	ym, err := d.YearMonth().AddMonths(n)
	if err != nil {
		return Date{}, err
	}
	return ClampedDate(ym.year, ym.month, d.day)
}

// Compare returns -1, 0 or +1 as d is before, equal to or after o.
func (d Date) Compare(o Date) int {
	switch {
	case d.year != o.year:
		return cmp3(d.year, o.year)
	case d.month != o.month:
		return cmp3(int(d.month), int(o.month))
	default:
		return cmp3(d.day, o.day)
	}
}

func cmp3(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Before reports whether d is before o.
func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }

// After reports whether d is after o.
func (d Date) After(o Date) bool { return d.Compare(o) > 0 }

// DaysUntil returns the number of days from d to o (negative if o is
// earlier).
func (d Date) DaysUntil(o Date) int {
	return int(o.midnightUTC().Sub(d.midnightUTC()) / (24 * time.Hour))
}

// String returns "YYYY-MM-DD", or "" for the zero value.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.year, int(d.month), d.day)
}

// MarshalText implements encoding.TextMarshaler.
func (d Date) MarshalText() ([]byte, error) {
	if d.IsZero() {
		return nil, fmt.Errorf("%w: zero date", ErrInvalidDate)
	}
	return []byte(d.String()), nil
}

// UnmarshalJSON implements json.Unmarshaler: only a JSON string is accepted,
// so null is an error instead of silently leaving the zero date.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fmt.Errorf("%w: a date must be a JSON string", ErrInvalidDate)
	}
	return d.UnmarshalText([]byte(s))
}

// UnmarshalText implements encoding.TextUnmarshaler with [ParseDate].
func (d *Date) UnmarshalText(b []byte) error {
	v, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// YearMonth is a calendar month, such as a reporting month. The zero value
// is not valid. YearMonths are comparable with ==.
type YearMonth struct {
	year  int
	month time.Month
}

// NewYearMonth returns the month, or an error outside 0001-01..9999-12.
func NewYearMonth(year int, month time.Month) (YearMonth, error) {
	if year < 1 || year > 9999 || month < time.January || month > time.December {
		return YearMonth{}, fmt.Errorf("%w: month %04d-%02d", ErrInvalidDate, year, int(month))
	}
	return YearMonth{year: year, month: month}, nil
}

// ParseYearMonth parses "YYYY-MM".
func ParseYearMonth(s string) (YearMonth, error) {
	if len(s) != len("2006-01") {
		return YearMonth{}, fmt.Errorf("%w: %q is not YYYY-MM", ErrInvalidDate, s)
	}
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return YearMonth{}, fmt.Errorf("%w: %q is not YYYY-MM", ErrInvalidDate, s)
	}
	return NewYearMonth(t.Year(), t.Month())
}

// Year returns the year.
func (m YearMonth) Year() int { return m.year }

// Month returns the month.
func (m YearMonth) Month() time.Month { return m.month }

// IsZero reports whether m is the zero value.
func (m YearMonth) IsZero() bool { return m == YearMonth{} }

// First returns the first day of the month.
func (m YearMonth) First() Date { return Date{year: m.year, month: m.month, day: 1} }

// Last returns the last day of the month.
func (m YearMonth) Last() Date {
	return Date{year: m.year, month: m.month, day: DaysIn(m.year, m.month)}
}

// Contains reports whether d falls in m.
func (m YearMonth) Contains(d Date) bool { return d.YearMonth() == m }

// AddMonths returns the month n months later (or earlier). It returns
// ErrInvalidDate if the result falls outside years 1..9999.
func (m YearMonth) AddMonths(n int) (YearMonth, error) {
	idx := m.year*12 + int(m.month-1) + n
	if idx < 12 || idx >= 10000*12 { // also guards against int overflow of n
		return YearMonth{}, fmt.Errorf("%w: %d months from %s is outside years 1..9999", ErrInvalidDate, n, m)
	}
	return YearMonth{year: idx / 12, month: time.Month(idx%12 + 1)}, nil
}

// Compare returns -1, 0 or +1 as m is before, equal to or after o.
func (m YearMonth) Compare(o YearMonth) int {
	if m.year != o.year {
		return cmp3(m.year, o.year)
	}
	return cmp3(int(m.month), int(o.month))
}

// String returns "YYYY-MM", or "" for the zero value.
func (m YearMonth) String() string {
	if m.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d", m.year, int(m.month))
}

// MarshalText implements encoding.TextMarshaler.
func (m YearMonth) MarshalText() ([]byte, error) {
	if m.IsZero() {
		return nil, fmt.Errorf("%w: zero month", ErrInvalidDate)
	}
	return []byte(m.String()), nil
}

// UnmarshalJSON implements json.Unmarshaler: only a JSON string is accepted,
// so null is an error instead of silently leaving the zero month.
func (m *YearMonth) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fmt.Errorf("%w: a month must be a JSON string", ErrInvalidDate)
	}
	return m.UnmarshalText([]byte(s))
}

// UnmarshalText implements encoding.TextUnmarshaler with [ParseYearMonth].
func (m *YearMonth) UnmarshalText(b []byte) error {
	v, err := ParseYearMonth(string(b))
	if err != nil {
		return err
	}
	*m = v
	return nil
}
