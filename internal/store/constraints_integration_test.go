//go:build integration

package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

// migrateConstraintSchema applies every migration to an isolated schema so the
// constraint and transaction checks below run against the production engine.
func migrateConstraintSchema(t *testing.T, harness *postgres.Harness) {
	t.Helper()
	runner, err := NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
}

func seedConstraintUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(),
		"INSERT INTO users (email, created_at) VALUES ($1, 100) RETURNING id", email).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}
	return id
}

func seedConstraintGroup(t *testing.T, db *sql.DB, invite string, owner int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO groups (name, slug, name_norm, created_by, created_at)
		VALUES ($1, $2, $3, $4, 100)
		RETURNING id
	`, "Group "+invite, "group-"+invite, "group "+invite, owner).Scan(&id); err != nil {
		t.Fatalf("insert group %s: %v", invite, err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO group_invites (group_id, code, role, created_by, created_at) VALUES ($1, $2, 'player', $3, 100)
	`, id, invite, owner); err != nil {
		t.Fatalf("insert invite %s: %v", invite, err)
	}
	return id
}

func TestSchemaUniqueConstraintsRejectDuplicates(t *testing.T) {
	harness := postgres.New(t)
	migrateConstraintSchema(t, harness)
	ctx := context.Background()

	userA := seedConstraintUser(t, harness.DB, "unique-a@example.test")
	userB := seedConstraintUser(t, harness.DB, "unique-b@example.test")
	groupA := seedConstraintGroup(t, harness.DB, "invite-unique-a", userA)

	tests := []struct {
		name      string
		statement string
		args      []any
	}{
		{
			name:      "duplicate email",
			statement: "INSERT INTO users (email, created_at) VALUES ($1, 200)",
			args:      []any{"unique-a@example.test"},
		},
		{
			name:      "duplicate username",
			statement: "INSERT INTO users (username, created_at) VALUES ($1, 201)",
			args:      []any{"unique-name"},
		},
		{
			name:      "duplicate session id",
			statement: "INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('dup-session', $1, 100, 200)",
			args:      []any{userA},
		},
		{
			name:      "duplicate provider subject",
			statement: "INSERT INTO oauth_identities (user_id, provider, subject, created_at) VALUES ($1, 'example', 'dup-subject', 100)",
			args:      []any{userB},
		},
		{
			name:      "duplicate guest claim code value",
			statement: "INSERT INTO guest_claim_codes (user_id, code, created_at) VALUES ($1, 'dup-claim', 100)",
			args:      []any{userB},
		},
		{
			name:      "duplicate group invite code",
			statement: "INSERT INTO group_invites (group_id, code, role, created_at) VALUES ($1, $2, 'player', 100)",
			args:      []any{groupA, "invite-unique-a"},
		},
		{
			name:      "duplicate oauth state",
			statement: "INSERT INTO oauth_states (state, provider, action, created_at, expires_at) VALUES ('dup-state', 'google', 'sign-in', 100, 200)",
			args:      nil,
		},
	}

	baseline := []struct {
		statement string
		args      []any
	}{
		{"INSERT INTO users (username, created_at) VALUES ('unique-name', 150)", nil},
		{"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('dup-session', $1, 100, 200)", []any{userA}},
		{"INSERT INTO oauth_identities (user_id, provider, subject, created_at) VALUES ($1, 'example', 'dup-subject', 100)", []any{userA}},
		{"INSERT INTO guest_claim_codes (user_id, code, created_at) VALUES ($1, 'dup-claim', 100)", []any{userA}},
		{"INSERT INTO oauth_states (state, provider, action, created_at, expires_at) VALUES ('dup-state', 'google', 'sign-in', 100, 200)", nil},
		{"INSERT INTO group_members (group_id, user_id, created_at) VALUES ($1, $2, 100)", []any{groupA, userA}},
		{"INSERT INTO group_members (group_id, user_id, created_at) VALUES ($1, $2, 100)", []any{groupA, userB}},
	}
	for _, insert := range baseline {
		if _, err := harness.DB.ExecContext(ctx, insert.statement, insert.args...); err != nil {
			t.Fatalf("seed baseline %q: %v", insert.statement, err)
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := harness.DB.ExecContext(ctx, test.statement, test.args...); err == nil {
				t.Fatalf("duplicate was accepted: %s", test.statement)
			}
		})
	}

	t.Run("duplicate membership", func(t *testing.T) {
		if _, err := harness.DB.ExecContext(ctx, `
			INSERT INTO group_members (group_id, user_id, created_at)
			VALUES ($1, $2, 200)
		`, groupA, userA); err == nil {
			t.Fatal("duplicate group membership was accepted")
		}
	})
	t.Run("second active guest claim code", func(t *testing.T) {
		if _, err := harness.DB.ExecContext(ctx, `
			INSERT INTO guest_claim_codes (user_id, code, created_at)
			VALUES ($1, 'dup-claim-active', 200)
		`, userA); err == nil {
			t.Fatal("second active guest claim code was accepted")
		}
	})
	t.Run("revoked claim code frees the active slot", func(t *testing.T) {
		if _, err := harness.DB.ExecContext(ctx, `
			UPDATE guest_claim_codes SET revoked_at = 200 WHERE user_id = $1 AND code = 'dup-claim'
		`, userA); err != nil {
			t.Fatalf("revoke claim code: %v", err)
		}
		if _, err := harness.DB.ExecContext(ctx, `
			INSERT INTO guest_claim_codes (user_id, code, created_at)
			VALUES ($1, 'dup-claim-replacement', 201)
		`, userA); err != nil {
			t.Fatalf("replacement claim code rejected: %v", err)
		}
	})
}

func TestSchemaForeignKeysRejectOrphans(t *testing.T) {
	harness := postgres.New(t)
	migrateConstraintSchema(t, harness)
	ctx := context.Background()

	userID := seedConstraintUser(t, harness.DB, "fk-owner@example.test")
	groupID := seedConstraintGroup(t, harness.DB, "invite-fk", userID)
	const missingID = 9_000_000

	tests := []struct {
		name      string
		statement string
	}{
		{name: "session owner", statement: "INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('orphan-session', 9000000, 100, 200)"},
		{name: "identity owner", statement: "INSERT INTO oauth_identities (user_id, provider, subject, created_at) VALUES (9000000, 'example', 'orphan-subject', 100)"},
		{name: "guest claim owner", statement: "INSERT INTO guest_claim_codes (user_id, code, created_at) VALUES (9000000, 'orphan-claim', 100)"},
		{name: "oauth state owner", statement: "INSERT INTO oauth_states (state, provider, action, user_id, created_at, expires_at) VALUES ('orphan-state', 'google', 'link', 9000000, 100, 200)"},
		{name: "group creator", statement: "INSERT INTO groups (name, slug, name_norm, created_by, created_at) VALUES ('Orphan', 'orphan', 'orphan', 9000000, 100)"},
		{name: "audit actor", statement: "INSERT INTO audit_log (occurred_at, actor_user_id, actor_email, action) VALUES (100, 9000000, 'orphan@example.test', 'account.delete')"},
		{name: "usage actor", statement: "INSERT INTO usage_events (created_at, method, route, status, user_id) VALUES (100, 'GET', '/healthz', 200, 9000000)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := harness.DB.ExecContext(ctx, test.statement); err == nil {
				t.Fatalf("orphan row was accepted: %s", test.statement)
			}
		})
	}

	t.Run("membership group", func(t *testing.T) {
		if _, err := harness.DB.ExecContext(ctx, `
			INSERT INTO group_members (group_id, user_id, created_at)
			VALUES ($1, $2, 100)
		`, missingID, userID); err == nil {
			t.Fatal("membership with missing group was accepted")
		}
	})
	t.Run("membership user", func(t *testing.T) {
		if _, err := harness.DB.ExecContext(ctx, `
			INSERT INTO group_members (group_id, user_id, created_at)
			VALUES ($1, $2, 100)
		`, groupID, missingID); err == nil {
			t.Fatal("membership with missing user was accepted")
		}
	})
}

func TestSchemaCascadesOwnedRowsAndDetachesHistory(t *testing.T) {
	harness := postgres.New(t)
	migrateConstraintSchema(t, harness)
	ctx := context.Background()

	owner := seedConstraintUser(t, harness.DB, "cascade-owner@example.test")
	groupID := seedConstraintGroup(t, harness.DB, "invite-cascade", owner)

	owned := []struct {
		statement string
	}{
		{"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('cascade-session', $1, 100, 200)"},
		{"INSERT INTO oauth_identities (user_id, provider, subject, created_at) VALUES ($1, 'example', 'cascade-subject', 100)"},
		{"INSERT INTO guest_claim_codes (user_id, code, created_at) VALUES ($1, 'cascade-claim', 100)"},
		{"INSERT INTO oauth_states (state, provider, action, user_id, created_at, expires_at) VALUES ('cascade-state', 'google', 'link', $1, 100, 200)"},
		{"INSERT INTO group_members (group_id, user_id, created_at) VALUES ($1, $2, 100)"},
	}
	for i, insert := range owned {
		args := []any{owner}
		if i == len(owned)-1 {
			args = []any{groupID, owner}
		}
		if _, err := harness.DB.ExecContext(ctx, insert.statement, args...); err != nil {
			t.Fatalf("insert owned row %d: %v", i, err)
		}
	}

	history := []struct {
		statement string
	}{
		{"INSERT INTO audit_log (occurred_at, actor_user_id, actor_email, action, detail) VALUES (100, $1, 'cascade-owner@example.test', 'account.delete', 'kept detail')"},
		{"INSERT INTO usage_events (created_at, method, route, status, user_id) VALUES (100, 'GET', '/groups', 200, $1)"},
	}
	for i, insert := range history {
		if _, err := harness.DB.ExecContext(ctx, insert.statement, owner); err != nil {
			t.Fatalf("insert history row %d: %v", i, err)
		}
	}

	if _, err := harness.DB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", owner); err != nil {
		t.Fatalf("delete owner: %v", err)
	}

	for _, table := range []string{
		"sessions", "oauth_identities", "guest_claim_codes", "oauth_states", "group_members",
	} {
		var count int
		if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows after cascade = %d, want 0", table, count)
		}
	}

	for _, reference := range []struct {
		table  string
		column string
	}{
		{"groups", "created_by"},
		{"audit_log", "actor_user_id"},
		{"usage_events", "user_id"},
	} {
		var count int
		if err := harness.DB.QueryRowContext(ctx,
			"SELECT count(*) FROM "+reference.table+" WHERE "+reference.column+" IS NULL").Scan(&count); err != nil {
			t.Fatalf("count detached %s.%s: %v", reference.table, reference.column, err)
		}
		if count != 1 {
			t.Fatalf("detached %s.%s rows = %d, want 1", reference.table, reference.column, count)
		}
	}

	var detail string
	if err := harness.DB.QueryRowContext(ctx,
		"SELECT detail FROM audit_log WHERE action = 'account.delete'").Scan(&detail); err != nil {
		t.Fatalf("read preserved audit detail: %v", err)
	}
	if detail != "kept detail" {
		t.Fatalf("preserved audit detail = %q, want kept detail", detail)
	}
}

func TestSchemaGroupDeletionCascadesMemberships(t *testing.T) {
	harness := postgres.New(t)
	migrateConstraintSchema(t, harness)
	ctx := context.Background()

	owner := seedConstraintUser(t, harness.DB, "group-delete-owner@example.test")
	keepGroup := seedConstraintGroup(t, harness.DB, "invite-keep", owner)
	deleteGroup := seedConstraintGroup(t, harness.DB, "invite-remove", owner)
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, created_at)
		VALUES ($1, $2, 100), ($3, $2, 100)
	`, keepGroup, owner, deleteGroup); err != nil {
		t.Fatalf("insert memberships: %v", err)
	}

	if _, err := harness.DB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", deleteGroup); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	var removed, kept int
	if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM group_members WHERE group_id = $1", deleteGroup).Scan(&removed); err != nil {
		t.Fatalf("count removed memberships: %v", err)
	}
	if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM group_members WHERE group_id = $1", keepGroup).Scan(&kept); err != nil {
		t.Fatalf("count kept memberships: %v", err)
	}
	if removed != 0 || kept != 1 {
		t.Fatalf("memberships removed/kept = %d/%d, want 0/1", removed, kept)
	}
}

func TestSchemaWritesAreAtomic(t *testing.T) {
	harness := postgres.New(t)
	migrateConstraintSchema(t, harness)
	ctx := context.Background()

	t.Run("rollback leaves no partial rows", func(t *testing.T) {
		transaction, err := harness.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx() error = %v", err)
		}
		var userID int64
		if err := transaction.QueryRowContext(ctx,
			"INSERT INTO users (email, created_at) VALUES ('atomic-rollback@example.test', 100) RETURNING id").Scan(&userID); err != nil {
			t.Fatalf("insert user in transaction: %v", err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('atomic-session', $1, 100, 200)", userID); err != nil {
			t.Fatalf("insert session in transaction: %v", err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('atomic-session', $1, 100, 200)", userID); err == nil {
			t.Fatal("duplicate session inside transaction was accepted")
		}
		if err := transaction.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("Rollback() error = %v", err)
		}

		for _, query := range []string{
			"SELECT count(*) FROM users WHERE email = 'atomic-rollback@example.test'",
			"SELECT count(*) FROM sessions WHERE id = 'atomic-session'",
		} {
			var count int
			if err := harness.DB.QueryRowContext(ctx, query).Scan(&count); err != nil {
				t.Fatalf("count after rollback: %v", err)
			}
			if count != 0 {
				t.Fatalf("%s rows after rollback = %d, want 0", query, count)
			}
		}
	})

	t.Run("commit persists dependent rows together", func(t *testing.T) {
		transaction, err := harness.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx() error = %v", err)
		}
		var userID int64
		if err := transaction.QueryRowContext(ctx,
			"INSERT INTO users (email, created_at) VALUES ('atomic-commit@example.test', 100) RETURNING id").Scan(&userID); err != nil {
			t.Fatalf("insert user in transaction: %v", err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('atomic-commit-session', $1, 100, 200)", userID); err != nil {
			t.Fatalf("insert session in transaction: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		for _, query := range []string{
			"SELECT count(*) FROM users WHERE email = 'atomic-commit@example.test'",
			"SELECT count(*) FROM sessions WHERE id = 'atomic-commit-session'",
		} {
			var count int
			if err := harness.DB.QueryRowContext(ctx, query).Scan(&count); err != nil {
				t.Fatalf("count after commit: %v", err)
			}
			if count != 1 {
				t.Fatalf("%s rows after commit = %d, want 1", query, count)
			}
		}
	})

	t.Run("idempotent membership insert keeps one row", func(t *testing.T) {
		owner := seedConstraintUser(t, harness.DB, "atomic-owner@example.test")
		groupID := seedConstraintGroup(t, harness.DB, "invite-atomic", owner)
		result, err := harness.DB.ExecContext(ctx, `
			INSERT INTO group_members (group_id, user_id, created_at)
			VALUES ($1, $2, 100)
			ON CONFLICT (group_id, user_id) DO NOTHING
		`, groupID, owner)
		if err != nil {
			t.Fatalf("first membership insert: %v", err)
		}
		if inserted, _ := result.RowsAffected(); inserted != 1 {
			t.Fatalf("first insert rows = %d, want 1", inserted)
		}
		result, err = harness.DB.ExecContext(ctx, `
			INSERT INTO group_members (group_id, user_id, created_at)
			VALUES ($1, $2, 101)
			ON CONFLICT (group_id, user_id) DO NOTHING
		`, groupID, owner)
		if err != nil {
			t.Fatalf("second membership insert: %v", err)
		}
		if inserted, _ := result.RowsAffected(); inserted != 0 {
			t.Fatalf("second insert rows = %d, want 0", inserted)
		}
		var count int
		if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM group_members WHERE group_id = $1", groupID).Scan(&count); err != nil {
			t.Fatalf("count memberships: %v", err)
		}
		if count != 1 {
			t.Fatalf("membership rows = %d, want 1", count)
		}
	})
}
