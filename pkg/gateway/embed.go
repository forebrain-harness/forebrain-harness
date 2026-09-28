// This file embeds the built Vue frontend (frontend/ → pkg/gateway/dist) so the
// forebrain binary can serve the SPA with no external files. The dist tree
// is produced by `pnpm build` (vite outDir points here) and is gitignored apart
// from a .gitkeep placeholder, which keeps `go build` working before any
// frontend build has run.

package gateway

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded dist subtree. ok is false when the frontend has not
// been built (only the .gitkeep placeholder is present), in which case callers
// should fall back to serving nothing.
func FS() (fs.FS, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, false
	}
	return sub, true
}
