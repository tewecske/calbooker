package httpadapter

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestTeamsListAndCreate(t *testing.T) {
	description := "U10"
	archivedAt := int64(5)
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleManager))
	teams.summaries = []service.TeamSummary{
		{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, Description: &description}, Players: 2, Parents: 1},
		{Team: service.Team{ID: 5, Slug: "t5", GroupID: 3, Name: "Old Boys", BirthYearFrom: 1980, BirthYearTo: 1980, ArchivedAt: &archivedAt}},
	}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams", "", false).Body.String()
	for _, want := range []string{"Lions", "2015–2016", "U10", "Archived teams", "Old Boys", `id="team-new-name"`, `data-section="teams"`} {
		if !strings.Contains(body, want) {
			t.Errorf("teams page missing %q", want)
		}
	}

	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams", "name=Tigers&birth_year_from=2014&birth_year_to=2015&description=U11", true)
	if created.Header().Get("HX-Redirect") != "/en/groups/lions/teams/t99" {
		t.Fatalf("create redirect = %q, want the new team", created.Header().Get("HX-Redirect"))
	}
	if teams.lastInput != (service.TeamInput{Name: "Tigers", BirthYearFrom: 2014, BirthYearTo: 2015, Description: "U11"}) {
		t.Fatalf("input = %+v", teams.lastInput)
	}

	teams.writeErr = service.ErrInvalidTeamBirthYears
	invalid := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams", "name=Tigers&birth_year_from=2016&birth_year_to=2015", false)
	invalidBody := invalid.Body.String()
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidBody, "between 1900 and 2200") || !strings.Contains(invalidBody, `value="2016"`) {
		t.Fatalf("invalid create = %d, want 422 keeping values and showing the year error", invalid.Code)
	}
}

func TestTeamsListHidesCreateFromPlayers(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRolePlayer))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams", "", false).Body.String()
	if strings.Contains(body, `id="team-new-name"`) || !strings.Contains(body, "no teams you can see") {
		t.Fatal("player teams page must not offer the create form")
	}
}

func TestTeamDetailByRole(t *testing.T) {
	email := "kid@example.test"
	detail := service.TeamDetail{
		Team:       service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, Version: 6},
		Roster:     []service.TeamRosterEntry{{UserID: 8, Role: service.GroupRolePlayer, Email: &email}, {UserID: 9, Role: service.GroupRoleParent}},
		Candidates: []service.GroupMember{{UserID: 10, Roles: []string{service.GroupRolePlayer, service.GroupRoleParent}}, {UserID: 8, Roles: []string{service.GroupRolePlayer}}},
	}
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantEdit   bool
		wantRoster bool
		wantEmail  bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantEdit: true, wantRoster: true, wantEmail: true},
		{name: "coach of the team", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{4}}), wantRoster: true, wantEmail: true},
		{name: "player on the team", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: []service.TeamRole{{TeamID: 4, Role: service.GroupRolePlayer}}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, teams := groupTestDependencies(t, test.access)
			teams.detail = detail
			if !test.access.CanViewTeamEmails(4) {
				teams.detail.Roster = []service.TeamRosterEntry{{UserID: 8, Role: service.GroupRolePlayer}, {UserID: 9, Role: service.GroupRoleParent}}
			}
			if !test.access.CanManageRoster(4) {
				teams.detail.Candidates = nil
			}
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false)
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `id="team-heading"`) || !strings.Contains(body, "2015–2016") {
				t.Fatalf("status = %d, team page incomplete", response.Code)
			}
			checks := map[string]bool{
				`id="team-edit-name"`:           test.wantEdit,
				`id="team-roster-entry"`:        test.wantRoster,
				`name="entry" value="8:player"`: test.wantRoster,
				"kid@example.test":              test.wantEmail,
			}
			for needle, want := range checks {
				if strings.Contains(body, needle) != want {
					t.Errorf("body contains %q = %v, want %v", needle, !want, want)
				}
			}
			if test.wantRoster {
				// Member 8 already plays here, so only member 10's roles are offered.
				if !strings.Contains(body, `<option value="10:player">`) || !strings.Contains(body, `<option value="10:parent">`) || strings.Contains(body, `<option value="8:player">`) {
					t.Error("roster candidates wrong")
				}
			}
		})
	}
}

func TestTeamDetailOfInvisibleTeamIsNotFound(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRolePlayer))
	teams.detailErr = service.ErrTeamNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/x", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("malformed team id status = %d, want 404", response.Code)
	}
}

func TestTeamWrites(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, Version: 6}}

	saved := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4", "version=6&name=Lions&birth_year_from=2015&birth_year_to=2016", true)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "Team saved.") || teams.lastVersion != 6 {
		t.Fatalf("save = %d (version %d)", saved.Code, teams.lastVersion)
	}
	archived := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/archive", "version=7&archived=true", true)
	if archived.Code != http.StatusOK || !strings.Contains(archived.Body.String(), "Team archived.") || !teams.lastArchived {
		t.Fatalf("archive = %d", archived.Code)
	}
	added := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/roster/add", "entry=10%3Aparent", true)
	if added.Code != http.StatusOK || teams.lastUser != 10 || teams.lastRole != service.GroupRoleParent {
		t.Fatalf("roster add = %d (user %d role %q)", added.Code, teams.lastUser, teams.lastRole)
	}
	removed := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/roster/remove", "entry=10:parent", true)
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), "Removed from the team.") {
		t.Fatalf("roster remove = %d", removed.Code)
	}

	writes := teams.writes
	for _, entry := range []string{"", "10", "x:player", "10:coach", "-1:player"} {
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/roster/add", "entry="+entry, false)
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("entry %q status = %d, want 422", entry, response.Code)
		}
	}
	if teams.writes != writes {
		t.Fatal("malformed roster entries reached the use case")
	}

	for _, test := range []struct {
		err    error
		status int
		text   string
	}{
		{service.ErrRosterRoleMissing, http.StatusUnprocessableEntity, "player or parent role first"},
		{service.ErrRosterEntryNotFound, http.StatusNotFound, "no longer on this team"},
		{service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
		{service.ErrTeamNotFound, http.StatusNotFound, ""},
	} {
		teams.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/roster/add", "entry=10:player", false)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
			t.Errorf("error %v = %d, want %d with %q", test.err, response.Code, test.status, test.text)
		}
	}
}

type teamsUseCaseStub struct {
	summaries    []service.TeamSummary
	detail       service.TeamDetail
	detailErr    error
	writeErr     error
	calls        int
	writes       int
	lastInput    service.TeamInput
	lastVersion  int64
	lastArchived bool
	lastUser     int64
	lastRole     string
}

func (s *teamsUseCaseStub) write() error {
	s.calls++
	s.writes++
	return s.writeErr
}

func (s *teamsUseCaseStub) List(context.Context, *service.GroupAccess) ([]service.TeamSummary, error) {
	s.calls++
	return s.summaries, nil
}

func (s *teamsUseCaseStub) Create(_ context.Context, _ service.GroupActor, input service.TeamInput) (service.Team, error) {
	s.lastInput = input
	return service.Team{ID: 99, Slug: "t99", GroupID: 3, Name: input.Name}, s.write()
}

// stubTeamSlug names team 4 "t4": stub teams are addressed as t<ID>.
func (s *teamsUseCaseStub) BySlug(_ context.Context, _ *service.GroupAccess, slug string) (service.Team, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(slug, "t"), 10, 64)
	if err != nil || !strings.HasPrefix(slug, "t") {
		return service.Team{}, service.ErrTeamNotFound
	}
	return service.Team{ID: id, GroupID: 3, Name: "Team " + slug, Slug: slug}, nil
}

func (s *teamsUseCaseStub) ByID(_ context.Context, _ *service.GroupAccess, teamID int64) (service.Team, error) {
	return service.Team{ID: teamID, GroupID: 3, Slug: "t" + strconv.FormatInt(teamID, 10)}, nil
}

func (s *teamsUseCaseStub) Slugs(context.Context, *service.GroupAccess) (map[int64]string, error) {
	slugs := make(map[int64]string)
	for id := int64(1); id <= 100; id++ {
		slugs[id] = "t" + strconv.FormatInt(id, 10)
	}
	return slugs, nil
}

func (s *teamsUseCaseStub) Detail(context.Context, *service.GroupAccess, int64) (service.TeamDetail, error) {
	s.calls++
	return s.detail, s.detailErr
}

func (s *teamsUseCaseStub) Update(_ context.Context, _ service.GroupActor, _, version int64, input service.TeamInput) (service.Team, error) {
	s.lastVersion, s.lastInput = version, input
	return s.detail.Team, s.write()
}

func (s *teamsUseCaseStub) SetArchived(_ context.Context, _ service.GroupActor, _, version int64, archived bool) (service.Team, error) {
	s.lastVersion, s.lastArchived = version, archived
	return s.detail.Team, s.write()
}

func (s *teamsUseCaseStub) AddToRoster(_ context.Context, _ service.GroupActor, _, userID int64, role string) error {
	s.lastUser, s.lastRole = userID, role
	return s.write()
}

func (s *teamsUseCaseStub) RemoveFromRoster(_ context.Context, _ service.GroupActor, _, userID int64, role string) error {
	s.lastUser, s.lastRole = userID, role
	return s.write()
}
