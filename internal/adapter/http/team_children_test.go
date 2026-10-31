package httpadapter

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func teamParentAccess(children ...int64) *service.GroupAccess {
	return groupAccessAs(service.GroupAccessGrants{
		Member:       true,
		Roles:        []string{service.GroupRoleParent},
		OwnTeams:     []service.TeamRole{{TeamID: 4, Role: service.GroupRoleParent}},
		ChildUserIDs: children,
	})
}

func TestTeamPageOffersAddChildToParentsOfTheTeam(t *testing.T) {
	name := "Anna"
	other := "Ben"
	overview := service.ChildrenOverview{Children: []service.ChildEntry{
		{Person: service.PersonRef{UserID: 20, Kind: service.UserKindProfile, DisplayName: &name}, Profile: &service.ChildProfile{UserID: 20, GroupID: 3, DisplayName: name, BirthYear: 2016, Version: 2}, Teams: []service.TeamRef{{ID: 4, Name: "Lions"}}},
		{Person: service.PersonRef{UserID: 21, Kind: service.UserKindProfile, DisplayName: &other}, Profile: &service.ChildProfile{UserID: 21, GroupID: 3, DisplayName: other, BirthYear: 2017, Version: 1}, Teams: []service.TeamRef{{ID: 6, Name: "Tigers"}}},
	}}
	tests := []struct {
		name   string
		access *service.GroupAccess
		want   bool
	}{
		{name: "parent of the team", access: teamParentAccess(20, 21), want: true},
		{name: "parent of another team", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, OwnTeams: []service.TeamRole{{TeamID: 9, Role: service.GroupRoleParent}}, ChildTeamIDs: []int64{4}, ChildUserIDs: []int64{20, 21}}), want: true},
		{name: "player on the team", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: []service.TeamRole{{TeamID: 4, Role: service.GroupRolePlayer}}})},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, teams := groupTestDependencies(t, test.access)
			teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016}}
			dependencies.Profiles = &profilesUseCaseStub{overview: overview}
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4?add_child=1", "", false)
			body := response.Body.String()
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if got := strings.Contains(body, `data-testid="team-add-child"`); got != test.want {
				t.Fatalf("add child card shown = %v, want %v", got, test.want)
			}
			if !test.want {
				return
			}
			for _, want := range []string{`action="/en/groups/lions/teams/t4/children"`, `data-testid="add-child-prompt"`, "/en/groups/lions/teams/t4/children/20/leave", "/en/groups/lions/teams/t4/children/21/join"} {
				if !strings.Contains(body, want) {
					t.Errorf("team page missing %q", want)
				}
			}
		})
	}
}

func TestTeamPageHidesAddChildOnArchivedTeam(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, teamParentAccess())
	archivedAt := int64(5)
	teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", ArchivedAt: &archivedAt}}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false).Body.String()
	if strings.Contains(body, `data-testid="team-add-child"`) {
		t.Error("archived team offers Add my child")
	}
}

func TestTeamRosterMarksProfilesWithoutAccount(t *testing.T) {
	name := "Anna"
	tests := []struct {
		name   string
		access *service.GroupAccess
		want   int
	}{
		{"parent", teamParentAccess(), 0},
		{"manager", groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleManager}}), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dependencies, _, teams := groupTestDependencies(t, tt.access)
			teams.detail = service.TeamDetail{
				Team:   service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions"},
				Roster: []service.TeamRosterEntry{{UserID: 20, Role: service.GroupRolePlayer, Kind: service.UserKindProfile, DisplayName: &name}, {UserID: 8, Role: service.GroupRolePlayer}},
			}
			body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false).Body.String()
			if got := strings.Count(body, `data-testid="roster-no-account"`); got != tt.want {
				t.Errorf("no-account markers = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAddTeamChild(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, teamParentAccess())
	teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions"}}
	profiles := &profilesUseCaseStub{}
	dependencies.Profiles = profiles
	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children", "display_name=Anna&birth_year=2016", true)
	if created.Code != http.StatusOK || profiles.lastTeam != 4 || profiles.lastInput != (service.ProfileInput{DisplayName: "Anna", BirthYear: 2016}) {
		t.Fatalf("create = %d team %d input %+v", created.Code, profiles.lastTeam, profiles.lastInput)
	}
	if !strings.Contains(created.Body.String(), "Your child was added to the team.") {
		t.Error("success message missing")
	}

	profiles.writeErr = service.ErrInvalidProfileBirthYear
	invalid := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children", "display_name=Anna&birth_year=x", false)
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), `value="Anna"`) {
		t.Fatalf("invalid create = %d, want 422 keeping the name: %s", invalid.Code, invalid.Body.String())
	}

	profiles.writeErr = service.ErrGroupForbidden
	if forbidden := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children", "display_name=Anna&birth_year=2016", false); forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden create = %d, want 403", forbidden.Code)
	}
}

func TestAddTeamChildByNonMemberIsNotFound(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupAccessAs(service.GroupAccessGrants{}))
	profiles := &profilesUseCaseStub{}
	dependencies.Profiles = profiles
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children", "display_name=Anna&birth_year=2016", true)
	if response.Code != http.StatusNotFound || profiles.writes != 0 {
		t.Fatalf("non-member create = %d writes %d, want 404 and no write", response.Code, profiles.writes)
	}
}

func TestJoinLeaveAndEditTeamChild(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, teamParentAccess(20))
	profiles := &profilesUseCaseStub{}
	dependencies.Profiles = profiles

	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children/20/join", "", true); response.Code != http.StatusOK || len(profiles.joined) != 1 || profiles.lastTeam != 4 {
		t.Fatalf("join = %d %v", response.Code, profiles.joined)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children/20/leave", "", true); response.Code != http.StatusOK || len(profiles.left) != 1 {
		t.Fatalf("leave = %d %v", response.Code, profiles.left)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children/20", "version=2&display_name=Anna+B&birth_year=2015", true); response.Code != http.StatusOK || profiles.lastChild != 20 || profiles.lastVersion != 2 || profiles.lastInput.BirthYear != 2015 {
		t.Fatalf("edit = %d child %d version %d input %+v", response.Code, profiles.lastChild, profiles.lastVersion, profiles.lastInput)
	}

	// A child of another parent: the service answers not found.
	profiles.writeErr = service.ErrProfileNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children/99/join", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("join foreign child = %d, want 404", response.Code)
	}
	profiles.writeErr = service.ErrOptimisticLockConflict
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/children/20", "version=1&display_name=Anna&birth_year=2016", false); response.Code != http.StatusConflict {
		t.Fatalf("stale edit = %d, want 409", response.Code)
	}
}

func TestJoinWithParentInviteOffersAddChild(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.JoinGroup = &rateLimitedJoinStub{redemption: service.InviteRedemption{Membership: service.GroupMembership{GroupID: 3}, GroupSlug: "lions", TeamID: 4, TeamSlug: "t4", Role: service.GroupRoleParent}}
	if got := serveGroups(t, dependencies, http.MethodPost, "/en/groups/join", "code=ABCDEFGHJKLM", false).Header().Get("Location"); got != "/en/groups/lions/teams/t4?add_child=1" {
		t.Fatalf("parent invite redirect = %q, want the team page with add_child", got)
	}
	dependencies.JoinGroup = &rateLimitedJoinStub{redemption: service.InviteRedemption{Membership: service.GroupMembership{GroupID: 3}, GroupSlug: "lions", TeamID: 4, TeamSlug: "t4", Role: service.GroupRolePlayer}}
	if got := serveGroups(t, dependencies, http.MethodPost, "/en/groups/join", "code=ABCDEFGHJKLM", false).Header().Get("Location"); got != "/en/groups/lions/teams/t4" {
		t.Fatalf("player invite redirect = %q, want the plain team page", got)
	}
}
