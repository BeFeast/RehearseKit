// Package stems serves stem WAVs and their peaks files with full HTTP Range
// support. It is the descendant of the stemd prototype: http.ServeContent
// does Range/206, If-Range, HEAD and 416; we add the headers the streaming
// player needs (inline disposition, CORP cross-origin, private caching).
package stems

import (
	"errors"
	"io/fs"
	"net/http"
	"os"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// Handlers serves /api/v1/jobs/{id}/stems/{name}[/peaks].
type Handlers struct {
	jobs   *jobs.Handlers
	layout storage.Layout
}

// NewHandlers builds the stem handlers on top of the job loader.
func NewHandlers(jh *jobs.Handlers, layout storage.Layout) *Handlers {
	return &Handlers{jobs: jh, layout: layout}
}

// Register mounts the routes.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs/{id}/stems/{name}", h.stem)
	mux.HandleFunc("HEAD /api/v1/jobs/{id}/stems/{name}", h.stem)
	mux.HandleFunc("GET /api/v1/jobs/{id}/stems/{name}/peaks", h.peaks)
	mux.HandleFunc("HEAD /api/v1/jobs/{id}/stems/{name}/peaks", h.peaks)
}

func (h *Handlers) stem(w http.ResponseWriter, r *http.Request) {
	j, ok := h.jobs.LoadReadable(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	path, err := h.layout.StemPath(j.ID, name)
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_stem", "invalid stem name")
		return
	}
	serveFile(w, r, path, name+".wav", "audio/wav")
}

func (h *Handlers) peaks(w http.ResponseWriter, r *http.Request) {
	j, ok := h.jobs.LoadReadable(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	path, err := h.layout.PeaksPath(j.ID, name)
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_stem", "invalid stem name")
		return
	}
	serveFile(w, r, path, name+".pk", "application/octet-stream")
}

// serveFile streams path with Range support and the player headers.
func serveFile(w http.ResponseWriter, r *http.Request, path, downloadName, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			respond.Failf(w, http.StatusNotFound, "not_found", "stem not found")
			return
		}
		respond.Fail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		respond.Failf(w, http.StatusNotFound, "not_found", "stem not found")
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", `inline; filename="`+downloadName+`"`)
	h.Set("Cache-Control", "private, no-transform, max-age=3600")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	http.ServeContent(w, r, downloadName, info.ModTime(), f)
}
