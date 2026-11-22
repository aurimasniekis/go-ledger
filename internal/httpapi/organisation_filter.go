package httpapi

import (
	"errors"
	"net/http"
	"slices"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
)

// Category and label filters of the lists that show organised objects (task
// O3, decision D12). category_id and label_id may be repeated (any of them
// matches); the two combine (both must hold). A category also matches its
// children. They need organisation:read as well as the list's own grant,
// because they reveal how objects are organised.
var orgFilterSpecs = map[string]FilterSpec{
	"category_id": {Ops: []FilterOp{OpEq}, Check: checkID("category_id must be a category identifier")},
	"label_id":    {Ops: []FilterOp{OpEq}, Check: checkID("label_id must be a label identifier")},
}

// withOrgFilters returns spec with the category and label filters added.
func withOrgFilters(spec ListSpec) ListSpec {
	out := ListSpec{Filters: map[string]FilterSpec{}, CursorKey: spec.CursorKey}
	for k, v := range spec.Filters {
		out.Filters[k] = v
	}
	for k, v := range orgFilterSpecs {
		out.Filters[k] = v
	}
	return out
}

// orgFilterJSON echoes an applied organisation filter, so a response says it
// is restricted.
type orgFilterJSON struct {
	CategoryIDs []string `json:"category_ids"`
	LabelIDs    []string `json:"label_ids"`
	Note        string   `json:"note"`
}

// selectOrganised resolves the category and label filters to the IDs of the
// objects of type t that satisfy them. active is false (and ids nil) when no
// filter was given. An empty ids with active true means nothing matches.
func selectOrganised(r *http.Request, st organisation.Store, t organisation.ObjectType, categories, labels []string) (ids []string, active bool, err error) {
	if len(categories) == 0 && len(labels) == 0 {
		return nil, false, nil
	}
	if p, ok := authz.FromContext(r.Context()); !ok || !p.Allows(grantOrganisationRead) {
		return nil, true, Forbidden("filtering by category or label requires the " + grantOrganisationRead.String() + " grant")
	}
	if st == nil {
		return nil, true, Internal(errors.New("organisation store is not configured"))
	}
	ids, err = st.Select(r.Context(), t, organisation.Filter{CategoryIDs: categories, LabelIDs: labels})
	var unknown *organisation.UnknownDefinitionError
	switch {
	case errors.As(err, &unknown):
		field := "category_id"
		if unknown.Kind == organisation.KindLabel {
			field = "label_id"
		}
		return nil, true, Validation(field, field+" does not name an existing "+string(unknown.Kind))
	case errors.Is(err, organisation.ErrTooManyObjects):
		// Name the filter the caller gave: label_id when only label filters were sent.
		field := "category_id"
		if len(categories) == 0 {
			field = "label_id"
		}
		return nil, true, Validation(field, err.Error())
	case err != nil:
		return nil, true, err
	}
	return ids, true, nil
}

// intersectIDs returns the IDs of a that are also in b, in a's order.
func intersectIDs(a, b []string) []string {
	out := []string{}
	for _, id := range a {
		if slices.Contains(b, id) {
			out = append(out, id)
		}
	}
	return out
}

func orgFilterEcho(categories, labels []string, what string) *orgFilterJSON {
	if len(categories) == 0 && len(labels) == 0 {
		return nil
	}
	return &orgFilterJSON{CategoryIDs: append([]string{}, categories...), LabelIDs: append([]string{}, labels...),
		Note: "Filtered: only " + what + " matching the category and label filter are shown and counted. Categories and labels group; they allocate no money."}
}
