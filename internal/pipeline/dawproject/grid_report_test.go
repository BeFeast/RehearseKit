package dawproject

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
)

var gridReport = flag.String("grid-report", "", "write <dir>/<fixture>.xml, the project.xml of every grid/testdata/grid-*.json fixture (one mix clip on the grid), for dawgrid.py")

// TestGridReport writes one project.xml per grid fixture so the grid can
// be measured with the head-to-head dawgrid.py exactly as a real export.
// A fixture the grid refuses gets its forced map as <name>-forced.xml and
// the single-tempo fallback as <name>.xml.
func TestGridReport(t *testing.T) {
	if *gridReport == "" {
		t.Skip("set -grid-report <dir>")
	}
	files, err := filepath.Glob(filepath.Join("..", "grid", "testdata", "grid-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no grid fixtures: %v", err)
	}
	if err := os.MkdirAll(*gridReport, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var in struct {
			Name       string    `json:"name"`
			Duration   float64   `json:"duration"`
			Beats      []float64 `json:"beats"`
			Downbeats  []float64 `json:"downbeats"`
			LibrosaBPM *float64  `json:"librosa_bpm"`
		}
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatal(err)
		}
		write := func(name string, m *grid.Map) {
			p := Project{Name: in.Name, DurationSeconds: in.Duration, BPM: in.LibrosaBPM, Grid: m, Year: 2026, GeneratorVersion: "grid-report",
				Stems: []Stem{{Name: "mix", Label: "Mix"}}}
			x, err := ProjectXML(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(*gridReport, name+".xml"), x, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		opt := grid.Options{RefBPM: in.LibrosaBPM}
		m, err := grid.BuildWith(in.Beats, in.Downbeats, in.Duration, opt)
		if err != nil {
			t.Logf("%s: %v", in.Name, err)
			opt.Force = true
			forced, ferr := grid.BuildWith(in.Beats, in.Downbeats, in.Duration, opt)
			if ferr != nil {
				t.Fatal(ferr)
			}
			write(in.Name+"-forced", forced)
		}
		write(in.Name, m)
	}
}
