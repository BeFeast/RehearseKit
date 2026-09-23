// Package analysis is the schema of the transcription artefacts a GPU runner
// uploads next to the stems: analysis.json (beat grid, sections, per-
// instrument status) and notes/<stem>.json (note events in seconds). The
// worker reads them from the job directory in finalize; a missing file
// means "no transcription", a failed instrument is recorded, never silent.
package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Version is the schema version written in analysis.json.
const Version = 1

// Status values of an instrument.
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// Result is analysis.json.
type Result struct {
	Version int `json:"version"`
	// Grid is nil when the beat tracker failed or was not confident.
	Grid *Grid `json:"grid"`
	// GridError explains a nil Grid.
	GridError string `json:"grid_error,omitempty"`
	// Sections is nil when no section model ran.
	Sections []Section `json:"sections"`
	// Instruments is keyed by stem name (guitar, bass, piano, drums).
	Instruments map[string]Instrument `json:"instruments"`
	// Errors lists runner-level problems (adapter not installed, ...).
	Errors []string `json:"errors,omitempty"`
	// Runner and Device describe where it ran.
	Runner string `json:"runner,omitempty"`
	Device string `json:"device,omitempty"`
}

// Grid is the beat tracker output.
type Grid struct {
	// Beats and Downbeats are onset times in seconds, ascending; downbeats
	// are a subset of beats (matched by the worker within half a beat).
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats"`
	Source    string    `json:"source"`
	Model     string    `json:"model,omitempty"`
}

// Section is a song part.
type Section struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Label string  `json:"label"`
}

// Instrument is the outcome for one stem.
type Instrument struct {
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Adapter string `json:"adapter,omitempty"`
	Model   string `json:"model,omitempty"`
	// Notes counts the events in notes/<stem>.json.
	Notes int `json:"notes"`
	// Seconds the adapter took.
	Seconds float64 `json:"seconds,omitempty"`
}

// Notes is notes/<stem>.json.
type Notes struct {
	Stem    string `json:"stem"`
	Adapter string `json:"adapter,omitempty"`
	Model   string `json:"model,omitempty"`
	Notes   []Note `json:"notes"`
}

// Note is one event. Pitch is a MIDI key (GM drum note for drums),
// Velocity 0..1.
type Note struct {
	Onset    float64 `json:"onset"`
	Offset   float64 `json:"offset"`
	Pitch    int     `json:"pitch"`
	Velocity float64 `json:"velocity"`
}

// Parse validates analysis.json.
func Parse(b []byte) (Result, error) {
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("analysis: %w", err)
	}
	if r.Version != Version {
		return r, fmt.Errorf("analysis: version %d, want %d", r.Version, Version)
	}
	if r.Grid != nil {
		if err := ascending("beats", r.Grid.Beats); err != nil {
			return r, err
		}
		if err := ascending("downbeats", r.Grid.Downbeats); err != nil {
			return r, err
		}
	}
	for i, s := range r.Sections {
		if bad(s.Start) || bad(s.End) || s.End < s.Start {
			return r, fmt.Errorf("analysis: sections[%d] invalid", i)
		}
	}
	for name, in := range r.Instruments {
		switch in.Status {
		case StatusOK, StatusFailed, StatusSkipped:
		default:
			return r, fmt.Errorf("analysis: instruments[%s].status %q", name, in.Status)
		}
	}
	if r.Instruments == nil {
		r.Instruments = map[string]Instrument{}
	}
	return r, nil
}

// ParseNotes validates notes/<stem>.json and sorts the events by onset.
func ParseNotes(b []byte) (Notes, error) {
	var n Notes
	if err := json.Unmarshal(b, &n); err != nil {
		return n, fmt.Errorf("notes: %w", err)
	}
	for i, e := range n.Notes {
		if bad(e.Onset) || bad(e.Offset) || e.Onset < 0 || e.Offset < e.Onset {
			return n, fmt.Errorf("notes: notes[%d] time invalid", i)
		}
		if e.Pitch < 0 || e.Pitch > 127 {
			return n, fmt.Errorf("notes: notes[%d] pitch %d", i, e.Pitch)
		}
		if bad(e.Velocity) || e.Velocity < 0 || e.Velocity > 1 {
			return n, fmt.Errorf("notes: notes[%d] velocity %v", i, e.Velocity)
		}
	}
	sort.SliceStable(n.Notes, func(i, j int) bool { return n.Notes[i].Onset < n.Notes[j].Onset })
	return n, nil
}

func ascending(name string, v []float64) error {
	prev := -1.0
	for i, t := range v {
		if bad(t) || t < 0 || t < prev {
			return fmt.Errorf("analysis: %s[%d]=%v is not non-negative and ascending", name, i, t)
		}
		prev = t
	}
	return nil
}

func bad(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }
