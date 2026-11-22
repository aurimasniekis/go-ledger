package httpapi

import (
	"net/http/httptest"
	"testing"
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
