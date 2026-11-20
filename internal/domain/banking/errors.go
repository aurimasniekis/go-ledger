package banking

import (
	"context"
	"errors"
	"net"
	"time"
)

// ErrorKind classifies provider failures by what Ledger should do next.
type ErrorKind string

const (
	// KindReconnectRequired: consent expired or was revoked, or the provider
	// session is gone. Retrying cannot help; the user must re-authorise. The
	// connection becomes reconnect_required and its data stays visible as
	// stale.
	KindReconnectRequired ErrorKind = "reconnect_required"
	// KindRateLimited: the provider or bank refused for now. Retry no
	// earlier than RetryAfter (or the provider's documented guidance).
	KindRateLimited ErrorKind = "rate_limited"
	// KindRetryable: a transient failure (timeout, network error, provider
	// or bank unavailable). An idempotent read may be retried with backoff
	// (decision D17: at most 3 retries, jitter, honour Retry-After).
	KindRetryable ErrorKind = "retryable"
	// KindPermanent: the request cannot succeed as made (bad input, an
	// unsupported institution or period, a configuration error). Do not
	// retry; surface it.
	KindPermanent ErrorKind = "permanent"
	// KindCanceled: Ledger cancelled the call itself (shutdown or a
	// cancelled request). Not a provider failure.
	KindCanceled ErrorKind = "canceled"
)

// Reason refines [KindReconnectRequired].
type Reason string

const (
	ReasonNone           Reason = ""
	ReasonConsentExpired Reason = "consent_expired"
	ReasonConsentRevoked Reason = "consent_revoked"
	ReasonSessionInvalid Reason = "session_invalid"
)

// Sentinels for errors.Is. Each matches every [*Error] of its kind;
// [ErrConsentExpired] matches only the consent-expired reason.
var (
	ErrReconnectRequired = errors.New("banking: reconnect required")
	ErrConsentExpired    = errors.New("banking: consent expired")
	ErrRateLimited       = errors.New("banking: rate limited")
	ErrRetryable         = errors.New("banking: temporary provider failure")
	ErrPermanent         = errors.New("banking: permanent provider failure")
)

// Error is a classified provider failure. Its message holds only the
// provider name, the operation, the kind, the reason and the provider's
// error code: never account identifiers, session references, descriptions
// or response bodies. Err is kept for errors.Is/As and must itself be free of
// those (adapters wrap sanitized errors only); it is not printed.
type Error struct {
	Kind     ErrorKind
	Reason   Reason
	Provider string
	// Op is the operation, such as "transactions" or "complete_consent".
	Op string
	// Code is the provider's machine-readable error code, if any (for Enable
	// Banking, for example, EXPIRED_SESSION).
	Code string
	// RetryAfter is the provider's requested delay, zero if none.
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	msg := "banking: " + e.Provider + " " + e.Op + ": " + string(e.Kind)
	if e.Reason != ReasonNone {
		msg += " (" + string(e.Reason) + ")"
	}
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.RetryAfter > 0 {
		msg += "; retry after " + e.RetryAfter.String()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches the kind sentinels.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrReconnectRequired:
		return e.Kind == KindReconnectRequired
	case ErrConsentExpired:
		return e.Kind == KindReconnectRequired && e.Reason == ReasonConsentExpired
	case ErrRateLimited:
		return e.Kind == KindRateLimited
	case ErrRetryable:
		return e.Kind == KindRetryable
	case ErrPermanent:
		return e.Kind == KindPermanent
	}
	return false
}

// Classify returns the kind of err. A classified [*Error] keeps its kind.
// Unclassified errors: context cancellation is [KindCanceled]; a deadline or
// a network timeout is [KindRetryable]; anything else is [KindPermanent], so
// an adapter that forgets to classify never causes a retry storm. nil
// returns "".
func Classify(err error) ErrorKind {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Kind
	}
	if errors.Is(err, context.Canceled) {
		return KindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindRetryable
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return KindRetryable
	}
	return KindPermanent
}

// RetryAfter returns the provider's requested delay, if err carries one.
func RetryAfter(err error) (time.Duration, bool) {
	if e, ok := errors.AsType[*Error](err); ok && e.RetryAfter > 0 {
		return e.RetryAfter, true
	}
	return 0, false
}

// StatusAfter returns the connection status a failure leads to, and whether
// it changes the status at all. Only reconnect-required failures do;
// retryable, rate-limited and permanent failures leave the connection as it
// is and are reported on the refresh attempt instead (B2).
func StatusAfter(err error) (ConnectionStatus, bool) {
	if Classify(err) == KindReconnectRequired {
		return ConnectionReconnectRequired, true
	}
	return "", false
}
