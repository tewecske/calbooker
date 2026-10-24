//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestTeamRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(other) error = %v", err)
	}

	description := "U10"
	created, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, Description: &description, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	if created.ID <= 0 || created.Version != 0 || created.Description == nil || *created.Description != "U10" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Bad", BirthYearFrom: 2016, BirthYearTo: 2015, CreatedAt: 101}); err == nil {
		t.Fatal("reversed birth years were stored")
	}

	// Every read is scoped to the group.
	if _, err := teams.FindTeam(ctx, other.ID, created.ID); !errors.Is(err, service.ErrTeamNotFound) {
		t.Fatalf("FindTeam(other group) error = %v, want ErrTeamNotFound", err)
	}
	if list, err := teams.ListTeams(ctx, other.ID); err != nil || len(list) != 0 {
		t.Fatalf("ListTeams(other group) = %+v, %v; want none", list, err)
	}

	created.Name = "Lions A"
	updated, err := teams.UpdateTeam(ctx, created, 0)
	if err != nil || updated.Name != "Lions A" || updated.Version != 1 {
		t.Fatalf("UpdateTeam() = %+v, %v", updated, err)
	}
	if _, err := teams.UpdateTeam(ctx, created, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale UpdateTeam() error = %v, want ErrOptimisticLockConflict", err)
	}
	foreign := updated
	foreign.GroupID = other.ID
	if _, err := teams.UpdateTeam(ctx, foreign, updated.Version); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("UpdateTeam(other group) error = %v, want ErrRecordNotFound", err)
	}
	archivedAt := int64(200)
	updated.ArchivedAt = &archivedAt
	archived, err := teams.UpdateTeam(ctx, updated, updated.Version)
	if err != nil || !archived.Archived() {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	if _, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Tigers", BirthYearFrom: 2017, BirthYearTo: 2017, CreatedAt: 102}); err != nil {
		t.Fatalf("CreateTeam(second) error = %v", err)
	}
	list, err := teams.ListTeams(ctx, group.ID)
	if err != nil || len(list) != 2 || list[0].Team.Name != "Tigers" || !list[1].Team.Archived() {
		t.Fatalf("ListTeams() = %+v, %v; want active teams first", list, err)
	}
}

func TestTeamRosterFollowsGroupRoles(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	playerID := insertIntegrationGuest(t, harness)

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: playerID, Roles: []string{service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}
	team, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}

	if err := teams.AddRosterEntry(ctx, group.ID, team.ID, playerID, service.GroupRoleParent, 103); !errors.Is(err, service.ErrRosterRoleMissing) {
		t.Fatalf("roster entry without the group role error = %v, want ErrRosterRoleMissing", err)
	}
	if err := teams.AddRosterEntry(ctx, group.ID, team.ID, adminID, service.GroupRolePlayer, 103); !errors.Is(err, service.ErrRosterRoleMissing) {
		t.Fatalf("admin as player error = %v, want ErrRosterRoleMissing", err)
	}
	for range 2 {
		if err := teams.AddRosterEntry(ctx, group.ID, team.ID, playerID, service.GroupRolePlayer, 103); err != nil {
			t.Fatalf("AddRosterEntry() error = %v, want idempotent success", err)
		}
	}
	roster, err := teams.ListRoster(ctx, group.ID, team.ID)
	if err != nil || len(roster) != 1 || roster[0].UserID != playerID || roster[0].Kind != service.UserKindGuest {
		t.Fatalf("ListRoster() = %+v, %v", roster, err)
	}
	if list, _ := teams.ListTeams(ctx, group.ID); list[0].Players != 1 || list[0].Parents != 0 {
		t.Fatalf("counts = %+v, want one player", list[0])
	}

	// Taking the group role away takes the member off every roster.
	membership, err := memberships.FindMembership(ctx, group.ID, playerID)
	if err != nil {
		t.Fatalf("FindMembership() error = %v", err)
	}
	if _, err := memberships.RemoveMemberRole(ctx, group.ID, playerID, service.GroupRolePlayer, membership.Version); err != nil {
		t.Fatalf("RemoveMemberRole() error = %v", err)
	}
	if roster, _ := teams.ListRoster(ctx, group.ID, team.ID); len(roster) != 0 {
		t.Fatalf("roster after role removal = %+v, want empty", roster)
	}
	if err := teams.RemoveRosterEntry(ctx, group.ID, team.ID, playerID, service.GroupRolePlayer); !errors.Is(err, service.ErrRosterEntryNotFound) {
		t.Fatalf("RemoveRosterEntry(missing) error = %v, want ErrRosterEntryNotFound", err)
	}
}

// TestGuestRetentionKeepsProfilesAndGroupGuests checks that guest cleanup only
// deletes old guests without group data, never profiles or accounts.
func TestGuestRetentionKeepsProfilesAndGroupGuests(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	claims, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewGuestClaimCodeRepository() error = %v", err)
	}
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	lonelyGuest := insertIntegrationGuest(t, harness)
	memberGuest := insertIntegrationGuest(t, harness)
	var profileID int64
	if err := harness.DB.QueryRowContext(ctx, "INSERT INTO users (kind, display_name, created_at) VALUES ('profile', 'Kid', 1) RETURNING id").Scan(&profileID); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: memberGuest, Roles: []string{service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership() error = %v", err)
	}

	if _, err := claims.DeleteEmptyAbandonedGuests(ctx, 1_000_000); err != nil {
		t.Fatalf("DeleteExpiredGuests() error = %v", err)
	}
	for id, want := range map[int64]bool{adminID: true, lonelyGuest: false, memberGuest: true, profileID: true} {
		var exists bool
		if err := harness.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", id).Scan(&exists); err != nil {
			t.Fatalf("lookup %d: %v", id, err)
		}
		if exists != want {
			t.Errorf("user %d exists = %v, want %v", id, exists, want)
		}
	}
}
