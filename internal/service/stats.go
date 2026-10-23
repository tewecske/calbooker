package service

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// DefaultDatastoreStatsCacheTTL briefly caches expensive aggregate counts.
	DefaultDatastoreStatsCacheTTL = 10 * time.Second
)

var (
	// ErrNilDatastoreStatsRepository identifies missing aggregate persistence.
	ErrNilDatastoreStatsRepository = errors.New("service: nil datastore stats repository")
	// ErrInvalidDatastoreStatsConfig identifies unusable health-count settings.
	ErrInvalidDatastoreStatsConfig = errors.New("service: invalid datastore stats config")
)

// DatastoreCounts contains safe aggregate counts. It never carries account
// identifiers, private records, or credentials.
type DatastoreCounts struct {
	TotalAccounts         int
	GuestAccounts         int
	AdministratorAccounts int
	UnconfirmedAccounts   int
	ActiveSessions        int
	ExpiredSessions       int
	ExpiredOAuthStates    int
}

// DatastoreStatsRepository returns bounded aggregate counts.
type DatastoreStatsRepository interface {
	CountAccounts(context.Context) (AccountCounts, error)
	CountActiveSessions(context.Context, int64) (int, error)
	CountExpiredSessions(context.Context, int64) (int, error)
	CountExpiredOAuthStates(context.Context, int64) (int, error)
}

// AccountCounts groups account aggregate counts.
type AccountCounts struct {
	Total          int
	Guests         int
	Administrators int
	Unconfirmed    int
}

// DatastoreStatsService reports safe data-store health counts with a short
// cache so repeated page views do not run expensive aggregates.
type DatastoreStatsService struct {
	repository DatastoreStatsRepository
	cacheTTL   time.Duration
	now        func() time.Time

	mu        sync.Mutex
	cached    DatastoreCounts
	cachedAt  time.Time
	hasCached bool
}

// NewDatastoreStatsService constructs health counts with a brief aggregate
// cache.
func NewDatastoreStatsService(repository DatastoreStatsRepository, cacheTTL time.Duration) (*DatastoreStatsService, error) {
	if repository == nil {
		return nil, ErrNilDatastoreStatsRepository
	}
	if cacheTTL < 0 {
		return nil, ErrInvalidDatastoreStatsConfig
	}
	return &DatastoreStatsService{repository: repository, cacheTTL: cacheTTL, now: time.Now}, nil
}

// Counts returns safe aggregate counts. The aggregate queries are cached for
// the configured TTL.
func (s *DatastoreStatsService) Counts(ctx context.Context) (DatastoreCounts, error) {
	if s == nil || s.repository == nil || s.now == nil {
		return DatastoreCounts{}, ErrInvalidDatastoreStatsConfig
	}
	if ctx == nil {
		return DatastoreCounts{}, errors.New("service: nil datastore stats context")
	}
	now := s.now()
	return s.cachedCounts(ctx, now)
}

func (s *DatastoreStatsService) cachedCounts(ctx context.Context, now time.Time) (DatastoreCounts, error) {
	s.mu.Lock()
	if s.hasCached && s.cacheTTL > 0 && now.Sub(s.cachedAt) < s.cacheTTL {
		cached := s.cached
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	counts, err := s.loadCounts(ctx, now)
	if err != nil {
		return DatastoreCounts{}, err
	}

	s.mu.Lock()
	s.cached = counts
	s.cachedAt = now
	s.hasCached = true
	s.mu.Unlock()
	return counts, nil
}

func (s *DatastoreStatsService) loadCounts(ctx context.Context, now time.Time) (DatastoreCounts, error) {
	accounts, err := s.repository.CountAccounts(ctx)
	if err != nil {
		return DatastoreCounts{}, err
	}
	activeSessions, err := s.repository.CountActiveSessions(ctx, now.Unix())
	if err != nil {
		return DatastoreCounts{}, err
	}
	expiredSessions, err := s.repository.CountExpiredSessions(ctx, now.Unix())
	if err != nil {
		return DatastoreCounts{}, err
	}
	expiredStates, err := s.repository.CountExpiredOAuthStates(ctx, now.Unix())
	if err != nil {
		return DatastoreCounts{}, err
	}
	return DatastoreCounts{
		TotalAccounts:         accounts.Total,
		GuestAccounts:         accounts.Guests,
		AdministratorAccounts: accounts.Administrators,
		UnconfirmedAccounts:   accounts.Unconfirmed,
		ActiveSessions:        activeSessions,
		ExpiredSessions:       expiredSessions,
		ExpiredOAuthStates:    expiredStates,
	}, nil
}
