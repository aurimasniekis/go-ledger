package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/commitments"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// Paths of the organisation routes (task O1, ADR-0053).
const (
	CategoriesPath   = APIPrefix + "/categories"
	LabelsPath       = APIPrefix + "/labels"
	BucketsPath      = APIPrefix + "/buckets"
	OrganisationPath = APIPrefix + "/organisation"
)

var (
	grantOrganisationRead  = authz.G(authz.ResourceOrganisation, authz.ActionRead)
	grantOrganisationWrite = authz.G(authz.ResourceOrganisation, authz.ActionWrite)
)

// Error codes of the organisation routes. A stale version is the usual
// version_conflict.
const (
	// CodeDefinitionArchived (409): the category, label or bucket is archived
	// and nothing new may use it.
	CodeDefinitionArchived Code = "definition_archived"
	// CodeDefinitionInUse (409): the definition is still assigned; archive it
	// or name a replacement (reassign_to).
	CodeDefinitionInUse Code = "definition_in_use"
	// CodeDuplicateName (409): another definition (a sibling, for a category)
	// has the name.
	CodeDuplicateName Code = "duplicate_name"
)

type definitionJSON struct {
	ID   string            `json:"id"`
	Kind organisation.Kind `json:"kind"`
	Name string            `json:"name"`
	// ParentID is a category's parent; null for a top-level category and for
	// labels and buckets.
	ParentID   *string    `json:"parent_id"`
	Archived   bool       `json:"archived"`
	ArchivedAt *time.Time `json:"archived_at"`
	Version    int64      `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func toDefinitionJSON(d organisation.Definition) definitionJSON {
	return definitionJSON{ID: d.ID, Kind: d.Kind, Name: d.Name, ParentID: d.ParentID, Archived: d.Archived(), ArchivedAt: d.ArchivedAt,
		Version: d.Version, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
}

type categoryAssignmentJSON struct {
	// Category is null when the person explicitly chose no category.
	Category  *definitionJSON     `json:"category"`
	Origin    organisation.Origin `json:"origin"`
	Source    *string             `json:"source"`
	By        deciderJSON         `json:"by"`
	At        time.Time           `json:"at"`
	Inherited bool                `json:"inherited"`
}

type labelAssignmentJSON struct {
	Label     definitionJSON      `json:"label"`
	Origin    organisation.Origin `json:"origin"`
	Source    *string             `json:"source"`
	By        deciderJSON         `json:"by"`
	At        time.Time           `json:"at"`
	Inherited bool                `json:"inherited"`
}

type assignmentsJSON struct {
	ObjectType    organisation.ObjectType `json:"object_type"`
	ObjectID      string                  `json:"object_id"`
	Category      *categoryAssignmentJSON `json:"category"`
	Labels        []labelAssignmentJSON   `json:"labels"`
	RemovedLabels []labelAssignmentJSON   `json:"removed_labels"`
	Buckets       []definitionJSON        `json:"buckets"`
}

func toActorJSON(a audit.Actor) deciderJSON {
	d := deciderJSON{Kind: a.Kind, UserID: strPtr(a.UserID)}
	if a.Via != "" {
		v := a.Via
		d.Via = &v
	}
	return d
}

func toAssignmentsJSON(a organisation.Assignments) assignmentsJSON {
	out := assignmentsJSON{ObjectType: a.Object.Type, ObjectID: a.Object.ID, Labels: []labelAssignmentJSON{}, RemovedLabels: []labelAssignmentJSON{}, Buckets: []definitionJSON{}}
	if c := a.Category; c != nil {
		cj := &categoryAssignmentJSON{Origin: c.Origin, Source: strPtr(c.Source), By: toActorJSON(c.By), At: c.At, Inherited: c.Inherited}
		if c.Category != nil {
			d := toDefinitionJSON(*c.Category)
			cj.Category = &d
		}
		out.Category = cj
	}
	label := func(l organisation.LabelAssignment) labelAssignmentJSON {
		return labelAssignmentJSON{Label: toDefinitionJSON(l.Label), Origin: l.Origin, Source: strPtr(l.Source), By: toActorJSON(l.By), At: l.At, Inherited: l.Inherited}
	}
	for _, l := range a.Labels {
		out.Labels = append(out.Labels, label(l))
	}
	for _, l := range a.Removed {
		out.RemovedLabels = append(out.RemovedLabels, label(l))
	}
	for _, b := range a.Buckets {
		out.Buckets = append(out.Buckets, toDefinitionJSON(b))
	}
	return out
}

type memberJSON struct {
	ObjectType organisation.ObjectType `json:"object_type"`
	ObjectID   string                  `json:"object_id"`
	AddedBy    deciderJSON             `json:"added_by"`
	AddedAt    time.Time               `json:"added_at"`
}

func orgConflict(code Code, err error) *Error {
	return &Error{Status: http.StatusConflict, Code: code, Message: err.Error(), Cause: err}
}

// organisationError maps the organisation package's errors to API errors.
func organisationError(err error) error {
	var ve *commitments.ValidationError
	var inUse *organisation.InUseError
	switch {
	case errors.As(err, &ve) && len(ve.Errors) > 0:
		return fieldErrors(ve.Errors)
	case errors.Is(err, organisation.ErrArchived):
		return orgConflict(CodeDefinitionArchived, err)
	case errors.Is(err, organisation.ErrDuplicateName):
		return orgConflict(CodeDuplicateName, err)
	case errors.As(err, &inUse):
		return orgConflict(CodeDefinitionInUse, err)
	case errors.Is(err, organisation.ErrHasActiveChildren), errors.Is(err, organisation.ErrHasChildren), errors.Is(err, organisation.ErrParentArchived):
		return orgConflict(CodeConflict, err)
	case errors.Is(err, organisation.ErrInheritedAssignment):
		return &Error{Status: http.StatusConflict, Code: CodeReviewInherited, Message: err.Error(), Cause: err}
	case errors.Is(err, organisation.ErrNoAssignment):
		return NotFound(err.Error())
	case errors.Is(err, organisation.ErrNotApplicable):
		return Validation("object_type", err.Error())
	}
	return err
}

var definitionsListSpec = func(k organisation.Kind) ListSpec {
	filters := map[string]FilterSpec{
		"archived": {Ops: []FilterOp{OpEq}, Check: func(v string) error {
			if v != "true" && v != "false" {
				return errors.New("archived must be true or false")
			}
			return nil
		}},
		"name": {Ops: []FilterOp{OpPrefix}, Check: func(v string) error {
			if len(v) > organisation.MaxName*4 {
				return errors.New("name prefix is too long")
			}
			return nil
		}},
	}
	if k == organisation.KindCategory {
		filters["parent_id"] = FilterSpec{Ops: []FilterOp{OpEq}, Check: func(v string) error {
			if v != "none" && !resource.ValidID(v) {
				return errors.New("parent_id must be a category identifier, or none for top-level categories")
			}
			return nil
		}}
	}
	return ListSpec{Filters: filters}
}

// definitionMembers are the writable members of a definition; parent_id is
// for categories only.
func definitionMembers(k organisation.Kind) []string {
	if k == organisation.KindCategory {
		return []string{"name", "parent_id"}
	}
	return []string{"name"}
}

func decodeDefinition(k organisation.Kind, doc map[string]json.RawMessage, base organisation.Fields, create bool) (organisation.Fields, []commitments.FieldError) {
	var errs []commitments.FieldError
	f := base
	var unknown []string
	for key := range doc {
		if !slices.Contains(definitionMembers(k), key) {
			unknown = append(unknown, key)
		}
	}
	slices.Sort(unknown)
	for _, key := range unknown {
		errs = append(errs, commitments.FieldError{Field: key, Message: "unknown field " + key})
	}
	if raw, ok := doc["name"]; ok && !isJSONNull(raw) {
		if json.Unmarshal(raw, &f.Name) != nil {
			errs = append(errs, commitments.FieldError{Field: "name", Message: "name must be a string"})
		}
	} else if ok || create {
		errs = append(errs, commitments.FieldError{Field: "name", Message: "name is required"})
	}
	if raw, ok := doc["parent_id"]; ok && k == organisation.KindCategory {
		if isJSONNull(raw) {
			f.ParentID = nil
		} else {
			var p string
			if json.Unmarshal(raw, &p) != nil {
				errs = append(errs, commitments.FieldError{Field: "parent_id", Message: "parent_id must be a string or null"})
			} else {
				f.ParentID = &p
			}
		}
	}
	if len(errs) == 0 {
		errs = f.Normalized().Validate(k)
	}
	return f.Normalized(), errs
}

func registerOrganisation(s *Server, st organisation.Store) {
	registerDefinitions(s, st, organisation.KindCategory, CategoriesPath)
	registerDefinitions(s, st, organisation.KindLabel, LabelsPath)
	registerDefinitions(s, st, organisation.KindBucket, BucketsPath)
	registerBucketMembers(s, st)
	registerAssignments(s, st)
}

func registerDefinitions(s *Server, st organisation.Store, k organisation.Kind, path string) {
	item := path + "/{id}"
	// withConflict turns a stale version into a version_conflict carrying the
	// current definition.
	failed := func(w http.ResponseWriter, r *http.Request, id string, err error) {
		var vc *resource.VersionConflictError
		if errors.As(err, &vc) {
			if cur, gerr := st.GetDefinition(r.Context(), k, id); gerr == nil {
				WriteError(w, r, VersionConflict(cur.Version, toDefinitionJSON(cur)))
				return
			}
		}
		WriteError(w, r, organisationError(err))
	}

	s.HandleAPI("GET "+path, Requires(grantOrganisationRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, definitionsListSpec(k))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f := organisation.ListFilter{After: q.After, Limit: q.Limit}
		if v := q.Filter("archived", OpEq); len(v) == 1 {
			b := v[0] == "true"
			f.Archived = &b
		} else if len(v) > 1 {
			WriteError(w, r, Validation("archived", "archived must be given once"))
			return
		}
		if v := q.Filter("parent_id", OpEq); len(v) == 1 {
			p := v[0]
			if p == "none" {
				p = ""
			}
			f.Parent = &p
		} else if len(v) > 1 {
			WriteError(w, r, Validation("parent_id", "parent_id must be given once"))
			return
		}
		if v := q.Filter("name", OpPrefix); len(v) == 1 {
			f.NamePrefix = v[0]
		}
		items, err := st.ListDefinitions(r.Context(), k, f)
		if err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		out := make([]definitionJSON, len(items))
		for i, d := range items {
			out[i] = toDefinitionJSON(d)
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(d definitionJSON) string { return d.ID }))
	}))

	s.HandleAPI("POST "+path, Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		f, errs := decodeDefinition(k, doc, organisation.Fields{}, true)
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		d, err := st.CreateDefinition(r.Context(), matchActor(r), k, f)
		if err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		w.Header().Set("Location", path+"/"+d.ID)
		SetVersion(w, d.Version)
		WriteJSON(w, http.StatusCreated, toDefinitionJSON(d))
	}))

	s.HandleAPI("GET "+item, Requires(grantOrganisationRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, err := st.GetDefinition(r.Context(), k, r.PathValue("id"))
		if err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		SetVersion(w, d.Version)
		WriteJSON(w, http.StatusOK, toDefinitionJSON(d))
	}))

	// PATCH is a JSON Merge Patch of the writable members (name, and for a
	// category parent_id, null for top level). The result is validated as a
	// whole; a patch that changes nothing is a no-op.
	s.HandleAPI("PATCH "+item, Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patch, err := DecodeMergePatch(r, append([]string{"version"}, definitionMembers(k)...)...)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var bodyVersion *int64
		if patch.Has("version") {
			var v int64
			if patch.IsNull("version") || patch.Decode("version", &v) != nil {
				WriteError(w, r, Validation("version", "version must be a positive integer"))
				return
			}
			bodyVersion = &v
			delete(patch, "version")
		}
		expected, err := ExpectedVersion(r, bodyVersion)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		id := r.PathValue("id")
		cur, err := st.GetDefinition(r.Context(), k, id)
		if err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		if cur.Version != expected {
			WriteError(w, r, VersionConflict(cur.Version, toDefinitionJSON(cur)))
			return
		}
		doc := map[string]json.RawMessage(patch)
		f, errs := decodeDefinition(k, doc, organisation.Fields{Name: cur.Name, ParentID: cur.ParentID}, false)
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		d, err := st.UpdateDefinition(r.Context(), matchActor(r), k, id, expected, f)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		SetVersion(w, d.Version)
		WriteJSON(w, http.StatusOK, toDefinitionJSON(d))
	}))

	archive := func(archived bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			expected, err := ExpectedVersion(r, nil)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			id := r.PathValue("id")
			d, err := st.SetArchived(r.Context(), matchActor(r), k, id, expected, archived)
			if err != nil {
				failed(w, r, id, err)
				return
			}
			SetVersion(w, d.Version)
			WriteJSON(w, http.StatusOK, toDefinitionJSON(d))
		})
	}
	s.HandleAPI("POST "+item+"/archive", Requires(grantOrganisationWrite), archive(true))
	s.HandleAPI("POST "+item+"/unarchive", Requires(grantOrganisationWrite), archive(false))

	// DELETE needs If-Match. A definition that is assigned is refused (409
	// definition_in_use) unless reassign_to names an active replacement of the
	// same kind.
	s.HandleAPI("DELETE "+item, Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key := range r.URL.Query() {
			if key != "reassign_to" {
				WriteError(w, r, Validation(key, "unknown query parameter"))
				return
			}
		}
		reassign := r.URL.Query()["reassign_to"]
		if len(reassign) > 1 || (len(reassign) == 1 && reassign[0] == "") {
			WriteError(w, r, Validation("reassign_to", "reassign_to must be given once and not be empty"))
			return
		}
		expected, err := ExpectedVersion(r, nil)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var to string
		if len(reassign) == 1 {
			to = reassign[0]
		}
		id := r.PathValue("id")
		res, err := st.DeleteDefinition(r.Context(), matchActor(r), k, id, expected, to)
		if err != nil {
			failed(w, r, id, err)
			return
		}
		WriteJSON(w, http.StatusOK, struct {
			Reassigned int `json:"reassigned"`
		}{res.Reassigned})
	}))
}

// objectOf reads the object a route names. Only its form is checked here; the
// store checks that it exists.
// merchantGate refuses a merchant-keyed route to a principal without the
// merchants grant: the key is bank-derived text, so what is filed under it is
// as sensitive as the merchant enrichment itself (admin-only, ADR-0051).
func merchantGate(r *http.Request, g authz.Grant) error {
	if objectOf(r).Type != organisation.ObjectMerchant {
		return nil
	}
	if p, ok := authz.FromContext(r.Context()); !ok || !p.Allows(g) {
		return Forbidden("merchant assignments require the " + g.String() + " grant")
	}
	return nil
}

func objectOf(r *http.Request) organisation.ObjectRef {
	return organisation.ObjectRef{Type: organisation.ObjectType(r.PathValue("object_type")), ID: r.PathValue("object_id")}
}

func memberCursorKey(key string) bool {
	typ, id, ok := strings.Cut(key, ":")
	return ok && organisation.ObjectType(typ).BucketMember() && resource.ValidID(id)
}

func registerBucketMembers(s *Server, st organisation.Store) {
	members := BucketsPath + "/{id}/members"
	s.HandleAPI("GET "+members, Requires(grantOrganisationRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := ParseList(r, ListSpec{CursorKey: memberCursorKey})
		if err != nil {
			WriteError(w, r, err)
			return
		}
		items, err := st.ListMembers(r.Context(), r.PathValue("id"), organisation.MemberFilter{After: q.After, Limit: q.Limit})
		if err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		out := make([]memberJSON, len(items))
		for i, m := range items {
			out[i] = memberJSON{ObjectType: m.Object.Type, ObjectID: m.Object.ID, AddedBy: toActorJSON(m.By), AddedAt: m.At}
		}
		WriteJSON(w, http.StatusOK, NewPage(out, q, func(m memberJSON) string { return string(m.ObjectType) + ":" + m.ObjectID }))
	}))
	one := members + "/{object_type}/{object_id}"
	s.HandleAPI("PUT "+one, Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := st.AddToBucket(r.Context(), matchActor(r), r.PathValue("id"), objectOf(r)); err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	s.HandleAPI("DELETE "+one, Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := st.RemoveFromBucket(r.Context(), matchActor(r), r.PathValue("id"), objectOf(r)); err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// registerAssignments registers what is assigned to one object: read it, set
// or reset its category, add or remove a label. Assignments made here are the
// person's (origin user); rules and models write through the store's
// ApplyAutomatic, never through these routes, and never replace them.
func registerAssignments(s *Server, st organisation.Store) {
	obj := OrganisationPath + "/{object_type}/{object_id}"
	respond := func(w http.ResponseWriter, r *http.Request, a organisation.Assignments, err error) {
		if err != nil {
			WriteError(w, r, organisationError(err))
			return
		}
		WriteJSON(w, http.StatusOK, toAssignmentsJSON(a))
	}
	s.HandleAPI("GET "+obj, Requires(grantOrganisationRead), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := merchantGate(r, grantMerchantsRead); err != nil {
			WriteError(w, r, err)
			return
		}
		a, err := st.Get(r.Context(), objectOf(r))
		respond(w, r, a, err)
	}))
	s.HandleAPI("PUT "+obj+"/category", Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := merchantGate(r, grantMerchantsWrite); err != nil {
			WriteError(w, r, err)
			return
		}
		doc, err := decodeObject(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var errs []commitments.FieldError
		for k := range doc {
			if k != "category_id" {
				errs = append(errs, commitments.FieldError{Field: k, Message: "unknown field " + k})
			}
		}
		slices.SortFunc(errs, func(a, b commitments.FieldError) int { return strings.Compare(a.Field, b.Field) })
		var id *string
		raw, ok := doc["category_id"]
		switch {
		case !ok:
			errs = append(errs, commitments.FieldError{Field: "category_id", Message: "category_id is required: an identifier, or null for no category"})
		case !isJSONNull(raw):
			var v string
			if json.Unmarshal(raw, &v) != nil {
				errs = append(errs, commitments.FieldError{Field: "category_id", Message: "category_id must be a string or null"})
			} else {
				id = &v
			}
		}
		if len(errs) > 0 {
			WriteError(w, r, fieldErrors(errs))
			return
		}
		a, err := st.SetCategory(r.Context(), matchActor(r), objectOf(r), id)
		respond(w, r, a, err)
	}))
	s.HandleAPI("DELETE "+obj+"/category", Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := merchantGate(r, grantMerchantsWrite); err != nil {
			WriteError(w, r, err)
			return
		}
		a, err := st.ResetCategory(r.Context(), matchActor(r), objectOf(r))
		respond(w, r, a, err)
	}))
	s.HandleAPI("PUT "+obj+"/labels/{label_id}", Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := merchantGate(r, grantMerchantsWrite); err != nil {
			WriteError(w, r, err)
			return
		}
		a, err := st.AddLabel(r.Context(), matchActor(r), objectOf(r), r.PathValue("label_id"))
		respond(w, r, a, err)
	}))
	s.HandleAPI("DELETE "+obj+"/labels/{label_id}", Requires(grantOrganisationWrite), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := merchantGate(r, grantMerchantsWrite); err != nil {
			WriteError(w, r, err)
			return
		}
		a, err := st.RemoveLabel(r.Context(), matchActor(r), objectOf(r), r.PathValue("label_id"))
		respond(w, r, a, err)
	}))
}
