package ledgerclient

import (
	"context"
	"iter"
	"net/url"
)

// Definition kinds of the organisation model (ADR-0053).
const (
	KindCategory = "category"
	KindLabel    = "label"
	KindBucket   = "bucket"
)

// Definition is a category, label or bucket. A bucket groups commitments and
// accounts to slice the outlook; it never holds or allocates money.
type Definition struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	ParentID   *string `json:"parent_id"`
	Archived   bool    `json:"archived"`
	ArchivedAt *string `json:"archived_at"`
	Version    int64   `json:"version"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// NewDefinition is the body of a create. ParentID is for categories only.
type NewDefinition struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id,omitempty"`
}

// DefinitionFilter filters the list of one kind of definition. Archived is
// "true", "false" or "" (both). ParentID is "none" (top-level categories), a
// category id or "" (any), and is for categories only.
type DefinitionFilter struct {
	ListOptions
	Archived   string
	ParentID   string
	NamePrefix string
}

func (f DefinitionFilter) query(kind string) url.Values {
	q := url.Values{}
	f.apply(q)
	setIf(q, "archived", f.Archived)
	if kind == KindCategory {
		setIf(q, "parent_id", f.ParentID)
	}
	setIf(q, "name[prefix]", f.NamePrefix)
	return q
}

// OrganisationActor says who set an assignment and how.
type OrganisationActor struct {
	Kind   string  `json:"kind"`
	UserID *string `json:"user_id"`
	Via    *string `json:"via"`
}

// CategoryAssignment is an object's category with its origin. Category is nil
// when the person explicitly chose none.
type CategoryAssignment struct {
	Category  *Definition       `json:"category"`
	Origin    string            `json:"origin"`
	Source    *string           `json:"source"`
	By        OrganisationActor `json:"by"`
	At        string            `json:"at"`
	Inherited bool              `json:"inherited"`
}

// LabelAssignment is one label on an object with its origin.
type LabelAssignment struct {
	Label     Definition        `json:"label"`
	Origin    string            `json:"origin"`
	Source    *string           `json:"source"`
	By        OrganisationActor `json:"by"`
	At        string            `json:"at"`
	Inherited bool              `json:"inherited"`
}

// ObjectAssignments is everything organised on one object.
type ObjectAssignments struct {
	ObjectType    string              `json:"object_type"`
	ObjectID      string              `json:"object_id"`
	Category      *CategoryAssignment `json:"category"`
	Labels        []LabelAssignment   `json:"labels"`
	RemovedLabels []LabelAssignment   `json:"removed_labels"`
	Buckets       []Definition        `json:"buckets"`
}

// BucketMember is a commitment or account in a bucket.
type BucketMember struct {
	ObjectType string            `json:"object_type"`
	ObjectID   string            `json:"object_id"`
	AddedBy    OrganisationActor `json:"added_by"`
	AddedAt    string            `json:"added_at"`
}

// DefinitionDeleted is the answer of a delete: how many assignments moved to
// the replacement.
type DefinitionDeleted struct {
	Reassigned int `json:"reassigned"`
}

type definitionOps struct{ list, create, get, update, archive, unarchive, del operation }

func opsOf(kind string) definitionOps {
	switch kind {
	case KindLabel:
		return definitionOps{opListLabels, opCreateLabel, opGetLabel, opUpdateLabel, opArchiveLabel, opUnarchiveLabel, opDeleteLabel}
	case KindBucket:
		return definitionOps{opListBuckets, opCreateBucket, opGetBucket, opUpdateBucket, opArchiveBucket, opUnarchiveBucket, opDeleteBucket}
	}
	return definitionOps{opListCategories, opCreateCategory, opGetCategory, opUpdateCategory, opArchiveCategory, opUnarchiveCategory, opDeleteCategory}
}

// ListDefinitions returns one page of categories, labels or buckets (kind is
// KindCategory, KindLabel or KindBucket; needs organisation:read).
func (c *Client) ListDefinitions(ctx context.Context, kind string, f DefinitionFilter) (Page[Definition], error) {
	var p Page[Definition]
	err := c.do(ctx, request{op: opsOf(kind).list, query: f.query(kind)}, &p)
	return p, err
}

// AllDefinitions walks every definition of kind matching f.
func (c *Client) AllDefinitions(ctx context.Context, kind string, f DefinitionFilter) iter.Seq2[Definition, error] {
	return Walk(ctx, func(ctx context.Context, cur string) (Page[Definition], error) {
		f.Cursor = cur
		return c.ListDefinitions(ctx, kind, f)
	})
}

// CreateDefinition creates a category, label or bucket (needs organisation:write).
func (c *Client) CreateDefinition(ctx context.Context, kind string, in NewDefinition) (*Definition, error) {
	var d Definition
	if err := c.do(ctx, request{op: opsOf(kind).create, body: in}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDefinition returns one definition.
func (c *Client) GetDefinition(ctx context.Context, kind, id string) (*Definition, error) {
	var d Definition
	if err := c.do(ctx, request{op: opsOf(kind).get, pathID: id}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// UpdateDefinition applies a merge patch (name, and parent_id for a category)
// to the definition whose current version is version.
func (c *Client) UpdateDefinition(ctx context.Context, kind, id string, version int64, patch MergePatch) (*Definition, error) {
	var d Definition
	if err := c.do(ctx, request{op: opsOf(kind).update, pathID: id, version: version, body: patch}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ArchiveDefinition archives a definition: it keeps every assignment and stops
// new use.
func (c *Client) ArchiveDefinition(ctx context.Context, kind, id string, version int64) (*Definition, error) {
	var d Definition
	if err := c.do(ctx, request{op: opsOf(kind).archive, pathID: id, version: version, body: struct{}{}}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// UnarchiveDefinition makes an archived definition usable again.
func (c *Client) UnarchiveDefinition(ctx context.Context, kind, id string, version int64) (*Definition, error) {
	var d Definition
	if err := c.do(ctx, request{op: opsOf(kind).unarchive, pathID: id, version: version, body: struct{}{}}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteDefinition deletes a definition nothing is assigned to, or moves its
// assignments to the active definition reassignTo (empty: none) first.
func (c *Client) DeleteDefinition(ctx context.Context, kind, id string, version int64, reassignTo string) (*DefinitionDeleted, error) {
	q := url.Values{}
	setIf(q, "reassign_to", reassignTo)
	var out DefinitionDeleted
	if err := c.do(ctx, request{op: opsOf(kind).del, pathID: id, version: version, query: q}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListBucketMembers returns one page of the commitments and accounts in a bucket.
func (c *Client) ListBucketMembers(ctx context.Context, bucketID string, o ListOptions) (Page[BucketMember], error) {
	q := url.Values{}
	o.apply(q)
	var p Page[BucketMember]
	err := c.do(ctx, request{op: opListBucketMembers, pathID: bucketID, query: q}, &p)
	return p, err
}

// AddBucketMember puts a commitment or account in a bucket (idempotent).
func (c *Client) AddBucketMember(ctx context.Context, bucketID, objectType, objectID string) error {
	return c.do(ctx, request{op: opAddBucketMember, pathID: bucketID, params: map[string]string{"object_type": objectType, "object_id": objectID}}, nil)
}

// RemoveBucketMember takes an object out of a bucket.
func (c *Client) RemoveBucketMember(ctx context.Context, bucketID, objectType, objectID string) error {
	return c.do(ctx, request{op: opRemoveBucketMember, pathID: bucketID, params: map[string]string{"object_type": objectType, "object_id": objectID}}, nil)
}

func objParams(objectType, objectID string) map[string]string {
	return map[string]string{"object_type": objectType, "object_id": objectID}
}

// GetObjectOrganisation reads what is assigned to a transaction, commitment
// or account (a merchant needs the merchants grant too).
func (c *Client) GetObjectOrganisation(ctx context.Context, objectType, objectID string) (*ObjectAssignments, error) {
	var a ObjectAssignments
	if err := c.do(ctx, request{op: opGetObjectOrg, params: objParams(objectType, objectID)}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// SetObjectCategory sets the person's category on an object; a nil categoryID
// records that the person chose no category.
func (c *Client) SetObjectCategory(ctx context.Context, objectType, objectID string, categoryID *string) (*ObjectAssignments, error) {
	var a ObjectAssignments
	body := struct {
		CategoryID *string `json:"category_id"`
	}{categoryID}
	if err := c.do(ctx, request{op: opSetObjectCategory, params: objParams(objectType, objectID), body: body}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// ResetObjectCategory hands the category back to automatic assignment.
func (c *Client) ResetObjectCategory(ctx context.Context, objectType, objectID string) (*ObjectAssignments, error) {
	var a ObjectAssignments
	if err := c.do(ctx, request{op: opResetObjectCategory, params: objParams(objectType, objectID)}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// AddObjectLabel puts a label on an object as the person's choice.
func (c *Client) AddObjectLabel(ctx context.Context, objectType, objectID, labelID string) (*ObjectAssignments, error) {
	var a ObjectAssignments
	p := objParams(objectType, objectID)
	p["label_id"] = labelID
	if err := c.do(ctx, request{op: opAddObjectLabel, params: p}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// RemoveObjectLabel takes a label off an object; automatic runs do not add it back.
func (c *Client) RemoveObjectLabel(ctx context.Context, objectType, objectID, labelID string) (*ObjectAssignments, error) {
	var a ObjectAssignments
	p := objParams(objectType, objectID)
	p["label_id"] = labelID
	if err := c.do(ctx, request{op: opRemoveObjectLabel, params: p}, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
