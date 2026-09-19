package jobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NotifyChannel is the Postgres NOTIFY channel; the payload is the job id.
const NotifyChannel = "job_events"

// Claim takes the oldest pending job, moves it to converting and returns it.
// Concurrent claimers never receive the same job (FOR UPDATE SKIP LOCKED).
// Returns ErrNoJobs when the queue is empty.
func Claim(ctx context.Context, pool *pgxpool.Pool) (*Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	j, err := scanJob(tx.QueryRow(ctx, `UPDATE jobs SET status = 'converting', started_at = now(), stage_progress = 0
		WHERE id = (SELECT id FROM jobs WHERE status = 'pending' ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING `+jobColumns))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoJobs
	}
	if err != nil {
		return nil, err
	}
	if err := emit(ctx, tx, j.ID, StatusConverting, 0, StatusMessage(StatusConverting, 0)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return j, nil
}

// Transition moves a job to status with the given progress, records a
// job_event and NOTIFYs listeners. Terminal statuses set completed_at; a
// failed transition stores message as the job error. Once a job is
// terminal (completed, failed or cancelled) every further transition
// returns ErrTerminal; a worker that finds its job cancelled simply stops.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, status string, progress int16, message string) error {
	if !ValidStatus(status) {
		return ErrInvalidStatus
	}
	if progress < 0 {
		progress = 0
	} else if progress > 100 {
		progress = 100
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var current string
	err = tx.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if IsTerminal(current) {
		return ErrTerminal
	}
	var errText *string
	if status == StatusFailed {
		errText = &message
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET status = $2, stage_progress = $3,
		error = CASE WHEN $2 = 'failed' THEN $4 ELSE error END,
		started_at = CASE WHEN started_at IS NULL AND $2 <> 'pending' THEN now() ELSE started_at END,
		completed_at = CASE WHEN $2 IN ('completed', 'failed', 'cancelled') THEN now() ELSE completed_at END
		WHERE id = $1`, id, status, progress, errText); err != nil {
		return err
	}
	if err := emit(ctx, tx, id, status, progress, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Cancel moves a non-terminal job to cancelled. Returns ErrTerminal when
// the job has already finished.
func Cancel(ctx context.Context, pool *pgxpool.Pool, id string) error {
	return Transition(ctx, pool, id, StatusCancelled, 0, "cancelled by user")
}
