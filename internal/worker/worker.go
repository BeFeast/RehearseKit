// Package worker is `rk worker`: it claims pending jobs and drives them
// through the CPU stages (converting, analyzing, finalizing, packaging),
// hands the separating stage to a GPU runner through the lease API (or
// runs demucs locally in development mode), and runs the housekeeping
// sweepers (expired leases, stalled jobs, retention).
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// Worker processes jobs.
type Worker struct {
	cfg    config.Config
	pool   *pgxpool.Pool
	store  *jobs.Store
	gpu    *gpu.Store
	layout storage.Layout

	// Poll is the queue polling and cancellation check interval.
	Poll time.Duration
	// Adopt makes Run pick up jobs left in separating/finalizing/packaging
	// by a previous worker process (single-worker deployments).
	Adopt bool
}

// New builds a worker.
func New(cfg config.Config, pool *pgxpool.Pool, layout storage.Layout) *Worker {
	return &Worker{
		cfg: cfg, pool: pool, store: jobs.NewStore(pool), gpu: gpu.NewStore(pool, cfg.LeaseTTL), layout: layout,
		Poll: 2 * time.Second, Adopt: true,
	}
}

// Run claims and processes jobs until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	slog.Info("rk worker", "data_dir", w.layout.Root, "local_demucs", w.cfg.LocalDemucs, "max_duration", w.cfg.MaxDuration)
	go w.sweep(ctx)
	if w.Adopt {
		w.adopt(ctx)
	}
	for ctx.Err() == nil {
		ran, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("worker: claim", "err", err)
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(w.Poll):
		}
	}
	return nil
}

// RunOnce claims one pending job and processes it. Returns false when the
// queue is empty.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	j, err := jobs.Claim(ctx, w.pool)
	if errors.Is(err, jobs.ErrNoJobs) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	w.Process(ctx, j)
	return true, nil
}

// adopt resumes jobs a previous worker left mid-pipeline.
func (w *Worker) adopt(ctx context.Context) {
	rows, err := w.pool.Query(ctx, `SELECT id FROM jobs WHERE status IN ('separating', 'finalizing', 'packaging') ORDER BY created_at`)
	if err != nil {
		slog.Error("worker: adopt", "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		j, err := w.store.Get(ctx, id)
		if err != nil {
			continue
		}
		slog.Info("worker: adopting job", "job", id, "status", j.Status)
		w.Process(ctx, j)
	}
}

// Process runs the pipeline for j from its current status onward. It
// returns when the job is terminal (or the worker is shutting down).
func (w *Worker) Process(ctx context.Context, j *jobs.Job) {
	log := slog.With("job", j.ID, "quality", j.Quality)
	jctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.watchCancel(jctx, j.ID, cancel)

	r, err := w.newRun(j)
	if err != nil {
		w.fail(ctx, j.ID, "Processing failed: "+err.Error())
		return
	}
	start := time.Now()
	err = r.run(jctx)
	switch {
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
