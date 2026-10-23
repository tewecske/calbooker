//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	testpostgres "github.com/tewecske/calbooker/tests/postgres"
)

func TestAdminUserRepositoryPaginatesAndCounts(t *testing.T) {
	harness, repository := newAdminUserHarness(t)
	ctx := context.Background()
	for index := 0; index < 25; index++ {
		email := fmt.Sprintf("page-%02d@example.test", index)
		verified := int64(100 + index)
		user := service.User{Email: &email, Theme: "light", Locale: "en", CreatedAt: int64(100 + index), EmailVerifiedAt: &verified}
		if _, err := repository.CreateUser(ctx, user); err != nil {
			t.Fatalf("CreateUser(%d) error = %v", index, err)
		}
	}

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	page, err := listed.ListUsers(ctx, service.AdminUserQuery{Size: 10, Page: 1, Sort: service.AdminSortCreatedAt, Direction: service.AdminDirectionAsc})
	if err != nil {
		t.Fatalf("ListUsers(page 1) error = %v", err)
	}
	if page.Total != 25 || len(page.Users) != 10 {
		t.Fatalf("page 1 = total %d rows %d, want 25/10", page.Total, len(page.Users))
	}
	if page.Users[0].CreatedAt != 100 || page.Users[9].CreatedAt != 109 {
		t.Fatalf("page 1 created_at = %d..%d, want 100..109", page.Users[0].CreatedAt, page.Users[9].CreatedAt)
	}

	page, err = listed.ListUsers(ctx, service.AdminUserQuery{Size: 10, Page: 3, Sort: service.AdminSortCreatedAt, Direction: service.AdminDirectionAsc})
	if err != nil {
		t.Fatalf("ListUsers(page 3) error = %v", err)
	}
	if len(page.Users) != 5 {
		t.Fatalf("page 3 rows = %d, want 5", len(page.Users))
	}
}

func TestAdminUserRepositorySearchAndFilters(t *testing.T) {
	harness, repository := newAdminUserHarness(t)
	ctx := context.Background()
	aliceEmail, bobEmail, guestName := "alice@example.test", "bob@example.test", "guest-a1b2"
	verified := int64(102)
	mustCreateAdminUser(t, ctx, repository, service.User{Email: &aliceEmail, IsAdmin: true, Theme: "light", Locale: "en", CreatedAt: 100})
	mustCreateAdminUser(t, ctx, repository, service.User{Email: &bobEmail, Theme: "light", Locale: "en", CreatedAt: 101})
	mustCreateAdminUser(t, ctx, repository, service.User{Email: strPtr("alice-extra@example.test"), Theme: "light", Locale: "en", CreatedAt: 102, EmailVerifiedAt: &verified})
	mustCreateAdminUser(t, ctx, repository, service.User{Username: &guestName, IsGuest: true, Theme: "light", Locale: "en", CreatedAt: 103})

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}

	search, err := listed.ListUsers(ctx, service.AdminUserQuery{Search: "ALICE", Size: 50})
	if err != nil {
		t.Fatalf("ListUsers(search) error = %v", err)
	}
	if search.Total != 2 {
		t.Fatalf("search total = %d, want 2", search.Total)
	}

	adminOnly, err := listed.ListUsers(ctx, service.AdminUserQuery{Admin: service.AdminFilterYes, Size: 50})
	if err != nil {
		t.Fatalf("ListUsers(admin) error = %v", err)
	}
	if adminOnly.Total != 1 || !adminOnly.Users[0].IsAdmin {
		t.Fatalf("admin filter = %+v, want one admin", adminOnly)
	}

	guests, err := listed.ListUsers(ctx, service.AdminUserQuery{Guest: service.AdminFilterYes, Size: 50})
	if err != nil {
		t.Fatalf("ListUsers(guest) error = %v", err)
	}
	if guests.Total != 1 || !guests.Users[0].IsGuest {
		t.Fatalf("guest filter = %+v, want one guest", guests)
	}

	confirmed, err := listed.ListUsers(ctx, service.AdminUserQuery{Confirmed: service.AdminFilterYes, Size: 50})
	if err != nil {
		t.Fatalf("ListUsers(confirmed) error = %v", err)
	}
	if confirmed.Total != 1 {
		t.Fatalf("confirmed filter total = %d, want 1", confirmed.Total)
	}

	unconfirmed, err := listed.ListUsers(ctx, service.AdminUserQuery{Confirmed: service.AdminFilterNo, Size: 50})
	if err != nil {
		t.Fatalf("ListUsers(unconfirmed) error = %v", err)
	}
	if unconfirmed.Total != 3 {
		t.Fatalf("unconfirmed filter total = %d, want 3", unconfirmed.Total)
	}
}

func TestAdminUserRepositorySortsWithoutInterpolatingInput(t *testing.T) {
	harness, repository := newAdminUserHarness(t)
	ctx := context.Background()
	first, second := "bravo@example.test", "alpha@example.test"
	mustCreateAdminUser(t, ctx, repository, service.User{Email: &first, Theme: "light", Locale: "en", CreatedAt: 200})
	mustCreateAdminUser(t, ctx, repository, service.User{Email: &second, Theme: "light", Locale: "en", CreatedAt: 100})

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	ascending, err := listed.ListUsers(ctx, service.AdminUserQuery{Sort: service.AdminSortEmail, Direction: service.AdminDirectionAsc, Size: 10})
	if err != nil {
		t.Fatalf("ListUsers(email asc) error = %v", err)
	}
	if ascending.Users[0].Email == nil || *ascending.Users[0].Email != second {
		t.Fatalf("email asc first = %v, want %s", ascending.Users[0].Email, second)
	}

	injected, err := listed.ListUsers(ctx, service.AdminUserQuery{Sort: "id; DROP TABLE users", Direction: "desc; DELETE", Size: 10})
	if err != nil {
		t.Fatalf("ListUsers(hostile sort) error = %v", err)
	}
	if injected.Total != 2 {
		t.Fatalf("hostile sort total = %d, want 2", injected.Total)
	}
	if injected.Users[0].CreatedAt != 200 {
		t.Fatalf("hostile sort default ordering first = %d, want 200", injected.Users[0].CreatedAt)
	}
}

func TestAdminUserRepositoryEscapesSearchWildcards(t *testing.T) {
	harness, repository := newAdminUserHarness(t)
	ctx := context.Background()
	literal, decoy := "50%off@example.test", "50xoff@example.test"
	mustCreateAdminUser(t, ctx, repository, service.User{Email: &literal, Theme: "light", Locale: "en", CreatedAt: 100})
	mustCreateAdminUser(t, ctx, repository, service.User{Email: &decoy, Theme: "light", Locale: "en", CreatedAt: 101})

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	found, err := listed.ListUsers(ctx, service.AdminUserQuery{Search: "50%off", Size: 10})
	if err != nil {
		t.Fatalf("ListUsers(search literal) error = %v", err)
	}
	if found.Total != 1 || found.Users[0].Email == nil || *found.Users[0].Email != literal {
		t.Fatalf("literal wildcard search = %+v, want only %s", found, literal)
	}
}

func newAdminUserHarness(t *testing.T) (*testpostgres.Harness, *UserRepository) {
	t.Helper()
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
	repository, err := NewUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	return harness, repository
}

func mustCreateAdminUser(t *testing.T, ctx context.Context, repository *UserRepository, user service.User) service.User {
	t.Helper()
	created, err := repository.CreateUser(ctx, user)
	if err != nil {
		t.Fatalf("CreateUser(%v) error = %v", user.Email, err)
	}
	return created
}

func strPtr(value string) *string { return &value }
