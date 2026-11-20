package apiexample

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/httpapi"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/redact"
	"github.com/aurimasniekis/go-ledger/internal/store"
)

func newServer(t *testing.T) (*httpapi.Server, *Store) {
	t.Helper()
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"), store.Options{Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log, err := logging.New(io.Discard, "error", "json", redact.New())
	if err != nil {
		t.Fatal(err)
	}
	srv := httpapi.New(config.HTTP{MaxBodyBytes: 1 << 16, ShutdownTimeout: time.Second}, log)
	httpapi.RegisterAPI(srv, httpapi.Deps{})
	st := &Store{DB: db, Now: time.Now}
	Register(srv, st)
	return srv, st
}

type resp struct {
	*httptest.ResponseRecorder
}

func call(t *testing.T, srv *httpapi.Server, method, path, body string, header ...string) resp {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, r)
	if method == "PATCH" {
		req.Header.Set("Content-Type", httpapi.MergePatchType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return resp{rec}
}

func (r resp) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", r.Body.String(), err)
	}
}

func (r resp) errBody(t *testing.T) httpapi.ErrorBody {
	t.Helper()
	var b httpapi.ErrorBody
	r.decode(t, &b)
	return b
}

func create(t *testing.T, srv *httpapi.Server, name, kind string) Example {
	t.Helper()
	r := call(t, srv, "POST", Path, fmt.Sprintf(`{"name":%q,"kind":%q}`, name, kind))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	var e Example
	r.decode(t, &e)
	if r.Header().Get("ETag") != `"1"` || r.Header().Get("Location") != Path+"/"+e.ID || e.Version != 1 {
		t.Fatalf("create headers: %v %+v", r.Header(), e)
	}
	return e
}

// TestExampleContract runs the contract check over the example routes with
// the example paths merged into the published document.
func TestExampleContract(t *testing.T) {
	srv, _ := newServer(t)
	var doc, frag map[string]any
	if err := yaml.Unmarshal(httpapi.OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(OpenAPIPaths, &frag); err != nil {
		t.Fatal(err)
	}
	for _, section := range [][2]string{{"paths", ""}, {"components", "schemas"}} {
		dst, src := doc[section[0]].(map[string]any), frag[section[0]].(map[string]any)
		if section[1] != "" {
			dst, src = dst[section[1]].(map[string]any), src[section[1]].(map[string]any)
		}
		for k, v := range src {
			if _, dup := dst[k]; dup {
				t.Fatalf("example %s.%s collides with the published document", section[0], k)
			}
			dst[k] = v
		}
	}
	merged, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	routes := srv.APIRoutes(httpapi.APIPrefix)
	if n := len(srv.APIRoutes(Path)); n != 5 {
		t.Fatalf("example routes: %d in %v", n, routes)
	}
	for _, p := range httpapi.CheckContract(merged, routes) {
		t.Error(p)
	}
	// And the published document does not contain the example.
	if p := httpapi.CheckContract(httpapi.OpenAPIYAML(), routes); len(p) != 5 {
		t.Errorf("the published document should lack exactly the 5 example operations, got %v", p)
	}
}

func TestListPaginatesAndFilters(t *testing.T) {
	srv, _ := newServer(t)
	var alphaNet []string
	for i := range 7 {
		create(t, srv, fmt.Sprintf("Net %d", i), "alpha")
		create(t, srv, fmt.Sprintf("Other %d", i), "alpha")
		create(t, srv, fmt.Sprintf("Net beta %d", i), "beta")
	}
	// The expected set, in ID order, read without paging.
	var all httpapi.Page[Example]
	call(t, srv, "GET", Path+"?limit=200", "").decode(t, &all)
	if len(all.Items) != 21 || all.NextCursor != nil {
		t.Fatalf("unpaged list: %d items, cursor %v", len(all.Items), all.NextCursor)
	}
	for i, e := range all.Items {
		if i > 0 && all.Items[i-1].ID >= e.ID {
			t.Fatal("items are not in ID order")
		}
		if e.Kind == "alpha" && len(e.Name) >= 3 && e.Name[:3] == "Net" {
			alphaNet = append(alphaNet, e.ID)
		}
	}

	q := url.Values{"kind": {"alpha"}, "name[prefix]": {"Net"}, "limit": {"3"}}
	var got []string
	pages := 0
	for {
		r := call(t, srv, "GET", Path+"?"+q.Encode(), "")
		if r.Code != 200 {
			t.Fatalf("page %d: %d %s", pages, r.Code, r.Body.String())
		}
		var p httpapi.Page[Example]
		r.decode(t, &p)
		pages++
		for _, e := range p.Items {
			got = append(got, e.ID)
		}
		if p.NextCursor == nil {
			break
		}
		if len(p.Items) != 3 {
			t.Fatalf("a non-final page has %d items", len(p.Items))
		}
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
		q.Set("cursor", *p.NextCursor)
	}
	if fmt.Sprint(got) != fmt.Sprint(alphaNet) || pages != 3 {
		t.Fatalf("paged %d pages: %v, want %v", pages, got, alphaNet)
	}

	// Several values of an equality filter mean any of them.
	var both httpapi.Page[Example]
	call(t, srv, "GET", Path+"?kind=alpha&kind=beta&limit=200", "").decode(t, &both)
	if len(both.Items) != 21 {
		t.Fatalf("kind=alpha&kind=beta: %d", len(both.Items))
	}
	// An empty page has items: [] and next_cursor: null.
	r := call(t, srv, "GET", Path+"?name[prefix]=Zzz", "")
	if r.Body.String() != "{\"items\":[],\"next_cursor\":null}\n" {
		t.Fatalf("empty page: %q", r.Body.String())
	}

	// A cursor is bound to its filters.
	var first httpapi.Page[Example]
	call(t, srv, "GET", Path+"?kind=alpha&limit=2", "").decode(t, &first)
	for _, c := range []struct{ query, field string }{
		{"kind=beta&cursor=" + *first.NextCursor, "cursor"},
		{"cursor=" + *first.NextCursor, "cursor"},
		{"cursor=not-a-cursor", "cursor"},
		{"limit=0", "limit"},
		{"limit=201", "limit"},
		{"limit=x", "limit"},
		{"kind=gamma", "kind"},
		{"kind[prefix]=al", "kind[prefix]"},
		{"name=Net", "name"},
		{"name[prefix]=a&name[prefix]=b", "name[prefix]"},
		{"colour=red", "colour"},
		{"kind=", "kind"},
		{"%zz", ""},
	} {
		r := call(t, srv, "GET", Path+"?"+c.query, "")
		b := r.errBody(t)
		if r.Code != 400 || b.Code != httpapi.CodeValidation || b.Field != c.field {
			t.Errorf("%s: %d %+v, want 400 on %q", c.query, r.Code, b, c.field)
		}
	}
	// The same filters in another order keep the cursor valid.
	r = call(t, srv, "GET", Path+"?limit=2&cursor="+*first.NextCursor+"&kind=alpha", "")
	if r.Code != 200 {
		t.Fatalf("same filters: %d %s", r.Code, r.Body.String())
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	srv, _ := newServer(t)
	e := create(t, srv, "Hosting", "alpha")
	item := Path + "/" + e.ID

	r := call(t, srv, "GET", item, "")
	if r.Code != 200 || r.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %v", r.Code, r.Header())
	}
	// Two clients read version 1. The first change wins.
	r = call(t, srv, "PATCH", item, `{"name":"Hosting EU"}`, "If-Match", `"1"`)
	if r.Code != 200 || r.Header().Get("ETag") != `"2"` {
		t.Fatalf("first change: %d %s", r.Code, r.Body.String())
	}
	// The second, still based on version 1, is a 409 with what changed.
	r = call(t, srv, "PATCH", item, `{"kind":"beta"}`, "If-Match", `"1"`)
	if r.Code != 409 || r.Header().Get("ETag") != `"2"` {
		t.Fatalf("stale change: %d %s", r.Code, r.Body.String())
	}
	var conflict struct {
		Code           string `json:"code"`
		Reconciliation struct {
			CurrentVersion int64   `json:"current_version"`
			Current        Example `json:"current"`
		} `json:"reconciliation"`
	}
	r.decode(t, &conflict)
	if conflict.Code != "version_conflict" || conflict.Reconciliation.CurrentVersion != 2 ||
		conflict.Reconciliation.Current.Name != "Hosting EU" || conflict.Reconciliation.Current.Kind != "alpha" {
		t.Fatalf("reconciliation: %+v", conflict)
	}
	// The stale change was not applied.
	var now Example
	call(t, srv, "GET", item, "").decode(t, &now)
	if now.Kind != "alpha" || now.Version != 2 {
		t.Fatalf("after conflict: %+v", now)
	}
	// Retrying with the current version succeeds; the body version works too.
	r = call(t, srv, "PATCH", item, `{"kind":"beta","version":2}`)
	if r.Code != 200 || r.Header().Get("ETag") != `"3"` {
		t.Fatalf("retry: %d %s", r.Code, r.Body.String())
	}
	// A change that names no version is refused before anything is written.
	r = call(t, srv, "PATCH", item, `{"kind":"alpha"}`)
	if r.Code != 428 || r.errBody(t).Code != httpapi.CodePrecondition {
		t.Fatalf("no version: %d %s", r.Code, r.Body.String())
	}
	// Missing objects are 404, not 409.
	missing := Path + "/01JD3Y4C8V9M2Q7W5R6T0XZBNA"
	for _, m := range []string{"GET", "PATCH", "DELETE"} {
		body := ""
		if m == "PATCH" {
			body = `{"name":"x"}`
		}
		if r := call(t, srv, m, missing, body, "If-Match", `"1"`); r.Code != 404 || r.errBody(t).Code != httpapi.CodeNotFound {
			t.Errorf("%s missing: %d %s", m, r.Code, r.Body.String())
		}
	}
	// Delete follows the same rule.
	if r := call(t, srv, "DELETE", item, "", "If-Match", `"2"`); r.Code != 409 {
		t.Fatalf("stale delete: %d %s", r.Code, r.Body.String())
	}
	if r := call(t, srv, "DELETE", item, ""); r.Code != 428 {
		t.Fatalf("delete without version: %d", r.Code)
	}
	if r := call(t, srv, "DELETE", item, "", "If-Match", `"3"`); r.Code != 204 {
		t.Fatalf("delete: %d %s", r.Code, r.Body.String())
	}
	if r := call(t, srv, "GET", item, ""); r.Code != 404 {
		t.Fatalf("after delete: %d", r.Code)
	}
}

func TestMergePatchSemantics(t *testing.T) {
	srv, _ := newServer(t)
	e := create(t, srv, "Hosting", "alpha")
	item := Path + "/" + e.ID
	patch := func(version int, body string) Example {
		t.Helper()
		r := call(t, srv, "PATCH", item, body, "If-Match", fmt.Sprintf(`"%d"`, version))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", body, r.Code, r.Body.String())
		}
		var got Example
		r.decode(t, &got)
		return got
	}
	got := patch(1, `{"note":"renewal in May"}`)
	if got.Note == nil || *got.Note != "renewal in May" || got.Name != "Hosting" {
		t.Fatalf("set note: %+v", got)
	}
	// Absent = unchanged: the note survives a change to another member.
	got = patch(2, `{"name":"Hosting EU"}`)
	if got.Note == nil || *got.Note != "renewal in May" || got.Name != "Hosting EU" {
		t.Fatalf("absent note changed: %+v", got)
	}
	// An empty patch is a no-op: no version bump, precondition still checked.
	if got = patch(3, `{}`); got.Version != 3 || got.Name != "Hosting EU" || got.Note == nil {
		t.Fatalf("empty patch: %+v", got)
	}
	if got = patch(3, `{"version":3}`); got.Version != 3 {
		t.Fatalf("version-only patch: %+v", got)
	}
	if r := call(t, srv, "PATCH", item, `{}`, "If-Match", `"2"`); r.Code != 409 || r.errBody(t).Code != httpapi.CodeVersionConflict {
		t.Fatalf("empty patch, stale version: %d %s", r.Code, r.Body.String())
	}
	if r := call(t, srv, "PATCH", item, `{}`); r.Code != 428 {
		t.Fatalf("empty patch, no version: %d", r.Code)
	}
	if r := call(t, srv, "PATCH", Path+"/01JD3Y4C8V9M2Q7W5R6T0XZBNA", `{}`, "If-Match", `"1"`); r.Code != 404 {
		t.Fatalf("empty patch, missing object: %d", r.Code)
	}
	if r := call(t, srv, "GET", item, ""); r.Header().Get("ETag") != `"3"` {
		t.Fatalf("etag after empty patch: %q", r.Header().Get("ETag"))
	}
	// A non-object patch says what it must be.
	if r := call(t, srv, "PATCH", item, `[1]`, "If-Match", `"3"`); r.Code != 400 || !strings.Contains(r.errBody(t).Message, "must be a JSON object") {
		t.Fatalf("array patch: %d %s", r.Code, r.Body.String())
	}
	// null = clear.
	got = patch(3, `{"note":null}`)
	if got.Note != nil || got.Name != "Hosting EU" {
		t.Fatalf("clear note: %+v", got)
	}
	var raw map[string]any
	call(t, srv, "GET", item, "").decode(t, &raw)
	if v, ok := raw["note"]; !ok || v != nil {
		t.Fatalf("a cleared note is sent as null: %v", raw)
	}
	// null on a required member, wrong content type, unknown member.
	if r := call(t, srv, "PATCH", item, `{"name":null}`, "If-Match", `"4"`); r.Code != 400 || r.errBody(t).Field != "name" {
		t.Fatalf("null name: %d %s", r.Code, r.Body.String())
	}
	if r := call(t, srv, "PATCH", item, `{"name":"x"}`, "If-Match", `"4"`, "Content-Type", "application/json"); r.Code != 415 || r.errBody(t).Code != httpapi.CodeUnsupportedType {
		t.Fatalf("json content type: %d %s", r.Code, r.Body.String())
	}
	if r := call(t, srv, "PATCH", item, `{"id":"x"}`, "If-Match", `"4"`); r.Code != 400 || r.errBody(t).Field != "id" {
		t.Fatalf("unknown member: %d %s", r.Code, r.Body.String())
	}
	// DELETE takes If-Match only: a body version does not count (ADR-0015).
	if r := call(t, srv, "DELETE", item, `{"version":5}`); r.Code != 428 {
		t.Fatalf("delete with body version: %d", r.Code)
	}
}

func TestConcurrentWritersOneWins(t *testing.T) {
	srv, _ := newServer(t)
	e := create(t, srv, "Domain", "alpha")
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := call(t, srv, "PATCH", Path+"/"+e.ID, fmt.Sprintf(`{"name":"writer %d"}`, i), "If-Match", `"1"`)
			codes[i] = r.Code
		}()
	}
	wg.Wait()
	won, lost := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			won++
		case 409:
			lost++
		}
	}
	if won != 1 || lost != n-1 {
		t.Fatalf("codes %v: want one 200 and %d 409s", codes, n-1)
	}
}

func TestCreateValidation(t *testing.T) {
	srv, _ := newServer(t)
	for body, field := range map[string]string{
		`{"kind":"alpha"}`:            "name",
		`{"name":"x"}`:                "kind",
		`{"name":"","kind":"alpha"}`:  "name",
		`{"name":"x","kind":"gamma"}`: "kind",
		`{"name":"x","kind":"alpha","id":"01JD3Y4C8V9M2Q7W5R6T0XZBNA"}`: "",
	} {
		r := call(t, srv, "POST", Path, body)
		if b := r.errBody(t); r.Code != 400 || b.Field != field {
			t.Errorf("%s: %d %+v", body, r.Code, b)
		}
	}
}

func TestCreateWithNote(t *testing.T) {
	srv, _ := newServer(t)
	r := call(t, srv, "POST", Path, `{"name":"Rent","kind":"beta","note":"due on the 1st"}`)
	var e Example
	r.decode(t, &e)
	if r.Code != 201 || e.Note == nil || *e.Note != "due on the 1st" {
		t.Fatalf("create with note: %d %s", r.Code, r.Body.String())
	}
	var got Example
	call(t, srv, "GET", Path+"/"+e.ID, "").decode(t, &got)
	if got.Note == nil || *got.Note != "due on the 1st" {
		t.Fatalf("stored note: %+v", got)
	}
	// null is the same as leaving it out; an empty or oversized note is refused.
	r = call(t, srv, "POST", Path, `{"name":"Rent","kind":"beta","note":null}`)
	r.decode(t, &e)
	if r.Code != 201 || e.Note != nil {
		t.Fatalf("null note: %d %s", r.Code, r.Body.String())
	}
	for _, n := range []string{`""`, `"` + strings.Repeat("x", 201) + `"`, `5`} {
		if r := call(t, srv, "POST", Path, `{"name":"Rent","kind":"beta","note":`+n+`}`); r.Code != 400 {
			t.Errorf("note %.10s: %d", n, r.Code)
		}
	}
}
