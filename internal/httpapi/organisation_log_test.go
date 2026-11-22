package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
)

func TestLoggedPathHidesMerchantKeys(t *testing.T) {
	for in, want := range map[string]string{
		"/api/v1/organisation/merchant/acme%20shop":                   "/api/v1/organisation/merchant/{object_id}",
		"/api/v1/organisation/merchant/acme%2Fshop/labels/01JZ":       "/api/v1/organisation/merchant/{object_id}/labels/01JZ",
		"/api/v1/organisation/merchant/acme/category":                 "/api/v1/organisation/merchant/{object_id}/category",
		"/api/v1/organisation/transaction/01JD3Y4C8V9M2Q7W5R6T0XZBNA": "/api/v1/organisation/transaction/01JD3Y4C8V9M2Q7W5R6T0XZBNA",
		"/api/v1/categories/01JD3Y4C8V9M2Q7W5R6T0XZBNA":               "/api/v1/categories/01JD3Y4C8V9M2Q7W5R6T0XZBNA",
	} {
		if got := loggedPath(httptest.NewRequest("GET", in, nil)); got != want {
			t.Errorf("loggedPath(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestMerchantGateNeedsTheMerchantsGrant(t *testing.T) {
	admin := authz.UserPrincipal("01JD3Y4C8V9M2Q7W5R6T0XZBNA", authz.RoleAdministrator)
	readOnly := authz.UserPrincipal("01JD3Y4C8V9M2Q7W5R6T0XZBNB", authz.RoleReadOnly)
	for _, c := range []struct {
		name  string
		path  string
		p     *authz.Principal
		g     authz.Grant
		allow bool
	}{
		{"admin reads a merchant", "/api/v1/organisation/merchant/acme", &admin, grantMerchantsRead, true},
		{"admin changes a merchant", "/api/v1/organisation/merchant/acme/category", &admin, grantMerchantsWrite, true},
		{"read-only role reads a merchant", "/api/v1/organisation/merchant/acme", &readOnly, grantMerchantsRead, false},
		{"read-only role changes a merchant", "/api/v1/organisation/merchant/acme/category", &readOnly, grantMerchantsWrite, false},
		{"no principal", "/api/v1/organisation/merchant/acme", nil, grantMerchantsRead, false},
		{"read-only role reads a transaction", "/api/v1/organisation/transaction/01JZ0000000000000000009999", &readOnly, grantMerchantsRead, true},
	} {
		mux := newGateMux(c.g)
		req := httptest.NewRequest("GET", c.path, nil)
		if c.p != nil {
			req = req.WithContext(authz.WithPrincipal(req.Context(), *c.p))
		}
		if err := merchantGate(mux(req), c.g); (err == nil) != c.allow {
			t.Errorf("%s: err = %v, allowed = %v", c.name, err, c.allow)
		}
	}
}

// newGateMux returns a function that fills in the path values merchantGate
// reads, as the router would.
func newGateMux(authz.Grant) func(*http.Request) *http.Request {
	return func(r *http.Request) *http.Request {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/organisation/"), "/")
		r.SetPathValue("object_type", parts[0])
		r.SetPathValue("object_id", parts[1])
		return r
	}
}
