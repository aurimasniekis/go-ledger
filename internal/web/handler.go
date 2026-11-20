package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/httpapi"
)

const (
	// Files under assets/ are content-hashed by the build, so they never change.
	cacheImmutable = "public, max-age=31536000, immutable"
	// Everything else is revalidated on every use; the ETag makes that cheap.
	cacheRevalidate = "no-cache"

	// The UI is same-origin only. Styles allow 'unsafe-inline' because React and
	// Radix set style attributes; scripts allow only the bundle.
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
)

// hasDotSegment reports whether any segment of the cleaned path p starts with a dot.
func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

type file struct {
	data []byte
	etag string
}

// Handler serves the frontend in assets (normally Assets()):
//
//   - an existing file is served as is. Under assets/ it is immutable; any
//     other file, index.html included, is no-cache with a strong ETag.
//   - a path that names no file but looks like a page route (no file extension,
//     not under assets/) gets index.html with 200, so deep links and reloads
//     reach the client-side router.
//   - a missing asset (assets/... or a path with an extension) is a 404 in the
//     JSON error envelope, never index.html.
//   - /api and everything under it is a 404 in the envelope too: the UI never
//     answers for the API.
//   - dot-named files and directories (such as .gitkeep) are never served, and a
//     request that names one gets a 404 envelope.
//   - only GET and HEAD are allowed.
//
// If assets has no index.html (the frontend was not built) page requests get a
// 503 envelope that says so.
func Handler(assets fs.FS) http.Handler {
	files := map[string]file{}
	_ = fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is simply not served
		}
		// Dot-named entries (.gitkeep, stray OS or editor files) are never served.
		if p != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, rerr := fs.ReadFile(assets, p)
		if rerr != nil {
			return nil //nolint:nilerr // same
		}
		sum := sha256.Sum256(data)
		files[p] = file{data: data, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusMethodNotAllowed, Code: httpapi.CodeMethod, Message: "method not allowed"})
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "api" || strings.HasPrefix(p, "api/") {
			httpapi.WriteError(w, r, httpapi.NotFound("no such resource"))
			return
		}
		// A request that names a dot-entry is a 404, never the shell.
		if hasDotSegment(p) {
			httpapi.WriteError(w, r, httpapi.NotFound("no such resource"))
			return
		}
		name := p
		f, ok := files[name]
		if !ok || p == "" {
			if p == "assets" || strings.HasPrefix(p, "assets/") || path.Ext(p) != "" {
				httpapi.WriteError(w, r, httpapi.NotFound("no such resource"))
				return
			}
			name = "index.html"
			if f, ok = files[name]; !ok {
				httpapi.WriteError(w, r, httpapi.Unavailable("the web UI is not built into this binary", nil))
				return
			}
		}
		h := w.Header()
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", cacheImmutable)
		} else {
			h.Set("Cache-Control", cacheRevalidate)
			h.Set("ETag", f.etag)
		}
		h.Set("X-Content-Type-Options", "nosniff")
		if name == "index.html" {
			h.Set("Content-Security-Policy", contentSecurityPolicy)
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
		}
		// A zero modification time: embedded files have none, and validation
		// is by ETag.
		// The shell needs no ranges; ignoring them avoids multi-range parsing cost.
		r.Header.Del("Range")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
	})
}
