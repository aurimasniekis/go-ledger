package commitments

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

// ScheduleVersion is the version of the schedule spec this package writes.
// Every stored and transmitted schedule names its version, so a later spec
// (for example weekly rules) can be added as version 2 without reinterpreting
// version 1 data.
const ScheduleVersion = 1

// Frequency is how often a version 1 schedule repeats.
type Frequency string

// Frequencies of schedule spec version 1 (decision D6, rules option a).
const (
	// FrequencyMonthly repeats every Interval months on Day. The months
	// counted are those of the start date: with Interval 3 and a start date
	// in January, the schedule falls in January, April, July and October.
	FrequencyMonthly Frequency = "monthly"
	// FrequencyAnnual repeats once a year on Month and Day.
	FrequencyAnnual Frequency = "annual"
)

// Frequencies returns every frequency of schedule spec version 1.
func Frequencies() []Frequency { return []Frequency{FrequencyMonthly, FrequencyAnnual} }

// MaxInterval is the largest monthly interval: every ten years.
const MaxInterval = 120

// Schedule is a recurrence rule, schedule spec version 1. It says when a
// commitment falls due; it does not hold dated occurrences, which are derived
// from it (task C2) and stored apart from the definition.
//
// The calendar rules of decision D6 belong to the spec: a Day past the end of
// a month falls on that month's last day (Day 31 is April 30), and an annual
// February 29 falls on February 28 in years that are not leap years.
type Schedule struct {
	// Version is the spec version; [ScheduleVersion] for every valid value.
	Version   int
	Frequency Frequency
	// Interval is the number of months between due dates (monthly only,
	// 1 to [MaxInterval]); zero for annual schedules.
	Interval int
	// Day is the day of the month, 1 to 31 (monthly) or 1 to the last day of
	// Month in a leap year (annual).
	Day int
	// Month is the month of an annual schedule; zero for monthly schedules.
	Month time.Month
}

// Monthly returns a monthly schedule spec, every interval months on day.
func Monthly(interval, day int) Schedule {
	return Schedule{Version: ScheduleVersion, Frequency: FrequencyMonthly, Interval: interval, Day: day}
}

// Annual returns an annual schedule spec on month and day.
func Annual(month time.Month, day int) Schedule {
	return Schedule{Version: ScheduleVersion, Frequency: FrequencyAnnual, Month: month, Day: day}
}

// Validate returns one field error per invalid member, with field names
// under prefix (for example "schedule.day").
func (s Schedule) Validate(prefix string) []FieldError {
	f := func(name string) string { return prefix + "." + name }
	if s.Version != ScheduleVersion {
		return []FieldError{{f("version"), fmt.Sprintf("version must be %d (the only schedule spec version)", ScheduleVersion)}}
	}
	var errs []FieldError
	switch s.Frequency {
	case FrequencyMonthly:
		if s.Interval < 1 || s.Interval > MaxInterval {
			errs = append(errs, FieldError{f("interval"), fmt.Sprintf("interval must be a whole number of months from 1 to %d", MaxInterval)})
		}
		if s.Day < 1 || s.Day > 31 {
			errs = append(errs, FieldError{f("day"), "day must be from 1 to 31; a day past the end of a month falls on its last day"})
		}
		if s.Month != 0 {
			errs = append(errs, FieldError{f("month"), "month applies to annual schedules only"})
		}
	case FrequencyAnnual:
		if s.Month < time.January || s.Month > time.December {
			errs = append(errs, FieldError{f("month"), "month must be from 1 to 12"})
		} else if max := calendar.DaysIn(2024, s.Month); s.Day < 1 || s.Day > max {
			errs = append(errs, FieldError{f("day"), fmt.Sprintf("day must be from 1 to %d for month %d", max, int(s.Month))})
		}
		if s.Interval != 0 {
			errs = append(errs, FieldError{f("interval"), "interval applies to monthly schedules only"})
		}
	default:
		errs = append(errs, FieldError{f("frequency"), "frequency must be one of " + joinNames(Frequencies())})
	}
	return errs
}

// scheduleJSON is the wire and storage form of version 1.
type scheduleJSON struct {
	Version   int       `json:"version"`
	Frequency Frequency `json:"frequency"`
	Interval  int       `json:"interval,omitempty"`
	Month     int       `json:"month,omitempty"`
	Day       int       `json:"day"`
}

// MarshalJSON writes the canonical form: {"version":1,"frequency":"monthly",
// "interval":1,"day":15} or {"version":1,"frequency":"annual","month":2,
// "day":29}. Members that do not apply to the frequency are left out.
func (s Schedule) MarshalJSON() ([]byte, error) {
	return json.Marshal(scheduleJSON{Version: s.Version, Frequency: s.Frequency, Interval: s.Interval, Month: int(s.Month), Day: s.Day})
}

// members of each version 1 frequency; version and frequency are common.
var scheduleMembers = map[Frequency][]string{
	FrequencyMonthly: {"interval", "day"},
	FrequencyAnnual:  {"month", "day"},
}

// ParseSchedule decodes and validates a schedule spec. It reports every
// problem it finds as a field error named under prefix: a missing, unknown
// or mistyped member, an unsupported version or a value out of range.
func ParseSchedule(raw []byte, prefix string) (Schedule, []FieldError) {
	f := func(name string) string { return prefix + "." + name }
	var m map[string]json.RawMessage
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &m) != nil {
		return Schedule{}, []FieldError{{prefix, prefix + " must be an object"}}
	}
	var errs []FieldError
	intMember := func(name string, dst *int) {
		v, ok := m[name]
		if !ok || isNull(v) {
			errs = append(errs, FieldError{f(name), name + " is required"})
			return
		}
		if err := json.Unmarshal(v, dst); err != nil {
			errs = append(errs, FieldError{f(name), name + " must be a whole number"})
		}
	}
	var s Schedule
	intMember("version", &s.Version)
	if len(errs) > 0 {
		return Schedule{}, errs
	}
	if s.Version != ScheduleVersion {
		return Schedule{}, []FieldError{{f("version"), fmt.Sprintf("version must be %d (the only schedule spec version)", ScheduleVersion)}}
	}
	fv, ok := m["frequency"]
	if !ok || isNull(fv) {
		return Schedule{}, []FieldError{{f("frequency"), "frequency is required"}}
	}
	if err := json.Unmarshal(fv, &s.Frequency); err != nil {
		return Schedule{}, []FieldError{{f("frequency"), "frequency must be one of " + joinNames(Frequencies())}}
	}
	members, known := scheduleMembers[s.Frequency]
	if !known {
		return Schedule{}, []FieldError{{f("frequency"), "frequency must be one of " + joinNames(Frequencies())}}
	}
	allowed := map[string]bool{"version": true, "frequency": true}
	for _, name := range members {
		allowed[name] = true
	}
	var unknown []string
	for k := range m {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		errs = append(errs, FieldError{f(k), fmt.Sprintf("%s is not a member of %s %s schedule", k, article(string(s.Frequency)), s.Frequency)})
	}
	for _, name := range members {
		switch name {
		case "interval":
			intMember(name, &s.Interval)
		case "day":
			intMember(name, &s.Day)
		case "month":
			var mo int
			intMember(name, &mo)
			s.Month = time.Month(mo)
		}
	}
	if len(errs) > 0 {
		return Schedule{}, errs
	}
	if errs := s.Validate(prefix); len(errs) > 0 {
		return Schedule{}, errs
	}
	return s, nil
}

// UnmarshalJSON decodes and validates a schedule spec; see [ParseSchedule].
func (s *Schedule) UnmarshalJSON(b []byte) error {
	v, errs := ParseSchedule(b, "schedule")
	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	*s = v
	return nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// article returns the indefinite article for a frequency name.
func article(word string) string {
	if word != "" && strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}
