package youtube

import (
	"fmt"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
func pv(id string) *Preview                  { return &Preview{VideoID: id, Title: "t-" + id} }
func mustGet(t *testing.T, c *cache, id string) *Preview {
	t.Helper()
	p, ok := c.get(id)
	if !ok {
		t.Fatalf("expected %q cached", id)
	}
	return p
}

func TestCacheHitMissAndTTL(t *testing.T) {
	clk := newFakeClock()
	c := newCache(100, 10*time.Minute, clk.now)
	if _, ok := c.get("a"); ok {
		t.Fatal("empty cache returned a hit")
	}
	c.put("a", pv("a"))
	if got := mustGet(t, c, "a"); got.Title != "t-a" {
		t.Fatal(got)
	}
	clk.advance(10*time.Minute - time.Second)
	mustGet(t, c, "a")
	clk.advance(2 * time.Second)
	if _, ok := c.get("a"); ok {
		t.Fatal("entry served past its TTL")
	}
	if c.len() != 0 {
		t.Fatalf("expired entry not evicted, len=%d", c.len())
	}
}

func TestCacheLRUEviction(t *testing.T) {
	clk := newFakeClock()
	c := newCache(3, time.Hour, clk.now)
	c.put("a", pv("a"))
	c.put("b", pv("b"))
	c.put("c", pv("c"))
	mustGet(t, c, "a") // a becomes most recent; b is now the oldest
	c.put("d", pv("d"))
	if c.len() != 3 {
		t.Fatalf("len=%d, want 3", c.len())
	}
	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted as least recently used")
	}
	for _, id := range []string{"a", "c", "d"} {
		mustGet(t, c, id)
	}
}

func TestCachePutRefreshesExisting(t *testing.T) {
	clk := newFakeClock()
	c := newCache(2, time.Minute, clk.now)
	c.put("a", pv("a"))
	clk.advance(50 * time.Second)
	c.put("a", &Preview{VideoID: "a", Title: "new"})
	clk.advance(50 * time.Second) // 100s since first put, 50s since refresh
	if got := mustGet(t, c, "a"); got.Title != "new" {
		t.Fatalf("title=%q", got.Title)
	}
	if c.len() != 1 {
		t.Fatalf("len=%d", c.len())
	}
}

func TestCacheBoundedUnderChurn(t *testing.T) {
	c := newCache(100, time.Hour, nil)
	for i := 0; i < 1000; i++ {
		c.put(fmt.Sprint(i), pv("x"))
	}
	if c.len() != 100 {
		t.Fatalf("len=%d, want 100", c.len())
	}
	if _, ok := c.get("0"); ok {
		t.Fatal("oldest entry survived")
	}
	mustGet(t, c, "999")
}
