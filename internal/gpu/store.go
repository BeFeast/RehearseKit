// Package gpu implements the pull-model GPU lease API: a runner
// authenticates with the runner token, leases the oldest job waiting in
// `separating`, downloads the source through a signed URL, uploads stems
// through signed PUT URLs, heartbeats progress and finally completes or
// fails the lease. Leases that stop heartbeating expire and the job is
// offered to the next runner; after MaxFailures attempts the job fails.
package gpu

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/jobs"
)

// Lease states (CHECK constraint in gpu_leases).
const (
	StateActive    = "active"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateExpired   = "expired"
)

// DefaultMaxFailures is how many failed/expired leases a job survives.
const DefaultMaxFailures = 3

// Errors returned by Store.
var (
	ErrNoJobs     = errors.New("gpu: no job is waiting for separation")
	ErrNotFound   = errors.New("gpu: lease not found")
	ErrNotActive  = errors.New("gpu: lease is no longer active")
	ErrWrongOwner = errors.New("gpu: lease belongs to another runner")
	ErrJobGone    = errors.New("gpu: job is no longer waiting for separation")
	ErrBadStems   = errors.New("gpu: stems do not match the job")
)

// Lease is a row of gpu_leases.
type Lease struct {
	ID          string    `json:"lease_id"`
	JobID       string    `json:"job_id"`
	RunnerID    string    `json:"runner_id"`
	LeasedAt    time.Time `json:"leased_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	State       string    `json:"state"`
}

// StemReport is one entry of a complete request.
type StemReport struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Store runs the lease queries.
type Store struct {
	pool        *pgxpool.Pool
	jobs        *jobs.Store
	TTL         time.Duration
	MaxFailures int
}

// NewStore wraps a pool. ttl is the heartbeat deadline for a lease.
func NewStore(pool *pgxpool.Pool, ttl time.Duration) *Store {
	return &Store{pool: pool, jobs: jobs.NewStore(pool), TTL: ttl, MaxFailures: DefaultMaxFailures}
}

const leaseColumns = `id, job_id, runner_id, leased_at, heartbeat_at, expires_at, state`

func scanLease(row pgx.Row) (*Lease, error) {
	var l Lease
	err := row.Scan(&l.ID, &l.JobID, &l.RunnerID, &l.LeasedAt, &l.HeartbeatAt, &l.ExpiresAt, &l.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// Get loads a lease.
func (s *Store) Get(ctx context.Context, id string) (*Lease, error) {
	return scanLease(s.pool.QueryRow(ctx, `SELECT `+leaseColumns+` FROM gpu_leases WHERE id = $1`, id))
}

// Failures counts the failed and expired leases of a job.
func (s *Store) Failures(ctx context.Context, jobID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM gpu_leases WHERE job_id = $1 AND state IN ('failed', 'expired')`, jobID).Scan(&n)
	return n, err
}

// Claim leases the oldest job that is in `separating`, has no active lease
// and has not exhausted its attempts. Concurrent runners never get the
// same job: the jobs row is taken FOR UPDATE SKIP LOCKED and the partial
// unique index gpu_leases_one_active_idx rejects a second active lease
// (the NOT EXISTS check alone is not re-evaluated after a lock wait), in
// which case the claim is retried. Returns ErrNoJobs when nothing waits.
func (s *Store) Claim(ctx context.Context, runnerID string) (*Lease, *jobs.Job, error) {
	var (
		l     *Lease
		jobID string
	)
	for attempt := 0; ; attempt++ {
		var err error
		l, jobID, err = s.tryClaim(ctx, runnerID)
		if err == nil {
			break
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && attempt < 5 {
			continue // lost the race for this job; pick the next one
		}
		return nil, nil, err
	}
	j, err := s.jobs.Get(ctx, jobID)
	if err != nil {
		return nil, nil, err
	}
	p := jobs.StageStart(jobs.StatusSeparating)
	if err := jobs.Transition(ctx, s.pool, jobID, jobs.StatusSeparating, p, jobs.StatusMessage(jobs.StatusSeparating, p)); err != nil {
		return nil, nil, err
	}
	return l, j, nil
}

func (s *Store) tryClaim(ctx context.Context, runnerID string) (*Lease, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var jobID string
	err = tx.QueryRow(ctx, `SELECT j.id FROM jobs j
		WHERE j.status = 'separating'
		  AND NOT EXISTS (SELECT 1 FROM gpu_leases l WHERE l.job_id = j.id AND l.state = 'active')
		  AND (SELECT count(*) FROM gpu_leases l WHERE l.job_id = j.id AND l.state IN ('failed', 'expired')) < $1
		ORDER BY j.started_at NULLS LAST, j.created_at, j.id
		FOR UPDATE OF j SKIP LOCKED LIMIT 1`, s.MaxFailures).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNoJobs
	}
	if err != nil {
		return nil, "", err
	}
	l, err := scanLease(tx.QueryRow(ctx, `INSERT INTO gpu_leases (id, job_id, runner_id, expires_at)
		VALUES (gen_random_uuid(), $1, $2, now() + $3::interval) RETURNING `+leaseColumns, jobID, runnerID, s.TTL))
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return l, jobID, nil
}

// active loads the lease and checks it is active and owned by runnerID.
func (s *Store) active(ctx context.Context, leaseID, runnerID string) (*Lease, error) {
	l, err := s.Get(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	if l.RunnerID != runnerID {
		return nil, ErrWrongOwner
	}
	if l.State != StateActive {
		return nil, ErrNotActive
	}
	return l, nil
}

// Heartbeat extends the lease and maps progress (0..1) onto the job's
// separating band (28–76 %). When the job has left `separating` (cancelled,
// or moved on) the lease is closed and ErrJobGone is returned so the
// runner stops.
func (s *Store) Heartbeat(ctx context.Context, leaseID, runnerID string, progress float64) (*Lease, error) {
	if _, err := s.active(ctx, leaseID, runnerID); err != nil {
		return nil, err
	}
	l, err := scanLease(s.pool.QueryRow(ctx, `UPDATE gpu_leases SET heartbeat_at = now(), expires_at = now() + $2::interval
		WHERE id = $1 AND state = 'active' RETURNING `+leaseColumns, leaseID, s.TTL))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotActive
	}
	if err != nil {
		return nil, err
	}
	status, current, err := s.jobs.Progress(ctx, l.JobID)
	if err != nil {
		return nil, err
	}
	if status != jobs.StatusSeparating {
		_, _ = s.pool.Exec(ctx, `UPDATE gpu_leases SET state = 'failed' WHERE id = $1 AND state = 'active'`, leaseID)
		return nil, ErrJobGone
	}
	p := jobs.StageProgress(jobs.StatusSeparating, progress)
	if p > current {
		if err := jobs.Transition(ctx, s.pool, l.JobID, jobs.StatusSeparating, p, jobs.StatusMessage(jobs.StatusSeparating, p)); err != nil {
			if errors.Is(err, jobs.ErrTerminal) {
				return nil, ErrJobGone
			}
			return nil, err
		}
	}
	return l, nil
}

// Verifier checks one uploaded stem on disk (size, checksum, WAV format).
type Verifier func(ctx context.Context, jobID string, st StemReport) error

// Complete validates the reported stems against the job's model, runs
// verify on each, moves the job to finalizing and closes the lease. On a
// verification error the lease stays active so the runner can re-upload.
func (s *Store) Complete(ctx context.Context, leaseID, runnerID string, reported []StemReport, verify Verifier) error {
	l, err := s.active(ctx, leaseID, runnerID)
	if err != nil {
		return err
	}
	j, err := s.jobs.Get(ctx, l.JobID)
	if err != nil {
		return err
	}
	if j.Status != jobs.StatusSeparating {
		_, _ = s.pool.Exec(ctx, `UPDATE gpu_leases SET state = 'failed' WHERE id = $1 AND state = 'active'`, leaseID)
		return ErrJobGone
	}
	_, want := jobs.ModelFor(j.Quality)
	got := map[string]StemReport{}
	for _, st := range reported {
		got[st.Name] = st
	}
	if len(got) != len(want) {
		return fmt.Errorf("%w: expected %v, got %d entries", ErrBadStems, want, len(got))
	}
	for _, name := range want {
		st, ok := got[name]
		if !ok {
			return fmt.Errorf("%w: missing %s", ErrBadStems, name)
		}
		if verify != nil {
			if err := verify(ctx, j.ID, st); err != nil {
				return fmt.Errorf("%w: %s: %v", ErrBadStems, name, err)
			}
		}
	}
	p := jobs.StageStart(jobs.StatusFinalizing)
	if err := jobs.Transition(ctx, s.pool, j.ID, jobs.StatusFinalizing, p, jobs.StatusMessage(jobs.StatusFinalizing, p)); err != nil {
		if errors.Is(err, jobs.ErrTerminal) {
			return ErrJobGone
		}
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE gpu_leases SET state = 'completed', heartbeat_at = now() WHERE id = $1`, leaseID)
	return err
}

// Fail closes the lease as failed. The job goes back to waiting unless it
// has now failed MaxFailures times, in which case it is marked failed.
// Returns whether the job was failed for good.
func (s *Store) Fail(ctx context.Context, leaseID, runnerID, reason string) (bool, error) {
	l, err := s.active(ctx, leaseID, runnerID)
	if err != nil {
		return false, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE gpu_leases SET state = 'failed', heartbeat_at = now() WHERE id = $1`, leaseID); err != nil {
		return false, err
	}
	return s.afterFailure(ctx, l.JobID, reason)
}

// afterFailure fails the job when its attempts are used up, otherwise
// records a progress event saying it is waiting for another runner.
func (s *Store) afterFailure(ctx context.Context, jobID, reason string) (bool, error) {
	n, err := s.Failures(ctx, jobID)
	if err != nil {
		return false, err
	}
	if reason == "" {
		reason = "unknown error"
	}
	var msg string
	failed := n >= s.MaxFailures
	if failed {
		msg = fmt.Sprintf("Stem separation failed after %d attempts: %s", n, reason)
		err = jobs.Transition(ctx, s.pool, jobID, jobs.StatusFailed, 0, msg)
	} else {
		p := jobs.StageStart(jobs.StatusSeparating)
		msg = fmt.Sprintf("GPU separation failed (attempt %d of %d), waiting for another runner: %s", n, s.MaxFailures, reason)
		err = jobs.Transition(ctx, s.pool, jobID, jobs.StatusSeparating, p, msg)
	}
	if err != nil && !errors.Is(err, jobs.ErrTerminal) && !errors.Is(err, jobs.ErrNotFound) {
		return failed, err
	}
	return failed, nil
}

// ExpireStale marks active leases past expires_at as expired and applies
// the failure policy to their jobs. Returns the expired lease ids.
func (s *Store) ExpireStale(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `UPDATE gpu_leases SET state = 'expired' WHERE state = 'active' AND expires_at < now() RETURNING id, job_id`)
	if err != nil {
		return nil, err
	}
	type pair struct{ lease, job string }
	var stale []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.lease, &p.job); err != nil {
			rows.Close()
			return nil, err
		}
		stale = append(stale, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var ids []string
	for _, p := range stale {
		ids = append(ids, p.lease)
		if _, err := s.afterFailure(ctx, p.job, "the GPU runner stopped sending heartbeats"); err != nil {
			return ids, err
		}
	}
	return ids, nil
}
