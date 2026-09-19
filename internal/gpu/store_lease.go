package gpu

import (
	"context"
	"errors"
)

// ActiveLease returns the job's active lease, or nil when none exists.
func (s *Store) ActiveLease(ctx context.Context, jobID string) (*Lease, error) {
	l, err := scanLease(s.pool.QueryRow(ctx, `SELECT `+leaseColumns+` FROM gpu_leases
		WHERE job_id = $1 AND state = 'active' ORDER BY leased_at DESC LIMIT 1`, jobID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return l, err
}

// Waiting is a job in `separating` and whether a runner currently holds it.
type Waiting struct {
	JobID  string
	Leased bool
}

// WaitingJobs lists every job in `separating` with its lease state; the
// worker's GPU-wait watchdog uses it.
func (s *Store) WaitingJobs(ctx context.Context) ([]Waiting, error) {
	rows, err := s.pool.Query(ctx, `SELECT j.id,
			EXISTS (SELECT 1 FROM gpu_leases l WHERE l.job_id = j.id AND l.state = 'active')
		FROM jobs j WHERE j.status = 'separating' ORDER BY j.created_at, j.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Waiting
	for rows.Next() {
		var w Waiting
		if err := rows.Scan(&w.JobID, &w.Leased); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
