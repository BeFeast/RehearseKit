package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/dawproject"
	"github.com/BeFeast/RehearseKit/internal/pipeline/demucs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/media"
	"github.com/BeFeast/RehearseKit/internal/pipeline/pack"
	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
	"github.com/BeFeast/RehearseKit/internal/pipeline/tempo"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavcheck"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// Stage timeouts.
const (
	ConvertTimeout  = 20 * time.Minute
	AnalyzeTimeout  = 15 * time.Minute
	FinalizeTimeout = 20 * time.Minute
	PackageTimeout  = 20 * time.Minute
)

// run is the state of one job being processed.
type run struct {
	w     *Worker
	job   *jobs.Job
	dir   string
	model string
	stems []string
	tempo tempo.Result
	info  wavcheck.Info
	log   *slog.Logger
}

func (w *Worker) newRun(j *jobs.Job) (*run, error) {
	dir, err := w.layout.JobDir(j.ID)
	if err != nil {
		return nil, err
	}
	model, stems := jobs.ModelFor(j.Quality)
	return &run{w: w, job: j, dir: dir, model: model, stems: stems, log: slog.With("job", j.ID)}, nil
}

func (r *run) sourceWAV() string { return filepath.Join(r.dir, "source.wav") }
func (r *run) tempoJSON() string { return filepath.Join(r.dir, "tempo.json") }

// run executes the stages from the job's current status.
func (r *run) run(ctx context.Context) error {
	status := r.job.Status
	resume := status != jobs.StatusConverting && status != jobs.StatusPending
	if resume {
		if err := r.reload(); err != nil {
			return fmt.Errorf("resume %s: %w", status, err)
		}
	}
	order := []string{jobs.StatusConverting, jobs.StatusAnalyzing, jobs.StatusSeparating, jobs.StatusFinalizing, jobs.StatusPackaging}
	started := false
	for _, st := range order {
		if !started {
			if st != status {
				continue
			}
			started = true
		}
		var err error
		switch st {
		case jobs.StatusConverting:
			err = r.convert(ctx)
		case jobs.StatusAnalyzing:
			err = r.analyze(ctx)
		case jobs.StatusSeparating:
			err = r.separate(ctx)
		case jobs.StatusFinalizing:
			err = r.finalize(ctx)
		case jobs.StatusPackaging:
			err = r.pack(ctx)
		}
		if err != nil {
			return err
		}
	}
	if !started {
		return fmt.Errorf("job is in status %s; nothing to run", status)
	}
	return nil
}

// reload restores what earlier stages learned (for resumed jobs).
func (r *run) reload() error {
	info, err := wavcheck.Read(r.sourceWAV())
	if err != nil {
		return fmt.Errorf("source.wav: %w", err)
	}
	r.info = info
	b, err := os.ReadFile(r.tempoJSON())
	if err == nil {
		if t, err := tempo.Parse(b); err == nil {
			r.tempo = t
		}
	}
	return nil
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

// stageErr labels an error with the stage's user-facing prefix, unless it
// is one of the control-flow errors.
func stageErr(prefix string, err error) error {
	if err == nil || errors.Is(err, errJobEnded) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: timed out", prefix)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// convert: source.<ext> (or the yt-dlp download) → source.wav, 24-bit/48k.
func (r *run) convert(ctx context.Context) error {
	ctx, cancel := withTimeout(ctx, ConvertTimeout)
	defer cancel()
	var in string
	if r.job.InputType == jobs.InputYouTube {
		if r.job.InputURL == nil {
			return errors.New("Conversion failed: job has no input_url")
		}
		p, err := media.DownloadYouTube(ctx, *r.job.InputURL, r.dir)
		if err != nil {
			return stageErr("Download failed", err)
		}
		in = p
		defer os.Remove(p)
	} else {
		ext := ""
		if r.job.SourceFilename != nil {
			ext, _ = storage.SourceExt(*r.job.SourceFilename)
		}
		if ext == "" {
			return errors.New("Conversion failed: unknown source extension")
		}
		in = filepath.Join(r.dir, "source."+ext)
	}
	if _, err := os.Stat(in); err != nil {
		return fmt.Errorf("Conversion failed: source file missing (%v)", err)
	}
	dur, err := media.CheckDuration(ctx, in, r.w.cfg.MaxDuration)
	if err != nil {
		if errors.Is(err, media.ErrTooLong) {
			return fmt.Errorf("Audio is too long: %s", strings.TrimPrefix(err.Error(), media.ErrTooLong.Error()+": "))
		}
		return stageErr("Conversion failed", err)
	}
	r.log.Info("converting", "input", filepath.Base(in), "duration", time.Duration(dur*float64(time.Second)).Round(time.Second))
	if err := r.w.transition(ctx, r.job.ID, jobs.StatusConverting, jobs.StageProgress(jobs.StatusConverting, 0.5),
		jobs.StatusMessage(jobs.StatusConverting, 0)); err != nil {
		return err
	}
	if err := media.ConvertToStemWAV(ctx, in, r.sourceWAV()); err != nil {
		return stageErr("Conversion failed", err)
	}
	info, err := wavcheck.Check(r.sourceWAV())
	if err != nil {
		return stageErr("Conversion failed", err)
	}
	r.info = info
	return nil
}

// analyze: tempo.json, peaks/source.pk, jobs.detected_bpm & co.
func (r *run) analyze(ctx context.Context) error {
	ctx, cancel := withTimeout(ctx, AnalyzeTimeout)
	defer cancel()
	p := jobs.StageStart(jobs.StatusAnalyzing)
	if err := r.w.transition(ctx, r.job.ID, jobs.StatusAnalyzing, p, jobs.StatusMessage(jobs.StatusAnalyzing, p)); err != nil {
		return err
	}
	res, err := tempo.Run(ctx, r.tempoCommand(), r.sourceWAV())
	if err != nil {
		return stageErr("Tempo analysis failed", err)
	}
	r.tempo = res
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(r.tempoJSON(), append(b, '\n'), 0o644); err != nil {
		return stageErr("Tempo analysis failed", err)
	}
	if res.BPM != nil {
		r.log.Info("tempo", "bpm", *res.BPM, "confidence", res.Confidence)
	} else {
		r.log.Info("tempo not detected", "confidence", res.Confidence, "raw_bpm", res.RawBPM)
	}
	if err := r.w.transition(ctx, r.job.ID, jobs.StatusAnalyzing, jobs.StageProgress(jobs.StatusAnalyzing, 0.5),
		jobs.StatusMessage(jobs.StatusAnalyzing, p)); err != nil {
		return err
	}
	if err := r.writePeaks(r.sourceWAV(), "source"); err != nil {
		return stageErr("Tempo analysis failed", err)
	}
	if err := r.w.store.SetAudioInfo(ctx, r.job.ID, jobs.AudioInfo{
		BPM: res.BPM, DurationSeconds: r.info.Duration(), SampleRate: r.info.SampleRate, Channels: r.info.Channels,
	}); err != nil {
		return stageErr("Tempo analysis failed", err)
	}
	return nil
}

func (r *run) tempoCommand() []string {
	if c := strings.TrimSpace(r.w.cfg.TempoCmd); c != "" {
		return strings.Fields(c)
	}
	return []string{r.w.cfg.Python, filepath.Join(r.w.cfg.ToolsDir, "tempo", "tempo.py")}
}

// writePeaks builds peaks/<name>.pk for a WAV.
func (r *run) writePeaks(wav, name string) error {
	out, err := r.w.layout.PeaksPath(r.job.ID, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Open(wav)
	if err != nil {
		return err
	}
	defer f.Close()
	pk, err := peaks.Build(f, nil)
	if err != nil {
		return fmt.Errorf("peaks %s: %w", name, err)
	}
	tmp := out + ".part"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := peaks.Write(w, pk); err != nil {
		w.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := w.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, out)
}

// separate: hand the job to a GPU runner and wait, or run demucs locally.
func (r *run) separate(ctx context.Context) error {
	p := jobs.StageStart(jobs.StatusSeparating)
	if r.job.Status != jobs.StatusSeparating {
		if err := r.w.transition(ctx, r.job.ID, jobs.StatusSeparating, p, jobs.StatusMessage(jobs.StatusSeparating, p)); err != nil {
			return err
		}
	}
	if r.w.cfg.LocalDemucs {
		return r.separateLocal(ctx)
	}
	return r.waitForGPU(ctx)
}

func (r *run) separateLocal(ctx context.Context) error {
	device := r.w.cfg.DemucsDevice
	if device == "" {
		device = "cpu"
	}
	work := filepath.Join(r.dir, "work")
	defer os.RemoveAll(work)
	var last int16
	lastAt := time.Time{}
	progress := func(f float64) {
		p := jobs.StageProgress(jobs.StatusSeparating, f)
		if p <= last || time.Since(lastAt) < time.Second {
			return
		}
		last, lastAt = p, time.Now()
		_ = r.w.transition(ctx, r.job.ID, jobs.StatusSeparating, p, jobs.StatusMessage(jobs.StatusSeparating, p))
	}
	r.log.Info("separating locally", "model", r.model, "device", device)
	out, err := demucs.Run(ctx, demucs.Options{
		Python: r.w.cfg.Python, Model: r.model, Device: device, Input: r.sourceWAV(), OutDir: work,
	}, progress)
	if err != nil {
		return stageErr("Stem separation failed", err)
	}
	if err := demucs.ConvertStems(ctx, out, filepath.Join(r.dir, "stems"), r.stems); err != nil {
		return stageErr("Stem separation failed", err)
	}
	p := jobs.StageStart(jobs.StatusFinalizing)
	return r.w.transition(ctx, r.job.ID, jobs.StatusFinalizing, p, jobs.StatusMessage(jobs.StatusFinalizing, p))
}

// waitForGPU polls until a runner completes the lease (job → finalizing).
// Without any runner taking the job within GPUWaitTimeout the job fails.
func (r *run) waitForGPU(ctx context.Context) error {
	r.log.Info("waiting for a GPU runner", "model", r.model)
	waitingSince := time.Now()
	t := time.NewTicker(r.w.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		st, err := r.w.store.Status(ctx, r.job.ID)
		if err != nil {
			if errors.Is(err, jobs.ErrNotFound) {
				return errJobEnded
			}
			continue
		}
		switch st {
		case jobs.StatusFinalizing:
			r.job.Status = st
			return nil
		case jobs.StatusSeparating:
			active, err := r.w.gpu.ActiveLease(ctx, r.job.ID)
			if err == nil && active != nil {
				waitingSince = time.Now()
			} else if time.Since(waitingSince) > r.w.cfg.GPUWaitTimeout {
				return fmt.Errorf("Stem separation failed: no GPU runner picked up the job within %s", r.w.cfg.GPUWaitTimeout)
			}
		default:
			if jobs.IsTerminal(st) {
				return errJobEnded
			}
			return fmt.Errorf("Stem separation failed: unexpected job status %s", st)
		}
	}
}

// finalize: verify stems, peaks, stems rows, project.dawproject.
func (r *run) finalize(ctx context.Context) error {
	ctx, cancel := withTimeout(ctx, FinalizeTimeout)
	defer cancel()
	p := jobs.StageStart(jobs.StatusFinalizing)
	if st, _ := r.w.store.Status(ctx, r.job.ID); st != jobs.StatusFinalizing {
		if err := r.w.transition(ctx, r.job.ID, jobs.StatusFinalizing, p, jobs.StatusMessage(jobs.StatusFinalizing, p)); err != nil {
			return err
		}
	}
	var dawStems []dawproject.Stem
	for i, name := range r.stems {
		path, err := r.w.layout.StemPath(r.job.ID, name)
		if err != nil {
			return err
		}
		info, err := wavcheck.Check(path)
		if err != nil {
			return fmt.Errorf("Finalizing failed: stem %s: %w", name, err)
		}
		if d := info.Duration() - r.info.Duration(); d > 2 || d < -2 {
			r.log.Warn("stem length differs from source", "stem", name, "stem_s", info.Duration(), "source_s", r.info.Duration())
		}
		if err := r.writePeaks(path, name); err != nil {
			return stageErr("Finalizing failed", err)
		}
		peaksRel := "peaks/" + name + ".pk"
		if err := r.w.store.UpsertStem(ctx, r.job.ID, jobs.Stem{
			Name: name, Bytes: info.Bytes, Frames: info.Frames, SampleRate: info.SampleRate, BitDepth: info.BitDepth, Channels: info.Channels,
		}, "stems/"+name+".wav", &peaksRel); err != nil {
			return stageErr("Finalizing failed", err)
		}
		dawStems = append(dawStems, dawproject.Stem{Name: name, Path: path, Duration: info.Duration()})
		frac := 0.5 * float64(i+1) / float64(len(r.stems))
		if err := r.w.transition(ctx, r.job.ID, jobs.StatusFinalizing, jobs.StageProgress(jobs.StatusFinalizing, frac),
			jobs.StatusMessage(jobs.StatusFinalizing, p)); err != nil {
			return err
		}
	}
	proj := dawproject.Project{
		Name: r.job.ProjectName, BPM: r.tempo.BPM, DurationSeconds: r.info.Duration(),
		SampleRate: wavcheck.SampleRate, Channels: wavcheck.Channels, Stems: dawStems, GeneratorVersion: "rk/3",
	}
	if err := dawproject.WriteFile(filepath.Join(r.dir, "project.dawproject"), proj); err != nil {
		return stageErr("Finalizing failed", err)
	}
	return nil
}

// pack: package.zip.
func (r *run) pack(ctx context.Context) error {
	ctx, cancel := withTimeout(ctx, PackageTimeout)
	defer cancel()
	p := jobs.StageStart(jobs.StatusPackaging)
	if err := r.w.transition(ctx, r.job.ID, jobs.StatusPackaging, p, jobs.StatusMessage(jobs.StatusPackaging, p)); err != nil {
		return err
	}
	var entries []pack.Entry
	for _, name := range r.stems {
		path, _ := r.w.layout.StemPath(r.job.ID, name)
		entries = append(entries, pack.Entry{Name: "stems/" + name + ".wav", Path: path})
	}
	entries = append(entries,
		pack.Entry{Name: "project.dawproject", Path: filepath.Join(r.dir, "project.dawproject")},
		pack.Entry{Name: "tempo.json", Path: r.tempoJSON(), Compress: true},
		pack.Entry{Name: "README.txt", Compress: true, Data: pack.Readme(pack.ReadmeParams{
			ProjectName: r.job.ProjectName, BPM: r.tempo.BPM, Duration: r.info.Duration(), Stems: r.stems, Model: r.model,
		})},
	)
	if err := pack.WriteFile(filepath.Join(r.dir, "package.zip"), entries); err != nil {
		return stageErr("Packaging failed", err)
	}
	return r.w.transition(ctx, r.job.ID, jobs.StatusCompleted, jobs.ProgressCompleted, "Processing complete")
}
