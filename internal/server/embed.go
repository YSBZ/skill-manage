package server

import (
	"embed"
	"io/fs"
)

// distFS embeds the hand-written single-page UI. The `all:` prefix is
// mandatory so dot/underscore-prefixed asset files are not silently dropped.
//
//go:embed all:dist
var distFS embed.FS

// UIFS returns the embedded UI rooted at the dist directory (so "/" maps to
// dist/index.html).
func UIFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
