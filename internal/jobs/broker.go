package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Broker holds one dedicated LISTEN connection and fans job notifications
// out to SSE subscribers keyed by job id.
type Broker struct {
	pool *pgxpool.Pool

	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}

	readyOnce sync.Once
	ready     chan struct{}
}

// NewBroker creates a broker; call Run to start listening.
func NewBroker(pool *pgxpool.Pool) *Broker {
	return &Broker{pool: pool, subs: map[string]map[chan struct{}]struct{}{}, ready: make(chan struct{})}
}

// Ready is closed once the first LISTEN has been established.
func (b *Broker) Ready() <-chan struct{} { return b.ready }

// Subscribe registers interest in jobID. The returned channel receives a
// (coalesced) wake-up per NOTIFY; call cancel when done.
func (b *Broker) Subscribe(jobID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	m := b.subs[jobID]
	if m == nil {
		m = map[chan struct{}]struct{}{}
		b.subs[jobID] = m
	}
	m[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if m := b.subs[jobID]; m != nil {
			delete(m, ch)
			if len(m) == 0 {
				delete(b.subs, jobID)
			}
		}
		b.mu.Unlock()
	}
}

func (b *Broker) publish(jobID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[jobID] {
		select {
		case ch <- struct{}{}:
		default: // a wake-up is already pending
		}
	}
}

// Run blocks, listening on NotifyChannel until ctx is done. Connection
// failures are retried with backoff; subscribers also poll periodically so
// a dropped notification only delays delivery.
func (b *Broker) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := b.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("job broker: listener stopped, reconnecting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (b *Broker) listen(ctx context.Context) error {
	cfg := b.pool.Config().ConnConfig.Copy()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		return err
	}
	b.readyOnce.Do(func() { close(b.ready) })
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		b.publish(n.Payload)
	}
}
