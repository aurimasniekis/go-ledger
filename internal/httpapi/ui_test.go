package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithUIServesFallbackButNotUnderAPI(t *testing.T) {
	srv, _ := newTestServer(t, testConfig())
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("shell")) })
	srv2 := New(srv.cfg, srv.log, WithUI(ui))
	srv2.Handle("GET /api/v1/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pong")) }))

	for path, want := range map[string]string{"/": "shell", "/some/page": "shell", "/api/v1/ping": "pong"} {
		rec := httptest.NewRecorder()
		srv2.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || rec.Body.String() != want {
			t.Fatalf("%s: got %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	for _, c := range [][2]string{{"GET", "/api/v1/nope"}, {"GET", "/api"}, {"POST", "/api"}, {"GET", "/api/"}} {
		rec := httptest.NewRecorder()
		srv2.Handler().ServeHTTP(rec, httptest.NewRequest(c[0], c[1], nil))
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Fatalf("%s %s: %d %q", c[0], c[1], rec.Code, rec.Body.String())
		}
	}
}
