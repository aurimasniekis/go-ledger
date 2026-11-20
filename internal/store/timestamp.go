package store

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// TimeLayout is the stored form of every timestamp: UTC, fixed width, with
// nanoseconds, so text order is time order and a round trip loses nothing.
const TimeLayout = "2006-01-02T15:04:05.000000000Z"

// TimestampCheck returns the CHECK expression a timestamp column carries in a
// migration. It rejects anything that is not [TimeLayout], including the
// driver's default encoding of a time.Time and any offset other than Z.
func TimestampCheck(column string) string {
	return fmt.Sprintf("length(%[1]s) = 30 AND %[1]s GLOB '????-??-??T??:??:??.?????????Z'", column)
}

// FormatTime converts t to UTC and formats it as [TimeLayout].
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// ParseTime parses a stored timestamp. It accepts only [TimeLayout] and
// returns a time in UTC.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: timestamp %q is not in the stored UTC layout: %w", s, err)
	}
	return t, nil
}

// Timestamp is a time.Time that is written and read as a stored UTC
// timestamp. Use it as a query argument or a Scan destination for *_at
// columns; passing a bare time.Time lets the driver choose an encoding that
// the column's CHECK constraint rejects.
type Timestamp struct {
	time.Time
}

// Value implements driver.Valuer.
func (t Timestamp) Value() (driver.Value, error) {
	return FormatTime(t.Time), nil
}

// Scan implements sql.Scanner. It accepts only text in [TimeLayout].
func (t *Timestamp) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("store: cannot scan %T into a timestamp", src)
	}
	parsed, err := ParseTime(s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}
