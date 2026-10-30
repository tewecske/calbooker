package httpadapter

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

var navSectionPattern = regexp.MustCompile(`data-section="([a-z-]+)"`)

// navSections lists the group menu entries of a page, in order.
func navSections(body string) []string {
	var sections []string
	for _, match := range navSectionPattern.FindAllStringSubmatch(body, -1) {
		sections = append(sections, match[1])
	}
	return sections
}

func roleNavDependencies(t *testing.T, access *service.GroupAccess, teamIDs ...int64) Dependencies {
	t.Helper()
	dependencies, _ := calendarDependencies(t, access)
	stub := dependencies.Teams.(visibleTeamsStub)
	stub.summaries = nil
	for _, id := range teamIDs {
		stub.summaries = append(stub.summaries, service.TeamSummary{Team: service.Team{ID: id, GroupID: 3, Name: calendarTeamNames[id], Slug: "t" + string(rune('0'+id))}})
	}
	dependencies.Teams = stub
	return dependencies
}

func TestGroupMenuPerRole(t *testing.T) {
	player := func(teamIDs ...int64) *service.GroupAccess {
		roles := make([]service.TeamRole, 0, len(teamIDs))
		for _, id := range teamIDs {
			roles = append(roles, service.TeamRole{TeamID: id, Role: service.GroupRolePlayer})
		}
		return groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: roles})
	}
	tests := []struct {
		name       string
		access     *service.GroupAccess
		teamIDs    []int64
		want       []string
		absent     []string
		teamsLabel string
		teamsHref  string
	}{
		{name: "player in two teams", access: player(1, 2), teamIDs: []int64{1, 2}, want: []string{"calendar", "teams", "events", "statistics"}, absent: []string{"overview", "members", "children"}, teamsLabel: "Teams", teamsHref: "/en/groups/lions/teams"},
		{name: "player in one team", access: player(1), teamIDs: []int64{1}, want: []string{"calendar", "teams", "events", "statistics"}, absent: []string{"overview", "members", "children"}, teamsLabel: "Lions", teamsHref: "/en/groups/lions/teams/t1"},
		{name: "parent in one team", access: calendarParent(), teamIDs: []int64{3}, want: []string{"calendar", "teams", "events", "statistics"}, absent: []string{"overview", "members", "children"}, teamsLabel: "Bears", teamsHref: "/en/groups/lions/teams/t3"},
		{name: "parent of children on two teams", access: calendarParent(), teamIDs: []int64{3, 4}, want: []string{"calendar", "teams", "events", "statistics"}, absent: []string{"overview", "members", "children"}, teamsLabel: "Teams", teamsHref: "/en/groups/lions/teams"},
		{name: "coach", access: calendarCoach(), teamIDs: []int64{1}, want: []string{"overview", "members", "teams", "events", "calendar"}, absent: []string{"children"}, teamsLabel: "Teams", teamsHref: "/en/groups/lions/teams"},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), teamIDs: []int64{1}, want: []string{"overview", "members", "teams", "events", "calendar"}, absent: []string{"children"}, teamsLabel: "Teams", teamsHref: "/en/groups/lions/teams"},
		{name: "parent who also coaches keeps the full menu", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach, service.GroupRoleParent}, CoachedTeamIDs: []int64{1}}), teamIDs: []int64{1}, want: []string{"overview", "members"}, absent: []string{"children"}, teamsLabel: "Teams", teamsHref: "/en/groups/lions/teams"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := roleNavDependencies(t, test.access, test.teamIDs...)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/calendar", "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			got := navSections(body)
			for _, section := range test.want {
				if !slices.Contains(got, section) {
					t.Errorf("menu %v is missing %q", got, section)
				}
			}
			for _, section := range test.absent {
				if slices.Contains(got, section) {
					t.Errorf("menu %v has %q", got, section)
				}
			}
			if test.absent[0] == "overview" && !slices.Equal(got, test.want) {
				t.Errorf("menu = %v, want exactly %v", got, test.want)
			}
			link := `href="` + test.teamsHref + `" data-section="teams"`
			if !strings.Contains(body, link) {
				t.Errorf("teams entry should link to %s", test.teamsHref)
			}
			if !regexp.MustCompile(`data-section="teams"[^>]*>` + test.teamsLabel + `</a>`).MatchString(body) {
				t.Errorf("teams entry should read %q", test.teamsLabel)
			}
		})
	}
}

func TestSingleTeamEntryIsCurrentOnTheTeamPage(t *testing.T) {
	dependencies := roleNavDependencies(t, playerOfTeam(1), 1)
	teams := dependencies.Teams.(visibleTeamsStub)
	teams.detail = service.TeamDetail{Team: service.Team{ID: 1, GroupID: 3, Name: "Lions", Slug: "t1"}}
	dependencies.Teams = teams
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t1", "", false).Body.String()
	if !strings.Contains(body, `href="/en/groups/lions/teams/t1" data-section="teams" aria-current="page"`) {
		t.Error("the team's own menu entry is not marked current on its page")
	}
}

func TestChildrenAndGuardianRoutesStillWork(t *testing.T) {
	parent := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, ChildUserIDs: []int64{20}})
	dependencies, _, _ := groupTestDependencies(t, parent)
	dependencies.Profiles = &profilesUseCaseStub{overview: service.ChildrenOverview{Children: []service.ChildEntry{childFixture()}}}
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/children", "", false)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Anna") {
		t.Errorf("children page = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), `data-section="children"`) {
		t.Error("children is advertised in the menu")
	}
}

// childCalendarParent guards children 20 (Anna, on Bears) and 21 (Ben, on
// Wolves and Bears); events 3 (Bears) and 4 (Wolves) are in the fixture week.
func childCalendarDependencies(t *testing.T) Dependencies {
	t.Helper()
	parent := groupAccessAs(service.GroupAccessGrants{
		Member: true, Roles: []string{service.GroupRoleParent},
		ChildTeamIDs: []int64{3, 4}, ChildUserIDs: []int64{20, 21},
	})
	dependencies, _ := calendarDependencies(t, parent)
	anna := childFixture()
	anna.Teams = []service.TeamRef{{ID: 3, Name: "Bears"}}
	ben := childFixture()
	ben.Person = service.PersonRef{UserID: 21, Kind: service.UserKindProfile, DisplayName: strPtr("Ben")}
	ben.Teams = []service.TeamRef{{ID: 3, Name: "Bears"}, {ID: 4, Name: "Wolves"}}
	dependencies.Profiles = &profilesUseCaseStub{overview: service.ChildrenOverview{Children: []service.ChildEntry{ben, anna}}}
	return dependencies
}

func TestCalendarMarksEventsWithChildColours(t *testing.T) {
	dependencies := childCalendarDependencies(t)
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/calendar?week=2026-10-19", "", false).Body.String()
	if got := shownEventIDs(t, body); !slices.Equal(got, []int64{3, 4}) {
		t.Fatalf("events = %v, want the children's team events", got)
	}
	// Anna (lower user ID) is color 0, Ben color 1; an event carries both.
	for _, want := range []string{
		`data-testid="calendar-children"`,
		`<span class="child-dot child-color-0" aria-hidden="true"></span>Anna`,
		`<span class="child-dot child-color-1" aria-hidden="true"></span>Ben`,
		`data-calendar-for="Anna"`, `data-calendar-for="Ben"`,
		`class="child-dot child-color-0" title="Anna"`, `class="child-dot child-color-1" title="Ben"`,
		`<select id="calendar-filter-child" class="select select-sm" name="child">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("calendar missing %q", want)
		}
	}
	if n := strings.Count(body, `data-calendar-for="Ben"`); n != 2 {
		t.Errorf("Ben is on both events, marked %d times in the day list, want 2", n)
	}
	if n := strings.Count(body, `data-calendar-for="Anna"`); n != 1 {
		t.Errorf("Anna is on one event, marked %d times, want 1", n)
	}
}

func TestCalendarFiltersByChild(t *testing.T) {
	dependencies := childCalendarDependencies(t)
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/calendar?week=2026-10-19&child=20", "", false).Body.String()
	if got := shownEventIDs(t, body); !slices.Equal(got, []int64{3}) {
		t.Errorf("Anna's events = %v, want [3]", got)
	}
	if !strings.Contains(body, `<option value="20" selected>Anna</option>`) || !strings.Contains(body, "child=20") {
		t.Error("child filter is not kept in the form and the week links")
	}
	if !strings.Contains(body, `data-testid="calendar-clear"`) {
		t.Error("clear link missing")
	}
	// A child the viewer does not guard never widens or narrows anything.
	body = serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/calendar?week=2026-10-19&child=99", "", false).Body.String()
	if got := shownEventIDs(t, body); !slices.Equal(got, []int64{3, 4}) {
		t.Errorf("unknown child filter changed the events: %v", got)
	}
}

func TestCalendarWithoutChildrenHasNoChildControls(t *testing.T) {
	for _, access := range []*service.GroupAccess{playerOfTeam(2), calendarCoach(), groupMemberAs(service.GroupRoleManager)} {
		dependencies, _ := calendarDependencies(t, access)
		body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/calendar?week=2026-10-19", "", false).Body.String()
		for _, absent := range []string{"calendar-filter-child", "child-dot", "calendar-children"} {
			if strings.Contains(body, absent) {
				t.Errorf("roles %v: %q shown without children", access.Roles(), absent)
			}
		}
	}
}
