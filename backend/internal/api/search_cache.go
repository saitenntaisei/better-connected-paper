package api

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultSearchTTL is how long a /api/search result stays memoized.
// 15 minutes is long enough to absorb user retries/tab reloads without
// giving stale abstract text for the length of a local session.
const defaultSearchTTL = 15 * time.Minute

// maxSearchCacheEntries caps memory use. Search query space is unbounded,
// so we need a ceiling; 200 distinct queries covers any plausible local
// session while staying trivially small in RAM.
const maxSearchCacheEntries = 200

type searchCacheEntry struct {
	value   searchResponse
	expires time.Time
}

// searchCache is a tiny mutex-guarded TTL map. Not LRU — a simple random
// eviction when full is sufficient for a cache whose working set is
// small and whose miss cost is a few S2 calls.
type searchCache struct {
	ttl     time.Duration
	maxSize int

	mu      sync.Mutex
	entries map[string]searchCacheEntry
}

func newSearchCache() *searchCache {
	return &searchCache{
		ttl:     defaultSearchTTL,
		maxSize: maxSearchCacheEntries,
		entries: make(map[string]searchCacheEntry, maxSearchCacheEntries),
	}
}

func searchCacheKey(q string, limit int) string {
	return strings.ToLower(strings.TrimSpace(q)) + "|" + strconv.Itoa(limit)
}

func (c *searchCache) get(key string) (searchResponse, bool) {
	if c == nil {
		return searchResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return searchResponse{}, false
	}
	if time.Now().After(entry.expires) {
		delete(c.entries, key)
		return searchResponse{}, false
	}
	return entry.value, true
}

func (c *searchCache) put(key string, v searchResponse) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxSize {
		// Drop any one entry — map iteration order is randomized in Go,
		// which is good enough for a bounded-size sacrifice eviction.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = searchCacheEntry{value: v, expires: time.Now().Add(c.ttl)}
}
