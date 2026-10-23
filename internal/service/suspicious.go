package service

import (
	"context"
	"errors"
	"time"
)

const (
	// SuspiciousActionCountThreshold flags accounts with many requests.
	SuspiciousActionCountThreshold = 500
	// SuspiciousDistinctOriginThreshold flags accounts seen from many origins.
	SuspiciousDistinctOriginThreshold = 5
	// MaxSuspiciousAccounts bounds one report.
	MaxSuspiciousAccounts = 100
)

var (
	// ErrNilSuspiciousAccountRepository identifies missing persistence.
	ErrNilSuspiciousAccountRepository = errors.New("service: nil suspicious account repository")
	// ErrInvalidSuspiciousConfig identifies unusable report settings.
	ErrInvalidSuspiciousConfig = errors.New("service: invalid suspicious account config")
)

// AccountUsage is one account's aggregate usage in a window.
type AccountUsage struct {
	UserID          int64
	Requests        int
	DistinctOrigins int
}

// AccountUsageQuery is the bounded, server-validated aggregate query.
type AccountUsageQuery struct {
	Since           int64
	Until           int64
	ActionThreshold int
	OriginThreshold int
	Limit           int
}

// SuspiciousAccountRepository finds accounts exceeding investigation
// thresholds.
type SuspiciousAccountRepository interface {
	QueryAccountUsage(context.Context, AccountUsageQuery) ([]AccountUsage, error)
}

// SuspiciousAccount is one account's usage with its investigation flags. Flags
// are signals, not accusations.
type SuspiciousAccount struct {
	UserID          int64
	Requests        int
	DistinctOrigins int
	ActionFlag      bool
	OriginFlag      bool
}

// SuspiciousReport is one window's flagged accounts and the applied thresholds.
type SuspiciousReport struct {
	WindowKey       string
	Since           int64
	Until           int64
	ActionThreshold int
	OriginThreshold int
	Accounts        []SuspiciousAccount
}

// SuspiciousAccountService finds accounts exceeding usage thresholds.
type SuspiciousAccountService struct {
	repository SuspiciousAccountRepository
	now        func() time.Time
}

// NewSuspiciousAccountService constructs the suspicious-account report.
func NewSuspiciousAccountService(repository SuspiciousAccountRepository) (*SuspiciousAccountService, error) {
	if repository == nil {
		return nil, ErrNilSuspiciousAccountRepository
	}
	return &SuspiciousAccountService{repository: repository, now: time.Now}, nil
}

// Report returns flagged accounts for the selected window.
func (s *SuspiciousAccountService) Report(ctx context.Context, windowKey string) (SuspiciousReport, error) {
	if s == nil || s.repository == nil || s.now == nil {
		return SuspiciousReport{}, ErrInvalidSuspiciousConfig
	}
	if ctx == nil {
		return SuspiciousReport{}, errors.New("service: nil suspicious account context")
	}
	key, duration := Window(windowKey)
	until := s.now()
	since := until.Add(-duration)
	rows, err := s.repository.QueryAccountUsage(ctx, AccountUsageQuery{
		Since:           since.Unix(),
		Until:           until.Unix(),
		ActionThreshold: SuspiciousActionCountThreshold,
		OriginThreshold: SuspiciousDistinctOriginThreshold,
		Limit:           MaxSuspiciousAccounts,
	})
	if err != nil {
		return SuspiciousReport{}, err
	}
	report := SuspiciousReport{
		WindowKey:       key,
		Since:           since.Unix(),
		Until:           until.Unix(),
		ActionThreshold: SuspiciousActionCountThreshold,
		OriginThreshold: SuspiciousDistinctOriginThreshold,
	}
	for _, row := range rows {
		if row.UserID <= 0 {
			continue
		}
		report.Accounts = append(report.Accounts, SuspiciousAccount{
			UserID:          row.UserID,
			Requests:        row.Requests,
			DistinctOrigins: row.DistinctOrigins,
			ActionFlag:      row.Requests >= SuspiciousActionCountThreshold,
			OriginFlag:      row.DistinctOrigins >= SuspiciousDistinctOriginThreshold,
		})
	}
	return report, nil
}
