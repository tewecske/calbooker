package service

import (
	"context"
	"sync"
	"time"
)

type memoryRateLimitBucket struct {
	action  string
	hint    string
	started time.Time
	expires time.Time
	count   int
}

// memoryRateLimitStore is the single-process RateLimitStore. It is bounded by
// MaxKeys: when full, the oldest key that is not at its limit is evicted, so
// flooding it with fresh keys cannot lock every other caller out, while keys at
// their limit are kept (evicting them would reset an active block).
type memoryRateLimitStore struct {
	mu      sync.Mutex
	limit   int
	maxKeys int
	buckets map[string]memoryRateLimitBucket
}

func newMemoryRateLimitStore(config RateLimitConfig) *memoryRateLimitStore {
	return &memoryRateLimitStore{limit: config.Limit, maxKeys: config.MaxKeys, buckets: make(map[string]memoryRateLimitBucket)}
}

func rateLimitCompositeKey(action, key string) string { return action + "\x00" + key }

func (m *memoryRateLimitStore) Hit(_ context.Context, action, key, hint string, window time.Duration, now time.Time) (RateLimitHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpired(now)
	composite := rateLimitCompositeKey(action, key)
	bucket, exists := m.buckets[composite]
	if !exists {
		if len(m.buckets) >= m.maxKeys && !m.evictOldestUnblocked() {
			return RateLimitHit{}, ErrRateLimitCapacity
		}
		bucket = memoryRateLimitBucket{action: action, hint: hint, started: now, expires: now.Add(window)}
	}
	bucket.count++
	m.buckets[composite] = bucket
	return RateLimitHit{Count: bucket.count, Started: bucket.started}, nil
}

func (m *memoryRateLimitStore) Get(_ context.Context, action, key string, now time.Time) (RateLimitHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket, exists := m.buckets[rateLimitCompositeKey(action, key)]
	if !exists || !now.Before(bucket.expires) {
		return RateLimitHit{}, nil
	}
	return RateLimitHit{Count: bucket.count, Started: bucket.started}, nil
}

func (m *memoryRateLimitStore) Clear(_ context.Context, action, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.buckets, rateLimitCompositeKey(action, key))
	return nil
}

func (m *memoryRateLimitStore) List(_ context.Context, now time.Time) ([]RateLimitStoredBucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpired(now)
	buckets := make([]RateLimitStoredBucket, 0, len(m.buckets))
	for _, bucket := range m.buckets {
		buckets = append(buckets, RateLimitStoredBucket{Action: bucket.action, KeyHint: bucket.hint, Count: bucket.count, Started: bucket.started})
	}
	return buckets, nil
}

func (m *memoryRateLimitStore) ClearAction(_ context.Context, action string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for key, bucket := range m.buckets {
		if bucket.action == action {
			delete(m.buckets, key)
			removed++
		}
	}
	return removed, nil
}

func (m *memoryRateLimitStore) ClearAll(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := len(m.buckets)
	m.buckets = make(map[string]memoryRateLimitBucket)
	return removed, nil
}

func (m *memoryRateLimitStore) pruneExpired(now time.Time) {
	for key, bucket := range m.buckets {
		if !now.Before(bucket.expires) {
			delete(m.buckets, key)
		}
	}
}

func (m *memoryRateLimitStore) evictOldestUnblocked() bool {
	oldestKey := ""
	var oldest time.Time
	for key, bucket := range m.buckets {
		if bucket.count < m.limit && (oldestKey == "" || bucket.started.Before(oldest)) {
			oldestKey, oldest = key, bucket.started
		}
	}
	if oldestKey == "" {
		return false
	}
	delete(m.buckets, oldestKey)
	return true
}
