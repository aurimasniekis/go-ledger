package calendar

import (
	"errors"
	"testing"
	"time"
	_ "time/tzdata" // zones resolve without a host zoneinfo database
)

func TestLoadZone(t *testing.T) {
	z, err := LoadZone("Europe/Vilnius")
	if err != nil || z.Name() != "Europe/Vilnius" {
		t.Fatalf("LoadZone = %v, %v", z.Name(), err)
	}
	for _, bad := range []string{"", "Local", "Mars/Base", "../etc/passwd"} {
		if _, err := LoadZone(bad); !errors.Is(err, ErrInvalidZone) {
			t.Errorf("LoadZone(%q) = %v, want ErrInvalidZone", bad, err)
		}
	}
}

func TestDateOfDependsOnReportingZone(t *testing.T) {
	// 22:30 UTC on 31 January is already 1 February in Vilnius (UTC+2) and
	// still 31 January in New York.
	instant := time.Date(2026, time.January, 31, 22, 30, 0, 0, time.UTC)
	for zone, want := range map[string]string{
		"UTC":                "2026-01-31",
		"Europe/Vilnius":     "2026-02-01",
		"America/New_York":   "2026-01-31",
		"Pacific/Kiritimati": "2026-02-01",
	} {
		if got := MustLoadZone(zone).DateOf(instant).String(); got != want {
			t.Errorf("%s: DateOf = %s, want %s", zone, got, want)
		}
	}
	if got := MustLoadZone("Europe/Vilnius").Today(instant); got.YearMonth().String() != "2026-02" {
		t.Errorf("Today's month = %s", got.YearMonth())
	}
}

func TestDayRangeAcrossDaylightSaving(t *testing.T) {
	z := MustLoadZone("Europe/Vilnius")
	for _, tc := range []struct {
		day   Date
		hours time.Duration
		start string
	}{
		{MustDate(2026, time.March, 29), 23, "2026-03-28T22:00:00Z"},   // spring forward
		{MustDate(2026, time.October, 25), 25, "2026-10-24T21:00:00Z"}, // fall back
		{MustDate(2026, time.June, 1), 24, "2026-05-31T21:00:00Z"},
	} {
		start, end := z.DayRange(tc.day)
		if end.Sub(start) != tc.hours*time.Hour || start.Format(time.RFC3339) != tc.start {
			t.Errorf("%s: [%s, %s) is %s", tc.day, start.Format(time.RFC3339), end.Format(time.RFC3339), end.Sub(start))
		}
		if start.Location() != time.UTC {
			t.Errorf("StartOf must return UTC, got %s", start.Location())
		}
		if z.DateOf(start) != tc.day || z.DateOf(end.Add(-time.Nanosecond)) != tc.day || z.DateOf(end) != tc.day.AddDays(1) {
			t.Errorf("%s: range does not map back to the day", tc.day)
		}
	}
}

func TestStartOfWhenMidnightIsSkipped(t *testing.T) {
	// Chile moves clocks from 24:00 to 01:00 on the first Sunday of
	// September, so 2024-09-08 has no midnight.
	z := MustLoadZone("America/Santiago")
	d := MustDate(2024, time.September, 8)
	start := z.StartOf(d)
	if z.DateOf(start) != d {
		t.Fatalf("StartOf(%s) = %s, which is %s in the zone", d, start, z.DateOf(start))
	}
	if z.DateOf(start.Add(-time.Nanosecond)) != d.AddDays(-1) {
		t.Fatalf("StartOf(%s) = %s is not the first instant of the day", d, start)
	}
}

func TestMonthRange(t *testing.T) {
	z := MustLoadZone("Europe/Vilnius")
	start, end := z.MonthRange(MustDate(2026, time.March, 10).YearMonth())
	if start.Format(time.RFC3339) != "2026-02-28T22:00:00Z" || end.Format(time.RFC3339) != "2026-03-31T21:00:00Z" {
		t.Fatalf("MonthRange = [%s, %s)", start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
}

func TestZeroZonePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("using the zero Zone must panic, not fall back to a host zone")
		}
	}()
	var z Zone
	z.DateOf(time.Now())
}

func TestHostZoneName(t *testing.T) {
	for _, tc := range []struct {
		tz, link, file, want string
		ok                   bool
	}{
		{"Europe/Vilnius", "", "", "Europe/Vilnius", true},
		{":Europe/Vilnius", "", "", "Europe/Vilnius", true},
		{"", "/usr/share/zoneinfo/America/New_York", "", "America/New_York", true},
		{"", "../usr/share/zoneinfo/Asia/Tokyo", "", "Asia/Tokyo", true},
		{"", "", "Europe/Berlin\n", "Europe/Berlin", true},
		{"Mars/Base", "/var/db/timezone/zoneinfo/Europe/Paris", "", "Europe/Paris", true},
		{"/etc/localtime", "", "", "", false},
		{"", "", "", "", false},
		{"Local", "/etc/somewhere", "garbage", "", false},
	} {
		got, ok := hostZoneName(tc.tz, tc.link, tc.file)
		if got != tc.want || ok != tc.ok {
			t.Errorf("hostZoneName(%q, %q, %q) = %q, %v; want %q, %v", tc.tz, tc.link, tc.file, got, ok, tc.want, tc.ok)
		}
	}
}
