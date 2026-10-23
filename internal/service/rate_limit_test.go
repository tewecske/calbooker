package service

import (
	"errors"
	"testing"
	"time"
)

func TestRateLimiterUsesIndependentBudgetsAndExpiry(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 2, Window: time.Minute, MaxKeys: 10})
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v, want nil", err)
	}
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }

	for range 2 {
		decision, err := limiter.Allow("signin", "user@example.com")
		if err != nil || !decision.Allowed {
			t.Fatalf("Allow() = %#v, %v; want allowed", decision, err)
		}
	}
	decision, err := limiter.Allow("signin", "user@example.com")
	if err != nil || decision.Allowed || decision.RetryAfter != time.Minute {
		t.Fatalf("blocked Allow() = %#v, %v; want blocked one-minute retry", decision, err)
	}
	decision, err = limiter.Allow("password-reset", "user@example.com")
	if err != nil || !decision.Allowed {
		t.Fatalf("independent action Allow() = %#v, %v; want allowed", decision, err)
	}
	now = now.Add(time.Minute)
	decision, err = limiter.Allow("signin", "user@example.com")
	if err != nil || !decision.Allowed {
		t.Fatalf("expired Allow() = %#v, %v; want allowed", decision, err)
	}
	if err := limiter.Clear("signin", "user@example.com"); err != nil {
		t.Fatalf("Clear() error = %v, want nil", err)
	}
}

func TestRateLimiterBoundsKeysAndConfig(t *testing.T) {
	if _, err := NewRateLimiter(RateLimitConfig{}); !errors.Is(err, ErrInvalidRateLimitConfig) {
		t.Fatalf("NewRateLimiter(zero) error = %v, want %v", err, ErrInvalidRateLimitConfig)
	}
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 1, Window: time.Minute, MaxKeys: 1})
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v, want nil", err)
	}
	if _, err := limiter.Allow("signin", ""); !errors.Is(err, ErrInvalidRateLimitKey) {
		t.Fatalf("Allow(empty key) error = %v, want %v", err, ErrInvalidRateLimitKey)
	}
	if _, err := limiter.Allow("signin", "first"); err != nil {
		t.Fatalf("Allow(first) error = %v, want nil", err)
	}
	if _, err := limiter.Allow("signin", "second"); !errors.Is(err, ErrRateLimitCapacity) {
		t.Fatalf("Allow(capacity) error = %v, want %v", err, ErrRateLimitCapacity)
	}
}

func TestRateLimiterAtCapacityEvictsOldestUnblockedKey(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 2, Window: time.Minute, MaxKeys: 3})
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v, want nil", err)
	}
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	allow := func(key string) (RateLimitDecision, error) {
		now = now.Add(time.Second)
		return limiter.Allow("signin", key)
	}

	// "blocked" is the oldest key, but it is at its limit and must survive.
	for _, key := range []string{"blocked", "blocked", "older", "newer"} {
		if _, err := allow(key); err != nil {
			t.Fatalf("Allow(%s) error = %v, want nil", key, err)
		}
	}
	if decision, err := allow("flood"); err != nil || !decision.Allowed {
		t.Fatalf("Allow(new key at capacity) = %#v, %v; want allowed", decision, err)
	}
	if decision, err := allow("blocked"); err != nil || decision.Allowed {
		t.Fatalf("Allow(blocked) = %#v, %v; want still blocked", decision, err)
	}
	if decision, err := allow("newer"); err != nil || !decision.Allowed || decision.Remaining != 0 {
		t.Fatalf("Allow(newer) = %#v, %v; want its existing budget kept", decision, err)
	}
	if decision, err := allow("older"); err != nil || !decision.Allowed || decision.Remaining != 1 {
		t.Fatalf("Allow(older) = %#v, %v; want a fresh budget after eviction", decision, err)
	}
}
