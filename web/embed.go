// Package web embeds the built SPA (web/dist) into the rk binary.
//
// dist/ is the Vite output directory. Only two files in it are tracked:
// .gitkeep and placeholder.html. `bun run build` (see web/package.json)
// writes index.html and assets/ next to them; those are gitignored. When the
// SPA has not been built, Dist serves placeholder.html as index.html so
// `go build ./...` and `rk serve` keep working from a clean checkout.
package web

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Dist returns the SPA files rooted at dist/. Without a Vite build the
// placeholder page stands in for index.html.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat(sub, "index.html"); err == nil {
		return sub
	}
	return placeholderFS{sub}
}

// Built reports whether a real SPA build is embedded.
func Built() bool {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}

// placeholderFS maps index.html onto placeholder.html.
type placeholderFS struct{ fs.FS }

func (p placeholderFS) Open(name string) (fs.File, error) {
	if name == "index.html" {
		return p.FS.Open("placeholder.html")
	}
	f, err := p.FS.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return f, err
}
