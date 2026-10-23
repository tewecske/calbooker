package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRateLimitedGuestServiceUsesSeparateBudgets(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 1, Window: time.Hour, MaxKeys: 10})
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v, want nil", err)
	}
	creator := &guestCreatorStub{}
	redeemer := &guestRedeemerStub{}
	service, err := NewRateLimitedGuestService(creator, redeemer, limiter)
	if err != nil {
		t.Fatalf("NewRateLimitedGuestService() error = %v, want nil", err)
	}

	if _, err := service.Create(context.Background(), "127.0.0.1", "dark"); err != nil {
		t.Fatalf("Create() first error = %v, want nil", err)
	}
	if _, err := service.Create(context.Background(), "127.0.0.1", "dark"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Create() second error = %v, want %v", err, ErrRateLimited)
	}
	if _, err := service.Redeem(context.Background(), "127.0.0.1", "ABCDEFGH23"); err != nil {
		t.Fatalf("Redeem() first error = %v, want nil", err)
	}
	if _, err := service.Redeem(context.Background(), "127.0.0.1", "ABCDEFGH23"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Redeem() second error = %v, want %v", err, ErrRateLimited)
	}
	if creator.calls != 1 || redeemer.calls != 1 {
		t.Fatalf("delegate calls = %d/%d, want 1/1", creator.calls, redeemer.calls)
	}
}

func TestRateLimitedGuestServiceGroupsIPv6OriginsByPrefix(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 1, Window: time.Hour, MaxKeys: 10})
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v, want nil", err)
	}
	service, err := NewRateLimitedGuestService(&guestCreatorStub{}, &guestRedeemerStub{}, limiter)
	if err != nil {
		t.Fatalf("NewRateLimitedGuestService() error = %v, want nil", err)
	}
	for _, step := range []struct {
		origin  string
		limited bool
	}{
		{origin: "2001:db8:1:2::1"},
		{origin: "2001:db8:1:2:ffff::9", limited: true}, // same /64
		{origin: "2001:db8:1:3::1"},                     // next /64
		{origin: "198.51.100.7"},
		{origin: "::ffff:198.51.100.7", limited: true}, // same IPv4 client
		{origin: "198.51.100.8"},
	} {
		_, err := service.Create(context.Background(), step.origin, "dark")
		if limited := errors.Is(err, ErrRateLimited); limited != step.limited || (err != nil && !limited) {
			t.Fatalf("Create(%s) error = %v, want limited %v", step.origin, err, step.limited)
		}
	}
}

func TestGuestCleanupServiceDeletesOnlyThroughEmptyGuestPort(t *testing.T) {
	guests := &guestCleanupRepositoryStub{deleted: 3}
	service, err := NewGuestCleanupService(guests, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewGuestCleanupService() error = %v, want nil", err)
	}
	service.now = func() time.Time { return time.Unix(100_000, 0) }

	deleted, err := service.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup() error = %v, want nil", err)
	}
	if deleted != 3 || guests.cutoff != 100_000-int64(24*time.Hour/time.Second) {
		t.Fatalf("cleanup result/cutoff = %d/%d, want 3/%d", deleted, guests.cutoff, 100_000-int64(24*time.Hour/time.Second))
	}
}

func TestGuestCleanupServicePropagatesFailures(t *testing.T) {
	backendErr := errors.New("cleanup failed")
	service, err := NewGuestCleanupService(&guestCleanupRepositoryStub{err: backendErr}, time.Hour)
	if err != nil {
		t.Fatalf("NewGuestCleanupService() error = %v, want nil", err)
	}
	service.now = func() time.Time { return time.Unix(10_000, 0) }
	if _, err := service.Cleanup(context.Background()); !errors.Is(err, backendErr) {
		t.Fatalf("Cleanup() error = %v, want errors.Is(_, %v)", err, backendErr)
	}
}

type guestCreatorStub struct {
	calls int
}

func (s *guestCreatorStub) CreateWithTheme(context.Context, string) (GuestSessionResult, error) {
	s.calls++
	return GuestSessionResult{User: User{ID: 1, IsGuest: true}}, nil
}

type guestRedeemerStub struct {
	calls int
}

func (s *guestRedeemerStub) Redeem(context.Context, string) (GuestSessionResult, error) {
	s.calls++
	return GuestSessionResult{User: User{ID: 1, IsGuest: true}}, nil
}

type guestCleanupRepositoryStub struct {
	deleted int
	cutoff  int64
	err     error
}

func (r *guestCleanupRepositoryStub) DeleteEmptyAbandonedGuests(_ context.Context, cutoff int64) (int, error) {
	r.cutoff = cutoff
	if r.err != nil {
		return 0, r.err
	}
	return r.deleted, nil
}
