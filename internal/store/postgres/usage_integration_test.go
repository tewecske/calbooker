//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	testpostgres "github.com/tewecske/calbooker/tests/postgres"
)

func TestUsageQueueRecordsEventsOutsideTheRequest(t *testing.T) {
	harness := testpostgres.New(t)
	runner, err := store.NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	repository, err := NewUsageEventRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := service.NewUsageQueue(repository, 8)
	if err != nil {
		t.Fatal(err)
	}
	runContext, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- queue.Run(runContext) }()

	origin := "203.0.113.9"
	for index := 0; index < 3; index++ {
		if err := queue.EnqueueUsage(ctx, service.UsageEvent{
			Method: "GET", Route: "/{language}/groups/{id}", Status: 200, RequestID: "request-link-1", TraceID: "trace-link-1", IP: &origin,
		}); err != nil {
			t.Fatalf("EnqueueUsage() error = %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for queue.Stats().Recorded < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var (
		method    string
		route     string
		requestID string
		traceID   string
		status    int
		count     int
	)
	if err := harness.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(method), MIN(route), MIN(request_id), MIN(trace_id), MIN(status) FROM usage_events
	`).Scan(&count, &method, &route, &requestID, &traceID, &status); err != nil {
		t.Fatal(err)
	}
	if count != 3 || method != "GET" || route != "/{language}/groups/{id}" || requestID != "request-link-1" || traceID != "trace-link-1" || status != 200 {
		t.Fatalf("stored usage = count %d method %q route %q request %q trace %q status %d", count, method, route, requestID, traceID, status)
	}
}

func TestUsageReportRepositoryOrdersAndWindowsRoutes(t *testing.T) {
	harness := testpostgres.New(t)
	runner, err := store.NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	insert := func(createdAt int64, route string) {
		if _, err := harness.DB.ExecContext(ctx, `
			INSERT INTO usage_events (created_at, method, route, status) VALUES ($1, 'GET', $2, 200)
		`, createdAt, route); err != nil {
			t.Fatal(err)
		}
	}
	insert(900, "/{language}/")
	insert(950, "/{language}/")
	insert(960, "/{language}/")
	insert(900, "/{language}/groups")
	insert(100, "/{language}/stale")

	repository, err := NewUsageReportRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	query := service.RouteUsageQuery{Since: 800, Until: 1000, Limit: 10}
	most, err := repository.QueryRouteUsage(ctx, query)
	if err != nil {
		t.Fatalf("QueryRouteUsage() error = %v", err)
	}
	if len(most) != 2 || most[0].Route != "/{language}/" || most[0].Requests != 3 || most[1].Requests != 1 {
		t.Fatalf("most-used = %+v, want / with 3 then /groups with 1", most)
	}
	query.Ascending = true
	least, err := repository.QueryRouteUsage(ctx, query)
	if err != nil {
		t.Fatalf("QueryRouteUsage(asc) error = %v", err)
	}
	if len(least) != 2 || least[0].Requests != 1 || least[1].Requests != 3 {
		t.Fatalf("least-used = %+v, want ascending order", least)
	}
}
