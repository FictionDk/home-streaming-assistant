// Package webassets embeds the built frontend. The dist directory is
// produced by `npm --prefix web run build` BEFORE `go build`; never
// commit a placeholder dist.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist returns the embedded dist subtree.
func Dist() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
