//go:build integration

package fixtures

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestStoreFixturesInsertExplicitRecords(t *testing.T) {
	harness := postgres.New(t)
	runner, err := store.NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	ctx := context.Background()
	userID, err := InsertUser(ctx, harness.DB, NewUser())
	if err != nil {
		t.Fatalf("InsertUser() error = %v", err)
	}
	groupID, err := InsertGroup(ctx, harness.DB, NewGroup(userID))
	if err != nil {
		t.Fatalf("InsertGroup() error = %v", err)
	}
	if err := InsertMembership(ctx, harness.DB, groupID, userID, "admin", 1, 0); err != nil {
		t.Fatalf("InsertMembership() error = %v", err)
	}
}
