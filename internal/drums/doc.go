package drums

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"

	"github.com/BeFeast/RehearseKit/internal/pipeline/analysis"
)

// Version is the schema version written in edits/drums.json.
const Version = 1

// Event sources.
const (
	SourceModel  = "model"  // seeded from notes/drums.json (moved or not)
	SourceManual = "manual" // drawn, pasted or duplicated in the editor
)

// Limits on a PUT.
const (
	MaxEvents    = 20000
	MaxBodyBytes = 4 << 20
)

// ModelRev identifies the model output an edit revision was derived from.
type ModelRev struct {
	Adapter string `json:"adapter,omitempty"`
	Model   string `json:"model,omitempty"`
	// Count is the number of notes in notes/drums.json.
	Count int `json:"count"`
	// NotesSHA256 is the hash of the notes file.
	NotesSHA256 string `json:"notes_sha256"`
}

// Event is one hit. T is absolute seconds, Vel 0..1 (the editor shows
// round(vel*127)); a model event keeps its onset and velocity bit for bit
// until the user touches it.
type Event struct {
	ID  string  `json:"id"`
	Art string  `json:"art"`
	T   float64 `json:"t"`
	Vel float64 `json:"vel"`
	Src string  `json:"src"`
	// Model is the index into notes/drums.json (sorted by onset) for
	// SourceModel events.
	Model *int `json:"model,omitempty"`
}

// Doc is edits/drums.json.
type Doc struct {
	Version  int      `json:"version"`
	Stem     string   `json:"stem"`
	ModelRev ModelRev `json:"model_rev"`
	// EditRev is 0 for the untouched seed and grows by one per saved PUT.
	EditRev int `json:"edit_rev"`
	// ExportedRev is the revision the last export was built from (0: none).
	ExportedRev int       `json:"exported_rev"`
	Profile     string    `json:"profile"`
	UpdatedAt   time.Time `json:"updated_at"`
	Events      []Event   `json:"events"`
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Seed derives the revision-0 document from the model output. Notes whose
// pitch the profile does not map are dropped with a warning (the taxonomy
// is fixed; nothing is invented for an unknown key).
func Seed(notes analysis.Notes, raw []byte, profile Profile, now time.Time) (Doc, []string) {
	var warnings []string
	d := Doc{
		Version: Version, Stem: "drums", Profile: profile.ID, UpdatedAt: now,
		ModelRev: ModelRevOf(notes, raw),
		Events:   make([]Event, 0, len(notes.Notes)),
	}
	for i, n := range notes.Notes {
		art, ok := profile.Articulation(n.Pitch)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("notes[%d]: pitch %d has no articulation in profile %s, dropped", i, n.Pitch, profile.ID))
			continue
		}
		idx := i
		d.Events = append(d.Events, Event{ID: fmt.Sprintf("m%d", i), Art: art, T: n.Onset, Vel: n.Velocity, Src: SourceModel, Model: &idx})
	}
	return d, warnings
}

// ModelRevOf describes a notes file.
func ModelRevOf(notes analysis.Notes, raw []byte) ModelRev {
	sum := sha256.Sum256(raw)
	return ModelRev{Adapter: notes.Adapter, Model: notes.Model, Count: len(notes.Notes), NotesSHA256: hex.EncodeToString(sum[:])}
}

// Validate checks a PUT's events: known articulations, finite times
// within [0, duration] (duration ≤ 0 disables the upper bound), velocities
// in (0, 1], unique well-formed ids, model indexes within the model
// output. Events are sorted by time in place.
func Validate(events []Event, duration float64, modelCount int) error {
	if len(events) > MaxEvents {
		return fmt.Errorf("%d events, max %d", len(events), MaxEvents)
	}
	seen := make(map[string]bool, len(events))
	for i, e := range events {
		if !idPattern.MatchString(e.ID) {
			return fmt.Errorf("events[%d]: invalid id %q", i, e.ID)
		}
		if seen[e.ID] {
			return fmt.Errorf("events[%d]: duplicate id %q", i, e.ID)
		}
		seen[e.ID] = true
		if !ValidArticulation(e.Art) {
			return fmt.Errorf("events[%d]: unknown articulation %q", i, e.Art)
		}
		if math.IsNaN(e.T) || math.IsInf(e.T, 0) || e.T < 0 || (duration > 0 && e.T > duration) {
			return fmt.Errorf("events[%d]: time %v out of range", i, e.T)
		}
		if math.IsNaN(e.Vel) || e.Vel <= 0 || e.Vel > 1 {
			return fmt.Errorf("events[%d]: velocity %v out of (0, 1]", i, e.Vel)
		}
		switch e.Src {
		case SourceManual:
			if e.Model != nil {
				return fmt.Errorf("events[%d]: manual event with a model index", i)
			}
		case SourceModel:
			if e.Model == nil || *e.Model < 0 || *e.Model >= modelCount {
				return fmt.Errorf("events[%d]: model index out of range", i)
			}
		default:
			return fmt.Errorf("events[%d]: unknown source %q", i, e.Src)
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].T < events[j].T })
	return nil
}
