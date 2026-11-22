package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/internal/domain/organisation"
)

// tooManyStore is an organisation store whose filter always matches more objects than allowed.
type tooManyStore struct{ organisation.Store }

func (tooManyStore) Select(context.Context, organisation.ObjectType, organisation.Filter) ([]string, error) {
	return nil, organisation.ErrTooManyObjects
}

// TestSelectOrganisedRefusesTooManyObjects pins the 400 for a filter that matches more than the cap, and that it
// names the filter the caller gave.
func TestSelectOrganisedRefusesTooManyObjects(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/commitments", nil)
	r = r.WithContext(authz.WithPrincipal(r.Context(), authz.UserPrincipal("01JD3Y4C8V9M2Q7W5R6T0XZBNA", authz.RoleAdministrator)))
	for _, c := range []struct {
		name       string
		categories []string
		labels     []string
		field      string
	}{
		{"label only", nil, []string{"L1"}, "label_id"},
		{"category only", []string{"C1"}, nil, "category_id"},
		{"both", []string{"C1"}, []string{"L1"}, "category_id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := selectOrganised(r, tooManyStore{}, organisation.ObjectCommitment, c.categories, c.labels)
			var e *Error
			if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Code != CodeValidation || e.Field != c.field {
				t.Fatalf("got %#v, want a 400 validation error on %s", err, c.field)
			}
		})
	}
}
