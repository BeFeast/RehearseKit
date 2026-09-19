package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the embedded SPA: real files as-is (hashed assets get a
// long cache), everything else falls back to index.html so client-side
// routes work. Only the job page (/jobs/{id}) carries the cross-origin
// isolation headers the player needs (COOP same-origin + COEP credentialless):
// COOP same-origin also blocks the Google sign-in popup, so the list, the
// landing page and everything else stay un-isolated and sign in there.
func spaHandler(dist fs.FS) http.Handler {
	fileServer := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(p, "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		if isJobPage(p) {
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Embedder-Policy", "credentialless")
		}
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

// isJobPage reports whether a cleaned path is a single job page, /jobs/{id}:
// not the list (/jobs), not deeper paths.
func isJobPage(p string) bool {
	id, ok := strings.CutPrefix(p, "/jobs/")
	return ok && id != "" && !strings.Contains(id, "/")
}
