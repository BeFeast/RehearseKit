package jobs

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// Options tunes the handlers.
type Options struct {
	MaxUploadBytes int64
	AnonRetention  time.Duration
	UserRetention  time.Duration
	// TranscribeAllowed reports whether a signed-in email may set
	// transcribe=1; nil disables the option for everyone.
	TranscribeAllowed func(email string) bool
}

// Handlers serves /api/v1/jobs*.
type Handlers struct {
	store  *Store
	layout storage.Layout
	broker *Broker
	opts   Options
}

// NewHandlers builds the job handlers.
func NewHandlers(store *Store, layout storage.Layout, broker *Broker, opts Options) *Handlers {
	return &Handlers{store: store, layout: layout, broker: broker, opts: opts}
}

// Register mounts the job routes.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/jobs", h.create)
	mux.Handle("GET /api/v1/jobs", auth.RequireSession(http.HandlerFunc(h.list)))
	mux.HandleFunc("GET /api/v1/jobs/{id}", h.get)
	mux.HandleFunc("GET /api/v1/jobs/{id}/events", h.events)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", h.cancel)
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", h.del)
	mux.Handle("POST /api/v1/jobs/{id}/claim", auth.RequireSession(http.HandlerFunc(h.claim)))
}

// Load fetches a job by path id, or writes the error and returns false.
func (h *Handlers) Load(w http.ResponseWriter, r *http.Request) (*Job, bool) {
	id := r.PathValue("id")
	if !storage.ValidJobID(id) {
		respond.Fail(w, respond.ErrNotFound)
		return nil, false
	}
	j, err := h.store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		respond.Fail(w, respond.ErrNotFound)
		return nil, false
	}
	if err != nil {
		respond.Fail(w, err)
		return nil, false
	}
	return j, true
}

// LoadReadable is Load plus AuthorizeRead.
func (h *Handlers) LoadReadable(w http.ResponseWriter, r *http.Request) (*Job, bool) {
	j, ok := h.Load(w, r)
	if !ok {
		return nil, false
	}
	if err := AuthorizeRead(r, j); err != nil {
		respond.Fail(w, err)
		return nil, false
	}
	return j, true
}

var allowedUploadExt = map[string]bool{
	"mp3": true, "wav": true, "flac": true, "m4a": true, "aac": true, "ogg": true, "opus": true,
	"aiff": true, "aif": true, "wma": true, "mp4": true, "webm": true, "mkv": true, "mov": true,
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	// Allow the upload plus a little slack for the multipart framing.
	r.Body = http.MaxBytesReader(w, r.Body, h.opts.MaxUploadBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_multipart", "expected multipart/form-data: "+err.Error())
		return
	}
	id, err := newUUID()
	if err != nil {
		respond.Fail(w, err)
		return
	}
	var (
		fields   = map[string]string{}
		filename string
		gotFile  bool
		bytesIn  int64
	)
	cleanup := func() { _ = h.layout.RemoveJob(id) }
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			respond.Fail(w, multipartErr(err))
			return
		}
		name := part.FormName()
		if part.FileName() == "" {
			if name == "" {
				continue
			}
			v, err := io.ReadAll(io.LimitReader(part, 4096))
			if err != nil {
				cleanup()
				respond.Fail(w, multipartErr(err))
				return
			}
			fields[name] = strings.TrimSpace(string(v))
			continue
		}
		if name != "file" || gotFile {
			continue // ignore stray file parts
		}
		filename = filepath.Base(part.FileName())
		ext, ok := storage.SourceExt(filename)
		if !ok || !allowedUploadExt[ext] {
			cleanup()
			respond.Failf(w, http.StatusBadRequest, "unsupported_media", "unsupported file type; upload mp3, wav, flac, m4a, aac, ogg, opus or aiff")
			return
		}
		n, err := h.layout.SaveSource(id, ext, part)
		if err != nil {
			cleanup()
			respond.Fail(w, multipartErr(err))
			return
		}
		gotFile, bytesIn = true, n
	}

	inputURL := fields["input_url"]
	if gotFile && inputURL != "" {
		cleanup()
		respond.Failf(w, http.StatusBadRequest, "ambiguous_input", "send either a file or input_url, not both")
		return
	}
	if !gotFile && inputURL == "" {
		cleanup()
		respond.Failf(w, http.StatusBadRequest, "missing_input", "a file or input_url is required")
		return
	}
	if gotFile && bytesIn == 0 {
		cleanup()
		respond.Failf(w, http.StatusBadRequest, "empty_file", "the uploaded file is empty")
		return
	}
	quality := fields["quality"]
	if quality == "" {
		quality = QualityFast
	}
	if !ValidQuality(quality) {
		cleanup()
		respond.Failf(w, http.StatusBadRequest, "invalid_quality", "quality must be one of fast, high, high6")
		return
	}
	p := CreateParams{Quality: quality}
	if t := fields["transcribe"]; t == "1" || strings.EqualFold(t, "true") {
		u := auth.UserFrom(r.Context())
		if u == nil || h.opts.TranscribeAllowed == nil || !h.opts.TranscribeAllowed(u.Email) {
			cleanup()
			respond.Failf(w, http.StatusForbidden, "transcribe_not_allowed", "transcription is not enabled for this account")
			return
		}
		if quality != QualityHigh6 {
			cleanup()
			respond.Failf(w, http.StatusBadRequest, "transcribe_requires_high6", "transcription needs the high6 quality (guitar and piano stems)")
			return
		}
		p.Transcribe = true
	}
	if gotFile {
		p.InputType = InputUpload
		p.SourceFilename = &filename
	} else {
		if !validYouTubeURL(inputURL) {
			cleanup()
			respond.Failf(w, http.StatusBadRequest, "invalid_url", "input_url must be a YouTube link")
			return
		}
		p.InputType = InputYouTube
		p.InputURL = &inputURL
	}
	p.ProjectName = fields["project_name"]
	if p.ProjectName == "" {
		if gotFile {
			p.ProjectName = strings.TrimSuffix(filename, filepath.Ext(filename))
		} else {
			p.ProjectName = "YouTube import"
		}
	}
	p.ProjectName = clampText(p.ProjectName, 200)

	var claimToken string
	if u := auth.UserFrom(r.Context()); u != nil {
		p.OwnerID = &u.ID
		p.ExpiresAt = time.Now().Add(h.opts.UserRetention)
	} else {
		tok, hash, err := NewClaimToken()
		if err != nil {
			cleanup()
			respond.Fail(w, err)
			return
		}
		claimToken, p.ClaimTokenHash = tok, hash
		p.ExpiresAt = time.Now().Add(h.opts.AnonRetention)
	}
	j, err := h.store.Create(r.Context(), id, p)
	if err != nil {
		cleanup()
		respond.Fail(w, err)
		return
	}
	j.ClaimToken = claimToken
	respond.JSON(w, http.StatusCreated, j)
}

func multipartErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return respond.E(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("upload exceeds the %d byte limit", mbe.Limit-1<<20))
	}
	return respond.E(http.StatusBadRequest, "invalid_multipart", "could not read the upload: "+err.Error())
}

func validYouTubeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be":
		return true
	}
	return false
}

type jobsPage struct {
	Items    []*Job `json:"items"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int    `json:"total"`
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	q := r.URL.Query()
	f := ListFilter{Status: q.Get("status"), Query: strings.TrimSpace(q.Get("q"))}
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.PageSize, _ = strconv.Atoi(q.Get("page_size"))
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	items, total, err := h.store.List(r.Context(), u.ID, f)
	if errors.Is(err, ErrInvalidStatus) {
		respond.Failf(w, http.StatusBadRequest, "invalid_status", "status must be all, active, completed or failed")
		return
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, jobsPage{Items: items, Page: f.Page, PageSize: f.PageSize, Total: total})
}

func (h *Handlers) get(w http.ResponseWriter, r *http.Request) {
	j, ok := h.LoadReadable(w, r)
	if !ok {
		return
	}
	respond.JSON(w, http.StatusOK, j)
}

func (h *Handlers) cancel(w http.ResponseWriter, r *http.Request) {
	j, ok := h.Load(w, r)
	if !ok {
		return
	}
	if err := AuthorizeWrite(r, j); err != nil {
		respond.Fail(w, err)
		return
	}
	err := Cancel(r.Context(), h.store.pool, j.ID)
	if errors.Is(err, ErrTerminal) {
		respond.Failf(w, http.StatusConflict, "already_finished", "the job has already finished")
		return
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	j, err = h.store.Get(r.Context(), j.ID)
	if err != nil {
		respond.Fail(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, j)
}

func (h *Handlers) del(w http.ResponseWriter, r *http.Request) {
	j, ok := h.Load(w, r)
	if !ok {
		return
	}
	if err := AuthorizeWrite(r, j); err != nil {
		respond.Fail(w, err)
		return
	}
	if err := h.store.Delete(r.Context(), j.ID); err != nil && !errors.Is(err, ErrNotFound) {
		respond.Fail(w, err)
		return
	}
	if err := h.layout.RemoveJob(j.ID); err != nil {
		slog.Warn("delete job files", "job", j.ID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) claim(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	id := r.PathValue("id")
	if !storage.ValidJobID(id) {
		respond.Fail(w, respond.ErrNotFound)
		return
	}
	var body struct {
		ClaimToken string `json:"claim_token"`
	}
	if err := respond.DecodeJSON(r, &body); err != nil {
		respond.Fail(w, err)
		return
	}
	j, err := h.store.ClaimForUser(r.Context(), id, body.ClaimToken, u.ID, h.opts.UserRetention)
	switch {
	case errors.Is(err, ErrNotFound):
		respond.Fail(w, respond.ErrNotFound)
	case errors.Is(err, ErrAlreadyOwned):
		respond.Failf(w, http.StatusConflict, "already_claimed", "this job already belongs to an account")
	case errors.Is(err, ErrBadClaimToken):
		respond.Failf(w, http.StatusForbidden, "invalid_claim_token", "the claim token does not match this job")
	case err != nil:
		respond.Fail(w, err)
	default:
		j, err = h.store.Get(r.Context(), j.ID)
		if err != nil {
			respond.Fail(w, err)
			return
		}
		respond.JSON(w, http.StatusOK, j)
	}
}

// clampText makes s valid UTF-8 and cuts it to at most maxBytes without
// splitting a rune, so it can be stored in a Postgres text column.
func clampText(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// newUUID returns a random (version 4) UUID in canonical lowercase form.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
