package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/redact"
)

// Code is a stable, machine-readable error code. Clients may switch on it; the
// set only grows, and a code's meaning never changes.
type Code string

const (
	CodeValidation      Code = "validation_failed"      // 400/422: the request is malformed or a field is invalid
	CodeUnauthenticated Code = "unauthenticated"        // 401: no or invalid credentials
	CodeForbidden       Code = "forbidden"              // 403: authenticated but not allowed
	CodeConflict        Code = "conflict"               // 409: the request conflicts with current state
	CodeRateLimited     Code = "rate_limited"           // 429
	CodeUnavailable     Code = "dependency_unavailable" // 503: a required dependency is down
	CodeInternal        Code = "internal_error"         // 500: unexpected; details are in the server log
	CodeNotFound        Code = "not_found"              // 404
	CodeTooLarge        Code = "payload_too_large"      // 413
	CodeMethod          Code = "method_not_allowed"     // 405
	CodePrecondition    Code = "precondition_required"  // 428: a write did not say which version it applies to
	CodeVersionConflict Code = "version_conflict"       // 409: the write was based on a stale version; carries reconciliation
	CodeUnsupportedType Code = "unsupported_media_type" // 415: the request body's media type is not accepted here
	// CodeInvalidCredentials: a username and password (sign-in, 401) or a
	// password (re-authentication, 403: the session stays valid) did not
	// match. The same answer for every reason (ADR-0026).
	CodeInvalidCredentials Code = "invalid_credentials"
	CodeCSRF               Code = "csrf_failed"               // 403: a cookie-authenticated write without the session's CSRF token, or from another origin
	CodeReauthRequired     Code = "reauthentication_required" // 403: the action needs a recent password check (POST /auth/reauthenticate)
	CodePendingTerms       Code = "pending_terms_change"      // 409: a commitment terms edit is refused because an earlier edit takes effect later
	// CodeEnrollmentRequired: 403, auth.require_2fa is on and the signed-in
	// user has not enrolled a second factor; only enrolment, the current
	// session, re-authentication and sign-out are reachable (ADR-0030).
	CodeEnrollmentRequired Code = "two_factor_enrollment_required"
	// CodeInvalidSecondFactor: a TOTP or recovery code did not match, was
	// malformed or was already used (401 at sign-in, 400 when confirming an
	// enrolment). The same answer for every reason.
	CodeInvalidSecondFactor Code = "invalid_second_factor"
	CodeEvidenceRequired    Code = "payment_evidence_required" // 409: a "paid" status override for an occurrence with no payment evidence
	// CodeOccurrenceCancelled: 409, a payment, match or status override for a
	// cancelled occurrence (D31).
	CodeOccurrenceCancelled Code = "occurrence_cancelled"
	// CodePasswordSignInDisabled: 403, auth.password.enabled is false, so
	// password sign-in and password re-authentication are refused; use
	// single sign-on (ADR-0033).
	CodePasswordSignInDisabled Code = "password_sign_in_disabled"
)

// ErrorBody is the JSON error envelope sent for every non-2xx API response.
type ErrorBody struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	// Errors lists every invalid field of a validation error that found more
	// than one problem (Field and Message repeat the first). Absent otherwise.
	Errors    []FieldDetail `json:"errors,omitempty"`
	RequestID string        `json:"request_id"`
	// Reconciliation is present on a version conflict (409) only.
	Reconciliation *Reconciliation `json:"reconciliation,omitempty"`
}

// FieldDetail is one invalid field of a validation error.
type FieldDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Reconciliation tells a client whose write lost an optimistic-concurrency
// race what the object looks like now, so it can merge and retry with
// current_version (ADR-0011, decision D17).
type Reconciliation struct {
	// CurrentVersion is the object's version now; retry with it in If-Match.
	CurrentVersion int64 `json:"current_version"`
	// Current is the object's current representation, when the handler
	// supplied it.
	Current any `json:"current,omitempty"`
}

// Error is an error that knows how to present itself to API clients. Message
// must be safe to show: never put secrets, SQL or stack traces in it.
type Error struct {
	Status  int
	Code    Code
	Message string
	Field   string
	// Errors are every invalid field when a validation found several; see
	// [ValidationFields].
	Errors []FieldDetail
	// Reconciliation is sent with a version conflict; see [VersionConflict].
	Reconciliation *Reconciliation
	// Cause is logged, never sent.
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Validation is a 400 about the named field ("" for the request as a whole).
func Validation(field, message string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: CodeValidation, Message: message, Field: field}
}

// ValidationFields is a 400 listing every invalid field. Field and Message
// are the first entry's; Errors carries all of them when there are several.
// It panics on an empty list.
func ValidationFields(fields []FieldDetail) *Error {
	e := Validation(fields[0].Field, fields[0].Message)
	if len(fields) > 1 {
		e.Errors = fields
	}
	return e
}

func Unauthenticated(message string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Message: message}
}

func Forbidden(message string) *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: message}
}

func NotFound(message string) *Error {
	return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: message}
}

// Conflict is a 409 for a state conflict other than a stale version, such as
// a name that is already taken. A stale version is [VersionConflict].
func Conflict(message string) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeConflict, Message: message}
}

// MethodNotAllowed is a 405; allow is the Allow header the mux computed.
func MethodNotAllowed(allow string) *Error {
	msg := "method not allowed"
	if allow != "" {
		msg += "; allowed: " + allow
	}
	return &Error{Status: http.StatusMethodNotAllowed, Code: CodeMethod, Message: msg}
}

func RateLimited(message string) *Error {
	return &Error{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: message}
}

// Unavailable reports a failed dependency; cause is logged, not sent.
func Unavailable(message string, cause error) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: message, Cause: cause}
}

// Internal wraps an unexpected failure. The client sees a generic message.
func Internal(cause error) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal error", Cause: cause}
}

func csrfFailed() *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeCSRF, Message: "missing or invalid CSRF token, or a cross-origin request"}
}

func enrollmentRequired() *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeEnrollmentRequired,
		Message: "two-factor authentication is required: enrol an authenticator (POST /auth/two-factor/totp) before anything else"}
}

func invalidSecondFactor(status int) *Error {
	return &Error{Status: status, Code: CodeInvalidSecondFactor, Message: auth.ErrInvalidSecondFactor.Error()}
}

func reauthRequired() *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeReauthRequired, Message: "this action needs recent authentication: confirm your password first"}
}

func unsupportedMediaType(message string) *Error {
	return &Error{Status: http.StatusUnsupportedMediaType, Code: CodeUnsupportedType, Message: message}
}

func tooLarge(limit int64) *Error {
	return &Error{Status: http.StatusRequestEntityTooLarge, Code: CodeTooLarge,
		Message: fmt.Sprintf("request body exceeds the limit of %d bytes", limit)}
}

// WriteError sends err as the error envelope. Anything that is not an *Error
// (other than an oversized body) becomes a generic 500 and its detail goes to
// the request's log only. The message is passed through the redactor.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *Error
	var maxErr *http.MaxBytesError
	var conflict *resource.VersionConflictError
	switch {
	case errors.As(err, &apiErr):
	case errors.As(err, &maxErr):
		apiErr = tooLarge(maxErr.Limit)
	case errors.As(err, &conflict):
		// The handler did not attach the current object; the version alone
		// still lets the client re-read and retry.
		apiErr = VersionConflict(conflict.Current, nil)
	case errors.Is(err, resource.ErrNotFound):
		apiErr = NotFound("no such resource")
	default:
		apiErr = Internal(err)
	}
	if i := infoFrom(r.Context()); i != nil {
		i.kind = string(apiErr.Code)
	}
	log := logging.FromContext(r.Context())
	if apiErr.Status >= 500 {
		log.ErrorContext(r.Context(), "request failed", "code", string(apiErr.Code), "error", errString(apiErr))
	}
	body := ErrorBody{
		Code:      apiErr.Code,
		Message:   redact.Default().Text(apiErr.Message),
		Field:     apiErr.Field,
		RequestID: logging.RequestID(r.Context()),
	}
	for _, fd := range apiErr.Errors {
		body.Errors = append(body.Errors, FieldDetail{Field: fd.Field, Message: redact.Default().Text(fd.Message)})
	}
	if rc := apiErr.Reconciliation; rc != nil {
		body.Reconciliation = rc
		w.Header().Set("ETag", ETag(rc.CurrentVersion))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if apiErr.Status == http.StatusRequestEntityTooLarge {
		w.Header().Set("Connection", "close")
	}
	w.WriteHeader(apiErr.Status)
	_ = json.NewEncoder(w).Encode(body)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// DecodeJSON reads the request body as one JSON value into dst. Unknown fields
// are rejected. An oversized body yields the 413 error, anything else
// malformed a validation error; pass the result to WriteError.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return tooLarge(maxErr.Limit)
		}
		return Validation("", "request body is not valid JSON for this endpoint")
	}
	if dec.More() {
		return Validation("", "request body must contain a single JSON value")
	}
	return nil
}
