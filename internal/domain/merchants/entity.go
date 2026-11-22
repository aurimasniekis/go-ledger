package merchants

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
)

// EntityTargetType is the audit target type of merchant records (task M1,
// ADR-0058); the target ID is the merchant's.
const EntityTargetType = "merchant"

// Entity is a merchant record (decision D-MER 1a): a stable identifier, the
// name to show and the alias names transactions are matched by. The person's
// decisions about transactions (corrections and confirmations, ADR-0051) and
// organisation assignments (ADR-0053) refer to it by ID, so renaming it or
// adding a spelling keeps them.
type Entity struct {
	ID          string
	DisplayName string
	// Aliases are the names that resolve to this merchant, sorted by key.
	Aliases []Alias
	// MergedInto is the merchant this one was merged into, empty unless it
	// was. A merged merchant is archived and keeps nothing of its own.
	MergedInto string
	// ArchivedAt is set while the merchant is archived: it is kept with
	// everything that refers to it, but nothing new is attached to it.
	ArchivedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Version    int64
}

// Archived reports whether the merchant is archived (merged ones are too).
func (e Entity) Archived() bool { return e.ArchivedAt != nil }

// AliasOrigin says how an alias came to be.
type AliasOrigin string

// Alias origins.
const (
	AliasUser     AliasOrigin = "user"     // added through the merchant API
	AliasDecision AliasOrigin = "decision" // a confirmation or correction named it
	AliasUpgrade  AliasOrigin = "upgrade"  // made by migration 0030 from earlier data
)

// Alias is one name that resolves to a merchant. Key is the name as matched
// (see [AliasKey]) and is unique across all merchants.
type Alias struct {
	ID        string
	Name      string
	Key       string
	Origin    AliasOrigin
	CreatedAt time.Time
}

// AliasKey returns the key a name is matched by: Unicode NFC, white space
// collapsed to single spaces and trimmed, lower case. "Café  Shop" written
// with a combining accent and "café shop" written with the precomposed letter
// give the same key. ok is false for a name that is not valid display text
// (empty, over 200 characters, control or invisible formatting characters).
// The same function backs the SQL function ledger_merchant_key that migration
// 0030 calls, so keys made by the upgrade and by the store are identical.
func AliasKey(name string) (string, bool) {
	if !utf8.ValidString(name) {
		return "", false
	}
	k := strings.Join(strings.Fields(norm.NFC.String(name)), " ")
	k = norm.NFC.String(strings.ToLower(k))
	if checkText(k, MaxName, false) != "" {
		return "", false
	}
	return k, true
}

// NormalizeDisplayName trims a name and collapses runs of white space; it
// keeps the case and composes the text (NFC), so equal-looking names are
// stored alike.
func NormalizeDisplayName(name string) string {
	if !utf8.ValidString(name) {
		return name
	}
	return strings.Join(strings.Fields(norm.NFC.String(name)), " ")
}

// CheckName reports a field error for a display or alias name, or nil.
func CheckName(field, name string) []commitments.FieldError {
	if msg := checkText(name, MaxName, false); msg != "" {
		return []commitments.FieldError{{Field: field, Message: field + " " + msg}}
	}
	return nil
}

// MergeResult counts what a merge moved to the remaining merchant. Kept counts
// the assignments of the merged merchant that were not moved because the
// remaining one already had its own for the same category slot or label (its
// own assignment stands; see [EntityStore.Merge]).
type MergeResult struct {
	Into        Entity
	Aliases     int
	Decisions   int
	Enrichments int
	Categories  int
	Labels      int
	Kept        int
}

// Errors of the merchant records.
var (
	// ErrAliasTaken means the alias key already resolves to another
	// merchant: merge the two instead, or remove the alias there first.
	ErrAliasTaken = errors.New("the alias already names another merchant")
	// ErrArchived means the merchant is archived (or merged): nothing new is
	// attached to it.
	ErrArchived = errors.New("the merchant is archived")
	// ErrMerged means the merchant was merged into another and cannot change.
	ErrMerged = errors.New("the merchant was merged into another merchant")
	// ErrInUse means a decision or an organisation assignment still refers to
	// the merchant, so it cannot be deleted (archive or merge it instead).
	ErrInUse = errors.New("decisions or assignments refer to the merchant: archive or merge it instead")
	// ErrMergeSelf means a merge named the same merchant twice.
	ErrMergeSelf = errors.New("a merchant cannot be merged into itself")
	// ErrNoAlias means the merchant has no such alias.
	ErrNoAlias = errors.New("the merchant has no such alias")
)

// EntityFilter selects merchants for a list.
type EntityFilter struct {
	// Archived includes archived (and merged) merchants when true; the
	// default lists active ones only.
	Archived bool
	// After is the last ID of the previous page; Limit the page size.
	After string
	Limit int
}

// EntityStore is the persistence of merchant records. Every write is one
// transaction with its audit event.
type EntityStore interface {
	ListEntities(ctx context.Context, f EntityFilter) ([]Entity, error)
	GetEntity(ctx context.Context, id string) (Entity, error)
	// CreateEntity makes a merchant; the display name is its first alias and
	// aliases are further names (ErrAliasTaken when one names another).
	CreateEntity(ctx context.Context, actor audit.Actor, displayName string, aliases []string) (Entity, error)
	// RenameEntity changes the display name and adds it as an alias when no
	// merchant has it yet (ErrAliasTaken when another merchant has it).
	RenameEntity(ctx context.Context, actor audit.Actor, id string, expected int64, displayName string) (Entity, error)
	SetEntityArchived(ctx context.Context, actor audit.Actor, id string, expected int64, archived bool) (Entity, error)
	// DeleteEntity removes a merchant nothing refers to (ErrInUse otherwise).
	DeleteEntity(ctx context.Context, actor audit.Actor, id string, expected int64) error
	AddAlias(ctx context.Context, actor audit.Actor, id string, name string) (Entity, error)
	RemoveAlias(ctx context.Context, actor audit.Actor, id string, aliasID string) (Entity, error)
	// Merge moves everything of merchant from into merchant into atomically
	// and leaves from archived, pointing at into.
	Merge(ctx context.Context, actor audit.Actor, into string, expected int64, from string) (MergeResult, error)
}
