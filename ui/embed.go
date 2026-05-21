// Package ui exposes the React SPA as an embedded filesystem so the Go
// binary serves the admin and catalog UIs without a separate static-host.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded UI filesystem rooted at "dist".
// dist/ is populated by `npm run build` in ui/src/ (the Dockerfile does this).
func FS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
