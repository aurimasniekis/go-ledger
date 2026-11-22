package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
	"github.com/aurimasniekis/go-ledger/internal/redact"
)

func apiServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	log, err := logging.New(&bytes.Buffer{}, "debug", "json", redact.New())
	if err != nil {
		t.Fatal(err)
	}
	s := New(testConfig(), log, opts...)
	RegisterAPI(s, Deps{})
	return s
}

// TestOpenAPIContract is the contract test (ADR-0011): every route RegisterAPI
// registers under /api/v1 is documented in openapi.yaml, and every documented
// operation is registered.
func TestOpenAPIContract(t *testing.T) {
	s := apiServer(t)
	routes := s.APIRoutes(APIPrefix)
	if len(routes) == 0 {
		t.Fatal("no API routes registered")
	}
	for _, p := range CheckContract(OpenAPIYAML(), routes) {
		t.Error(p)
	}
}

func TestOpenAPIDocumentVersionMatchesPrefix(t *testing.T) {
	var doc struct {
		OpenAPI string `yaml:"openapi"`
		Info    struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
	}
	if err := yaml.Unmarshal(OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1.") {
		t.Errorf("openapi = %q, want 3.1.x", doc.OpenAPI)
	}
	if len(doc.Servers) != 1 || doc.Servers[0].URL != APIPrefix {
		t.Errorf("servers = %+v, want one server %s", doc.Servers, APIPrefix)
	}
	if major, _, _ := strings.Cut(doc.Info.Version, "."); "/api/v"+major != APIPrefix {
		t.Errorf("info.version %q does not match the major version of %s", doc.Info.Version, APIPrefix)
	}
}

func TestOpenAPIDocumentListsEveryErrorCode(t *testing.T) {
	codes := []Code{CodeValidation, CodeUnauthenticated, CodeForbidden, CodeConflict, CodeRateLimited,
		CodeUnavailable, CodeInternal, CodeNotFound, CodeTooLarge, CodeMethod, CodePrecondition,
		CodeVersionConflict, CodeUnsupportedType, CodeInvalidCredentials, CodeCSRF, CodeReauthRequired, CodeRootTokenRequired, CodePendingTerms,
		CodeEnrollmentRequired, CodeInvalidSecondFactor, CodePasswordSignInDisabled, CodeEvidenceRequired, CodeOccurrenceCancelled,
		CodeTransactionNotCountable, CodeTransactionNotDebit, CodeOverAllocated, CodeOverpaid, CodeDuplicateMatch, CodeMatchState, CodeUndoNotDecider, CodeUndoWindowClosed,
		CodeManualPaymentOtherOccurrence, CodeManualPaymentNotReplaceable, CodeNothingToConfirm, CodeReviewInherited,
		CodeBankReconnectRequired, CodeBankUnavailable, CodeBankRateLimited, CodeBankRejected, CodeConnectionNotActive, CodeRefreshInProgress, CodeRefreshCooldown, CodeConsentUnknown, CodeConnectUnavailable, CodeRatesRefreshUnavailable,
		CodeNothingOutstanding, CodeDeferralPlanExists, CodeDeferralPlanUndone, CodeAdjustmentRemoved, CodeDeferralCurrency}
	var doc map[string]any
	if err := yaml.Unmarshal(OpenAPIYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	enum, _ := resolve(doc, "#/components/schemas/Error/properties/code/enum")
	list, _ := enum.([]any)
	if len(list) != len(codes) {
		t.Errorf("the Error code enum has %d entries, httpapi defines %d", len(list), len(codes))
	}
	for _, c := range codes {
		found := false
		for _, v := range list {
			found = found || v == string(c)
		}
		if !found {
			t.Errorf("code %q is not in the OpenAPI Error enum", c)
		}
	}
}

func TestCheckContractReportsBothDirections(t *testing.T) {
	doc := []byte(`
openapi: 3.1.1
info: {title: t, version: 1.0.0}
servers: [{url: /api/v1}]
paths:
  /things:
    get:
      operationId: listThings
      responses: {default: {$ref: "#/components/responses/Error"}}
  /things/{id}:
    get:
      operationId: listThings
      responses: {default: {$ref: "#/components/responses/Missing"}}
  /gone:
    delete:
      responses: {default: {$ref: "#/components/responses/Error"}}
components:
  responses:
    Error: {description: e}
`)
	got := strings.Join(CheckContract(doc, []Route{
		{Method: "GET", Path: "/api/v1/things"},
		{Method: "GET", Path: "/api/v1/things/{id}"},
		{Method: "POST", Path: "/api/v1/things"},
		{Method: "GET", Path: "/api/v1/files/{path...}"},
		{Method: "GET", Path: "/api/v1/sub/"},
	}), "\n")
	for _, want := range []string{
		"POST /api/v1/things: registered but not documented",
		"DELETE /api/v1/gone: documented in the OpenAPI document but not registered",
		"DELETE /api/v1/gone: no operationId",
		`operationId "listThings" is also used`,
		`GET /api/v1/things/{id}: path parameter "id" is not declared`,
		"unresolved $ref #/components/responses/Missing",
		"GET /api/v1/things/{id}: default response is not",
		"GET /api/v1/files/{path...}: API routes must not use",
		"GET /api/v1/sub/: API routes must not use",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "GET /api/v1/things: registered but not documented") {
		t.Errorf("a documented route was reported:\n%s", got)
	}
	if p := CheckContract([]byte("openapi: [unclosed"), nil); len(p) != 1 || !strings.Contains(p[0], "does not parse") {
		t.Errorf("bad YAML: %v", p)
	}
}

func TestOpenAPIIsServedAsJSONAndYAML(t *testing.T) {
	s := apiServer(t)
	rec := do(s.Handler(), httptest.NewRequest("GET", "/api/v1/openapi.json", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("json: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var fromJSON map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fromJSON); err != nil {
		t.Fatal(err)
	}
	var fromYAML map[string]any
	if err := yaml.Unmarshal(OpenAPIYAML(), &fromYAML); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fromJSON)
	b, _ := json.Marshal(fromYAML)
	if !bytes.Equal(a, b) {
		t.Fatal("the JSON document differs from the YAML one")
	}
	rec = do(s.Handler(), httptest.NewRequest("GET", "/api/v1/openapi.yaml", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/yaml") || !bytes.Equal(rec.Body.Bytes(), OpenAPIYAML()) {
		t.Fatalf("yaml: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := do(s.Handler(), httptest.NewRequest("HEAD", "/api/v1/openapi.yaml", nil)); rec.Code != 200 {
		t.Fatalf("HEAD: %d", rec.Code)
	}
}

func TestUnmatchedAPIRequestsGetTheEnvelope(t *testing.T) {
	app := metrics.NewApp("test")
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("shell")) })
	s := apiServer(t, WithMetrics(app), WithUI(ui))
	s.HandleAPI("DELETE /api/v1/things/{id}", Public(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	s.HandleAPI("PATCH /api/v1/things/{id}", Public(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	for _, c := range []struct {
		method, path string
		status       int
		code         Code
		allow        string
	}{
		{"POST", "/api/v1/openapi.json", 405, CodeMethod, "GET, HEAD"},
		{"PUT", "/api/v1/things/01JD3Y4C8V9M2Q7W5R6T0XZBNA", 405, CodeMethod, "DELETE, PATCH"},
		{"BREW", "/api/v1/openapi.yaml", 405, CodeMethod, "GET, HEAD"},
		{"GET", "/api/v1/nope", 404, CodeNotFound, ""},
		{"POST", "/api/v1/nope", 404, CodeNotFound, ""},
		{"GET", "/api/v2/openapi.json", 404, CodeNotFound, ""},
		{"GET", "/api", 404, CodeNotFound, ""},
		{"GET", "/api/", 404, CodeNotFound, ""},
		{"DELETE", "/api/v1", 404, CodeNotFound, ""},
	} {
		rec := do(s.Handler(), httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s %s: status %d, want %d (%q)", c.method, c.path, rec.Code, c.status, rec.Body.String())
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s %s: Content-Type %q", c.method, c.path, ct)
		}
		b := decodeErr(t, rec)
		if b.Code != c.code || b.RequestID == "" {
			t.Errorf("%s %s: envelope %+v", c.method, c.path, b)
		}
		if got := rec.Header().Get("Allow"); got != c.allow {
			t.Errorf("%s %s: Allow %q, want %q", c.method, c.path, got, c.allow)
		}
		if strings.Contains(rec.Body.String(), "Method Not Allowed") || strings.Contains(rec.Body.String(), "404 page not found") {
			t.Errorf("%s %s: plain-text body leaked: %q", c.method, c.path, rec.Body.String())
		}
	}
	// Outside /api the UI still answers.
	if rec := do(s.Handler(), httptest.NewRequest("GET", "/apis", nil)); rec.Body.String() != "shell" {
		t.Errorf("/apis: %q", rec.Body.String())
	}

	var buf bytes.Buffer
	_ = app.Registry.Write(&buf)
	out := buf.String()
	for _, want := range []string{
		`ledger_http_requests_total{route="unmatched",method="POST",status="405"} 1`,
		`ledger_http_requests_total{route="unmatched",method="OTHER",status="405"} 1`,
		`ledger_http_requests_total{route="unmatched",method="GET",status="404"} 4`,
		`ledger_errors_total{kind="method_not_allowed"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Contains(out, "01JD3Y4C8V9M2Q7W5R6T0XZBNA") || strings.Contains(out, "/api/v1/nope") {
		t.Error("a raw path leaked into a metric label")
	}
}

func TestAPIRoutesNeedAMethod(t *testing.T) {
	s := apiServer(t)
	for _, p := range []string{"/api/v1/things", "example.com/api/v1/things", "GET example.com/api/v1/x"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Handle(%q) did not panic", p)
				}
			}()
			s.Handle(p, http.NotFoundHandler())
		}()
	}
}

func TestAPIMiddlewareWrapsMatchedRoutesOnly(t *testing.T) {
	var seen []string
	mw := func(tag string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, tag+" "+r.Pattern)
				next.ServeHTTP(w, r)
			})
		}
	}
	s := apiServer(t, WithAPIMiddleware(mw("outer"), mw("inner")))
	s.Handle("GET /ok", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	do(s.Handler(), httptest.NewRequest("GET", "/api/v1/openapi.yaml", nil))
	want := []string{"outer GET /api/v1/openapi.yaml", "inner GET /api/v1/openapi.yaml"}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("middleware calls %v, want %v", seen, want)
	}
	seen = nil
	do(s.Handler(), httptest.NewRequest("POST", "/api/v1/openapi.yaml", nil))
	do(s.Handler(), httptest.NewRequest("GET", "/api/v1/none", nil))
	do(s.Handler(), httptest.NewRequest("GET", "/ok", nil))
	if len(seen) != 0 {
		t.Fatalf("middleware ran outside matched API routes: %v", seen)
	}
}

func TestResourceErrorsMapToEnvelope(t *testing.T) {
	s := apiServer(t)
	s.HandleAPI("GET /api/v1/e/{kind}", Public(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("kind") {
		case "missing":
			WriteError(w, r, fmt.Errorf("load: %w", resource.ErrNotFound))
		case "conflict":
			WriteError(w, r, fmt.Errorf("save: %w", &resource.VersionConflictError{Expected: 1, Current: 4}))
		case "full":
			WriteError(w, r, VersionConflict(4, map[string]any{"id": "x", "version": 4}))
		}
	}))
	rec := do(s.Handler(), httptest.NewRequest("GET", "/api/v1/e/missing", nil))
	if rec.Code != 404 || decodeErr(t, rec).Code != CodeNotFound {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(s.Handler(), httptest.NewRequest("GET", "/api/v1/e/conflict", nil))
	b := decodeErr(t, rec)
	if rec.Code != 409 || b.Code != CodeVersionConflict || b.Reconciliation == nil || b.Reconciliation.CurrentVersion != 4 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("conflict: %d %s %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	if strings.Contains(rec.Body.String(), `"current":`) {
		t.Fatalf("conflict without a representation sent one: %s", rec.Body.String())
	}
	rec = do(s.Handler(), httptest.NewRequest("GET", "/api/v1/e/full", nil))
	if !strings.Contains(rec.Body.String(), `"reconciliation":{"current_version":4,"current":{"id":"x","version":4}}`) {
		t.Fatalf("full: %s", rec.Body.String())
	}
}

func TestPreconditionRequiredWording(t *testing.T) {
	for method, wantBody := range map[string]bool{"PATCH": true, "PUT": true, "DELETE": false} {
		_, err := ExpectedVersion(httptest.NewRequest(method, "/x", nil), nil)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != 428 {
			t.Fatalf("%s: %v", method, err)
		}
		if got := strings.Contains(apiErr.Message, "version member"); got != wantBody {
			t.Errorf("%s: message %q", method, apiErr.Message)
		}
	}
}

func TestExpectedVersion(t *testing.T) {
	five, zero := int64(5), int64(0)
	for _, c := range []struct {
		name    string
		ifMatch []string
		body    *int64
		want    int64
		status  int
		field   string
	}{
		{name: "header", ifMatch: []string{`"5"`}, want: 5},
		{name: "body", body: &five, want: 5},
		{name: "both agree", ifMatch: []string{` "5" `}, body: &five, want: 5},
		{name: "neither", status: 428},
		{name: "disagree", ifMatch: []string{`"4"`}, body: &five, status: 400, field: "version"},
		{name: "star", ifMatch: []string{"*"}, status: 400, field: "If-Match"},
		{name: "weak", ifMatch: []string{`W/"5"`}, status: 400, field: "If-Match"},
		{name: "list", ifMatch: []string{`"5", "6"`}, status: 400, field: "If-Match"},
		{name: "two headers", ifMatch: []string{`"5"`, `"6"`}, status: 400, field: "If-Match"},
		{name: "unquoted", ifMatch: []string{"5"}, status: 400, field: "If-Match"},
		{name: "zero", ifMatch: []string{`"0"`}, status: 400, field: "If-Match"},
		{name: "zero padded", ifMatch: []string{`"01"`}, status: 400, field: "If-Match"},
		{name: "empty tag", ifMatch: []string{`""`}, status: 400, field: "If-Match"},
		{name: "negative", ifMatch: []string{`"-1"`}, status: 400, field: "If-Match"},
		{name: "overflow", ifMatch: []string{`"99999999999999999999"`}, status: 400, field: "If-Match"},
		{name: "body zero", body: &zero, status: 400, field: "version"},
	} {
		r := httptest.NewRequest("PATCH", "/x", nil)
		for _, v := range c.ifMatch {
			r.Header.Add("If-Match", v)
		}
		got, err := ExpectedVersion(r, c.body)
		if c.status == 0 {
			if err != nil || got != c.want {
				t.Errorf("%s: %d %v, want %d", c.name, got, err, c.want)
			}
			continue
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != c.status || apiErr.Field != c.field {
			t.Errorf("%s: error %#v, want status %d field %q", c.name, err, c.status, c.field)
		}
	}
	if ETag(7) != `"7"` {
		t.Error(ETag(7))
	}
}
