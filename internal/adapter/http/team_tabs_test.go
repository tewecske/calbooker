package httpadapter

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

// unseenTeams makes every team unknown, like teams the account cannot see.
type unseenTeams struct {
	TeamUseCases
}

func (unseenTeams) BySlug(context.Context, *service.GroupAccess, string) (service.Team, error) {
	return service.Team{}, service.ErrTeamNotFound
}

func TestTeamTabsOnEveryTeamPage(t *testing.T) {
	dependencies, _ := eventDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	dependencies.Teams.(*teamsUseCaseStub).detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016}}
	for target, current := range map[string]string{
		"/en/groups/lions/teams/t4":          "roster",
		"/en/groups/lions/teams/t4/events":   "events",
		"/en/groups/lions/teams/t4/calendar": "calendar",
	} {
		body := serveGroups(t, dependencies, http.MethodGet, target, "", false).Body.String()
		for _, want := range []string{
			`href="/en/groups/lions/teams/t4" data-team-tab="roster"`,
			`href="/en/groups/lions/teams/t4/events" data-team-tab="events"`,
			`href="/en/groups/lions/teams/t4/calendar" data-team-tab="calendar"`,
			`data-team-tab="` + current + `" aria-selected="true" aria-current="page"`,
			`data-section="teams" aria-current="page"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", target, want)
			}
		}
	}
	if body := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/lions/teams/t4/events", "", false).Body.String(); !strings.Contains(body, "Névsor") || !strings.Contains(body, `href="/hu/groups/lions/teams/t4/calendar"`) {
		t.Error("Hungarian team tabs missing")
	}
}

func TestTeamEventsTabByRole(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantCreate bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantCreate: true},
		{name: "coach of the team", access: coachOfTeam(4), wantCreate: true},
		{name: "player", access: playerOfTeam(4)},
		{name: "parent", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, ChildTeamIDs: []int64{4}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, events := eventDependencies(t, test.access)
			// A stray team filter in the URL must not change the tab.
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4/events?team=5", "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			for _, want := range []string{`id="group-events-heading"`, "Autumn cup", `href="/en/groups/lions/teams/t4/events/5"`, `href="/en/groups/lions/teams/t4/events?when=past"`} {
				if !strings.Contains(body, want) {
					t.Errorf("team events tab missing %q", want)
				}
			}
			if strings.Contains(body, `id="events-filter-team"`) {
				t.Error("team filter shown on a team's events")
			}
			if events.lastQuery.TeamID != 4 {
				t.Errorf("query team = %d, want 4", events.lastQuery.TeamID)
			}
			if got := strings.Contains(body, `id="event-new-title"`); got != test.wantCreate {
				t.Fatalf("create form shown = %v, want %v", got, test.wantCreate)
			}
			if test.wantCreate {
				if !strings.Contains(body, `action="/en/groups/lions/teams/t4/events"`) {
					t.Error("create form does not post to the team's events")
				}
				if !strings.Contains(body, `<option value="4" selected>`) || strings.Contains(body, `<option value="5" selected>`) {
					t.Error("create form should preselect only the team")
				}
			}
		})
	}
}

func TestTeamEventsTabHidesCreateForTeamsOutsideTheOptions(t *testing.T) {
	// The form offers teams 4 and 5 only; team 7 is not among them.
	dependencies, _ := eventDependencies(t, coachOfTeam(4))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t7/events", "", false).Body.String()
	if strings.Contains(body, `id="event-new-title"`) {
		t.Error("create form shown for a team the account cannot create events for")
	}
}

func TestTeamEventCreate(t *testing.T) {
	dependencies, events := eventDependencies(t, coachOfTeam(4))
	form := "title=Cup&kind=training&starts_at=2026-11-07T09:00&ends_at=2026-11-07T10:00&team_id=4&location=Park"
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/events", form, true)
	if response.Header().Get("HX-Redirect") != "/en/groups/lions/teams/t4/events/99" || events.writes != 1 {
		t.Fatalf("redirect = %q, writes = %d", response.Header().Get("HX-Redirect"), events.writes)
	}
	events.writeErr = service.ErrInvalidEventTeams
	failed := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/events", form, true)
	if failed.Code != http.StatusUnprocessableEntity || !strings.Contains(failed.Body.String(), `id="event-new-title"`) || !strings.Contains(failed.Body.String(), `value="Cup"`) {
		t.Fatalf("failed create status = %d, form not kept", failed.Code)
	}
}

func TestTeamCalendarTabByRole(t *testing.T) {
	tests := []struct {
		name   string
		access *service.GroupAccess
		team   string
		want   []int64
	}{
		{name: "admin sees the team's events", access: groupMemberAs(service.GroupRoleAdmin), team: "t2", want: []int64{2, 5, 6}},
		{name: "admin on another team", access: groupMemberAs(service.GroupRoleAdmin), team: "t1", want: []int64{1, 5}},
		{name: "coach of the team", access: calendarCoach(), team: "t1", want: []int64{1, 5}},
		{name: "player of the team", access: playerOfTeam(2), team: "t2", want: []int64{2, 5, 6}},
		{name: "player sees only visible events of another team", access: playerOfTeam(1), team: "t2", want: []int64{5}},
		{name: "parent through the child's team", access: calendarParent(), team: "t3", want: []int64{3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _ := calendarDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/"+test.team+"/calendar?week=2026-10-21&team=4", "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			if got := shownEventIDs(t, body); !slices.Equal(got, test.want) {
				t.Errorf("shown = %v, want %v", got, test.want)
			}
			if strings.Contains(body, `id="calendar-filter-team"`) || strings.Contains(body, `data-testid="calendar-groups"`) {
				t.Error("team filter or group switcher shown on a team's calendar")
			}
			base := `/en/groups/lions/teams/` + test.team + `/calendar`
			for _, want := range []string{`href="` + base + `?week=2026-10-26"`, `action="` + base + `"`, `data-team-tab="calendar" aria-selected="true"`} {
				if !strings.Contains(body, want) {
					t.Errorf("team calendar missing %q", want)
				}
			}
			if strings.Contains(body, "team=") {
				t.Error("the fixed team leaks into the calendar links")
			}
		})
	}
}

func TestTeamTabsAreNotFoundForOutsidersAndUnseenTeams(t *testing.T) {
	for _, suffix := range []string{"/events", "/calendar"} {
		dependencies, events := calendarDependencies(t, nil)
		dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
		if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t2"+suffix, "", false); response.Code != http.StatusNotFound || events.calls != 0 {
			t.Errorf("non-member %s status = %d, calls = %d", suffix, response.Code, events.calls)
		}
		dependencies, events = calendarDependencies(t, playerOfTeam(1))
		dependencies.Teams = unseenTeams{dependencies.Teams}
		if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t2"+suffix, "", false); response.Code != http.StatusNotFound || events.calls != 0 {
			t.Errorf("unseen team %s status = %d, calls = %d", suffix, response.Code, events.calls)
		}
		if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/x"+suffix, "", false); response.Code != http.StatusNotFound {
			t.Errorf("malformed team %s status = %d", suffix, response.Code)
		}
	}
	dependencies, _ := calendarDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	dependencies.Teams = unseenTeams{dependencies.Teams}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t2/events", "title=x&team_id=2", false); response.Code != http.StatusNotFound {
		t.Errorf("create on an unseen team status = %d", response.Code)
	}
}

func TestTeamRosterShowsParentsNextToEachChild(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	name := func(s string) *string { return &s }
	teams.detail = service.TeamDetail{
		Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016},
		Roster: []service.TeamRosterEntry{
			{UserID: 8, Role: service.GroupRolePlayer, DisplayName: name("Anna Kid")},
			{UserID: 9, Role: service.GroupRolePlayer, DisplayName: name("Ben Kid")},
			{UserID: 10, Role: service.GroupRolePlayer, DisplayName: name("Cili Kid")},
			{UserID: 20, Role: service.GroupRoleParent, DisplayName: name("Pat Parent"), ChildUserIDs: []int64{8, 9}, Linked: true},
			{UserID: 21, Role: service.GroupRoleParent, DisplayName: name("Olga Other")},
		},
	}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false).Body.String()
	for _, id := range []string{"8", "9"} {
		row := rosterRow(t, body, id, "player")
		if !strings.Contains(row, `data-testid="roster-parents"`) || !strings.Contains(row, "Pat Parent") {
			t.Errorf("player %s row lacks the parent: %s", id, row)
		}
	}
	if row := rosterRow(t, body, "10", "player"); strings.Contains(row, "roster-parents") {
		t.Error("a player without a guardian shows parents")
	}
	if got := strings.Count(body, `data-roster-user="20" data-roster-role="parent"`); got != 2 {
		t.Errorf("parent of two children listed %d times, want 2", got)
	}
	other := body[strings.Index(body, `data-testid="team-other-parents"`):]
	if !strings.Contains(other, "Olga Other") || strings.Contains(other, "Pat Parent") {
		t.Errorf("other parents = %s", other)
	}
}

// rosterRow returns the list item of one roster entry.
func rosterRow(t *testing.T, body, userID, role string) string {
	t.Helper()
	start := strings.Index(body, `data-roster-user="`+userID+`" data-roster-role="`+role+`"`)
	if start < 0 {
		t.Fatalf("roster row %s/%s missing", userID, role)
	}
	end := strings.Index(body[start:], "</li>")
	return body[start : start+end]
}
