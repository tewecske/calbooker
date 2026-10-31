package httpadapter

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestTeamEventPageByRole(t *testing.T) {
	tests := []struct {
		name     string
		access   *service.GroupAccess
		wantEdit bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantEdit: true},
		{name: "coach of the team", access: coachOfTeam(4), wantEdit: true},
		{name: "player of the team", access: playerOfTeam(4)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _ := eventDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4/events/5", "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			for _, want := range []string{
				"Autumn cup",
				`data-team-tab="events" aria-selected="true" aria-current="page"`,
				`href="/en/groups/lions/teams/t4/calendar" data-team-tab="calendar"`,
				`data-section="teams" aria-current="page"`,
				`href="/en/groups/lions/teams/t4/events">Back to events`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("team event page missing %q", want)
				}
			}
			if test.wantEdit && !strings.Contains(body, `action="/en/groups/lions/teams/t4/events/5"`) {
				t.Error("edit form does not post to the team event URL")
			}
			if strings.Contains(body, `action="/en/groups/lions/events/5`) {
				t.Error("page posts to a group-level event URL")
			}
		})
	}
}

func TestTeamEventOfAnotherTeamIsNotFound(t *testing.T) {
	dependencies, _ := eventDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	for _, target := range []string{"/en/groups/lions/teams/t5/events/5", "/en/groups/lions/teams/bad.slug/events/5", "/en/groups/lions/teams/t4/events/abc"} {
		if response := serveGroups(t, dependencies, http.MethodGet, target, "", false); response.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, response.Code)
		}
	}
	for _, target := range []string{"/en/groups/lions/teams/t5/events/5", "/en/groups/lions/teams/t5/events/5/cancel", "/en/groups/lions/teams/t5/events/5/delete", "/en/groups/lions/teams/t5/events/5/participation"} {
		if response := serveGroups(t, dependencies, http.MethodPost, target, "version=2", false); response.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", target, response.Code)
		}
	}
	// An invisible event is 404 whatever the team.
	hidden, events := eventDependencies(t, playerOfTeam(7))
	events.detailErr = service.ErrEventNotFound
	if response := serveGroups(t, hidden, http.MethodGet, "/en/groups/lions/teams/t4/events/5", "", false); response.Code != http.StatusNotFound {
		t.Errorf("hidden event = %d, want 404", response.Code)
	}
	// A player of another team cannot view the event, so 404.
	other, _ := eventDependencies(t, playerOfTeam(7))
	if response := serveGroups(t, other, http.MethodGet, "/en/groups/lions/teams/t4/events/5", "", false); response.Code != http.StatusNotFound {
		t.Errorf("player of another team = %d, want 404", response.Code)
	}
}

func TestTeamEventCalledUpPlayerOpensIt(t *testing.T) {
	access := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: []service.TeamRole{{TeamID: 7, Role: service.GroupRolePlayer}}, CalledUpEventIDs: []int64{5}})
	dependencies, _ := eventDependencies(t, access)
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4/events/5", "", false)
	if response.Code != http.StatusOK {
		t.Fatalf("called-up player = %d, want 200", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Autumn cup") || strings.Contains(body, `data-team-tab=`) {
		t.Error("called-up player should see the event without tabs of a team that is not theirs")
	}
}

func TestTeamEventWritesStayInTheTeam(t *testing.T) {
	dependencies, events := eventDependencies(t, coachOfTeam(4))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/events/5", eventFormBody+"&version=2", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Event saved.") || !strings.Contains(response.Body.String(), `data-team-tab="events"`) {
		t.Fatalf("team update = %d", response.Code)
	}
	response = serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/events/5/cancel", "version=2&cancelled=true", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Event cancelled.") || !strings.Contains(response.Body.String(), `action="/en/groups/lions/teams/t4/events/5/cancel"`) {
		t.Fatalf("team cancel = %d", response.Code)
	}
	deleted := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/events/5/delete", "version=3", false)
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/en/groups/lions/teams/t4/events" || events.lastVersion != 3 {
		t.Fatalf("team delete = %d %q", deleted.Code, deleted.Header().Get("Location"))
	}
}

func TestTeamEventCreateRedirectsToTeamPage(t *testing.T) {
	dependencies, _ := eventDependencies(t, coachOfTeam(4, 5))
	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/events", eventFormBody, false)
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/en/groups/lions/teams/t4/events/99" {
		t.Fatalf("create = %d %q, want 303 to the team event", created.Code, created.Header().Get("Location"))
	}
}

func TestEventLinks(t *testing.T) {
	dependencies, _ := eventDependencies(t, playerOfTeam(4))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4/events", "", false).Body.String()
	if !strings.Contains(body, `href="/en/groups/lions/teams/t4/events/5"`) {
		t.Error("team Events tab does not link to the team event page")
	}
	// The group-level list keeps group URLs.
	body = serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/events", "", false).Body.String()
	if !strings.Contains(body, `href="/en/groups/lions/events/5"`) {
		t.Error("group Events list lost its group-level link")
	}
}

func TestSingleViewerTeam(t *testing.T) {
	event := service.Event{ID: 5, GroupID: 3, Teams: []service.EventTeam{{ID: 4}, {ID: 5}}}
	for name, test := range map[string]struct {
		access *service.GroupAccess
		want   int64
	}{
		"one of two teams":      {playerOfTeam(4), 4},
		"both teams":            {coachOfTeam(4, 5), 0},
		"neither team":          {playerOfTeam(9), 0},
		"admin sees both teams": {groupMemberAs(service.GroupRoleAdmin), 0},
	} {
		if got := singleViewerTeam(test.access, event); got != test.want {
			t.Errorf("%s = %d, want %d", name, got, test.want)
		}
	}
	single := service.Event{ID: 6, GroupID: 3, Teams: []service.EventTeam{{ID: 4}}}
	if got := singleViewerTeam(groupMemberAs(service.GroupRoleAdmin), single); got != 4 {
		t.Errorf("admin on a one-team event = %d, want 4", got)
	}
}
