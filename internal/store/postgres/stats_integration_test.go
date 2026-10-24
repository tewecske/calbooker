//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	testfixtures "github.com/tewecske/calbooker/tests/fixtures"
	testpostgres "github.com/tewecske/calbooker/tests/postgres"
)

func TestDatastoreStatsRepositoryCountsSafely(t *testing.T) {
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

	confirmed := int64(100)
	guestFixture := testfixtures.NewUser()
	guestFixture.Email = nil
	guestFixture.Username = nil
	guestFixture.Kind = "guest"
	if _, err := testfixtures.InsertUser(ctx, harness.DB, guestFixture); err != nil {
		t.Fatal(err)
	}
	adminFixture := testfixtures.NewUser()
	adminFixture.Email = testfixtures.String("admin@example.test")
	adminFixture.Username = testfixtures.String("admin-account")
	adminFixture.IsAdmin = true
	adminFixture.EmailVerifiedAt = &confirmed
	if _, err := testfixtures.InsertUser(ctx, harness.DB, adminFixture); err != nil {
		t.Fatal(err)
	}
	unconfirmedFixture := testfixtures.NewUser()
	unconfirmedFixture.Email = testfixtures.String("unconfirmed@example.test")
	unconfirmedFixture.Username = testfixtures.String("unconfirmed-account")
	if _, err := testfixtures.InsertUser(ctx, harness.DB, unconfirmedFixture); err != nil {
		t.Fatal(err)
	}
	confirmedFixture := testfixtures.NewUser()
	confirmedFixture.Email = testfixtures.String("confirmed@example.test")
	confirmedFixture.Username = testfixtures.String("confirmed-account")
	confirmedFixture.EmailVerifiedAt = &confirmed
	if _, err := testfixtures.InsertUser(ctx, harness.DB, confirmedFixture); err != nil {
		t.Fatal(err)
	}

	now := int64(1_000_000)
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at, revoked_at)
		VALUES
			('active-session', 1, 1, $1, NULL),
			('expired-session', 1, 1, 100, NULL),
			('revoked-session', 1, 1, $1, 50)
	`, now+10_000); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, action, user_id, created_at, expires_at, consumed_at)
		VALUES ('pending-state', 'example', 'sign-in', NULL, 1, 100, NULL)
	`); err != nil {
		t.Fatal(err)
	}
	repository, err := NewDatastoreStatsRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := repository.CountAccounts(ctx)
	if err != nil {
		t.Fatalf("CountAccounts() error = %v", err)
	}
	if accounts.Total != 4 || accounts.Guests != 1 || accounts.Administrators != 1 || accounts.Unconfirmed != 1 {
		t.Fatalf("accounts = %+v, want total 4 guests 1 admins 1 unconfirmed 1", accounts)
	}
	active, err := repository.CountActiveSessions(ctx, now)
	if err != nil || active != 1 {
		t.Fatalf("active sessions = %d, %v; want 1", active, err)
	}
	expired, err := repository.CountExpiredSessions(ctx, now)
	if err != nil || expired != 2 {
		t.Fatalf("expired sessions = %d, %v; want 2", expired, err)
	}
	states, err := repository.CountExpiredOAuthStates(ctx, now)
	if err != nil || states != 1 {
		t.Fatalf("expired oauth states = %d, %v; want 1", states, err)
	}
}
