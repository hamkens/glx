// Package cache provides a tiny concurrency-safe TTL cache used to keep the
// TUI responsive: re-entering a view returns the last result immediately while
// an explicit refresh bypasses the cache.
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	val     V
	expires time.Time
}

// Cache is a generic key/value store with per-entry TTL.
type Cache[K comparable, V any] struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[K]entry[V]
}

// New returns a cache whose entries live for ttl.
func New[K comparable, V any](ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{ttl: ttl, m: make(map[K]entry[V])}
}

// Get returns the cached value for k and whether it was present and unexpired.
// now is injected so the caller (and tests) control the clock; pass time.Now().
func (c *Cache[K, V]) Get(k K, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || now.After(e.expires) {
		var zero V
		return zero, false
	}
	return e.val, true
}

// Set stores v for k, expiring ttl after now.
func (c *Cache[K, V]) Set(k K, v V, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = entry[V]{val: v, expires: now.Add(c.ttl)}
}

// Invalidate removes k from the cache.
func (c *Cache[K, V]) Invalidate(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
}
