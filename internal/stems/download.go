package stems

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/jobs"
)

// RegisterDownload mounts GET/HEAD /api/v1/jobs/{id}/download, which serves
// package.zip as an attachment once the job is completed (Range supported).
func (h *Handlers) RegisterDownload(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs/{id}/download", h.download)
	mux.HandleFunc("HEAD /api/v1/jobs/{id}/download", h.download)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

// PackageFilename derives the attachment name from the project name.
func PackageFilename(projectName string) string {
	n := strings.TrimSpace(unsafeName.ReplaceAllString(projectName, "_"))
	n = strings.Trim(n, "._ ")
	if n == "" {
		n = "rehearsekit"
	}
	if len(n) > 80 {
		n = n[:80]
	}
	return n + "-stems.zip"
}

func (h *Handlers) download(w http.ResponseWriter, r *http.Request) {
	j, ok := h.jobs.LoadReadable(w, r)
	if !ok {
		return
	}
	if j.Status != jobs.StatusCompleted {
		respond.Failf(w, http.StatusConflict, "not_ready", "the package is available once the job has completed")
		return
	}
	dir, err := h.layout.JobDir(j.ID)
	if err != nil {
		respond.Fail(w, respond.ErrNotFound)
		return
	}
	f, err := os.Open(filepath.Join(dir, "package.zip"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			respond.Failf(w, http.StatusNotFound, "not_found", "package not found")
			return
		}
		respond.Fail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		respond.Fail(w, err)
		return
	}
	name := PackageFilename(j.ProjectName)
	hdr := w.Header()
	hdr.Set("Content-Type", "application/zip")
	hdr.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	hdr.Set("Cache-Control", "private, no-transform")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
