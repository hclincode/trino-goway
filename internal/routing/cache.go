package routing

import (
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	defaultCacheSize      = 4096
	defaultExternalTimeout = 1_000_000_000 // 1s in nanoseconds as time.Duration
)

// queryCache is a bounded LRU mapping queryId → backend URL.
// singleflight for concurrent miss coalescing is handled in router.go.
type queryCache struct {
	lru *lru.Cache[string, string]
}

// newQueryCache creates a queryCache with the given capacity.
func newQueryCache(size int) (*queryCache, error) {
	if size <= 0 {
		size = defaultCacheSize
	}
	c, err := lru.New[string, string](size)
	if err != nil {
		return nil, err
	}
	return &queryCache{lru: c}, nil
}

// get returns the backend URL for queryID, or ("", false).
func (c *queryCache) get(queryID string) (string, bool) {
	return c.lru.Get(queryID)
}

// set stores queryID → backendURL in the cache.
func (c *queryCache) set(queryID, backendURL string) {
	c.lru.Add(queryID, backendURL)
}

// remove evicts queryID from the cache.
func (c *queryCache) remove(queryID string) {
	c.lru.Remove(queryID)
}
