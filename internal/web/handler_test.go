package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var testAssets = fstest.MapFS{
	"index.html":           {Data: []byte("<!doctype html><title>Ledger</title>")},
	"favicon.svg":          {Data: []byte("<svg/>")},
	"assets/index-abc.js":  {Data: []byte("console.log(1)")},
	"assets/index-abc.css": {Data: []byte("body{}")},
}

func get(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func envelopeCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q, want JSON envelope; body %q", ct, rec.Body.String())
	}
	var b struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Code
}

func TestShellAtRootAndDeepLinks(t *testing.T) {
	h := Handler(testAssets)
	for _, p := range []string{"/", "/index.html", "/commitments/42", "/a/b/c/", "/%2e%2e/etc"} {
		rec := get(h, "GET", p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Ledger</title>") {
			t.Fatalf("%s: got %d %q", p, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: Cache-Control %q, want no-cache", p, cc)
		}
		if rec.Header().Get("ETag") == "" || rec.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: missing ETag or CSP: %v", p, rec.Header())
		}
	}
}

func TestHashedAssetsAreImmutable(t *testing.T) {
	h := Handler(testAssets)
	rec := get(h, "GET", "/assets/index-abc.js")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type %q", ct)
	}
	if rec := get(h, "GET", "/assets/index-abc.css"); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css Content-Type %q", rec.Header().Get("Content-Type"))
	}
}

func TestOtherFilesRevalidate(t *testing.T) {
	h := Handler(testAssets)
	rec := get(h, "GET", "/favicon.svg")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
	// A matching If-None-Match yields 304 with no body.
	rec2 := get(h, "GET", "/favicon.svg", "If-None-Match", rec.Header().Get("ETag"))
	if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
		t.Fatalf("conditional: %d %q", rec2.Code, rec2.Body.String())
	}
}

func TestAPIPathsNeverGetTheShell(t *testing.T) {
	h := Handler(testAssets)
	for _, p := range []string{"/api", "/api/", "/api/v1/accounts", "/api/../api/x", "//api/x"} {
		rec := get(h, "GET", p)
		if rec.Code != 404 || envelopeCode(t, rec) != "not_found" {
			t.Fatalf("%s: got %d %q", p, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "<title>") {
			t.Fatalf("%s: served the shell", p)
		}
	}
	// Only the exact segment "api" is reserved.
	if rec := get(h, "GET", "/apiary"); rec.Code != 200 {
		t.Fatalf("/apiary: %d", rec.Code)
	}
}

func TestMissingAssetsAre404NotTheShell(t *testing.T) {
	h := Handler(testAssets)
	for _, p := range []string{"/assets/missing.js", "/assets/", "/robots.txt", "/x/y.map"} {
		rec := get(h, "GET", p)
		if rec.Code != 404 || envelopeCode(t, rec) != "not_found" {
			t.Fatalf("%s: got %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestMethods(t *testing.T) {
	h := Handler(testAssets)
	if rec := get(h, "HEAD", "/"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Body.String())
	}
	rec := get(h, "POST", "/")
	if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" || envelopeCode(t, rec) != "method_not_allowed" {
		t.Fatalf("POST: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	// A write to /api/ is also refused here, in the envelope.
	if rec := get(h, "DELETE", "/api/x"); rec.Code != 405 {
		t.Fatalf("DELETE /api/x: %d", rec.Code)
	}
}

func TestUnbuiltFrontendIsReported(t *testing.T) {
	h := Handler(fstest.MapFS{".gitkeep": {}})
	rec := get(h, "GET", "/")
	if rec.Code != http.StatusServiceUnavailable || envelopeCode(t, rec) != "dependency_unavailable" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not built") {
		t.Fatalf("message does not say the UI is not built: %q", rec.Body.String())
	}
	// The API stays a 404 envelope regardless.
	if rec := get(h, "GET", "/api/x"); rec.Code != 404 {
		t.Fatalf("api: %d", rec.Code)
	}
}

func TestEmbeddedAssetsAreReadable(t *testing.T) {
	// Without a frontend build the embedded tree holds only .gitkeep; with one it
	// holds index.html. Either way Assets must be a valid root.
	rec := get(Handler(Assets()), "GET", "/")
	if rec.Code != 200 && rec.Code != 503 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestDotEntriesAreNeverServed(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":        {Data: []byte("<title>shell</title>")},
		"assets/app.js":     {Data: []byte("console.log(1)")},
		".gitkeep":          {},
		".env":              {Data: []byte("LEDGER_KEK_VALUE=not-real")},
		".hidden/secret.js": {Data: []byte("x")},
		"assets/.keep":      {},
	}
	h := Handler(assets)
	for _, p := range []string{"/.gitkeep", "/.env", "/.hidden", "/.hidden/secret.js", "/assets/.keep", "/.well-known/x"} {
		rec := get(h, "GET", p)
		if rec.Code != 404 || envelopeCode(t, rec) != "not_found" {
			t.Fatalf("%s: got %d %q", p, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "<title>") || strings.Contains(rec.Body.String(), "not-real") {
			t.Fatalf("%s: leaked content", p)
		}
	}
	// Dot-entries do not affect the rest of the UI.
	if rec := get(h, "GET", "/"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("/: %d", rec.Code)
	}
	if rec := get(h, "GET", "/assets/app.js"); rec.Code != 200 {
		t.Fatalf("/assets/app.js: %d", rec.Code)
	}
}
