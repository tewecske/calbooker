//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	testfixtures "github.com/tewecske/calbooker/tests/fixtures"
	testpostgres "github.com/tewecske/calbooker/tests/postgres"
)

func TestSuspiciousAccountRepositoryFlagsThresholds(t *testing.T) {
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

	userID, err := testfixtures.InsertUser(ctx, harness.DB, testfixtures.NewUser())
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1_000_000)
	origins := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}
	for index, origin := range origins {
		if _, err := harness.DB.ExecContext(ctx, `
			INSERT INTO usage_events (created_at, method, route, status, user_id, ip)
			VALUES ($1, 'GET', '/{language}/', 200, $2, $3)
		`, now-int64(index), userID, origin); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO usage_events (created_at, method, route, status, user_id, ip)
		VALUES ($1, 'GET', '/{language}/stale', 200, $2, '203.0.113.1')
	`, now-100_000, userID); err != nil {
		t.Fatal(err)
	}

	repository, err := NewSuspiciousAccountRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	// Below both thresholds, and the stale event is outside the window.
	rows, err := repository.QueryAccountUsage(ctx, service.AccountUsageQuery{
		Since: now - 1_000, Until: now + 1, ActionThreshold: 10, OriginThreshold: 5, Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryAccountUsage() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none below thresholds", rows)
	}
	// The action threshold now flags the account with its distinct origins.
	rows, err = repository.QueryAccountUsage(ctx, service.AccountUsageQuery{
		Since: now - 1_000, Until: now + 1, ActionThreshold: 2, OriginThreshold: 10, Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryAccountUsage() error = %v", err)
	}
	if len(rows) != 1 || rows[0].UserID != userID || rows[0].Requests != 3 || rows[0].DistinctOrigins != 3 {
		t.Fatalf("rows = %+v, want one flagged account with 3 requests and 3 origins", rows)
	}
}
