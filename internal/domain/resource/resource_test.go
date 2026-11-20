package resource

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNewIDIsValidAndOrderedByTime(t *testing.T) {
	a := NewIDAt(time.UnixMilli(1_700_000_000_000))
	b := NewIDAt(time.UnixMilli(1_700_000_001_000))
	if !ValidID(a) || !ValidID(b) {
		t.Fatalf("generated IDs are not valid: %q %q", a, b)
	}
	if a >= b {
		t.Fatalf("IDs do not sort by time: %q >= %q", a, b)
	}
	if first, second := NewID(), NewID(); first == second {
		t.Fatal("two new IDs are equal")
	}
	// The time part is fixed by the millisecond.
	if a[:10] != NewIDAt(time.UnixMilli(1_700_000_000_000))[:10] {
		t.Fatal("time part differs for the same millisecond")
	}
}

func TestValidIDRefusesNonCanonicalSpellings(t *testing.T) {
	good := NewID()
	for _, s := range []string{
		"",
		good[:25],
		good + "0",
		strings.ToLower(good),
		"8" + good[1:], // over 128 bits
		good[:25] + "I",
		good[:25] + "L",
		good[:25] + "O",
		good[:25] + "U",
		good[:25] + "-",
	} {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true", s)
		}
	}
	if !ValidID("7ZZZZZZZZZZZZZZZZZZZZZZZZZ") || !ValidID("00000000000000000000000000") {
		t.Error("boundary IDs refused")
	}
}

func TestVersionConflictError(t *testing.T) {
	err := fmt.Errorf("update: %w", &VersionConflictError{Expected: 2, Current: 3})
	var vc *VersionConflictError
	if !errors.As(err, &vc) || vc.Current != 3 || vc.Expected != 2 {
		t.Fatalf("errors.As failed: %v", err)
	}
	if !strings.Contains(err.Error(), "expected version 2") {
		t.Fatalf("message: %v", err)
	}
}
