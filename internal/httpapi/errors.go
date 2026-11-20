package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
)

// ErrorBody is the JSON error envelope sent for every non-2xx API response.
type ErrorBody struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Field     string `json:"field,omitempty"`
	RequestID string `json:"request_id"`
}

// Error is an error that knows how to present itself to API clients. Message
// must be safe to show: never put secrets, SQL or stack traces in it.
type Error struct {
	Status  int
	Code    Code
	Message string
	Field   string
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

func Unauthenticated(message string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Message: message}
}

func Forbidden(message string) *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: message}
}

func NotFound(message string) *Error {
	return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: message}
}

func Conflict(message string) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeConflict, Message: message}
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
	switch {
	case errors.As(err, &apiErr):
	case errors.As(err, &maxErr):
		apiErr = tooLarge(maxErr.Limit)
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
