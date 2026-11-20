package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

// Optimistic concurrency (ADR-0011, decision D17). Every mutable object has
// a version, starting at 1 and incremented by each change. Reads return it in
// the body ("version") and as a strong ETag ("\"<version>\""). A write to an
// existing object must name the version it was based on, in If-Match or in
// the body's "version"; a stale version is a 409 with reconciliation details,
// and a write that names none is a 428.

// ETag returns the entity tag for an object version: the decimal version in
// double quotes.
func ETag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// SetVersion sets the ETag header for an object version. Call it before
// writing the response.
func SetVersion(w http.ResponseWriter, version int64) { w.Header().Set("ETag", ETag(version)) }

// ExpectedVersion returns the version a write applies to. It reads If-Match
// and, when the request body has a version field, bodyVersion (nil when the
// body has none). Both may be given if they agree. It returns a 428 error when
// neither is present and a 400 when either is malformed, "*", weak, lists
// several tags or disagrees with the other.
func ExpectedVersion(r *http.Request, bodyVersion *int64) (int64, error) {
	var header int64
	values := r.Header.Values("If-Match")
	if len(values) > 0 {
		raw := strings.TrimSpace(strings.Join(values, ","))
		switch {
		case raw == "*":
			return 0, Validation("If-Match", "If-Match must name the version the change is based on, not *")
		case strings.HasPrefix(raw, "W/"):
			return 0, Validation("If-Match", "If-Match must be a strong entity tag as returned in ETag")
		case strings.Contains(raw, ","):
			return 0, Validation("If-Match", "If-Match must name exactly one version")
		}
		v, ok := parseETag(raw)
		if !ok {
			return 0, Validation("If-Match", "If-Match must be an entity tag as returned in ETag, such as \"3\"")
		}
		header = v
	}
	if bodyVersion != nil && *bodyVersion < 1 {
		return 0, Validation("version", "version must be a positive integer")
	}
	switch {
	case header != 0 && bodyVersion != nil && *bodyVersion != header:
		return 0, Validation("version", "version in the body and If-Match disagree")
	case header != 0:
		return header, nil
	case bodyVersion != nil:
		return *bodyVersion, nil
	}
	return 0, &Error{Status: http.StatusPreconditionRequired, Code: CodePrecondition,
		Message: "this change must name the version it is based on, in If-Match or the version field"}
}

func parseETag(s string) (int64, bool) {
	if len(s) < 3 || s[0] != '"' || s[len(s)-1] != '"' {
		return 0, false
	}
	digits := s[1 : len(s)-1]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || v < 1 {
		return 0, false
	}
	return v, true
}

// VersionConflict is the 409 for a write based on a stale version. current is
// the object's current representation (nil to send the version only); send
// it only to a caller allowed to read the object. The response carries the
// current ETag.
func VersionConflict(currentVersion int64, current any) *Error {
	return &Error{
		Status:  http.StatusConflict,
		Code:    CodeConflict,
		Message: "the object was changed by another request; review the current version and retry with it",
		Reconciliation: &Reconciliation{
			CurrentVersion: currentVersion,
			Current:        current,
		},
	}
}
