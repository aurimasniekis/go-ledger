package banking

import (
	"fmt"
	"log/slog"
)

// Mask is what a [Sensitive] value prints, logs and marshals as.
const Mask = "[REDACTED]"

// Sensitive holds a value that must never reach a log line, an error message
// or an API response by accident: an IBAN, a masked card number, a provider
// session reference or an authorisation code. Every formatting path (fmt
// verbs, slog, JSON and text marshalling) prints [Mask]; [Sensitive.Reveal]
// returns the value for the few places that need it (the store and the
// adapter that sends it back to the provider).
type Sensitive struct {
	v string
}

// NewSensitive wraps s.
func NewSensitive(s string) Sensitive { return Sensitive{v: s} }

// Reveal returns the wrapped value. Call it only to store the value or to send
// it to the provider it came from.
func (s Sensitive) Reveal() string { return s.v }

// IsZero reports whether no value is held.
func (s Sensitive) IsZero() bool { return s.v == "" }

// Equal compares two values without revealing them.
func (s Sensitive) Equal(o Sensitive) bool { return s.v == o.v }

// String returns [Mask], or "" when no value is held.
func (s Sensitive) String() string {
	if s.v == "" {
		return ""
	}
	return Mask
}

// GoString keeps %#v masked too.
func (s Sensitive) GoString() string { return "banking.Sensitive{" + s.String() + "}" }

// Format masks every fmt verb, including %s, %v, %q, %x and %#v.
func (s Sensitive) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = fmt.Fprint(f, s.GoString())
		return
	}
	_, _ = fmt.Fprint(f, s.String())
}

// LogValue implements slog.LogValuer.
func (s Sensitive) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalText implements encoding.TextMarshaler with the mask, so encoding a
// struct that holds a Sensitive never leaks it. API code that must expose a
// value (for example a masked IBAN) does so explicitly.
func (s Sensitive) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
