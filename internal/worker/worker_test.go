package worker_test

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/json"
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
	l, j, err := e.gpu.Claim(ctx, "fake", gpu.Capabilities{})
	if err != nil {
		e.t.Fatalf("fake runner claim: %v", err)
	}
	if j.ID != jobID {
		e.t.Fatalf("fake runner got job %s, want %s", j.ID, jobID)
	}
	if _, err := e.gpu.Heartbeat(ctx, l.ID, "fake", 0.5, ""); err != nil {
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
	if err := e.gpu.Complete(ctx, l.ID, "fake", reports, nil, nil, nil); err != nil {
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

	// The intake path returns as soon as the job is handed to the GPU queue.
	if err := <-done; err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	e.fakeRunner(j.ID)
	e.waitStatus(j.ID, jobs.StatusFinalizing, 5*time.Second)

	// The resume path picks the finished job up and finishes it.
	if ran, err := e.w.ResumeOnce(ctx); err != nil || !ran {
		t.Fatalf("ResumeOnce = %v, %v; want true, nil", ran, err)
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
	e := newEnv(t, func(c *config.Config) { c.GPUWaitTimeout = 300 * time.Millisecond })
	ctx := context.Background()
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	if _, err := e.w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(j.ID, jobs.StatusSeparating, 5*time.Second)
	// First sweep starts the clock; nothing fails yet.
	if ids, err := e.w.SweepGPUWait(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("first sweep: %v %v", ids, err)
	}
	time.Sleep(400 * time.Millisecond)
	ids, err := e.w.SweepGPUWait(ctx)
	if err != nil || len(ids) != 1 || ids[0] != j.ID {
		t.Fatalf("second sweep: %v %v", ids, err)
	}
	final, _ := e.store.Get(ctx, j.ID)
	if final.Status != jobs.StatusFailed || !strings.Contains(deref(final.Error), "no GPU runner") {
		t.Fatalf("%s %v", final.Status, deref(final.Error))
	}
}

// TestGPUWaitClockResetsOnLease: a runner holding (then dropping) a lease
// restarts the wait, so a job that had a failed attempt is not failed early.
func TestGPUWaitClockResetsOnLease(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.GPUWaitTimeout = 300 * time.Millisecond })
	ctx := context.Background()
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	if _, err := e.w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.w.SweepGPUWait(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	l, _, err := e.gpu.Claim(ctx, "fake", gpu.Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.w.SweepGPUWait(ctx); len(ids) != 0 {
		t.Fatalf("failed a leased job: %v", ids)
	}
	if _, err := e.gpu.Fail(ctx, l.ID, "fake", "boom"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if ids, _ := e.w.SweepGPUWait(ctx); len(ids) != 0 {
		t.Fatalf("failed a job whose clock should have restarted: %v", ids)
	}
	if st, _ := e.store.Status(ctx, j.ID); st != jobs.StatusSeparating {
		t.Fatalf("status %s", st)
	}
}

// runWorker starts w.Run in the background and stops it when the test ends.
func (e *env) runWorker(ctx context.Context) (stop func()) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		_ = e.w.Run(ctx)
		close(done)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			e.t.Error("worker.Run did not return after cancel")
		}
	}
}

// fakeRunnerAny leases whichever job is offered next and completes it.
func (e *env) fakeRunnerAny() string {
	e.t.Helper()
	ctx := context.Background()
	l, j, err := e.gpu.Claim(ctx, "fake", gpu.Capabilities{})
	if err != nil {
		e.t.Fatalf("fake runner claim: %v", err)
	}
	e.deliverStems(j.ID)
	_, stems := jobs.ModelFor(j.Quality)
	var reports []gpu.StemReport
	for _, name := range stems {
		st, _ := os.Stat(mustStemPath(e, j.ID, name))
		reports = append(reports, gpu.StemReport{Name: name, Bytes: st.Size()})
	}
	if err := e.gpu.Complete(ctx, l.ID, "fake", reports, nil, nil, nil); err != nil {
		e.t.Fatalf("fake runner complete: %v", err)
	}
	return j.ID
}

// deliverStems copies source.wav into each stem file the job expects.
func (e *env) deliverStems(jobID string) {
	e.t.Helper()
	j, err := e.store.Get(context.Background(), jobID)
	if err != nil {
		e.t.Fatal(err)
	}
	dir, _ := e.layout.JobDir(jobID)
	_, stems := jobs.ModelFor(j.Quality)
	for _, name := range stems {
		dst := mustStemPath(e, jobID, name)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := media.CopyFile(filepath.Join(dir, "source.wav"), dst); err != nil {
			e.t.Fatal(err)
		}
	}
}

// stageAtFinalizing fakes a job that was converted, analysed and separated
// elsewhere and sits at status (finalizing by default) with stems on disk,
// the way a runner's Complete leaves it.
func (e *env) stageAtFinalizing(j *jobs.Job) {
	e.t.Helper()
	ctx := context.Background()
	dir, _ := e.layout.JobDir(j.ID)
	if err := media.ConvertToStemWAV(ctx, filepath.Join(dir, "source.wav"), filepath.Join(dir, "source.wav")); err != nil {
		e.t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "tempo.json"), []byte(`{"bpm": 120, "confidence": 0.9}`), 0o644)
	e.deliverStems(j.ID)
	for _, st := range []string{jobs.StatusConverting, jobs.StatusAnalyzing, jobs.StatusSeparating, jobs.StatusFinalizing} {
		if err := jobs.Transition(ctx, e.pool, j.ID, st, jobs.StageStart(st), st); err != nil {
			e.t.Fatal(err)
		}
	}
}

// TestSlotsDoNotWaitForGPU: with two slots, two pending jobs run their CPU
// stages concurrently and both reach `separating` while no runner exists;
// the slots are free again (a third job starts) before any runner shows up.
func TestSlotsDoNotWaitForGPU(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("RK_STUB_TRACE", trace)
	e := newEnv(t, func(c *config.Config) {
		c.WorkerSlots = 2
		c.TempoCmd = tempoStub(t, `{"bpm": 100, "confidence": 1}`, 2*time.Second)
	})
	ctx := context.Background()
	a := e.upload(1, "wav", 1, jobs.QualityFast)
	b := e.upload(2, "wav", 1, jobs.QualityFast)
	c := e.upload(3, "wav", 1, jobs.QualityFast)
	stop := e.runWorker(ctx)
	defer stop()

	for _, j := range []*jobs.Job{a, b, c} {
		e.waitStatus(j.ID, jobs.StatusSeparating, 30*time.Second)
	}
	// The tempo stub ran for a and b at the same time: the second start
	// precedes the first end.
	starts, ends := stubTimes(t, trace)
	if len(starts) != 3 || len(ends) != 3 {
		t.Fatalf("trace: %d starts, %d ends", len(starts), len(ends))
	}
	if !(starts[1] < ends[0]) {
		t.Fatalf("second analysis (%.2f) started only after the first ended (%.2f): slots ran sequentially", starts[1], ends[0])
	}
	if st, err := e.gpu.QueueStats(ctx); err != nil || st.Waiting != 3 {
		t.Fatalf("queue %+v %v", st, err)
	}

	// Runners complete the jobs; the resume loop finishes each one.
	var done []string
	for i := 0; i < 3; i++ {
		done = append(done, e.fakeRunnerAny())
	}
	for _, id := range done {
		e.waitStatus(id, jobs.StatusCompleted, 30*time.Second)
	}
}

func stubTimes(t *testing.T, trace string) (starts, ends []float64) {
	t.Helper()
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		var ts float64
		fmt.Sscanf(f[2], "%f", &ts)
		switch f[1] {
		case "start":
			starts = append(starts, ts)
		case "end":
			ends = append(ends, ts)
		}
	}
	return starts, ends
}

// TestResumeClaimsFinalizing: a job a runner completed is claimed by
// ResumeOnce (not by intake) and taken to completed; a second call finds
// nothing.
func TestResumeClaimsFinalizing(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	e.stageAtFinalizing(j)
	if ran, err := e.w.RunOnce(ctx); err != nil || ran {
		t.Fatalf("intake took a finalizing job: %v %v", ran, err)
	}
	if ran, err := e.w.ResumeOnce(ctx); err != nil || !ran {
		t.Fatalf("ResumeOnce = %v, %v", ran, err)
	}
	final, _ := e.store.Get(ctx, j.ID)
	if final.Status != jobs.StatusCompleted || len(final.Stems) != 4 {
		t.Fatalf("status %s error %v stems %d", final.Status, deref(final.Error), len(final.Stems))
	}
	if ran, err := e.w.ResumeOnce(ctx); err != nil || ran {
		t.Fatalf("second ResumeOnce = %v, %v; want false", ran, err)
	}
}

// TestRequeuedJobAdoptedNextPoll: while the worker runs, an operator sets a
// job's status back by hand in SQL (no event, no lease, no goroutine). A
// job put in finalizing is finished within a poll; a job put back in
// separating is offered to runners again and finished after a runner
// completes it. No restart involved.
func TestRequeuedJobAdoptedNextPoll(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a := e.upload(1, "wav", 1, jobs.QualityFast)
	b := e.upload(2, "wav", 1, jobs.QualityFast)
	for _, j := range []*jobs.Job{a, b} {
		e.stageAtFinalizing(j)
		if err := jobs.Transition(ctx, e.pool, j.ID, jobs.StatusFailed, 0, "GPU blew up"); err != nil {
			t.Fatal(err)
		}
	}
	stop := e.runWorker(ctx)
	defer stop()
	time.Sleep(3 * e.w.Poll) // the worker is idle and polling

	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET status = 'finalizing', error = NULL, completed_at = NULL WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(a.ID, jobs.StatusCompleted, 10*e.w.Poll+5*time.Second)

	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET status = 'separating', error = NULL, completed_at = NULL WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * e.w.Poll)
	if st, _ := e.store.Status(ctx, b.ID); st != jobs.StatusSeparating {
		t.Fatalf("re-queued separating job was touched by the worker: %s", st)
	}
	if got := e.fakeRunnerAny(); got != b.ID {
		t.Fatalf("runner got %s, want %s", got, b.ID)
	}
	e.waitStatus(b.ID, jobs.StatusCompleted, 10*e.w.Poll+5*time.Second)
}

// TestNoDoubleRun: concurrent resume attempts on one job run it once; a
// job locked by another holder is skipped.
func TestNoDoubleRun(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	j := e.upload(1, "wav", 1, jobs.QualityFast)
	e.stageAtFinalizing(j)

	unlock, ok, err := e.store.TryLock(ctx, j.ID)
	if err != nil || !ok {
		t.Fatalf("TryLock %v %v", ok, err)
	}
	if ran, err := e.w.ResumeOnce(ctx); err != nil || ran {
		t.Fatalf("ResumeOnce ran a locked job: %v %v", ran, err)
	}
	unlock()

	const n = 4
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() {
			ran, err := e.w.ResumeOnce(ctx)
			if err != nil {
				t.Error(err)
			}
			results <- ran
		}()
	}
	runs := 0
	for i := 0; i < n; i++ {
		if <-results {
			runs++
		}
	}
	if runs != 1 {
		t.Fatalf("%d concurrent ResumeOnce calls ran the job, want 1", runs)
	}
	evs, _ := e.store.Events(ctx, j.ID, 0)
	var packaging, completed int
	for _, ev := range evs {
		switch ev.Status {
		case jobs.StatusPackaging:
			packaging++
		case jobs.StatusCompleted:
			completed++
		}
	}
	if packaging != 1 || completed != 1 {
		t.Fatalf("packaging events %d, completed events %d; want 1 and 1", packaging, completed)
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

// fakeTranscribeRunner plays a transcribe-capable runner: stems as copies
// of the source, a steady 120 BPM grid, notes for bass, a failed drums
// adapter recorded in analysis.json.
func (e *env) fakeTranscribeRunner(jobID string, seconds float64) {
	e.t.Helper()
	ctx := context.Background()
	l, j, err := e.gpu.Claim(ctx, "fake", gpu.Capabilities{Transcribe: true})
	if err != nil {
		e.t.Fatalf("claim: %v", err)
	}
	if j.ID != jobID || !j.Transcribe {
		e.t.Fatalf("got job %+v", j)
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
	var beats, downs []float64
	for t := 0.1; t < seconds; t += 0.5 {
		beats = append(beats, t)
		if len(beats)%4 == 1 {
			downs = append(downs, t)
		}
	}
	an := map[string]any{
		"version":  1,
		"grid":     map[string]any{"beats": beats, "downbeats": downs, "source": "fake"},
		"sections": []map[string]any{{"start": 0.1, "end": seconds, "label": "Song"}},
		"instruments": map[string]any{
			"bass":  map[string]any{"status": "ok", "adapter": "fake", "notes": 2},
			"drums": map[string]any{"status": "failed", "reason": "adapter timed out"},
		},
	}
	ab, _ := json.Marshal(an)
	ap, _ := e.layout.AnalysisPath(jobID)
	if err := os.WriteFile(ap, ab, 0o644); err != nil {
		e.t.Fatal(err)
	}
	nb := []byte(`{"stem":"bass","adapter":"fake","notes":[{"onset":0.1,"offset":0.5,"pitch":40,"velocity":0.9},{"onset":1.1,"offset":1.6,"pitch":43,"velocity":0.7}]}`)
	np, _ := e.layout.NotesPath(jobID, "bass")
	_ = os.MkdirAll(filepath.Dir(np), 0o755)
	if err := os.WriteFile(np, nb, 0o644); err != nil {
		e.t.Fatal(err)
	}
	arts := []gpu.ArtifactReport{{Name: gpu.ArtifactAnalysis, Bytes: int64(len(ab))}, {Name: "notes/bass", Bytes: int64(len(nb))}}
	if err := e.gpu.Complete(ctx, l.ID, "fake", reports, arts, nil, nil); err != nil {
		e.t.Fatalf("complete: %v", err)
	}
}

func TestTranscribePipeline(t *testing.T) {
	e := newEnv(t, nil)
	j := e.upload(1, "wav", 4, jobs.QualityHigh6)
	if _, err := e.pool.Exec(context.Background(), `UPDATE jobs SET transcribe = true WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := e.w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(j.ID, jobs.StatusSeparating, 30*time.Second)
	// An old runner (no capability) is not offered the job.
	if _, _, err := e.gpu.Claim(ctx, "old", gpu.Capabilities{}); err == nil {
		t.Fatal("old runner leased a transcribe job")
	}
	e.fakeTranscribeRunner(j.ID, 4)
	e.waitStatus(j.ID, jobs.StatusFinalizing, 5*time.Second)
	if ran, err := e.w.ResumeOnce(ctx); err != nil || !ran {
		t.Fatalf("ResumeOnce = %v, %v", ran, err)
	}
	e.waitStatus(j.ID, jobs.StatusCompleted, 10*time.Second)
	dir, _ := e.layout.JobDir(j.ID)

	// midi/bass.mid written, no midi for the failed drums.
	if _, err := os.Stat(filepath.Join(dir, "midi", "bass.mid")); err != nil {
		t.Fatalf("bass.mid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "midi", "drums.mid")); err == nil {
		t.Fatal("drums.mid should not exist")
	}
	// project.xml carries the tempo map, marker and the bass notes track.
	zr, err := zip.OpenReader(filepath.Join(dir, "project.dawproject"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var proj string
	for _, f := range zr.File {
		if f.Name == "project.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			proj = string(b)
		}
	}
	for _, want := range []string{`<Track contentType="notes" loaded="true" id="track-bass-midi"`, `<Marker time=`, `name="Song"`, `<Note time=`} {
		if !strings.Contains(proj, want) {
			t.Errorf("project.xml lacks %s", want)
		}
	}
	// A steady grid → constant tempo, no automation lane; but the transport
	// tempo is the grid's, not librosa's stub 136.5.
	if strings.Contains(proj, "<TempoAutomation") || !strings.Contains(proj, `value="120.000000" id="tempo"`) {
		t.Errorf("tempo: %s", proj[:600])
	}
	// package.zip has the transcription files and the README reports both instruments.
	pz, err := zip.OpenReader(filepath.Join(dir, "package.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer pz.Close()
	names := map[string]bool{}
	var readme string
	for _, f := range pz.File {
		names[f.Name] = true
		if f.Name == "README.txt" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			readme = string(b)
		}
	}
	for _, want := range []string{"midi/bass.mid", "analysis.json", "notes/bass.json", "project.dawproject"} {
		if !names[want] {
			t.Errorf("package lacks %s", want)
		}
	}
	if names["midi/drums.mid"] {
		t.Error("package has drums.mid")
	}
	for _, want := range []string{"TRANSCRIPTION", "bass:    ok (2 notes, fake)", "drums:   failed: adapter timed out", "120.00 BPM constant", "1 markers"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README lacks %q:\n%s", want, readme)
		}
	}
}

func TestLocalDemucsRefusesTranscribe(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LocalDemucs = true })
	j := e.upload(1, "wav", 1, jobs.QualityHigh6)
	if _, err := e.pool.Exec(context.Background(), `UPDATE jobs SET transcribe = true WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := e.w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(j.ID, jobs.StatusSeparating, 30*time.Second)
	if _, err := e.w.ResumeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(j.ID, jobs.StatusFailed, 10*time.Second)
	final, _ := e.store.Get(ctx, j.ID)
	if final.Error == nil || !strings.Contains(*final.Error, "transcription needs a GPU runner") {
		t.Fatalf("error %v", deref(final.Error))
	}
}
