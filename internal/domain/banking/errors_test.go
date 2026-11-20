package banking

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestClassify(t *testing.T) {
	expired := &Error{Kind: KindReconnectRequired, Reason: ReasonConsentExpired, Provider: "fake", Op: "accounts", Code: "EXPIRED_SESSION"}
	for name, tc := range map[string]struct {
		err  error
		want ErrorKind
	}{
		"nil":                {nil, ""},
		"classified":         {expired, KindReconnectRequired},
		"wrapped classified": {fmt.Errorf("refresh: %w", &Error{Kind: KindRateLimited}), KindRateLimited},
		"retryable":          {&Error{Kind: KindRetryable}, KindRetryable},
		"permanent":          {&Error{Kind: KindPermanent}, KindPermanent},
		"canceled":           {fmt.Errorf("x: %w", context.Canceled), KindCanceled},
		"deadline":           {context.DeadlineExceeded, KindRetryable},
		"net timeout":        {&net.OpError{Op: "dial", Err: timeoutErr{}}, KindRetryable},
		"unclassified":       {errors.New("something odd"), KindPermanent},
	} {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: Classify = %q, want %q", name, got, tc.want)
		}
	}
}

func TestErrorSentinels(t *testing.T) {
	expired := fmt.Errorf("wrapped: %w", &Error{Kind: KindReconnectRequired, Reason: ReasonConsentExpired})
	revoked := &Error{Kind: KindReconnectRequired, Reason: ReasonConsentRevoked}
	limited := &Error{Kind: KindRateLimited, RetryAfter: 6 * time.Hour}
	for name, tc := range map[string]struct {
		err    error
		target error
		want   bool
	}{
		"expired is reconnect":       {expired, ErrReconnectRequired, true},
		"expired is consent expired": {expired, ErrConsentExpired, true},
		"revoked is reconnect":       {revoked, ErrReconnectRequired, true},
		"revoked is not expired":     {revoked, ErrConsentExpired, false},
		"limited is rate limited":    {limited, ErrRateLimited, true},
		"limited is not retryable":   {limited, ErrRetryable, false},
		"limited is not permanent":   {limited, ErrPermanent, false},
		"permanent":                  {&Error{Kind: KindPermanent}, ErrPermanent, true},
		"retryable":                  {&Error{Kind: KindRetryable}, ErrRetryable, true},
	} {
		if got := errors.Is(tc.err, tc.target); got != tc.want {
			t.Errorf("%s: errors.Is = %v, want %v", name, got, tc.want)
		}
	}
	if d, ok := RetryAfter(fmt.Errorf("x: %w", limited)); !ok || d != 6*time.Hour {
		t.Errorf("RetryAfter = %v, %v", d, ok)
	}
	if _, ok := RetryAfter(revoked); ok {
		t.Error("RetryAfter reported a delay for an error without one")
	}
}

func TestErrorUnwrapKeepsCause(t *testing.T) {
	e := &Error{Kind: KindRetryable, Err: context.DeadlineExceeded}
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Error("cause not reachable through Unwrap")
	}
}

func TestErrorMessageOmitsTheWrappedCause(t *testing.T) {
	// An adapter that wrapped a response body by mistake must still not leak
	// it through the message.
	e := &Error{Kind: KindPermanent, Provider: "fake", Op: "transactions", Code: "WRONG_TRANSACTIONS_PERIOD",
		Err: errors.New("body: {\"iban\":\"XX00FAKE0000000000000001\"}")}
	msg := e.Error()
	if want := "banking: fake transactions: permanent: WRONG_TRANSACTIONS_PERIOD"; msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
}

func TestStatusAfter(t *testing.T) {
	if s, ok := StatusAfter(&Error{Kind: KindReconnectRequired}); !ok || s != ConnectionReconnectRequired {
		t.Errorf("reconnect: %q, %v", s, ok)
	}
	for _, k := range []ErrorKind{KindRetryable, KindRateLimited, KindPermanent} {
		if _, ok := StatusAfter(&Error{Kind: k}); ok {
			t.Errorf("%s changed the connection status", k)
		}
	}
}
