//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestMembershipRoleUpdateDistinguishesStaleFromMissing(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "lock-admin@example.test")
	memberID := insertIntegrationUser(t, harness, "lock-member@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Locked Roles", NameNorm: "locked roles", CreatedBy: &adminID, CreatedAt: 100,
	}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberID, Roles: []string{service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}

	member, err := memberships.FindMembership(ctx, group.ID, memberID)
	if err != nil {
		t.Fatalf("FindMembership() error = %v", err)
	}

	if _, err := memberships.AddMemberRole(ctx, group.ID, memberID, service.GroupRoleAdmin, member.Version+1); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale role update error = %v, want ErrOptimisticLockConflict", err)
	}

	updated, err := memberships.AddMemberRole(ctx, group.ID, memberID, service.GroupRoleAdmin, member.Version)
	if err != nil {
		t.Fatalf("AddMemberRole() error = %v", err)
	}
	if updated.Version != member.Version+1 || updated.Roles[0] != service.GroupRoleAdmin {
		t.Fatalf("updated = %+v, want incremented version and admin role", updated)
	}

	if _, err := memberships.RemoveMemberRole(ctx, group.ID, 999_999, service.GroupRolePlayer, 0); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("missing membership role update error = %v, want ErrMembershipNotFound", err)
	} else if errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("missing membership reported a conflict: %v", err)
	}
}

func TestMembershipDeleteDistinguishesStaleFromMissing(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "delete-admin@example.test")
	memberID := insertIntegrationUser(t, harness, "delete-member@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Locked Deletes", NameNorm: "locked deletes", CreatedBy: &adminID, CreatedAt: 100,
	}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberID, Roles: []string{service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}

	member, err := memberships.FindMembership(ctx, group.ID, memberID)
	if err != nil {
		t.Fatalf("FindMembership() error = %v", err)
	}

	if err := memberships.DeleteMembership(ctx, group.ID, memberID, member.Version+1); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale delete error = %v, want ErrOptimisticLockConflict", err)
	} else if errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("stale delete reported not-found: %v", err)
	}

	if err := memberships.DeleteMembership(ctx, group.ID, memberID, member.Version); err != nil {
		t.Fatalf("DeleteMembership() error = %v", err)
	}

	if err := memberships.DeleteMembership(ctx, group.ID, memberID, 0); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("missing delete error = %v, want ErrMembershipNotFound", err)
	} else if errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("missing delete reported a conflict: %v", err)
	}
}
