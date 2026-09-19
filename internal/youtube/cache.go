package youtube

import (
	"container/list"
	"sync"
	"time"
)

// cache is a small mutex-guarded LRU with per-entry expiry, keyed by video
// id. Only successful previews are stored.
type cache struct {
	mu   sync.Mutex
	max  int
	ttl  time.Duration
	now  func() time.Time
	ll   *list.List
	byID map[string]*list.Element
}

type cacheEntry struct {
	id      string
	value   *Preview
	expires time.Time
}

func newCache(max int, ttl time.Duration, now func() time.Time) *cache {
	if now == nil {
		now = time.Now
	}
	return &cache{max: max, ttl: ttl, now: now, ll: list.New(), byID: map[string]*list.Element{}}
}

func (c *cache) get(id string) (*Preview, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byID[id]
	if !ok {
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if !c.now().Before(e.expires) {
		c.ll.Remove(el)
		delete(c.byID, id)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return e.value, true
}

func (c *cache) put(id string, v *Preview) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byID[id]; ok {
		e := el.Value.(*cacheEntry)
		e.value, e.expires = v, c.now().Add(c.ttl)
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&cacheEntry{id: id, value: v, expires: c.now().Add(c.ttl)})
	c.byID[id] = el
	for c.ll.Len() > c.max {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.byID, last.Value.(*cacheEntry).id)
	}
}

func (c *cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
