// Package web embeds the built frontend and serves it as a single-page
// application. The React source lives in /web; "make web" builds it into
// dist/, which is embedded here. See ADR-0008.
package web

import (
	"embed"
	"io/fs"
)

// dist holds the Vite build output. Only dist/.gitkeep is tracked, so a Go
// build without a frontend build still compiles; the server then reports the
// UI as not built (see Handler).
//
//go:embed all:dist
var dist embed.FS

// Assets returns the embedded frontend rooted at its index.html.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: embedded dist directory is missing: " + err.Error()) // cannot happen: go:embed checked it
	}
	return sub
}
