//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestSessionsIdentitiesAndTokensSchema(t *testing.T) {
	harness := postgres.New(t)
	runner, err := NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	ctx := context.Background()
	var userID int64
	if err := harness.DB.QueryRowContext(ctx, "INSERT INTO users (created_at) VALUES (100) RETURNING id").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at)
		VALUES ('session-1', $1, 100, 200)
	`, userID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at)
		VALUES ('session-2', $1, 200, 100)
	`, userID); err == nil {
		t.Fatal("session with invalid expiry was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO oauth_identities (user_id, provider, subject, created_at)
		VALUES ($1, 'example', 'subject-1', 100)
	`, userID); err != nil {
		t.Fatalf("insert external identity: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO oauth_identities (user_id, provider, subject, created_at)
		VALUES ($1, 'example', 'subject-1', 101)
	`, userID); err == nil {
		t.Fatal("duplicate provider subject was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO guest_claim_codes (user_id, code, created_at)
		VALUES ($1, 'code-1', 100)
	`, userID); err != nil {
		t.Fatalf("insert guest claim code: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO guest_claim_codes (user_id, code, created_at)
		VALUES ($1, 'code-2', 101)
	`, userID); err == nil {
		t.Fatal("second active guest claim code was accepted")
	}
}
