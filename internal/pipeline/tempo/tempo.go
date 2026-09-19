// Package tempo runs the librosa-based analyser (tools/tempo/tempo.py) and
// parses its JSON. The analyser reports bpm as null when its confidence is
// below threshold: a wrong tempo baked into a DAW project is worse than no
// tempo, so the pipeline never invents one.
package tempo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/BeFeast/RehearseKit/internal/pipeline/media"
)

// Result is tempo.json.
type Result struct {
	// BPM is nil when the analyser was not confident.
	BPM *float64 `json:"bpm"`
	// Confidence is 0..1.
	Confidence float64 `json:"confidence"`
	// Beats are beat onset times in seconds (optional).
	Beats []float64 `json:"beats,omitempty"`
	// RawBPM is the analyser's estimate before the confidence gate (debugging).
	RawBPM *float64 `json:"raw_bpm,omitempty"`
	// Method names the analyser (e.g. "librosa.beat_track").
	Method string `json:"method,omitempty"`
}

// Limits on a believable tempo.
const (
	MinBPM = 20
	MaxBPM = 400
)

// Parse validates JSON produced by the analyser.
func Parse(b []byte) (Result, error) {
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("tempo: %w", err)
	}
	if r.BPM != nil {
		if math.IsNaN(*r.BPM) || math.IsInf(*r.BPM, 0) || *r.BPM < MinBPM || *r.BPM > MaxBPM {
			return r, fmt.Errorf("tempo: bpm %v out of range", *r.BPM)
		}
		v := math.Round(*r.BPM*100) / 100
		r.BPM = &v
	}
	if math.IsNaN(r.Confidence) || r.Confidence < 0 || r.Confidence > 1 {
		return r, fmt.Errorf("tempo: confidence %v out of range", r.Confidence)
	}
	prev := -1.0
	for i, t := range r.Beats {
		if math.IsNaN(t) || t < 0 || t < prev {
			return r, fmt.Errorf("tempo: beats[%d]=%v is not non-negative and ascending", i, t)
		}
		prev = t
	}
	return r, nil
}

// Run executes cmd (e.g. ["python3", "tools/tempo/tempo.py"]) with the WAV
// path appended and parses its stdout.
func Run(ctx context.Context, cmd []string, wav string) (Result, error) {
	if len(cmd) == 0 {
		return Result{}, errors.New("tempo: empty command")
	}
	args := append(append([]string(nil), cmd[1:]...), wav)
	out, err := media.Run(ctx, cmd[0], args...)
	if err != nil {
		return Result{}, err
	}
	return Parse(out)
}
