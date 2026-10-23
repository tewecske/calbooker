//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestUsersSchemaEnforcesIdentityAndGuestRules(t *testing.T) {
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
		INSERT INTO users (email, username, created_at)
		VALUES ('user@example.test', 'user', 100)
	`); err != nil {
		t.Fatalf("insert valid user: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO users (email, created_at)
		VALUES ('USER@example.test', 101)
	`); err == nil {
		t.Fatal("duplicate normalized email was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO users (username, created_at)
		VALUES ('has@sign', 102)
	`); err == nil {
		t.Fatal("username containing @ was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO users (email, is_guest, created_at)
		VALUES ('guest@example.test', TRUE, 103)
	`); err == nil {
		t.Fatal("guest credentials were accepted")
	}

	var version int64
	if err := harness.DB.QueryRowContext(ctx, "SELECT version FROM users WHERE email = 'user@example.test'").Scan(&version); err != nil {
		t.Fatalf("read default version: %v", err)
	}
	if version != 0 {
		t.Fatalf("default version = %d, want 0", version)
	}
}
