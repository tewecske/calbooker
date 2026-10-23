//go:build integration

package postgres

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	testpostgres "github.com/tewecske/calbooker/tests/postgres"
)

func newRateLimitHarness(t *testing.T) (*testpostgres.Harness, context.Context) {
	t.Helper()
	harness := testpostgres.New(t)
	runner, err := store.NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	return harness, ctx
}

func TestTwoInstancesShareOneRateLimitBudget(t *testing.T) {
	harness, _ := newRateLimitHarness(t)
	config := service.RateLimitConfig{Limit: 3, Window: time.Minute}
	limiters := make([]*service.RateLimiter, 2)
	for i := range limiters {
		// Each instance has its own store value over the same database.
		store, err := NewRateLimitStore(harness.DB, "signin")
		if err != nil {
			t.Fatal(err)
		}
		if limiters[i], err = service.NewRateLimiterWithStore(config, store); err != nil {
			t.Fatal(err)
		}
	}
	// Five attempts alternate between the instances. A per-process limiter
	// would allow all five (at most three per instance would be refused).
	var allowed int
	for attempt := range 5 {
		decision, err := limiters[attempt%2].Allow("signin.identifier", "ada@example.test")
		if err != nil {
			t.Fatalf("Allow() error = %v", err)
		}
		if decision.Allowed {
			allowed++
		} else if decision.RetryAfter <= 0 || decision.RetryAfter > time.Minute {
			t.Fatalf("blocked RetryAfter = %v, want within the window", decision.RetryAfter)
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed %d of 5 attempts across two instances, want the shared limit of 3", allowed)
	}
	// The block is visible to, and clearable from, the other instance.
	snapshot, err := limiters[1].Snapshot("signin.identifier", "ada@example.test")
	if err != nil || snapshot.Count != 3 || snapshot.Limit != 3 {
		t.Fatalf("Snapshot() = %+v, %v; want count capped at the limit of 3", snapshot, err)
	}
	if locked := limiters[0].LockedCount(); locked != 1 {
		t.Fatalf("LockedCount() = %d, want 1", locked)
	}
	if err := limiters[1].Clear("signin.identifier", "ada@example.test"); err != nil {
		t.Fatal(err)
	}
	if decision, err := limiters[0].Allow("signin.identifier", "ada@example.test"); err != nil || !decision.Allowed {
		t.Fatalf("Allow() after Clear from the other instance = %+v, %v; want allowed", decision, err)
	}
}

func TestRateLimitStoreHitIsAtomicAndRestartsExpiredWindows(t *testing.T) {
	harness, ctx := newRateLimitHarness(t)
	limits, err := NewRateLimitStore(harness.DB, "signin")
	if err != nil {
		t.Fatal(err)
	}
	window := time.Minute
	start := time.UnixMilli(1_000_000)

	// Concurrent attempts never lose an increment.
	var wg sync.WaitGroup
	counts := make(chan int, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hit, err := limits.Hit(ctx, "signin.origin", "203.0.113.7", "2**********7", window, start)
			if err != nil {
				t.Error(err)
				return
			}
			counts <- hit.Count
		}()
	}
	wg.Wait()
	close(counts)
	seen := map[int]bool{}
	for count := range counts {
		seen[count] = true
	}
	if len(seen) != 20 {
		t.Fatalf("20 concurrent hits returned %d distinct counts, want 20 (a lost update)", len(seen))
	}

	// One millisecond before the window ends it is still the same window;
	// at the end it restarts with a fresh start time.
	hit, err := limits.Hit(ctx, "signin.origin", "203.0.113.7", "2**********7", window, start.Add(window-time.Millisecond))
	if err != nil || hit.Count != 21 || !hit.Started.Equal(start) {
		t.Fatalf("Hit(just inside) = %+v, %v; want count 21 in the original window", hit, err)
	}
	if got, err := limits.Get(ctx, "signin.origin", "203.0.113.7", start.Add(window)); err != nil || got.Count != 0 {
		t.Fatalf("Get(at expiry) = %+v, %v; want no live window", got, err)
	}
	restarted := start.Add(window)
	hit, err = limits.Hit(ctx, "signin.origin", "203.0.113.7", "2**********7", window, restarted)
	if err != nil || hit.Count != 1 || !hit.Started.Equal(restarted) {
		t.Fatalf("Hit(at expiry) = %+v, %v; want a restarted window of 1", hit, err)
	}
}

func TestRateLimitStoreScopesActionsAndNeverStoresKeysInTheClear(t *testing.T) {
	harness, ctx := newRateLimitHarness(t)
	signin, _ := NewRateLimitStore(harness.DB, "signin")
	guest, _ := NewRateLimitStore(harness.DB, "guest")
	now := time.UnixMilli(5_000_000)
	for _, hit := range []struct {
		store         *RateLimitStore
		action, email string
	}{
		{signin, "signin.identifier", "ada@example.test"},
		{signin, "signin.origin", "203.0.113.7"},
		{guest, "guest.create", "203.0.113.7"},
	} {
		if _, err := hit.store.Hit(ctx, hit.action, hit.email, "a***t", time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := signin.List(ctx, now)
	if err != nil || len(listed) != 2 {
		t.Fatalf("signin List() = %+v, %v; want only the two signin windows", listed, err)
	}
	if removed, err := signin.ClearAction(ctx, "signin.origin"); err != nil || removed != 1 {
		t.Fatalf("ClearAction() = %d, %v; want 1", removed, err)
	}
	if removed, err := signin.ClearAll(ctx); err != nil || removed != 1 {
		t.Fatalf("ClearAll() = %d, %v; want only the remaining signin window", removed, err)
	}
	if live, _ := guest.List(ctx, now); len(live) != 1 {
		t.Fatalf("guest windows after clearing signin = %+v, want 1 untouched", live)
	}

	// Keys are not stored in the clear: only a hash and the redacted hint.
	var hash []byte
	var hint string
	if err := harness.DB.QueryRowContext(ctx, `SELECT key_hash, key_hint FROM rate_limit_buckets WHERE scope = 'guest'`).Scan(&hash, &hint); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(hash, []byte("203.0.113.7")) || hint != "a***t" || len(hash) != 32 {
		t.Fatalf("stored hash/hint = %x / %q, want a SHA-256 hash and the redacted hint", hash, hint)
	}

	// Cleanup removes expired windows of every scope, and only those.
	if _, err := signin.Hit(ctx, "signin.identifier", "old@example.test", "o***t", time.Second, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, err := signin.DeleteExpired(ctx)
	if err != nil || removed < 1 {
		t.Fatalf("DeleteExpired() = %d, %v; want the expired window removed", removed, err)
	}
}
