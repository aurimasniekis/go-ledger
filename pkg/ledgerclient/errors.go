package ledgerclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Error codes of the API's error envelope (ADR-0006, ADR-0011, ADR-0015).
// The set only grows: switch on the codes you handle and treat the rest by
// status.
const (
	CodeValidation      = "validation_failed"
	CodeUnauthenticated = "unauthenticated"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeVersionConflict = "version_conflict"
	CodePrecondition    = "precondition_required"
	CodePendingTerms    = "pending_terms_change"
	CodeRateLimited     = "rate_limited"
	CodeUnavailable     = "dependency_unavailable"
	CodeInternal        = "internal_error"
)

// FieldError is one invalid field of a validation error.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Reconciliation is sent with a version_conflict: the object's current
// version and, when the caller may read it, the object itself.
type Reconciliation struct {
	CurrentVersion int64           `json:"current_version"`
	Current        json.RawMessage `json:"current,omitempty"`
}

// DecodeCurrent decodes the current object into dst (for example a *Commitment).
// It fails when the server sent no object.
func (r *Reconciliation) DecodeCurrent(dst any) error {
	if r == nil || len(r.Current) == 0 {
		return errors.New("ledgerclient: the conflict carries no current object")
	}
	return json.Unmarshal(r.Current, dst)
}

// APIError is a non-2xx answer of the API, decoded from the error envelope.
// It never contains the bearer token.
type APIError struct {
	// Status is the HTTP status.
	Status int `json:"-"`
	// Code is the stable machine-readable code ("" when the answer was not an
	// API error envelope, for example from a proxy).
	Code      string       `json:"code"`
	Message   string       `json:"message"`
	Field     string       `json:"field,omitempty"`
	Errors    []FieldError `json:"errors,omitempty"`
	RequestID string       `json:"request_id"`
	// RetryAfter is the Retry-After header of the answer (0 when absent), set
	// for 429 and some 409/503 answers.
	RetryAfter time.Duration `json:"-"`
	// Reconciliation is set on a version_conflict only.
	Reconciliation *Reconciliation `json:"reconciliation,omitempty"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ledger API: %d", e.Status)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if len(e.Errors) > 1 {
		for _, f := range e.Errors {
			fmt.Fprintf(&b, "; %s: %s", f.Field, f.Message)
		}
	} else if e.Field != "" {
		fmt.Fprintf(&b, " (field %s)", e.Field)
	}
	return b.String()
}

// FieldErrors returns every invalid field of a validation error: Errors when
// the server listed several, else the single Field and Message.
func (e *APIError) FieldErrors() []FieldError {
	if len(e.Errors) > 0 {
		return e.Errors
	}
	if e.Field != "" {
		return []FieldError{{Field: e.Field, Message: e.Message}}
	}
	return nil
}

func asAPIError(err error) *APIError {
	var e *APIError
	if errors.As(err, &e) {
		return e
	}
	return nil
}

func hasCode(err error, code string) bool {
	e := asAPIError(err)
	return e != nil && e.Code == code
}

// IsNotFound reports a 404 not_found.
func IsNotFound(err error) bool { return hasCode(err, CodeNotFound) }

// IsVersionConflict reports a 409 version_conflict: the write was based on a
// stale version. Read the *APIError's Reconciliation, merge, and retry.
func IsVersionConflict(err error) bool { return hasCode(err, CodeVersionConflict) }

// IsConflict reports any other 409 (a taken name, pending_terms_change, ...).
func IsConflict(err error) bool {
	e := asAPIError(err)
	return e != nil && e.Status == http.StatusConflict && e.Code != CodeVersionConflict
}

// IsValidation reports a 400 validation_failed; see APIError.FieldErrors.
func IsValidation(err error) bool { return hasCode(err, CodeValidation) }

// IsUnauthenticated reports a 401 (no, wrong, expired or revoked token).
func IsUnauthenticated(err error) bool { return hasCode(err, CodeUnauthenticated) }

// IsForbidden reports a 403 forbidden: the token lacks the grant.
func IsForbidden(err error) bool { return hasCode(err, CodeForbidden) }
