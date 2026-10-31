package modupdates

import (
	"sync"
	"time"
)

// Bounded map whose entries expire after a fixed ttl
type ttlCache[K comparable, V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	now   func() time.Time
	items map[K]cacheItem[V]
}

type cacheItem[V any] struct {
	val     V
	expires time.Time
}

func newTTLCache[K comparable, V any](ttl time.Duration, max int) *ttlCache[K, V] {
	return &ttlCache[K, V]{ttl: ttl, max: max, now: time.Now, items: make(map[K]cacheItem[V])}
}

// Live value for key, expired entries read as missing
func (c *ttlCache[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || !c.now().Before(it.expires) {
		delete(c.items, key)
		var zero V
		return zero, false
	}
	return it.val, true
}

// Stores key, a full cache drops expired then soonest expiring entries
func (c *ttlCache[K, V]) put(key K, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, ok := c.items[key]; !ok && len(c.items) >= c.max {
		for k, it := range c.items {
			if !now.Before(it.expires) {
				delete(c.items, k)
			}
		}
		for len(c.items) >= c.max {
			var oldest K
			var oldestAt time.Time
			first := true
			for k, it := range c.items {
				if first || it.expires.Before(oldestAt) {
					oldest, oldestAt, first = k, it.expires, false
				}
			}
			delete(c.items, oldest)
		}
	}
	c.items[key] = cacheItem[V]{val: val, expires: now.Add(c.ttl)}
}

// Entry count including not yet swept expired ones
func (c *ttlCache[K, V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
