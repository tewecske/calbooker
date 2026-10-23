//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	testfixtures "github.com/tewecske/calbooker/tests/fixtures"
	testpostgres "github.com/tewecske/calbooker/tests/postgres"
)

func TestRetentionRepositoryRemovesOnlyEligibleRecords(t *testing.T) {
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

	if _, err := testfixtures.InsertUser(ctx, harness.DB, testfixtures.NewUser()); err != nil {
		t.Fatal(err)
	}

	now := int64(200_000)
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, action, user_id, created_at, expires_at, consumed_at)
		VALUES
			('expired-state', 'example', 'sign-in', NULL, 1, 100, NULL),
			('consumed-state', 'example', 'sign-in', NULL, 1, 999_999, 100),
			('valid-state', 'example', 'sign-in', NULL, 1, 999_999, NULL)
	`); err != nil {
		t.Fatal(err)
	}
	usageCutoff := int64(120_000)
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO usage_events (created_at, method, route, status, user_id, ip)
		VALUES
			(50, 'GET', '/en/', 200, NULL, NULL),
			(150_000, 'GET', '/en/', 200, NULL, NULL)
	`); err != nil {
		t.Fatal(err)
	}

	repository, err := NewRetentionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := repository.DeleteExpiredOAuthStates(ctx, now)
	if err != nil || removed != 2 {
		t.Fatalf("oauth states removed = %d, err = %v; want 2", removed, err)
	}
	removed, err = repository.DeleteUsageEventsBefore(ctx, usageCutoff)
	if err != nil || removed != 1 {
		t.Fatalf("usage events removed = %d, err = %v; want 1", removed, err)
	}

	assertRemainingRowCount(t, harness.DB, ctx, "oauth_states", 1)
	assertRemainingRowCount(t, harness.DB, ctx, "usage_events", 1)

	// Running again is a no-op.
	if removed, err := repository.DeleteExpiredOAuthStates(ctx, now); err != nil || removed != 0 {
		t.Fatalf("second oauth state cleanup = %d, %v; want 0", removed, err)
	}
}

func TestRetentionServiceIntegrationAgainstPostgres(t *testing.T) {
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
	repository, err := NewRetentionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	retention, err := service.NewRetentionService(repository, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := retention.CleanupTokens(ctx); err != nil || removed != 0 {
		t.Fatalf("CleanupTokens() = %d, %v; want 0", removed, err)
	}
	if removed, err := retention.CleanupUsageEvents(ctx); err != nil || removed != 0 {
		t.Fatalf("CleanupUsageEvents() = %d, %v; want 0", removed, err)
	}
}

func assertRemainingRowCount(t *testing.T, db *sql.DB, ctx context.Context, table string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s rows = %d, want %d", table, count, want)
	}
}
