package drums

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/analysis"
	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// Handlers serves the transcription artefacts and the drum edit revision.
//
//	GET  /api/v1/jobs/{id}/analysis       analysis.json (job read access)
//	GET  /api/v1/jobs/{id}/notes/{stem}   notes/<stem>.json (job read access)
//	GET  /api/v1/jobs/{id}/drums/edits    edit revision + profile + grid (owner)
//	PUT  /api/v1/jobs/{id}/drums/edits    {base_rev, events} → 409 edit_conflict when stale (owner)
type Handlers struct {
	jobs   *jobs.Handlers
	layout storage.Layout
	store  *Store
	now    func() time.Time
}

// NewHandlers builds the handlers on top of the job loader.
func NewHandlers(jh *jobs.Handlers, layout storage.Layout) *Handlers {
	return &Handlers{jobs: jh, layout: layout, store: NewStore(layout), now: time.Now}
}

// Register mounts the routes.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs/{id}/analysis", h.analysis)
	mux.HandleFunc("GET /api/v1/jobs/{id}/notes/{stem}", h.notes)
	mux.HandleFunc("GET /api/v1/jobs/{id}/drums/edits", h.getEdits)
	mux.HandleFunc("PUT /api/v1/jobs/{id}/drums/edits", h.putEdits)
}

func (h *Handlers) analysis(w http.ResponseWriter, r *http.Request) {
	j, ok := h.jobs.LoadReadable(w, r)
	if !ok {
		return
	}
	path, err := h.layout.AnalysisPath(j.ID)
	if err != nil {
		respond.Fail(w, respond.ErrNotFound)
		return
	}
	serveJSON(w, r, path, "analysis.json", "analysis_not_found", "this job has no analysis")
}

func (h *Handlers) notes(w http.ResponseWriter, r *http.Request) {
	j, ok := h.jobs.LoadReadable(w, r)
	if !ok {
		return
	}
	stem := r.PathValue("stem")
	if !transcribable(stem) {
		respond.Failf(w, http.StatusBadRequest, "invalid_stem", "no notes are produced for this stem")
		return
	}
	path, err := h.layout.NotesPath(j.ID, stem)
	if err != nil {
		respond.Fail(w, respond.ErrNotFound)
		return
	}
	serveJSON(w, r, path, stem+".json", "notes_not_found", "this job has no notes for "+stem)
}

func transcribable(stem string) bool {
	for _, s := range jobs.TranscribeStems {
		if s == stem {
			return true
		}
	}
	return false
}

// serveJSON streams a JSON artefact with the same caching as the stems.
func serveJSON(w http.ResponseWriter, r *http.Request, path, name, code, msg string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			respond.Failf(w, http.StatusNotFound, code, msg)
			return
		}
		respond.Fail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		respond.Failf(w, http.StatusNotFound, code, msg)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "application/json; charset=utf-8")
	hdr.Set("Cache-Control", "private, no-transform, max-age=3600")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// EditsResponse is the body of GET .../drums/edits.
type EditsResponse struct {
	Doc     Doc     `json:"doc"`
	Profile Profile `json:"profile"`
	// Grid is the cleaned beat grid the export uses; nil when the job has
	// none (GridError says why), in which case the editor works in seconds.
	Grid      *grid.Export `json:"grid"`
	GridError string       `json:"grid_error,omitempty"`
	// Model describes notes/drums.json as it is now; StaleModel is true
	// when the saved revision was derived from a different file.
	Model      ModelRev `json:"model"`
	StaleModel bool     `json:"stale_model"`
	// Duration of the song in seconds (0 when unknown).
	Duration float64  `json:"duration"`
	Warnings []string `json:"warnings"`
}

// SaveResponse is the body of a successful PUT.
type SaveResponse struct {
	EditRev   int       `json:"edit_rev"`
	UpdatedAt time.Time `json:"updated_at"`
}

type putBody struct {
	BaseRev int     `json:"base_rev"`
	Events  []Event `json:"events"`
}

// source is what the editor needs from the job directory.
type source struct {
	job      *jobs.Job
	notes    analysis.Notes
	raw      []byte
	profile  Profile
	duration float64
	grid     *grid.Export
	gridErr  string
}

// load resolves the job, checks the owner and reads the model output.
func (h *Handlers) load(w http.ResponseWriter, r *http.Request) (*source, bool) {
	j, ok := h.jobs.Load(w, r)
	if !ok {
		return nil, false
	}
	if err := jobs.AuthorizeWrite(r, j); err != nil {
		respond.Fail(w, err)
		return nil, false
	}
	if j.Status != jobs.StatusCompleted {
		respond.Failf(w, http.StatusConflict, "not_ready", "the drum editor opens once the job has completed")
		return nil, false
	}
	if !j.Transcribe {
		respond.Failf(w, http.StatusNotFound, "drum_notes_not_found", "this job was not transcribed")
		return nil, false
	}
	ap, err := h.layout.AnalysisPath(j.ID)
	if err != nil {
		respond.Fail(w, respond.ErrNotFound)
		return nil, false
	}
	ab, err := os.ReadFile(ap)
	if errors.Is(err, fs.ErrNotExist) {
		respond.Failf(w, http.StatusNotFound, "drum_notes_not_found", "this job has no analysis")
		return nil, false
	}
	if err != nil {
		respond.Fail(w, err)
		return nil, false
	}
	res, err := analysis.Parse(ab)
	if err != nil {
		respond.Fail(w, err)
		return nil, false
	}
	if in := res.Instruments["drums"]; in.Status != analysis.StatusOK {
		msg := "drum transcription " + in.Status
		if in.Reason != "" {
			msg += ": " + in.Reason
		}
		respond.Failf(w, http.StatusNotFound, "drum_notes_not_found", msg)
		return nil, false
	}
	np, err := h.layout.NotesPath(j.ID, "drums")
	if err != nil {
		respond.Fail(w, respond.ErrNotFound)
		return nil, false
	}
	raw, err := os.ReadFile(np)
	if errors.Is(err, fs.ErrNotExist) {
		respond.Failf(w, http.StatusNotFound, "drum_notes_not_found", "this job has no drum notes")
		return nil, false
	}
	if err != nil {
		respond.Fail(w, err)
		return nil, false
	}
	notes, err := analysis.ParseNotes(raw)
	if err != nil {
		respond.Fail(w, err)
		return nil, false
	}
	profile, _ := ProfileByID(DefaultProfile)
	s := &source{job: j, notes: notes, raw: raw, profile: profile}
	if j.DurationSeconds != nil {
		s.duration = *j.DurationSeconds
	}
	switch {
	case res.Grid == nil && res.GridError != "":
		s.gridErr = res.GridError
	case res.Grid == nil:
		s.gridErr = "beat tracker produced no grid"
	default:
		m, err := grid.Build(res.Grid.Beats, res.Grid.Downbeats, s.duration)
		if err != nil {
			s.gridErr = err.Error()
		} else {
			e := m.Export()
			s.grid = &e
		}
	}
	return s, true
}

func (s *source) seed(now time.Time) (Doc, []string) {
	return Seed(s.notes, s.raw, s.profile, now)
}

func (h *Handlers) getEdits(w http.ResponseWriter, r *http.Request) {
	s, ok := h.load(w, r)
	if !ok {
		return
	}
	var warnings []string
	doc, err := h.store.Load(s.job.ID)
	if errors.Is(err, ErrNotFound) {
		doc, warnings = s.seed(h.now())
		err = nil
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	if warnings == nil {
		warnings = []string{}
	}
	model := ModelRevOf(s.notes, s.raw)
	respond.JSON(w, http.StatusOK, EditsResponse{
		Doc: doc, Profile: s.profile, Grid: s.grid, GridError: s.gridErr,
		Model: model, StaleModel: doc.ModelRev.NotesSHA256 != model.NotesSHA256,
		Duration: s.duration, Warnings: warnings,
	})
}

func (h *Handlers) putEdits(w http.ResponseWriter, r *http.Request) {
	s, ok := h.load(w, r)
	if !ok {
		return
	}
	var body putBody
	if err := respond.DecodeJSONMax(r, &body, MaxBodyBytes); err != nil {
		respond.Fail(w, err)
		return
	}
	if body.BaseRev < 0 {
		respond.Failf(w, http.StatusBadRequest, "invalid_edits", "base_rev must be ≥ 0")
		return
	}
	if err := Validate(body.Events, s.duration, len(s.notes.Notes)); err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_edits", err.Error())
		return
	}
	now := h.now()
	doc, err := h.store.Save(s.job.ID, func() (Doc, error) { d, _ := s.seed(now); return d, nil }, body.BaseRev, body.Events, now)
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		respond.JSON(w, http.StatusConflict, map[string]any{
			"code":     "edit_conflict",
			"message":  "the edit revision changed since this page loaded; reload to continue",
			"edit_rev": conflict.EditRev,
		})
		return
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, SaveResponse{EditRev: doc.EditRev, UpdatedAt: doc.UpdatedAt})
}
