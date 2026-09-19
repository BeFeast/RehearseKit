package scaler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/gpu"
)

// Queue reports the separation queue.
type Queue interface {
	Stats(ctx context.Context) (gpu.QueueStats, error)
}

// ErrNoQueueEndpoint is returned by APIQueue when the server does not
// serve GET /api/v1/gpu/queue (a build older than this feature).
var ErrNoQueueEndpoint = errors.New("scaler: the server has no /api/v1/gpu/queue endpoint")

// APIQueue asks rk serve with the runner token.
type APIQueue struct {
	URL   string // base URL of rk serve
	Token string
	HTTP  *http.Client
}

// Stats implements Queue.
func (q *APIQueue) Stats(ctx context.Context) (gpu.QueueStats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(q.URL, "/")+"/api/v1/gpu/queue", nil)
	if err != nil {
		return gpu.QueueStats{}, err
	}
	req.Header.Set("Authorization", "Bearer "+q.Token)
	hc := q.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return gpu.QueueStats{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return gpu.QueueStats{}, ErrNoQueueEndpoint
	case resp.StatusCode != http.StatusOK:
		return gpu.QueueStats{}, fmt.Errorf("GET /api/v1/gpu/queue: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var st gpu.QueueStats
	if err := json.Unmarshal(body, &st); err != nil {
		return gpu.QueueStats{}, fmt.Errorf("GET /api/v1/gpu/queue: parse: %w", err)
	}
	return st, nil
}

// DBQueue reads the queue straight from Postgres (read-only queries).
type DBQueue struct {
	store *gpu.Store
}

// NewDBQueue wraps a pool. maxFailures mirrors the server's lease policy.
func NewDBQueue(pool *pgxpool.Pool, maxFailures int) *DBQueue {
	st := gpu.NewStore(pool, time.Minute)
	if maxFailures > 0 {
		st.MaxFailures = maxFailures
	}
	return &DBQueue{store: st}
}

// Stats implements Queue.
func (q *DBQueue) Stats(ctx context.Context) (gpu.QueueStats, error) { return q.store.QueueStats(ctx) }

// FallbackQueue prefers the API and, when the server predates the queue
// endpoint, switches to the database for the rest of the process.
type FallbackQueue struct {
	API      Queue
	DB       Queue // may be nil
	useDB    bool
	reported bool
}

// Stats implements Queue.
func (q *FallbackQueue) Stats(ctx context.Context) (gpu.QueueStats, error) {
	if q.useDB && q.DB != nil {
		return q.DB.Stats(ctx)
	}
	st, err := q.API.Stats(ctx)
	if errors.Is(err, ErrNoQueueEndpoint) && q.DB != nil {
		if !q.reported {
			slog.Warn("scaler: server has no /api/v1/gpu/queue; reading the queue from Postgres until it is upgraded")
			q.reported = true
		}
		q.useDB = true
		return q.DB.Stats(ctx)
	}
	return st, err
}
