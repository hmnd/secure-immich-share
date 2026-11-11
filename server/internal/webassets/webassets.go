// Package webassets embeds the web build (web/build, copied to dist/ by
// `just web-embed`): the islands' scripts and styles and the Vite manifest
// that names them. Without a build, pages render unstyled.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the embedded build rooted at dist/.
func FS() fs.FS {
	sub, _ := fs.Sub(embedded, "dist") // fails only for an invalid path
	return sub
}
