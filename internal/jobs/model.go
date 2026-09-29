// Package jobs holds the job model, the Postgres-backed queue (FOR UPDATE
// SKIP LOCKED), status transitions with job_events + NOTIFY, and the
// /api/v1/jobs handlers including the SSE progress stream.
package jobs

import (
	"time"

	"github.com/BeFeast/RehearseKit/internal/models"
)

// Job statuses (CHECK constraint in the jobs table).
const (
	StatusPending    = "pending"
	StatusConverting = "converting"
	StatusAnalyzing  = "analyzing"
	StatusSeparating = "separating"
	StatusFinalizing = "finalizing"
	StatusPackaging  = "packaging"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// Input types.
const (
	InputUpload  = "upload"
	InputYouTube = "youtube"
)

// Quality presets. The recipe each maps to depends on the job's stack
// (internal/models/manifest.json): public offers high and hifi (4 stems),
// internal offers all four with Demucs for fast/high/high6 and 6-stem hifi.
const (
	QualityFast  = "fast"
	QualityHigh  = "high"
	QualityHigh6 = "high6"
	QualityHiFi  = "hifi"
)

// Qualities lists the accepted quality values in display order.
var Qualities = []string{QualityFast, QualityHigh, QualityHigh6, QualityHiFi}

// ValidQuality reports whether q is an accepted preset.
func ValidQuality(q string) bool {
	for _, v := range Qualities {
		if v == q {
			return true
		}
	}
	return false
}

// IsTerminal reports whether status ends a job's life cycle.
func IsTerminal(status string) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCancelled
}

// ValidStatus reports whether s is one of the known statuses.
func ValidStatus(s string) bool {
	switch s {
	case StatusPending, StatusConverting, StatusAnalyzing, StatusSeparating,
		StatusFinalizing, StatusPackaging, StatusCompleted, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Job is a row of the jobs table plus its stems.
type Job struct {
	ID             string  `json:"id"`
	OwnerID        *string `json:"owner_id"`
	ProjectName    string  `json:"project_name"`
	InputType      string  `json:"input_type"`
	InputURL       *string `json:"input_url"`
	SourceFilename *string `json:"source_filename"`
	Quality        string  `json:"quality"`
	// Stack is the entitlement the job was created under (public |
	// internal); Model is the separation recipe it runs.
	Stack           string     `json:"stack"`
	Model           string     `json:"model"`
	Transcribe      bool       `json:"transcribe"`
	Status          string     `json:"status"`
	StageProgress   int16      `json:"stage_progress"`
	Error           *string    `json:"error"`
	DetectedBPM     *float64   `json:"detected_bpm"`
	DurationSeconds *float64   `json:"duration_seconds"`
	SampleRate      *int32     `json:"sample_rate"`
	Channels        *int16     `json:"channels"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	ExpiresAt       time.Time  `json:"expires_at"`

	// Stems is the manifest (empty until the worker finalises).
	Stems []Stem `json:"stems"`
	// ClaimToken is set only on the create response for anonymous jobs.
	ClaimToken string `json:"claim_token,omitempty"`

	claimTokenHash []byte
}

// Recipe returns the separation recipe of the job. A row written by a
// server that predates the registry has no model; it ran Demucs by quality.
func (j *Job) Recipe() models.Recipe {
	if r, ok := models.Lookup(j.Model); ok {
		return r
	}
	r, _ := models.Lookup(LegacyModel(j.Quality))
	return r
}

// LegacyModel is the Demucs model the pre-registry server used per quality.
func LegacyModel(quality string) string {
	switch quality {
	case QualityHigh:
		return "htdemucs_ft"
	case QualityHigh6:
		return "htdemucs_6s"
	}
	return "htdemucs"
}

// IsAnonymous reports whether the job has no owner yet.
func (j *Job) IsAnonymous() bool { return j.OwnerID == nil }

// Stem is a row of the stems table with the URLs a client needs.
type Stem struct {
	Name       string  `json:"name"`
	Bytes      int64   `json:"bytes"`
	Frames     int64   `json:"frames"`
	SampleRate int32   `json:"sample_rate"`
	BitDepth   int16   `json:"bit_depth"`
	Channels   int16   `json:"channels"`
	StreamURL  string  `json:"stream_url"`
	PeaksURL   *string `json:"peaks_url"`
}

// Event is a row of job_events; it is also the SSE payload.
type Event struct {
	ID       int64     `json:"-"`
	JobID    string    `json:"-"`
	Status   string    `json:"status"`
	Progress int16     `json:"progress"`
	Message  string    `json:"message"`
	At       time.Time `json:"at"`
}

// StemStreamURL is the API path that serves a stem's WAV.
func StemStreamURL(jobID, name string) string {
	return "/api/v1/jobs/" + jobID + "/stems/" + name
}

// StemPeaksURL is the API path that serves a stem's peaks file.
func StemPeaksURL(jobID, name string) string {
	return StemStreamURL(jobID, name) + "/peaks"
}
