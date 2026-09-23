package signed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// MaxStemBytes caps one stem upload (2 GiB).
const MaxStemBytes = 2 << 30

// MaxArtifactBytes caps one JSON artefact upload (analysis.json, notes).
const MaxArtifactBytes = 32 << 20

// Paths of the signed endpoints, relative to the API root.
const (
	SourcePathPrefix = "/api/v1/signed/jobs/"
)

// SourcePath is the request path for a job's converted source.
func SourcePath(jobID string) string { return SourcePathPrefix + jobID + "/source" }

// StemPath is the request path for uploading one stem.
func StemPath(jobID, name string) string { return SourcePathPrefix + jobID + "/stems/" + name }

// AnalysisPath is the request path for uploading analysis.json.
func AnalysisPath(jobID string) string { return SourcePathPrefix + jobID + "/analysis" }

// NotesPath is the request path for uploading notes/<stem>.json.
func NotesPath(jobID, stem string) string { return SourcePathPrefix + jobID + "/notes/" + stem }

// Handlers serves the signed endpoints.
type Handlers struct {
	signer *Signer
	layout storage.Layout
}

// NewHandlers builds the handlers.
func NewHandlers(signer *Signer, layout storage.Layout) *Handlers {
	return &Handlers{signer: signer, layout: layout}
}

// Register mounts the routes.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/signed/jobs/{id}/source", h.source)
	mux.HandleFunc("HEAD /api/v1/signed/jobs/{id}/source", h.source)
	mux.HandleFunc("PUT /api/v1/signed/jobs/{id}/stems/{name}", h.putStem)
	mux.HandleFunc("PUT /api/v1/signed/jobs/{id}/analysis", h.putAnalysis)
	mux.HandleFunc("PUT /api/v1/signed/jobs/{id}/notes/{name}", h.putNotes)
}

func (h *Handlers) putAnalysis(w http.ResponseWriter, r *http.Request) {
	if !h.verify(w, r) {
		return
	}
	path, err := h.layout.AnalysisPath(r.PathValue("id"))
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_job", "invalid job id")
		return
	}
	h.putJSON(w, r, "analysis", path)
}

func (h *Handlers) putNotes(w http.ResponseWriter, r *http.Request) {
	if !h.verify(w, r) {
		return
	}
	path, err := h.layout.NotesPath(r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_stem", "invalid job id or stem name")
		return
	}
	h.putJSON(w, r, "notes/"+r.PathValue("name"), path)
}

// putJSON stores a small JSON artefact (transcription output) at path:
// Content-Length required and capped at MaxArtifactBytes, body must parse
// as JSON, written via a temp file. Answers {"name","bytes","sha256"}.
func (h *Handlers) putJSON(w http.ResponseWriter, r *http.Request, name, path string) {
	if r.ContentLength < 0 {
		respond.Failf(w, http.StatusLengthRequired, "length_required", "Content-Length is required")
		return
	}
	if r.ContentLength == 0 {
		respond.Failf(w, http.StatusBadRequest, "empty_body", "artifact upload is empty")
		return
	}
	if r.ContentLength > MaxArtifactBytes {
		respond.Failf(w, http.StatusRequestEntityTooLarge, "too_large", "artifact exceeds the 32 MiB limit")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxArtifactBytes))
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "upload_failed", "could not read the artifact body: "+err.Error())
		return
	}
	if int64(len(body)) != r.ContentLength {
		respond.Failf(w, http.StatusBadRequest, "short_body", "body shorter than Content-Length")
		return
	}
	if !json.Valid(body) {
		respond.Failf(w, http.StatusBadRequest, "invalid_json", "artifact is not valid JSON")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		respond.Fail(w, err)
		return
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		respond.Fail(w, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		respond.Fail(w, err)
		return
	}
	sum := sha256.Sum256(body)
	respond.JSON(w, http.StatusCreated, map[string]any{
		"name":   name,
		"bytes":  len(body),
		"sha256": hex.EncodeToString(sum[:]),
	})
}

func (h *Handlers) verify(w http.ResponseWriter, r *http.Request) bool {
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	err := h.signer.Verify(method, r.URL.Path, r.URL.Query())
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrNoKey):
		respond.Failf(w, http.StatusServiceUnavailable, "signing_disabled", "signed URLs are not configured on this server")
	case errors.Is(err, ErrExpired):
		respond.Failf(w, http.StatusForbidden, "url_expired", "this signed URL has expired")
	default:
		respond.Failf(w, http.StatusForbidden, "bad_signature", "invalid signed URL")
	}
	return false
}

func (h *Handlers) source(w http.ResponseWriter, r *http.Request) {
	if !h.verify(w, r) {
		return
	}
	dir, err := h.layout.JobDir(r.PathValue("id"))
	if err != nil {
		respond.Fail(w, respond.ErrNotFound)
		return
	}
	path := filepath.Join(dir, "source.wav")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			respond.Failf(w, http.StatusNotFound, "not_found", "source not found")
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
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "source.wav", info.ModTime(), f)
}

// putStem streams the body into jobs/<id>/stems/<name>.wav via a temp file
// and answers {"bytes": n, "sha256": hex}. Content-Length is required and
// capped at MaxStemBytes; a short body is an error and leaves no file.
func (h *Handlers) putStem(w http.ResponseWriter, r *http.Request) {
	if !h.verify(w, r) {
		return
	}
	path, err := h.layout.StemPath(r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_stem", "invalid job id or stem name")
		return
	}
	if r.ContentLength < 0 {
		respond.Failf(w, http.StatusLengthRequired, "length_required", "Content-Length is required")
		return
	}
	if r.ContentLength == 0 {
		respond.Failf(w, http.StatusBadRequest, "empty_body", "stem upload is empty")
		return
	}
	if r.ContentLength > MaxStemBytes {
		respond.Failf(w, http.StatusRequestEntityTooLarge, "too_large", "stem exceeds the 2 GiB limit")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		respond.Fail(w, err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
	if err != nil {
		respond.Fail(w, err)
		return
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), http.MaxBytesReader(w, r.Body, MaxStemBytes))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		respond.Failf(w, http.StatusBadRequest, "upload_failed", "could not read the stem body: "+err.Error())
		return
	}
	if n != r.ContentLength {
		cleanup()
		respond.Failf(w, http.StatusBadRequest, "short_body", "body shorter than Content-Length")
		return
	}
	_ = os.Chmod(tmpName, 0o644) // CreateTemp defaults to 0600
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		respond.Fail(w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{
		"name":   r.PathValue("name"),
		"bytes":  n,
		"sha256": hex.EncodeToString(sum.Sum(nil)),
	})
}
