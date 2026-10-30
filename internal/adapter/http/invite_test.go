package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestInvitesPageListsAndCreates(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleManager))
	teams.summaries = []service.TeamSummary{{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016}}}
	teamName := "Lions"
	teamID := int64(4)
	maxUses := 10
	expires := int64(2_000_000_000)
	revokedAt := int64(50)
	invites := &invitesUseCaseStub{now: 100, listings: []service.GroupInviteListing{
		{Invite: service.GroupInvite{ID: 1, Code: "PLAYERCODE23", Role: service.GroupRolePlayer, TeamID: &teamID, MaxUses: &maxUses, Uses: 3, ExpiresAt: &expires}, TeamName: &teamName},
		{Invite: service.GroupInvite{ID: 2, Code: "REVOKEDCODE2", Role: service.GroupRoleCoach, RevokedAt: &revokedAt}},
	}}
	dependencies.Invites = invites
	page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/invites", "", false)
	body := page.Body.String()
	if page.Code != http.StatusOK {
		t.Fatalf("invites page status = %d", page.Code)
	}
	for _, want := range []string{"PLAYERCODE23", "3 / 10", "Lions", "2033-05-18", `data-invite-status="active"`, `data-invite-status="revoked"`, "/en/groups/lions/invites/1/revoke", `<option value="manager"`, `<option value="">No team</option>`} {
		if !strings.Contains(body, want) {
			t.Errorf("invites page missing %q", want)
		}
	}
	// Only admins invite admins; a manager is not offered the admin role.
	if strings.Contains(body, `<option value="admin"`) || strings.Contains(body, "/en/groups/lions/invites/2/revoke") {
		t.Error("manager page offers the admin role or revokes a revoked invite")
	}

	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites", "role=parent&team_id=4&expires_in=7&max_uses=5", true)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `data-testid="invite-created-code"`) {
		t.Fatalf("create = %d, want the new code shown", created.Code)
	}
	if invites.lastInput != (service.InviteInput{Role: service.GroupRoleParent, TeamID: 4, ExpiresInDays: 7, MaxUses: 5}) {
		t.Fatalf("input = %+v", invites.lastInput)
	}

	for _, form := range []string{"role=player&max_uses=abc", "role=player&expires_in=-1"} {
		writes := invites.writes
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites", form, true)
		if response.Code != http.StatusUnprocessableEntity || invites.writes != writes {
			t.Fatalf("malformed %q = %d (writes %d), want 422 without a write", form, response.Code, invites.writes-writes)
		}
	}
	invites.writeErr = service.ErrInvalidInvite
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites", "role=coach&team_id=4", false); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "only for players and parents") {
		t.Fatalf("invalid invite = %d, want 422 with the rule", response.Code)
	}
}

func TestInvitesPageForCoachRequiresTeam(t *testing.T) {
	coach := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{4}})
	dependencies, _, teams := groupTestDependencies(t, coach)
	teams.summaries = []service.TeamSummary{{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016}}}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/invites", "", false).Body.String()
	if !strings.Contains(body, `<option value="player"`) || !strings.Contains(body, `<option value="parent"`) || strings.Contains(body, `<option value="coach"`) {
		t.Fatal("coach must be offered only player and parent invites")
	}
	if strings.Contains(body, `<option value="">No team</option>`) || !strings.Contains(body, `name="team_id" required`) {
		t.Fatal("coach invites must name a team")
	}
}

func TestInviteRoutesForbidPlayers(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRolePlayer))
	invites := &invitesUseCaseStub{writeErr: service.ErrGroupForbidden}
	dependencies.Invites = invites
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/invites", "", false); response.Code != http.StatusForbidden || invites.calls != 0 {
		t.Fatalf("player invites page = %d (calls %d), want 403 without loading", response.Code, invites.calls)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites", "role=player", false); response.Code != http.StatusForbidden {
		t.Fatalf("player create = %d, want 403", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites/5/revoke", "", true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("player HTMX revoke = %d, want 422", response.Code)
	}
}

func TestInviteRevoke(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	invites := &invitesUseCaseStub{}
	dependencies.Invites = invites
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites/5/revoke", "", true)
	if response.Code != http.StatusOK || invites.lastRevoked != 5 || !strings.Contains(response.Body.String(), "Invite revoked.") {
		t.Fatalf("revoke = %d (invite %d), want 200", response.Code, invites.lastRevoked)
	}
	invites.writeErr = service.ErrGroupInviteNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites/6/revoke", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("unknown invite revoke = %d, want 404", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/invites/abc/revoke", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("malformed invite revoke = %d, want 404", response.Code)
	}
}

func TestInviteInputParsing(t *testing.T) {
	input, err := inviteInput(map[string]string{"role": "player", "team_id": "4", "expires_in": "30", "max_uses": "2"})
	if err != nil || input != (service.InviteInput{Role: "player", TeamID: 4, ExpiresInDays: 30, MaxUses: 2}) {
		t.Fatalf("inviteInput() = %+v, %v", input, err)
	}
	if _, err := inviteInput(map[string]string{"role": "player", "team_id": "x"}); err == nil {
		t.Fatal("malformed team accepted")
	}
}

type invitesUseCaseStub struct {
	listings    []service.GroupInviteListing
	now         int64
	writeErr    error
	calls       int
	writes      int
	lastInput   service.InviteInput
	lastRevoked int64
}

func (s *invitesUseCaseStub) List(context.Context, *service.GroupAccess) ([]service.GroupInviteListing, error) {
	s.calls++
	return s.listings, nil
}

func (s *invitesUseCaseStub) Create(_ context.Context, _ service.GroupActor, input service.InviteInput) (service.GroupInvite, error) {
	s.calls++
	s.writes++
	s.lastInput = input
	if s.writeErr != nil {
		return service.GroupInvite{}, s.writeErr
	}
	return service.GroupInvite{ID: 9, Code: "NEWCODE23456", Role: input.Role}, nil
}

func (s *invitesUseCaseStub) Revoke(_ context.Context, _ service.GroupActor, inviteID int64) (service.GroupInvite, error) {
	s.calls++
	s.writes++
	s.lastRevoked = inviteID
	return service.GroupInvite{ID: inviteID}, s.writeErr
}

func (s *invitesUseCaseStub) Now() int64 {
	return s.now
}
