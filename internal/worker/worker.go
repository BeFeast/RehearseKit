// Package worker is `rk worker`. Two loops share one process:
//
//   - the intake loop claims pending jobs and drives each through the CPU
//     stages (converting, analyzing) in its own goroutine, up to Slots at a
//     time, and lets go of the job the moment it is in `separating`: from
//     there a GPU runner takes it through the lease API, and nothing in this
//     process waits on it;
//   - the resume loop picks up, one at a time, every job that is past the
//     GPU hand-off and not being processed by anyone (`finalizing` or
//     `packaging`, whether a runner just completed it or a previous worker
//     died on it) and runs finalizing → packaging → completed. In local
//     demucs mode it also takes `separating` jobs without a lease and runs
//     demucs itself.
//
// "Not being processed" is a per-job Postgres advisory lock (jobs.TryLock),
// so several goroutines or several worker processes never double-run a job.
// Both loops poll every Poll; a job re-queued by hand (status set back in
// SQL) is picked up within one interval.
//
// The process also runs the housekeeping sweepers: expired GPU leases, the
// GPU-wait watchdog (a job nobody leases within GPUWaitTimeout fails),
// stalled CPU-stage jobs and retention.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/config"
	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// Sweeper cadences.
const (
	LeaseSweepInterval     = 30 * time.Second
	StaleSweepInterval     = 5 * time.Minute
	StaleAfter             = 45 * time.Minute
	RetentionSweepInterval = 10 * time.Minute
)

// resumeBatch bounds how many resumable candidates one poll tries to lock.
const resumeBatch = 16

// Worker processes jobs.
type Worker struct {
	cfg    config.Config
	pool   *pgxpool.Pool
	store  *jobs.Store
	gpu    *gpu.Store
	layout storage.Layout

	// Poll is the queue polling and cancellation check interval.
	Poll time.Duration
	// Slots is how many jobs run the CPU stages concurrently (default
	// cfg.WorkerSlots, RK_WORKER_SLOTS).
	Slots int

	// waiting is when each job in `separating` was last seen without an
	// active lease; the GPU-wait watchdog fails a job after GPUWaitTimeout.
	mu      sync.Mutex
	waiting map[string]time.Time
}

// New builds a worker.
func New(cfg config.Config, pool *pgxpool.Pool, layout storage.Layout) *Worker {
	slots := cfg.WorkerSlots
	if slots < 1 {
		slots = 1
	}
	return &Worker{
		cfg: cfg, pool: pool, store: jobs.NewStore(pool), gpu: gpu.NewStore(pool, cfg.LeaseTTL), layout: layout,
		Poll: 2 * time.Second, Slots: slots, waiting: map[string]time.Time{},
	}
}

// Run drives the intake and resume loops and the sweepers until ctx is
// cancelled, then waits for the jobs in flight to stop.
func (w *Worker) Run(ctx context.Context) error {
	slog.Info("rk worker", "data_dir", w.layout.Root, "slots", w.Slots, "local_demucs", w.cfg.LocalDemucs, "max_duration", w.cfg.MaxDuration)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.sweep(ctx) }()
	go func() { defer wg.Done(); w.resumeLoop(ctx) }()
	w.intakeLoop(ctx, &wg)
	wg.Wait()
	return nil
}

// intakeLoop claims pending jobs into free slots.
func (w *Worker) intakeLoop(ctx context.Context, wg *sync.WaitGroup) {
	slots := make(chan struct{}, w.Slots)
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case slots <- struct{}{}:
		}
		j, err := jobs.Claim(ctx, w.pool)
		if err != nil {
			<-slots
			if !errors.Is(err, jobs.ErrNoJobs) && ctx.Err() == nil {
				slog.Error("worker: claim", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(w.Poll):
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			w.intake(ctx, j)
		}()
	}
}

// RunOnce claims one pending job and runs its CPU stages up to the GPU
// hand-off (or, with local demucs, up to `separating` as well; the resume
// loop runs demucs). Returns false when the queue is empty.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	j, err := jobs.Claim(ctx, w.pool)
	if errors.Is(err, jobs.ErrNoJobs) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	w.intake(ctx, j)
	return true, nil
}

// intake runs a freshly claimed job under its lock up to `separating`.
func (w *Worker) intake(ctx context.Context, j *jobs.Job) {
	unlock, ok, err := w.store.TryLock(ctx, j.ID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("worker: lock", "job", j.ID, "err", err)
		}
		return
	}
	if !ok {
		// Claim moved it out of pending, so no worker should hold it; never
		// run without the lock, give it back to the queue instead.
		slog.Warn("worker: claimed job is locked elsewhere; re-queued", "job", j.ID)
		if err := jobs.Transition(ctx, w.pool, j.ID, jobs.StatusPending, 0, jobs.StatusMessage(jobs.StatusPending, 0)); err != nil && ctx.Err() == nil {
			slog.Error("worker: re-queue", "job", j.ID, "err", err)
		}
		return
	}
	defer unlock()
	w.process(ctx, j, true)
}

// resumeLoop runs ResumeOnce and the GPU-wait watchdog every Poll.
func (w *Worker) resumeLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if ids, err := w.SweepGPUWait(ctx); err != nil && ctx.Err() == nil {
			slog.Error("worker: gpu wait", "err", err)
		} else if len(ids) > 0 {
			slog.Warn("worker: no GPU runner took these jobs in time; failed", "jobs", ids)
		}
		ran, err := w.ResumeOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("worker: resume", "err", err)
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(w.Poll):
		}
	}
}

// ResumeOnce locks and runs to completion the oldest job past the GPU
// hand-off that nobody is processing (`finalizing`, `packaging`; with local
// demucs also lease-less `separating`). Returns false when there is none.
func (w *Worker) ResumeOnce(ctx context.Context) (bool, error) {
	ids, err := w.store.Resumable(ctx, w.cfg.LocalDemucs, resumeBatch)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		unlock, ok, err := w.store.TryLock(ctx, id)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		ran := func() bool {
			defer unlock()
			// Re-read under the lock: the previous holder may just have
			// finished it.
			j, err := w.store.Get(ctx, id)
			if err != nil {
				return false
			}
			if j.Status != jobs.StatusFinalizing && j.Status != jobs.StatusPackaging &&
				!(w.cfg.LocalDemucs && j.Status == jobs.StatusSeparating) {
				return false
			}
			slog.Info("worker: resuming job", "job", id, "status", j.Status)
			w.process(ctx, j, false)
			return true
		}()
		if ran {
			return true, nil
		}
	}
	return false, nil
}

// SweepGPUWait fails jobs that sat in `separating` without any runner
// leasing them for GPUWaitTimeout, counted from when this process first
// saw them unleased (a lease, even a failed one, restarts the clock).
// Returns the ids it failed. No-op in local demucs mode.
func (w *Worker) SweepGPUWait(ctx context.Context) ([]string, error) {
	if w.cfg.LocalDemucs {
		return nil, nil
	}
	list, err := w.gpu.WaitingJobs(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var expired []string
	w.mu.Lock()
	seen := make(map[string]bool, len(list))
	for _, wj := range list {
		seen[wj.JobID] = true
		if wj.Leased {
			delete(w.waiting, wj.JobID)
			continue
		}
		since, ok := w.waiting[wj.JobID]
		if !ok {
			w.waiting[wj.JobID] = now
			continue
		}
		if now.Sub(since) > w.cfg.GPUWaitTimeout {
			expired = append(expired, wj.JobID)
		}
	}
	for id := range w.waiting {
		if !seen[id] {
			delete(w.waiting, id)
		}
	}
	w.mu.Unlock()
	var failed []string
	for _, id := range expired {
		msg := fmt.Sprintf("Stem separation failed: no GPU runner picked up the job within %s", w.cfg.GPUWaitTimeout)
		err := jobs.Transition(ctx, w.pool, id, jobs.StatusFailed, 0, msg)
		if err != nil && !errors.Is(err, jobs.ErrTerminal) && !errors.Is(err, jobs.ErrNotFound) {
			return failed, err
		}
		w.mu.Lock()
		delete(w.waiting, id)
		w.mu.Unlock()
		failed = append(failed, id)
	}
	return failed, nil
}

// Process runs the pipeline for j from its current status to a terminal
// status (or until the worker shuts down). In GPU mode a job that reaches
// `separating` this way is still handed off, since only a runner can
// separate it. Process does not take the job lock; Run's loops do.
func (w *Worker) Process(ctx context.Context, j *jobs.Job) {
	w.process(ctx, j, false)
}

// process runs the stages from j's current status. With handoff set the run
// stops as soon as the job is in `separating` (the intake path); otherwise
// it continues to a terminal status (the resume path).
func (w *Worker) process(ctx context.Context, j *jobs.Job, handoff bool) {
	log := slog.With("job", j.ID, "quality", j.Quality)
	jctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.watchCancel(jctx, j.ID, cancel)

	r, err := w.newRun(j)
	if err != nil {
		w.fail(ctx, j.ID, "Processing failed: "+err.Error())
		return
	}
	r.handoff = handoff
	start := time.Now()
	err = r.run(jctx)
	switch {
	case err == nil && r.handedOff:
		log.Info("job handed to the GPU queue", "took", time.Since(start).Round(time.Second))
	case err == nil:
		log.Info("job completed", "took", time.Since(start).Round(time.Second))
	case errors.Is(err, errJobEnded):
		log.Info("job ended elsewhere (cancelled or failed)")
	case ctx.Err() != nil:
		// Worker shutdown mid-stage: put early-stage jobs back in the queue.
		bg, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if st, _ := w.store.Status(bg, j.ID); st == jobs.StatusConverting || st == jobs.StatusAnalyzing {
			if terr := jobs.Transition(bg, w.pool, j.ID, jobs.StatusPending, 0, jobs.StatusMessage(jobs.StatusPending, 0)); terr == nil {
				log.Info("worker shutting down; job re-queued", "from", st)
				return
			}
		}
		log.Warn("worker shutting down mid-job", "err", err)
	default:
		log.Error("job failed", "err", err)
		w.fail(ctx, j.ID, userMessage(err))
	}
}

func (w *Worker) fail(ctx context.Context, id, msg string) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := jobs.Transition(bg, w.pool, id, jobs.StatusFailed, 0, msg); err != nil && !errors.Is(err, jobs.ErrTerminal) {
		slog.Error("worker: mark failed", "job", id, "err", err)
	}
}

// userMessage turns a stage error into the text stored as the job error.
func userMessage(err error) string {
	msg := err.Error()
	msg = strings.ToValidUTF8(msg, "")
	if len(msg) > 1000 {
		msg = msg[:1000] + "…"
	}
	return msg
}

// watchCancel polls the job status and cancels the run when the job
// becomes terminal behind the worker's back (user cancel, GPU failure).
func (w *Worker) watchCancel(ctx context.Context, id string, cancel context.CancelFunc) {
	t := time.NewTicker(w.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, err := w.store.Status(ctx, id)
		if err != nil {
			if errors.Is(err, jobs.ErrNotFound) {
				cancel()
				return
			}
			continue
		}
		if jobs.IsTerminal(st) {
			cancel()
			return
		}
	}
}

// sweep runs the periodic housekeeping until ctx is done.
func (w *Worker) sweep(ctx context.Context) {
	lease := time.NewTicker(LeaseSweepInterval)
	stale := time.NewTicker(StaleSweepInterval)
	retention := time.NewTicker(RetentionSweepInterval)
	defer lease.Stop()
	defer stale.Stop()
	defer retention.Stop()
	w.SweepRetention(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-lease.C:
			if ids, err := w.gpu.ExpireStale(ctx); err != nil && ctx.Err() == nil {
				slog.Error("sweep: leases", "err", err)
			} else if len(ids) > 0 {
				slog.Warn("sweep: expired GPU leases", "leases", ids)
			}
		case <-stale.C:
			if ids, err := w.store.FailStale(ctx, StaleAfter); err != nil && ctx.Err() == nil {
				slog.Error("sweep: stale", "err", err)
			} else if len(ids) > 0 {
				slog.Warn("sweep: failed stalled jobs", "jobs", ids)
			}
		case <-retention.C:
			w.SweepRetention(ctx)
		}
	}
}

// SweepRetention deletes jobs past expires_at with their files.
func (w *Worker) SweepRetention(ctx context.Context) {
	ids, err := w.store.DeleteExpired(ctx, time.Now())
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("sweep: retention", "err", err)
		}
		return
	}
	for _, id := range ids {
		if err := w.layout.RemoveJob(id); err != nil {
			slog.Warn("sweep: remove job dir", "job", id, "err", err)
		}
	}
	if len(ids) > 0 {
		slog.Info("sweep: deleted expired jobs", "count", len(ids))
	}
}

// errJobEnded means the job reached a terminal state through another
// path (cancel, GPU failure policy) while this run was in progress.
var errJobEnded = errors.New("job ended")

// transition wraps jobs.Transition, mapping ErrTerminal to errJobEnded.
func (w *Worker) transition(ctx context.Context, id, status string, progress int16, msg string) error {
	err := jobs.Transition(ctx, w.pool, id, status, progress, msg)
	if errors.Is(err, jobs.ErrTerminal) || errors.Is(err, jobs.ErrNotFound) {
		return errJobEnded
	}
	if err != nil {
		return fmt.Errorf("transition to %s: %w", status, err)
	}
	return nil
}
