package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWindowResolvesKnownAndUnknownKeys(t *testing.T) {
	if key, duration := Window("7d"); key != "7d" || duration != 7*24*time.Hour {
		t.Fatalf("Window(7d) = %q/%s", key, duration)
	}
	if key, duration := Window("nonsense"); key != DefaultUsageWindowKey || duration != 24*time.Hour {
		t.Fatalf("Window(nonsense) = %q/%s, want default", key, duration)
	}
}

func TestUsageReportServiceBuildsBoundedWindow(t *testing.T) {
	repository := &usageReportRepositoryStub{routes: []RouteUsage{{Route: "/{language}/", Requests: 9}}}
	queue := &usageStatsStub{stats: UsageQueueStats{Capacity: 10, Pending: 2, Recorded: 5}}
	service, err := NewUsageReportService(repository, queue)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	service.now = func() time.Time { return now }

	report, err := service.Report(context.Background(), "7d", true)
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if report.WindowKey != "7d" || !report.Ascending {
		t.Fatalf("report = %+v, want 7d ascending", report)
	}
	if report.Until != now.Unix() || report.Since != now.Add(-7*24*time.Hour).Unix() {
		t.Fatalf("window bounds = %d..%d, want 7d ending now", report.Since, report.Until)
	}
	if repository.query.Since != report.Since || repository.query.Until != report.Until || !repository.query.Ascending {
		t.Fatalf("repository query = %+v, want the report window", repository.query)
	}
	if len(report.Routes) != 1 || report.Routes[0].Requests != 9 {
		t.Fatalf("routes = %+v, want the repository rows", report.Routes)
	}
	if report.Queue.Recorded != 5 || report.Queue.Capacity != 10 {
		t.Fatalf("queue = %+v, want live queue stats", report.Queue)
	}
}

func TestNewUsageReportServiceValidatesRepository(t *testing.T) {
	if _, err := NewUsageReportService(nil, nil); !errors.Is(err, ErrNilUsageReportRepository) {
		t.Fatalf("NewUsageReportService(nil) error = %v, want %v", err, ErrNilUsageReportRepository)
	}
}

func TestUsageReportServicePropagatesFailures(t *testing.T) {
	backendErr := errors.New("query failed")
	service, err := NewUsageReportService(&usageReportRepositoryStub{err: backendErr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Report(context.Background(), "24h", false); !errors.Is(err, backendErr) {
		t.Fatalf("Report() error = %v, want errors.Is(_, %v)", err, backendErr)
	}
}

func TestUsageReportServiceRejectsNilContext(t *testing.T) {
	service, err := NewUsageReportService(&usageReportRepositoryStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Report(nil, "24h", false); err == nil {
		t.Fatal("Report(nil) error = nil, want failure")
	}
}

type usageReportRepositoryStub struct {
	routes []RouteUsage
	query  RouteUsageQuery
	err    error
}

func (r *usageReportRepositoryStub) QueryRouteUsage(_ context.Context, query RouteUsageQuery) ([]RouteUsage, error) {
	r.query = query
	if r.err != nil {
		return nil, r.err
	}
	return r.routes, nil
}

type usageStatsStub struct {
	stats UsageQueueStats
}

func (s *usageStatsStub) Stats() UsageQueueStats { return s.stats }
