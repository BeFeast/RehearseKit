package grid

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/export-*.json")

// exportFixture is testdata/export-<name>.json: the Export of a real
// stand grid plus sample points, so a client port of Beat/Seconds can be
// checked against the Go implementation (web/src/lib/drums/grid.test.ts
// reads the same files).
type exportFixture struct {
	Name     string   `json:"name"`
	Duration float64  `json:"duration"`
	Export   Export   `json:"export"`
	Samples  []sample `json:"samples"`
}

type sample struct {
	Seconds float64 `json:"seconds"`
	Beat    float64 `json:"beat"`
}

type gridInput struct {
	Name      string    `json:"name"`
	Duration  float64   `json:"duration"`
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats"`
}

func loadInputs(t *testing.T) []gridInput {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "grid-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no grid inputs: %v", err)
	}
	var out []gridInput
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var in gridInput
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, in)
	}
	return out
}

func buildFixture(t *testing.T, in gridInput) exportFixture {
	t.Helper()
	m, err := Build(in.Beats, in.Downbeats, in.Duration)
	if err != nil {
		t.Fatalf("%s: %v", in.Name, err)
	}
	fx := exportFixture{Name: in.Name, Duration: in.Duration, Export: m.Export()}
	// Samples: a coarse sweep, the neighbourhood of the first and last
	// beat, and a few exact beats.
	var secs []float64
	for s := 0.0; s <= in.Duration; s += 0.37 {
		secs = append(secs, s)
	}
	b := m.Beats()
	secs = append(secs, b[0]-0.01, b[0], b[0]+0.01, b[len(b)-1]-0.01, b[len(b)-1], b[len(b)-1]+0.01, in.Duration)
	for _, k := range []int{1, 7, len(b) / 2, len(b) - 2} {
		secs = append(secs, b[k])
	}
	for _, s := range secs {
		fx.Samples = append(fx.Samples, sample{Seconds: s, Beat: m.Beat(s)})
	}
	return fx
}

func TestExportFixtures(t *testing.T) {
	for _, in := range loadInputs(t) {
		fx := buildFixture(t, in)
		path := filepath.Join("testdata", "export-"+in.Name+".json")
		got, err := json.MarshalIndent(fx, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		if *update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with -update)", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the built export (run with -update after a deliberate grid change)", path)
		}
	}
}

// TestExportRoundTrip checks that Export carries everything Beat and
// Seconds need: a Map rebuilt from the exported fields agrees with the
// original on every sample, and Seconds inverts Beat.
func TestExportRoundTrip(t *testing.T) {
	for _, in := range loadInputs(t) {
		m, err := Build(in.Beats, in.Downbeats, in.Duration)
		if err != nil {
			t.Fatal(err)
		}
		e := m.Export()
		if len(e.Beats) < MinBeats || e.Median <= 0 || e.BPM < MinBPM || e.BPM > MaxBPM || e.Numerator < 1 {
			t.Fatalf("%s: export %+v", in.Name, e)
		}
		port := &Map{beats: e.Beats, offset: e.Offset, median: e.Median, Constant: e.Constant}
		for s := 0.0; s <= in.Duration; s += 0.11 {
			if b, p := m.Beat(s), port.Beat(s); b != p {
				t.Fatalf("%s: Beat(%v) = %v, port %v", in.Name, s, b, p)
			}
			if back := m.Seconds(m.Beat(s)); math.Abs(back-s) > 1e-9 {
				t.Fatalf("%s: Seconds(Beat(%v)) = %v", in.Name, s, back)
			}
		}
		if e.TimeSignatures[0].Numerator != e.Numerator {
			t.Errorf("%s: first signature %+v vs numerator %d", in.Name, e.TimeSignatures[0], e.Numerator)
		}
		if m.Beat(0) < 0 {
			t.Errorf("%s: second 0 is at beat %v", in.Name, m.Beat(0))
		}
	}
}
