package service

import (
	"context"
	"errors"
	"time"
)

const (
	// DefaultUsageReportLimit bounds a route report.
	DefaultUsageReportLimit = 20
	// MaxUsageReportLimit bounds a route report.
	MaxUsageReportLimit = 100
)

var (
	// ErrNilUsageReportRepository identifies missing usage-report persistence.
	ErrNilUsageReportRepository = errors.New("service: nil usage report repository")
	// ErrInvalidUsageReportConfig identifies unusable report settings.
	ErrInvalidUsageReportConfig = errors.New("service: invalid usage report config")
)

// UsageWindows are the selectable report windows.
var UsageWindows = []struct {
	Key      string
	Duration time.Duration
}{
	{Key: "24h", Duration: 24 * time.Hour},
	{Key: "7d", Duration: 7 * 24 * time.Hour},
	{Key: "30d", Duration: 30 * 24 * time.Hour},
}

// DefaultUsageWindowKey is used when a request omits a window.
const DefaultUsageWindowKey = "24h"

// RouteUsage is one normalized route's request count.
type RouteUsage struct {
	Route    string
	Requests int
}

// RouteUsageQuery is the bounded, server-validated route aggregation.
type RouteUsageQuery struct {
	Since     int64
	Until     int64
	Limit     int
	Ascending bool
}

// UsageReportRepository aggregates usage events by normalized route.
type UsageReportRepository interface {
	QueryRouteUsage(context.Context, RouteUsageQuery) ([]RouteUsage, error)
}

// UsageQueueStatsProvider reports live queue health for the report page.
type UsageQueueStatsProvider interface {
	Stats() UsageQueueStats
}

// UsageReport is one window's route report plus live queue health.
type UsageReport struct {
	WindowKey string
	Window    time.Duration
	Since     int64
	Until     int64
	Ascending bool
	Routes    []RouteUsage
	Queue     UsageQueueStats
}

// UsageReportService builds most-used and least-used route reports.
type UsageReportService struct {
	repository UsageReportRepository
	queue      UsageQueueStatsProvider
	now        func() time.Time
}

// NewUsageReportService constructs route reporting over usage events.
func NewUsageReportService(repository UsageReportRepository, queue UsageQueueStatsProvider) (*UsageReportService, error) {
	if repository == nil {
		return nil, ErrNilUsageReportRepository
	}
	return &UsageReportService{repository: repository, queue: queue, now: time.Now}, nil
}

// Window resolves a window key to its duration, falling back to the default.
func Window(key string) (string, time.Duration) {
	for _, window := range UsageWindows {
		if window.Key == key {
			return window.Key, window.Duration
		}
	}
	return DefaultUsageWindowKey, UsageWindows[0].Duration
}

// Report returns route usage for the selected window and order.
func (s *UsageReportService) Report(ctx context.Context, windowKey string, ascending bool) (UsageReport, error) {
	if s == nil || s.repository == nil || s.now == nil {
		return UsageReport{}, ErrInvalidUsageReportConfig
	}
	if ctx == nil {
		return UsageReport{}, errors.New("service: nil usage report context")
	}
	key, duration := Window(windowKey)
	until := s.now()
	since := until.Add(-duration)
	rows, err := s.repository.QueryRouteUsage(ctx, RouteUsageQuery{
		Since:     since.Unix(),
		Until:     until.Unix(),
		Limit:     DefaultUsageReportLimit,
		Ascending: ascending,
	})
	if err != nil {
		return UsageReport{}, err
	}
	report := UsageReport{
		WindowKey: key,
		Window:    duration,
		Since:     since.Unix(),
		Until:     until.Unix(),
		Ascending: ascending,
		Routes:    rows,
	}
	if s.queue != nil {
		report.Queue = s.queue.Stats()
	}
	return report, nil
}
