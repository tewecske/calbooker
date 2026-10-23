//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestGroupsAndMembershipsSchema(t *testing.T) {
	harness := postgres.New(t)
	runner, err := NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	ctx := context.Background()
	var userID, groupID int64
	if err := harness.DB.QueryRowContext(ctx, "INSERT INTO users (created_at) VALUES (100) RETURNING id").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := harness.DB.QueryRowContext(ctx, `
		INSERT INTO groups (name, name_norm, invite_code, created_by, created_at)
		VALUES ('Example Group', 'example group', 'invite-1', $1, 100)
		RETURNING id
	`, userID).Scan(&groupID); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, created_at)
		VALUES ($1, $2, 'admin', 100)
	`, groupID, userID); err != nil {
		t.Fatalf("insert administrator membership: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, created_at)
		VALUES ($1, $2, 'member', 101)
	`, groupID, userID); err == nil {
		t.Fatal("duplicate membership was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, created_at)
		VALUES ($1, 999999, 'owner', 101)
	`, groupID); err == nil {
		t.Fatal("invalid membership role was accepted")
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO groups (name, name_norm, invite_code, created_at)
		VALUES ('Other Group', 'other group', 'invite-1', 102)
	`); err == nil {
		t.Fatal("duplicate invite code was accepted")
	}
}
