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
