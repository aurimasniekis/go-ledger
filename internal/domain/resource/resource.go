// Package resource holds the identity and concurrency vocabulary shared by
// every configurable object: ULID identifiers, the not-found error and the
// version-conflict error that optimistic concurrency produces (ADR-0011).
//
// The store returns these errors and the HTTP layer maps them to responses,
// so neither depends on the other.
package resource

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// IDLength is the length of an identifier: a ULID in Crockford base32.
const IDLength = 26

// NewID returns a new identifier: a ULID (48 bits of millisecond time, then
// 80 random bits) as 26 upper-case Crockford base32 characters. Identifiers
// sort by creation time to the millisecond; within one millisecond their order
// is random but fixed.
func NewID() string { return NewIDAt(time.Now()) }

// NewIDAt returns a new identifier whose time part is t. Tests use it to make
// ordered identifiers.
func NewIDAt(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:]) // never fails since Go 1.24
	var out [IDLength]byte
	var acc uint32
	bits, n := 2, 0 // 128 bits are written as 130, so two leading zero bits
	for _, x := range b {
		acc = acc<<8 | uint32(x)
		bits += 8
		for bits >= 5 {
			out[n] = crockford[(acc>>(bits-5))&31]
			n++
			bits -= 5
		}
		acc &= 1<<bits - 1
	}
	return string(out[:])
}

// ValidID reports whether s is an identifier in its canonical form: 26
// upper-case Crockford base32 characters whose first is 0-7 (a ULID never
// exceeds 128 bits). Lower case and the ambiguous letters I, L, O and U are
// refused, so one identifier has exactly one spelling.
func ValidID(s string) bool {
	if len(s) != IDLength || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z' && c != 'I' && c != 'L' && c != 'O' && c != 'U':
		default:
			return false
		}
	}
	return true
}

// ErrNotFound means the object does not exist (or is not visible to the
// caller). The HTTP layer answers it with 404.
var ErrNotFound = errors.New("not found")

// VersionConflictError means a write named a version that is no longer the
// object's current version: someone else changed it first. The HTTP layer
// answers it with 409 and reconciliation details.
type VersionConflictError struct {
	// Expected is the version the write was based on.
	Expected int64
	// Current is the version the object has now.
	Current int64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict: the write expected version %d, the current version is %d", e.Expected, e.Current)
}
