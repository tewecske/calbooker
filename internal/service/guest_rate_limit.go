package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	guestCreationAction   = "guest.create"
	guestRedemptionAction = "guest.redeem"
)

// GuestCreator is the guest creation operation consumed by rate limiting.
type GuestCreator interface {
	CreateWithTheme(context.Context, string) (GuestSessionResult, error)
}

// GuestRedeemer is the guest redemption operation consumed by rate limiting.
type GuestRedeemer interface {
	Redeem(context.Context, string) (GuestSessionResult, error)
}

// RateLimitedGuestService applies separate origin budgets to guest creation
// and transfer-code redemption.
type RateLimitedGuestService struct {
	creator  GuestCreator
	redeemer GuestRedeemer
	limiter  *RateLimiter
}

// NewRateLimitedGuestService constructs guest operations with independent
// creation and redemption rate-limit actions.
func NewRateLimitedGuestService(
	creator GuestCreator,
	redeemer GuestRedeemer,
	limiter *RateLimiter,
) (*RateLimitedGuestService, error) {
	if creator == nil || redeemer == nil {
		return nil, errors.New("service: nil guest operation")
	}
	if limiter == nil {
		return nil, ErrInvalidRateLimitConfig
	}
	return &RateLimitedGuestService{creator: creator, redeemer: redeemer, limiter: limiter}, nil
}

// Create applies the guest-creation budget for origin before creating a guest.
func (s *RateLimitedGuestService) Create(
	ctx context.Context,
	origin string,
	theme string,
) (GuestSessionResult, error) {
	if s == nil || s.creator == nil || s.limiter == nil {
		return GuestSessionResult{}, ErrInvalidRateLimitConfig
	}
	if decision, err := s.limiter.Allow(guestCreationAction, originRateLimitKey(origin)); err != nil {
		return GuestSessionResult{}, err
	} else if !decision.Allowed {
		return GuestSessionResult{}, RateLimitError{RetryAfter: decision.RetryAfter}
	}
	return s.creator.CreateWithTheme(ctx, theme)
}

// Redeem applies the independent redemption budget without including the
// bearer code in a limiter key or error message.
func (s *RateLimitedGuestService) Redeem(
	ctx context.Context,
	origin string,
	code string,
) (GuestSessionResult, error) {
	if s == nil || s.redeemer == nil || s.limiter == nil {
		return GuestSessionResult{}, ErrInvalidRateLimitConfig
	}
	if decision, err := s.limiter.Allow(guestRedemptionAction, originRateLimitKey(origin)); err != nil {
		return GuestSessionResult{}, err
	} else if !decision.Allowed {
		return GuestSessionResult{}, RateLimitError{RetryAfter: decision.RetryAfter}
	}
	return s.redeemer.Redeem(ctx, code)
}

// GuestCleanupService removes guests older than configured retention when the
// persistence adapter confirms they own no application data.
type GuestCleanupService struct {
	guests    GuestCleanupRepository
	retention time.Duration
	now       func() time.Time
}

// NewGuestCleanupService constructs guest retention maintenance.
func NewGuestCleanupService(guests GuestCleanupRepository, retention time.Duration) (*GuestCleanupService, error) {
	if guests == nil || retention <= 0 {
		return nil, errors.New("service: invalid guest cleanup config")
	}
	return &GuestCleanupService{guests: guests, retention: retention, now: time.Now}, nil
}

// Cleanup removes eligible guests and returns the number deleted.
func (s *GuestCleanupService) Cleanup(ctx context.Context) (int, error) {
	if s == nil || s.guests == nil || s.now == nil || s.retention <= 0 {
		return 0, errors.New("service: invalid guest cleanup config")
	}
	if ctx == nil {
		return 0, errors.New("service: nil guest cleanup context")
	}
	cutoff := s.now().Add(-s.retention).Unix()
	if cutoff <= 0 {
		return 0, errors.New("service: invalid guest cleanup cutoff")
	}
	deleted, err := s.guests.DeleteEmptyAbandonedGuests(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete empty abandoned guests: %w", err)
	}
	return deleted, nil
}
