package audit

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Limits on what an event may carry.
const (
	MaxTargetType = 32
	MaxTargetID   = 64
	MaxValueBytes = 256
)

// ErrInvalid wraps every reason [Validate] refuses an event.
var ErrInvalid = errors.New("audit: invalid event")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate checks an event before it is written. The log has no free-form
// payload: the action must be registered, Details may only use the fields the
// action declares, enumerated fields only their listed values, and values are
// short printable text. An error never repeats a value (it could be a secret
// that should not have been passed).
func Validate(e Event) error {
	fields, ok := Definitions[e.Action]
	if !ok {
		return invalid("unknown action %q", string(e.Action))
	}
	switch e.Actor.Kind {
	case ActorUser:
		if !resource.ValidID(e.Actor.UserID) || e.Actor.TokenID != "" {
			return invalid("a user actor needs a user ID and no token ID")
		}
	case ActorToken:
		if !resource.ValidID(e.Actor.UserID) || !resource.ValidID(e.Actor.TokenID) {
			return invalid("a token actor needs the token ID and its owner's user ID")
		}
	case ActorSystem, ActorAnonymous:
		if e.Actor.UserID != "" || e.Actor.TokenID != "" {
			return invalid("a %s actor has no user or token ID", e.Actor.Kind)
		}
	default:
		return invalid("unknown actor kind %q", string(e.Actor.Kind))
	}
	if e.At.IsZero() {
		return invalid("the event has no time")
	}
	if len(e.TargetType) > MaxTargetType || len(e.TargetID) > MaxTargetID || !printable(e.TargetType) || !printable(e.TargetID) {
		return invalid("target type or ID is too long or not printable")
	}
	for k, v := range e.Details {
		i := slices.IndexFunc(fields, func(fl Field) bool { return fl.Name == k })
		if i < 0 {
			return invalid("action %s has no detail field %q", string(e.Action), k)
		}
		if len(v) > MaxValueBytes || !printable(v) {
			return invalid("detail %q is too long or not printable", k)
		}
		if enum := fields[i].Enum; len(enum) > 0 && !slices.Contains(enum, v) {
			return invalid("detail %q is not one of its allowed values", k)
		}
	}
	return nil
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
