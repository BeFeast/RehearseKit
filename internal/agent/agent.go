// Package agent is `rk gpu-agent`: the process that runs on a GPU box. It
// leases jobs from `rk serve`, downloads the source, runs demucs, uploads
// the stems through the signed PUT URLs and completes (or fails) the
// lease, heartbeating progress the whole time.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/pipeline/demucs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavcheck"
)

// Config for the agent.
type Config struct {
	APIURL      string        // base URL of rk serve, e.g. https://rk.example.com
	Token       string        // RK_RUNNER_TOKEN
	RunnerID    string        // shown in leases; default hostname
	Python      string        // interpreter with demucs; default python3
	Device      string        // demucs -d; default cuda
	WorkDir     string        // scratch; default os.TempDir()/rk-gpu
	Poll        time.Duration // idle polling interval; default 5s
	Once        bool          // process at most one job, then return
	DemucsExtra []string      // extra demucs args
}

// Agent runs the lease loop.
type Agent struct {
	cfg  Config
	http *http.Client
}

// New builds an agent.
func New(cfg Config) (*Agent, error) {
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	if cfg.APIURL == "" || cfg.Token == "" {
		return nil, errors.New("RK_API_URL and RK_RUNNER_TOKEN are required")
	}
	if cfg.RunnerID == "" {
		cfg.RunnerID, _ = os.Hostname()
		if cfg.RunnerID == "" {
			cfg.RunnerID = "gpu-agent"
		}
	}
	if cfg.Python == "" {
		cfg.Python = "python3"
	}
	if cfg.Device == "" {
		cfg.Device = "cuda"
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join(os.TempDir(), "rk-gpu")
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 5 * time.Second
	}
	return &Agent{cfg: cfg, http: &http.Client{}}, nil
}

// Run loops until ctx is cancelled (or after one job with Once).
func (a *Agent) Run(ctx context.Context) error {
	slog.Info("rk gpu-agent", "api", a.cfg.APIURL, "runner", a.cfg.RunnerID, "device", a.cfg.Device, "once", a.cfg.Once)
	if err := demucs.Check(ctx, a.cfg.Python); err != nil {
		return err
	}
	for ctx.Err() == nil {
		ran, err := a.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("gpu-agent", "err", err)
		}
		if a.cfg.Once && (ran || err != nil) {
			return err
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(a.cfg.Poll):
		}
	}
	return nil
}

// apiError is a non-2xx response.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// post sends JSON and decodes a JSON reply into out (may be nil).
func (a *Agent) post(ctx context.Context, path string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.APIURL+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set(gpu.RunnerIDHeader, a.cfg.RunnerID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		e := &apiError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return resp.StatusCode, e
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// RunOnce leases and processes one job. Returns false when none waits.
func (a *Agent) RunOnce(ctx context.Context) (bool, error) {
	var lease gpu.LeaseResponse
	status, err := a.post(ctx, "/api/v1/gpu/lease", map[string]string{"runner_id": a.cfg.RunnerID}, &lease)
	if err != nil {
		return false, fmt.Errorf("lease: %w", err)
	}
	if status == http.StatusNoContent || lease.LeaseID == "" {
		return false, nil
	}
	log := slog.With("lease", lease.LeaseID, "job", lease.JobID, "model", lease.Model)
	log.Info("leased job", "stems", lease.Stems, "expires_at", lease.ExpiresAt)
	start := time.Now()
	err = a.process(ctx, lease, log)
	if err != nil {
		if ctx.Err() != nil {
			// Shutting down: let the lease expire so another runner takes it.
			return true, ctx.Err()
		}
		log.Error("job failed", "err", err, "took", time.Since(start).Round(time.Second))
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, ferr := a.post(bg, "/api/v1/gpu/lease/"+lease.LeaseID+"/fail",
			map[string]string{"runner_id": a.cfg.RunnerID, "error": truncate(err.Error(), 1900)}, nil); ferr != nil {
			log.Error("report failure", "err", ferr)
		}
		return true, err
	}
	log.Info("job completed", "took", time.Since(start).Round(time.Second))
	return true, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// errJobGone is raised when the server says the job is no longer ours.
var errJobGone = errors.New("job is no longer waiting for separation")

func (a *Agent) process(ctx context.Context, lease gpu.LeaseResponse, log *slog.Logger) error {
	work := filepath.Join(a.cfg.WorkDir, lease.JobID)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// Heartbeats carry the latest progress and detect a cancelled job.
	var progress atomic.Int64 // progress * 1000
	hctx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	gone := make(chan struct{})
	interval := time.Duration(lease.HeartbeatSeconds) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
			}
			p := float64(progress.Load()) / 1000
			hb, cancel := context.WithTimeout(hctx, 20*time.Second)
			_, err := a.post(hb, "/api/v1/gpu/lease/"+lease.LeaseID+"/heartbeat",
				map[string]any{"runner_id": a.cfg.RunnerID, "progress": p}, nil)
			cancel()
			var ae *apiError
			if errors.As(err, &ae) && (ae.Status == http.StatusConflict || ae.Status == http.StatusGone) {
				log.Warn("server closed the lease", "err", err)
				close(gone)
				return
			}
			if err != nil && hctx.Err() == nil {
				log.Warn("heartbeat", "err", err)
			}
		}
	}()
	// Work context: cancelled when the lease is gone.
	wctx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	go func() {
		select {
		case <-gone:
			cancelWork()
		case <-wctx.Done():
		}
	}()
	wrap := func(err error) error {
		if err != nil && ctx.Err() == nil && wctx.Err() != nil {
			return errJobGone
		}
		return err
	}

	// 1. Download the source.
	src := filepath.Join(work, "source.wav")
	if err := a.download(wctx, lease.SourceURL, src); err != nil {
		return wrap(fmt.Errorf("download source: %w", err))
	}
	if _, err := wavcheck.Read(src); err != nil {
		return fmt.Errorf("source is not a readable WAV: %w", err)
	}
	progress.Store(50) // 5 %

	// 2. Separate.
	out, err := demucs.Run(wctx, demucs.Options{
		Python: a.cfg.Python, Model: lease.Model, Device: a.cfg.Device, Input: src,
		OutDir: filepath.Join(work, "out"), Extra: a.cfg.DemucsExtra,
	}, func(p float64) { progress.Store(int64((0.05 + 0.85*p) * 1000)) })
	if err != nil {
		return wrap(err)
	}
	progress.Store(900)

	// 3. FLAC → 24-bit/48 kHz WAV.
	stemsDir := filepath.Join(work, "stems")
	if err := demucs.ConvertStems(wctx, out, stemsDir, lease.Stems); err != nil {
		return wrap(err)
	}
	progress.Store(930)

	// 4. Upload.
	var reports []gpu.StemReport
	for i, name := range lease.Stems {
		url, ok := lease.UploadURLs[name]
		if !ok {
			return fmt.Errorf("lease has no upload URL for %s", name)
		}
		path := filepath.Join(stemsDir, name+".wav")
		rep, err := a.upload(wctx, url, name, path)
		if err != nil {
			return wrap(fmt.Errorf("upload %s: %w", name, err))
		}
		reports = append(reports, rep)
		progress.Store(int64((0.93 + 0.06*float64(i+1)/float64(len(lease.Stems))) * 1000))
		log.Info("uploaded stem", "stem", name, "bytes", rep.Bytes)
	}

	// 5. Complete.
	stopHeartbeat()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := a.post(cctx, "/api/v1/gpu/lease/"+lease.LeaseID+"/complete",
		map[string]any{"runner_id": a.cfg.RunnerID, "stems": reports}, nil); err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	return nil
}

func (a *Agent) download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return fmt.Errorf("short download: %d of %d bytes", n, resp.ContentLength)
	}
	return nil
}

func (a *Agent) upload(ctx context.Context, url, name, path string) (gpu.StemReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return gpu.StemReport{}, err
	}
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err != nil {
		return gpu.StemReport{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return gpu.StemReport{}, err
	}
	rep := gpu.StemReport{Name: name, Bytes: size, SHA256: hex.EncodeToString(sum.Sum(nil))}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return rep, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "audio/wav")
	resp, err := a.http.Do(req)
	if err != nil {
		return rep, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return rep, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var got struct {
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(body, &got) == nil && got.SHA256 != "" && !strings.EqualFold(got.SHA256, rep.SHA256) {
		return rep, fmt.Errorf("server checksum %s differs from local %s", got.SHA256, rep.SHA256)
	}
	return rep, nil
}
