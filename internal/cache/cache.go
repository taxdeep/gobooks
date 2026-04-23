// 遵循project_guide.md
package cache

import (
	"container/list"
	"sync"
	"time"
)

// cacheEntry is the LRU-list payload. Holds the key (so eviction can
// remove the matching map entry) plus the value + expiry.
type cacheEntry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
}

// TTLCache is a thread-safe, in-memory key-value store with per-entry TTL
// and an optional hard cap on entry count. It is acceleration-only
// infrastructure: the authoritative data always lives in the database.
// Never treat a cache miss as a security boundary.
//
// Eviction policy:
//   - Per-entry TTL — entries expire after the configured duration; a
//     background goroutine sweeps expired rows every 5 minutes.
//   - Optional MaxEntries cap (set via NewBounded) — when exceeded,
//     the least-recently-WRITTEN entry is evicted on the next Set.
//     "Least-recently-written" rather than "least-recently-used"
//     intentionally: Get takes only an RLock and never mutates the LRU
//     order, so reads stay cheap. For the bookkeeping use case (TTL is
//     short, hot keys are repeatedly re-Set) this is indistinguishable
//     from true LRU but avoids RWMutex upgrade contention on hot reads.
//
// Type parameters:
//
//	K — comparable key type (e.g. string)
//	V — value type (any pointer or struct)
type TTLCache[K comparable, V any] struct {
	mu         sync.RWMutex
	data       map[K]*list.Element // value is *cacheEntry[K, V]
	lru        *list.List          // front = most recently set; back = oldest
	ttl        time.Duration
	maxEntries int // 0 = unbounded
	stop       chan struct{}
}

// New creates an unbounded TTLCache with the given TTL. Equivalent to
// NewBounded(ttl, 0). Existing call sites in tests and legacy code use
// this — new code that touches user-supplied keyspace should prefer
// NewBounded with an explicit cap to defend against adversarial growth.
func New[K comparable, V any](ttl time.Duration) *TTLCache[K, V] {
	return NewBounded[K, V](ttl, 0)
}

// NewBounded creates a TTLCache with both a TTL and a maximum entry
// count. When maxEntries > 0 and the cache hits that ceiling, the next
// Set evicts the least-recently-written entry to make room.
//
// Pass maxEntries=0 to opt out of the size cap (use New instead for
// readability). Pass a small number — say 1k–10k — for caches that key
// off user-controlled inputs (search query, filter combination) so a
// hostile or buggy client can't blow up the process heap.
//
// Starts a background cleanup goroutine that evicts expired entries
// every 5 minutes. Call Close() to stop it (e.g. in tests).
func NewBounded[K comparable, V any](ttl time.Duration, maxEntries int) *TTLCache[K, V] {
	c := &TTLCache[K, V]{
		data:       make(map[K]*list.Element),
		lru:        list.New(),
		ttl:        ttl,
		maxEntries: maxEntries,
		stop:       make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Get returns the value for key and true if the entry exists and has
// not expired. Does NOT update the LRU position — see the package
// comment for the rationale.
func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	elem, ok := c.data[key]
	c.mu.RUnlock()
	if !ok {
		var zero V
		return zero, false
	}
	e := elem.Value.(*cacheEntry[K, V])
	if time.Now().After(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores value under key with the cache's configured TTL and moves
// the entry to the front of the LRU list. If MaxEntries is configured
// and the cache would exceed it, evicts the back of the LRU list to
// make room — never deletes the key being set, even on update.
func (c *TTLCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.data[key]; ok {
		// Update in place + bump to front of LRU.
		e := elem.Value.(*cacheEntry[K, V])
		e.value = value
		e.expiresAt = time.Now().Add(c.ttl)
		c.lru.MoveToFront(elem)
		return
	}
	e := &cacheEntry[K, V]{key: key, value: value, expiresAt: time.Now().Add(c.ttl)}
	elem := c.lru.PushFront(e)
	c.data[key] = elem
	// Cap enforcement — evict back of LRU until we're under the limit.
	// Multiple evictions per Set are rare (only after a Flush + sudden
	// burst) but the loop keeps the invariant tight.
	for c.maxEntries > 0 && len(c.data) > c.maxEntries {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		o := oldest.Value.(*cacheEntry[K, V])
		delete(c.data, o.key)
		c.lru.Remove(oldest)
	}
}

// Delete removes a single key from the cache immediately.
func (c *TTLCache[K, V]) Delete(key K) {
	c.mu.Lock()
	if elem, ok := c.data[key]; ok {
		delete(c.data, key)
		c.lru.Remove(elem)
	}
	c.mu.Unlock()
}

// Flush removes all entries from the cache.
// Use this to invalidate after a write that affects many keys (e.g. company settings change).
func (c *TTLCache[K, V]) Flush() {
	c.mu.Lock()
	c.data = make(map[K]*list.Element)
	c.lru = list.New()
	c.mu.Unlock()
}

// Len returns the number of entries currently in the cache (including not-yet-evicted expired ones).
func (c *TTLCache[K, V]) Len() int {
	c.mu.RLock()
	n := len(c.data)
	c.mu.RUnlock()
	return n
}

// FlushWhere removes all entries for which match(key) returns true.
// It holds the write lock for the full scan — use sparingly on large caches.
func (c *TTLCache[K, V]) FlushWhere(match func(K) bool) {
	c.mu.Lock()
	for k, elem := range c.data {
		if match(k) {
			delete(c.data, k)
			c.lru.Remove(elem)
		}
	}
	c.mu.Unlock()
}

// Close stops the background cleanup goroutine. Safe to call more than once.
func (c *TTLCache[K, V]) Close() {
	select {
	case <-c.stop:
		// already closed
	default:
		close(c.stop)
	}
}

func (c *TTLCache[K, V]) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.evictExpired()
		case <-c.stop:
			return
		}
	}
}

func (c *TTLCache[K, V]) evictExpired() {
	now := time.Now()
	c.mu.Lock()
	for k, elem := range c.data {
		e := elem.Value.(*cacheEntry[K, V])
		if now.After(e.expiresAt) {
			delete(c.data, k)
			c.lru.Remove(elem)
		}
	}
	c.mu.Unlock()
}
