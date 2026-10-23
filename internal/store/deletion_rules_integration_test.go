//go:build integration

package store

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestDeletionRulesPreserveHistoryAndCascadeOwnedRows(t *testing.T) {
	harness := postgres.New(t)
	runner, err := NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	ctx := context.Background()
	var userID, groupID, groupToDeleteID int64
	if err := harness.DB.QueryRowContext(ctx, "INSERT INTO users (email, created_at) VALUES ('owner@example.test', 100) RETURNING id").Scan(&userID); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	if err := harness.DB.QueryRowContext(ctx, `
		INSERT INTO groups (name, name_norm, invite_code, created_by, created_at)
		VALUES ('Owned Group', 'owned group', 'invite-owned', $1, 100)
		RETURNING id
	`, userID).Scan(&groupID); err != nil {
		t.Fatalf("insert owned group: %v", err)
	}
	if err := harness.DB.QueryRowContext(ctx, `
		INSERT INTO groups (name, name_norm, invite_code, created_by, created_at)
		VALUES ('Deleted Group', 'deleted group', 'invite-deleted', $1, 100)
		RETURNING id
	`, userID).Scan(&groupToDeleteID); err != nil {
		t.Fatalf("insert group to delete: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, created_at)
		VALUES ($1, $2, 'admin', 100), ($3, $2, 'admin', 100)
	`, groupID, userID, groupToDeleteID); err != nil {
		t.Fatalf("insert memberships: %v", err)
	}
	accountReferenceStatements := []string{
		"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('session-delete', $1, 100, 200)",
		"INSERT INTO oauth_identities (user_id, provider, subject, created_at) VALUES ($1, 'example', 'subject-delete', 100)",
		"INSERT INTO guest_claim_codes (user_id, code, created_at) VALUES ($1, 'claim-delete', 100)",
		"INSERT INTO audit_log (occurred_at, actor_user_id, actor_email, action) VALUES (100, $1, 'owner@example.test', 'group.create')",
		"INSERT INTO usage_events (created_at, method, route, status, user_id) VALUES (100, 'GET', '/groups', 200, $1)",
	}
	for _, statement := range accountReferenceStatements {
		if _, err := harness.DB.ExecContext(ctx, statement, userID); err != nil {
			t.Fatalf("insert account reference: %v", err)
		}
	}

	if _, err := harness.DB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", groupToDeleteID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	var membershipCount int
	if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM group_members WHERE group_id = $1", groupToDeleteID).Scan(&membershipCount); err != nil {
		t.Fatalf("count deleted group memberships: %v", err)
	}
	if membershipCount != 0 {
		t.Fatalf("deleted group memberships = %d, want 0", membershipCount)
	}

	if _, err := harness.DB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	for _, table := range []string{
		"sessions",
		"oauth_identities",
		"guest_claim_codes",
		"group_members",
	} {
		var count int
		if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows = %d, want 0", table, count)
		}
	}

	var createdBy any
	if err := harness.DB.QueryRowContext(ctx, "SELECT created_by FROM groups WHERE id = $1", groupID).Scan(&createdBy); err != nil {
		t.Fatalf("read surviving group: %v", err)
	}
	if createdBy != nil {
		t.Fatalf("surviving group created_by = %v, want NULL", createdBy)
	}
	for _, reference := range []struct {
		table  string
		column string
	}{
		{table: "audit_log", column: "actor_user_id"},
		{table: "usage_events", column: "user_id"},
	} {
		var count int
		if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+reference.table+" WHERE "+reference.column+" IS NULL").Scan(&count); err != nil {
			t.Fatalf("count preserved %s.%s: %v", reference.table, reference.column, err)
		}
		if count != 1 {
			t.Fatalf("preserved %s.%s rows = %d, want 1", reference.table, reference.column, count)
		}
	}
}
