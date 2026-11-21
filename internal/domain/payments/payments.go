// Package payments derives the payment state of commitment occurrences
// (task C3, ADR-0031) and defines manual payment records and manual status
// overrides.
//
// The status of an occurrence is never stored (decision D7 option a). It is
// derived from payment evidence and dates each time it is read:
//
//   - evidence is a manual payment record (user data, this package) or, from
//     task X1, a confirmed allocation of a bank transaction to the occurrence;
//   - only evidence in the occurrence's own currency counts towards its
//     amount. Evidence in another currency is reported, never converted (no
//     conversion policy applies here), and never summed with the rest;
//   - a stored manual override, with a reason, can say "paid" or "unpaid".
//     "paid" applies only while some evidence exists, so no occurrence
//     becomes paid without evidence.
//
// Overdue counts from the original due date (decision D7 grace option a). A
// documented grace period (task DF2) only annotates an overdue occurrence as
// "within documented grace until X"; it never hides the obligation. This
// package has no grace default: [GraceFunc] is the hook DF2 fills.
//
// Bank facts (ADR-0013) are never written by anything here.
package payments

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
)

// Status is the derived payment status of an occurrence (decision D7).
type Status string

// Statuses. Upcoming, due and overdue describe an occurrence (or the unpaid
// remainder of one) by date alone; see [TimingOf].
const (
	StatusPaid          Status = "paid"
	StatusPartiallyPaid Status = "partially_paid"
	StatusUpcoming      Status = "upcoming"
	StatusDue           Status = "due"
	StatusOverdue       Status = "overdue"
	// StatusCancelled is the status of a cancelled occurrence (D31): it is
	// not an obligation, so it is never upcoming, due or overdue.
	StatusCancelled Status = "cancelled"
)

// TimingOf places a due date against today in the reporting zone:
// upcoming when it is later, due on the day itself, overdue from the next
// day on. Overdue always counts from the original due date (D7 grace option
// a); a grace period never moves it. (The meaning of "due" as the due day
// itself is proposed in ADR-0031, not an operator decision.)
func TimingOf(due, today calendar.Date) Status {
	switch {
	case due.After(today):
		return StatusUpcoming
	case due == today:
		return StatusDue
	default:
		return StatusOverdue
	}
}

// EvidenceKind says where a piece of payment evidence comes from.
type EvidenceKind string

// Evidence kinds.
const (
	// EvidenceManual is a manual payment record entered by the user.
	EvidenceManual EvidenceKind = "manual_payment"
	// EvidenceBankTransaction is a confirmed allocation of a bank transaction
	// to the occurrence (a payment match, task X1, ADR-0035); its ID is the
	// match's.
	EvidenceBankTransaction EvidenceKind = "bank_transaction"
)

// Evidence is one piece of payment evidence for one occurrence: an amount
// allocated to it and the date it was paid. A manual payment is one piece of
// evidence; a bank transaction split across several occurrences (decision D8
// option a, task X1) is one piece per occurrence, each with its own
// allocated amount.
type Evidence struct {
	Kind         EvidenceKind
	ID           string // the manual payment's or the allocation's identifier
	OccurrenceID string
	Amount       money.Money
	Date         calendar.Date
}

// OverrideStatus is what a manual status override says.
type OverrideStatus string

// Override statuses.
const (
	// OverridePaid says the occurrence is settled although the counted
	// evidence does not cover its amount (for example a payment in another
	// currency, or a creditor accepting less). It needs evidence.
	OverridePaid OverrideStatus = "paid"
	// OverrideUnpaid says the evidence does not settle the occurrence; its
	// status follows its dates.
	OverrideUnpaid OverrideStatus = "unpaid"
)

// OverrideStatuses lists the valid override statuses.
func OverrideStatuses() []OverrideStatus { return []OverrideStatus{OverridePaid, OverrideUnpaid} }

// Valid reports whether s is an override status.
func (s OverrideStatus) Valid() bool { return slices.Contains(OverrideStatuses(), s) }

// MaxReason is the longest override reason, in characters.
const MaxReason = 500

// MaxNote is the longest manual payment note, in characters.
const MaxNote = commitments.MaxNotes

// Override is the active manual status override of an occurrence.
type Override struct {
	ID           string
	OccurrenceID string
	Status       OverrideStatus
	Reason       string
	SetAt        time.Time
	// Previous is the override this one replaced, if any: undoing this one
	// restores it.
	Previous *OverrideStatus
}

// Facts are the stored inputs of an occurrence's payment state.
type Facts struct {
	Evidence []Evidence
	Override *Override // nil when none is active
}

// GraceFunc returns the end of a documented grace period for an occurrence,
// if one is known. It is the hook for task DF2 (leeway); nil means no grace
// is known for any occurrence. There is deliberately no default length.
type GraceFunc func(o occurrences.Occurrence) (until calendar.Date, ok bool)

// State is the derived payment state of an occurrence.
type State struct {
	// Status is the status after the override; Derived the status from
	// evidence and dates alone.
	Status  Status
	Derived Status
	// Timing is upcoming, due or overdue for an occurrence that is not paid
	// (for partially_paid it describes the unpaid remainder); empty when paid.
	Timing Status
	// Paid sums the evidence in the occurrence's currency. Outstanding is the
	// amount less Paid, never below zero, and zero when the status is paid;
	// under an applied "unpaid" override it is the full amount, because the
	// evidence is declared not to settle the occurrence. Overpaid is Paid less
	// the amount when positive (zero otherwise); it is informational only.
	Paid        money.Money
	Outstanding money.Money
	Overpaid    money.Money
	// Unconverted lists the currencies of evidence that is not in the
	// occurrence's currency: it is not counted and never converted.
	Unconverted   []money.Currency
	EvidenceCount int
	// GraceUntil is the end of a documented grace period of an overdue
	// occurrence; WithinGrace is true while today is not after it. The
	// occurrence is overdue either way.
	GraceUntil  *calendar.Date
	WithinGrace bool
	Override    *Override
	// OverrideApplied is false for a "paid" override whose evidence has
	// since gone (it then has no effect).
	OverrideApplied bool
}

// Derive computes the payment state of o from its facts, today's date in the
// reporting zone and an optional grace hook.
func Derive(o occurrences.Occurrence, f Facts, today calendar.Date, grace GraceFunc) State {
	cur := o.Amount.Currency()
	paid, _ := money.Zero(cur, o.Amount.Amount().Scale())
	var other []money.Currency
	for _, e := range f.Evidence {
		if e.Amount.Currency() != cur {
			if !slices.Contains(other, e.Amount.Currency()) {
				other = append(other, e.Amount.Currency())
			}
			continue
		}
		paid, _ = paid.Add(e.Amount) // same currency: cannot fail
	}
	slices.SortFunc(other, func(a, b money.Currency) int { return strings.Compare(a.Code(), b.Code()) })
	timing := TimingOf(o.DueDate, today)
	zero, _ := money.Zero(cur, o.Amount.Amount().Scale())
	st := State{Paid: paid, Overpaid: zero, Unconverted: other, EvidenceCount: len(f.Evidence), Override: f.Override}
	if o.CancelledAt != nil {
		// A cancelled occurrence owes nothing whatever its dates say. Paid and
		// EvidenceCount stay as history; there is no timing and no grace.
		st.Status, st.Derived, st.Outstanding = StatusCancelled, StatusCancelled, zero
		return st
	}
	c, _ := paid.Cmp(o.Amount)
	if c > 0 {
		st.Overpaid, _ = paid.Sub(o.Amount)
	}
	switch {
	case c >= 0:
		st.Derived = StatusPaid
	case paid.Sign() > 0:
		st.Derived = StatusPartiallyPaid
	default:
		st.Derived = timing
	}
	st.Status = st.Derived
	if ov := f.Override; ov != nil {
		switch ov.Status {
		case OverridePaid:
			if len(f.Evidence) > 0 {
				st.Status, st.OverrideApplied = StatusPaid, true
			}
		case OverrideUnpaid:
			st.Status, st.OverrideApplied = timing, true
		}
	}
	if st.Status == StatusPaid {
		st.Outstanding, _ = money.Zero(cur, o.Amount.Amount().Scale())
		return st
	}
	st.Timing = timing
	if st.Override != nil && st.Override.Status == OverrideUnpaid && st.OverrideApplied {
		// The user declared the evidence does not settle it: never report
		// nothing outstanding while the status is not paid.
		st.Outstanding = o.Amount
	} else {
		st.Outstanding, _ = o.Amount.Sub(paid)
		if st.Outstanding.Sign() < 0 {
			st.Outstanding = zero
		}
	}
	if timing == StatusOverdue && grace != nil {
		if until, ok := grace(o); ok {
			st.GraceUntil = &until
			st.WithinGrace = !today.After(until)
		}
	}
	return st
}

// ManualPayment is a payment the user recorded by hand for one occurrence. It
// is user data: kept apart from bank facts (ADR-0013) and from match links
// (task X1). Records are never edited; a mistake is deleted and recorded
// again.
type ManualPayment struct {
	ID           string
	OccurrenceID string
	Amount       money.Money
	PaidOn       calendar.Date
	Note         *string
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Evidence returns the payment as evidence for its occurrence.
func (p ManualPayment) Evidence() Evidence {
	return Evidence{Kind: EvidenceManual, ID: p.ID, OccurrenceID: p.OccurrenceID, Amount: p.Amount, Date: p.PaidOn}
}

// NewManual is what the user enters to record a manual payment.
type NewManual struct {
	Amount money.Money
	PaidOn calendar.Date
	Note   *string
}

// Validate checks n against the occurrence it pays and today's date in the
// reporting zone. The amount must be positive and in the occurrence's
// currency (amounts are never converted); the payment date must not be after
// today (a planned payment is not a payment).
func (n NewManual) Validate(o occurrences.Occurrence, today calendar.Date) []commitments.FieldError {
	var errs []commitments.FieldError
	switch {
	case !n.Amount.IsValid():
		errs = append(errs, commitments.FieldError{Field: "amount", Message: "amount is required"})
	case n.Amount.Currency() != o.Amount.Currency():
		errs = append(errs, commitments.FieldError{Field: "amount.currency", Message: fmt.Sprintf(
			"amount.currency must be %s, the occurrence's currency; amounts are never converted", o.Amount.Currency())})
	case n.Amount.Sign() <= 0:
		errs = append(errs, commitments.FieldError{Field: "amount.amount", Message: "amount.amount must be greater than zero"})
	}
	switch {
	case n.PaidOn.IsZero():
		errs = append(errs, commitments.FieldError{Field: "paid_on", Message: "paid_on is required"})
	case n.PaidOn.After(today):
		errs = append(errs, commitments.FieldError{Field: "paid_on", Message: "paid_on must not be after today; a planned payment is not a payment"})
	case n.PaidOn.Before(commitments.MinStartDate):
		errs = append(errs, commitments.FieldError{Field: "paid_on", Message: "paid_on must not be before " + commitments.MinStartDate.String()})
	}
	if n.Note != nil && (!utf8.ValidString(*n.Note) || strings.TrimSpace(*n.Note) == "" || utf8.RuneCountInString(*n.Note) > MaxNote) {
		errs = append(errs, commitments.FieldError{Field: "note", Message: fmt.Sprintf("note must be 1 to %d characters and not blank; use null for none", MaxNote)})
	}
	return errs
}

// ValidateOverride checks a manual status override.
func ValidateOverride(s OverrideStatus, reason string) []commitments.FieldError {
	var errs []commitments.FieldError
	if !s.Valid() {
		errs = append(errs, commitments.FieldError{Field: "status", Message: "status must be paid or unpaid"})
	}
	if !utf8.ValidString(reason) || strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > MaxReason {
		errs = append(errs, commitments.FieldError{Field: "reason", Message: fmt.Sprintf("reason must be 1 to %d characters and not blank", MaxReason)})
	}
	return errs
}

// Errors of the payment store.
var (
	// ErrNoEvidence refuses a "paid" override for an occurrence that has no
	// payment evidence: nothing becomes paid without evidence.
	ErrNoEvidence = errors.New("the occurrence has no payment evidence; record a payment before marking it paid")
	// ErrNoOverride means there is no active override to undo.
	ErrNoOverride = errors.New("the occurrence has no manual status override")
)

// TargetType is the audit target type of payment-state events: the
// occurrence whose state changed.
const TargetType = "occurrence"

func overrideName(s *OverrideStatus) string {
	if s == nil {
		return "none"
	}
	return string(*s)
}

// RecordedEvent is the audit event for recording a manual payment. It carries
// the payment's identifier and currency, never its amount or note.
func RecordedEvent(actor audit.Actor, p ManualPayment, at time.Time) audit.Event {
	return audit.Event{Action: audit.ManualPaymentRecorded, Actor: actor, TargetType: TargetType, TargetID: p.OccurrenceID, At: at,
		Details: map[string]string{"payment_id": p.ID, "currency": p.Amount.Currency().Code()}}
}

// DeletedEvent is the audit event for deleting a manual payment. Unlike the
// recording it carries the amount (decision D26-3a), because the row is kept
// but no longer listed.
func DeletedEvent(actor audit.Actor, p ManualPayment, at time.Time) audit.Event {
	return audit.Event{Action: audit.ManualPaymentDeleted, Actor: actor, TargetType: TargetType, TargetID: p.OccurrenceID, At: at,
		Details: map[string]string{"payment_id": p.ID, "amount": p.Amount.Amount().String(), "currency": p.Amount.Currency().Code()}}
}

// OverriddenEvent is the audit event for setting a manual status override:
// the new status and the one it replaced ("none" when there was none). The
// reason is free text and is not recorded in the audit log.
func OverriddenEvent(actor audit.Actor, occurrenceID string, s OverrideStatus, previous *OverrideStatus, at time.Time) audit.Event {
	return audit.Event{Action: audit.OccurrenceStatusOverridden, Actor: actor, TargetType: TargetType, TargetID: occurrenceID, At: at,
		Details: map[string]string{"status": string(s), "previous": overrideName(previous)}}
}

// UndoneEvent is the audit event for undoing a manual status override: the
// status undone and the one restored ("none" when the derived status
// applies again).
func UndoneEvent(actor audit.Actor, occurrenceID string, undone OverrideStatus, restored *OverrideStatus, at time.Time) audit.Event {
	return audit.Event{Action: audit.OccurrenceStatusOverrideUndone, Actor: actor, TargetType: TargetType, TargetID: occurrenceID, At: at,
		Details: map[string]string{"undone": string(undone), "restored": overrideName(restored)}}
}

// Change is the result of a payment-state write: the occurrence afterwards
// (its version increases with every payment-state change) and its facts.
type Change struct {
	Occurrence occurrences.Occurrence
	Facts      Facts
}

// Store persists manual payments and overrides; internal/store/payments
// implements it. Every write runs in one transaction, increases the
// occurrence's version and records its audit event.
type Store interface {
	// Facts returns the facts of each listed occurrence (missing ones map to
	// empty facts).
	Facts(ctx context.Context, occurrenceIDs []string) (map[string]Facts, error)
	// Manual lists an occurrence's manual payments, oldest first, or
	// returns resource.ErrNotFound for an unknown occurrence.
	Manual(ctx context.Context, occurrenceID string) ([]ManualPayment, error)
	// GetManual returns one manual payment of the occurrence or
	// resource.ErrNotFound.
	GetManual(ctx context.Context, occurrenceID, id string) (ManualPayment, error)
	// RecordManual validates and stores a manual payment.
	RecordManual(ctx context.Context, actor audit.Actor, occurrenceID string, in NewManual, today calendar.Date) (ManualPayment, Change, error)
	// DeleteManual deletes a manual payment that still has version expected.
	DeleteManual(ctx context.Context, actor audit.Actor, occurrenceID, id string, expected int64) (Change, error)
	// SetOverride sets (or replaces) the occurrence's override when the
	// occurrence still has version expected. A "paid" override without
	// evidence is ErrNoEvidence.
	SetOverride(ctx context.Context, actor audit.Actor, occurrenceID string, expected int64, s OverrideStatus, reason string) (Change, error)
	// UndoOverride undoes the active override when the occurrence still has
	// version expected, restoring the override it replaced (or none).
	// Without an active override it is ErrNoOverride.
	UndoOverride(ctx context.Context, actor audit.Actor, occurrenceID string, expected int64) (Change, error)
}
