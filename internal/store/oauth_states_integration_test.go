//go:build integration

package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestOAuthStatesMigrationSupportsAnonymousAndLinkedRows(t *testing.T) {
	harness := postgres.New(t)
	runner, err := NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	ctx := context.Background()
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	var userID int64
	if err := harness.DB.QueryRowContext(ctx, "INSERT INTO users (created_at) VALUES (100) RETURNING id").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, action, user_id, created_at, expires_at)
		VALUES ('anonymous-state', 'google', 'sign-in', NULL, 100, 200),
		       ('linked-state', 'google', 'link', $1, 100, 200)
	`, userID); err != nil {
		t.Fatalf("insert oauth states: %v", err)
	}

	var anonymousUserID sql.NullInt64
	if err := harness.DB.QueryRowContext(ctx, "SELECT user_id FROM oauth_states WHERE state = 'anonymous-state'").Scan(&anonymousUserID); err != nil {
		t.Fatalf("scan anonymous state: %v", err)
	}
	if anonymousUserID.Valid {
		t.Fatalf("anonymous user_id = %d, want NULL", anonymousUserID.Int64)
	}

	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, action, created_at, expires_at)
		VALUES ('bad-action', 'google', 'callback', 100, 200)
	`); err == nil {
		t.Fatal("invalid OAuth state action was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, action, created_at, expires_at)
		VALUES ('bad-expiry', 'google', 'sign-in', 200, 100)
	`); err == nil {
		t.Fatal("invalid OAuth state expiry was accepted")
	}
}
