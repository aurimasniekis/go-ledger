package organisation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Limits on stored text.
const (
	MaxName   = 80
	MaxSource = 200
)

// Kind is a kind of definition.
type Kind string

// Definition kinds.
const (
	KindCategory Kind = "category"
	KindLabel    Kind = "label"
	KindBucket   Kind = "bucket"
)

// Kinds returns every kind.
func Kinds() []Kind { return []Kind{KindCategory, KindLabel, KindBucket} }

// Valid reports whether k is a kind.
func (k Kind) Valid() bool { return slices.Contains(Kinds(), k) }

// ObjectType is a kind of object definitions are assigned to.
type ObjectType string

// Object types (decision D12).
const (
	ObjectTransaction ObjectType = "transaction"
	ObjectCommitment  ObjectType = "commitment"
	ObjectAccount     ObjectType = "account"
	ObjectMerchant    ObjectType = "merchant"
)

// ObjectTypes returns every object type.
func ObjectTypes() []ObjectType {
	return []ObjectType{ObjectTransaction, ObjectCommitment, ObjectAccount, ObjectMerchant}
}

// Valid reports whether t is an object type.
func (t ObjectType) Valid() bool { return slices.Contains(ObjectTypes(), t) }

// BucketMember reports whether objects of type t may be in a bucket: buckets
// group commitments and accounts only (decision D12).
func (t ObjectType) BucketMember() bool { return t == ObjectCommitment || t == ObjectAccount }

// ObjectRef names one object by its ULID. A merchant is named by its merchant
// record's id (task M1, ADR-0058, decision D-MER 1a), no longer by a name key.
type ObjectRef struct {
	Type ObjectType
	ID   string
}

// Validate checks the reference's form (not that the object exists).
func (o ObjectRef) Validate() []commitments.FieldError {
	switch {
	case !o.Type.Valid():
		return []commitments.FieldError{{Field: "object_type", Message: "object_type must be transaction, commitment, account or merchant"}}
	case !resource.ValidID(o.ID):
		return []commitments.FieldError{{Field: "object_id", Message: "object_id must be an identifier"}}
	}
	return nil
}

// Origin is where an assignment came from.
type Origin string

// Origins. A person's word outranks a rule's, which outranks a model's.
const (
	OriginUser  Origin = "user"
	OriginRule  Origin = "rule"
	OriginModel Origin = "model"
)

// Origins returns every origin.
func Origins() []Origin { return []Origin{OriginUser, OriginRule, OriginModel} }

// Valid reports whether o is an origin.
func (o Origin) Valid() bool { return slices.Contains(Origins(), o) }

// Automatic reports whether o is a rule or a model.
func (o Origin) Automatic() bool { return o == OriginRule || o == OriginModel }

func (o Origin) rank() int {
	switch o {
	case OriginUser:
		return 3
	case OriginRule:
		return 2
	case OriginModel:
		return 1
	}
	return 0
}

// CanReplace reports whether an assignment of origin incoming may replace an
// existing one of origin existing. The person may replace anything; a rule
// replaces rules and models; a model replaces models only; nothing automatic
// replaces a user assignment. It is the one rule that keeps corrections alive
// through refreshes, rule runs and model runs.
func CanReplace(existing, incoming Origin) bool {
	if incoming == OriginUser {
		return true
	}
	return incoming.rank() >= existing.rank() && existing != OriginUser
}

// Definition is a category, a label or a bucket.
type Definition struct {
	ID   string
	Kind Kind
	Name string
	// ParentID is a category's parent; nil for a top-level category, a label
	// or a bucket.
	ParentID *string
	// ArchivedAt is when the definition was archived; nil while active.
	ArchivedAt *time.Time
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Archived reports whether the definition is archived.
func (d Definition) Archived() bool { return d.ArchivedAt != nil }

// Fields are the writable members of a definition.
type Fields struct {
	Name string
	// ParentID makes a category a child of another (categories only).
	ParentID *string
}

// NormalizeName collapses runs of white space and trims the ends.
func NormalizeName(s string) string { return strings.Join(strings.Fields(s), " ") }

// NameKey is the case-folded form names are unique by.
func NameKey(name string) string { return strings.ToLower(NormalizeName(name)) }

func checkText(s string) string {
	switch {
	case !utf8.ValidString(s):
		return "must be valid UTF-8"
	case strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0:
		return "must not contain control or invisible formatting characters"
	}
	return ""
}

// Normalized returns the fields with the name normalized.
func (f Fields) Normalized() Fields {
	f.Name = NormalizeName(f.Name)
	return f
}

// Validate checks the (normalized) fields for a definition of kind k.
func (f Fields) Validate(k Kind) []commitments.FieldError {
	var errs []commitments.FieldError
	switch msg := checkText(f.Name); {
	case f.Name == "":
		errs = append(errs, commitments.FieldError{Field: "name", Message: "name is required"})
	case msg != "":
		errs = append(errs, commitments.FieldError{Field: "name", Message: "name " + msg})
	case utf8.RuneCountInString(f.Name) > MaxName:
		errs = append(errs, commitments.FieldError{Field: "name", Message: fmt.Sprintf("name must be at most %d characters", MaxName)})
	}
	if f.ParentID != nil && k != KindCategory {
		errs = append(errs, commitments.FieldError{Field: "parent_id", Message: "only a category has a parent"})
	} else if f.ParentID != nil && !resource.ValidID(*f.ParentID) {
		errs = append(errs, commitments.FieldError{Field: "parent_id", Message: "parent_id must be an identifier"})
	}
	return errs
}

// ListFilter selects definitions for [Store.ListDefinitions], in ID order.
type ListFilter struct {
	After string
	// Limit is the most to return; the store fetches Limit+1 so the caller can
	// tell whether another page follows.
	Limit int
	// Archived selects archived (true) or active (false) definitions; nil all.
	Archived *bool
	// Parent selects the children of a category; a pointer to "" selects the
	// top-level categories. Categories only.
	Parent *string
	// NamePrefix selects names starting with it, ignoring case.
	NamePrefix string
}

// CategoryAssignment is an object's category. Category is nil when the person
// explicitly chose none, which automatic origins respect.
type CategoryAssignment struct {
	Category *Definition
	Origin   Origin
	// Source names the rule or provider/model of an automatic origin.
	Source string
	By     audit.Actor
	At     time.Time
	// Inherited is true when the assignment is the one of the pending
	// transaction this one superseded (the booking arrived under a new key).
	Inherited bool
}

// LabelAssignment is one label on an object. Removed marks a label the person
// took off: it stays recorded so automatic runs do not add it back.
type LabelAssignment struct {
	Label     Definition
	Origin    Origin
	Source    string
	By        audit.Actor
	At        time.Time
	Removed   bool
	Inherited bool
}

// Assignments is everything organisation says about one object.
type Assignments struct {
	Object ObjectRef
	// Category is nil when nothing is assigned.
	Category *CategoryAssignment
	// Labels are the labels on the object, by name; Removed lists the ones
	// the person took off (never shown as labels).
	Labels  []LabelAssignment
	Removed []LabelAssignment
	// Buckets are the buckets the object is in (commitments and accounts).
	Buckets []Definition
}

// MaxSelected bounds how many objects [Store.Select] may return: a filter that
// matches more must be narrowed, so a list request never builds an unbounded
// query.
const MaxSelected = 5000

// Filter selects objects by their effective organisation (decision D12,
// task O3). Within one list a repeated value matches any of them; the two
// lists combine (an object must satisfy both). A category also matches its
// children, so a parent selects its whole branch. An archived definition
// still matches: archiving keeps history.
type Filter struct {
	CategoryIDs []string
	LabelIDs    []string
}

// Empty reports whether the filter selects nothing in particular.
func (f Filter) Empty() bool { return len(f.CategoryIDs) == 0 && len(f.LabelIDs) == 0 }

// ErrTooManyObjects means a filter matches more than [MaxSelected] objects.
var ErrTooManyObjects = fmt.Errorf("the filter matches more than %d objects: narrow it", MaxSelected)

// UnknownDefinitionError means a filter names a category or label that does
// not exist.
type UnknownDefinitionError struct {
	Kind Kind
	ID   string
}

func (e *UnknownDefinitionError) Error() string { return "unknown " + string(e.Kind) + " " + e.ID }

// Member is an object in a bucket.
type Member struct {
	Object ObjectRef
	By     audit.Actor
	At     time.Time
}

// MemberFilter selects bucket members, ordered by object type then ID.
type MemberFilter struct {
	// After is the "type:id" key of the last member of the previous page.
	After string
	Limit int
}

// Automatic is what a rule or a model proposes for one object.
type Automatic struct {
	Origin Origin
	// Source names the rule or the provider/model: required.
	Source     string
	CategoryID *string
	LabelIDs   []string
}

// Validate checks the proposal's form.
func (a Automatic) Validate() []commitments.FieldError {
	var errs []commitments.FieldError
	if !a.Origin.Automatic() {
		errs = append(errs, commitments.FieldError{Field: "origin", Message: "origin must be rule or model"})
	}
	if msg := checkText(a.Source); a.Source == "" || msg != "" || utf8.RuneCountInString(a.Source) > MaxSource {
		errs = append(errs, commitments.FieldError{Field: "source", Message: "source is required, at most 200 characters, without control characters"})
	}
	if a.CategoryID != nil && !resource.ValidID(*a.CategoryID) {
		errs = append(errs, commitments.FieldError{Field: "category_id", Message: "category_id must be an identifier"})
	}
	for _, id := range a.LabelIDs {
		if !resource.ValidID(id) {
			errs = append(errs, commitments.FieldError{Field: "label_ids", Message: "label_ids must be identifiers"})
			break
		}
	}
	return errs
}

// KeptReason says why an automatic proposal was left unapplied.
type KeptReason string

// Reasons.
const (
	KeptUserDecision KeptReason = "user_decision" // the person assigned, cleared or removed it
	KeptHigherOrigin KeptReason = "higher_origin" // an assignment of a stronger automatic origin stands
	KeptArchived     KeptReason = "archived"      // the definition is archived
	KeptPresent      KeptReason = "present"       // the object already has it
)

// Kept is one proposal left unapplied.
type Kept struct {
	Kind   Kind
	ID     string
	Reason KeptReason
}

// AutomaticResult says what an automatic proposal changed.
type AutomaticResult struct {
	CategoryApplied bool
	LabelsApplied   []string
	Kept            []Kept
}

// Changed reports whether anything was written.
func (r AutomaticResult) Changed() bool { return r.CategoryApplied || len(r.LabelsApplied) > 0 }

// DeleteResult says what a deletion did.
type DeleteResult struct {
	// Reassigned is how many assignments moved to the replacement.
	Reassigned int
}

// Errors of the organisation store (beyond resource.ErrNotFound and
// *resource.VersionConflictError).
var (
	// ErrArchived means an archived definition cannot be assigned or used as a
	// parent: unarchive it first.
	ErrArchived = errors.New("the definition is archived: unarchive it to use it")
	// ErrDuplicateName means another definition has the name (among siblings
	// for a category).
	ErrDuplicateName = errors.New("another definition has this name")
	// ErrHasActiveChildren means a category with active children cannot be
	// archived: archive its children first.
	ErrHasActiveChildren = errors.New("the category has active children: archive them first")
	// ErrHasChildren means a category with children cannot be deleted.
	ErrHasChildren = errors.New("the category has children: delete or move them first")
	// ErrParentArchived means a category cannot be unarchived under an
	// archived parent.
	ErrParentArchived = errors.New("the parent category is archived: unarchive it first")
	// ErrNoAssignment means there is nothing to remove or reset.
	ErrNoAssignment = errors.New("there is no such assignment")
	// ErrAssignmentChanged means a conditional reset found the category is no
	// longer the automatic one it was meant to take back.
	ErrAssignmentChanged = errors.New("the category is no longer the automatic assignment it was asked to reset")
	// ErrInheritedAssignment means the assignment a reset would remove is
	// inherited from the pending transaction this one replaced; reset it there.
	ErrInheritedAssignment = errors.New("the assignment is inherited from the pending transaction it replaces; reset it there")
	// ErrNotApplicable means the definition cannot apply to the object type.
	ErrNotApplicable = errors.New("buckets hold commitments and accounts only")
	// ErrObjectArchived means the object is an archived (or merged) merchant:
	// nothing new is assigned to it (task M1, ADR-0058).
	ErrObjectArchived = errors.New("the merchant is archived or merged: nothing new is assigned to it")
)

// InUseError means a definition is still assigned and no replacement was
// named.
type InUseError struct{ Assignments int }

func (e *InUseError) Error() string {
	return fmt.Sprintf("the definition is assigned %d times: archive it instead, or name a replacement (reassign_to)", e.Assignments)
}

// Store is the persistence of definitions and assignments.
type Store interface {
	CreateDefinition(ctx context.Context, actor audit.Actor, k Kind, f Fields) (Definition, error)
	GetDefinition(ctx context.Context, k Kind, id string) (Definition, error)
	// ListDefinitions returns up to f.Limit+1 definitions after f.After.
	ListDefinitions(ctx context.Context, k Kind, f ListFilter) ([]Definition, error)
	// UpdateDefinition renames a definition or moves a category; fields are
	// the complete new writable state. A change to a stale version is a
	// *resource.VersionConflictError.
	UpdateDefinition(ctx context.Context, actor audit.Actor, k Kind, id string, expected int64, f Fields) (Definition, error)
	SetArchived(ctx context.Context, actor audit.Actor, k Kind, id string, expected int64, archived bool) (Definition, error)
	// DeleteDefinition deletes an unused definition; one that is assigned
	// needs reassignTo, the replacement the assignments move to (ErrArchived
	// if it is archived).
	DeleteDefinition(ctx context.Context, actor audit.Actor, k Kind, id string, expected int64, reassignTo string) (DeleteResult, error)

	// Get returns what is assigned to an object (resource.ErrNotFound when
	// the object does not exist).
	Get(ctx context.Context, obj ObjectRef) (Assignments, error)
	// SetCategory records the person's category; a nil categoryID records
	// that the person chose none. ResetCategory forgets the person's choice,
	// so automatic origins may assign again.
	SetCategory(ctx context.Context, actor audit.Actor, obj ObjectRef, categoryID *string) (Assignments, error)
	ResetCategory(ctx context.Context, actor audit.Actor, obj ObjectRef) (Assignments, error)
	// AddLabel adds a label as the person's; RemoveLabel takes one off and
	// remembers that, so automatic origins do not add it back.
	AddLabel(ctx context.Context, actor audit.Actor, obj ObjectRef, labelID string) (Assignments, error)
	RemoveLabel(ctx context.Context, actor audit.Actor, obj ObjectRef, labelID string) (Assignments, error)
	// AddToBucket and RemoveFromBucket manage membership (commitments and
	// accounts); ListMembers pages through a bucket.
	AddToBucket(ctx context.Context, actor audit.Actor, bucketID string, obj ObjectRef) error
	RemoveFromBucket(ctx context.Context, actor audit.Actor, bucketID string, obj ObjectRef) error
	ListMembers(ctx context.Context, bucketID string, f MemberFilter) ([]Member, error)
	// MemberIDs returns the IDs of every member of a bucket of type t, sorted
	// (resource.ErrNotFound for an unknown bucket). It backs the bucket slice
	// of the outlook.
	MemberIDs(ctx context.Context, bucketID string, t ObjectType) ([]string, error)
	// Select returns the IDs of the objects of type t whose effective
	// organisation satisfies f, sorted: what the list filters use. A
	// transaction's effective organisation includes what it inherits from the
	// pending transaction it superseded, like [Store.Get]. It returns
	// *UnknownDefinitionError for a category or label that does not exist and
	// ErrTooManyObjects beyond [MaxSelected].
	Select(ctx context.Context, t ObjectType, f Filter) ([]string, error)
	// ApplyAutomatic applies a rule's or a model's proposal for one object,
	// never replacing what [CanReplace] forbids.
	ApplyAutomatic(ctx context.Context, actor audit.Actor, obj ObjectRef, a Automatic) (AutomaticResult, error)
}
