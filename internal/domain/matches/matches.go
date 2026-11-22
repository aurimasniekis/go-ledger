// Package matches defines payment matches (task X1, ADR-0035): links
// between a bank transaction and a commitment occurrence, each with the amount
// of the transaction allocated to that occurrence (decision D8 option a), and
// the rules for creating, confirming, rejecting, unmatching, correcting and
// reversing them.
//
// A match keeps four kinds of information apart, so a reader can always tell
// them apart:
//
//   - the bank fact it refers to ([Transaction]: what the provider reported,
//     never written here);
//   - who proposed it ([Origin]: the matching engine's rules, an optional
//     model, or the user);
//   - the decision about it ([State] and the decider: suggested awaits one;
//     confirmed, rejected and reversed are decisions);
//   - whether it currently counts as payment evidence ([Match.CountsAsEvidence]:
//     confirmed, on a transaction that is booked, still reported and not
//     superseded).
//
// Only confirmed matches are payment evidence for the payment state of
// ADR-0031. A pending transaction only ever produces suggestions (decision D8),
// and a model suggestion is never confirmed except by a user (decision D9).
// This package has no matching engine: task X2 creates suggestions and
// rule-confirmed matches through [Store.Create].
package matches

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/banking"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/domain/occurrences"
	"github.com/aurimasniekis/go-ledger/internal/domain/payments"
)

// Origin says who proposed a match.
type Origin string

// Origins.
const (
	// OriginAutoRule is the matching engine (task X2): its safeguards
	// (decision D9) or a rule the user marked as confirming may confirm a
	// match; any other rule only suggests.
	OriginAutoRule Origin = "auto_rule"
	// OriginModel is an optional model's suggestion. It is only ever a
	// suggestion; a user may confirm it (decision D9).
	OriginModel Origin = "model_suggestion"
	// OriginUser is a match the user made by hand. It is confirmed from the
	// start.
	OriginUser Origin = "user"
)

// Origins lists the valid origins.
func Origins() []Origin { return []Origin{OriginAutoRule, OriginModel, OriginUser} }

// Valid reports whether o is an origin.
func (o Origin) Valid() bool { return slices.Contains(Origins(), o) }

// State is the decision state of a match.
type State string

// States.
const (
	// StateSuggested awaits a decision. It is never payment evidence.
	StateSuggested State = "suggested"
	// StateConfirmed is payment evidence while its transaction is countable.
	StateConfirmed State = "confirmed"
	// StateRejected records that the transaction does not pay the occurrence:
	// a rejected suggestion or an undone (unmatched) confirmation. Final.
	StateRejected State = "rejected"
	// StateReversed records a confirmed match whose payment was reversed
	// (bounced, refunded or returned). The occurrence reopens. Final.
	StateReversed State = "reversed"
)

// States lists the valid states.
func States() []State { return []State{StateSuggested, StateConfirmed, StateRejected, StateReversed} }

// Valid reports whether s is a state.
func (s State) Valid() bool { return slices.Contains(States(), s) }

// Final reports whether no further change is allowed.
func (s State) Final() bool { return s == StateRejected || s == StateReversed }

// Open reports whether the match still links its pair (suggested or
// confirmed): at most one open match exists per transaction and occurrence.
func (s State) Open() bool { return s == StateSuggested || s == StateConfirmed }

// Action is one change in a match's history.
type Action string

// Actions. Each has an audit action of the same meaning.
const (
	ActionSuggested Action = "suggested" // created as a suggestion
	ActionConfirmed Action = "confirmed" // created confirmed, or a suggestion confirmed
	ActionRejected  Action = "rejected"  // a suggestion rejected
	ActionUnmatched Action = "unmatched" // a confirmation undone: the transaction does not pay the occurrence
	ActionCorrected Action = "corrected" // the allocated amount changed
	ActionReversed  Action = "reversed"  // the payment was reversed
	// ActionUnconfirmed is a confirmation taken back by the person who made
	// it, inside the undo window (ADR-0045): the match is a suggestion again.
	ActionUnconfirmed Action = "unconfirmed"
)

// MaxReason is the longest reason, in characters.
const MaxReason = 500

// Transaction is the bank fact a match refers to, as the provider reported it
// (ADR-0013). Nothing in this package writes it.
type Transaction struct {
	ID        banking.TransactionID
	AccountID banking.AccountID
	Provider  string
	Status    banking.BookingStatus
	// Amount is the signed source amount: a payment is negative.
	Amount       money.Money
	BookingDate  calendar.Date
	ValueDate    calendar.Date
	Date         calendar.Date // booking, else value, else transaction date
	Description  string
	Counterparty string
	// NoLongerReportedAt is set when the provider stopped reporting the
	// transaction (decision D8: never deleted). SupersededBy is the booked
	// transaction a pending one became, when the import can tell.
	NoLongerReportedAt time.Time
	SupersededBy       banking.TransactionID
}

// Countable reports whether the transaction may count as money moved:
// booked, still reported and not superseded (banking.StoredTransaction
// Countable, ADR-0020 point 7).
func (t Transaction) Countable() bool {
	return t.Status == banking.StatusBooked && t.NoLongerReportedAt.IsZero() && t.SupersededBy == ""
}

// Magnitude is the transaction amount without its sign: the most that can be
// allocated from it.
func (t Transaction) Magnitude() money.Money { return t.Amount.Abs() }

// Decider is who made a decision about a match, and how it reached the
// server (decision D42 3a).
type Decider struct {
	Kind   audit.ActorKind
	UserID string // empty for the system
	// Via is session, token, mcp or system; empty for changes recorded
	// before provenance was (migration 0023).
	Via audit.Via
}

// DeciderOf returns the decider for an audit actor.
func DeciderOf(a audit.Actor) Decider {
	return Decider{Kind: a.Kind, UserID: a.UserID, Via: a.Channel()}
}

// Event is one entry of a match's history: the change, the state and amount
// after it, who made it and the reason given.
type Event struct {
	ID     string
	Action Action
	State  State
	Amount money.Money
	By     Decider
	Reason *string
	At     time.Time
	// Replaced lists the manual payment records this change marked replaced
	// by the match (a confirmation "as the payment I recorded", decision D45,
	// ADR-0049); Restored those it made count again (an unconfirm or unmatch
	// of such a confirmation). Both are empty for other changes.
	Replaced []string
	Restored []string
}

// Match is a payment match with the bank fact it refers to and its history.
type Match struct {
	ID            string
	TransactionID banking.TransactionID
	OccurrenceID  string
	// Amount is the part of the transaction allocated to the occurrence, in
	// the currency of both.
	Amount    money.Money
	Origin    Origin
	State     State
	CreatedBy Decider
	// DecidedBy and DecidedAt are the latest decision (confirmation,
	// rejection, unmatch or reversal); nil while suggested. Reason is the
	// reason given with the latest change, if any.
	DecidedBy   *Decider
	DecidedAt   *time.Time
	Reason      *string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Transaction Transaction
	History     []Event
}

// CountsAsEvidence reports whether the match is currently payment evidence:
// confirmed, on a countable transaction. A confirmed match on a transaction
// the provider no longer reports stays confirmed and visible, but does not
// count until the transaction is reported again.
func (m Match) CountsAsEvidence() bool { return m.State == StateConfirmed && m.Transaction.Countable() }

// NeedsReview reports whether an open match (suggested or confirmed) is on a
// transaction that can no longer back it: the provider stopped reporting it,
// it was superseded, or it is not booked and not a pending one that is still
// reported (a pending transaction only ever yields suggestions, which is
// normal).
func (m Match) NeedsReview() bool {
	t := m.Transaction
	if !m.State.Open() {
		return false
	}
	return !t.NoLongerReportedAt.IsZero() || t.SupersededBy != "" || (!t.Countable() && t.Status != banking.StatusPending)
}

// New is a match to create.
type New struct {
	TransactionID banking.TransactionID
	OccurrenceID  string
	Amount        money.Money
	Origin        Origin
	// State is StateSuggested or StateConfirmed. A user match is always
	// confirmed; a model suggestion always suggested.
	State State
	// AllowOverpayment lets a confirmation take the occurrence's evidence
	// past its amount; the excess shows as overpaid_amount.
	AllowOverpayment bool
	Reason           *string
}

// Validate checks n on its own (not against stored data).
func (n New) Validate() []commitments.FieldError {
	var errs []commitments.FieldError
	if n.TransactionID == "" {
		errs = append(errs, commitments.FieldError{Field: "transaction_id", Message: "transaction_id is required"})
	}
	if n.OccurrenceID == "" {
		errs = append(errs, commitments.FieldError{Field: "occurrence_id", Message: "occurrence_id is required"})
	}
	errs = append(errs, validateAmount(n.Amount)...)
	switch {
	case !n.Origin.Valid():
		errs = append(errs, commitments.FieldError{Field: "origin", Message: "origin must be auto_rule, model_suggestion or user"})
	case n.State != StateSuggested && n.State != StateConfirmed:
		errs = append(errs, commitments.FieldError{Field: "state", Message: "a new match is suggested or confirmed"})
	case n.Origin == OriginModel && n.State != StateSuggested:
		errs = append(errs, commitments.FieldError{Field: "state", Message: "a model suggestion is only ever a suggestion; a user confirms it"})
	case n.Origin == OriginUser && n.State != StateConfirmed:
		errs = append(errs, commitments.FieldError{Field: "state", Message: "a match made by a user is confirmed"})
	}
	errs = append(errs, ValidateReason(n.Reason)...)
	return errs
}

func validateAmount(m money.Money) []commitments.FieldError {
	switch {
	case !m.IsValid():
		return []commitments.FieldError{{Field: "amount", Message: "amount is required"}}
	case m.Sign() <= 0:
		return []commitments.FieldError{{Field: "amount.amount", Message: "amount.amount must be greater than zero"}}
	}
	return nil
}

// ValidateAmount checks a corrected allocation on its own.
func ValidateAmount(m money.Money) []commitments.FieldError { return validateAmount(m) }

// ValidateReason checks an optional reason: nil, or 1 to [MaxReason]
// characters and not blank.
func ValidateReason(r *string) []commitments.FieldError {
	if r == nil {
		return nil
	}
	if !utf8.ValidString(*r) || strings.TrimSpace(*r) == "" || utf8.RuneCountInString(*r) > MaxReason {
		return []commitments.FieldError{{Field: "reason", Message: fmt.Sprintf("reason must be 1 to %d characters and not blank; use null for none", MaxReason)}}
	}
	return nil
}

// Errors of the match rules. The store returns them; the API maps each to a
// stable error code.
var (
	// ErrNotCountable refuses to confirm a match on a transaction that is
	// not booked, no longer reported or superseded: pending transactions only
	// produce suggestions (decision D8).
	ErrNotCountable = errors.New("only a booked bank transaction that is still reported and not superseded can confirm a payment; a pending one can only be suggested")
	// ErrNotDebit refuses a match on an incoming transaction: only an
	// outgoing payment pays a commitment.
	ErrNotDebit = errors.New("only an outgoing bank transaction can pay an occurrence")
	// ErrCurrency refuses an allocation whose currency differs from the
	// transaction's or the occurrence's: amounts are never converted.
	ErrCurrency = errors.New("the allocation must be in the currency of both the transaction and the occurrence; amounts are never converted")
	// ErrOverAllocated refuses confirmed allocations of one transaction that
	// would add up to more than its amount (double assignment).
	ErrOverAllocated = errors.New("the confirmed allocations of this transaction would exceed its amount")
	// ErrOverpaid refuses a confirmation that would take the occurrence's
	// payment evidence past its amount, unless overpayment is explicitly
	// allowed.
	ErrOverpaid = errors.New("this would pay the occurrence more than its amount; allow overpayment only if the payment really is larger than the amount due. If the bank re-reported a payment that an earlier match already replaces, unmatch that earlier match first (this restores the manual record it replaced), then confirm the new transaction as replacing that record")
	// ErrScale refuses an allocation with more decimal places than the
	// transaction's amount: it would leave an unpayable sub-unit remainder.
	ErrScale = errors.New("the allocation has more decimal places than the bank transaction's amount")
	// ErrAutoNotOneToOne refuses a rule confirmation (decision D8: automatic
	// matching is 1:1 only) that is not the transaction's whole amount, or
	// whose transaction has another open allocation.
	ErrAutoNotOneToOne = errors.New("an automatic confirmation must allocate the whole transaction and be its only open match")
	// ErrAutoDate refuses a rule confirmation whose transaction is dated
	// more than AutoConfirmDays from the occurrence's due date (decision D9).
	ErrAutoDate = errors.New("an automatic confirmation needs a transaction dated within 3 days of the due date")
	// ErrAutoAmount refuses a rule confirmation whose allocation differs
	// from the occurrence's amount by more than the tolerance given.
	ErrAutoAmount = errors.New("an automatic confirmation must match the occurrence's amount within the tolerance given")
	// ErrActorOrigin refuses a match that claims origin user from an actor
	// that is neither a user nor a token.
	ErrActorOrigin = errors.New("a match with origin user needs a user or token actor")
	// ErrActorOverpayment refuses AllowOverpayment from automation: deciding
	// that a payment really happened twice is a user's judgement (duplicate
	// payments are an unresolved decision).
	ErrActorOverpayment = errors.New("only a user or a token can allow an overpayment")
	// ErrActorCorrect refuses a correction by an actor that is neither a
	// user nor a token: automation cannot change an allocation.
	ErrActorCorrect = errors.New("only a user or a token can correct a match's allocation")
	// ErrActorUndo refuses an unmatch or reverse by automation (its own
	// confirmations included) and a reject of a suggestion a person (user or
	// token) made or corrected (decision D33). Automation may only drop its
	// own suggestions.
	ErrActorUndo = errors.New("automation cannot unmatch or reverse a match, or reject one a user or token touched")
	// ErrAutoModel refuses a confirmation of a model suggestion by automation:
	// model output is only ever a suggestion, which a person confirms
	// (decision D9).
	ErrAutoModel = errors.New("automation cannot confirm a model suggestion; only a user or a token can")
	// ErrUndoNotDecider refuses an unconfirm by anyone but the person who
	// confirmed the match (a token acts as its owner, decision D42 3a), and
	// any unconfirm of a confirmation automation made.
	ErrUndoNotDecider = errors.New("only the person who confirmed this match can unconfirm it; unmatch it instead")
	// ErrUndoWindow refuses an unconfirm outside the undo window (ADR-0045):
	// the confirmation is no longer the match's latest change or the person's
	// latest match decision, or something else changed the occurrence since.
	ErrUndoWindow = errors.New("the undo window for this confirmation has closed: something changed since; unmatch it instead")
	// ErrNotUnconfirmable refuses an unconfirm of a match that was never a
	// suggestion: a match a person created confirmed is undone by unmatching.
	ErrNotUnconfirmable = fmt.Errorf("%w: only a confirmed suggestion can go back to suggested; unmatch a match created confirmed", ErrTransition)
	// ErrActorReplace refuses a confirmation that replaces manual payments
	// by automation: only a person says a bank payment is the one they
	// recorded (decision D45).
	ErrActorReplace = errors.New("only a user or a token can confirm a match as replacing manual payments")
	// ErrReplaceNotFound names a manual payment to replace that does not
	// exist.
	ErrReplaceNotFound = errors.New("no manual payment has this identifier")
	// ErrReplaceOtherOccurrence refuses to replace a manual payment of
	// another occurrence than the match's.
	ErrReplaceOtherOccurrence = errors.New("the manual payment belongs to another occurrence than the match")
	// ErrReplaceUnavailable refuses to replace a manual payment that no
	// longer counts: it was deleted or another match already replaces it.
	ErrReplaceUnavailable = errors.New("the manual payment was deleted or is already replaced by a match")
	// ErrReplaceCurrency refuses to replace a manual payment in another
	// currency than the match: amounts are never converted.
	ErrReplaceCurrency = errors.New("the manual payment is in another currency than the match; amounts are never converted")
	// ErrDuplicate refuses a second open (suggested or confirmed) match of
	// the same transaction and occurrence.
	ErrDuplicate = errors.New("the transaction and the occurrence already have an open match")
	// ErrTransition refuses a state change the match's state does not
	// allow.
	ErrTransition = errors.New("this change is not allowed in the match's current state")
	// ErrTransactionNotFound and ErrOccurrenceNotFound name a missing link
	// target in a create request.
	ErrTransactionNotFound = errors.New("no bank transaction has this identifier")
	ErrOccurrenceNotFound  = errors.New("no occurrence has this identifier")
)

// CheckTarget checks that a match of amount between t and o may exist at all:
// an outgoing transaction, the same currency on all three, and an amount no
// larger than the transaction.
func CheckTarget(t Transaction, o occurrences.Occurrence, amount money.Money) error {
	if t.Amount.Sign() >= 0 {
		return ErrNotDebit
	}
	if amount.Currency() != t.Amount.Currency() || amount.Currency() != o.Amount.Currency() {
		return ErrCurrency
	}
	if amount.Amount().Scale() > t.Amount.Amount().Scale() {
		return ErrScale
	}
	if c, _ := amount.Cmp(t.Magnitude()); c > 0 {
		return ErrOverAllocated
	}
	return nil
}

// CheckAuto is the store's guard for a confirmation by the matching engine
// (origin auto_rule, decision D8: 1:1 only), whatever the caller decided: the
// allocation is the transaction's whole magnitude, the transaction has no
// other open (suggested or confirmed) allocation (otherAllocations counts
// them, leaving out the match itself), and the allocation equals the
// occurrence's amount, or differs by at most tolerance when it is valid, and
// the transaction is dated within [AutoConfirmDays] of the due date. The
// caller reads the tolerance from the commitment inside the write transaction;
// the other safeguards of decision D9 (one candidate on each side) stay with
// the engine.
func CheckAuto(t Transaction, otherOpen int, amount money.Money, o occurrences.Occurrence, tolerance money.Money) error {
	if otherOpen > 0 {
		return ErrAutoNotOneToOne
	}
	if c, err := amount.Cmp(t.Magnitude()); err != nil || c != 0 {
		return ErrAutoNotOneToOne
	}
	diff, err := amount.Sub(o.Amount)
	if err != nil {
		return ErrCurrency
	}
	diff = diff.Abs()
	if !tolerance.IsValid() {
		if diff.Sign() != 0 {
			return ErrAutoAmount
		}
	} else if c, err := diff.Cmp(tolerance); err != nil || c > 0 {
		return ErrAutoAmount
	}
	n := t.Date.DaysUntil(o.DueDate)
	if n < -AutoConfirmDays || n > AutoConfirmDays {
		return ErrAutoDate
	}
	return nil
}

// AutoConfirmDays is how many days before or after the due date a transaction
// may be dated for the engine to confirm it (decision D9).
const AutoConfirmDays = 3

// CheckConfirm checks that a confirmed allocation of amount from t to o is
// allowed, given the amounts already confirmed from t (to other occurrences,
// or to o by this very match before a correction: the caller leaves that one
// out) and o's payment facts (the caller leaves this match out of them). It
// returns ErrNotCountable, ErrOverAllocated or ErrOverpaid.
func CheckConfirm(t Transaction, otherAllocated []money.Money, amount money.Money, o occurrences.Occurrence, facts payments.Facts, allowOverpayment bool) error {
	if !t.Countable() {
		return ErrNotCountable
	}
	total := amount
	for _, a := range otherAllocated {
		var err error
		if total, err = total.Add(a); err != nil {
			return ErrCurrency
		}
	}
	if c, _ := total.Cmp(t.Magnitude()); c > 0 {
		return ErrOverAllocated
	}
	if allowOverpayment {
		return nil
	}
	// Evidence in the occurrence's currency only; the amount is in it too.
	// No grace hook is given, so nothing reads the context.
	paid := payments.Derive(context.Background(), o, facts, o.DueDate, nil).Paid
	after, err := paid.Add(amount)
	if err != nil {
		return ErrCurrency
	}
	if c, _ := after.Cmp(o.Amount); c > 0 {
		return ErrOverpaid
	}
	return nil
}

// TargetType is the audit target type of match events.
const TargetType = "payment_match"

// auditActions maps history actions to audit actions.
var auditActions = map[Action]audit.Action{
	ActionSuggested:   audit.PaymentMatchSuggested,
	ActionConfirmed:   audit.PaymentMatchConfirmed,
	ActionRejected:    audit.PaymentMatchRejected,
	ActionUnmatched:   audit.PaymentMatchUnmatched,
	ActionCorrected:   audit.PaymentMatchCorrected,
	ActionReversed:    audit.PaymentMatchReversed,
	ActionUnconfirmed: audit.PaymentMatchUnconfirmed,
}

// AuditEvent is the audit entry for a change of m (after the change). It
// names the occurrence, the transaction, the origin, the state before and the
// currency and how the change came (via); never the amount, the reason or
// anything from the bank payload (ADR-0025).
func AuditEvent(a Action, actor audit.Actor, m Match, from State, at time.Time) audit.Event {
	prev := string(from)
	if prev == "" {
		prev = "none"
	}
	d := map[string]string{"occurrence_id": m.OccurrenceID, "transaction_id": string(m.TransactionID), "origin": string(m.Origin),
		"from": prev, "currency": m.Amount.Currency().Code()}
	if v := actor.Channel(); v != "" {
		d["via"] = string(v)
	}
	return audit.Event{Action: auditActions[a], Actor: actor, TargetType: TargetType, TargetID: m.ID, At: at, Details: d}
}

// ListFilter selects matches. Empty fields select everything; After is the
// last ID of the previous page; Limit rows are returned at most.
type ListFilter struct {
	TransactionIDs []string
	OccurrenceIDs  []string
	States         []State
	Origins        []Origin
	After          string
	Limit          int
}

// Change is the result of a match write: the match afterwards and the
// occurrence's payment change (its version increases with every change).
type Change struct {
	Match  Match
	Result payments.Change
}

// Store persists matches; internal/store/matches implements it. Every write
// runs in one transaction that re-checks the rules above against the stored
// data, writes the match and its history entry, increases the occurrence's
// version and records the audit event. Versions are the match's.
type Store interface {
	// List returns matches by ID (creation order).
	List(ctx context.Context, f ListFilter) ([]Match, error)
	// Get returns a match or resource.ErrNotFound.
	Get(ctx context.Context, id string) (Match, error)
	// Create stores a new match: a user's confirmed match, or a suggestion
	// or rule confirmation from the matching engine (task X2).
	Create(ctx context.Context, actor audit.Actor, n New) (Change, error)
	// Confirm confirms a suggestion.
	Confirm(ctx context.Context, actor audit.Actor, id string, expected int64, allowOverpayment bool, reason *string) (Change, error)
	// Reject rejects a suggestion; Unmatch undoes a confirmation. Both leave
	// the match rejected.
	Reject(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (Change, error)
	Unmatch(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (Change, error)
	// Correct changes the allocated amount of a suggested or confirmed match.
	Correct(ctx context.Context, actor audit.Actor, id string, expected int64, amount money.Money, allowOverpayment bool, reason *string) (Change, error)
	// Reverse marks a confirmed match reversed; the occurrence reopens.
	Reverse(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (Change, error)
	// Unconfirm takes back a confirmation: the match is a suggestion again,
	// as it was before it was confirmed (decision D42 1a, ADR-0045). Only the
	// person who confirmed it may, and only inside the undo window. Manual
	// payments the confirmation replaced count again.
	Unconfirm(ctx context.Context, actor audit.Actor, id string, expected int64, reason *string) (Change, error)
	// ConfirmReplacing confirms a suggestion "as the payment I recorded"
	// (decision D45, ADR-0049): in the same write it marks the listed manual
	// payments of the match's occurrence replaced by the match, so they stop
	// counting and the bank match pays the occurrence instead.
	ConfirmReplacing(ctx context.Context, actor audit.Actor, id string, expected int64, r Replacement) (Change, error)
}

// MaxReplaced is the most manual payments one confirmation may replace.
const MaxReplaced = 20

// Replacement is a confirmation that replaces manual payment records
// (decision D45, ADR-0049).
type Replacement struct {
	// ManualPaymentIDs are the records the bank payment is: 1 to
	// [MaxReplaced], each once, all of the match's occurrence.
	ManualPaymentIDs []string
	// AllowOverpayment lets the confirmation take the occurrence's
	// remaining evidence past its amount, as for Confirm.
	AllowOverpayment bool
	Reason           *string
}

// Validate checks r on its own (not against stored data).
func (r Replacement) Validate() []commitments.FieldError {
	var errs []commitments.FieldError
	switch n := len(r.ManualPaymentIDs); {
	case n == 0:
		errs = append(errs, commitments.FieldError{Field: "manual_payment_ids", Message: "manual_payment_ids needs at least one manual payment"})
	case n > MaxReplaced:
		errs = append(errs, commitments.FieldError{Field: "manual_payment_ids", Message: fmt.Sprintf("manual_payment_ids lists at most %d manual payments", MaxReplaced)})
	}
	seen := make(map[string]bool, len(r.ManualPaymentIDs))
	for _, id := range r.ManualPaymentIDs {
		if seen[id] {
			errs = append(errs, commitments.FieldError{Field: "manual_payment_ids", Message: "manual_payment_ids lists a manual payment twice"})
			break
		}
		seen[id] = true
	}
	return append(errs, ValidateReason(r.Reason)...)
}
