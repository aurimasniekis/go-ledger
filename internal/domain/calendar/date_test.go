package calendar

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestParseDate(t *testing.T) {
	d, err := ParseDate("2024-02-29")
	if err != nil || d != MustDate(2024, time.February, 29) || d.String() != "2024-02-29" {
		t.Fatalf("ParseDate = %v, %v", d, err)
	}
	for _, bad := range []string{
		"", "2025-02-29", "2026-04-31", "2026-13-01", "2026-00-10", "2026-1-5", "26-01-05",
		"2026-01-05T00:00:00Z", "2026/01/05", " 2026-01-05", "0000-01-01",
	} {
		if _, err := ParseDate(bad); !errors.Is(err, ErrInvalidDate) {
			t.Errorf("ParseDate(%q) = %v, want ErrInvalidDate", bad, err)
		}
	}
}

func TestClampedDate(t *testing.T) {
	for _, tc := range []struct {
		y    int
		m    time.Month
		day  int
		want string
	}{
		{2026, time.January, 31, "2026-01-31"},
		{2026, time.February, 31, "2026-02-28"},
		{2024, time.February, 31, "2024-02-29"},
		{2026, time.April, 31, "2026-04-30"},
		{2027, time.February, 29, "2027-02-28"},
		{2026, time.March, 15, "2026-03-15"},
	} {
		d, err := ClampedDate(tc.y, tc.m, tc.day)
		if err != nil || d.String() != tc.want {
			t.Errorf("ClampedDate(%d, %d, %d) = %s, %v; want %s", tc.y, tc.m, tc.day, d, err, tc.want)
		}
	}
	if _, err := ClampedDate(2026, time.March, 0); err == nil {
		t.Error("day 0 must be rejected")
	}
}

func TestAddMonthsClamped(t *testing.T) {
	jan31 := MustDate(2026, time.January, 31)
	for _, tc := range []struct {
		from Date
		n    int
		want string
	}{
		{jan31, 1, "2026-02-28"},
		{jan31, 2, "2026-03-31"}, // steps from the anchor, never accumulates
		{jan31, 3, "2026-04-30"},
		{jan31, -1, "2025-12-31"},
		{jan31, 25, "2028-02-29"},
		{MustDate(2024, time.February, 29), 12, "2025-02-28"}, // D6: annual Feb 29
		{MustDate(2024, time.February, 29), 48, "2028-02-29"},
		{MustDate(2026, time.December, 15), 1, "2027-01-15"},
		{MustDate(2026, time.January, 15), -13, "2024-12-15"},
	} {
		if got := tc.from.AddMonthsClamped(tc.n); got.String() != tc.want {
			t.Errorf("%s + %d months = %s, want %s", tc.from, tc.n, got, tc.want)
		}
	}
}

func TestDateArithmeticAndOrder(t *testing.T) {
	a := MustDate(2026, time.February, 27)
	if got := a.AddDays(2).String(); got != "2026-03-01" {
		t.Errorf("AddDays = %s", got)
	}
	if got := a.AddDays(-58).String(); got != "2025-12-31" {
		t.Errorf("AddDays(-58) = %s", got)
	}
	b := MustDate(2026, time.March, 29) // a DST change day in Europe; dates do not care
	if a.DaysUntil(b) != 30 || b.DaysUntil(a) != -30 {
		t.Errorf("DaysUntil = %d / %d", a.DaysUntil(b), b.DaysUntil(a))
	}
	if !a.Before(b) || !b.After(a) || a.Compare(a) != 0 || b.Compare(a) != 1 {
		t.Error("order wrong")
	}
	if MustDate(2026, time.November, 20).Weekday() != time.Friday {
		t.Error("weekday wrong")
	}
	var zero Date
	if !zero.IsZero() || zero.String() != "" {
		t.Error("zero date")
	}
}

func TestYearMonth(t *testing.T) {
	m, err := ParseYearMonth("2024-02")
	if err != nil {
		t.Fatal(err)
	}
	if m.First().String() != "2024-02-01" || m.Last().String() != "2024-02-29" {
		t.Errorf("First/Last = %s/%s", m.First(), m.Last())
	}
	if got := m.AddMonths(11).String(); got != "2025-01" {
		t.Errorf("AddMonths(11) = %s", got)
	}
	if got := m.AddMonths(-2).String(); got != "2023-12" {
		t.Errorf("AddMonths(-2) = %s", got)
	}
	if !m.Contains(MustDate(2024, time.February, 10)) || m.Contains(MustDate(2024, time.March, 1)) {
		t.Error("Contains wrong")
	}
	if m.Compare(m.AddMonths(1)) != -1 {
		t.Error("Compare wrong")
	}
	for _, bad := range []string{"2024-2", "2024-13", "2024-02-01", ""} {
		if _, err := ParseYearMonth(bad); err == nil {
			t.Errorf("ParseYearMonth(%q) succeeded", bad)
		}
	}
}

func TestJSON(t *testing.T) {
	type row struct {
		Due   Date      `json:"due"`
		Month YearMonth `json:"month"`
	}
	b, err := json.Marshal(row{MustDate(2026, time.March, 5), MustDate(2026, time.March, 5).YearMonth()})
	if err != nil || string(b) != `{"due":"2026-03-05","month":"2026-03"}` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var r row
	if err := json.Unmarshal(b, &r); err != nil || r.Due.String() != "2026-03-05" || r.Month.String() != "2026-03" {
		t.Fatalf("Unmarshal = %+v, %v", r, err)
	}
	if err := json.Unmarshal([]byte(`{"due":"2026-03-05T10:00:00Z"}`), &r); err == nil {
		t.Fatal("an instant must not parse as a date")
	}
	if _, err := json.Marshal(row{}); err == nil {
		t.Fatal("a zero date must not marshal")
	}
}
