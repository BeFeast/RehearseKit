package gpu

import (
	"errors"
	"strings"
)

// Runner ↔ server protocol additions for transcription. Everything here is
// optional on the wire so an older runner or an older server keeps
// working: a runner advertises capabilities in a request header (ignored
// by an old server), the server only offers transcribe jobs to capable
// runners, and the new response/request fields are omitted unless the
// lease is a transcribe lease (the server decodes with
// DisallowUnknownFields, so a runner must not send them otherwise).

// FeaturesHeader carries the runner's comma-separated capabilities on
// POST /gpu/lease, e.g. "transcribe".
const FeaturesHeader = "X-Runner-Features"

// FeatureTranscribe is the capability for beat grid + MIDI transcription.
const FeatureTranscribe = "transcribe"

// Heartbeat stages.
const (
	StageSeparating   = "separating"
	StageTranscribing = "transcribing"
)

// Artifact names in ArtifactReport.
const (
	ArtifactAnalysis    = "analysis"
	ArtifactNotesPrefix = "notes/"
)

// ErrBadArtifacts is returned when the reported artefacts do not match
// the job (missing analysis on a transcribe job, unknown name, failed
// verification).
var ErrBadArtifacts = errors.New("gpu: artifacts do not match the job")

// Capabilities are what a runner can do beyond separation.
type Capabilities struct {
	Transcribe bool
}

// ParseFeatures parses the FeaturesHeader value.
func ParseFeatures(h string) Capabilities {
	var c Capabilities
	for _, f := range strings.Split(h, ",") {
		if strings.EqualFold(strings.TrimSpace(f), FeatureTranscribe) {
			c.Transcribe = true
		}
	}
	return c
}

// ArtifactURLs are the signed PUT URLs of a transcribe lease.
type ArtifactURLs struct {
	Analysis string            `json:"analysis"`
	Notes    map[string]string `json:"notes"`
}

// ArtifactReport is one uploaded artefact in a complete request: Name is
// "analysis" or "notes/<stem>".
type ArtifactReport struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// HeartbeatRequest is the body of POST /gpu/lease/{id}/heartbeat.
type HeartbeatRequest struct {
	RunnerID string  `json:"runner_id"`
	Progress float64 `json:"progress"`
	// Stage is sent only by transcribe-capable runners on transcribe leases.
	Stage string `json:"stage,omitempty"`
}

// CompleteRequest is the body of POST /gpu/lease/{id}/complete.
type CompleteRequest struct {
	RunnerID string       `json:"runner_id"`
	Stems    []StemReport `json:"stems"`
	// Artifacts is sent only on transcribe leases.
	Artifacts []ArtifactReport `json:"artifacts,omitempty"`
}

// FailRequest is the body of POST /gpu/lease/{id}/fail.
type FailRequest struct {
	RunnerID string `json:"runner_id"`
	Error    string `json:"error"`
}
