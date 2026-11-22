package commitments

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
)

// LeewayKind says what the user knows about the grace a creditor allows after
// a due date (decision D14, task DF2, ADR-0052). Leeway is user-entered only:
// nothing here is inferred from payment history, from bank data or from model
// output, and there is no default length.
//
// A commitment with no leeway recorded (nil) is a fourth, distinct state:
// the user has not said anything. [LeewayUnknown] is the user's explicit
// statement that they do not know, and [LeewayNone] their statement that
// there is none. Neither is ever read as zero days.
type LeewayKind string

// Leeway kinds.
const (
	// LeewayUnknown: the user states they do not know whether any grace exists.
	LeewayUnknown LeewayKind = "unknown"
	// LeewayNone: the user states the creditor allows no grace.
	LeewayNone LeewayKind = "none"
	// LeewayPeriod: grace lasts Days days after each occurrence's due date.
	LeewayPeriod LeewayKind = "period"
	// LeewayDeadline: grace lasts until a fixed date, for the occurrences due
	// on or before it (typically a one-off arrangement with the creditor).
	LeewayDeadline LeewayKind = "deadline"
)

// LeewayKinds returns every kind.
func LeewayKinds() []LeewayKind {
	return []LeewayKind{LeewayUnknown, LeewayNone, LeewayPeriod, LeewayDeadline}
}

// Valid reports whether k is a known kind.
func (k LeewayKind) Valid() bool { return slices.Contains(LeewayKinds(), k) }

// Limits of leeway, in days and characters.
const (
	MaxLeewayDays  = 365
	MaxLeewayText  = 500
	MaxLeewaySrc   = 200
	leewayTextName = "must be 1 to %d characters and not blank; use null for none"
)

// Leeway is what the user recorded about a commitment's grace. Days is set
// exactly for [LeewayPeriod] and Until exactly for [LeewayDeadline]. The text
// members are optional and free for every kind: the conditions the grace
// depends on, the fees or other consequences the user knows of, and where the
// information comes from (a contract clause, a letter, a call).
type Leeway struct {
	Kind         LeewayKind
	Days         *int
	Until        *calendar.Date
	Conditions   *string
	Consequences *string
	Source       *string
}

// Validate returns one error per invalid member, named leeway.<member>.
func (l Leeway) Validate() []FieldError {
	var errs []FieldError
	if !l.Kind.Valid() {
		return []FieldError{{"leeway.kind", "kind must be one of " + joinNames(LeewayKinds())}}
	}
	switch {
	case l.Kind == LeewayPeriod && (l.Days == nil || *l.Days < 1 || *l.Days > MaxLeewayDays):
		errs = append(errs, FieldError{"leeway.days", fmt.Sprintf("days must be 1 to %d for a period; use kind none for no grace", MaxLeewayDays)})
	case l.Kind != LeewayPeriod && l.Days != nil:
		errs = append(errs, FieldError{"leeway.days", "days applies only to kind period; a patch that changes the kind must send days as null"})
	}
	switch {
	case l.Kind == LeewayDeadline && (l.Until == nil || l.Until.IsZero()):
		errs = append(errs, FieldError{"leeway.until", "until is required for a deadline"})
	case l.Kind != LeewayDeadline && l.Until != nil:
		errs = append(errs, FieldError{"leeway.until", "until applies only to kind deadline; a patch that changes the kind must send until as null"})
	}
	text := func(member string, s *string, max int) {
		if s != nil && (!utf8.ValidString(*s) || strings.TrimSpace(*s) == "" || utf8.RuneCountInString(*s) > max) {
			errs = append(errs, FieldError{"leeway." + member, member + " " + fmt.Sprintf(leewayTextName, max)})
		}
	}
	text("conditions", l.Conditions, MaxLeewayText)
	text("consequences", l.Consequences, MaxLeewayText)
	text("source", l.Source, MaxLeewaySrc)
	return errs
}

// GraceUntil returns the last day of grace for an occurrence due on due:
// ok is false unless the leeway is a period or a deadline that covers it. An
// unknown or absent leeway never yields a date.
func (l *Leeway) GraceUntil(due calendar.Date) (calendar.Date, bool) {
	if l == nil {
		return calendar.Date{}, false
	}
	switch l.Kind {
	case LeewayPeriod:
		if l.Days == nil {
			return calendar.Date{}, false
		}
		until, err := due.AddDays(*l.Days)
		return until, err == nil
	case LeewayDeadline:
		if l.Until != nil && !due.After(*l.Until) {
			return *l.Until, true
		}
	}
	return calendar.Date{}, false
}

// SameLeeway compares two optional leeways exactly.
func SameLeeway(a, b *Leeway) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Kind == b.Kind && equalPtr(a.Days, b.Days) && equalPtr(a.Until, b.Until) &&
		equalPtr(a.Conditions, b.Conditions) && equalPtr(a.Consequences, b.Consequences) && equalPtr(a.Source, b.Source)
}
