package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/config"
	"github.com/aurimasniekis/go-ledger/internal/logging"
	"github.com/aurimasniekis/go-ledger/internal/metrics"
	"github.com/aurimasniekis/go-ledger/internal/redact"
)

const sentinel = "SENTINEL-httpapi-secret-5566778899"

func testConfig() config.HTTP {
	return config.HTTP{
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second,
		ShutdownTimeout: 5 * time.Second, MaxBodyBytes: 64,
	}
}

func TestMetricsUseRouteTemplatesNotRawPaths(t *testing.T) {
	app := metrics.NewApp("test")
	log, _ := logging.New(&bytes.Buffer{}, "debug", "json", redact.New())
	s := New(testConfig(), log, WithMetrics(app))
	s.Handle("GET /api/things/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "bad" {
			WriteError(w, r, Validation("id", "bad"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	s.Handle("GET /boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }))
	do := func(method, path string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	}
	do("GET", "/api/things/alice-12345")
	do("GET", "/api/things/98765")
	do("GET", "/api/things/bad")
	do("GET", "/no/such/path/with-user-id-777")
	do("BREW", "/api/things/1")
	do("GET", "/boom")

	var b bytes.Buffer
	_ = app.Registry.Write(&b)
	out := b.String()
	for _, want := range []string{
		`ledger_http_requests_total{route="/api/things/{id}",method="GET",status="204"} 2`,
		`ledger_http_requests_total{route="/api/things/{id}",method="GET",status="400"} 1`,
		`ledger_http_requests_total{route="unmatched",method="GET",status="404"} 1`,
		// An unknown method on an API route is a 405 (ADR-0011), still under the bounded "unmatched" label.
		`ledger_http_requests_total{route="unmatched",method="OTHER",status="405"} 1`,
		`ledger_http_requests_total{route="/boom",method="GET",status="500"} 1`,
		`ledger_http_request_duration_seconds_count{route="/api/things/{id}",method="GET"} 3`,
		`ledger_errors_total{kind="validation_failed"} 1`,
		`ledger_errors_total{kind="not_found"} 1`,
		`ledger_errors_total{kind="method_not_allowed"} 1`,
		`ledger_errors_total{kind="internal_error"} 1`,
		"ledger_http_requests_in_flight 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	for _, raw := range []string{"alice-12345", "98765", "user-id-777", "/no/such"} {
		if strings.Contains(out, raw) {
			t.Errorf("raw path fragment %q leaked into a label", raw)
		}
	}
}

func newTestServer(t *testing.T, cfg config.HTTP) (*Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	redact.Default().Add(sentinel) // error messages use the default redactor
	log, err := logging.New(&buf, "debug", "json", redact.New(sentinel))
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, log), &buf
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) ErrorBody {
	t.Helper()
	var b ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("not an error envelope: %q", rec.Body.String())
	}
	return b
}

func TestRequestIDEchoedAndOnEveryLogLine(t *testing.T) {
	s, buf := newTestServer(t, testConfig())
	s.Handle("GET /ok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logging.FromContext(r.Context()).InfoContext(r.Context(), "inside handler")
		_, _ = io.WriteString(w, "ok")
	}))
	req := httptest.NewRequest("GET", "/ok", nil)
	req.Header.Set(RequestIDHeader, "client-id-123")
	rec := do(s.Handler(), req)
	if got := rec.Header().Get(RequestIDHeader); got != "client-id-123" {
		t.Fatalf("request ID not echoed: %q", got)
	}
	lines := logLines(t, buf)
	if len(lines) != 2 {
		t.Fatalf("want handler and access lines, got %d: %s", len(lines), buf.String())
	}
	for _, l := range lines {
		if l["request_id"] != "client-id-123" {
			t.Errorf("line without request_id: %v", l)
		}
	}
}

func TestRequestIDGeneratedWhenMissingOrUnsafe(t *testing.T) {
	s, _ := newTestServer(t, testConfig())
	for _, in := range []string{"", "bad id with spaces", strings.Repeat("a", 65), "x\ny"} {
		req := httptest.NewRequest("GET", "/nothing", nil)
		if in != "" && !strings.ContainsAny(in, "\n") {
			req.Header.Set(RequestIDHeader, in)
		}
		rec := do(s.Handler(), req)
		got := rec.Header().Get(RequestIDHeader)
		if len(got) != 26 {
			t.Errorf("input %q: want a generated ULID, got %q", in, got)
		}
		if decodeErr(t, rec).RequestID != got {
			t.Errorf("envelope request_id differs from header")
		}
	}
}

func TestNotFoundUsesEnvelope(t *testing.T) {
	s, _ := newTestServer(t, testConfig())
	rec := do(s.Handler(), httptest.NewRequest("GET", "/nope", nil))
	b := decodeErr(t, rec)
	if rec.Code != 404 || b.Code != CodeNotFound || b.RequestID == "" {
		t.Fatalf("got %d %+v", rec.Code, b)
	}
}

func TestOversizedBodyIs413Envelope(t *testing.T) {
	s, buf := newTestServer(t, testConfig())
	s.Handle("POST /echo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if err := DecodeJSON(r, &v); err != nil {
			WriteError(w, r, err)
			return
		}
		w.WriteHeader(204)
	}))
	big := `{"note":"` + strings.Repeat("x", 200) + `"}`

	// Declared length over the limit: rejected before the handler runs.
	rec := do(s.Handler(), httptest.NewRequest("POST", "/echo", strings.NewReader(big)))
	if rec.Code != 413 || decodeErr(t, rec).Code != CodeTooLarge {
		t.Fatalf("declared: got %d %s", rec.Code, rec.Body)
	}

	// Unknown length (chunked): rejected when the handler reads past the limit.
	req := httptest.NewRequest("POST", "/echo", io.NopCloser(strings.NewReader(big)))
	req.ContentLength = -1
	rec = do(s.Handler(), req)
	b := decodeErr(t, rec)
	if rec.Code != 413 || b.Code != CodeTooLarge || b.RequestID == "" {
		t.Fatalf("chunked: got %d %s", rec.Code, rec.Body)
	}

	// A body within the limit still works.
	rec = do(s.Handler(), httptest.NewRequest("POST", "/echo", strings.NewReader(`{"a":1}`)))
	if rec.Code != 204 {
		t.Fatalf("small body: got %d", rec.Code)
	}
	if strings.Contains(buf.String(), "xxxxxxxx") {
		t.Fatal("request body was logged")
	}
}

func TestDecodeJSONValidation(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	for _, body := range []string{"{", `{"b":1}`, `{"a":1}{"a":2}`} {
		err := DecodeJSON(httptest.NewRequest("POST", "/", strings.NewReader(body)), &v)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeValidation || e.Status != 400 {
			t.Errorf("%q: got %v", body, err)
		}
	}
}

func TestErrorEnvelopeCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   Code
		field  string
	}{
		{Validation("amount", "must be positive"), 400, CodeValidation, "amount"},
		{Unauthenticated("sign in"), 401, CodeUnauthenticated, ""},
		{Forbidden("no"), 403, CodeForbidden, ""},
		{Conflict("name taken"), 409, CodeConflict, ""},
		{VersionConflict(3, nil), 409, CodeVersionConflict, ""},
		{unsupportedMediaType("use merge patch"), 415, CodeUnsupportedType, ""},
		{RateLimited("slow down"), 429, CodeRateLimited, ""},
		{Unavailable("database unavailable", errors.New("disk I/O")), 503, CodeUnavailable, ""},
		{errors.New("raw failure"), 500, CodeInternal, ""},
	}
	for _, c := range cases {
		s, _ := newTestServer(t, testConfig())
		err := c.err
		s.Handle("GET /e", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { WriteError(w, r, err) }))
		rec := do(s.Handler(), httptest.NewRequest("GET", "/e", nil))
		b := decodeErr(t, rec)
		if rec.Code != c.status || b.Code != c.code || b.Field != c.field || b.RequestID == "" {
			t.Errorf("%v: got %d %+v", c.err, rec.Code, b)
		}
		if strings.Contains(rec.Body.String(), "raw failure") || strings.Contains(rec.Body.String(), "disk I/O") {
			t.Errorf("cause leaked to the client: %s", rec.Body)
		}
	}
}

func TestPanicRecoveryHidesDetail(t *testing.T) {
	s, buf := newTestServer(t, testConfig())
	s.Handle("GET /boom", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom " + sentinel)
	}))
	rec := do(s.Handler(), httptest.NewRequest("GET", "/boom", nil))
	b := decodeErr(t, rec)
	if rec.Code != 500 || b.Code != CodeInternal || b.RequestID == "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "kaboom") || strings.Contains(body, "goroutine") || strings.Contains(body, ".go:") {
		t.Fatalf("panic detail in response: %s", body)
	}
	if !strings.Contains(buf.String(), "panic in handler") || !strings.Contains(buf.String(), "kaboom") {
		t.Fatalf("panic not logged: %s", buf.String())
	}
	if strings.Contains(buf.String(), sentinel) {
		t.Fatal("sentinel secret leaked into the log")
	}
	for _, l := range logLines(t, buf) {
		if l["request_id"] != b.RequestID {
			t.Errorf("line without the request ID: %v", l)
		}
	}
}

func TestNoBodyOrQueryLoggedAndSecretsRedacted(t *testing.T) {
	s, buf := newTestServer(t, testConfig())
	s.Handle("POST /login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		logging.FromContext(r.Context()).InfoContext(r.Context(), "handler saw "+sentinel,
			"password", sentinel, "size", len(b), "err", errors.New("failed with "+sentinel))
		WriteError(w, r, Validation("", "bad value "+sentinel))
	}))
	req := httptest.NewRequest("POST", "/login?token="+sentinel, strings.NewReader(`{"password":"`+sentinel+`"}`))
	req.Header.Set("Authorization", "Bearer "+sentinel)
	rec := do(s.Handler(), req)
	if strings.Contains(buf.String(), sentinel) {
		t.Fatalf("secret in logs: %s", buf.String())
	}
	if strings.Contains(rec.Body.String(), sentinel) {
		t.Fatalf("secret in error body: %s", rec.Body)
	}
}

func TestDefaultRedactorUsedForErrorMessages(t *testing.T) {
	s, _ := newTestServer(t, testConfig())
	s.Handle("GET /e", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, Validation("f", "got "+sentinel))
	}))
	rec := do(s.Handler(), httptest.NewRequest("GET", "/e", nil))
	if strings.Contains(rec.Body.String(), sentinel) {
		t.Fatalf("secret in error body: %s", rec.Body)
	}
}

func TestTimeoutsAndLimitsFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.ReadHeaderTimeout, cfg.ReadTimeout, cfg.WriteTimeout, cfg.IdleTimeout = time.Second, 2*time.Second, 3*time.Second, 4*time.Second
	s, _ := newTestServer(t, cfg)
	h := s.http
	if h.ReadHeaderTimeout != time.Second || h.ReadTimeout != 2*time.Second || h.WriteTimeout != 3*time.Second || h.IdleTimeout != 4*time.Second {
		t.Fatalf("timeouts not applied: %+v", h)
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestGracefulShutdownDrainsInFlightRequests(t *testing.T) {
	s, buf := newTestServer(t, testConfig())
	started, release := make(chan struct{}), make(chan struct{})
	s.Handle("GET /slow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	}))
	ln := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()

	type result struct {
		body string
		err  error
	}
	resc := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			resc <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		resc <- result{string(b), err}
	}()
	<-started
	cancel() // begin shutdown while the request is in flight

	select {
	case err := <-done:
		t.Fatalf("Run returned before the in-flight request finished: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	// New connections are refused once shutdown has begun.
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond); err == nil {
		_ = c.Close()
		t.Error("listener still accepting during shutdown")
	}
	close(release)

	if r := <-resc; r.err != nil || r.body != "finished" {
		t.Fatalf("in-flight request was not drained: %+v", r)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(buf.String(), "shutdown complete") {
		t.Fatalf("missing shutdown log: %s", buf.String())
	}
}

func TestShutdownTimeoutClosesStuckRequests(t *testing.T) {
	cfg := testConfig()
	cfg.ShutdownTimeout = 100 * time.Millisecond
	s, _ := newTestServer(t, cfg)
	started := make(chan struct{})
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	s.Handle("GET /stuck", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-block
	}))
	ln := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	go func() {
		if resp, err := http.Get("http://" + ln.Addr().String() + "/stuck"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline exceeded, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not give up after the timeout")
	}
}

func TestRequestIDOnLinesLoggedWithoutContext(t *testing.T) {
	s, buf := newTestServer(t, testConfig())
	s.Handle("GET /noctx", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logging.FromContext(r.Context()).Info("no-ctx-line")
		logging.FromContext(r.Context()).InfoContext(r.Context(), "ctx-line")
	}))
	rec := do(s.Handler(), httptest.NewRequest("GET", "/noctx", nil))
	id := rec.Header().Get(RequestIDHeader)
	lines := logLines(t, buf)
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %s", len(lines), buf.String())
	}
	for _, l := range lines {
		if l["request_id"] != id {
			t.Errorf("line without request_id: %v", l)
		}
	}
	if n := strings.Count(buf.String(), "request_id"); n != 3 {
		t.Errorf("request_id duplicated: %s", buf.String())
	}
}

func TestAbortedRequestStillAccessLogged(t *testing.T) {
	s, buf := newTestServer(t, testConfig())
	s.Handle("GET /abort", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	func() {
		defer func() { _ = recover() }()
		do(s.Handler(), httptest.NewRequest("GET", "/abort", nil))
	}()
	lines := logLines(t, buf)
	if len(lines) != 1 || lines[0]["msg"] != "request" || lines[0]["aborted"] != true || lines[0]["request_id"] == nil {
		t.Fatalf("want one aborted access line, got %s", buf.String())
	}
}

func TestPanicAfterResponseStartedCountsAsInternalError(t *testing.T) {
	app := metrics.NewApp("test")
	log, _ := logging.New(&bytes.Buffer{}, "debug", "json", redact.New())
	s := New(testConfig(), log, WithMetrics(app))
	s.Handle("GET /partial", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	}))
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/partial", nil))
	var b bytes.Buffer
	_ = app.Registry.Write(&b)
	want := fmt.Sprintf("ledger_errors_total{kind=%q} 1", string(CodeInternal))
	if !strings.Contains(b.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, b.String())
	}
}
