//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/fixtures"
	"github.com/tewecske/calbooker/tests/postgres"
)

func TestGroupRepositoryCreateAndList(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	groups := mustGroupRepository(t, harness)
	memberships := mustMembershipRepository(t, harness)

	ownerID := insertIntegrationUser(t, harness, "owner@example.test")
	otherID := insertIntegrationUser(t, harness, "other@example.test")

	group := service.Group{Name: "Example", NameNorm: "example", InviteCode: "INVITE000001", CreatedBy: &ownerID, CreatedAt: 100}
	created, membership, err := memberships.CreateGroupWithAdmin(ctx, group, ownerID, service.GroupRoleAdmin)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if created.ID <= 0 || membership.Role != service.GroupRoleAdmin || membership.UserID != ownerID {
		t.Fatalf("created = %+v membership = %+v, want owner admin", created, membership)
	}

	if _, _, err := memberships.CreateGroupWithAdmin(ctx, group, otherID, service.GroupRoleAdmin); !errors.Is(err, service.ErrGroupInviteTaken) {
		t.Fatalf("duplicate invite code error = %v, want ErrGroupInviteTaken", err)
	}

	summaries, err := groups.ListGroupsForUser(ctx, ownerID)
	if err != nil {
		t.Fatalf("ListGroupsForUser() error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].Group.ID != created.ID || summaries[0].MemberCount != 1 || summaries[0].Role != service.GroupRoleAdmin {
		t.Fatalf("summaries = %+v, want one admin group with one member", summaries)
	}

	empty, err := groups.ListGroupsForUser(ctx, otherID)
	if err != nil {
		t.Fatalf("ListGroupsForUser(other) error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("other summaries = %+v, want none", empty)
	}

	allForOwner, err := groups.ListAllGroupsForUser(ctx, ownerID)
	if err != nil {
		t.Fatalf("ListAllGroupsForUser(owner) error = %v", err)
	}
	if len(allForOwner) != 1 || allForOwner[0].Group.ID != created.ID || allForOwner[0].Role != service.GroupRoleAdmin {
		t.Fatalf("owner all groups = %+v, want one group with the real admin role", allForOwner)
	}

	allForOther, err := groups.ListAllGroupsForUser(ctx, otherID)
	if err != nil {
		t.Fatalf("ListAllGroupsForUser(other) error = %v", err)
	}
	if len(allForOther) != 1 || allForOther[0].Group.ID != created.ID || allForOther[0].Role != "" {
		t.Fatalf("other all groups = %+v, want the group with an empty non-member role", allForOther)
	}
}

func TestGroupRepositoryOptimisticLockDistinguishesMissing(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	groups := mustGroupRepository(t, harness)
	memberships := mustMembershipRepository(t, harness)
	ownerID := insertIntegrationUser(t, harness, "owner@example.test")

	created, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Locked", NameNorm: "locked", InviteCode: "INVITE000002", CreatedBy: &ownerID, CreatedAt: 100,
	}, ownerID, service.GroupRoleAdmin)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}

	created.Name = "Renamed"
	created.NameNorm = "renamed"
	if _, err := groups.UpdateGroup(ctx, created, created.Version+1); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale update error = %v, want ErrOptimisticLockConflict", err)
	}
	updated, err := groups.UpdateGroup(ctx, created, created.Version)
	if err != nil {
		t.Fatalf("UpdateGroup() error = %v", err)
	}
	if updated.Version != created.Version+1 || updated.Name != "Renamed" {
		t.Fatalf("updated = %+v, want incremented version and new name", updated)
	}
	if _, err := groups.UpdateGroup(ctx, service.Group{ID: 999999, Name: "x", NameNorm: "x"}, 0); !errors.Is(err, service.ErrGroupNotFound) {
		t.Fatalf("missing update error = %v, want ErrGroupNotFound", err)
	}
}

func TestMembershipRoleChangePreservesLastAdmin(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	memberID := insertIntegrationUser(t, harness, "member@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Team", NameNorm: "team", InviteCode: "INVITE000003", CreatedBy: &adminID, CreatedAt: 100,
	}, adminID, service.GroupRoleAdmin)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberID, Role: service.GroupRoleMember, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}

	admin, err := memberships.FindMembership(ctx, group.ID, adminID)
	if err != nil {
		t.Fatalf("FindMembership(admin) error = %v", err)
	}
	admin.Role = service.GroupRoleMember
	if _, err := memberships.UpdateMembershipRole(ctx, admin, admin.Version); !errors.Is(err, service.ErrLastGroupAdmin) {
		t.Fatalf("demoting last admin error = %v, want ErrLastGroupAdmin", err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, adminID, admin.Version); !errors.Is(err, service.ErrLastGroupAdmin) {
		t.Fatalf("removing last admin error = %v, want ErrLastGroupAdmin", err)
	}

	member, err := memberships.FindMembership(ctx, group.ID, memberID)
	if err != nil {
		t.Fatalf("FindMembership(member) error = %v", err)
	}
	member.Role = service.GroupRoleAdmin
	if _, err := memberships.UpdateMembershipRole(ctx, member, member.Version); err != nil {
		t.Fatalf("promote member error = %v", err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, adminID, admin.Version); err != nil {
		t.Fatalf("removing admin after promotion error = %v", err)
	}

	count, err := memberships.CountMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("CountMembers() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("member count = %d, want 1", count)
	}

	secondAdminID := insertIntegrationUser(t, harness, "second-admin@example.test")
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: secondAdminID, Role: service.GroupRoleMember, CreatedAt: 102}); err != nil {
		t.Fatalf("CreateMembership(second) error = %v", err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, secondAdminID, 99); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale delete error = %v, want ErrOptimisticLockConflict", err)
	}
}

func TestGroupRepositoryRosterVisibilityData(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	groups := mustGroupRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	memberID := insertIntegrationUser(t, harness, "member@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Roster", NameNorm: "roster", InviteCode: "INVITE000004", CreatedBy: &adminID, CreatedAt: 100,
	}, adminID, service.GroupRoleAdmin)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberID, Role: service.GroupRoleMember, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}

	members, err := memberships.ListMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListMembers() error = %v", err)
	}
	if len(members) != 2 || members[0].Role != service.GroupRoleAdmin || members[0].Version != 0 {
		t.Fatalf("members = %+v, want admin first with version", members)
	}

	found, err := groups.FindGroupByInviteCode(ctx, "INVITE000004")
	if err != nil || found.ID != group.ID {
		t.Fatalf("FindGroupByInviteCode() = %+v, %v", found, err)
	}
	if _, err := groups.FindGroupByInviteCode(ctx, "UNKNOWN00000"); !errors.Is(err, service.ErrGroupNotFound) {
		t.Fatalf("unknown invite error = %v, want ErrGroupNotFound", err)
	}
}

func migratedHarness(t *testing.T) *postgres.Harness {
	t.Helper()
	harness := postgres.New(t)
	runner, err := store.NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	if err := runner.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	return harness
}

func mustGroupRepository(t *testing.T, harness *postgres.Harness) *GroupRepository {
	t.Helper()
	repository, err := NewGroupRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewGroupRepository() error = %v", err)
	}
	return repository
}

func mustMembershipRepository(t *testing.T, harness *postgres.Harness) *GroupMembershipRepository {
	t.Helper()
	repository, err := NewGroupMembershipRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewGroupMembershipRepository() error = %v", err)
	}
	return repository
}

func insertIntegrationUser(t *testing.T, harness *postgres.Harness, email string) int64 {
	t.Helper()
	user := fixtures.NewUser()
	user.Email = fixtures.String(email)
	user.Username = nil
	id, err := fixtures.InsertUser(context.Background(), harness.DB, user)
	if err != nil {
		t.Fatalf("InsertUser(%s) error = %v", email, err)
	}
	return id
}

func TestGroupServiceRotateInviteChangesCode(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	groups := mustGroupRepository(t, harness)
	memberships := mustMembershipRepository(t, harness)
	ownerID := insertIntegrationUser(t, harness, "rotate@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Rotate", NameNorm: "rotate", InviteCode: "INVITE000099", CreatedBy: &ownerID, CreatedAt: 100,
	}, ownerID, service.GroupRoleAdmin)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	serviceGroups, err := service.NewGroupService(groups, memberships, &singleUserRepository{userID: ownerID})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	before := group.InviteCode
	after, err := serviceGroups.RotateInvite(ctx, ownerID, group.ID)
	if err != nil {
		t.Fatalf("RotateInvite() error = %v", err)
	}
	if after == before || after == "" {
		t.Fatalf("rotated code = %q, want a new code different from %q", after, before)
	}
	persisted, err := groups.FindGroupByID(ctx, group.ID)
	if err != nil {
		t.Fatalf("FindGroupByID() error = %v", err)
	}
	if persisted.InviteCode != after {
		t.Fatalf("persisted invite code = %q, want %q", persisted.InviteCode, after)
	}
	if _, err := groups.FindGroupByInviteCode(ctx, before); err == nil {
		t.Fatal("previous invite code is still redeemable")
	}
}

type singleUserRepository struct{ userID int64 }

func (r *singleUserRepository) CreateUser(context.Context, service.User) (service.User, error) {
	return service.User{}, nil
}

func (r *singleUserRepository) FindUserByID(context.Context, int64) (service.User, error) {
	return service.User{ID: r.userID}, nil
}

func (r *singleUserRepository) FindUserByEmail(context.Context, string) (service.User, error) {
	return service.User{}, service.ErrUserNotFound
}

func (r *singleUserRepository) FindUserByUsername(context.Context, string) (service.User, error) {
	return service.User{}, service.ErrUserNotFound
}

func (r *singleUserRepository) UpdateUser(context.Context, service.User, int64) (service.User, error) {
	return service.User{}, service.ErrUserNotFound
}

func (r *singleUserRepository) DeleteUser(context.Context, int64, int64) error { return nil }
