package gpu_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavtest"
	"github.com/BeFeast/RehearseKit/internal/signed"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

const token = "runner-secret"

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	store  *gpu.Store
	jobs   *jobs.Store
	layout storage.Layout
	signer *signed.Signer
	ts     *httptest.Server
}

func newEnv(t *testing.T, ttl time.Duration) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	layout, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := gpu.NewStore(pool, ttl)
	signer := signed.New([]byte("sign-key"))
	mux := http.NewServeMux()
	gpu.NewHandlers(store, signer, layout, gpu.Options{RunnerToken: token}).Register(mux)
	signed.NewHandlers(signer, layout).Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &env{t: t, pool: pool, store: store, jobs: jobs.NewStore(pool), layout: layout, signer: signer, ts: ts}
}

// separatingJob creates a job and walks it to `separating`, with a
// source.wav on disk.
// randomID returns a v4 UUID; tests across packages share one NOTIFY
// channel, so fixed ids would cross-talk.
func randomID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (e *env) separatingJob(n int, quality string) *jobs.Job {
	e.t.Helper()
	id := randomID(e.t)
	j, err := e.jobs.Create(context.Background(), id, jobs.CreateParams{
		ProjectName: fmt.Sprintf("job %d", n), InputType: jobs.InputUpload, Quality: quality,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	dir, _ := e.layout.JobDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if _, err := wavtest.Write(filepath.Join(dir, "source.wav"), wavtest.Options{Seconds: 0.1}); err != nil {
		e.t.Fatal(err)
	}
	for _, st := range []string{jobs.StatusConverting, jobs.StatusAnalyzing, jobs.StatusSeparating} {
		if err := jobs.Transition(context.Background(), e.pool, id, st, jobs.StageStart(st), st); err != nil {
			e.t.Fatal(err)
		}
	}
	return j
}

func (e *env) status(id string) string {
	st, err := e.jobs.Status(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) post(path string, body any, tok string) (*http.Response, []byte) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestLeaseStateMachine(t *testing.T) {
	e := newEnv(t, time.Minute)
	ctx := context.Background()
	j := e.separatingJob(1, jobs.QualityHigh6)

	if _, _, err := e.store.Claim(ctx, "r1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// A second claim finds nothing: the job has an active lease.
	if _, _, err := e.store.Claim(ctx, "r2"); !errors.Is(err, gpu.ErrNoJobs) {
		t.Fatalf("second claim: %v", err)
	}
	l, err := e.pool.Query(ctx, `SELECT id FROM gpu_leases WHERE job_id = $1`, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var leaseID string
	for l.Next() {
		_ = l.Scan(&leaseID)
	}
	l.Close()

	// Heartbeats map progress to 28..76 and are owner-bound.
	if _, err := e.store.Heartbeat(ctx, leaseID, "r2", 0.5); !errors.Is(err, gpu.ErrWrongOwner) {
		t.Fatalf("foreign heartbeat: %v", err)
	}
	if _, err := e.store.Heartbeat(ctx, leaseID, "r1", 0.5); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	st, p, _ := e.jobs.Progress(ctx, j.ID)
	if st != jobs.StatusSeparating || p != 52 {
		t.Fatalf("after 50%%: %s %d", st, p)
	}
	// Progress never goes backwards.
	if _, err := e.store.Heartbeat(ctx, leaseID, "r1", 0.1); err != nil {
		t.Fatal(err)
	}
	_, p, _ = e.jobs.Progress(ctx, j.ID)
	if p != 52 {
		t.Fatalf("progress regressed to %d", p)
	}
	if _, err := e.store.Heartbeat(ctx, leaseID, "r1", 1); err != nil {
		t.Fatal(err)
	}
	_, p, _ = e.jobs.Progress(ctx, j.ID)
	if p != 76 {
		t.Fatalf("progress at 100%% = %d, want 76", p)
	}

	// Complete needs all six stems.
	err = e.store.Complete(ctx, leaseID, "r1", []gpu.StemReport{{Name: "vocals"}}, nil)
	if !errors.Is(err, gpu.ErrBadStems) {
		t.Fatalf("incomplete stems: %v", err)
	}
	var reports []gpu.StemReport
	for _, n := range jobs.SixStems {
		reports = append(reports, gpu.StemReport{Name: n})
	}
	verifyCalls := 0
	if err := e.store.Complete(ctx, leaseID, "r1", reports, func(_ context.Context, _ string, _ gpu.StemReport) error {
		verifyCalls++
		return nil
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if verifyCalls != 6 {
		t.Fatalf("verify called %d times", verifyCalls)
	}
	if got := e.status(j.ID); got != jobs.StatusFinalizing {
		t.Fatalf("status after complete: %s", got)
	}
	lease, err := e.store.Get(ctx, leaseID)
	if err != nil || lease.State != gpu.StateCompleted {
		t.Fatalf("lease state %v %v", lease, err)
	}
	// The closed lease rejects further calls.
	if _, err := e.store.Heartbeat(ctx, leaseID, "r1", 1); !errors.Is(err, gpu.ErrNotActive) {
		t.Fatalf("heartbeat on closed lease: %v", err)
	}
	if _, err := e.store.Fail(ctx, leaseID, "r1", "x"); !errors.Is(err, gpu.ErrNotActive) {
		t.Fatalf("fail on closed lease: %v", err)
	}
}

func TestFailPolicy(t *testing.T) {
	e := newEnv(t, time.Minute)
	ctx := context.Background()
	j := e.separatingJob(1, jobs.QualityFast)
	for attempt := 1; attempt <= gpu.DefaultMaxFailures; attempt++ {
		l, _, err := e.store.Claim(ctx, "r")
		if err != nil {
			t.Fatalf("claim %d: %v", attempt, err)
		}
		jobFailed, err := e.store.Fail(ctx, l.ID, "r", fmt.Sprintf("boom %d", attempt))
		if err != nil {
			t.Fatal(err)
		}
		if jobFailed != (attempt == gpu.DefaultMaxFailures) {
			t.Fatalf("attempt %d: jobFailed=%v", attempt, jobFailed)
		}
		if attempt < gpu.DefaultMaxFailures {
			if got := e.status(j.ID); got != jobs.StatusSeparating {
				t.Fatalf("attempt %d: status %s", attempt, got)
			}
		}
	}
	if got := e.status(j.ID); got != jobs.StatusFailed {
		t.Fatalf("final status %s", got)
	}
	jj, _ := e.jobs.Get(ctx, j.ID)
	if jj.Error == nil || !strings.Contains(*jj.Error, "boom 3") {
		t.Fatalf("error %v", jj.Error)
	}
	if _, _, err := e.store.Claim(ctx, "r"); !errors.Is(err, gpu.ErrNoJobs) {
		t.Fatalf("claim after job failed: %v", err)
	}
}

func TestExpiryReleasesJob(t *testing.T) {
	e := newEnv(t, 50*time.Millisecond)
	ctx := context.Background()
	j := e.separatingJob(1, jobs.QualityFast)
	l1, _, err := e.store.Claim(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.Claim(ctx, "r2"); !errors.Is(err, gpu.ErrNoJobs) {
		t.Fatalf("claim while leased: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	expired, err := e.store.ExpireStale(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != l1.ID {
		t.Fatalf("expired %v", expired)
	}
	if got := e.status(j.ID); got != jobs.StatusSeparating {
		t.Fatalf("status after expiry %s", got)
	}
	// The old runner is told its lease is gone.
	if _, err := e.store.Heartbeat(ctx, l1.ID, "r1", 0.9); !errors.Is(err, gpu.ErrNotActive) {
		t.Fatalf("stale heartbeat: %v", err)
	}
	// And another runner can take the job.
	l2, jj, err := e.store.Claim(ctx, "r2")
	if err != nil {
		t.Fatal(err)
	}
	if jj.ID != j.ID || l2.ID == l1.ID {
		t.Fatalf("re-lease %v %v", l2, jj)
	}
}

func TestCancelledJobClosesLease(t *testing.T) {
	e := newEnv(t, time.Minute)
	ctx := context.Background()
	j := e.separatingJob(1, jobs.QualityFast)
	l, _, err := e.store.Claim(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Cancel(ctx, e.pool, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Heartbeat(ctx, l.ID, "r", 0.5); !errors.Is(err, gpu.ErrJobGone) {
		t.Fatalf("heartbeat after cancel: %v", err)
	}
	lease, _ := e.store.Get(ctx, l.ID)
	if lease.State == gpu.StateActive {
		t.Fatal("lease still active after the job was cancelled")
	}
}

func TestConcurrentRunners(t *testing.T) {
	e := newEnv(t, time.Minute)
	ctx := context.Background()
	const n = 6
	for i := 1; i <= n; i++ {
		e.separatingJob(i, jobs.QualityFast)
	}
	var mu sync.Mutex
	got := map[string]int{}
	var wg sync.WaitGroup
	errs := make(chan error, 4*n)
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for {
				_, j, err := e.store.Claim(ctx, fmt.Sprintf("r%d", r))
				if errors.Is(err, gpu.ErrNoJobs) {
					return
				}
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				got[j.ID]++
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("leased %d distinct jobs, want %d", len(got), n)
	}
	for id, c := range got {
		if c != 1 {
			t.Errorf("job %s leased %d times", id, c)
		}
	}
}

func TestHTTPFlow(t *testing.T) {
	e := newEnv(t, time.Minute)
	j := e.separatingJob(1, jobs.QualityFast)

	// Auth.
	resp, _ := e.post("/api/v1/gpu/lease", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	resp, _ = e.post("/api/v1/gpu/lease", nil, "wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}

	// Lease.
	resp, body := e.post("/api/v1/gpu/lease", map[string]string{"runner_id": "vast-1"}, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lease: %d %s", resp.StatusCode, body)
	}
	var lease gpu.LeaseResponse
	if err := json.Unmarshal(body, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.JobID != j.ID || lease.Model != "htdemucs" || len(lease.Stems) != 4 || len(lease.UploadURLs) != 4 {
		t.Fatalf("lease %+v", lease)
	}
	// Empty queue → 204.
	resp, _ = e.post("/api/v1/gpu/lease", nil, token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("empty lease: %d", resp.StatusCode)
	}

	// Download the source through the signed URL.
	src, err := http.Get(lease.SourceURL)
	if err != nil {
		t.Fatal(err)
	}
	srcBytes, _ := io.ReadAll(src.Body)
	src.Body.Close()
	if src.StatusCode != http.StatusOK || len(srcBytes) < 44 {
		t.Fatalf("source: %d %d bytes", src.StatusCode, len(srcBytes))
	}

	// Heartbeat.
	resp, body = e.post("/api/v1/gpu/lease/"+lease.LeaseID+"/heartbeat", map[string]any{"runner_id": "vast-1", "progress": 0.5}, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.post("/api/v1/gpu/lease/"+lease.LeaseID+"/heartbeat", map[string]any{"runner_id": "vast-1", "progress": 1.5}, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad progress: %d", resp.StatusCode)
	}
	resp, _ = e.post("/api/v1/gpu/lease/"+lease.LeaseID+"/heartbeat", map[string]any{"runner_id": "other", "progress": 0.5}, token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign heartbeat: %d", resp.StatusCode)
	}

	// Upload stems (the source itself is a valid 24/48/2 WAV).
	var reports []gpu.StemReport
	for _, name := range lease.Stems {
		req, _ := http.NewRequest(http.MethodPut, lease.UploadURLs[name], bytes.NewReader(srcBytes))
		req.ContentLength = int64(len(srcBytes))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("put %s: %d", name, r.StatusCode)
		}
		sum := sha256.Sum256(srcBytes)
		reports = append(reports, gpu.StemReport{Name: name, Bytes: int64(len(srcBytes)), SHA256: hex.EncodeToString(sum[:])})
	}
	// Wrong checksum is rejected and the lease stays active.
	bad := append([]gpu.StemReport(nil), reports...)
	bad[0].SHA256 = strings.Repeat("0", 64)
	resp, body = e.post("/api/v1/gpu/lease/"+lease.LeaseID+"/complete", map[string]any{"runner_id": "vast-1", "stems": bad}, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad sha complete: %d %s", resp.StatusCode, body)
	}
	resp, body = e.post("/api/v1/gpu/lease/"+lease.LeaseID+"/complete", map[string]any{"runner_id": "vast-1", "stems": reports}, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, body)
	}
	if got := e.status(j.ID); got != jobs.StatusFinalizing {
		t.Fatalf("status %s", got)
	}
	// Closed lease → 410.
	resp, _ = e.post("/api/v1/gpu/lease/"+lease.LeaseID+"/heartbeat", map[string]any{"runner_id": "vast-1", "progress": 1}, token)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("closed heartbeat: %d", resp.StatusCode)
	}
	// Unknown lease → 404.
	resp, _ = e.post("/api/v1/gpu/lease/"+randomID(t)+"/fail", map[string]any{"error": "x"}, token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown lease: %d", resp.StatusCode)
	}
}

func TestHTTPFail(t *testing.T) {
	e := newEnv(t, time.Minute)
	j := e.separatingJob(1, jobs.QualityFast)
	_, body := e.post("/api/v1/gpu/lease", nil, token)
	var lease gpu.LeaseResponse
	_ = json.Unmarshal(body, &lease)
	resp, body := e.post("/api/v1/gpu/lease/"+lease.LeaseID+"/fail", map[string]any{"error": "CUDA out of memory"}, token)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"job_failed":false`) {
		t.Fatalf("fail: %d %s", resp.StatusCode, body)
	}
	if got := e.status(j.ID); got != jobs.StatusSeparating {
		t.Fatalf("status %s", got)
	}
	evs, _ := e.jobs.Events(context.Background(), j.ID, 0)
	last := evs[len(evs)-1]
	if !strings.Contains(last.Message, "attempt 1 of 3") || !strings.Contains(last.Message, "CUDA out of memory") {
		t.Fatalf("last event %q", last.Message)
	}
}

func TestDisabledWithoutToken(t *testing.T) {
	layout, _ := storage.New(t.TempDir())
	mux := http.NewServeMux()
	gpu.NewHandlers(nil, signed.New(nil), layout, gpu.Options{}).Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/gpu/lease", nil)
	req.Header.Set("Authorization", "Bearer x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
