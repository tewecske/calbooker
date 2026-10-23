package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultUsageRetention bounds anonymous usage-event growth.
	DefaultUsageRetention = 90 * 24 * time.Hour
)

var (
	// ErrNilRetentionRepository identifies missing retention persistence.
	ErrNilRetentionRepository = errors.New("service: nil retention repository")
	// ErrInvalidRetentionConfig identifies unusable retention periods.
	ErrInvalidRetentionConfig = errors.New("service: invalid retention config")
)

// RetentionRepository removes records that are expired, consumed, or older
// than a cutoff. Every operation is idempotent and returns the removed count.
type RetentionRepository interface {
	DeleteExpiredOAuthStates(context.Context, int64) (int, error)
	DeleteUsageEventsBefore(context.Context, int64) (int, error)
}

// RetentionService removes expired credentials and old operational history
// according to configured retention. It never reads or returns secret values.
type RetentionService struct {
	repository     RetentionRepository
	usageRetention time.Duration
	now            func() time.Time
}

// NewRetentionService constructs retention cleanup with explicit periods.
func NewRetentionService(repository RetentionRepository, usageRetention time.Duration) (*RetentionService, error) {
	if repository == nil {
		return nil, ErrNilRetentionRepository
	}
	if usageRetention <= 0 {
		return nil, ErrInvalidRetentionConfig
	}
	return &RetentionService{
		repository:     repository,
		usageRetention: usageRetention,
		now:            time.Now,
	}, nil
}

// CleanupTokens removes consumed or expired sign-in flow states and returns
// the number removed.
func (s *RetentionService) CleanupTokens(ctx context.Context) (int, error) {
	if err := s.validate(ctx); err != nil {
		return 0, err
	}
	count, err := s.repository.DeleteExpiredOAuthStates(ctx, s.now().Unix())
	if err != nil {
		return 0, fmt.Errorf("clean up tokens: %w", err)
	}
	return count, nil
}

// CleanupUsageEvents removes usage events older than the usage retention.
func (s *RetentionService) CleanupUsageEvents(ctx context.Context) (int, error) {
	if err := s.validate(ctx); err != nil {
		return 0, err
	}
	cutoff := s.now().Add(-s.usageRetention).Unix()
	count, err := s.repository.DeleteUsageEventsBefore(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("clean up usage events: %w", err)
	}
	return count, nil
}

// UsageRetention reports the configured usage-event period.
func (s *RetentionService) UsageRetention() time.Duration {
	if s == nil {
		return 0
	}
	return s.usageRetention
}

func (s *RetentionService) validate(ctx context.Context) error {
	if s == nil || s.repository == nil || s.now == nil || s.usageRetention <= 0 {
		return ErrInvalidRetentionConfig
	}
	if ctx == nil {
		return errors.New("service: nil retention context")
	}
	return nil
}
