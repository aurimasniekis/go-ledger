package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

var thingSpec = ListSpec{Filters: map[string]FilterSpec{
	"kind": {Ops: []FilterOp{OpEq}},
	"name": {Ops: []FilterOp{OpPrefix}},
}}

func TestParseListDefaultsAndCanonicalFilters(t *testing.T) {
	q, err := ParseList(httptest.NewRequest("GET", "/x?kind=b&name[prefix]=N&kind=a&kind=b", nil), thingSpec)
	if err != nil {
		t.Fatal(err)
	}
	if q.Limit != DefaultLimit || q.After != "" {
		t.Fatalf("defaults: %+v", q)
	}
	if got := q.Filter("kind", OpEq); strings.Join(got, ",") != "a,b" {
		t.Fatalf("kind values %v, want sorted and de-duplicated", got)
	}
	if got := q.Filter("name", OpPrefix); len(got) != 1 || got[0] != "N" {
		t.Fatalf("name prefix %v", got)
	}
	if q.Filter("name", OpEq) != nil {
		t.Fatal("unexpected filter")
	}
	// The same filters in another order give the same cursor.
	q2, _ := ParseList(httptest.NewRequest("GET", "/x?name[prefix]=N&kind=a&kind=b", nil), thingSpec)
	id := resource.NewID()
	if q.Cursor(id) != q2.Cursor(id) {
		t.Fatal("cursor depends on parameter order")
	}
}

func TestCursorRejectsTampering(t *testing.T) {
	q, _ := ParseList(httptest.NewRequest("GET", "/x", nil), thingSpec)
	enc := base64.RawURLEncoding.EncodeToString
	for name, c := range map[string]string{
		"not base64":   "!!!",
		"not JSON":     enc([]byte("hello")),
		"bad id":       enc([]byte(`{"a":"DROP TABLE","f":"` + q.filterHash() + `"}`)),
		"extra field":  enc([]byte(`{"a":"` + resource.NewID() + `","f":"` + q.filterHash() + `","x":1}`)),
		"other filter": enc([]byte(`{"a":"` + resource.NewID() + `","f":"0000000000000000"}`)),
		"too long":     strings.Repeat("A", maxCursorLen+1),
	} {
		_, err := ParseList(httptest.NewRequest("GET", "/x?cursor="+c, nil), thingSpec)
		var e *Error
		if !errors.As(err, &e) || e.Status != 400 || e.Field != "cursor" {
			t.Errorf("%s: %v", name, err)
		}
	}
	id := resource.NewID()
	got, err := ParseList(httptest.NewRequest("GET", "/x?cursor="+q.Cursor(id), nil), thingSpec)
	if err != nil || got.After != id {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestNewPage(t *testing.T) {
	q := ListQuery{Limit: 2}
	ids := []string{resource.NewID(), resource.NewID(), resource.NewID()}
	p := NewPage(ids, q, func(s string) string { return s })
	if len(p.Items) != 2 || p.NextCursor == nil {
		t.Fatalf("full page: %+v", p)
	}
	next, err := ParseList(httptest.NewRequest("GET", "/x?cursor="+*p.NextCursor, nil), thingSpec)
	if err != nil || next.After != ids[1] {
		t.Fatalf("cursor points after %q: %+v %v", ids[1], next, err)
	}
	if p := NewPage(ids[:2], q, func(s string) string { return s }); p.NextCursor != nil {
		t.Fatal("last page has a cursor")
	}
	if p := NewPage[string](nil, q, func(s string) string { return s }); p.Items == nil {
		t.Fatal("nil items would encode as null")
	}
}
