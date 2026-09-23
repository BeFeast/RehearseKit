package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/analysis"
)

// Transcription on the runner: after the stems are uploaded, adapters
// (Python CLIs under TranscribeConfig.ToolsDir) produce the beat grid from
// the mix and note events per stem. Each adapter runs under its own
// timeout; a failure is recorded in analysis.json (status "failed" +
// reason) and never fails the lease, so the job still completes with its
// stems. analysis.json is always written by this Go code, even when every
// adapter failed, so the server can require it on a transcribe lease.
//
// Adapter CLI contract (tools/transcribe/*.py):
//
//	grid_<name>.py  --input mix.wav  --output grid.json  --device cuda|cpu
//	notes_<name>.py --input stem.wav --output notes.json --stem guitar --device cuda|cpu
//	sections_<name>.py --input mix.wav --output sections.json --stems <dir> --device cuda|cpu
//
// grid.json is analysis.Grid; notes.json is analysis.Notes; sections.json
// is a JSON array of analysis.Section. Non-zero exit = failure; stderr is
// the reason.

// TranscribeConfig selects the adapters.
type TranscribeConfig struct {
	// Enabled advertises the capability and runs the adapters.
	Enabled bool
	// ToolsDir holds the adapter scripts (tools/transcribe).
	ToolsDir string
	// Device passed to the adapters (cuda / cpu); default the demucs device.
	Device string
	// Adapters by stem ("drums", "bass", "guitar", "piano"), "grid" and
	// "sections"; empty = default, "off" = skip.
	Adapters map[string]string
	// Timeouts.
	GridTimeout     time.Duration
	NotesTimeout    time.Duration
	SectionsTimeout time.Duration
	// Env is appended to every adapter's environment (model dirs, HF offline).
	Env []string
}

// Default adapters. The instrument default can be overridden per stem via
// RK_ADAPTER_<STEM>; muscriptor needs gated weights, so the fallback that
// needs none is the default.
var defaultAdapters = map[string]string{
	"grid":     "beatthis",
	"drums":    "adtof",
	"bass":     "hfmidi",
	"guitar":   "hfmidi",
	"piano":    "hfmidi",
	"sections": "off",
}

func (c TranscribeConfig) adapter(key string) string {
	if v := strings.TrimSpace(c.Adapters[key]); v != "" {
		return v
	}
	return defaultAdapters[key]
}

// transcribeResult is what the runner uploads.
type transcribeResult struct {
	Analysis []byte
	Notes    map[string][]byte // stem → notes.json bytes
}

// transcribe runs the adapters for a lease. mix is the source WAV, stems
// maps stem name → WAV path. Progress is reported through report in
// 0..1 of the transcription phase.
func (a *Agent) transcribe(ctx context.Context, lease gpu.LeaseResponse, mix string, stems map[string]string, work string, log *slog.Logger, report func(float64)) transcribeResult {
	cfg := a.cfg.Transcribe
	res := analysis.Result{Version: analysis.Version, Instruments: map[string]analysis.Instrument{}, Runner: a.cfg.RunnerID, Device: cfg.Device}
	out := transcribeResult{Notes: map[string][]byte{}}
	dir := filepath.Join(work, "transcribe")
	_ = os.MkdirAll(dir, 0o755)

	// Which stems to transcribe: the transcribable ones the lease produced.
	var targets []string
	for _, s := range jobs.TranscribeStems {
		if _, ok := stems[s]; ok {
			targets = append(targets, s)
		}
	}
	steps := 1 + len(targets) + 1
	done := 0
	tick := func() {
		done++
		report(float64(done) / float64(steps))
	}

	// 1. Grid.
	if ad := cfg.adapter("grid"); ad == "off" {
		res.GridError = "grid adapter disabled"
	} else {
		gridPath := filepath.Join(dir, "grid.json")
		start := time.Now()
		err := a.runAdapter(ctx, cfg.GridTimeout, log, "grid_"+ad+".py", "--input", mix, "--output", gridPath, "--device", cfg.Device)
		if err == nil {
			var g analysis.Grid
			if b, rerr := os.ReadFile(gridPath); rerr != nil {
				err = rerr
			} else if jerr := json.Unmarshal(b, &g); jerr != nil {
				err = fmt.Errorf("grid.json: %w", jerr)
			} else if len(g.Beats) == 0 {
				err = errors.New("grid adapter returned no beats")
			} else {
				if g.Source == "" {
					g.Source = ad
				}
				res.Grid = &g
			}
		}
		if err != nil {
			res.GridError = truncate(err.Error(), 500)
			log.Warn("grid adapter failed", "adapter", ad, "err", err, "took", time.Since(start).Round(time.Second))
		} else {
			log.Info("grid", "adapter", ad, "beats", len(res.Grid.Beats), "downbeats", len(res.Grid.Downbeats), "took", time.Since(start).Round(time.Second))
		}
	}
	tick()

	// 2. Notes per stem.
	for _, stem := range targets {
		ad := cfg.adapter(stem)
		inst := analysis.Instrument{Adapter: ad}
		if ad == "off" {
			inst.Status = analysis.StatusSkipped
			inst.Reason = "adapter disabled"
			res.Instruments[stem] = inst
			tick()
			continue
		}
		notesPath := filepath.Join(dir, stem+".json")
		start := time.Now()
		err := a.runAdapter(ctx, cfg.NotesTimeout, log, "notes_"+ad+".py", "--input", stems[stem], "--output", notesPath, "--stem", stem, "--device", cfg.Device)
		var notes analysis.Notes
		var raw []byte
		if err == nil {
			raw, err = os.ReadFile(notesPath)
		}
		if err == nil {
			notes, err = analysis.ParseNotes(raw)
		}
		inst.Seconds = time.Since(start).Seconds()
		if err != nil {
			inst.Status = analysis.StatusFailed
			inst.Reason = truncate(err.Error(), 500)
			log.Warn("notes adapter failed", "stem", stem, "adapter", ad, "err", err, "took", time.Since(start).Round(time.Second))
		} else {
			notes.Stem = stem
			if notes.Adapter == "" {
				notes.Adapter = ad
			}
			inst.Status = analysis.StatusOK
			inst.Model = notes.Model
			inst.Notes = len(notes.Notes)
			b, _ := json.Marshal(notes)
			out.Notes[stem] = b
			log.Info("notes", "stem", stem, "adapter", ad, "notes", len(notes.Notes), "took", time.Since(start).Round(time.Second))
		}
		res.Instruments[stem] = inst
		tick()
	}

	// 3. Sections (optional).
	if ad := cfg.adapter("sections"); ad != "off" {
		secPath := filepath.Join(dir, "sections.json")
		stemsDir := dir
		if len(targets) > 0 {
			stemsDir = filepath.Dir(stems[targets[0]])
		}
		err := a.runAdapter(ctx, cfg.SectionsTimeout, log, "sections_"+ad+".py", "--input", mix, "--output", secPath, "--stems", stemsDir, "--device", cfg.Device)
		if err == nil {
			var secs []analysis.Section
			if b, rerr := os.ReadFile(secPath); rerr != nil {
				err = rerr
			} else if jerr := json.Unmarshal(b, &secs); jerr != nil {
				err = jerr
			} else {
				res.Sections = secs
			}
		}
		if err != nil {
			res.Errors = append(res.Errors, "sections: "+truncate(err.Error(), 500))
			log.Warn("sections adapter failed", "adapter", ad, "err", err)
		}
	}
	tick()

	b, _ := json.MarshalIndent(res, "", "  ")
	out.Analysis = append(b, '\n')
	return out
}

// runAdapter runs one adapter script with a timeout. The error carries the
// tail of stderr.
func (a *Agent) runAdapter(ctx context.Context, timeout time.Duration, log *slog.Logger, script string, args ...string) error {
	path := filepath.Join(a.cfg.Transcribe.ToolsDir, script)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("adapter %s not installed", script)
	}
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.cfg.Python, append([]string{path}, args...)...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(append(os.Environ(), "PYTHONUNBUFFERED=1"), a.cfg.Transcribe.Env...)
	var stderr strings.Builder
	cmd.Stderr = &tailWriter{b: &stderr, max: 4000}
	cmd.Stdout = cmd.Stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s timed out after %s", script, timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 && len(msg)-i > 1 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("%s: %v: %s", script, err, msg)
	}
	return nil
}

// tailWriter keeps the last max bytes written.
type tailWriter struct {
	b   *strings.Builder
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > t.max {
		s := t.b.String()
		t.b.Reset()
		t.b.WriteString(s[len(s)-t.max:])
	}
	return len(p), nil
}

// uploadArtifact PUTs a JSON artefact and returns its report.
func (a *Agent) uploadArtifact(ctx context.Context, url, name string, body []byte) (gpu.ArtifactReport, error) {
	sum := sha256.Sum256(body)
	rep := gpu.ArtifactReport{Name: name, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, strings.NewReader(string(body)))
	if err != nil {
		return rep, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return rep, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b := make([]byte, 4096)
		n, _ := resp.Body.Read(b)
		return rep, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b[:n])))
	}
	return rep, nil
}
