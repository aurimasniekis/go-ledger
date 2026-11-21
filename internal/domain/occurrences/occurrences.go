// Package occurrences derives dated occurrences from recurring commitment
// definitions (task C2, ADR-0027).
//
// An occurrence is one due date of a commitment: the month it belongs to, the
// calendar date it falls due in the reporting time zone (decision D5) and the
// exact amount due then. Occurrences are derived data: they are generated from
// a definition's terms (its amount, schedule spec, start and end dates) and
// stored apart from the definition (ADR-0024), from bank facts (ADR-0013) and
// from payment evidence (task C3), so a later edit of the definition never
// rewrites what the bank reported or what was paid.
//
// The calendar rules are those of decision D6 as built into schedule spec
// version 1: a day past the end of a month falls on its last day, and an
// annual February 29 falls on February 28 in common years. An edit applies
// from an effective date forward; it regenerates only occurrences that are
// not paid and fall due on or after that date, and never touches paid ones
// (see [PlanEdit]).
package occurrences

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// MaxMonthsAhead bounds how far after the current reporting month occurrences
// are materialised: a month view or an effective date later than this is
// refused, so a request can never make the server store decades of rows.
// (Proposed in ADR-0027; not an operator decision.)
const MaxMonthsAhead = 60

// Terms are the parts of a commitment definition that decide its
// occurrences. The name, kind, priority and notes do not.
type Terms struct {
	Amount   money.Money
	Schedule commitments.Schedule
	Start    calendar.Date
	End      *calendar.Date // inclusive; nil for open-ended
}

// TermsOf returns the occurrence terms of a definition.
func TermsOf(f commitments.Fields) Terms {
	return Terms{Amount: f.Amount, Schedule: f.Schedule, Start: f.StartDate, End: f.EndDate}
}

// Equal reports whether t and o generate the same occurrences. Amounts must be
// identical, scale included: "12.5" and "12.50" are different source values.
func (t Terms) Equal(o Terms) bool {
	if !t.Amount.Identical(o.Amount) || t.Schedule != o.Schedule || t.Start != o.Start {
		return false
	}
	if t.End == nil || o.End == nil {
		return t.End == o.End
	}
	return *t.End == *o.End
}

// FirstMonth is the first month that can hold an occurrence.
func (t Terms) FirstMonth() calendar.YearMonth { return t.Start.YearMonth() }

// monthIndex counts months from year 0, for differences between months.
func monthIndex(m calendar.YearMonth) int { return m.Year()*12 + int(m.Month()-1) }

// DueIn returns the due date the terms schedule in month m, if any.
//
//   - Monthly (every Interval months on Day): the months counted are those of
//     the start date, so with Interval 3 and a January start the schedule
//     falls in January, April, July and October.
//   - Annual (Month and Day): once a year in Month.
//   - Day past the end of the month: the month's last day (D6). An annual
//     February 29 is February 28 in common years (D6).
//   - First occurrence: a scheduled date before the start date is not an
//     occurrence. With a start of January 20 and day 15, the first monthly
//     occurrence is February 15 (April 15 with Interval 3); the start month
//     holds one only when its scheduled date is on or after the start date.
//   - The end date is inclusive: a date after it is not an occurrence.
func (t Terms) DueIn(m calendar.YearMonth) (calendar.Date, bool) {
	s := t.Schedule
	switch s.Frequency {
	case commitments.FrequencyMonthly:
		diff := monthIndex(m) - monthIndex(t.Start.YearMonth())
		if s.Interval < 1 || diff < 0 || diff%s.Interval != 0 {
			return calendar.Date{}, false
		}
	case commitments.FrequencyAnnual:
		if m.Month() != s.Month {
			return calendar.Date{}, false
		}
	default:
		return calendar.Date{}, false
	}
	d, err := calendar.ClampedDate(m.Year(), m.Month(), s.Day)
	if err != nil || d.Before(t.Start) || (t.End != nil && d.After(*t.End)) {
		return calendar.Date{}, false
	}
	return d, true
}

// Planned is an occurrence the terms schedule.
type Planned struct {
	Month   calendar.YearMonth
	DueDate calendar.Date
	Amount  money.Money
}

// Generate returns the occurrences the terms schedule in the months from
// through to, both inclusive, in date order.
func Generate(t Terms, from, to calendar.YearMonth) []Planned {
	var out []Planned
	if from.Compare(t.FirstMonth()) < 0 {
		from = t.FirstMonth()
	}
	for m := from; m.Compare(to) <= 0; {
		if d, ok := t.DueIn(m); ok {
			out = append(out, Planned{Month: m, DueDate: d, Amount: t.Amount})
		}
		next, err := m.AddMonths(1)
		if err != nil {
			break
		}
		m = next
	}
	return out
}

// Occurrence is a stored occurrence. It carries no payment status: status is
// derived from payment evidence (task C3, decision D7).
type Occurrence struct {
	ID           string
	CommitmentID string
	Month        calendar.YearMonth
	DueDate      calendar.Date
	Amount       money.Money
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Existing is a stored occurrence as edit planning sees it: Paid means payment
// evidence refers to it (a manual payment record or a confirmed match, task
// C3), which makes it immutable.
type Existing struct {
	Occurrence
	Paid bool
}

// Update rewrites an unpaid occurrence in place, keeping its identifier.
type Update struct {
	ID string
	Planned
}

// Plan is what an edit does to the stored occurrences.
type Plan struct {
	Create []Planned
	Update []Update
	Remove []string // IDs of unpaid occurrences the new terms no longer schedule
	// KeptPaid counts paid occurrences on or after the effective date left
	// unchanged; KeptHistory counts unpaid ones before it.
	KeptPaid    int
	KeptHistory int
}

// Empty reports whether the plan changes nothing.
func (p Plan) Empty() bool { return len(p.Create) == 0 && len(p.Update) == 0 && len(p.Remove) == 0 }

// PlanEdit decides how new terms, effective from effective, reach the stored
// occurrences of the months from effective's month through through (the
// last month materialised). existing holds the stored occurrences of those
// months, at most one per month. Months after through are not stored; they
// follow the current terms when they are materialised.
//
// Each month is one slot (D6 edits option a):
//
//   - a paid occurrence is never changed, whatever the new terms say;
//   - an unpaid occurrence due before the effective date is history and stays;
//   - an unpaid occurrence due on or after it is rewritten in place to the new
//     terms' date and amount (even when the new date is before the effective
//     date, so moving a due day earlier does not drop that month), or removed
//     when the new terms schedule nothing that month;
//   - an empty month gets the new terms' occurrence when it falls due on or
//     after the effective date.
func PlanEdit(existing []Existing, through calendar.YearMonth, next Terms, effective calendar.Date) Plan {
	byMonth := make(map[calendar.YearMonth]Existing, len(existing))
	for _, e := range existing {
		byMonth[e.Month] = e
	}
	var p Plan
	for m := effective.YearMonth(); m.Compare(through) <= 0; {
		due, scheduled := next.DueIn(m)
		e, has := byMonth[m]
		switch {
		case has && e.Paid:
			p.KeptPaid++
		case has && e.DueDate.Before(effective):
			p.KeptHistory++
		case has && scheduled:
			if e.DueDate != due || !e.Amount.Identical(next.Amount) {
				p.Update = append(p.Update, Update{ID: e.ID, Planned: Planned{Month: m, DueDate: due, Amount: next.Amount}})
			}
		case has:
			p.Remove = append(p.Remove, e.ID)
		case scheduled && !due.Before(effective):
			p.Create = append(p.Create, Planned{Month: m, DueDate: due, Amount: next.Amount})
		}
		nm, err := m.AddMonths(1)
		if err != nil {
			break
		}
		m = nm
	}
	return p
}

// HorizonLimit returns the last month that may be materialised when today is
// today: [MaxMonthsAhead] months after today's month.
func HorizonLimit(today calendar.Date) calendar.YearMonth {
	m, err := today.YearMonth().AddMonths(MaxMonthsAhead)
	if err != nil {
		last, _ := calendar.NewYearMonth(9999, time.December)
		return last
	}
	return m
}

// ErrBeyondHorizon means a month or an effective date is later than
// [HorizonLimit] allows.
var ErrBeyondHorizon = errors.New("occurrences: month is beyond the materialisation horizon")

// TargetType is the audit target type of regeneration entries: the
// commitment whose occurrences changed.
const TargetType = commitments.TargetType

// RegeneratedEvent is the audit event for applying changed terms to stored
// occurrences: the effective date and how many occurrences each rule hit.
// It carries no amounts or names.
func RegeneratedEvent(actor audit.Actor, commitmentID string, effective calendar.Date, p Plan, at time.Time) audit.Event {
	return audit.Event{Action: audit.OccurrencesRegenerated, Actor: actor, TargetType: TargetType, TargetID: commitmentID, At: at,
		Details: map[string]string{
			"effective_from": effective.String(),
			"created":        strconv.Itoa(len(p.Create)),
			"updated":        strconv.Itoa(len(p.Update)),
			"removed":        strconv.Itoa(len(p.Remove)),
			"kept_paid":      strconv.Itoa(p.KeptPaid),
		}}
}

// ErrCursorGone means the occurrence a page cursor points after no longer
// exists (an edit removed it); the client starts again from the first page.
var ErrCursorGone = errors.New("occurrences: the cursor's occurrence no longer exists")

// ListFilter selects occurrences of one month. Limit is the page size;
// implementations return up to Limit+1 occurrences after After, ordered by
// due date, then ID.
type ListFilter struct {
	Month         calendar.YearMonth
	CommitmentIDs []string
	After         string
	Limit         int
}

// Reader is the read side of stored occurrences; internal/store/occurrences
// implements it. Listing a month first materialises every commitment through
// that month (occurrences are derived data, so this is not an audited change).
type Reader interface {
	List(ctx context.Context, f ListFilter) ([]Occurrence, error)
	// Get returns one occurrence or resource.ErrNotFound.
	Get(ctx context.Context, id string) (Occurrence, error)
}
