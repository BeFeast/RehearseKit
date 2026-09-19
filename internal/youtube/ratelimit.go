package youtube

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter is a per-key token bucket: burst tokens, refilled at rate/second.
// Idle buckets are swept lazily so the map does not grow with every IP that
// ever called the endpoint.
type limiter struct {
	mu      sync.Mutex
	burst   float64
	rate    float64 // tokens per second
	now     func() time.Time
	buckets map[string]*bucket
	sweptAt time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(burst int, per time.Duration, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{
		burst:   float64(burst),
		rate:    float64(burst) / per.Seconds(),
		now:     now,
		buckets: map[string]*bucket{},
		sweptAt: now(),
	}
}

// allow takes one token for key. When the bucket is empty it returns false
// and the wait until the next token.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}
	l.maybeSweep(now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// maybeSweep drops buckets that have been idle long enough to be full again,
// i.e. indistinguishable from a fresh one. Runs at most once a minute and is
// called with the mutex held.
func (l *limiter) maybeSweep(now time.Time) {
	if now.Sub(l.sweptAt) < time.Minute {
		return
	}
	l.sweptAt = now
	idle := time.Duration(l.burst/l.rate*float64(time.Second)) + time.Minute
	for k, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, k)
		}
	}
}

// clientIP identifies the caller for rate limiting. Behind a reverse proxy
// (the deployment shape; auth already trusts X-Forwarded-Proto) the proxy
// appends the real client to X-Forwarded-For, so the rightmost entry is the
// address the proxy saw. Without those headers the socket peer is used.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
