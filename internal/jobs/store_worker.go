package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AudioInfo is what the analysing stage learns about the source.
type AudioInfo struct {
	BPM             *float64
	DurationSeconds float64
	SampleRate      int32
	Channels        int16
}

// SetAudioInfo records the analysed source properties on the job row.
func (s *Store) SetAudioInfo(ctx context.Context, id string, info AudioInfo) error {
	tag, err := s.pool.Exec(ctx, `UPDATE jobs SET detected_bpm = $2, duration_seconds = $3, sample_rate = $4, channels = $5 WHERE id = $1`,
		id, info.BPM, info.DurationSeconds, info.SampleRate, info.Channels)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Status returns just the job's current status (cheap; used by the
// worker's cancellation watcher).
func (s *Store) Status(ctx context.Context, id string) (string, error) {
	var st string
	err := s.pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, id).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return st, err
}

// Progress returns the job's status and stage_progress.
func (s *Store) Progress(ctx context.Context, id string) (status string, progress int16, err error) {
	err = s.pool.QueryRow(ctx, `SELECT status, stage_progress FROM jobs WHERE id = $1`, id).Scan(&status, &progress)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return status, progress, err
}

// DeleteExpired removes every job past expires_at (rows cascade to stems,
// events and leases) and returns the ids so the caller can remove the files.
func (s *Store) DeleteExpired(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `DELETE FROM jobs WHERE expires_at < $1 RETURNING id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FailStale marks CPU-stage jobs whose last event is older than maxAge as
// failed: their worker died mid-stage. Jobs waiting in `separating` are
// left alone (a GPU runner may be a long time coming; the lease sweeper
// covers that stage). Returns the ids it failed.
func (s *Store) FailStale(ctx context.Context, maxAge time.Duration) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM jobs j
		WHERE status IN ('converting', 'analyzing', 'finalizing', 'packaging')
		  AND NOT EXISTS (SELECT 1 FROM job_events e WHERE e.job_id = j.id AND e.at > now() - $1::interval)`, maxAge)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var failed []string
	for _, id := range ids {
		err := Transition(ctx, s.pool, id, StatusFailed, 0, "Processing stalled: the worker stopped responding")
		if err != nil && !errors.Is(err, ErrTerminal) && !errors.Is(err, ErrNotFound) {
			return failed, err
		}
		failed = append(failed, id)
	}
	return failed, nil
}

// lockNamespace is the first key of the per-job advisory lock; the second
// is hashtext(job id). It keeps job locks apart from the migration lock.
const lockNamespace = 0x524b4a4f // "RKJO"

// TryLock takes the job's processing lock: a session-level Postgres
// advisory lock held on a connection reserved from the pool until unlock
// is called. It is how workers (goroutines in one process, or several
// processes on one database) make sure a job is driven by exactly one of
// them; the lock disappears with the session, so a crashed worker never
// leaves a job locked. ok is false when another holder has it.
func (s *Store) TryLock(ctx context.Context, id string) (unlock func(), ok bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, hashtext($2))`, lockNamespace, id).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	unlock = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1, hashtext($2))`, lockNamespace, id).Scan(&released); err != nil || !released {
			// Drop the session rather than return a connection that may
			// still hold the lock.
			_ = conn.Hijack().Close(ctx)
			return
		}
		conn.Release()
	}
	return unlock, true, nil
}

// Resumable lists jobs a worker may pick up and drive to completion:
// finalizing and packaging (a runner or a previous worker left them there)
// and, when withSeparating is set (local demucs mode), separating jobs no
// runner holds a lease on. Oldest first. Whether one is already being
// processed is decided by TryLock, not here.
func (s *Store) Resumable(ctx context.Context, withSeparating bool, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT j.id FROM jobs j
		WHERE j.status IN ('finalizing', 'packaging')
		   OR ($1 AND j.status = 'separating'
		       AND NOT EXISTS (SELECT 1 FROM gpu_leases l WHERE l.job_id = j.id AND l.state = 'active'))
		ORDER BY j.started_at NULLS LAST, j.created_at, j.id LIMIT $2`, withSeparating, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
