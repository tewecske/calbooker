//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
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

	group := service.Group{Name: "Example", NameNorm: "example", TimeZone: "Europe/Vienna", CreatedBy: &ownerID, CreatedAt: 100}
	created, membership, err := memberships.CreateGroupWithAdmin(ctx, group, ownerID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if created.ID <= 0 || created.TimeZone != "Europe/Vienna" || !slices.Equal(membership.Roles, []string{service.GroupRoleAdmin}) || membership.UserID != ownerID {
		t.Fatalf("created = %+v membership = %+v, want owner admin in Vienna", created, membership)
	}

	summaries, err := groups.ListGroupsForUser(ctx, ownerID)
	if err != nil {
		t.Fatalf("ListGroupsForUser() error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].Group.ID != created.ID || summaries[0].MemberCount != 1 || !slices.Equal(summaries[0].Roles, []string{service.GroupRoleAdmin}) {
		t.Fatalf("summaries = %+v, want one admin group with one member", summaries)
	}

	empty, err := groups.ListGroupsForUser(ctx, otherID)
	if err != nil || len(empty) != 0 {
		t.Fatalf("other summaries = %+v, %v; want none", empty, err)
	}

	allForOther, err := groups.ListAllGroupsForUser(ctx, otherID)
	if err != nil {
		t.Fatalf("ListAllGroupsForUser(other) error = %v", err)
	}
	if len(allForOther) != 1 || allForOther[0].Group.ID != created.ID || len(allForOther[0].Roles) != 0 {
		t.Fatalf("other all groups = %+v, want the group without roles", allForOther)
	}

	// A group created without a time zone gets the default.
	defaulted, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Default", NameNorm: "default", CreatedAt: 100}, otherID)
	if err != nil || defaulted.TimeZone != service.DefaultGroupTimeZone {
		t.Fatalf("defaulted group = %+v, %v; want %s", defaulted, err, service.DefaultGroupTimeZone)
	}
}

func TestGroupRepositoryOptimisticLockDistinguishesMissing(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	groups := mustGroupRepository(t, harness)
	memberships := mustMembershipRepository(t, harness)
	ownerID := insertIntegrationUser(t, harness, "owner@example.test")

	created, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Locked", NameNorm: "locked", CreatedBy: &ownerID, CreatedAt: 100,
	}, ownerID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}

	created.Name = "Renamed"
	created.NameNorm = "renamed"
	created.TimeZone = "UTC"
	if _, err := groups.UpdateGroup(ctx, created, created.Version+1); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale update error = %v, want ErrOptimisticLockConflict", err)
	}
	updated, err := groups.UpdateGroup(ctx, created, created.Version)
	if err != nil {
		t.Fatalf("UpdateGroup() error = %v", err)
	}
	if updated.Version != created.Version+1 || updated.Name != "Renamed" || updated.TimeZone != "UTC" {
		t.Fatalf("updated = %+v, want incremented version, new name and time zone", updated)
	}
	if _, err := groups.UpdateGroup(ctx, service.Group{ID: 999999, Name: "x", NameNorm: "x", TimeZone: "UTC"}, 0); !errors.Is(err, service.ErrGroupNotFound) {
		t.Fatalf("missing update error = %v, want ErrGroupNotFound", err)
	}
}

func TestMembershipRolesAndLastAdmin(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	memberID := insertIntegrationUser(t, harness, "member@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{
		Name: "Team", NameNorm: "team", CreatedBy: &adminID, CreatedAt: 100,
	}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberID, Roles: []string{service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberID, CreatedAt: 101}); !errors.Is(err, service.ErrMembershipExists) {
		t.Fatalf("duplicate membership error = %v, want ErrMembershipExists", err)
	}

	// Several roles per member, each change bumps the revision.
	withCoach, err := memberships.AddMemberRole(ctx, group.ID, memberID, service.GroupRoleCoach, 0)
	if err != nil {
		t.Fatalf("AddMemberRole() error = %v", err)
	}
	if !slices.Equal(withCoach.Roles, []string{service.GroupRoleCoach, service.GroupRolePlayer}) || withCoach.Version != 1 {
		t.Fatalf("membership = %+v, want coach and player at version 1", withCoach)
	}
	if _, err := memberships.AddMemberRole(ctx, group.ID, memberID, service.GroupRoleParent, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale AddMemberRole() error = %v, want ErrOptimisticLockConflict", err)
	}
	if _, err := memberships.AddMemberRole(ctx, group.ID, 999999, service.GroupRoleParent, 0); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("missing member AddMemberRole() error = %v, want ErrMembershipNotFound", err)
	}

	admin, err := memberships.FindMembership(ctx, group.ID, adminID)
	if err != nil {
		t.Fatalf("FindMembership(admin) error = %v", err)
	}
	if _, err := memberships.RemoveMemberRole(ctx, group.ID, adminID, service.GroupRoleAdmin, admin.Version); !errors.Is(err, service.ErrLastGroupAdmin) {
		t.Fatalf("removing the last admin role error = %v, want ErrLastGroupAdmin", err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, adminID, admin.Version); !errors.Is(err, service.ErrLastGroupAdmin) {
		t.Fatalf("removing the last admin error = %v, want ErrLastGroupAdmin", err)
	}

	promoted, err := memberships.AddMemberRole(ctx, group.ID, memberID, service.GroupRoleAdmin, withCoach.Version)
	if err != nil {
		t.Fatalf("promote member error = %v", err)
	}
	demoted, err := memberships.RemoveMemberRole(ctx, group.ID, adminID, service.GroupRoleAdmin, admin.Version)
	if err != nil || len(demoted.Roles) != 0 || demoted.Version != admin.Version+1 {
		t.Fatalf("demote former last admin = %+v, %v; want no roles at the next version", demoted, err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, adminID, admin.Version); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale delete error = %v, want ErrOptimisticLockConflict", err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, adminID, demoted.Version); err != nil {
		t.Fatalf("DeleteMembership() error = %v", err)
	}
	if err := memberships.DeleteMembership(ctx, group.ID, adminID, demoted.Version); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("delete vanished member error = %v, want ErrMembershipNotFound", err)
	}
	if count, err := memberships.CountMembers(ctx, group.ID); err != nil || count != 1 {
		t.Fatalf("member count = %d, %v; want 1", count, err)
	}
	var orphanRoles int
	if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM group_member_roles WHERE user_id = $1", adminID).Scan(&orphanRoles); err != nil || orphanRoles != 0 {
		t.Fatalf("roles left after removal = %d, %v", orphanRoles, err)
	}
	_ = promoted
}

func TestMembershipAccessAndMemberList(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	guestID := insertIntegrationGuest(t, harness)
	outsiderID := insertIntegrationUser(t, harness, "outsider@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Access", NameNorm: "access", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: guestID, Roles: []string{service.GroupRoleParent, service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}
	team, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "U10", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	if err := teams.AddRosterEntry(ctx, group.ID, team.ID, guestID, service.GroupRolePlayer, 103); err != nil {
		t.Fatalf("AddRosterEntry() error = %v", err)
	}

	access, err := memberships.FindMemberAccess(ctx, group.ID, guestID)
	if err != nil {
		t.Fatalf("FindMemberAccess() error = %v", err)
	}
	if !slices.Equal(access.Roles, []string{service.GroupRolePlayer, service.GroupRoleParent}) || !slices.Equal(access.OwnTeams, []service.TeamRole{{TeamID: team.ID, Role: service.GroupRolePlayer}}) {
		t.Fatalf("access = %+v, want player and parent on U10", access)
	}
	if _, err := memberships.FindMemberAccess(ctx, group.ID, outsiderID); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("outsider access error = %v, want ErrMembershipNotFound", err)
	}
	if exists, err := memberships.GroupExists(ctx, group.ID); err != nil || !exists {
		t.Fatalf("GroupExists() = %v, %v", exists, err)
	}
	if exists, err := memberships.GroupExists(ctx, 999999); err != nil || exists {
		t.Fatalf("GroupExists(missing) = %v, %v", exists, err)
	}

	members, err := memberships.ListMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListMembers() error = %v", err)
	}
	if len(members) != 2 || members[0].UserID != adminID || !members[0].HasRole(service.GroupRoleAdmin) {
		t.Fatalf("members = %+v, want admin first", members)
	}
	if members[1].Kind != service.UserKindGuest || !slices.Equal(members[1].TeamIDs, []int64{team.ID}) || members[1].Email != nil {
		t.Fatalf("guest member = %+v, want guest kind and team", members[1])
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

func mustTeamRepository(t *testing.T, harness *postgres.Harness) *TeamRepository {
	t.Helper()
	repository, err := NewTeamRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewTeamRepository() error = %v", err)
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

func insertIntegrationGuest(t *testing.T, harness *postgres.Harness) int64 {
	t.Helper()
	user := fixtures.NewUser()
	user.Email = nil
	user.Username = nil
	user.Kind = string(service.UserKindGuest)
	id, err := fixtures.InsertUser(context.Background(), harness.DB, user)
	if err != nil {
		t.Fatalf("InsertUser(guest) error = %v", err)
	}
	return id
}
