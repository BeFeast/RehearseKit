package worker_test

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/config"
	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/media"
	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavcheck"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavtest"
	"github.com/BeFeast/RehearseKit/internal/storage"
	"github.com/BeFeast/RehearseKit/internal/worker"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	store  *jobs.Store
	gpu    *gpu.Store
	layout storage.Layout
	cfg    config.Config
	w      *worker.Worker
}

// tempoStub writes a script that emits fixed JSON (optionally after a sleep).
func tempoStub(t *testing.T, json string, sleep time.Duration) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tempo.sh")
	body := fmt.Sprintf("#!/bin/sh\ntest -f \"$1\" || { echo missing >&2; exit 2; }\n"+
		"[ -n \"$RK_STUB_TRACE\" ] && echo \"$$ start $(date +%%s.%%N) sleep=%d\" >> \"$RK_STUB_TRACE\"\n"+
		"sleep %d\n"+
		"[ -n \"$RK_STUB_TRACE\" ] && echo \"$$ end $(date +%%s.%%N) rc=$?\" >> \"$RK_STUB_TRACE\"\n"+
		"echo '%s'\n", int(sleep.Seconds()), int(sleep.Seconds()), json)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newEnv(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()
	if !media.Available(media.FFmpeg, media.FFprobe) {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	pool := dbtest.Pool(t)
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.TempoCmd = tempoStub(t, `{"bpm": 136.5, "confidence": 0.9, "beats": [0.1, 0.54]}`, 0)
	cfg.LeaseTTL = time.Minute
	cfg.GPUWaitTimeout = 20 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}
	layout, err := storage.New(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	w := worker.New(cfg, pool, layout)
	w.Poll = 100 * time.Millisecond
	return &env{t: t, pool: pool, store: jobs.NewStore(pool), gpu: gpu.NewStore(pool, cfg.LeaseTTL), layout: layout, cfg: cfg, w: w}
}

// upload creates a pending upload job with a synthetic source of the given
// extension ("wav" writes directly; anything else is transcoded by ffmpeg).
func (e *env) upload(_ int, ext string, seconds float64, quality string) *jobs.Job {
	e.t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		e.t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	dir, _ := e.layout.JobDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	wav := filepath.Join(dir, "source.wav")
	if _, err := wavtest.Write(wav, wavtest.Options{Seconds: seconds, BitDepth: 16, SampleRate: 44100}); err != nil {
		e.t.Fatal(err)
	}
	if ext != "wav" {
		out := filepath.Join(dir, "source."+ext)
		if _, err := media.Run(context.Background(), media.FFmpeg, "-y", "-loglevel", "error", "-i", wav, out); err != nil {
			e.t.Skipf("ffmpeg cannot encode %s: %v", ext, err)
		}
		_ = os.Remove(wav)
	}
	name := "song." + ext
	j, err := e.store.Create(context.Background(), id, jobs.CreateParams{
		ProjectName: "Song", InputType: jobs.InputUpload, SourceFilename: &name, Quality: quality,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

func (e *env) waitStatus(id string, want string, timeout time.Duration) *jobs.Job {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	last := "?"
	for time.Now().Before(deadline) {
		j, err := e.store.Get(context.Background(), id)
		if err != nil {
			e.t.Fatal(err)
		}
		last = j.Status
		if j.Status == want {
			return j
		}
		if jobs.IsTerminal(j.Status) && j.Status != want {
			e.t.Fatalf("job reached %s (error %v) while waiting for %s", j.Status, deref(j.Error), want)
		}
		time.Sleep(50 * time.Millisecond)
	}
	evs, _ := e.store.Events(context.Background(), id, 0)
	var log []string
	for _, ev := range evs {
		log = append(log, fmt.Sprintf("%s/%d %q", ev.Status, ev.Progress, ev.Message))
	}
	e.t.Fatalf("job did not reach %s within %s (last status %s); events: %s", want, timeout, last, strings.Join(log, "; "))
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// fakeRunner plays the GPU side: lease, "separate" by copying the source
// into each stem, complete.
func (e *env) fakeRunner(jobID string) {
	e.t.Helper()
	ctx := context.Background()
	l, j, err := e.gpu.Claim(ctx, "fake")
	if err != nil {
		e.t.Fatalf("fake runner claim: %v", err)
	}
	if j.ID != jobID {
		e.t.Fatalf("fake runner got job %s, want %s", j.ID, jobID)
	}
	if _, err := e.gpu.Heartbeat(ctx, l.ID, "fake", 0.5); err != nil {
		e.t.Fatal(err)
	}
	_, stems := jobs.ModelFor(j.Quality)
	src := filepath.Join(filepath.Dir(mustStemPath(e, jobID, "vocals")), "..", "source.wav")
	var reports []gpu.StemReport
	for _, name := range stems {
		dst := mustStemPath(e, jobID, name)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := media.CopyFile(src, dst); err != nil {
			e.t.Fatal(err)
		}
		st, _ := os.Stat(dst)
		reports = append(reports, gpu.StemReport{Name: name, Bytes: st.Size()})
	}
	if err := e.gpu.Complete(ctx, l.ID, "fake", reports, nil); err != nil {
		e.t.Fatalf("fake runner complete: %v", err)
	}
}

func mustStemPath(e *env, id, name string) string {
	p, err := e.layout.StemPath(id, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestPipelineEndToEnd(t *testing.T) {
	e := newEnv(t, nil)
	j := e.upload(1, "mp3", 2, jobs.QualityHigh6)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.w.RunOnce(ctx)
		done <- err
	}()

	// The worker converts and analyses, then waits for a GPU runner.
	e.waitStatus(j.ID, jobs.StatusSeparating, 30*time.Second)
	dir, _ := e.layout.JobDir(j.ID)
	info, err := wavcheck.Check(filepath.Join(dir, "source.wav"))
	if err != nil {
		t.Fatalf("converted source: %v", err)
	}
	if d := info.Duration(); d < 1.9 || d > 2.2 {
		t.Fatalf("converted duration %v", d)
	}
	if _, err := os.Stat(filepath.Join(dir, "peaks", "source.pk")); err != nil {
		t.Fatalf("source peaks: %v", err)
	}
	tj, err := os.ReadFile(filepath.Join(dir, "tempo.json"))
	if err != nil || !strings.Contains(string(tj), `"bpm": 136.5`) {
		t.Fatalf("tempo.json %s %v", tj, err)
	}
	mid, _ := e.store.Get(ctx, j.ID)
	if mid.DetectedBPM == nil || *mid.DetectedBPM != 136.5 || mid.SampleRate == nil || *mid.SampleRate != 48000 || mid.Channels == nil || *mid.Channels != 2 {
		t.Fatalf("audio info %+v", mid)
	}

	e.fakeRunner(j.ID)

	if err := <-done; err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	final := e.waitStatus(j.ID, jobs.StatusCompleted, 10*time.Second)
	if final.StageProgress != 100 || final.CompletedAt == nil {
		t.Fatalf("final %+v", final)
	}
	if len(final.Stems) != 6 {
		t.Fatalf("stems rows %d", len(final.Stems))
	}
	for _, st := range final.Stems {
		if st.BitDepth != 24 || st.SampleRate != 48000 || st.Channels != 2 || st.Frames != info.Frames || st.PeaksURL == nil {
			t.Fatalf("stem row %+v", st)
		}
		pk, err := os.Open(filepath.Join(dir, "peaks", st.Name+".pk"))
		if err != nil {
			t.Fatal(err)
		}
		f, err := peaks.ReadHeader(pk)
		pk.Close()
		if err != nil || f.Frames != uint64(info.Frames) || f.SampleRate != 48000 {
			t.Fatalf("peaks %s: %+v %v", st.Name, f, err)
		}
	}

	// project.dawproject
	daw, err := zip.OpenReader(filepath.Join(dir, "project.dawproject"))
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(daw.File)
	daw.Close()
	for _, want := range []string{"project.xml", "metadata.xml", "audio/vocals.wav", "audio/piano.wav"} {
		if !names[want] {
			t.Errorf("dawproject lacks %s", want)
		}
	}
	// package.zip
	pkg, err := zip.OpenReader(filepath.Join(dir, "package.zip"))
	if err != nil {
		t.Fatal(err)
	}
	names = zipNames(pkg.File)
	pkg.Close()
	for _, want := range []string{"stems/vocals.wav", "stems/guitar.wav", "project.dawproject", "tempo.json", "README.txt"} {
		if !names[want] {
			t.Errorf("package lacks %s", want)
		}
	}

	// Event stream: statuses in pipeline order, progress monotonic, legacy copy.
	evs, _ := e.store.Events(ctx, j.ID, 0)
	var seq []string
	var last int16 = -1
	for _, ev := range evs {
		if len(seq) == 0 || seq[len(seq)-1] != ev.Status {
			seq = append(seq, ev.Status)
		}
		if ev.Progress < last {
			t.Errorf("progress went backwards: %v then %v (%s)", last, ev.Progress, ev.Status)
		}
		last = ev.Progress
		if ev.Status == jobs.StatusAnalyzing && ev.Message != "Analyzing tempo and detecting BPM..." {
			t.Errorf("analyzing message %q", ev.Message)
		}
	}
	want := "pending converting analyzing separating finalizing packaging completed"
	if strings.Join(seq, " ") != want {
		t.Fatalf("status sequence %v", seq)
	}
}

func zipNames(files []*zip.File) map[string]bool {
	m := map[string]bool{}
	for _, f := range files {
		m[f.Name] = true
	}
	return m
}

func TestTooLongIsRejected(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.MaxDuration = time.Second })
	j := e.upload(1, "wav", 3, jobs.QualityFast)
	if _, err := e.w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	final, _ := e.store.Get(context.Background(), j.ID)
	if final.Status != jobs.StatusFailed || final.Error == nil || !strings.HasPrefix(*final.Error, "Audio is too long") {
		t.Fatalf("final %+v error %v", final.Status, deref(final.Error))
	}
}

func TestCancelKillsStage(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.TempoCmd = tempoStub(t, `{"bpm": 100, "confidence": 1}`, 30*time.Second)
	})
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	done := make(chan struct{})
	go func() {
		_, _ = e.w.RunOnce(context.Background())
		close(done)
	}()
	e.waitStatus(j.ID, jobs.StatusAnalyzing, 20*time.Second)
	start := time.Now()
	if err := jobs.Cancel(context.Background(), e.pool, j.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop after cancel (child not killed)")
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Fatalf("took %s to react to cancel", took)
	}
	final, _ := e.store.Get(context.Background(), j.ID)
	if final.Status != jobs.StatusCancelled {
		t.Fatalf("status %s", final.Status)
	}
}

func TestResumeFromFinalizing(t *testing.T) {
	e := newEnv(t, nil)
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	ctx := context.Background()
	dir, _ := e.layout.JobDir(j.ID)
	// Pretend an earlier worker converted, analysed and a runner delivered stems.
	if err := media.ConvertToStemWAV(ctx, filepath.Join(dir, "source.wav"), filepath.Join(dir, "source.wav")); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "tempo.json"), []byte(`{"bpm": null, "confidence": 0.1}`), 0o644)
	for _, name := range jobs.FourStems {
		dst := mustStemPath(e, j.ID, name)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := media.CopyFile(filepath.Join(dir, "source.wav"), dst); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range []string{jobs.StatusConverting, jobs.StatusAnalyzing, jobs.StatusSeparating, jobs.StatusFinalizing} {
		if err := jobs.Transition(ctx, e.pool, j.ID, st, jobs.StageStart(st), st); err != nil {
			t.Fatal(err)
		}
	}
	jj, _ := e.store.Get(ctx, j.ID)
	e.w.Process(ctx, jj)
	final, _ := e.store.Get(ctx, j.ID)
	if final.Status != jobs.StatusCompleted {
		t.Fatalf("status %s error %v", final.Status, deref(final.Error))
	}
	// Null tempo → placeholder in the project and README.
	pkg, err := zip.OpenReader(filepath.Join(dir, "package.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	for _, f := range pkg.File {
		if f.Name == "README.txt" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.Contains(string(b), "not detected") {
				t.Fatalf("README %s", b)
			}
		}
	}
}

func TestGPUWaitTimeout(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.GPUWaitTimeout = 500 * time.Millisecond })
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	if _, err := e.w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	final, _ := e.store.Get(context.Background(), j.ID)
	if final.Status != jobs.StatusFailed || !strings.Contains(deref(final.Error), "no GPU runner") {
		t.Fatalf("%s %v", final.Status, deref(final.Error))
	}
}

func TestRetentionSweep(t *testing.T) {
	e := newEnv(t, nil)
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET expires_at = now() - interval '1 minute' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	e.w.SweepRetention(ctx)
	if _, err := e.store.Get(ctx, j.ID); err == nil {
		t.Fatal("expired job still in the database")
	}
	dir, _ := e.layout.JobDir(j.ID)
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("expired job directory still on disk")
	}
}
