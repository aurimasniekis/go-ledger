package httpapi

import (
	"encoding/json"
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

func TestDecodeMergePatchNotObjectMessage(t *testing.T) {
	for _, body := range []string{`[1]`, `"s"`, `3`, `null`, `true`} {
		r := httptest.NewRequest("PATCH", "/x", strings.NewReader(body))
		r.Header.Set("Content-Type", MergePatchType)
		_, err := DecodeMergePatch(r, "a")
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != 400 || !strings.Contains(apiErr.Message, "must be a JSON object") {
			t.Errorf("%s: %#v", body, err)
		}
	}
}

func TestApplyMergePatch(t *testing.T) {
	// RFC 7396 appendix A examples, plus nested null delete and array replace.
	for _, c := range []struct{ name, target, patch, want string }{
		{"replace member", `{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{"add member", `{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{"delete member", `{"a":"b"}`, `{"a":null}`, `{}`},
		{"delete keeps others", `{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{"array replaced by scalar", `{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{"scalar replaced by array", `{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{"arrays replace whole", `{"a":[1,2,3]}`, `{"a":[9]}`, `{"a":[9]}`},
		{"nested merge", `{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{"nested null deletes one member", `{"s":{"x":1,"y":2}}`, `{"s":{"x":null}}`, `{"s":{"y":2}}`},
		{"nested add", `{"s":{"x":1}}`, `{"s":{"z":{"k":true}}}`, `{"s":{"x":1,"z":{"k":true}}}`},
		{"nested null inside new object dropped", `{}`, `{"s":{"x":null,"y":1}}`, `{"s":{"y":1}}`},
		{"object over scalar", `{"a":1}`, `{"a":{"b":1}}`, `{"a":{"b":1}}`},
		{"empty patch", `{"a":{"b":1}}`, `{}`, `{"a":{"b":1}}`},
		{"patch replaces non-object target", `[1]`, `{"a":1}`, `{"a":1}`},
		{"non-object patch replaces", `{"a":1}`, `[1]`, `[1]`},
		{"empty target", ``, `{"a":{"b":null,"c":1}}`, `{"a":{"c":1}}`},
	} {
		got, err := ApplyMergePatch(json.RawMessage(c.target), json.RawMessage(c.patch))
		if err != nil || string(got) != c.want {
			t.Errorf("%s: got %s, %v; want %s", c.name, got, err, c.want)
		}
	}
	if _, err := ApplyMergePatch(nil, json.RawMessage(`{`)); err == nil {
		t.Error("invalid patch accepted")
	}
	if _, err := ApplyMergePatch(nil, json.RawMessage(`{} {}`)); err == nil {
		t.Error("trailing data in patch accepted")
	}
}

func TestApplyMergePatchKeepsNumbers(t *testing.T) {
	// Numbers must round-trip as written: float64 would turn 10000000000000001
	// into ...000 and 12345678901234567890 into ...567000, and drop 1.10's zero.
	for _, c := range []struct{ name, target, patch, want string }{
		{"untouched large integer", `{"a":1,"b":10000000000000001}`, `{"a":2}`, `{"a":2,"b":10000000000000001}`},
		{"untouched big integer", `{"a":1,"b":12345678901234567890}`, `{"a":2}`, `{"a":2,"b":12345678901234567890}`},
		{"untouched trailing-zero decimal", `{"a":1,"b":1.10}`, `{"a":2}`, `{"a":2,"b":1.10}`},
		{"untouched nested numbers", `{"s":{"n":12345678901234567890,"d":1.10}}`, `{"s":{"x":1}}`, `{"s":{"d":1.10,"n":12345678901234567890,"x":1}}`},
		{"patch large integer", `{}`, `{"a":10000000000000001}`, `{"a":10000000000000001}`},
		{"patch big integer", `{"a":1}`, `{"a":12345678901234567890}`, `{"a":12345678901234567890}`},
		{"patch trailing-zero decimal", `{"a":1}`, `{"a":1.10}`, `{"a":1.10}`},
		{"patch nested numbers", `{}`, `{"s":{"n":12345678901234567890,"d":1.10}}`, `{"s":{"d":1.10,"n":12345678901234567890}}`},
		{"patch exponent kept as written", `{}`, `{"a":1e400}`, `{"a":1e400}`},
		{"untouched exponent", `{"b":1E+2}`, `{"a":1}`, `{"a":1,"b":1E+2}`},
	} {
		got, err := ApplyMergePatch(json.RawMessage(c.target), json.RawMessage(c.patch))
		if err != nil || string(got) != c.want {
			t.Errorf("%s: got %s, %v; want %s", c.name, got, err, c.want)
		}
	}
}

func TestMergePatchApplyAbsentMember(t *testing.T) {
	r := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"a":1}`))
	r.Header.Set("Content-Type", MergePatchType)
	p, err := DecodeMergePatch(r, "a", "s")
	if err != nil {
		t.Fatal(err)
	}
	current := json.RawMessage(`{"x":1}`)
	got, err := p.Apply("s", current)
	if err != nil || string(got) != string(current) {
		t.Errorf("absent member: got %s, %v; want %s unchanged", got, err, current)
	}
	got, err = p.Apply("s", nil)
	if err != nil || got != nil {
		t.Errorf("absent member, no target: got %s, %v; want nil", got, err)
	}
}

func TestMergePatchApplyMember(t *testing.T) {
	r := httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"s":{"x":null,"y":2}}`))
	r.Header.Set("Content-Type", MergePatchType)
	p, err := DecodeMergePatch(r, "s")
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Apply("s", json.RawMessage(`{"x":1,"z":3}`))
	if err != nil || string(got) != `{"y":2,"z":3}` {
		t.Errorf("%s %v", got, err)
	}
}
