package youtube

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterBurstAndRefill(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(10, time.Minute, clk.now)
	for i := 0; i < 10; i++ {
		if ok, _ := l.allow("ip1"); !ok {
			t.Fatalf("request %d of the burst denied", i+1)
		}
	}
	ok, wait := l.allow("ip1")
	if ok {
		t.Fatal("11th request within the window allowed")
	}
	if wait <= 0 || wait > 6*time.Second {
		t.Fatalf("wait=%v, want (0, 6s]", wait)
	}
	// A different key has its own bucket.
	if ok, _ := l.allow("ip2"); !ok {
		t.Fatal("second client denied by first client's bucket")
	}
	// One token every 6s at 10/min.
	clk.advance(5 * time.Second)
	if ok, _ := l.allow("ip1"); ok {
		t.Fatal("token refilled too early")
	}
	clk.advance(2 * time.Second)
	if ok, _ := l.allow("ip1"); !ok {
		t.Fatal("token not refilled after 6s")
	}
	// Refill never exceeds the burst.
	clk.advance(time.Hour)
	for i := 0; i < 10; i++ {
		if ok, _ := l.allow("ip1"); !ok {
			t.Fatalf("request %d after idle denied", i+1)
		}
	}
	if ok, _ := l.allow("ip1"); ok {
		t.Fatal("bucket overflowed its burst")
	}
}

func TestLimiterSweepsIdleBuckets(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(10, time.Minute, clk.now)
	l.allow("old")
	clk.advance(30 * time.Second)
	l.allow("recent")
	if len(l.buckets) != 2 {
		t.Fatalf("buckets=%d", len(l.buckets))
	}
	// idle threshold is burst/rate (60s) + 1min = 2min after last use.
	clk.advance(2*time.Minute + time.Second) // "old" idle 2m31s, "recent" 2m01s -> both past
	l.allow("trigger")
	if _, ok := l.buckets["old"]; ok {
		t.Fatal("idle bucket not swept")
	}
	if _, ok := l.buckets["recent"]; ok {
		t.Fatal("idle bucket 'recent' not swept")
	}
	if _, ok := l.buckets["trigger"]; !ok {
		t.Fatal("active bucket swept")
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		hdr    map[string]string
		want   string
	}{
		{"remote addr", "203.0.113.5:4321", nil, "203.0.113.5"},
		{"remote addr v6", "[2001:db8::1]:4321", nil, "2001:db8::1"},
		{"remote addr no port", "203.0.113.5", nil, "203.0.113.5"},
		{"xff single", "10.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		{"xff chain uses rightmost", "10.0.0.1:1", map[string]string{"X-Forwarded-For": "1.1.1.1, 2.2.2.2, 198.51.100.7"}, "198.51.100.7"},
		{"xff empty falls through", "10.0.0.1:1", map[string]string{"X-Forwarded-For": " ", "X-Real-IP": "198.51.100.8"}, "198.51.100.8"},
		{"x-real-ip", "10.0.0.1:1", map[string]string{"X-Real-IP": "198.51.100.9"}, "198.51.100.9"},
		{"loopback proxy", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		{"v6 loopback proxy", "[::1]:1", map[string]string{"X-Real-IP": "198.51.100.7"}, "198.51.100.7"},
		{"cgnat-ish 172.16", "172.16.4.4:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		{"public peer ignores xff", "203.0.113.5:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "203.0.113.5"},
		{"public peer ignores x-real-ip", "203.0.113.5:1", map[string]string{"X-Real-IP": "198.51.100.7"}, "203.0.113.5"},
		{"unix socket peer ignores xff", "@", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "@"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.RemoteAddr = tc.remote
			for k, v := range tc.hdr {
				r.Header.Set(k, v)
			}
			if got := clientIP(r); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
