//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

// TestAccountDeletionPreservesSharedGroup verifies that deleting an account
// removes its memberships but keeps the group, its remaining members, and the
// group's shared resources alive.
func TestAccountDeletionPreservesSharedGroup(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	groups := mustGroupRepository(t, harness)

	creatorID := insertIntegrationUser(t, harness, "creator@example.test")
	secondAdminID := insertIntegrationUser(t, harness, "second-admin@example.test")
	memberID := insertIntegrationUser(t, harness, "member@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Shared", NameNorm: "shared", InviteCode: "INVITE000071", CreatedBy: &creatorID, CreatedAt: 100,
	}, creatorID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: secondAdminID, Roles: []string{service.GroupRoleAdmin}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership(second admin) error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberID, Roles: []string{service.GroupRolePlayer}, CreatedAt: 102}); err != nil {
		t.Fatalf("CreateMembership(member) error = %v", err)
	}

	if _, err := harness.DB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", creatorID); err != nil {
		t.Fatalf("delete creator account: %v", err)
	}

	surviving, err := groups.FindGroupByID(ctx, group.ID)
	if err != nil {
		t.Fatalf("surviving group error = %v, want group to remain", err)
	}
	if surviving.CreatedBy != nil {
		t.Fatalf("created_by = %v, want NULL after creator deletion", surviving.CreatedBy)
	}
	if surviving.InviteCode != "INVITE000071" {
		t.Fatalf("invite code = %q, want preserved", surviving.InviteCode)
	}

	if _, err := memberships.FindMembership(ctx, group.ID, creatorID); !isMembershipNotFound(err) {
		t.Fatalf("creator membership error = %v, want not found", err)
	}
	count, err := memberships.CountMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("CountMembers() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("member count = %d, want 2 remaining members", count)
	}

	// Removing the ordinary member leaves a single administrator whose own
	// removal must then be refused to preserve the last-administrator rule.
	if err := memberships.DeleteMembership(ctx, group.ID, memberID, 0); err != nil {
		t.Fatalf("remove ordinary member error = %v", err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, secondAdminID, 0); err == nil {
		t.Fatal("removing the last administrator was accepted")
	}
}

func isMembershipNotFound(err error) bool {
	return err != nil && err.Error() == service.ErrMembershipNotFound.Error()
}
