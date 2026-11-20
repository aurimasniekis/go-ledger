package httpapi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeMergePatch(t *testing.T) {
	for _, c := range []struct {
		name, ctype, body string
		status            int
		field             string
	}{
		{"ok", MergePatchType, `{"a":1,"b":null}`, 0, ""},
		{"ok with charset", MergePatchType + "; charset=utf-8", `{}`, 0, ""},
		{"plain json refused", "application/json", `{}`, 415, ""},
		{"no content type", "", `{}`, 415, ""},
		{"unknown member", MergePatchType, `{"zzz":1}`, 400, "zzz"},
		{"not an object", MergePatchType, `[1]`, 400, ""},
		{"null body", MergePatchType, `null`, 400, ""},
	} {
		r := httptest.NewRequest("PATCH", "/x", strings.NewReader(c.body))
		if c.ctype != "" {
			r.Header.Set("Content-Type", c.ctype)
		}
		p, err := DecodeMergePatch(r, "a", "b", "c")
		if c.status == 0 {
			if err != nil || !p.Has("a") && c.name == "ok" {
				t.Errorf("%s: %v %v", c.name, p, err)
			}
			continue
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != c.status || apiErr.Field != c.field {
			t.Errorf("%s: %#v", c.name, err)
		}
	}
	r := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"a":1,"b":null}`))
	r.Header.Set("Content-Type", MergePatchType)
	p, _ := DecodeMergePatch(r, "a", "b", "c")
	if !p.Has("b") || !p.IsNull("b") || p.IsNull("a") || p.Has("c") || p.IsNull("c") {
		t.Errorf("absent/null not distinguished: %v", p)
	}
}
