//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestSecurityHistorySchemaPreservesNullableReferences(t *testing.T) {
	harness := postgres.New(t)
	runner, err := NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	ctx := context.Background()
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO audit_log (occurred_at, actor_email, action, detail, ip)
		VALUES (100, 'admin@example.test', 'account.delete', 'safe detail', '127.0.0.1')
	`); err != nil {
		t.Fatalf("insert audit record: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO usage_events (created_at, method, route, status)
		VALUES (100, 'GET', '/healthz', 200)
	`); err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO usage_events (created_at, method, route, status)
		VALUES (101, 'GET', '/healthz?secret=value', 200)
	`); err == nil {
		t.Fatal("usage event with query string was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO usage_events (created_at, method, route, status)
		VALUES (102, 'GET', '/healthz', 700)
	`); err == nil {
		t.Fatal("usage event with invalid status was accepted")
	}
}
