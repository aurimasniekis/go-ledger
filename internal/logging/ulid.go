package logging

import (
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// NewULID returns a new ULID: 48 bits of millisecond time then 80 random bits,
// as 26 Crockford base32 characters. It sorts by creation time to the
// millisecond. Request and job IDs use the same encoding as resource
// identifiers (resource.NewID).
func NewULID() string { return resource.NewID() }

func ulidAt(t time.Time) string { return resource.NewIDAt(t) }
