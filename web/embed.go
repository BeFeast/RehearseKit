// Package web embeds the built SPA (web/dist) into the rk binary.
//
// Until phase 4 lands the Vite build, dist/ holds a placeholder index.html so
// `rk serve` answers on / without a separate frontend.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Dist returns the SPA files rooted at dist/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
