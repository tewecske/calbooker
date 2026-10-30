package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestLandingRoute(t *testing.T) {
	tests := []struct {
		name   string
		groups []landingGroup
		want   string
	}{
		{"no groups", nil, "/home"},
		{"one team", []landingGroup{{Slug: "lions", TeamSlugs: []string{"u9"}}}, "/groups/lions/teams/u9"},
		{"one team of a player", []landingGroup{{Slug: "lions", FamilyOnly: true, TeamSlugs: []string{"u9"}}}, "/groups/lions/teams/u9"},
		{"one team among several groups", []landingGroup{{Slug: "lions"}, {Slug: "bears", TeamSlugs: []string{"u11"}}}, "/groups/bears/teams/u11"},
		{"parent with two teams", []landingGroup{{Slug: "lions", FamilyOnly: true, TeamSlugs: []string{"u9", "u11"}}}, "/groups/lions/calendar"},
		{"player without a team", []landingGroup{{Slug: "lions", FamilyOnly: true}}, "/groups/lions/calendar"},
		{"coach of two teams", []landingGroup{{Slug: "lions", TeamSlugs: []string{"u9", "u11"}}}, "/groups/lions"},
		{"staff without teams", []landingGroup{{Slug: "lions"}}, "/groups/lions"},
		{"two groups", []landingGroup{{Slug: "lions"}, {Slug: "bears"}}, "/home"},
		{"two groups of a parent", []landingGroup{{Slug: "lions", FamilyOnly: true, TeamSlugs: []string{"a", "b"}}, {Slug: "bears", FamilyOnly: true}}, "/home"},
		{"two teams in two groups", []landingGroup{{Slug: "lions", TeamSlugs: []string{"a"}}, {Slug: "bears", TeamSlugs: []string{"b"}}}, "/home"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := landingRoute(test.groups); got != test.want {
				t.Errorf("landingRoute = %q, want %q", got, test.want)
			}
		})
	}
}

// landingAccessStub answers For per group.
type landingAccessStub struct {
	byGroup map[int64]*service.GroupAccess
	err     error
}

func (s landingAccessStub) For(_ context.Context, _, groupID int64) (*service.GroupAccess, error) {
	return s.byGroup[groupID], s.err
}

type landingTeamsStub struct {
	visibleTeamsStub
	err error
}

func (s landingTeamsStub) List(ctx context.Context, access *service.GroupAccess) ([]service.TeamSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	// Teams are numbered by group: team N belongs to group N/10.
	all, _ := s.visibleTeamsStub.List(ctx, access)
	var mine []service.TeamSummary
	for _, summary := range all {
		if summary.Team.GroupID == access.GroupID() {
			mine = append(mine, summary)
		}
	}
	return mine, nil
}

func landingDependencies(t *testing.T, access map[int64]*service.GroupAccess, teams ...service.Team) Dependencies {
	t.Helper()
	dependencies := newHTTPTestDependencies(t)
	groups := &groupsUseCaseStub{}
	for id := range access {
		slug := map[int64]string{3: "lions", 4: "bears"}[id]
		groups.summaries = append(groups.summaries, service.GroupSummary{Group: service.Group{ID: id, Slug: slug}})
	}
	// Map order is random; the landing must not depend on it.
	teamStub := &teamsUseCaseStub{}
	for _, team := range teams {
		teamStub.summaries = append(teamStub.summaries, service.TeamSummary{Team: team})
	}
	dependencies.Groups = groups
	dependencies.GroupAccess = landingAccessStub{byGroup: access}
	dependencies.Teams = landingTeamsStub{visibleTeamsStub: visibleTeamsStub{teamStub}}
	return dependencies
}

func familyAccess(groupID int64, teamIDs ...int64) *service.GroupAccess {
	roles := make([]service.TeamRole, 0, len(teamIDs))
	for _, id := range teamIDs {
		roles = append(roles, service.TeamRole{TeamID: id, Role: service.GroupRolePlayer})
	}
	return service.NewGroupAccess(groupID, 7, service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: roles})
}

func TestLandingFor(t *testing.T) {
	ctx := context.Background()
	u9 := service.Team{ID: 1, GroupID: 3, Slug: "u9", Name: "U9"}
	u11 := service.Team{ID: 2, GroupID: 3, Slug: "u11", Name: "U11"}
	archived := service.Team{ID: 5, GroupID: 3, Slug: "old", Name: "Old"}
	archivedAt := int64(1)
	archived.ArchivedAt = &archivedAt
	bears := service.Team{ID: 6, GroupID: 4, Slug: "u13", Name: "U13"}

	t.Run("single team player lands on the team", func(t *testing.T) {
		dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: familyAccess(3, 1)}, u9, u11)
		if got := landingFor(ctx, dependencies, 7); got != "/groups/lions/teams/u9" {
			t.Errorf("landing = %q", got)
		}
	})
	t.Run("an archived team does not count", func(t *testing.T) {
		dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: familyAccess(3, 1, 5)}, u9, archived)
		if got := landingFor(ctx, dependencies, 7); got != "/groups/lions/teams/u9" {
			t.Errorf("landing = %q", got)
		}
	})
	t.Run("two teams land on the calendar", func(t *testing.T) {
		dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: familyAccess(3, 1, 2)}, u9, u11)
		if got := landingFor(ctx, dependencies, 7); got != "/groups/lions/calendar" {
			t.Errorf("landing = %q", got)
		}
	})
	t.Run("staff of one group land on the group even with one team", func(t *testing.T) {
		manager := service.NewGroupAccess(3, 7, service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleManager}})
		dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: manager}, u9)
		if got := landingFor(ctx, dependencies, 7); got != "/groups/lions" {
			t.Errorf("landing = %q", got)
		}
	})
	t.Run("one team across two groups", func(t *testing.T) {
		coach := service.NewGroupAccess(4, 7, service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{6}})
		manager := service.NewGroupAccess(3, 7, service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleManager}})
		dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: manager, 4: coach}, u9, bears)
		if got := landingFor(ctx, dependencies, 7); got != "/groups/bears/teams/u13" {
			t.Errorf("landing = %q", got)
		}
	})
	t.Run("a site administrator who is not a member does not count the group", func(t *testing.T) {
		admin := service.NewGroupAccess(3, 7, service.GroupAccessGrants{SiteAdmin: true})
		dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: admin}, u9)
		if got := landingFor(ctx, dependencies, 7); got != "/home" {
			t.Errorf("landing = %q", got)
		}
	})
	t.Run("failures fall back to the home page", func(t *testing.T) {
		dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: familyAccess(3, 1)}, u9)
		dependencies.Teams = landingTeamsStub{err: errors.New("down")}
		if got := landingFor(ctx, dependencies, 7); got != "/home" {
			t.Errorf("teams failure landing = %q", got)
		}
		dependencies = landingDependencies(t, map[int64]*service.GroupAccess{3: familyAccess(3, 1)}, u9)
		dependencies.GroupAccess = landingAccessStub{err: service.ErrGroupNotFound}
		if got := landingFor(ctx, dependencies, 7); got != "/home" {
			t.Errorf("access failure landing = %q", got)
		}
		dependencies.Groups = nil
		if got := landingFor(ctx, dependencies, 7); got != "/home" {
			t.Errorf("missing groups landing = %q", got)
		}
	})
}

func TestClientModeCallbackLandsPerRole(t *testing.T) {
	state := strings.Repeat("ab", 32)
	u9 := service.Team{ID: 1, GroupID: 3, Slug: "u9", Name: "U9"}
	tests := []struct {
		name   string
		access *service.GroupAccess
		teams  []service.Team
		want   string
	}{
		{"single-team player", familyAccess(3, 1), []service.Team{u9}, "/en/groups/lions/teams/u9"},
		{"player without teams", familyAccess(3), []service.Team{u9}, "/en/groups/lions/calendar"},
		{"member of a group", service.NewGroupAccess(3, 7, service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleManager}}), []service.Team{u9}, "/en/groups/lions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := landingDependencies(t, map[int64]*service.GroupAccess{3: test.access}, test.teams...)
			dependencies.SSOClient = &httpSSOClientStub{}
			dependencies.SSOAccountURL = "https://accounts.example.test/en/account/settings"
			handler := NewRouterWithServices(dependencies)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, oauthCallbackRequest("/en/oauth/sso/callback?code=c&state="+state, state))
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.want {
				t.Errorf("callback = %d %q, want %q", response.Code, response.Header().Get("Location"), test.want)
			}
		})
	}
}
