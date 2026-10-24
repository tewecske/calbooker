package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

func seasonDay(raw string) time.Time {
	return service.ParseSeasonDate(raw)
}

func seasonFixtures() []service.Season {
	return []service.Season{
		{
			ID: 2, GroupID: 3, Name: "2027/28", StartsOn: seasonDay("2027-07-01"), EndsOn: seasonDay("2028-06-30"),
			Parts: []service.SeasonPart{{ID: 21, Position: 1, Name: "Whole year", StartsOn: seasonDay("2027-07-01"), EndsOn: seasonDay("2028-06-30")}},
		},
		{
			ID: 1, GroupID: 3, Name: "2026/27", StartsOn: seasonDay("2026-08-01"), EndsOn: seasonDay("2027-06-30"), Version: 5,
			Parts: []service.SeasonPart{
				{ID: 11, Position: 1, Name: "Autumn", StartsOn: seasonDay("2026-08-01"), EndsOn: seasonDay("2026-12-20")},
				{ID: 12, Position: 2, Name: "Spring", StartsOn: seasonDay("2027-01-04"), EndsOn: seasonDay("2027-06-30"), AlternatingWeeks: true},
			},
		},
	}
}

func seasonDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *seasonsUseCaseStub, *teamsUseCaseStub) {
	t.Helper()
	dependencies, _, teams := groupTestDependencies(t, access)
	fixtures := seasonFixtures()
	coachName := "Carla Coach"
	coachEmail := "carla@example.test"
	seasons := &seasonsUseCaseStub{
		list:    fixtures,
		found:   fixtures[1],
		current: fixtures[1], hasCurrent: true,
		coaches: []service.SeasonCoach{{GroupID: 3, SeasonID: 1, TeamID: 4, CoachUserID: 8, IsHead: true, TeamName: "Lions", DisplayName: &coachName, Email: &coachEmail}},
		coaching: service.TeamCoaching{
			Seasons:         fixtures,
			CurrentSeasonID: 1,
			Coaches:         []service.SeasonCoach{{GroupID: 3, SeasonID: 1, TeamID: 4, CoachUserID: 8, IsHead: true, DisplayName: &coachName}},
		},
	}
	dependencies.Seasons = seasons
	teams.detail = service.TeamDetail{Team: service.Team{ID: 4, GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, Version: 6}}
	return dependencies, seasons, teams
}

func TestSeasonsListByRole(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantStatus int
		wantCreate bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantStatus: http.StatusOK, wantCreate: true},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), wantStatus: http.StatusOK, wantCreate: true},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantStatus: http.StatusOK, wantCreate: true},
		{name: "coach reads", access: groupMemberAs(service.GroupRoleCoach), wantStatus: http.StatusOK},
		{name: "player is denied", access: groupMemberAs(service.GroupRolePlayer), wantStatus: http.StatusForbidden},
		{name: "parent is denied", access: groupMemberAs(service.GroupRoleParent), wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, seasons, _ := seasonDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons", "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantStatus != http.StatusOK {
				if seasons.calls != 0 || strings.Contains(body, "2026/27") {
					t.Fatal("denied account reached the season list")
				}
				return
			}
			for _, want := range []string{`id="group-seasons-heading"`, "2026/27", "2027/28", "Autumn", "Spring", "2026-08-01 – 2027-06-30", `data-season-id="1" data-season-current="true"`, "A/B weeks", `data-section="seasons" aria-current="page"`, `href="/en/groups/3/seasons/1"`} {
				if !strings.Contains(body, want) {
					t.Errorf("seasons page missing %q", want)
				}
			}
			if strings.Contains(body, `id="season-new-name"`) != test.wantCreate {
				t.Errorf("create form shown = %v, want %v", !test.wantCreate, test.wantCreate)
			}
			if strings.Contains(body, `data-testid="seasons-read-only"`) == test.wantCreate {
				t.Error("read-only notice must show exactly when the create form is hidden")
			}
		})
	}
}

func TestSeasonsNavigationFollowsAccess(t *testing.T) {
	for _, test := range []struct {
		access *service.GroupAccess
		want   bool
	}{
		{groupMemberAs(service.GroupRoleCoach), true},
		{groupMemberAs(service.GroupRoleManager), true},
		{groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), true},
		{groupMemberAs(service.GroupRolePlayer), false},
		{groupMemberAs(service.GroupRoleParent), false},
	} {
		dependencies, _, _ := seasonDependencies(t, test.access)
		body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false).Body.String()
		if strings.Contains(body, `data-section="seasons"`) != test.want {
			t.Errorf("roles %v: seasons tab shown = %v, want %v", test.access.Roles(), !test.want, test.want)
		}
	}
}

func TestSeasonRoutesOutsiderGetsNotFound(t *testing.T) {
	dependencies, seasons, teams := seasonDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, test := range []struct{ method, target string }{
		{http.MethodGet, "/en/groups/3/seasons"},
		{http.MethodPost, "/en/groups/3/seasons"},
		{http.MethodGet, "/en/groups/3/seasons/1"},
		{http.MethodPost, "/en/groups/3/seasons/1"},
		{http.MethodPost, "/en/groups/3/seasons/1/delete"},
		{http.MethodPost, "/en/groups/3/teams/4/coaches/add"},
		{http.MethodPost, "/en/groups/3/teams/4/coaches/head"},
		{http.MethodPost, "/en/groups/3/teams/4/coaches/remove"},
		{http.MethodGet, "/hu/groups/3/seasons"},
	} {
		response := serveGroups(t, dependencies, test.method, test.target, "version=1&name=X&season_id=1&coach_user_id=8", false)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
			t.Errorf("%s %s = %d, want 404 without the group name", test.method, test.target, response.Code)
		}
	}
	if seasons.calls != 0 || teams.calls != 0 {
		t.Fatal("outsider requests reached the use cases")
	}
}

func TestSeasonCreate(t *testing.T) {
	dependencies, seasons, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	form := "name=2026%2F27&starts_on=2026-08-01&ends_on=2027-06-30" +
		"&part1_name=Autumn&part1_starts_on=2026-08-01&part1_ends_on=2026-12-20" +
		"&part2_name=Spring&part2_starts_on=2027-01-04&part2_ends_on=2027-06-30&part2_alternating=true"
	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons", form, false)
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/en/groups/3/seasons/99" {
		t.Fatalf("create = %d %q, want 303 to the new season", created.Code, created.Header().Get("Location"))
	}
	input := seasons.lastInput
	if input.Name != "2026/27" || service.FormatSeasonDate(input.StartsOn) != "2026-08-01" || len(input.Parts) != 2 || input.Parts[0].AlternatingWeeks || !input.Parts[1].AlternatingWeeks || service.FormatSeasonDate(input.Parts[1].StartsOn) != "2027-01-04" {
		t.Fatalf("input = %+v", input)
	}
	if htmx := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons", form, true); htmx.Code != http.StatusOK || htmx.Header().Get("HX-Redirect") != "/en/groups/3/seasons/99" {
		t.Fatalf("HTMX create = %d %q", htmx.Code, htmx.Header().Get("HX-Redirect"))
	}

	// An empty part 2 means one part; bad dates are passed on as zero.
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons", "name=X&starts_on=soon&ends_on=2027-06-30&part1_name=A&part1_starts_on=2026-08-01&part1_ends_on=2026-12-20&part2_name=+&part2_starts_on=&part2_ends_on=", true)
	if len(seasons.lastInput.Parts) != 1 || !seasons.lastInput.StartsOn.IsZero() {
		t.Fatalf("one-part input = %+v", seasons.lastInput)
	}

	for _, test := range []struct {
		err   error
		text  string
		field string
	}{
		{service.ErrInvalidSeasonName, "up to 64 characters", `id="season-new-name-error"`},
		{service.ErrSeasonNameTaken, "already has this name", `id="season-new-name-error"`},
		{service.ErrInvalidSeasonDates, "first day before the last day", `id="season-new-dates-error"`},
		{service.ErrSeasonOverlap, "overlap another season", `id="season-new-dates-error"`},
		{&service.SeasonPartError{Position: 1, Err: service.ErrInvalidSeasonPartName}, "part name up to 64", `id="season-new-part1-name-error"`},
		{&service.SeasonPartError{Position: 1, Err: service.ErrSeasonPartOutsideSeason}, "inside the season", `id="season-new-part1-dates-error"`},
		{&service.SeasonPartError{Position: 2, Err: service.ErrInvalidSeasonPartDates}, "last day of the part", `id="season-new-part2-dates-error"`},
		{&service.SeasonPartError{Position: 2, Err: service.ErrSeasonPartsOverlap}, "Part 2 must start after part 1 ends", `id="season-new-part2-dates-error"`},
		{service.ErrSeasonPartsOverlap, "Part 2 must start after part 1 ends", `id="season-new-part2-dates-error"`},
	} {
		seasons.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons", form, true)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, test.text) || !strings.Contains(body, test.field) || !strings.Contains(body, `value="Spring"`) || !strings.Contains(body, `value="2027-01-04"`) {
			t.Errorf("error %v = %d, want 422 with %q on the field and the submitted values", test.err, response.Code, test.text)
		}
		if !strings.Contains(body, `id="season-new-part2-alternating" class="checkbox" type="checkbox" name="part2_alternating" value="true" checked`) {
			t.Errorf("error %v: submitted alternating weeks not kept", test.err)
		}
	}
}

func TestSeasonCreateForbiddenForCoach(t *testing.T) {
	dependencies, seasons, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleCoach))
	seasons.writeErr = service.ErrGroupForbidden
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons", "name=X", false); response.Code != http.StatusForbidden {
		t.Fatalf("coach create = %d, want 403", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons", "name=X", true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("coach HTMX create = %d, want 422 so the alert swaps", response.Code)
	}
	player, playerSeasons, _ := seasonDependencies(t, groupMemberAs(service.GroupRolePlayer))
	if response := serveGroups(t, player, http.MethodPost, "/en/groups/3/seasons", "name=X", false); response.Code != http.StatusForbidden || playerSeasons.writes != 0 {
		t.Fatalf("player create = %d (writes %d), want 403 before the use case", response.Code, playerSeasons.writes)
	}
}

func TestSeasonDetailByRole(t *testing.T) {
	for _, test := range []struct {
		name     string
		access   *service.GroupAccess
		wantEdit bool
	}{
		{name: "manager edits", access: groupMemberAs(service.GroupRoleManager), wantEdit: true},
		{name: "site administrator edits", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantEdit: true},
		{name: "coach reads", access: groupMemberAs(service.GroupRoleCoach)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, _ := seasonDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons/1", "", false)
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `id="season-heading"`) || !strings.Contains(body, `data-testid="season-current-badge"`) || !strings.Contains(body, `data-section="seasons" aria-current="page"`) {
				t.Fatalf("status = %d, season page incomplete", response.Code)
			}
			for _, want := range []string{`data-part-position="2"`, `data-testid="season-part-alternating"`, `data-testid="season-coaches"`, "Carla Coach", "Head coach", `href="/en/groups/3/teams/4"`} {
				if !strings.Contains(body, want) {
					t.Errorf("season page missing %q", want)
				}
			}
			for _, needle := range []string{`id="season-edit-name"`, `/en/groups/3/seasons/1/delete`, `name="version" value="5"`} {
				if strings.Contains(body, needle) != test.wantEdit {
					t.Errorf("body contains %q = %v, want %v", needle, !test.wantEdit, test.wantEdit)
				}
			}
			if test.wantEdit && (!strings.Contains(body, `id="season-edit-part2-name" class="input w-full" name="part2_name" maxlength="64" value="Spring"`) || !strings.Contains(body, `value="2026-12-20"`)) {
				t.Error("edit form not filled from the stored season")
			}
		})
	}
	dependencies, seasons, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	seasons.findErr = service.ErrSeasonNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons/9", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("missing season = %d, want 404", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons/x", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("malformed id = %d, want 404", response.Code)
	}
	player, _, _ := seasonDependencies(t, groupMemberAs(service.GroupRolePlayer))
	if response := serveGroups(t, player, http.MethodGet, "/en/groups/3/seasons/1", "", false); response.Code != http.StatusForbidden {
		t.Fatalf("player detail = %d, want 403", response.Code)
	}
}

func TestSeasonUpdateAndDelete(t *testing.T) {
	dependencies, seasons, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	form := "version=5&name=Renamed&starts_on=2026-08-01&ends_on=2027-06-30&part1_name=Autumn&part1_starts_on=2026-08-01&part1_ends_on=2026-12-20"
	saved := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons/1", form, true)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "Season saved.") || seasons.lastVersion != 5 || seasons.lastID != 1 || seasons.lastInput.Name != "Renamed" || len(seasons.lastInput.Parts) != 1 {
		t.Fatalf("save = %d (version %d, id %d, input %+v)", saved.Code, seasons.lastVersion, seasons.lastID, seasons.lastInput)
	}
	deleted := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons/1/delete", "version=6", false)
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/en/groups/3/seasons" || seasons.lastVersion != 6 {
		t.Fatalf("delete = %d %q", deleted.Code, deleted.Header().Get("Location"))
	}

	writes := seasons.writes
	for _, target := range []string{"/en/groups/3/seasons/1", "/en/groups/3/seasons/1/delete"} {
		for _, version := range []string{"", "x", "-1"} {
			if response := serveGroups(t, dependencies, http.MethodPost, target, "version="+version+"&name=A", false); response.Code != http.StatusConflict {
				t.Errorf("%s version %q = %d, want 409", target, version, response.Code)
			}
		}
	}
	if seasons.writes != writes {
		t.Fatal("malformed versions reached the use case")
	}

	for _, test := range []struct {
		target string
		err    error
		status int
		text   string
	}{
		{"/en/groups/3/seasons/1", service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
		{"/en/groups/3/seasons/1", service.ErrSeasonNotFound, http.StatusNotFound, ""},
		{"/en/groups/3/seasons/1", service.ErrSeasonOverlap, http.StatusUnprocessableEntity, "overlap another season"},
		{"/en/groups/3/seasons/1", service.ErrGroupForbidden, http.StatusForbidden, ""},
		{"/en/groups/3/seasons/1/delete", service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
		{"/en/groups/3/seasons/1/delete", service.ErrSeasonNotFound, http.StatusNotFound, ""},
	} {
		seasons.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, test.target, form, false)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
			t.Errorf("%s error %v = %d, want %d with %q", test.target, test.err, response.Code, test.status, test.text)
		}
	}
}

func TestTeamCoachesCard(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		coaching   func(*service.TeamCoaching)
		wantCard   bool
		wantManage bool
	}{
		{name: "admin assigns", access: groupMemberAs(service.GroupRoleAdmin), wantCard: true, wantManage: true,
			coaching: func(c *service.TeamCoaching) { c.Candidates = []service.GroupMember{{UserID: 8}, {UserID: 9}} }},
		{name: "coach of the team reads", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{4}}), wantCard: true},
		{name: "player sees the current coaches", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: []service.TeamRole{{TeamID: 4, Role: service.GroupRolePlayer}}}), wantCard: true,
			coaching: func(c *service.TeamCoaching) { c.Seasons = c.Seasons[1:] }},
		{name: "player without a current season sees no card", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: []service.TeamRole{{TeamID: 4, Role: service.GroupRolePlayer}}}),
			coaching: func(c *service.TeamCoaching) { *c = service.TeamCoaching{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, seasons, _ := seasonDependencies(t, test.access)
			if test.coaching != nil {
				test.coaching(&seasons.coaching)
			}
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/teams/4", "", false)
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `id="team-heading"`) {
				t.Fatalf("status = %d, team page incomplete", response.Code)
			}
			if strings.Contains(body, `data-testid="team-coaches"`) != test.wantCard {
				t.Fatalf("coaches card shown = %v, want %v", !test.wantCard, test.wantCard)
			}
			if test.wantCard && (!strings.Contains(body, "Carla Coach") || !strings.Contains(body, `data-coach-id="8" data-coach-head="true"`) || !strings.Contains(body, `data-season-id="1" data-season-current="true"`)) {
				t.Error("coaches card misses the current head coach")
			}
			for _, needle := range []string{`id="team-coach-user"`, `/en/groups/3/teams/4/coaches/remove`, `/en/groups/3/teams/4/coaches/head`, `<option value="1" selected>2026/27</option>`} {
				if strings.Contains(body, needle) != test.wantManage {
					t.Errorf("body contains %q = %v, want %v", needle, !test.wantManage, test.wantManage)
				}
			}
		})
	}

	// Admins without candidates or seasons get guidance instead of a form.
	dependencies, seasons, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleManager))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/teams/4", "", false).Body.String()
	if !strings.Contains(body, `data-testid="team-coaches-no-candidates"`) || strings.Contains(body, `id="team-coach-user"`) {
		t.Error("manager without coach candidates must see the hint")
	}
	seasons.coaching = service.TeamCoaching{}
	body = serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/teams/4", "", false).Body.String()
	if !strings.Contains(body, `data-testid="team-coaches-no-seasons"`) || !strings.Contains(body, `href="/en/groups/3/seasons"`) {
		t.Error("manager without seasons must see the link to the seasons page")
	}
	seasons.coachingErr = service.ErrTeamNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/teams/4", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("coaching not found = %d, want 404", response.Code)
	}
}

func TestTeamCoachWrites(t *testing.T) {
	dependencies, seasons, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	added := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/teams/4/coaches/add", "season_id=1&coach_user_id=8&head=true", true)
	if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), "Coach assigned.") || !strings.Contains(added.Body.String(), `id="alerts"`) {
		t.Fatalf("assign = %d", added.Code)
	}
	if seasons.lastAssignment != (service.CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8, Head: true}) {
		t.Fatalf("assignment = %+v", seasons.lastAssignment)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/teams/4/coaches/add", "season_id=1&coach_user_id=9", true)
	if seasons.lastAssignment.Head {
		t.Fatal("assignment without the head box must not be head coach")
	}
	head := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/teams/4/coaches/head", "season_id=1&coach_user_id=9&head=true", true)
	if head.Code != http.StatusOK || !strings.Contains(head.Body.String(), "Head coach set.") || !seasons.lastAssignment.Head || seasons.lastAssignment.CoachUserID != 9 {
		t.Fatalf("set head = %d (%+v)", head.Code, seasons.lastAssignment)
	}
	unset := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/teams/4/coaches/head", "season_id=1&coach_user_id=9&head=false", true)
	if unset.Code != http.StatusOK || !strings.Contains(unset.Body.String(), "Head coach mark removed.") || seasons.lastAssignment.Head {
		t.Fatalf("unset head = %d", unset.Code)
	}
	removed := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/teams/4/coaches/remove", "season_id=1&coach_user_id=8", true)
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), "Coach removed.") || seasons.lastRemoved != [3]int64{1, 4, 8} {
		t.Fatalf("remove = %d (%v)", removed.Code, seasons.lastRemoved)
	}

	writes := seasons.writes
	for _, form := range []string{"coach_user_id=8", "season_id=x&coach_user_id=8", "season_id=1", "season_id=1&coach_user_id=0"} {
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/teams/4/coaches/add", form, false)
		if response.Code != http.StatusNotFound && response.Code != http.StatusUnprocessableEntity {
			t.Errorf("form %q = %d, want 404 or 422", form, response.Code)
		}
	}
	if seasons.writes != writes {
		t.Fatal("malformed coach forms reached the use case")
	}

	for _, test := range []struct {
		target string
		err    error
		status int
		text   string
	}{
		{"/en/groups/3/teams/4/coaches/add", service.ErrSeasonCoachRoleMissing, http.StatusUnprocessableEntity, "Only members with the coach role"},
		{"/en/groups/3/teams/4/coaches/add", service.ErrGroupForbidden, http.StatusForbidden, ""},
		{"/en/groups/3/teams/4/coaches/add", service.ErrSeasonNotFound, http.StatusNotFound, ""},
		{"/en/groups/3/teams/4/coaches/add", service.ErrTeamNotFound, http.StatusNotFound, ""},
		{"/en/groups/3/teams/4/coaches/head", service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
		{"/en/groups/3/teams/4/coaches/remove", service.ErrSeasonCoachNotFound, http.StatusNotFound, "no longer assigned"},
	} {
		seasons.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, test.target, "season_id=1&coach_user_id=8", false)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
			t.Errorf("%s error %v = %d, want %d with %q", test.target, test.err, response.Code, test.status, test.text)
		}
	}

	// Coaches and players are refused by the use case; HTMX gets 422.
	for _, access := range []*service.GroupAccess{
		groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{4}}),
		groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: []service.TeamRole{{TeamID: 4, Role: service.GroupRolePlayer}}}),
	} {
		dependencies, seasons, _ := seasonDependencies(t, access)
		seasons.writeErr = service.ErrGroupForbidden
		if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/teams/4/coaches/add", "season_id=1&coach_user_id=8", true); response.Code != http.StatusUnprocessableEntity {
			t.Errorf("roles %v HTMX assign = %d, want 422", access.Roles(), response.Code)
		}
	}
}

func TestSeasonsHungarianLabels(t *testing.T) {
	dependencies, _, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/3/seasons", "", false).Body.String()
	for _, want := range []string{"Szezonok", "Szezon hozzáadása", "1. szakasz", "Váltakozó A/B hetek", "Aktuális", `href="/hu/groups/3/seasons/1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Hungarian seasons page missing %q", want)
		}
	}
	team := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/3/teams/4", "", false).Body.String()
	for _, want := range []string{"Edzők", "Vezetőedző"} {
		if !strings.Contains(team, want) {
			t.Errorf("Hungarian team page missing %q", want)
		}
	}
}

type seasonsUseCaseStub struct {
	list           []service.Season
	found          service.Season
	findErr        error
	current        service.Season
	hasCurrent     bool
	coaches        []service.SeasonCoach
	coaching       service.TeamCoaching
	coachingErr    error
	writeErr       error
	calls          int
	writes         int
	lastInput      service.SeasonInput
	lastID         int64
	lastVersion    int64
	lastAssignment service.CoachAssignment
	lastRemoved    [3]int64
}

func (s *seasonsUseCaseStub) write() error {
	s.calls++
	s.writes++
	return s.writeErr
}

func (s *seasonsUseCaseStub) List(context.Context, *service.GroupAccess) ([]service.Season, error) {
	s.calls++
	return s.list, nil
}

func (s *seasonsUseCaseStub) Find(context.Context, *service.GroupAccess, int64) (service.Season, error) {
	s.calls++
	return s.found, s.findErr
}

func (s *seasonsUseCaseStub) Current(context.Context, *service.GroupAccess) (service.Season, bool, error) {
	s.calls++
	return s.current, s.hasCurrent, nil
}

func (s *seasonsUseCaseStub) Create(_ context.Context, _ service.GroupActor, input service.SeasonInput) (service.Season, error) {
	s.lastInput = input
	return service.Season{ID: 99, GroupID: 3, Name: input.Name}, s.write()
}

func (s *seasonsUseCaseStub) Update(_ context.Context, _ service.GroupActor, id, version int64, input service.SeasonInput) (service.Season, error) {
	s.lastID, s.lastVersion, s.lastInput = id, version, input
	return s.found, s.write()
}

func (s *seasonsUseCaseStub) Delete(_ context.Context, _ service.GroupActor, id, version int64) error {
	s.lastID, s.lastVersion = id, version
	return s.write()
}

func (s *seasonsUseCaseStub) SeasonCoaches(context.Context, *service.GroupAccess, int64) ([]service.SeasonCoach, error) {
	s.calls++
	return s.coaches, nil
}

func (s *seasonsUseCaseStub) TeamCoaching(context.Context, *service.GroupAccess, int64) (service.TeamCoaching, error) {
	s.calls++
	return s.coaching, s.coachingErr
}

func (s *seasonsUseCaseStub) AssignCoach(_ context.Context, _ service.GroupActor, assignment service.CoachAssignment) error {
	s.lastAssignment = assignment
	return s.write()
}

func (s *seasonsUseCaseStub) RemoveCoach(_ context.Context, _ service.GroupActor, seasonID, teamID, coachUserID int64) error {
	s.lastRemoved = [3]int64{seasonID, teamID, coachUserID}
	return s.write()
}
