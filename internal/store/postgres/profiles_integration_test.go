//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/tests/postgres"
)

func mustProfileRepository(t *testing.T, harness *postgres.Harness) *ProfileRepository {
	t.Helper()
	repository, err := NewProfileRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewProfileRepository() error = %v", err)
	}
	return repository
}

// profileClub is a group with an admin, a parent (guest), and a team.
type profileClub struct {
	group    service.Group
	team     service.Team
	adminID  int64
	parentID int64
}

func newProfileClub(t *testing.T, harness *postgres.Harness) profileClub {
	t.Helper()
	ctx := context.Background()
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	club := profileClub{adminID: insertIntegrationUser(t, harness, "profile-admin@example.test"), parentID: insertIntegrationGuest(t, harness)}
	var err error
	if club.group, _, err = memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, club.adminID); err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: club.group.ID, UserID: club.parentID, Roles: []string{service.GroupRoleParent}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership(parent) error = %v", err)
	}
	if club.team, err = teams.CreateTeam(ctx, service.Team{GroupID: club.group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	return club
}

func TestProfileRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	profiles := mustProfileRepository(t, harness)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	club := newProfileClub(t, harness)

	created, err := profiles.CreateProfile(ctx, service.ChildProfile{GroupID: club.group.ID, DisplayName: "Anna", BirthYear: 2016, CreatedBy: &club.parentID, CreatedAt: 200}, club.parentID, club.team.ID)
	if err != nil || created.UserID <= 0 || created.DisplayName != "Anna" || created.BirthYear != 2016 {
		t.Fatalf("CreateProfile() = %+v, %v", created, err)
	}
	var kind string
	if err := harness.DB.QueryRowContext(ctx, "SELECT kind FROM users WHERE id = $1", created.UserID).Scan(&kind); err != nil || kind != "profile" {
		t.Fatalf("profile kind = %q, %v", kind, err)
	}
	membership, err := memberships.FindMembership(ctx, club.group.ID, created.UserID)
	if err != nil || !slices.Equal(membership.Roles, []string{service.GroupRolePlayer}) {
		t.Fatalf("profile membership = %+v, %v; want a player", membership, err)
	}

	// The guardian source of GroupAccess.
	if ids, err := profiles.ChildUserIDs(ctx, club.group.ID, club.parentID); err != nil || !slices.Equal(ids, []int64{created.UserID}) {
		t.Fatalf("ChildUserIDs() = %v, %v", ids, err)
	}
	if ids, err := profiles.TeamIDs(ctx, club.group.ID, club.parentID); err != nil || !slices.Equal(ids, []int64{club.team.ID}) {
		t.Fatalf("TeamIDs() = %v, %v", ids, err)
	}

	// The linked parent appears on the child's team as a parent, derived.
	roster, err := teams.ListRoster(ctx, club.group.ID, club.team.ID)
	if err != nil || len(roster) != 2 {
		t.Fatalf("roster = %+v, %v; want the child and the linked parent", roster, err)
	}
	linked := roster[1]
	if linked.UserID != club.parentID || linked.Role != service.GroupRoleParent || !linked.Linked || !slices.Equal(linked.ChildUserIDs, []int64{created.UserID}) {
		t.Fatalf("linked parent = %+v", linked)
	}
	summaries, err := teams.ListTeams(ctx, club.group.ID)
	if err != nil || len(summaries) != 1 || summaries[0].Players != 1 || summaries[0].Parents != 1 {
		t.Fatalf("ListTeams() = %+v, %v; want one player and one parent", summaries, err)
	}
	// A parent also on the roster themselves counts once and is not linked.
	if err := teams.AddRosterEntry(ctx, club.group.ID, club.team.ID, club.parentID, service.GroupRoleParent, 201); err != nil {
		t.Fatalf("AddRosterEntry(parent) error = %v", err)
	}
	roster, _ = teams.ListRoster(ctx, club.group.ID, club.team.ID)
	if len(roster) != 2 || roster[1].Linked || !slices.Equal(roster[1].ChildUserIDs, []int64{created.UserID}) {
		t.Fatalf("roster with explicit parent = %+v", roster)
	}
	if summaries, _ := teams.ListTeams(ctx, club.group.ID); summaries[0].Parents != 1 {
		t.Fatalf("parents = %d, want 1", summaries[0].Parents)
	}

	children, err := profiles.ListChildren(ctx, club.group.ID)
	if err != nil || len(children) != 1 || children[0].Profile == nil || children[0].Profile.BirthYear != 2016 ||
		len(children[0].Guardians) != 1 || children[0].Guardians[0].UserID != club.parentID ||
		len(children[0].Teams) != 1 || children[0].Teams[0].Name != "Lions" {
		t.Fatalf("ListChildren() = %+v, %v", children, err)
	}

	created.DisplayName, created.BirthYear = "Anna B", 2015
	updated, err := profiles.UpdateProfile(ctx, created, 0)
	if err != nil || updated.DisplayName != "Anna B" || updated.BirthYear != 2015 || updated.Version != 1 {
		t.Fatalf("UpdateProfile() = %+v, %v", updated, err)
	}
	if _, err := profiles.UpdateProfile(ctx, created, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale UpdateProfile() error = %v, want conflict", err)
	}
	if err := profiles.DeleteProfile(ctx, club.group.ID, created.UserID, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale DeleteProfile() error = %v, want conflict", err)
	}
	if err := profiles.DeleteProfile(ctx, club.group.ID+1000, created.UserID, 1); !errors.Is(err, service.ErrProfileNotFound) {
		t.Fatalf("DeleteProfile(other group) error = %v, want not found", err)
	}
	if err := profiles.DeleteProfile(ctx, club.group.ID, created.UserID, 1); err != nil {
		t.Fatalf("DeleteProfile() error = %v", err)
	}
	// Everything about the profile is gone.
	for _, query := range []string{
		"SELECT count(*) FROM users WHERE id = $1",
		"SELECT count(*) FROM group_members WHERE user_id = $1",
		"SELECT count(*) FROM team_members WHERE user_id = $1",
		"SELECT count(*) FROM guardians WHERE child_user_id = $1",
		"SELECT count(*) FROM child_profiles WHERE user_id = $1",
	} {
		var count int
		if err := harness.DB.QueryRowContext(ctx, query, created.UserID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s = %d, %v; want 0", query, count, err)
		}
	}
	if err := profiles.DeleteProfile(ctx, club.group.ID, created.UserID, 1); !errors.Is(err, service.ErrProfileNotFound) {
		t.Fatalf("second DeleteProfile() error = %v, want not found (never a conflict)", err)
	}
}

func TestGuardianLinksFollowTheParentRole(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	profiles := mustProfileRepository(t, harness)
	memberships := mustMembershipRepository(t, harness)
	club := newProfileClub(t, harness)

	child, err := profiles.CreateProfile(ctx, service.ChildProfile{GroupID: club.group.ID, DisplayName: "Ben", BirthYear: 2016, CreatedAt: 200}, 0, 0)
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	// The admin lacks the parent role and cannot be a guardian.
	if err := profiles.AddGuardian(ctx, club.group.ID, club.adminID, child.UserID, nil, 201); !errors.Is(err, service.ErrGuardianNeedsParentRole) {
		t.Fatalf("AddGuardian(no parent role) error = %v", err)
	}
	if _, err := profiles.CreateProfile(ctx, service.ChildProfile{GroupID: club.group.ID, DisplayName: "Cleo", BirthYear: 2016, CreatedAt: 200}, club.adminID, 0); !errors.Is(err, service.ErrGuardianNeedsParentRole) {
		t.Fatalf("CreateProfile(guardian without role) error = %v", err)
	}
	var orphans int
	if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE display_name = 'Cleo'").Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("a failed profile left %d users behind", orphans)
	}
	// A child must be a member of the group.
	outsider := insertIntegrationGuest(t, harness)
	if err := profiles.AddGuardian(ctx, club.group.ID, club.parentID, outsider, nil, 201); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("AddGuardian(outsider child) error = %v", err)
	}
	if err := profiles.AddGuardian(ctx, club.group.ID, club.parentID, club.parentID, nil, 201); !errors.Is(err, service.ErrInvalidGuardian) {
		t.Fatalf("AddGuardian(self) error = %v", err)
	}
	// A member account can be a child too.
	if err := profiles.AddGuardian(ctx, club.group.ID, club.parentID, club.adminID, &club.adminID, 201); err != nil {
		t.Fatalf("AddGuardian(account child) error = %v", err)
	}
	if err := profiles.AddGuardian(ctx, club.group.ID, club.parentID, child.UserID, nil, 201); err != nil {
		t.Fatalf("AddGuardian() error = %v", err)
	}
	if err := profiles.AddGuardian(ctx, club.group.ID, club.parentID, child.UserID, nil, 202); err != nil {
		t.Fatalf("repeat AddGuardian() error = %v, want idempotent", err)
	}
	if ids, _ := profiles.ChildUserIDs(ctx, club.group.ID, club.parentID); len(ids) != 2 {
		t.Fatalf("children = %v, want two", ids)
	}
	if err := profiles.RemoveGuardian(ctx, club.group.ID, club.parentID, club.adminID); err != nil {
		t.Fatalf("RemoveGuardian() error = %v", err)
	}
	if err := profiles.RemoveGuardian(ctx, club.group.ID, club.parentID, club.adminID); !errors.Is(err, service.ErrGuardianNotFound) {
		t.Fatalf("second RemoveGuardian() error = %v", err)
	}

	// Removing the parent role removes the remaining link.
	membership, err := memberships.FindMembership(ctx, club.group.ID, club.parentID)
	if err != nil {
		t.Fatalf("FindMembership() error = %v", err)
	}
	if _, err := memberships.RemoveMemberRole(ctx, club.group.ID, club.parentID, service.GroupRoleParent, membership.Version); err != nil {
		t.Fatalf("RemoveMemberRole() error = %v", err)
	}
	if ids, err := profiles.ChildUserIDs(ctx, club.group.ID, club.parentID); err != nil || len(ids) != 0 {
		t.Fatalf("children after role removal = %v, %v; want none", ids, err)
	}
	// The child profile stays in the group.
	if _, err := profiles.FindProfile(ctx, club.group.ID, child.UserID); err != nil {
		t.Fatalf("FindProfile() error = %v", err)
	}
}

func TestGroupAccessSeesChildrensTeams(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	profiles := mustProfileRepository(t, harness)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	users, err := NewUserRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewUserRepository() error = %v", err)
	}
	club := newProfileClub(t, harness)
	otherTeam, err := teams.CreateTeam(ctx, service.Team{GroupID: club.group.ID, Name: "Tigers", BirthYearFrom: 2010, BirthYearTo: 2011, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	child, err := profiles.CreateProfile(ctx, service.ChildProfile{GroupID: club.group.ID, DisplayName: "Dora", BirthYear: 2016, CreatedAt: 200}, club.parentID, club.team.ID)
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	accessService, err := service.NewGroupAccessService(memberships, users)
	if err != nil {
		t.Fatalf("NewGroupAccessService() error = %v", err)
	}
	accessService.SetChildTeams(profiles)
	access, err := accessService.For(ctx, club.parentID, club.group.ID)
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}
	if !access.IsGuardianOf(child.UserID) || !access.CanViewTeam(club.team.ID) || access.CanViewTeam(otherTeam.ID) || access.CanManageRoster(club.team.ID) || access.CanViewTeamEmails(club.team.ID) {
		t.Fatalf("parent access: children %v teams %v", access.ChildUserIDs(), access.VisibleTeamIDs())
	}
}

func TestProfilesNeverSignInAndSurviveGuestCleanup(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	profiles := mustProfileRepository(t, harness)
	sessions, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewSessionRepository() error = %v", err)
	}
	cleanup, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewGuestClaimCodeRepository() error = %v", err)
	}
	club := newProfileClub(t, harness)
	member, err := profiles.CreateProfile(ctx, service.ChildProfile{GroupID: club.group.ID, DisplayName: "Eve", BirthYear: 2016, CreatedAt: 1}, club.parentID, 0)
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	// A profile without a membership (left behind by hand) is still kept.
	var loose int64
	if err := harness.DB.QueryRowContext(ctx, "INSERT INTO users (display_name, kind, created_at) VALUES ('Loose', 'profile', 1) RETURNING id").Scan(&loose); err != nil {
		t.Fatalf("insert loose profile: %v", err)
	}

	for _, userID := range []int64{member.UserID, loose} {
		err := sessions.CreateSession(ctx, service.Session{ID: "profile-session-" + strconv.FormatInt(userID, 10), UserID: userID, CreatedAt: 10, ExpiresAt: 20})
		if !errors.Is(err, service.ErrProfileCannotSignIn) {
			t.Fatalf("CreateSession(profile %d) error = %v, want ErrProfileCannotSignIn", userID, err)
		}
	}
	var sessionCount int
	if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&sessionCount); err != nil || sessionCount != 0 {
		t.Fatalf("sessions = %d, %v; want none", sessionCount, err)
	}
	// A guest still gets one.
	guest := insertIntegrationGuest(t, harness)
	if err := sessions.CreateSession(ctx, service.Session{ID: "guest-session", UserID: guest, CreatedAt: 10, ExpiresAt: 20}); err != nil {
		t.Fatalf("CreateSession(guest) error = %v", err)
	}

	if _, err := cleanup.DeleteEmptyAbandonedGuests(ctx, 1_000_000); err != nil {
		t.Fatalf("DeleteEmptyAbandonedGuests() error = %v", err)
	}
	for _, userID := range []int64{member.UserID, loose} {
		var count int
		if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id = $1", userID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("profile %d count = %d, %v; guest cleanup must never remove profiles", userID, count, err)
		}
	}
}
