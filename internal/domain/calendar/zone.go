package calendar

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidZone is wrapped by every reporting-zone error.
var ErrInvalidZone = errors.New("calendar: invalid reporting time zone")

// Zone is the reporting time zone: one named IANA zone (decision D5) that
// decides which calendar date an instant belongs to, and so whether a
// payment counts as on time, which month a transaction is reported in, and
// what "today" is. The zero value is not usable; build one with [LoadZone].
type Zone struct {
	loc *time.Location
}

// LoadZone loads a zone by its IANA name ("Europe/Vilnius", "UTC"). The name
// must be explicit: "" and "Local" are rejected, because a zone that depends
// on the host would silently change the meaning of stored dates when the
// host changes. Capture the host's zone name once at setup instead
// ([HostZoneName]).
//
// The binary should import time/tzdata so that names resolve on hosts
// without a zoneinfo database.
func LoadZone(name string) (Zone, error) {
	if name == "" || name == "Local" {
		return Zone{}, fmt.Errorf("%w: a named IANA zone is required, got %q", ErrInvalidZone, name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Zone{}, fmt.Errorf("%w: unknown IANA zone %q", ErrInvalidZone, name)
	}
	return Zone{loc: loc}, nil
}

// MustLoadZone is [LoadZone] that panics on error, for tests.
func MustLoadZone(name string) Zone {
	z, err := LoadZone(name)
	if err != nil {
		panic(err)
	}
	return z
}

// Name returns the IANA name.
func (z Zone) Name() string {
	if z.loc == nil {
		return ""
	}
	return z.loc.String()
}

// IsZero reports whether z is the zero value.
func (z Zone) IsZero() bool { return z.loc == nil }

func (z Zone) location() *time.Location {
	if z.loc == nil {
		panic("calendar: zero Zone used; load one with LoadZone")
	}
	return z.loc
}

// DateOf returns the calendar date the instant t falls on in the zone.
func (z Zone) DateOf(t time.Time) Date {
	l := t.In(z.location())
	return Date{year: l.Year(), month: l.Month(), day: l.Day()}
}

// Today returns the date of now in the zone. Pass the clock's time so that
// tests stay deterministic.
func (z Zone) Today(now time.Time) Date { return z.DateOf(now) }

// StartOf returns the UTC instant at which d begins in the zone. On a day
// whose midnight is skipped by a daylight-saving change, it is the first
// instant that exists on that day.
func (z Zone) StartOf(d Date) time.Time {
	loc := z.location()
	t := time.Date(d.year, d.month, d.day, 0, 0, 0, 0, loc)
	if t.Day() != d.day { // midnight did not exist and Go moved back a day
		for t.Day() != d.day {
			t = t.Add(time.Hour)
		}
		t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
	}
	return t.UTC()
}

// DayRange returns the half-open UTC interval [start, end) that d covers in
// the zone. It is 23 or 25 hours long on daylight-saving change days.
func (z Zone) DayRange(d Date) (start, end time.Time) {
	return z.StartOf(d), z.StartOf(d.AddDays(1))
}

// MonthRange returns the half-open UTC interval [start, end) that m covers in
// the zone.
func (z Zone) MonthRange(m YearMonth) (start, end time.Time) {
	return z.StartOf(m.First()), z.StartOf(m.AddMonths(1).First())
}
