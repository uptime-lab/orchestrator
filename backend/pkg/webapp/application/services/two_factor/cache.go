package two_factor

import (
	"sync"
	"time"
)

// policyCacheTTL bounds how stale a cached policy answer can be. Short, because
// the only thing it costs to be wrong is one request cycle, and toggling the
// policy explicitly invalidates the entry anyway.
const policyCacheTTL = 30 * time.Second

type cacheEntry[V any] struct {
	value     V
	expiresAt time.Time
}

// ttlCache is a small expiring map. Deliberately not a general-purpose cache:
// it holds one boolean per account and never grows past the account count.
type ttlCache[K comparable, V any] struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[K]cacheEntry[V]
}

func newTTLCache[K comparable, V any](ttl time.Duration) *ttlCache[K, V] {
	return &ttlCache[K, V]{ttl: ttl, entries: make(map[K]cacheEntry[V])}
}

func (c *ttlCache[K, V]) get(key K) (V, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		var zero V
		return zero, false
	}
	return entry.value, true
}

func (c *ttlCache[K, V]) set(key K, value V) {
	c.mu.Lock()
	c.entries[key] = cacheEntry[V]{value: value, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *ttlCache[K, V]) delete(key K) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}
