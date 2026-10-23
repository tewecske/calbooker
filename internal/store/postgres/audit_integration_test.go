//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	testpostgres "github.com/tewecske/calbooker/tests/postgres"
)

func TestAuditLogRepositoryPreservesHistoryAfterActorDeletion(t *testing.T) {
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

	users, err := NewUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	email := "actor@example.test"
	actor, err := users.CreateUser(ctx, service.User{Email: &email, Theme: "light", Locale: "en", CreatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}

	repository, err := NewAuditLogRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	actorID := actor.ID
	actorEmail := email
	targetType, targetID, detail, ip := "user", "99", "created account", "203.0.113.9"
	if err := repository.CreateAuditEntry(ctx, service.AuditEntry{
		OccurredAt:  100,
		ActorUserID: &actorID,
		ActorEmail:  &actorEmail,
		Action:      service.AuditActionAccountUpdated,
		TargetType:  &targetType,
		TargetID:    &targetID,
		Detail:      &detail,
		IP:          &ip,
	}); err != nil {
		t.Fatalf("CreateAuditEntry() error = %v", err)
	}

	if err := users.DeleteUser(ctx, actor.ID, actor.Version); err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}

	var (
		action      string
		storedActor *int64
		snapshot    *string
	)
	if err := harness.DB.QueryRowContext(ctx, "SELECT action, actor_user_id, actor_email FROM audit_log ORDER BY id DESC LIMIT 1").Scan(&action, &storedActor, &snapshot); err != nil {
		t.Fatalf("query audit row: %v", err)
	}
	if action != service.AuditActionAccountUpdated {
		t.Fatalf("action = %q, want %q", action, service.AuditActionAccountUpdated)
	}
	if storedActor != nil {
		t.Fatalf("actor_user_id = %v, want NULL after deletion", *storedActor)
	}
	if snapshot == nil || *snapshot != actorEmail {
		t.Fatalf("actor_email snapshot = %v, want %q", snapshot, actorEmail)
	}
}

func TestAuditLogRepositoryFiltersAndPaginates(t *testing.T) {
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
	users, err := NewUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	actorEmail := "actor@example.test"
	actor, err := users.CreateUser(ctx, service.User{Email: &actorEmail, Theme: "light", Locale: "en", CreatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewAuditLogRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	actorID := actor.ID
	targetType := "user"
	for index, action := range []string{service.AuditActionAdminGranted, service.AuditActionAccountUpdated, service.AuditActionAccountDeleted} {
		targetID := "10" + string(rune('0'+index))
		if err := repository.CreateAuditEntry(ctx, service.AuditEntry{
			OccurredAt:  int64(200 + index),
			ActorUserID: &actorID,
			ActorEmail:  &actorEmail,
			Action:      action,
			TargetType:  &targetType,
			TargetID:    &targetID,
		}); err != nil {
			t.Fatalf("CreateAuditEntry(%s) error = %v", action, err)
		}
	}

	page, err := repository.ListAuditEntries(ctx, service.AuditQuery{Size: 2})
	if err != nil {
		t.Fatalf("ListAuditEntries() error = %v", err)
	}
	if page.Total != 3 || len(page.Entries) != 2 {
		t.Fatalf("page = total %d rows %d, want 3/2", page.Total, len(page.Entries))
	}
	if page.Entries[0].Action != service.AuditActionAccountDeleted {
		t.Fatalf("newest entry = %q, want deleted first", page.Entries[0].Action)
	}

	filtered, err := repository.ListAuditEntries(ctx, service.AuditQuery{Action: "updated", Size: 10})
	if err != nil {
		t.Fatalf("ListAuditEntries(action) error = %v", err)
	}
	if filtered.Total != 1 || filtered.Entries[0].Action != service.AuditActionAccountUpdated {
		t.Fatalf("action filter = %+v, want one updated entry", filtered)
	}

	byActor, err := repository.ListAuditEntries(ctx, service.AuditQuery{Actor: "ACTOR@example", Size: 10})
	if err != nil {
		t.Fatalf("ListAuditEntries(actor) error = %v", err)
	}
	if byActor.Total != 3 {
		t.Fatalf("actor filter total = %d, want 3", byActor.Total)
	}

	byTarget, err := repository.ListAuditEntries(ctx, service.AuditQuery{Target: "101", Size: 10})
	if err != nil {
		t.Fatalf("ListAuditEntries(target) error = %v", err)
	}
	if byTarget.Total != 1 {
		t.Fatalf("target filter total = %d, want 1", byTarget.Total)
	}

	// History survives actor deletion: the snapshot email remains filterable.
	if err := users.DeleteUser(ctx, actor.ID, actor.Version); err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}
	afterDelete, err := repository.ListAuditEntries(ctx, service.AuditQuery{Actor: "actor@example.test", Size: 10})
	if err != nil {
		t.Fatalf("ListAuditEntries(after delete) error = %v", err)
	}
	if afterDelete.Total != 3 {
		t.Fatalf("after-delete total = %d, want 3", afterDelete.Total)
	}
}
