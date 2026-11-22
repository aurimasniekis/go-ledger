// Package alerts holds Ledger's in-app alerts (task L6, ADR-0065, decision
// D13): a bank consent about to expire, a commitment whose amount changed, and
// an overdue commitment with no matching payment.
//
// The condition, its facts and its message are Ledger's own, deterministic and
// exact (amounts are decimal text with their currency). A model may add
// wording for the alerts task, kept apart and labelled with its provider and
// model; it never replaces the message and is never needed to raise an alert.
// Alerts are in-app only: a channel (an optional notifier) receives them only
// when the operator has selected one.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/calendar"
	"github.com/aurimasniekis/go-ledger/internal/domain/money"
)

// Kind is an alert use case (decision D13).
type Kind string

// Kinds.
const (
	KindConsentExpiring  Kind = "consent_expiring"
	KindAmountChanged    Kind = "commitment_amount_changed"
	KindUnmatchedOverdue Kind = "unmatched_overdue"
)

// ObjectType is what an alert is about.
type ObjectType string

// Object types.
const (
	ObjectBankConnection ObjectType = "bank_connection"
	ObjectCommitment     ObjectType = "commitment"
	ObjectOccurrence     ObjectType = "occurrence"
)

// State of an alert.
type State string

// States: an alert is open until a person dismisses it or the condition no
// longer holds (resolved).
const (
	StateOpen      State = "open"
	StateDismissed State = "dismissed"
	StateResolved  State = "resolved"
)

// Condition is a detected situation, ready to raise.
type Condition struct {
	Kind       Kind
	ObjectType ObjectType
	ObjectID   string
	// Key identifies the situation: raising the same key while one alert is
	// open does nothing. It includes what makes the situation new (the expiry
	// date, the new amount, the due date).
	Key     string
	Facts   map[string]string
	Message string
}

// ConsentExpiring is the condition for a bank connection whose consent ends on
// expires, raised when that is within horizonDays of today (or has passed).
func ConsentExpiring(connectionID string, expires, today calendar.Date, horizonDays int) (Condition, bool) {
	if expires.IsZero() || today.IsZero() || today.DaysUntil(expires) > horizonDays {
		return Condition{}, false
	}
	days := today.DaysUntil(expires)
	msg := fmt.Sprintf("Bank consent expires on %s (in %d days): reconnect to keep data fresh.", expires, days)
	if days < 0 {
		msg = fmt.Sprintf("Bank consent expired on %s: reconnect to refresh data.", expires)
	}
	return Condition{Kind: KindConsentExpiring, ObjectType: ObjectBankConnection, ObjectID: connectionID,
		Key: "consent_expiring:" + connectionID + ":" + expires.String(), Facts: map[string]string{"expires_on": expires.String()}, Message: msg}, true
}

// AmountChanged is the condition for a commitment whose most recent matched
// bank payment (paid) differs from the expected amount of the occurrence it
// paid (for example a price rise). In the same currency any non-zero
// difference counts; in another currency it is reported as a currency
// difference and nothing is converted. False when they are identical.
func AmountChanged(commitmentID, occurrenceID string, expected, paid money.Money) (Condition, bool) {
	if !expected.IsValid() || !paid.IsValid() || expected.Identical(paid) {
		return Condition{}, false
	}
	facts := map[string]string{"occurrence_id": occurrenceID,
		"expected_amount": expected.Amount().String(), "expected_currency": expected.Currency().String(),
		"paid_amount": paid.Amount().String(), "paid_currency": paid.Currency().String()}
	msg := fmt.Sprintf("The latest matched payment was %s but %s was expected.", paid, expected)
	if expected.Currency() != paid.Currency() {
		facts["currency_differs"] = "true"
		msg = fmt.Sprintf("The latest matched payment was in %s but %s was expected (not converted).", paid.Currency(), expected)
	}
	return Condition{Kind: KindAmountChanged, ObjectType: ObjectCommitment, ObjectID: commitmentID,
		Key:     "commitment_amount_changed:" + commitmentID + ":" + expected.String() + ":" + paid.String(),
		Facts:   facts,
		Message: msg}, true
}

// UnmatchedOverdue is the condition for an occurrence past its due date with
// an outstanding amount and no matched payment; false when nothing is
// outstanding or it is not yet overdue.
func UnmatchedOverdue(occurrenceID string, due, today calendar.Date, outstanding money.Money) (Condition, bool) {
	if !outstanding.IsValid() || outstanding.Sign() <= 0 || due.IsZero() || !due.Before(today) {
		return Condition{}, false
	}
	return Condition{Kind: KindUnmatchedOverdue, ObjectType: ObjectOccurrence, ObjectID: occurrenceID,
		Key:     "unmatched_overdue:" + occurrenceID + ":" + due.String(),
		Facts:   map[string]string{"due_date": due.String(), "outstanding": outstanding.Amount().String(), "currency": outstanding.Currency().String()},
		Message: fmt.Sprintf("Overdue since %s with no matching payment recorded: %s still due.", due, outstanding)}, true
}

// Wording is optional model-written text for an alert.
type Wording struct {
	Text     string
	Provider string
	Model    string
}

// Alert is a stored alert.
type Alert struct {
	ID string
	Condition
	Wording   *Wording
	State     State
	CreatedAt time.Time
	ClosedAt  *time.Time
	Version   int64
}

// ErrNotOpen means the alert is already closed.
var ErrNotOpen = errors.New("the alert is not open")

// Store persists alerts (internal/store/alerts).
type Store interface {
	// Raise stores the condition as an open alert unless one with its key is
	// open or dismissed (a dismissal sticks); a resolved alert lets the key be
	// raised anew. created says which happened.
	Raise(ctx context.Context, actor audit.Actor, c Condition) (a Alert, created bool, err error)
	// SetWording attaches wording to an open alert that has none.
	SetWording(ctx context.Context, id string, w Wording) error
	// Close dismisses or resolves an open alert.
	Close(ctx context.Context, actor audit.Actor, id string, to State) (Alert, error)
	Get(ctx context.Context, id string) (Alert, error)
	// List returns alerts in a state (all when empty), newest first.
	List(ctx context.Context, state State, limit int) ([]Alert, error)
}

// Wordsmith writes optional wording for an alert (the alerts decision task).
// ok is false when none was written: disabled, no consent, an outage, invalid
// output. The alert is raised either way.
type Wordsmith interface {
	Word(ctx context.Context, a Alert) (w Wording, ok bool, err error)
}

// Channel is an optional delivery beyond the app, selected by the operator.
type Channel struct {
	Name   string
	Notify func(ctx context.Context, a Alert) error
}

// Service raises alerts.
type Service struct {
	store    Store
	words    Wordsmith
	channels []Channel
}

// New returns a Service. words may be nil (no model wording). Without channels
// alerts are in-app only.
func New(s Store, words Wordsmith, channels ...Channel) *Service {
	return &Service{store: s, words: words, channels: channels}
}

// Result is what raising did.
type Result struct {
	Alert Alert
	// Created is false when an open or a dismissed alert for the key already
	// existed (nothing was reworded or sent again; a dismissal sticks).
	Created bool
	// WordingErr is set when the optional model wording failed or could not be
	// stored; the alert stands without it and the channels still got it.
	WordingErr error
	// Delivery lists channel failures by name; the alert is stored regardless.
	Delivery map[string]error
}

// Raise stores the condition as an in-app alert, adds model wording when a
// wordsmith is set and answers, then hands it to each selected channel. A
// wording failure (reported in the result) or channel failure never loses the alert nor skips the channels.
func (s *Service) Raise(ctx context.Context, actor audit.Actor, c Condition) (Result, error) {
	if strings.TrimSpace(c.Message) == "" || c.Key == "" || c.ObjectID == "" {
		return Result{}, errors.New("alerts: a condition needs an object, a key and a message")
	}
	a, created, err := s.store.Raise(ctx, actor, c)
	if err != nil {
		return Result{}, err
	}
	res := Result{Alert: a, Created: created}
	if !created {
		return res, nil
	}
	if s.words != nil {
		if w, ok, err := s.words.Word(ctx, a); err != nil {
			res.WordingErr = err
		} else if ok {
			if err := s.store.SetWording(ctx, a.ID, w); err != nil {
				res.WordingErr = err
			} else {
				res.Alert.Wording = &w
			}
		}
	}
	for _, ch := range s.channels {
		if err := ch.Notify(ctx, res.Alert); err != nil {
			if res.Delivery == nil {
				res.Delivery = map[string]error{}
			}
			res.Delivery[ch.Name] = err
		}
	}
	return res, nil
}

// Dismiss closes an alert as the person's: seen, no action wanted.
func (s *Service) Dismiss(ctx context.Context, actor audit.Actor, id string) (Alert, error) {
	return s.store.Close(ctx, actor, id, StateDismissed)
}

// Resolve closes an alert because its condition no longer holds.
func (s *Service) Resolve(ctx context.Context, actor audit.Actor, id string) (Alert, error) {
	return s.store.Close(ctx, actor, id, StateResolved)
}
