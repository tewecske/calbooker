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

func TestSessionRepositoryListsAndCountsActiveSessions(t *testing.T) {
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
	email := "sessions@example.test"
	user, err := users.CreateUser(ctx, service.User{Email: &email, Theme: "light", Locale: "en", CreatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	active := service.Session{ID: "active-credential", UserID: user.ID, CreatedAt: now, ExpiresAt: now + 3600}
	expired := service.Session{ID: "expired-credential", UserID: user.ID, CreatedAt: now - 7200, ExpiresAt: now - 3600}
	if err := sessions.CreateSession(ctx, active); err != nil {
		t.Fatalf("CreateSession(active) error = %v", err)
	}
	if err := sessions.CreateSession(ctx, expired); err != nil {
		t.Fatalf("CreateSession(expired) error = %v", err)
	}

	count, err := sessions.CountActiveSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("CountActiveSessions() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("active count = %d, want 1", count)
	}
	listed, err := sessions.ListActiveSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListActiveSessions() error = %v", err)
	}
	if len(listed) != 1 || listed[0].ExpiresAt != active.ExpiresAt {
		t.Fatalf("active sessions = %+v, want the unexpired session", listed)
	}

	if err := sessions.RevokeUserSessions(ctx, user.ID); err != nil {
		t.Fatalf("RevokeUserSessions() error = %v", err)
	}
	count, err = sessions.CountActiveSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("CountActiveSessions() after revoke error = %v", err)
	}
	if count != 0 {
		t.Fatalf("active count after revoke = %d, want 0", count)
	}
}
