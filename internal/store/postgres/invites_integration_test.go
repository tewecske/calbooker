//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/tests/postgres"
)

func mustInviteRepository(t *testing.T, harness *postgres.Harness) *InviteRepository {
	t.Helper()
	repository, err := NewInviteRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewInviteRepository() error = %v", err)
	}
	return repository
}

func TestInviteRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	invites := mustInviteRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "invite-admin@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(other) error = %v", err)
	}
	team, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	otherTeam, err := teams.CreateTeam(ctx, service.Team{GroupID: other.ID, Name: "Elsewhere", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateTeam(other) error = %v", err)
	}

	expires, maxUses := int64(500), 3
	created, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "TEAMCODE2222", Role: service.GroupRolePlayer, TeamID: &team.ID, ExpiresAt: &expires, MaxUses: &maxUses, CreatedBy: &adminID, CreatedAt: 102})
	if err != nil || created.ID <= 0 || created.Team() != team.ID || created.Uses != 0 || *created.MaxUses != 3 {
		t.Fatalf("CreateInvite() = %+v, %v", created, err)
	}
	if _, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: other.ID, Code: "TEAMCODE2222", Role: service.GroupRolePlayer, CreatedAt: 102}); !errors.Is(err, service.ErrGroupInviteTaken) {
		t.Fatalf("duplicate code error = %v, want ErrGroupInviteTaken", err)
	}
	// A team of another group cannot be named.
	if _, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "FOREIGNTEAM2", Role: service.GroupRolePlayer, TeamID: &otherTeam.ID, CreatedAt: 102}); !errors.Is(err, service.ErrTeamNotFound) {
		t.Fatalf("foreign team error = %v, want ErrTeamNotFound", err)
	}
	// Team invites carry only roster roles.
	if _, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "COACHTEAM222", Role: service.GroupRoleCoach, TeamID: &team.ID, CreatedAt: 102}); err == nil {
		t.Fatal("coach team invite was stored")
	}
	if _, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "COACHCODE222", Role: service.GroupRoleCoach, CreatedAt: 103}); err != nil {
		t.Fatalf("CreateInvite(coach) error = %v", err)
	}

	listings, err := invites.ListInvites(ctx, group.ID)
	if err != nil || len(listings) != 2 || listings[0].Invite.Code != "COACHCODE222" || listings[1].TeamName == nil || *listings[1].TeamName != "Lions" {
		t.Fatalf("ListInvites() = %+v, %v", listings, err)
	}
	if listings, err := invites.ListInvites(ctx, other.ID); err != nil || len(listings) != 0 {
		t.Fatalf("ListInvites(other) = %+v, %v; want none", listings, err)
	}
	if _, err := invites.FindInvite(ctx, other.ID, created.ID); !errors.Is(err, service.ErrGroupInviteNotFound) {
		t.Fatalf("FindInvite(other group) error = %v", err)
	}
	if found, err := invites.FindInviteByCode(ctx, "TEAMCODE2222"); err != nil || found.ID != created.ID {
		t.Fatalf("FindInviteByCode() = %+v, %v", found, err)
	}
	if _, err := invites.FindInviteByCode(ctx, "UNKNOWN00000"); !errors.Is(err, service.ErrGroupInviteNotFound) {
		t.Fatalf("unknown code error = %v", err)
	}

	if _, err := invites.RevokeInvite(ctx, other.ID, created.ID, 300); !errors.Is(err, service.ErrGroupInviteNotFound) {
		t.Fatalf("RevokeInvite(other group) error = %v", err)
	}
	revoked, err := invites.RevokeInvite(ctx, group.ID, created.ID, 300)
	if err != nil || revoked.RevokedAt == nil || *revoked.RevokedAt != 300 {
		t.Fatalf("RevokeInvite() = %+v, %v", revoked, err)
	}
	again, err := invites.RevokeInvite(ctx, group.ID, created.ID, 400)
	if err != nil || *again.RevokedAt != 300 {
		t.Fatalf("second RevokeInvite() = %+v, %v; want the first time kept", again, err)
	}
	if _, err := invites.RedeemInvite(ctx, created.ID, adminID, 301); !errors.Is(err, service.ErrInviteRevoked) {
		t.Fatalf("RedeemInvite(revoked) error = %v", err)
	}

	// Deleting the team removes its invites; deleting the group removes all.
	if _, err := harness.DB.ExecContext(ctx, "DELETE FROM teams WHERE id = $1", team.ID); err != nil {
		t.Fatalf("delete team: %v", err)
	}
	if _, err := invites.FindInvite(ctx, group.ID, created.ID); !errors.Is(err, service.ErrGroupInviteNotFound) {
		t.Fatalf("team invite survived its team: %v", err)
	}
}

func TestInviteRepositoryRedeemGrantsRoleAndTeam(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	invites := mustInviteRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "redeem-admin@example.test")
	guestID := insertIntegrationGuest(t, harness)

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	team, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	maxUses := 5
	player, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "PLAYERTEAM22", Role: service.GroupRolePlayer, TeamID: &team.ID, MaxUses: &maxUses, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateInvite(player) error = %v", err)
	}
	parent, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "PARENTCODE22", Role: service.GroupRoleParent, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateInvite(parent) error = %v", err)
	}

	// A new member gets the role and the team.
	joined, err := invites.RedeemInvite(ctx, player.ID, guestID, 200)
	if err != nil || !joined.Changed || joined.TeamID != team.ID || !slices.Equal(joined.Membership.Roles, []string{service.GroupRolePlayer}) {
		t.Fatalf("RedeemInvite(new) = %+v, %v", joined, err)
	}
	roster, err := teams.ListRoster(ctx, group.ID, team.ID)
	if err != nil || len(roster) != 1 || roster[0].UserID != guestID || roster[0].Role != service.GroupRolePlayer {
		t.Fatalf("roster = %+v, %v", roster, err)
	}
	// Redeeming the same code again changes nothing and uses nothing.
	again, err := invites.RedeemInvite(ctx, player.ID, guestID, 201)
	if err != nil || again.Changed {
		t.Fatalf("repeat RedeemInvite() = %+v, %v; want unchanged", again, err)
	}
	if found, _ := invites.FindInvite(ctx, group.ID, player.ID); found.Uses != 1 {
		t.Fatalf("uses = %d, want 1", found.Uses)
	}
	// An existing member gains the new role and a new revision.
	withParent, err := invites.RedeemInvite(ctx, parent.ID, guestID, 202)
	if err != nil || !withParent.Changed || !slices.Equal(withParent.Membership.Roles, []string{service.GroupRolePlayer, service.GroupRoleParent}) || withParent.Membership.Version != joined.Membership.Version+1 {
		t.Fatalf("RedeemInvite(parent) = %+v, %v", withParent, err)
	}
	if _, err := invites.RedeemInvite(ctx, 9_000_000, guestID, 202); !errors.Is(err, service.ErrGroupInviteNotFound) {
		t.Fatalf("RedeemInvite(unknown) error = %v", err)
	}

	// A second team invite adds the player to a second team (#47), and a team
	// invite for the parent role puts the parent on that team's roster.
	tigers, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Tigers", BirthYearFrom: 2017, BirthYearTo: 2017, CreatedAt: 103})
	if err != nil {
		t.Fatalf("CreateTeam(tigers) error = %v", err)
	}
	second, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "PLAYERTEAM33", Role: service.GroupRolePlayer, TeamID: &tigers.ID, CreatedAt: 104})
	if err != nil {
		t.Fatalf("CreateInvite(second team) error = %v", err)
	}
	secondParent, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "PARENTTEAM33", Role: service.GroupRoleParent, TeamID: &tigers.ID, CreatedAt: 104})
	if err != nil {
		t.Fatalf("CreateInvite(second team parent) error = %v", err)
	}
	for _, invite := range []service.GroupInvite{second, secondParent} {
		if redeemed, err := invites.RedeemInvite(ctx, invite.ID, guestID, 203); err != nil || !redeemed.Changed || redeemed.TeamID != tigers.ID {
			t.Fatalf("RedeemInvite(%s) = %+v, %v", invite.Role, redeemed, err)
		}
	}
	for _, teamID := range []int64{team.ID, tigers.ID} {
		roster, err := teams.ListRoster(ctx, group.ID, teamID)
		if err != nil || !slices.ContainsFunc(roster, func(entry service.TeamRosterEntry) bool {
			return entry.UserID == guestID && entry.Role == service.GroupRolePlayer
		}) {
			t.Fatalf("team %d roster = %+v, %v; the player belongs to both teams", teamID, roster, err)
		}
	}
	if roster, _ := teams.ListRoster(ctx, group.ID, tigers.ID); !slices.ContainsFunc(roster, func(entry service.TeamRosterEntry) bool {
		return entry.UserID == guestID && entry.Role == service.GroupRoleParent
	}) {
		t.Fatalf("tigers roster = %+v, want the parent entry", roster)
	}
}

func TestInviteRepositoryRedeemEnforcesLimitsUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	invites := mustInviteRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "limit-admin@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	maxUses := 2
	limited, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "TWOUSECODE22", Role: service.GroupRolePlayer, MaxUses: &maxUses, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateInvite() error = %v", err)
	}
	const joiners = 6
	guests := make([]int64, joiners)
	for index := range guests {
		guests[index] = insertIntegrationGuest(t, harness)
	}
	var (
		wait    sync.WaitGroup
		mutex   sync.Mutex
		success int
		usedUp  int
	)
	for _, guestID := range guests {
		wait.Go(func() {
			_, err := invites.RedeemInvite(ctx, limited.ID, guestID, 200)
			mutex.Lock()
			defer mutex.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, service.ErrInviteUsedUp):
				usedUp++
			default:
				t.Errorf("RedeemInvite() error = %v", err)
			}
		})
	}
	wait.Wait()
	if success != 2 || usedUp != joiners-2 {
		t.Fatalf("successes %d, used up %d; want 2 and %d", success, usedUp, joiners-2)
	}
	if count, err := memberships.CountMembers(ctx, group.ID); err != nil || count != 3 {
		t.Fatalf("members = %d, %v; want the admin and two joiners", count, err)
	}

	expires := int64(150)
	expiring, err := invites.CreateInvite(ctx, service.GroupInvite{GroupID: group.ID, Code: "EXPIRING2222", Role: service.GroupRolePlayer, ExpiresAt: &expires, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateInvite(expiring) error = %v", err)
	}
	if _, err := invites.RedeemInvite(ctx, expiring.ID, guests[0], 150); !errors.Is(err, service.ErrInviteExpired) {
		t.Fatalf("RedeemInvite(expired) error = %v, want ErrInviteExpired", err)
	}
	if _, err := invites.RedeemInvite(ctx, expiring.ID, guests[0], 149); err != nil {
		t.Fatalf("RedeemInvite(before expiry) error = %v", err)
	}
}

func TestInviteServiceJoinEndToEnd(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	invites := mustInviteRepository(t, harness)
	users, err := NewUserRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewUserRepository() error = %v", err)
	}
	adminID := insertIntegrationUser(t, harness, "service-admin@example.test")
	guestID := insertIntegrationGuest(t, harness)
	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	accessService, err := service.NewGroupAccessService(memberships, users)
	if err != nil {
		t.Fatalf("NewGroupAccessService() error = %v", err)
	}
	access, err := accessService.For(ctx, adminID, group.ID)
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}
	inviteService, err := service.NewInviteService(invites, teams, users)
	if err != nil {
		t.Fatalf("NewInviteService() error = %v", err)
	}
	coach, err := inviteService.Create(ctx, service.GroupActor{Access: access}, service.InviteInput{Role: service.GroupRoleCoach})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := inviteService.Join(ctx, guestID, coach.Code); !errors.Is(err, service.ErrRoleNeedsAccount) {
		t.Fatalf("guest coach Join() error = %v, want ErrRoleNeedsAccount", err)
	}
	if _, err := memberships.FindMembership(ctx, group.ID, guestID); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("refused guest became a member: %v", err)
	}
}
