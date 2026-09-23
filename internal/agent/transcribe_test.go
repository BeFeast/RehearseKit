package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/pipeline/analysis"
)

// stubTools writes fake adapter scripts: the "python" is /bin/sh, so each
// adapter is a shell script that takes the CLI contract's flags.
func stubTools(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// argv parses "--k v" pairs into a shell snippet that sets $out, $stem.
const parseArgs = `out=""; stem=""; while [ $# -gt 0 ]; do case "$1" in --output) out="$2"; shift;; --stem) stem="$2"; shift;; esac; shift; done;`

func TestTranscribeDegradesPerAdapter(t *testing.T) {
	tools := stubTools(t, map[string]string{
		"grid_ok.py":    parseArgs + ` echo '{"beats":[0.5,1.0,1.5,2.0],"downbeats":[0.5],"source":"stub"}' > "$out"`,
		"notes_ok.py":   parseArgs + ` echo "{\"stem\":\"$stem\",\"model\":\"m\",\"notes\":[{\"onset\":0.5,\"offset\":1,\"pitch\":40,\"velocity\":0.8}]}" > "$out"`,
		"notes_bad.py":  parseArgs + ` echo "boom: model not found" >&2; exit 3`,
		"notes_slow.py": parseArgs + ` sleep 5`,
		"notes_junk.py": parseArgs + ` echo '{"notes":[{"onset":2,"offset":1,"pitch":40,"velocity":1}]}' > "$out"`,
	})
	a, err := New(Config{APIURL: "http://127.0.0.1:1", Token: "t", Python: "/bin/sh", Transcribe: TranscribeConfig{
		Enabled: true, ToolsDir: tools, Device: "cpu", NotesTimeout: 300 * time.Millisecond,
		Adapters: map[string]string{"grid": "ok", "drums": "bad", "bass": "ok", "guitar": "slow", "piano": "junk"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	stems := map[string]string{}
	for _, s := range []string{"vocals", "drums", "bass", "other", "guitar", "piano"} {
		stems[s] = filepath.Join(work, s+".wav")
	}
	var last float64
	res := a.transcribe(context.Background(), gpu.LeaseResponse{Transcribe: true}, filepath.Join(work, "source.wav"), stems, work,
		slog.New(slog.NewTextHandler(io.Discard, nil)), func(f float64) { last = f })
	if last != 1 {
		t.Errorf("progress ended at %v", last)
	}
	r, err := analysis.Parse(res.Analysis)
	if err != nil {
		t.Fatalf("analysis.json invalid: %v\n%s", err, res.Analysis)
	}
	if r.Grid == nil || len(r.Grid.Beats) != 4 || r.Grid.Source != "stub" {
		t.Errorf("grid %+v", r.Grid)
	}
	if in := r.Instruments["bass"]; in.Status != analysis.StatusOK || in.Notes != 1 || in.Model != "m" || in.Adapter != "ok" {
		t.Errorf("bass %+v", in)
	}
	if in := r.Instruments["drums"]; in.Status != analysis.StatusFailed || in.Reason == "" || in.Adapter != "bad" {
		t.Errorf("drums %+v", in)
	}
	if in := r.Instruments["guitar"]; in.Status != analysis.StatusFailed || in.Reason == "" {
		t.Errorf("guitar (timeout) %+v", in)
	}
	if in := r.Instruments["piano"]; in.Status != analysis.StatusFailed {
		t.Errorf("piano (invalid notes) %+v", in)
	}
	if _, ok := res.Notes["bass"]; !ok || len(res.Notes) != 1 {
		t.Errorf("notes uploaded: %v", res.Notes)
	}
	var n analysis.Notes
	_ = json.Unmarshal(res.Notes["bass"], &n)
	if n.Stem != "bass" || n.Adapter != "ok" {
		t.Errorf("notes %+v", n)
	}
	// A missing adapter script (not installed in the image) degrades too.
	a.cfg.Transcribe.Adapters = map[string]string{"grid": "missing", "drums": "off", "bass": "off", "guitar": "off", "piano": "off"}
	res = a.transcribe(context.Background(), gpu.LeaseResponse{Transcribe: true}, filepath.Join(work, "source.wav"), stems, work,
		slog.New(slog.NewTextHandler(io.Discard, nil)), func(float64) {})
	r, err = analysis.Parse(res.Analysis)
	if err != nil || r.Grid != nil || r.GridError == "" || r.Instruments["drums"].Status != analysis.StatusSkipped {
		t.Errorf("missing adapter: %v %+v", err, r)
	}
}

func TestRebaseArtifactURLs(t *testing.T) {
	a, err := New(Config{APIURL: "http://127.0.0.1:18080", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	lease := gpu.LeaseResponse{
		SourceURL:  "https://rk.example.com/api/v1/signed/jobs/x/source?exp=1&sig=a",
		UploadURLs: map[string]string{"vocals": "https://rk.example.com/api/v1/signed/jobs/x/stems/vocals?exp=1&sig=b"},
		ArtifactURLs: &gpu.ArtifactURLs{
			Analysis: "https://rk.example.com/api/v1/signed/jobs/x/analysis?exp=1&sig=c",
			Notes:    map[string]string{"bass": "https://rk.example.com/api/v1/signed/jobs/x/notes/bass?exp=1&sig=d"},
		},
	}
	if err := a.rebase(&lease); err != nil {
		t.Fatal(err)
	}
	if lease.ArtifactURLs.Analysis != "http://127.0.0.1:18080/api/v1/signed/jobs/x/analysis?exp=1&sig=c" ||
		lease.ArtifactURLs.Notes["bass"] != "http://127.0.0.1:18080/api/v1/signed/jobs/x/notes/bass?exp=1&sig=d" {
		t.Fatalf("rebased %+v", lease.ArtifactURLs)
	}
	if lease.UploadURLs["vocals"] != "http://127.0.0.1:18080/api/v1/signed/jobs/x/stems/vocals?exp=1&sig=b" {
		t.Fatalf("stems %+v", lease.UploadURLs)
	}
}

func TestTranscribeConfigValidation(t *testing.T) {
	if _, err := New(Config{APIURL: "http://x", Token: "t", Transcribe: TranscribeConfig{Enabled: true}}); err == nil {
		t.Fatal("enabled without tools dir must fail")
	}
	a, err := New(Config{APIURL: "http://x", Token: "t", Device: "cuda", Transcribe: TranscribeConfig{Enabled: true, ToolsDir: "/tools"}})
	if err != nil || a.cfg.Transcribe.Device != "cuda" || a.cfg.Transcribe.GridTimeout == 0 {
		t.Fatalf("defaults: %v %+v", err, a.cfg.Transcribe)
	}
}
