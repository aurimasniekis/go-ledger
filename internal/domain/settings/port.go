package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
)

// Audit actions of a settings write (ADR-0066 point 20). Details carry the
// group and the member names, never a value.
const (
	ActionUpdated = audit.SettingsUpdated
	ActionReset   = audit.SettingsReset
)

// Stored is one group's row as the store keeps it. A group without a row has
// Exists false and the synthesised version 1 (ADR-0066 point 9).
type Stored struct {
	Name      string
	Exists    bool
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
	// Values holds only the members set through the API, by member name, in
	// the canonical JSON form of [config.NormalizeStored].
	Values map[string]json.RawMessage
}

// Change is what a write callback decides: the group's next stored values and
// the audit event recording them. A nil Event means nothing changes: the row,
// its version and the audit log stay as they are.
type Change struct {
	Values map[string]json.RawMessage
	Event  *audit.Event
}

// Store keeps the stored settings layer (the setting_groups table).
type Store interface {
	// Groups returns every stored row.
	Groups(ctx context.Context) ([]Stored, error)
	// Write runs decide with the group's current row inside one write
	// transaction, after checking that its version is expected (1 when there
	// is no row yet, else a *resource.VersionConflictError). When decide
	// returns an event, the store inserts the row with version 2 or updates
	// it with the next version, sets the event's time, target and actor
	// and writes it in the same transaction. It returns the row as it is
	// after the write.
	Write(ctx context.Context, group string, expected int64, decide func(cur Stored) (Change, error)) (Stored, error)
}

// ErrUnknownGroup is a group name the registry does not have.
var ErrUnknownGroup = errors.New("settings: no such settings group")

// FieldError is one problem with one member of a write. Field is the
// configuration key when the member is known, else the member name as sent.
// Message never repeats a secret; like "ledger config check", it may quote an
// ordinary value that was refused (an unknown enum value, a zone name).
type FieldError struct {
	Field   string
	Message string
}

// ValidationError lists every problem a write has (400 validation_failed).
type ValidationError struct{ Fields []FieldError }

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		if f.Field == "" {
			parts[i] = f.Message
			continue
		}
		parts[i] = f.Field + ": " + f.Message
	}
	return "settings: invalid write: " + strings.Join(parts, "; ")
}

// PinnedKey is one key a configuration layer sets.
type PinnedKey struct {
	// Key is the configuration key and Layer the configuration layer that
	// sets it: yaml, environment, dotenv, flag or file.
	Key, Layer string
}

// PinnedError is a write that names pinned keys (409 setting_pinned). It names
// the keys and their layers, never a value.
type PinnedError struct{ Keys []PinnedKey }

func (e *PinnedError) Error() string {
	parts := make([]string, len(e.Keys))
	for i, k := range e.Keys {
		parts[i] = k.Key + " (set by " + k.Layer + ")"
	}
	return "settings: pinned by the configuration: " + strings.Join(parts, ", ")
}

// MsgNotWritableYet refuses a member whose gate does not exist yet: sensitive
// members and secrets wait for recent authentication and the root token (task
// S2), the reporting zone for the reclassification rule (task S3).
const MsgNotWritableYet = "not writable through the API yet"
