package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aurimasniekis/go-ledger/internal/domain/authz"
	"github.com/aurimasniekis/go-ledger/pkg/ledgerclient"
)

var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, tl := range Tools() {
		if !toolName.MatchString(tl.Name) || seen[tl.Name] {
			t.Errorf("tool name %q is invalid or duplicated", tl.Name)
		}
		seen[tl.Name] = true
		if tl.Title == "" || len(tl.Description) < 40 {
			t.Errorf("%s needs a title and a real description", tl.Name)
		}
		if !tl.Grant.Valid() {
			t.Errorf("%s declares unknown grant %s", tl.Name, tl.Grant)
		}
		switch {
		case tl.Kind == KindRead && tl.Method != http.MethodGet:
			t.Errorf("%s is a read tool on %s", tl.Name, tl.Method)
		case tl.Kind != KindRead && tl.Method == http.MethodGet:
			t.Errorf("%s is a %s tool on GET", tl.Name, tl.Kind)
		case tl.Method == http.MethodDelete && tl.Kind != KindDestructive:
			t.Errorf("%s deletes but is not marked destructive", tl.Name)
		case tl.Kind != KindRead && tl.Grant.Action != authz.ActionWrite:
			t.Errorf("%s changes state but needs %s", tl.Name, tl.Grant)
		}
		raw, _ := json.Marshal(tl.Schema)
		var sc jsonschema.Schema
		if err := json.Unmarshal(raw, &sc); err != nil {
			t.Fatalf("%s schema: %v", tl.Name, err)
		}
		if _, err := sc.Resolve(nil); err != nil {
			t.Errorf("%s schema does not resolve: %v", tl.Name, err)
		}
		if sc.Type != "object" || sc.AdditionalProperties == nil {
			t.Errorf("%s schema must be an object that refuses unknown members", tl.Name)
		}
		for name, p := range sc.Properties {
			if p.Description == "" && len(p.Enum) == 0 && p.Items == nil {
				t.Errorf("%s.%s has no description", tl.Name, name)
			}
		}
	}
}

// The argument mapping is explicit: path arguments are the placeholders of the
// path, and IfMatchArg is declared. Nothing else is taken out of the body.
func TestToolArgumentMappingHasNoCollisions(t *testing.T) {
	for _, tl := range Tools() {
		props, _ := tl.Schema["properties"].(map[string]any)
		req := map[string]bool{}
		required, _ := tl.Schema["required"].([]string)
		for _, n := range required {
			req[n] = true
		}
		for rest := tl.Path; strings.Contains(rest, "{"); {
			i := strings.Index(rest, "{")
			j := strings.Index(rest, "}")
			name := rest[i+1 : j]
			rest = rest[j+1:]
			if props[name] == nil || !req[name] {
				t.Errorf("%s: path argument %q must be a required argument", tl.Name, name)
			}
			if name == tl.IfMatchArg {
				t.Errorf("%s: %q is both a path argument and the If-Match argument", tl.Name, name)
			}
		}
		if tl.IfMatchArg != "" {
			if p, _ := props[tl.IfMatchArg].(map[string]any); p == nil || p["type"] != "integer" || !req[tl.IfMatchArg] {
				t.Errorf("%s: IfMatchArg %q must be a required integer argument", tl.Name, tl.IfMatchArg)
			}
		}
		// A top-level "version" that is not the If-Match argument stays in the body.
		if _, ok := props["version"]; ok && tl.IfMatchArg != "version" {
			r, err := buildRequest(tl, map[string]any{"id": "01JD3Y4C8V9M2Q7W5R6T0XZBNT", "version": float64(1)})
			if err != nil || r.IfMatch != "" {
				t.Errorf("%s: version must not become If-Match unless declared: %+v %v", tl.Name, r, err)
			}
		}
	}
}

func TestTooLargeResponseIsNotAFailedWrite(t *testing.T) {
	s := &server{}
	w := Tool{Name: "create_commitment", Kind: KindWrite}
	res := s.result(&boundTool{Tool: w}, Response{Status: 201, Truncated: true, Location: "/api/v1/commitments/01JD3Y4C8V9M2Q7W5R6T0XZBNT"})
	if res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "01JD3Y4C8V9M2Q7W5R6T0XZBNT") {
		t.Errorf("a truncated successful write must be a success that names the resource: %+v", res)
	}
	rd := s.result(&boundTool{Tool: Tool{Name: "list_commitments", Kind: KindRead}}, Response{Status: 200, Truncated: true})
	if !rd.IsError {
		t.Error("a truncated read has no result to return and must say so")
	}
}

func TestSchemasRejectBadArguments(t *testing.T) {
	if srv, err := NewServer(Options{}); err == nil || srv != nil {
		t.Fatal("a server without a caller must be refused")
	}
	s := &server{tools: map[string]*boundTool{}}
	for _, tl := range registry() {
		raw, _ := json.Marshal(tl.Schema)
		var sc jsonschema.Schema
		_ = json.Unmarshal(raw, &sc)
		res, _ := sc.Resolve(nil)
		s.tools[tl.Name] = &boundTool{Tool: tl, schema: res}
	}
	const id = "01JD3Y4C8V9M2Q7W5R6T0XZBNT"
	const sched = `"schedule":{"version":1,"frequency":"monthly","interval":1,"day":1}`
	cases := []struct {
		tool, args string
		ok         bool
	}{
		{"list_commitments", "{}", true},
		{"list_commitments", "", true},
		{"list_commitments", `{"kind":["service"],"limit":10}`, true},
		{"list_commitments", `{"kind":["bogus"]}`, false},
		{"list_commitments", `{"limit":0}`, false},
		{"list_commitments", `{"limit":201}`, false},
		{"list_commitments", `{"extra":1}`, false},
		{"list_commitments", "[1]", false},
		{"list_occurrences", "{}", false},
		{"list_occurrences", `{"month":"2026-13"}`, false},
		{"list_occurrences", `{"month":"2026-11"}`, true},
		{"get_commitment", `{"id":"nope"}`, false},
		{"get_commitment", `{"id":"` + id + `"}`, true},
		{"delete_commitment", `{"id":"` + id + `"}`, false},
		{"delete_commitment", `{"id":"` + id + `","version":2}`, true},
		{"create_commitment", `{"name":"x","kind":"service","amount":{"amount":"1.5","currency":"EUR"},"schedule":{"version":1,"frequency":"annual","month":2,"day":29},"start_date":"2026-01-01"}`, true},
		{"create_commitment", `{"name":"x","kind":"service","amount":{"amount":1.5,"currency":"EUR"},` + sched + `,"start_date":"2026-01-01"}`, false},
		{"create_commitment", `{"name":"x","kind":"service","amount":{"amount":"1.5","currency":"eur"},` + sched + `,"start_date":"2026-01-01"}`, false},
		{"create_commitment", `{"name":"x","kind":"service","amount":{"amount":"1.5","currency":"EUR"},"schedule":{"version":1,"frequency":"weekly"},"start_date":"2026-01-01"}`, false},
	}
	for _, c := range cases {
		_, bad := s.parse(s.tools[c.tool], json.RawMessage(c.args))
		if (bad == nil) != c.ok {
			t.Errorf("%s %s: accepted=%v, want %v", c.tool, c.args, bad == nil, c.ok)
		}
	}
}

func TestBuildRequest(t *testing.T) {
	get := func(name string) Tool { tl, _ := ToolByName(name); return tl }
	id := "01JD3Y4C8V9M2Q7W5R6T0XZBNT"
	r, err := buildRequest(get("list_occurrences"), map[string]any{"month": "2026-11", "commitment_id": []any{id, id}, "include_cancelled": true, "limit": float64(5)})
	if err != nil || r.Method != "GET" || r.Path != "/occurrences" || r.Body != nil {
		t.Fatal(r, err)
	}
	want := url.Values{"month": {"2026-11"}, "commitment_id": {id, id}, "include_cancelled": {"true"}, "limit": {"5"}}
	if r.Query.Encode() != want.Encode() {
		t.Errorf("query %v, want %v", r.Query, want)
	}
	r, err = buildRequest(get("delete_commitment"), map[string]any{"id": id, "version": float64(7)})
	if err != nil || r.Path != "/commitments/"+id || r.IfMatch != `"7"` || len(r.Query) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = buildRequest(get("create_commitment"), map[string]any{"name": "a/b?c", "kind": "service"})
	if err != nil || r.Path != "/commitments" || r.IfMatch != "" || !strings.Contains(string(r.Body), `"name":"a/b?c"`) {
		t.Fatalf("%+v %v", r, err)
	}
	// A path member is escaped, never spliced.
	r, err = buildRequest(get("get_commitment"), map[string]any{"id": "../users?x=1"})
	if err != nil || r.Path != "/commitments/..%2Fusers%3Fx=1" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := buildRequest(get("get_commitment"), map[string]any{}); err == nil {
		t.Error("a missing path member must be an error")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(60, 3, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("burst request %d refused", i)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("over the burst: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("keys are independent")
	}
	now = now.Add(wait)
	if ok, _ := l.Allow("a"); !ok {
		t.Error("a token is available after the advised wait")
	}
	now = now.Add(time.Hour)
	l.Allow("c")
	if len(l.buckets) != 1 {
		t.Errorf("idle buckets were not pruned: %d", len(l.buckets))
	}
}

// TestDocsListEveryTool keeps docs/mcp.md in step with the registry: it embeds
// Doc() between markers. Run with UPDATE_DOCS=1 to rewrite it.
func TestDocsListEveryTool(t *testing.T) {
	const path = "../../docs/mcp.md"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	const start, end = "<!-- tools:start -->\n", "<!-- tools:end -->"
	i, j := strings.Index(doc, start), strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatal("docs/mcp.md lacks the tools markers")
	}
	want := doc[:i+len(start)] + "\n" + Doc() + doc[j:]
	if os.Getenv("UPDATE_DOCS") != "" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if doc != want {
		t.Error("docs/mcp.md is out of date: run UPDATE_DOCS=1 go test ./internal/mcpserver -run TestDocsListEveryTool")
	}
}

// The stdio caller reaches the server over HTTP. A write whose answer is larger than the client keeps has still
// happened, so it must come back as a success naming the created resource and never as "unavailable", which a model
// would answer by calling create again.
func TestRemoteCallerReportsATooLargeWriteAnswerAsDone(t *testing.T) {
	const loc = "/api/v1/commitments/01JD3Y4C8V9M2Q7W5R6T0XZBNT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(strings.Repeat("x", 9<<20)))
	}))
	defer srv.Close()
	c, err := ledgerclient.New(srv.URL, "ldgr_test_secret_value")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Remote(c).Call(context.Background(), nil, Request{Method: http.MethodPost, Path: "/commitments", Body: []byte("{}"), Tool: "create_commitment"})
	if err != nil {
		t.Fatalf("a too-large answer must not be an error: %v", err)
	}
	if resp.Status != http.StatusCreated || !resp.Truncated || resp.Location != loc {
		t.Fatalf("response: status %d truncated=%v location=%q", resp.Status, resp.Truncated, resp.Location)
	}
	res := (&server{}).result(&boundTool{Tool: Tool{Name: "create_commitment", Kind: KindWrite}}, resp)
	if res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, loc) {
		t.Errorf("the tool result must be a success naming the resource: %+v", res)
	}
}

func TestOpenWorldHintMarksProviderTools(t *testing.T) {
	want := map[string]bool{"list_bank_institutions": true, "refresh_bank_connection": true}
	for _, tl := range registry() {
		if tl.OpenWorld != want[tl.Name] {
			t.Errorf("%s: OpenWorld = %v, want %v", tl.Name, tl.OpenWorld, want[tl.Name])
		}
	}
	srv, err := NewServer(Options{Caller: Remote(&ledgerclient.Client{})})
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range lt.Tools {
		got := tl.Annotations != nil && tl.Annotations.OpenWorldHint != nil && *tl.Annotations.OpenWorldHint
		if got != want[tl.Name] {
			t.Errorf("%s: openWorldHint = %v, want %v", tl.Name, got, want[tl.Name])
		}
		if want[tl.Name] {
			seen++
		}
	}
	if seen != len(want) {
		t.Errorf("found %d provider tools, want %d", seen, len(want))
	}
}
