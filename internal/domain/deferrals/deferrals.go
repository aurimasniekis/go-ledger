// Package deferrals defines deferral plans (task DF1, ADR-0050): the user's
// plan to pay an outstanding occurrence later than its due date.
//
// A plan is separate from the occurrence it is about. It never changes the
// occurrence's due date, amount, payment evidence or derived status: the
// obligation stays outstanding and, past its original due date, overdue
// (decision D7 grace option a). A plan is never payment evidence, so nothing
// here can make an occurrence paid; it only moves where the outstanding amount
// is planned to be paid (see [PlannedMonth]). Three plan types are kept apart
// (decision D14): a personal plan, an extension the creditor agreed to, and a
// documented grace period. Fees and interest are never inferred: the user
// records them as explicit adjustments on the plan.
//
// Revising a plan keeps its history; undoing it ends it, and the occurrence's
// outstanding amount is planned in its own month again. An undone plan is
// history and never changes.
//
// All amounts are exact decimals in the occurrence's currency; a plan never
// combines currencies.
package deferrals

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
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// PlanType says on what authority a payment is planned later (decision D14).
type PlanType string

// Plan types.
const (
	// TypePersonal is the user's own plan: the creditor has not agreed to
	// anything.
	TypePersonal PlanType = "personal"
	// TypeCreditorAgreed is a later date the creditor agreed to.
	TypeCreditorAgreed PlanType = "creditor_agreed"
	// TypeDocumentedGrace is a grace period the creditor documents (for
	// example in its terms); the planned date is its last day. The
	// occurrence is still overdue from its original due date, annotated as
	// within documented grace until the planned date (decision D7).
	TypeDocumentedGrace PlanType = "documented_grace"
)

// PlanTypes lists the plan types.
func PlanTypes() []PlanType { return []PlanType{TypePersonal, TypeCreditorAgreed, TypeDocumentedGrace} }

// Valid reports whether t is a plan type.
func (t PlanType) Valid() bool { return slices.Contains(PlanTypes(), t) }

// AdjustmentKind says what an explicit adjustment is.
type AdjustmentKind string

// Adjustment kinds.
const (
	AdjustmentFee      AdjustmentKind = "fee"
	AdjustmentInterest AdjustmentKind = "interest"
	AdjustmentOther    AdjustmentKind = "other"
)

// AdjustmentKinds lists the adjustment kinds.
func AdjustmentKinds() []AdjustmentKind {
	return []AdjustmentKind{AdjustmentFee, AdjustmentInterest, AdjustmentOther}
}

// Valid reports whether k is an adjustment kind.
func (k AdjustmentKind) Valid() bool { return slices.Contains(AdjustmentKinds(), k) }

// MaxReason is the longest reason, and MaxNote the longest adjustment note, in
// characters.
const (
	MaxReason = 500
	MaxNote   = 500
)

// State is whether a plan is in force.
type State string

// Plan states.
const (
	StateActive State = "active"
	StateUndone State = "undone"
)

// States lists the plan states.
func States() []State { return []State{StateActive, StateUndone} }

// Valid reports whether s is a plan state.
func (s State) Valid() bool { return slices.Contains(States(), s) }

// Adjustment is a fee, interest or other charge the user recorded on a plan.
// It is a positive amount in the plan's currency. It stays planned (in the
// plan's month, under its own currency) until the person marks it paid or
// removes it: paying the occurrence's principal never settles it, and it is
// never payment evidence for the occurrence (D-DF 2). A removed adjustment is
// kept as history and no longer counts; so is a paid one, which counts as
// paid, and the marker can be cleared again.
type Adjustment struct {
	ID        string
	PlanID    string
	Kind      AdjustmentKind
	Amount    money.Money
	Note      *string
	CreatedAt time.Time
	RemovedAt *time.Time
	// PaidAt and PaidBy are when and by whom the adjustment was marked paid;
	// both nil while it is still owed.
	PaidAt *time.Time
	PaidBy *By
}

// Paid reports whether the adjustment was marked paid.
func (a Adjustment) Paid() bool { return a.PaidAt != nil }

// Planned reports whether the adjustment still counts as planned: neither
// removed nor marked paid.
func (a Adjustment) Planned() bool { return a.RemovedAt == nil && a.PaidAt == nil }

// Plan is a deferral plan.
type Plan struct {
	ID           string
	OccurrenceID string
	// CommitmentID is the occurrence's commitment, for filters and display.
	CommitmentID string
	Type         PlanType
	PlannedDate  calendar.Date
	Reason       *string
	// OriginalDueDate, OriginalAmount and OutstandingWhenPlanned are the
	// occurrence's due date and amount, and what was still owed, when the
	// plan was made. They are history: a later edit of the commitment's terms
	// or a payment does not change them.
	OriginalDueDate        calendar.Date
	OriginalAmount         money.Money
	OutstandingWhenPlanned money.Money
	// Adjustments are every adjustment recorded on the plan, oldest first,
	// removed ones included.
	Adjustments []Adjustment
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	UndoneAt    *time.Time
}

// State returns whether the plan is in force.
func (p Plan) State() State {
	if p.UndoneAt != nil {
		return StateUndone
	}
	return StateActive
}

// Active reports whether the plan is in force.
func (p Plan) Active() bool { return p.UndoneAt == nil }

// PlannedMonth is the month of the planned date.
func (p Plan) PlannedMonth() calendar.YearMonth { return p.PlannedDate.YearMonth() }

// AdjustmentTotal sums the adjustments that are not removed, in the plan's
// currency (zero when there are none).
func (p Plan) AdjustmentTotal() money.Money {
	total, _ := money.Zero(p.OriginalAmount.Currency(), p.OriginalAmount.Amount().Scale())
	for _, a := range p.Adjustments {
		if a.RemovedAt == nil {
			total, _ = total.Add(a.Amount) // same currency (store and schema check it)
		}
	}
	return total
}

// UnpaidAdjustmentTotal sums the adjustments that are still planned (neither
// removed nor marked paid), in the plan's currency.
func (p Plan) UnpaidAdjustmentTotal() money.Money {
	total, _ := money.Zero(p.OriginalAmount.Currency(), p.OriginalAmount.Amount().Scale())
	for _, a := range p.Adjustments {
		if a.Planned() {
			total, _ = total.Add(a.Amount)
		}
	}
	return total
}

// HasUnpaidAdjustments reports whether any adjustment is still planned.
func (p Plan) HasUnpaidAdjustments() bool {
	return slices.ContainsFunc(p.Adjustments, Adjustment.Planned)
}

// Missed reports whether the plan's date has passed while the occurrence, in
// payment state st, still owes something: the person planned to pay by
// PlannedDate and has not. It is derived when read (from the plan and the
// occurrence's evidence) and never stored, and it is not payment state: it
// clears when the occurrence is paid, when the plan is revised to a later date
// and when the plan is undone. A plan whose date is today is not missed yet.
func (p Plan) Missed(st payments.State, today calendar.Date) bool {
	return p.MissedReason(st, today) != ""
}

// Missed reasons: why a plan is missed.
const (
	// MissedPrincipal: the occurrence is overdue and still owes something.
	MissedPrincipal = "principal"
	// MissedFee: a recorded fee is still planned (not paid, not removed) after
	// the plan date, whether or not the principal is paid (D-DF-b 2a).
	MissedFee = "fee"
)

// MissedReason is why the plan is missed, or "" when it is not. The principal
// reason takes precedence over the fee one. An active plan whose date has
// passed is missed for the principal while the occurrence is overdue (its own
// due date has passed, so a due day moved later keeps it from being missed
// early) and still owes something, and for a fee while any adjustment is still
// planned; marking the fee paid or removing it clears the fee reason.
func (p Plan) MissedReason(st payments.State, today calendar.Date) string {
	if !p.Active() || !today.After(p.PlannedDate) {
		return ""
	}
	if st.Timing == payments.StatusOverdue && st.Status != payments.StatusCancelled && st.Status != payments.StatusPaid && st.Outstanding.Sign() > 0 {
		return MissedPrincipal
	}
	if p.HasUnpaidAdjustments() {
		return MissedFee
	}
	return ""
}

// GraceUntil is the last day of a documented grace period: the planned date
// of an active documented_grace plan. ok is false for other plans.
func (p Plan) GraceUntil() (calendar.Date, bool) {
	if p.Active() && p.Type == TypeDocumentedGrace {
		return p.PlannedDate, true
	}
	return calendar.Date{}, false
}

// PlannedMonth is the month in which an occurrence's outstanding amount is
// planned to be paid: the planned date's month while it has an active plan,
// otherwise its own month. Every outstanding occurrence is planned in exactly
// one month, so planned totals never count an amount twice: a deferred
// occurrence counts in its planned month and not in its own.
func PlannedMonth(o occurrences.Occurrence, p *Plan) calendar.YearMonth {
	if p != nil && p.Active() {
		return p.PlannedMonth()
	}
	return o.Month
}

// Fields are what the user decides about a plan: its type, its planned date
// and an optional reason.
type Fields struct {
	Type        PlanType
	PlannedDate calendar.Date
	Reason      *string
}

// NewPlan is a new plan for an occurrence.
type NewPlan struct {
	OccurrenceID string
	Fields
}

// Validate checks the fields on their own.
func (f Fields) Validate() []commitments.FieldError {
	var errs []commitments.FieldError
	if !f.Type.Valid() {
		errs = append(errs, commitments.FieldError{Field: "plan_type", Message: "plan_type must be one of " + joinTypes()})
	}
	if f.PlannedDate.IsZero() {
		errs = append(errs, commitments.FieldError{Field: "planned_date", Message: "planned_date is required"})
	}
	return append(errs, ValidateReason(f.Reason)...)
}

// ValidateReason checks an optional reason: nil, or 1 to [MaxReason]
// characters and not blank.
func ValidateReason(r *string) []commitments.FieldError {
	if r != nil && !validText(*r, MaxReason) {
		return []commitments.FieldError{{Field: "reason", Message: fmt.Sprintf("reason must be 1 to %d characters and not blank; use null for none", MaxReason)}}
	}
	return nil
}

func validText(s string, max int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}

func joinTypes() string {
	names := make([]string, 0, 3)
	for _, t := range PlanTypes() {
		names = append(names, string(t))
	}
	return strings.Join(names, ", ")
}

// CheckOccurrence decides whether occurrence o, whose payment state today is
// st, can be planned for planned. It is the store's guard for creating and
// revising a plan:
//
//   - a cancelled occurrence owes nothing ([occurrences.ErrCancelled]);
//   - an occurrence with nothing outstanding (paid, or "not recorded" because
//     it fell due before its commitment was tracked, D38 1a) is not deferred
//     ([ErrNothingOutstanding]);
//   - the planned date must be after the occurrence's due date (a deferral
//     plans a later payment) and within the materialisation horizon.
func CheckOccurrence(o occurrences.Occurrence, st payments.State, planned, today calendar.Date) error {
	if o.CancelledAt != nil || st.Status == payments.StatusCancelled {
		return occurrences.ErrCancelled
	}
	if st.Status == payments.StatusPaid || st.Timing == payments.StatusNotRecorded || st.Outstanding.Sign() <= 0 {
		return ErrNothingOutstanding
	}
	var errs []commitments.FieldError
	if !planned.After(o.DueDate) {
		errs = append(errs, commitments.FieldError{Field: "planned_date", Message: "planned_date must be after the occurrence's due date " + o.DueDate.String()})
	}
	if planned.YearMonth().Compare(occurrences.HorizonLimit(today)) > 0 {
		errs = append(errs, commitments.FieldError{Field: "planned_date", Message: fmt.Sprintf("planned_date must be within %d months after the current month", occurrences.MaxMonthsAhead)})
	}
	if len(errs) > 0 {
		return &commitments.ValidationError{Errors: errs}
	}
	return nil
}

// NewAdjustment is an explicit fee, interest or other charge to record on a
// plan.
type NewAdjustment struct {
	Kind   AdjustmentKind
	Amount money.Money
	Note   *string
}

// Validate checks n against the plan's currency: a positive amount in that
// currency (amounts are never converted).
func (n NewAdjustment) Validate(cur money.Currency) []commitments.FieldError {
	var errs []commitments.FieldError
	if !n.Kind.Valid() {
		errs = append(errs, commitments.FieldError{Field: "kind", Message: "kind must be fee, interest or other"})
	}
	switch {
	case !n.Amount.IsValid():
		errs = append(errs, commitments.FieldError{Field: "amount", Message: "amount is required"})
	case n.Amount.Currency() != cur:
		errs = append(errs, commitments.FieldError{Field: "amount.currency", Message: fmt.Sprintf(
			"amount.currency must be %s, the plan's currency; amounts are never converted", cur)})
	case n.Amount.Sign() <= 0:
		errs = append(errs, commitments.FieldError{Field: "amount.amount", Message: "amount.amount must be greater than zero"})
	}
	if n.Note != nil && !validText(*n.Note, MaxNote) {
		errs = append(errs, commitments.FieldError{Field: "note", Message: fmt.Sprintf("note must be 1 to %d characters and not blank; use null for none", MaxNote)})
	}
	return errs
}

// Action names a change in a plan's history.
type Action string

// History actions.
const (
	ActionCreated           Action = "created"
	ActionRevised           Action = "revised"
	ActionUndone            Action = "undone"
	ActionAdjustmentAdded   Action = "adjustment_added"
	ActionAdjustmentRemoved Action = "adjustment_removed"
	ActionAdjustmentPaid    Action = "adjustment_paid"
	ActionAdjustmentUnpaid  Action = "adjustment_unpaid"
)

// By is who made a change and how (decision D42 3a).
type By struct {
	Kind   audit.ActorKind
	UserID string
	Via    audit.Via
}

// ByOf returns who an actor is, for the history.
func ByOf(a audit.Actor) By { return By{Kind: a.Kind, UserID: a.UserID, Via: a.Channel()} }

// Event is one entry of a plan's history: the change, the plan's type and
// planned date after it, and the reason recorded with it (for an undo, the
// reason given for undoing).
type Event struct {
	Action       Action
	Version      int64 // the plan's version after the change
	Type         PlanType
	PlannedDate  calendar.Date
	Reason       *string
	AdjustmentID string
	By           By
	At           time.Time
}

// Errors of the plan store.
var (
	// ErrNothingOutstanding refuses a plan for an occurrence that owes
	// nothing now: a deferral plans an unpaid amount.
	ErrNothingOutstanding = errors.New("the occurrence has nothing outstanding; only an unpaid occurrence can be deferred")
	// ErrActivePlan refuses a second active plan for an occurrence: revise
	// the existing one instead.
	ErrActivePlan = errors.New("the occurrence already has an active deferral plan; revise or undo it instead")
	// ErrUndone refuses a change to an undone plan: it is history.
	ErrUndone = errors.New("the deferral plan was undone; it is history and cannot change")
	// ErrAdjustmentRemoved refuses removing an adjustment twice.
	ErrAdjustmentRemoved = errors.New("the adjustment was already removed")
	// ErrAdjustmentPaid refuses marking an adjustment paid twice.
	ErrAdjustmentPaid = errors.New("the adjustment is already marked paid")
	// ErrAdjustmentNotPaid refuses clearing the paid marker of an adjustment
	// that is not marked paid.
	ErrAdjustmentNotPaid = errors.New("the adjustment is not marked paid")
	// ErrUnpaidAdjustments refuses undoing a plan while it has a fee that is
	// still planned: a recorded fee never silently drops out of the planned
	// totals, so it is marked paid or removed first.
	ErrUnpaidAdjustments = errors.New("the plan has fees or interest that are still planned; mark each paid or remove it before undoing the plan")
	// ErrNoChange refuses a revision that changes nothing.
	ErrNoChange = errors.New("the revision changes nothing")
	// ErrOccurrenceNotFound means a new plan names an occurrence that does
	// not exist.
	ErrOccurrenceNotFound = errors.New("no occurrence has this identifier")
)

// TargetType is the audit target type of plan events.
const TargetType = "deferral_plan"

func details(p Plan, a audit.Actor) map[string]string {
	d := map[string]string{"occurrence_id": p.OccurrenceID, "plan_type": string(p.Type), "planned_date": p.PlannedDate.String(), "currency": p.OriginalAmount.Currency().Code()}
	if v := a.Channel(); v != "" {
		d["via"] = string(v)
	}
	return d
}

// CreatedEvent is the audit event for a new plan. Like every plan event it
// carries the occurrence, plan type, planned date and currency, never a
// reason, note or amount.
func CreatedEvent(a audit.Actor, p Plan, at time.Time) audit.Event {
	return audit.Event{Action: audit.DeferralPlanCreated, Actor: a, TargetType: TargetType, TargetID: p.ID, At: at, Details: details(p, a)}
}

// RevisedEvent is the audit event for a revision; fields names what changed
// (comma-separated: plan_type, planned_date, reason).
func RevisedEvent(a audit.Actor, p Plan, fields []string, at time.Time) audit.Event {
	d := details(p, a)
	d["fields"] = strings.Join(fields, ",")
	return audit.Event{Action: audit.DeferralPlanRevised, Actor: a, TargetType: TargetType, TargetID: p.ID, At: at, Details: d}
}

// UndoneEvent is the audit event for undoing a plan.
func UndoneEvent(a audit.Actor, p Plan, at time.Time) audit.Event {
	return audit.Event{Action: audit.DeferralPlanUndone, Actor: a, TargetType: TargetType, TargetID: p.ID, At: at, Details: details(p, a)}
}

// AdjustmentAddedEvent is the audit event for recording an adjustment: its
// identifier and kind, never its amount or note.
func AdjustmentAddedEvent(a audit.Actor, p Plan, adj Adjustment, at time.Time) audit.Event {
	d := details(p, a)
	d["adjustment_id"], d["kind"] = adj.ID, string(adj.Kind)
	return audit.Event{Action: audit.DeferralAdjustmentAdded, Actor: a, TargetType: TargetType, TargetID: p.ID, At: at, Details: d}
}

// AdjustmentRemovedEvent is the audit event for removing an adjustment.
func AdjustmentRemovedEvent(a audit.Actor, p Plan, adj Adjustment, at time.Time) audit.Event {
	e := AdjustmentAddedEvent(a, p, adj, at)
	e.Action = audit.DeferralAdjustmentRemoved
	return e
}

// AdjustmentPaidEvent is the audit event for marking an adjustment paid, like
// the other adjustment events without amount or note.
func AdjustmentPaidEvent(a audit.Actor, p Plan, adj Adjustment, at time.Time) audit.Event {
	e := AdjustmentAddedEvent(a, p, adj, at)
	e.Action = audit.DeferralAdjustmentPaid
	return e
}

// AdjustmentUnpaidEvent is the audit event for clearing an adjustment's paid
// marker.
func AdjustmentUnpaidEvent(a audit.Actor, p Plan, adj Adjustment, at time.Time) audit.Event {
	e := AdjustmentAddedEvent(a, p, adj, at)
	e.Action = audit.DeferralAdjustmentUnpaid
	return e
}

// ListFilter selects plans. Each list matches any of its values; empty
// matches all. PlannedMonth, when not zero, keeps plans planned in that month.
// Implementations return up to Limit plans after the identifier After,
// ordered by identifier.
type ListFilter struct {
	OccurrenceIDs []string
	CommitmentIDs []string
	States        []State
	PlannedMonth  calendar.YearMonth
	After         string
	Limit         int
}

// Reader is what the monthly view reads of plans.
type Reader interface {
	// Active returns the active plan of each listed occurrence that has
	// one.
	Active(ctx context.Context, occurrenceIDs []string) (map[string]Plan, error)
	// PlannedIn returns the active plans whose planned date is in month,
	// ordered by planned date, then identifier.
	PlannedIn(ctx context.Context, month calendar.YearMonth) ([]Plan, error)
}

// Store persists plans; internal/store/deferrals implements it. Every write
// runs in one transaction that checks the plan's version, appends to its
// history and records the audit event. No write changes the occurrence.
type Store interface {
	Reader
	// Create validates and stores a plan for an occurrence whose payment
	// state today is checked by [CheckOccurrence].
	Create(ctx context.Context, actor audit.Actor, in NewPlan, today calendar.Date) (Plan, error)
	// Get returns one plan or resource.ErrNotFound.
	Get(ctx context.Context, id string) (Plan, error)
	// List returns plans matching f.
	List(ctx context.Context, f ListFilter) ([]Plan, error)
	// History returns a plan's history, oldest first, or
	// resource.ErrNotFound.
	History(ctx context.Context, id string) ([]Event, error)
	// Revise replaces an active plan's fields when it still has version
	// expected.
	Revise(ctx context.Context, actor audit.Actor, id string, expected int64, f Fields, today calendar.Date) (Plan, error)
	// Undo ends an active plan when it still has version expected; reason is
	// optional.
	Undo(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (Plan, error)
	// AddAdjustment records an adjustment on an active plan.
	AddAdjustment(ctx context.Context, actor audit.Actor, id string, expected int64, in NewAdjustment) (Plan, Adjustment, error)
	// RemoveAdjustment removes an adjustment of an active plan.
	RemoveAdjustment(ctx context.Context, actor audit.Actor, id, adjustmentID string, expected int64) (Plan, error)
	// MarkAdjustmentPaid records that the person paid an adjustment of an
	// active plan, whether or not the occurrence's principal is paid. It is
	// never payment evidence for the occurrence.
	MarkAdjustmentPaid(ctx context.Context, actor audit.Actor, id, adjustmentID string, expected int64) (Plan, error)
	// UnmarkAdjustmentPaid clears the paid marker: the adjustment is planned
	// again.
	UnmarkAdjustmentPaid(ctx context.Context, actor audit.Actor, id, adjustmentID string, expected int64) (Plan, error)
}
