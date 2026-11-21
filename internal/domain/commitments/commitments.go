// Package commitments holds the rules for recurring commitment definitions
// (task C1, ADR-0024): what a commitment is (kind, name, exact amount with its
// currency), when it falls due (a versioned schedule spec), the dates it runs
// between, its priority and notes.
//
// A definition is user-entered data. It is kept apart from bank facts
// (ADR-0013) and from dated occurrences and payment state, which are derived
// from it by later tasks (C2, C3) and stored in their own tables, so editing a
// definition never rewrites what the bank reported or what was paid.
package commitments

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Kind is what a commitment pays for.
type Kind string

// Kinds named by the requirements. A kind added later needs a migration (the
// commitments table checks the value).
const (
	KindService       Kind = "service"
	KindServer        Kind = "server" // a server or VPS
	KindDomain        Kind = "domain"
	KindSubscription  Kind = "subscription"
	KindDebtRepayment Kind = "debt_repayment"
)

// Kinds returns every kind.
func Kinds() []Kind {
	return []Kind{KindService, KindServer, KindDomain, KindSubscription, KindDebtRepayment}
}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return slices.Contains(Kinds(), k) }

// Priority is how important it is to pay a commitment on time, on the
// three-level scale of decision D14. A commitment may have no priority yet.
type Priority string

// Priorities (D14 option a).
const (
	PriorityEssential Priority = "essential"
	PriorityImportant Priority = "important"
	PriorityFlexible  Priority = "flexible"
)

// Priorities returns every priority, most important first.
func Priorities() []Priority {
	return []Priority{PriorityEssential, PriorityImportant, PriorityFlexible}
}

// Valid reports whether p is a known priority.
func (p Priority) Valid() bool { return slices.Contains(Priorities(), p) }

// Limits on free text, in characters.
const (
	MaxName  = 100
	MaxNotes = 2000
)

// Commitment is a stored recurring commitment definition.
type Commitment struct {
	ID string
	Fields
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields are the user-editable members of a commitment. Create and update
// both take the complete set; a partial update is applied to the current
// fields first (JSON Merge Patch at the API).
type Fields struct {
	Name string
	Kind Kind
	// Amount is the expected amount per due date, exact, in its currency
	// (ADR-0005). It is a positive amount to pay; it is never converted.
	Amount   money.Money
	Schedule Schedule
	// StartDate is the first day the commitment applies; EndDate, when set,
	// the last (inclusive).
	StartDate calendar.Date
	EndDate   *calendar.Date
	Priority  *Priority
	Notes     *string
}

// FieldError is a validation error about one field. Message is safe to show.
type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// ValidationError lists every invalid field of a request, in field order.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Error()
	}
	return "invalid commitment: " + strings.Join(parts, "; ")
}

// Validation returns errs as a *ValidationError, or nil when errs is empty.
func Validation(errs []FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: errs}
}

// Validate checks every field and returns one error per invalid field.
func (f Fields) Validate() []FieldError {
	var errs []FieldError
	if !utf8.ValidString(f.Name) || strings.TrimSpace(f.Name) == "" || utf8.RuneCountInString(f.Name) > MaxName {
		errs = append(errs, FieldError{"name", fmt.Sprintf("name must be 1 to %d characters and not blank", MaxName)})
	}
	if !f.Kind.Valid() {
		errs = append(errs, FieldError{"kind", "kind must be one of " + joinNames(Kinds())})
	}
	switch {
	case f.Amount.Currency().IsZero():
		errs = append(errs, FieldError{"amount.currency", "currency is required"})
	case f.Amount.Sign() <= 0:
		errs = append(errs, FieldError{"amount.amount", "amount must be greater than zero"})
	}
	errs = append(errs, f.Schedule.Validate("schedule")...)
	if f.StartDate.IsZero() {
		errs = append(errs, FieldError{"start_date", "start_date is required"})
	}
	if f.EndDate != nil && !f.StartDate.IsZero() && f.EndDate.Before(f.StartDate) {
		errs = append(errs, FieldError{"end_date", "end_date must not be before start_date"})
	}
	if f.Priority != nil && !f.Priority.Valid() {
		errs = append(errs, FieldError{"priority", "priority must be one of " + joinNames(Priorities())})
	}
	if f.Notes != nil && (!utf8.ValidString(*f.Notes) || strings.TrimSpace(*f.Notes) == "" || utf8.RuneCountInString(*f.Notes) > MaxNotes) {
		errs = append(errs, FieldError{"notes", fmt.Sprintf("notes must be 1 to %d characters and not blank; use null for none", MaxNotes)})
	}
	return errs
}

// ChangedFields returns the names of the fields that differ, sorted.
func ChangedFields(a, b Fields) []string {
	var out []string
	if a.Name != b.Name {
		out = append(out, "name")
	}
	if a.Kind != b.Kind {
		out = append(out, "kind")
	}
	if !a.Amount.Identical(b.Amount) {
		out = append(out, "amount")
	}
	if a.Schedule != b.Schedule {
		out = append(out, "schedule")
	}
	if a.StartDate != b.StartDate {
		out = append(out, "start_date")
	}
	if !equalPtr(a.EndDate, b.EndDate) {
		out = append(out, "end_date")
	}
	if !equalPtr(a.Priority, b.Priority) {
		out = append(out, "priority")
	}
	if !equalPtr(a.Notes, b.Notes) {
		out = append(out, "notes")
	}
	slices.Sort(out)
	return out
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func joinNames[T ~string](vs []T) string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = string(v)
	}
	return strings.Join(names, ", ")
}

// TargetType is the audit target type of commitments.
const TargetType = "commitment"

// CreatedEvent is the audit event for a new commitment. Details hold only
// fixed values (kind and currency), never the name or notes.
func CreatedEvent(actor audit.Actor, c Commitment, at time.Time) audit.Event {
	return audit.Event{Action: audit.CommitmentCreated, Actor: actor, TargetType: TargetType, TargetID: c.ID, At: at,
		Details: map[string]string{"kind": string(c.Kind), "currency": c.Amount.Currency().Code()}}
}

// UpdatedEvent is the audit event for changing a commitment's fields: the
// names of the changed fields, not their values.
func UpdatedEvent(actor audit.Actor, id string, fields []string, at time.Time) audit.Event {
	return audit.Event{Action: audit.CommitmentUpdated, Actor: actor, TargetType: TargetType, TargetID: id, At: at,
		Details: map[string]string{"fields": strings.Join(fields, ",")}}
}

// DeletedEvent is the audit event for deleting a commitment.
func DeletedEvent(actor audit.Actor, c Commitment, at time.Time) audit.Event {
	return audit.Event{Action: audit.CommitmentDeleted, Actor: actor, TargetType: TargetType, TargetID: c.ID, At: at,
		Details: map[string]string{"kind": string(c.Kind)}}
}

// ErrInUse means a commitment cannot be deleted because other records (for
// example occurrences with payment history, once they exist) refer to it.
var ErrInUse = errors.New("the commitment has dependent records and cannot be deleted")

// ListFilter selects commitments. Limit is the page size; implementations
// return up to Limit+1 commitments after After in ID order.
type ListFilter struct {
	Kinds      []Kind
	Priorities []Priority
	Currencies []money.Currency
	After      string
	Limit      int
}

// Store is the commitments port; internal/store/commitments implements it.
// Writes record their audit events in the same transaction.
type Store interface {
	Create(ctx context.Context, actor audit.Actor, f Fields) (Commitment, error)
	Get(ctx context.Context, id string) (Commitment, error)
	List(ctx context.Context, f ListFilter) ([]Commitment, error)
	// Update replaces the fields when the commitment still has version
	// expected. It returns a *ValidationError, resource.ErrNotFound or a
	// *resource.VersionConflictError. Unchanged fields write nothing. When
	// the amount, schedule, start or end date change, the change reaches the
	// commitment's occurrences from effective forward (decision D6 edits
	// option a, ADR-0027): paid occurrences never change.
	Update(ctx context.Context, actor audit.Actor, id string, expected int64, f Fields, effective calendar.Date) (Commitment, error)
	// Delete removes the commitment and its unpaid occurrences when it still
	// has version expected. It returns resource.ErrNotFound, a
	// *resource.VersionConflictError or ErrInUse (paid occurrences refer to
	// it).
	Delete(ctx context.Context, actor audit.Actor, id string, expected int64) error
}
