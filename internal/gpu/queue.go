package gpu

import (
	"context"
	"time"

	"github.com/BeFeast/RehearseKit/internal/models"
)

// QueueStats is what an autoscaler needs to decide whether a GPU should be
// running: how many jobs wait for a runner and how many leases are active.
type QueueStats struct {
	// Waiting is the number of jobs in `separating` that have no active
	// lease and have not exhausted their attempts, i.e. what the next
	// POST /gpu/lease would be offered.
	Waiting int `json:"waiting"`
	// ActiveLeases is the number of leases currently held by runners.
	ActiveLeases int `json:"active_leases"`
	// WaitingTranscribe counts the waiting jobs that need a transcribe-capable
	// runner (a subset of Waiting).
	WaitingTranscribe int `json:"waiting_transcribe"`
	// WaitingInternal counts the waiting jobs whose recipe loads internal
	// weights, i.e. that only the internal runner image can take.
	WaitingInternal int `json:"waiting_internal"`
	// OldestWaitingAt is when the oldest waiting job entered the queue
	// (its started_at, or created_at); nil when nothing waits.
	OldestWaitingAt *time.Time `json:"oldest_waiting_at,omitempty"`
}

// QueueStats reports the separation queue as seen by Claim.
func (s *Store) QueueStats(ctx context.Context) (QueueStats, error) {
	var (
		st     QueueStats
		oldest *time.Time
	)
	err := s.pool.QueryRow(ctx, `WITH waiting AS (
			SELECT j.id, coalesce(j.started_at, j.created_at) AS since FROM jobs j
			WHERE j.status = 'separating'
			  AND NOT EXISTS (SELECT 1 FROM gpu_leases l WHERE l.job_id = j.id AND l.state = 'active')
			  AND (SELECT count(*) FROM gpu_leases l WHERE l.job_id = j.id AND l.state IN ('failed', 'expired')) < $1
		)
		SELECT (SELECT count(*) FROM waiting),
		       (SELECT min(since) FROM waiting),
		       (SELECT count(*) FROM gpu_leases WHERE state = 'active'),
		       (SELECT count(*) FROM waiting w JOIN jobs j ON j.id = w.id WHERE j.transcribe),
		       (SELECT count(*) FROM waiting w JOIN jobs j ON j.id = w.id WHERE NOT (`+jobModelSQL+` = ANY($2::text[])))`,
		s.MaxFailures, publicRecipes()).
		Scan(&st.Waiting, &oldest, &st.ActiveLeases, &st.WaitingTranscribe, &st.WaitingInternal)
	if err != nil {
		return QueueStats{}, err
	}
	st.OldestWaitingAt = oldest
	return st, nil
}

// publicRecipes lists the recipes a public runner may run.
func publicRecipes() []string {
	var out []string
	for _, r := range models.Recipes() {
		if r.Class() == models.Public {
			out = append(out, r.ID)
		}
	}
	return out
}
