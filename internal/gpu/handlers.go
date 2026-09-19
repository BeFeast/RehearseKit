package gpu

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavcheck"
	"github.com/BeFeast/RehearseKit/internal/signed"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// RunnerIDHeader lets a runner identify itself; the JSON body may also
// carry runner_id. Both empty → "anonymous".
const RunnerIDHeader = "X-Runner-ID"

// Options configures the handlers.
type Options struct {
	// RunnerToken is the shared bearer token; empty disables the API (503).
	RunnerToken string
	// PublicURL is the base for absolute signed URLs; empty derives it from the request.
	PublicURL string
	// SignedURLTTL is the lifetime of the source and upload URLs.
	SignedURLTTL time.Duration
}

// Handlers serves /api/v1/gpu/*.
type Handlers struct {
	store  *Store
	signer *signed.Signer
	layout storage.Layout
	opts   Options
}

// NewHandlers builds the handlers.
func NewHandlers(store *Store, signer *signed.Signer, layout storage.Layout, opts Options) *Handlers {
	if opts.SignedURLTTL <= 0 {
		opts.SignedURLTTL = 2 * time.Hour
	}
	return &Handlers{store: store, signer: signer, layout: layout, opts: opts}
}

// Register mounts the routes behind the runner-token check.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/gpu/lease", h.auth(h.lease))
	mux.Handle("POST /api/v1/gpu/lease/{id}/heartbeat", h.auth(h.heartbeat))
	mux.Handle("POST /api/v1/gpu/lease/{id}/complete", h.auth(h.complete))
	mux.Handle("POST /api/v1/gpu/lease/{id}/fail", h.auth(h.fail))
	mux.Handle("GET /api/v1/gpu/queue", h.auth(h.queue))
}

func (h *Handlers) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.opts.RunnerToken == "" {
			respond.Failf(w, http.StatusServiceUnavailable, "gpu_disabled", "the GPU runner API is not configured (RK_RUNNER_TOKEN)")
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(tok)), []byte(h.opts.RunnerToken)) != 1 {
			respond.Failf(w, http.StatusUnauthorized, "runner_unauthorized", "invalid runner token")
			return
		}
		next(w, r)
	})
}

func runnerID(r *http.Request, bodyID string) string {
	if v := strings.TrimSpace(bodyID); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get(RunnerIDHeader)); v != "" {
		return v
	}
	return "anonymous"
}

func (h *Handlers) baseURL(r *http.Request) string {
	if h.opts.PublicURL != "" {
		return h.opts.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = v
	}
	return scheme + "://" + r.Host
}

// LeaseResponse is what a runner gets from POST /gpu/lease.
type LeaseResponse struct {
	LeaseID          string            `json:"lease_id"`
	JobID            string            `json:"job_id"`
	Model            string            `json:"model"`
	Stems            []string          `json:"stems"`
	SourceURL        string            `json:"source_url"`
	UploadURLs       map[string]string `json:"upload_urls"`
	ExpiresAt        time.Time         `json:"expires_at"`
	HeartbeatSeconds int               `json:"heartbeat_seconds"`
}

func (h *Handlers) lease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunnerID string `json:"runner_id"`
	}
	if r.ContentLength != 0 {
		if err := respond.DecodeJSON(r, &body); err != nil {
			respond.Fail(w, err)
			return
		}
	}
	rid := runnerID(r, body.RunnerID)
	l, j, err := h.store.Claim(r.Context(), rid)
	if errors.Is(err, ErrNoJobs) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	model, stems := jobs.ModelFor(j.Quality)
	exp := time.Now().Add(h.opts.SignedURLTTL)
	base := h.baseURL(r)
	src, err := h.signer.Sign(http.MethodGet, signed.SourcePath(j.ID), exp)
	if err != nil {
		respond.Fail(w, err)
		return
	}
	resp := LeaseResponse{
		LeaseID: l.ID, JobID: j.ID, Model: model, Stems: stems,
		SourceURL: base + src, UploadURLs: map[string]string{}, ExpiresAt: l.ExpiresAt,
		HeartbeatSeconds: heartbeatSeconds(h.store.TTL),
	}
	for _, name := range stems {
		u, err := h.signer.Sign(http.MethodPut, signed.StemPath(j.ID, name), exp)
		if err != nil {
			respond.Fail(w, err)
			return
		}
		resp.UploadURLs[name] = base + u
	}
	slog.Info("gpu lease", "lease", l.ID, "job", j.ID, "runner", rid, "model", model)
	respond.JSON(w, http.StatusOK, resp)
}

// queue answers GET /gpu/queue for autoscalers: {waiting, active_leases,
// oldest_waiting_at}. Runner token required, like the lease calls.
func (h *Handlers) queue(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.QueueStats(r.Context())
	if err != nil {
		respond.Fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond.JSON(w, http.StatusOK, st)
}

// heartbeatSeconds is the interval runners are told to heartbeat at: a
// quarter of the TTL for liveness, but never more than 15 s so progress
// reaches the UI at a useful rate, and never less than 5 s.
func heartbeatSeconds(ttl time.Duration) int {
	s := int(ttl.Seconds() / 4)
	if s > 15 {
		s = 15
	}
	if s < 5 {
		s = 5
	}
	return s
}

func (h *Handlers) leaseErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		respond.Failf(w, http.StatusNotFound, "lease_not_found", "unknown lease")
	case errors.Is(err, ErrWrongOwner):
		respond.Failf(w, http.StatusForbidden, "lease_owner", "this lease belongs to another runner")
	case errors.Is(err, ErrNotActive):
		respond.Failf(w, http.StatusGone, "lease_closed", "this lease is no longer active (expired or finished)")
	case errors.Is(err, ErrJobGone):
		respond.Failf(w, http.StatusConflict, "job_gone", "the job is no longer waiting for separation (cancelled or finished); stop working on it")
	case errors.Is(err, ErrBadStems):
		respond.Failf(w, http.StatusBadRequest, "bad_stems", err.Error())
	default:
		respond.Fail(w, err)
	}
}

func (h *Handlers) heartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunnerID string  `json:"runner_id"`
		Progress float64 `json:"progress"`
	}
	if err := respond.DecodeJSON(r, &body); err != nil {
		respond.Fail(w, err)
		return
	}
	if body.Progress < 0 || body.Progress > 1 {
		respond.Failf(w, http.StatusBadRequest, "invalid_progress", "progress must be between 0 and 1")
		return
	}
	l, err := h.store.Heartbeat(r.Context(), r.PathValue("id"), runnerID(r, body.RunnerID), body.Progress)
	if err != nil {
		h.leaseErr(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"lease_id": l.ID, "expires_at": l.ExpiresAt})
}

func (h *Handlers) complete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunnerID string       `json:"runner_id"`
		Stems    []StemReport `json:"stems"`
	}
	if err := respond.DecodeJSON(r, &body); err != nil {
		respond.Fail(w, err)
		return
	}
	err := h.store.Complete(r.Context(), r.PathValue("id"), runnerID(r, body.RunnerID), body.Stems, h.verifyStem)
	if err != nil {
		h.leaseErr(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"lease_id": r.PathValue("id"), "status": jobs.StatusFinalizing})
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunnerID string `json:"runner_id"`
		Error    string `json:"error"`
	}
	if err := respond.DecodeJSON(r, &body); err != nil {
		respond.Fail(w, err)
		return
	}
	if len(body.Error) > 2000 {
		body.Error = body.Error[:2000]
	}
	failed, err := h.store.Fail(r.Context(), r.PathValue("id"), runnerID(r, body.RunnerID), strings.TrimSpace(body.Error))
	if err != nil {
		h.leaseErr(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"lease_id": r.PathValue("id"), "job_failed": failed})
}

// verifyStem checks the uploaded file: size and checksum as reported, and
// a 24-bit/48 kHz stereo WAV header.
func (h *Handlers) verifyStem(_ context.Context, jobID string, st StemReport) error {
	path, err := h.layout.StemPath(jobID, st.Name)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("not uploaded: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != st.Bytes {
		return fmt.Errorf("size %d on disk, %d reported", info.Size(), st.Bytes)
	}
	if st.SHA256 != "" {
		sum := sha256.New()
		if _, err := io.Copy(sum, f); err != nil {
			return err
		}
		if got := hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(got, st.SHA256) {
			return fmt.Errorf("sha256 mismatch")
		}
	}
	if _, err := wavcheck.Check(path); err != nil {
		return err
	}
	return nil
}
