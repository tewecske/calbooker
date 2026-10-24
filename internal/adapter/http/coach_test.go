package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

type coachesUseCaseStub struct {
	coaches     []service.Coach
	parts       service.AvailabilityParts
	windows     []service.AvailabilityWindow
	previous    bool
	writeErr    error
	viewErr     error
	calls       int
	writes      int
	lastCoach   int64
	lastPart    int64
	lastWindow  int64
	lastInput   service.AvailabilityInput
	lastMinAge  int
	lastMaxAge  int
	lastCleared int64
	lastAction  string
}

func (s *coachesUseCaseStub) part(partID int64) (service.Season, service.SeasonPart, bool) {
	for _, season := range s.parts.Seasons {
		if part, ok := season.Part(partID); ok {
			return season, part, true
		}
	}
	return service.Season{}, service.SeasonPart{}, false
}

func (s *coachesUseCaseStub) write(action string, partID, coachID int64) error {
	s.calls++
	s.writes++
	s.lastAction, s.lastPart, s.lastCoach = action, partID, coachID
	return s.writeErr
}

func (s *coachesUseCaseStub) Coaches(_ context.Context, access *service.GroupAccess) ([]service.Coach, error) {
	s.calls++
	if access.CanViewAllAvailability() {
		return s.coaches, nil
	}
	var own []service.Coach
	for _, coach := range s.coaches {
		if coach.UserID == access.UserID() {
			own = append(own, coach)
		}
	}
	return own, nil
}

func (s *coachesUseCaseStub) SetQualification(_ context.Context, _ service.GroupActor, coachID int64, minAge, maxAge int) (service.CoachQualification, error) {
	s.lastMinAge, s.lastMaxAge = minAge, maxAge
	return service.CoachQualification{CoachUserID: coachID, MinAge: minAge, MaxAge: maxAge}, s.write("qualification", 0, coachID)
}

func (s *coachesUseCaseStub) ClearQualification(_ context.Context, _ service.GroupActor, coachID int64) error {
	s.lastCleared = coachID
	return s.write("clear", 0, coachID)
}

func (s *coachesUseCaseStub) Parts(context.Context, *service.GroupAccess) (service.AvailabilityParts, error) {
	s.calls++
	return s.parts, nil
}

func (s *coachesUseCaseStub) view(partID int64) (service.PartAvailability, error) {
	s.calls++
	if s.viewErr != nil {
		return service.PartAvailability{}, s.viewErr
	}
	season, part, ok := s.part(partID)
	if !ok {
		return service.PartAvailability{}, service.ErrSeasonNotFound
	}
	return service.PartAvailability{Season: season, Part: part, Windows: s.windows}, nil
}

func (s *coachesUseCaseStub) Availability(_ context.Context, _ *service.GroupAccess, partID int64) (service.PartAvailability, error) {
	return s.view(partID)
}

func (s *coachesUseCaseStub) CoachAvailability(_ context.Context, _ *service.GroupAccess, partID, coachID int64) (service.PartAvailability, error) {
	view, err := s.view(partID)
	view.Windows = view.ForCoach(coachID)
	return view, err
}

func (s *coachesUseCaseStub) PreviousPart(_ context.Context, _ *service.GroupAccess, partID int64) (service.Season, service.SeasonPart, bool, error) {
	s.calls++
	if !s.previous {
		return service.Season{}, service.SeasonPart{}, false, nil
	}
	season, part, _ := s.part(11)
	return season, part, partID != 11, nil
}

func (s *coachesUseCaseStub) AddAvailability(_ context.Context, _ service.GroupActor, partID, coachID int64, input service.AvailabilityInput) error {
	s.lastInput = input
	return s.write("add", partID, coachID)
}

func (s *coachesUseCaseStub) MoveAvailability(_ context.Context, _ service.GroupActor, partID, coachID, windowID int64, input service.AvailabilityInput) error {
	s.lastInput, s.lastWindow = input, windowID
	return s.write("move", partID, coachID)
}

func (s *coachesUseCaseStub) DeleteAvailability(_ context.Context, _ service.GroupActor, partID, coachID, windowID int64) error {
	s.lastWindow = windowID
	return s.write("delete", partID, coachID)
}

func (s *coachesUseCaseStub) CopyPreviousAvailability(_ context.Context, _ service.GroupActor, partID, coachID int64) (int, error) {
	return 2, s.write("copy", partID, coachID)
}

// coachDependencies: user 7 acts; coaches are 7 (Carla, qualified 9-12)
// and 8 (Otto); seasons from seasonFixtures (part 11 every week, 12 A/B).
func coachDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *coachesUseCaseStub, *teamsUseCaseStub) {
	t.Helper()
	dependencies, _, teams := seasonDependencies(t, access)
	carla, otto := "Carla Coach", "Otto Other"
	fixtures := seasonFixtures()
	coaches := &coachesUseCaseStub{
		coaches: []service.Coach{
			{UserID: 7, DisplayName: &carla, Qualification: &service.CoachQualification{CoachUserID: 7, MinAge: 9, MaxAge: 12}},
			{UserID: 8, DisplayName: &otto},
		},
		parts: service.AvailabilityParts{Seasons: fixtures, DefaultSeason: fixtures[1], Default: fixtures[1].Parts[0], HasDefault: true},
		windows: []service.AvailabilityWindow{
			{ID: 31, CoachUserID: 7, SeasonPartID: 11, Week: service.SeasonWeekAll, Weekday: 2, StartMinute: 16 * 60, EndMinute: 18 * 60},
			{ID: 32, CoachUserID: 7, SeasonPartID: 11, Week: service.SeasonWeekA, Weekday: 3, StartMinute: 9 * 60, EndMinute: 10 * 60},
			{ID: 41, CoachUserID: 8, SeasonPartID: 11, Week: service.SeasonWeekAll, Weekday: 4, StartMinute: 17 * 60, EndMinute: 19 * 60},
		},
		previous: true,
	}
	dependencies.Coaches = coaches
	teams.summaries = []service.TeamSummary{{Team: service.Team{ID: 4, GroupID: 3, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016}}}
	return dependencies, coaches, teams
}

func TestCoachesPageByRole(t *testing.T) {
	for _, test := range []struct {
		name       string
		access     *service.GroupAccess
		wantStatus int
		staff      bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantStatus: http.StatusOK, staff: true},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), wantStatus: http.StatusOK, staff: true},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantStatus: http.StatusOK, staff: true},
		{name: "coach", access: groupMemberAs(service.GroupRoleCoach), wantStatus: http.StatusOK},
		{name: "player", access: groupMemberAs(service.GroupRolePlayer), wantStatus: http.StatusForbidden},
		{name: "parent", access: groupMemberAs(service.GroupRoleParent), wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, coaches, _ := coachDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/coaches", "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantStatus != http.StatusOK {
				if coaches.calls != 0 || strings.Contains(body, "Carla") {
					t.Fatal("denied account reached the coaches page")
				}
				return
			}
			for _, want := range []string{`id="group-coaches-heading"`, "Carla Coach", "9–12 years", `data-section="coaches" aria-current="page"`, "Ages in the season 2026/27.", `<td data-testid="team-age">10–11 years</td>`, `href="/en/groups/3/coaches/7/availability"`} {
				if !strings.Contains(body, want) {
					t.Errorf("coaches page missing %q", want)
				}
			}
			if strings.Contains(body, "Otto Other") != test.staff || strings.Contains(body, `id="coach-7-min-age"`) != test.staff ||
				strings.Contains(body, `data-testid="coaches-overview-link"`) != test.staff || strings.Contains(body, `data-testid="team-qualified-coaches">Carla Coach`) != test.staff {
				t.Errorf("staff parts shown wrongly (staff = %v)", test.staff)
			}
			if !test.staff && !strings.Contains(body, "Edit my availability") {
				t.Error("coach misses the link to their availability")
			}
		})
	}
}

func TestCoachesNavigationFollowsAccess(t *testing.T) {
	for _, test := range []struct {
		access *service.GroupAccess
		want   bool
	}{
		{groupMemberAs(service.GroupRoleCoach), true},
		{groupMemberAs(service.GroupRoleAdmin), true},
		{groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), true},
		{groupMemberAs(service.GroupRolePlayer), false},
		{groupMemberAs(service.GroupRoleParent), false},
	} {
		dependencies, _, _ := coachDependencies(t, test.access)
		body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false).Body.String()
		if strings.Contains(body, `data-section="coaches"`) != test.want {
			t.Errorf("roles %v: coaches tab shown = %v, want %v", test.access.Roles(), !test.want, test.want)
		}
	}
}

func TestCoachRoutesOutsiderGetsNotFound(t *testing.T) {
	dependencies, coaches, _ := coachDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, test := range []struct{ method, target string }{
		{http.MethodGet, "/en/groups/3/coaches"},
		{http.MethodPost, "/en/groups/3/coaches/7/qualification"},
		{http.MethodPost, "/en/groups/3/coaches/7/qualification/clear"},
		{http.MethodGet, "/en/groups/3/coaches/availability"},
		{http.MethodGet, "/en/groups/3/coaches/7/availability"},
		{http.MethodPost, "/en/groups/3/coaches/7/availability/create?part=11&week=all"},
		{http.MethodPost, "/en/groups/3/coaches/7/availability/move?part=11&week=all"},
		{http.MethodPost, "/en/groups/3/coaches/7/availability/delete?part=11&week=all"},
		{http.MethodPost, "/en/groups/3/coaches/7/availability/copy?part=11&week=all"},
		{http.MethodGet, "/hu/groups/3/coaches"},
	} {
		response := serveGroups(t, dependencies, test.method, test.target, "min_age=1&max_age=2&weekday=1&start=600&end=660&week=&item=31&window=31", false)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
			t.Errorf("%s %s = %d, want 404 without the group name", test.method, test.target, response.Code)
		}
	}
	if coaches.calls != 0 {
		t.Fatal("outsider requests reached the use cases")
	}
}

func TestCoachQualificationWrites(t *testing.T) {
	dependencies, coaches, _ := coachDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/8/qualification", "min_age=10&max_age=14", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Qualification saved.") || coaches.lastCoach != 8 || coaches.lastMinAge != 10 || coaches.lastMaxAge != 14 {
		t.Fatalf("set = %d, stub %+v", response.Code, coaches)
	}
	// Unreadable ages never reach the use case and keep the input.
	writes := coaches.writes
	response = serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/8/qualification", "min_age=ten&max_age=14", true)
	body := response.Body.String()
	if response.Code != http.StatusUnprocessableEntity || coaches.writes != writes || !strings.Contains(body, "Enter whole ages from 0 to 99") || !strings.Contains(body, `id="coach-8-error"`) || !strings.Contains(body, `value="ten"`) {
		t.Fatalf("invalid ages = %d", response.Code)
	}
	coaches.writeErr = service.ErrInvalidQualification
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/8/qualification", "min_age=14&max_age=10", false); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused ages = %d, want 422", response.Code)
	}
	coaches.writeErr = service.ErrCoachNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/9/qualification", "min_age=10&max_age=14", true); response.Code != http.StatusNotFound {
		t.Fatalf("not a coach = %d, want 404", response.Code)
	}
	coaches.writeErr = nil
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/8/qualification/clear", "", true); response.Code != http.StatusOK || coaches.lastCleared != 8 || !strings.Contains(response.Body.String(), "Qualification cleared.") {
		t.Fatalf("clear = %d", response.Code)
	}

	coach, coachStub, _ := coachDependencies(t, groupMemberAs(service.GroupRoleCoach))
	coachStub.writeErr = service.ErrGroupForbidden
	if response := serveGroups(t, coach, http.MethodPost, "/en/groups/3/coaches/7/qualification", "min_age=1&max_age=99", false); response.Code != http.StatusForbidden {
		t.Fatalf("coach set own qualification = %d, want 403", response.Code)
	}
	player, playerStub, _ := coachDependencies(t, groupMemberAs(service.GroupRolePlayer))
	if response := serveGroups(t, player, http.MethodPost, "/en/groups/3/coaches/7/qualification", "min_age=1&max_age=99", false); response.Code != http.StatusForbidden || playerStub.writes != 0 {
		t.Fatalf("player set qualification = %d, want 403", response.Code)
	}
}

func TestAvailabilityPageByRole(t *testing.T) {
	for _, test := range []struct {
		name       string
		access     *service.GroupAccess
		target     string
		wantStatus int
		heading    string
	}{
		{name: "coach themself", access: groupMemberAs(service.GroupRoleCoach), target: "/en/groups/3/coaches/7/availability", wantStatus: http.StatusOK, heading: "My availability"},
		{name: "another coach", access: groupMemberAs(service.GroupRoleCoach), target: "/en/groups/3/coaches/8/availability", wantStatus: http.StatusForbidden},
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), target: "/en/groups/3/coaches/8/availability", wantStatus: http.StatusOK, heading: "Availability of Otto Other"},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), target: "/en/groups/3/coaches/8/availability", wantStatus: http.StatusOK, heading: "Availability of Otto Other"},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), target: "/en/groups/3/coaches/8/availability", wantStatus: http.StatusOK, heading: "Availability of Otto Other"},
		{name: "player", access: groupMemberAs(service.GroupRolePlayer), target: "/en/groups/3/coaches/7/availability", wantStatus: http.StatusForbidden},
		{name: "admin, not a coach", access: groupMemberAs(service.GroupRoleAdmin), target: "/en/groups/3/coaches/9/availability", wantStatus: http.StatusNotFound},
		{name: "unknown part", access: groupMemberAs(service.GroupRoleAdmin), target: "/en/groups/3/coaches/7/availability?part=999", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, _ := coachDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, test.target, "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantStatus != http.StatusOK {
				return
			}
			body := response.Body.String()
			coach, own, other := "7", "31", "41"
			if test.heading != "My availability" {
				coach, own, other = "8", "41", "31"
			}
			for _, want := range []string{
				test.heading,
				`id="availability-grid"`,
				`data-move-url="/en/groups/3/coaches/` + coach + `/availability/move?part=11&amp;week=all"`,
				`data-create-url="/en/groups/3/coaches/` + coach + `/availability/create?part=11&amp;week=all"`,
				`data-wg-item="` + own + `"`,
				`<option value="11" selected>2026/27 · Autumn</option>`,
				`data-testid="availability-no-previous"`,
				`data-section="coaches" aria-current="page"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("availability page missing %q", want)
				}
			}
			// The A window does not count in a part without A/B weeks, and
			// another coach's windows never show.
			if coach == "7" && (!strings.Contains(body, `data-window-id="32"`) || !strings.Contains(body, "Not counted")) {
				t.Error("the stored A window must be listed as not counted")
			}
			if strings.Contains(body, `data-wg-item="32"`) || strings.Contains(body, `data-wg-item="`+other+`"`) || strings.Contains(body, `data-testid="availability-weeks"`) {
				t.Error("grid shows windows it must not")
			}
		})
	}
}

func TestAvailabilityAlternatingWeeks(t *testing.T) {
	dependencies, coaches, _ := coachDependencies(t, groupMemberAs(service.GroupRoleCoach))
	coaches.windows = []service.AvailabilityWindow{
		{ID: 51, CoachUserID: 7, SeasonPartID: 12, Week: service.SeasonWeekAll, Weekday: 1, StartMinute: 600, EndMinute: 700},
		{ID: 52, CoachUserID: 7, SeasonPartID: 12, Week: service.SeasonWeekA, Weekday: 2, StartMinute: 600, EndMinute: 700},
		{ID: 53, CoachUserID: 7, SeasonPartID: 12, Week: service.SeasonWeekB, Weekday: 3, StartMinute: 600, EndMinute: 700},
	}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/coaches/7/availability?part=12", "", false).Body.String()
	for _, want := range []string{`data-testid="availability-weeks"`, `href="/en/groups/3/coaches/7/availability?part=12&amp;week=B"`, `data-week="A"`, `data-wg-item="52" data-weekday="2"`, "Every week", `action="/en/groups/3/coaches/7/availability/copy?part=12&amp;week=A"`, "Replaces the windows of this part with those of 2026/27 · Autumn."} {
		if !strings.Contains(body, want) {
			t.Errorf("week A page missing %q", want)
		}
	}
	if strings.Contains(body, `data-wg-item="53"`) {
		t.Error("week A shows a week B window")
	}
	// Every-week windows show on the A tab but move only on their own tab.
	if !strings.Contains(body, `data-wg-item="51" data-weekday="1" data-start="600" data-end="700" data-resource="">`) {
		t.Error("every-week window on the A tab must not be draggable")
	}
	all := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/coaches/7/availability?part=12&week=all", "", false).Body.String()
	if !strings.Contains(all, `data-wg-item="51" data-weekday="1" data-start="600" data-end="700" data-resource="" data-wg-draggable`) || strings.Contains(all, `data-wg-item="52"`) || !strings.Contains(all, `data-week=""`) {
		t.Error("every-week tab shows the wrong windows")
	}

	// A change on the B tab stores a week B window; the grid's week must match.
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/create?part=12&week=B", "weekday=5&start=600&end=720&week=B", true)
	if response.Code != http.StatusOK || coaches.lastInput != (service.AvailabilityInput{Week: service.SeasonWeekB, Weekday: 5, StartMinute: 600, EndMinute: 720}) || coaches.lastPart != 12 {
		t.Fatalf("week B create = %d, input %+v", response.Code, coaches.lastInput)
	}
	writes := coaches.writes
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/create?part=12&week=B", "weekday=5&start=600&end=720&week=A", true); response.Code != http.StatusUnprocessableEntity || coaches.writes != writes {
		t.Fatalf("mismatched week = %d, want 422 without a write", response.Code)
	}
}

func TestAvailabilityGridChanges(t *testing.T) {
	dependencies, coaches, _ := coachDependencies(t, groupMemberAs(service.GroupRoleCoach))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/create?part=11&week=all", "weekday=2&start=965&end=1085&week=", true)
	body := response.Body.String()
	if response.Code != http.StatusOK || coaches.lastAction != "add" || coaches.lastInput != (service.AvailabilityInput{Week: service.SeasonWeekAll, Weekday: 2, StartMinute: 960, EndMinute: 1080}) {
		t.Fatalf("create = %d, input %+v", response.Code, coaches.lastInput)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), `<div id="alerts"`) || !strings.Contains(body, "hx-swap-oob") || !strings.Contains(body, "Availability added.") || !strings.Contains(body, `id="availability-grid"`) || strings.Contains(body, `id="page-content"`) {
		t.Fatal("HTMX change must answer with the alerts and the grid only")
	}
	plain := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/create?part=11&week=all", "weekday=2&start=960&end=1080&week=", false)
	if plain.Code != http.StatusSeeOther || plain.Header().Get("Location") != "/en/groups/3/coaches/7/availability?part=11&week=all" {
		t.Fatalf("plain create = %d %q", plain.Code, plain.Header().Get("Location"))
	}

	response = serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/move?part=11&week=all", "item=31&weekday=4&start=1020&end=1140&week=", true)
	if response.Code != http.StatusOK || coaches.lastAction != "move" || coaches.lastWindow != 31 || coaches.lastInput.Weekday != 4 || !strings.Contains(response.Body.String(), "Availability updated.") {
		t.Fatalf("move = %d, window %d input %+v", response.Code, coaches.lastWindow, coaches.lastInput)
	}

	writes := coaches.writes
	for _, form := range []string{"item=31&weekday=9&start=1020&end=1140&week=", "item=31&weekday=4&start=1020&end=1000&week=", "weekday=4&start=1020&end=1140&week="} {
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/move?part=11&week=all", form, true)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "That change does not fit the grid.") || !strings.Contains(response.Body.String(), `id="availability-grid"`) {
			t.Errorf("invalid move %q = %d, want 422 with the grid", form, response.Code)
		}
	}
	if coaches.writes != writes {
		t.Fatal("invalid changes reached the use case")
	}

	for err, text := range map[error]string{
		service.ErrAvailabilityNotFound:       "That window no longer exists.",
		service.ErrTooManyAvailabilityWindows: "at most 100 windows",
		service.ErrAvailabilityWeek:           "need a part with alternating weeks",
	} {
		coaches.writeErr = err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/move?part=11&week=all", "item=31&weekday=4&start=1020&end=1140&week=", true)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), text) {
			t.Errorf("%v = %d, want 422 with %q", err, response.Code, text)
		}
	}
	coaches.writeErr = service.ErrCoachNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/create?part=11&week=all", "weekday=2&start=960&end=1080&week=", true); response.Code != http.StatusNotFound {
		t.Fatalf("coach role gone = %d, want 404", response.Code)
	}
	coaches.writeErr = nil
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/create?week=all", "weekday=2&start=960&end=1080&week=", true); response.Code != http.StatusNotFound {
		t.Fatalf("missing part = %d, want 404", response.Code)
	}

	// Another coach's grid is refused before any change.
	coaches.viewErr = service.ErrGroupForbidden
	writes = coaches.writes
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/8/availability/create?part=11&week=all", "weekday=2&start=960&end=1080&week=", false); response.Code != http.StatusForbidden || coaches.writes != writes {
		t.Fatalf("other coach change = %d, want 403", response.Code)
	}
}

func TestAvailabilityDeleteAndCopy(t *testing.T) {
	dependencies, coaches, _ := coachDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/delete?part=11&week=all", "window=31", true)
	if response.Code != http.StatusOK || coaches.lastAction != "delete" || coaches.lastWindow != 31 || coaches.lastPart != 11 || !strings.Contains(response.Body.String(), "Availability removed.") {
		t.Fatalf("delete = %d, stub %+v", response.Code, coaches)
	}
	coaches.writeErr = service.ErrAvailabilityNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/delete?part=11&week=all", "window=99", false); response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "That window no longer exists.") {
		t.Fatalf("stale delete = %d", response.Code)
	}
	coaches.writeErr = nil
	response = serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/copy?part=12&week=A", "", true)
	if response.Code != http.StatusOK || coaches.lastAction != "copy" || coaches.lastPart != 12 || !strings.Contains(response.Body.String(), "Availability copied from the previous part.") {
		t.Fatalf("copy = %d", response.Code)
	}
	for err, text := range map[error]string{service.ErrNothingToCopy: "nothing changed", service.ErrNoPreviousSeasonPart: "nothing to copy"} {
		coaches.writeErr = err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/coaches/7/availability/copy?part=12&week=A", "", true)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), text) {
			t.Errorf("%v = %d, want 422 with %q", err, response.Code, text)
		}
	}
	coaches.writeErr = nil
	coaches.previous = false
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/coaches/7/availability", "", false).Body.String()
	if !strings.Contains(body, `data-testid="availability-no-previous"`) || strings.Contains(body, `data-testid="availability-copy"`) {
		t.Error("first part must not offer a copy")
	}
}

func TestAvailabilityOverview(t *testing.T) {
	for _, test := range []struct {
		name   string
		access *service.GroupAccess
		want   int
	}{
		{"admin", groupMemberAs(service.GroupRoleAdmin), http.StatusOK},
		{"manager", groupMemberAs(service.GroupRoleManager), http.StatusOK},
		{"site administrator", groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), http.StatusOK},
		{"coach", groupMemberAs(service.GroupRoleCoach), http.StatusForbidden},
		{"player", groupMemberAs(service.GroupRolePlayer), http.StatusForbidden},
	} {
		dependencies, coaches, _ := coachDependencies(t, test.access)
		response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/coaches/availability", "", false)
		if response.Code != test.want {
			t.Errorf("%s: status = %d, want %d", test.name, response.Code, test.want)
			continue
		}
		body := response.Body.String()
		if test.want != http.StatusOK {
			if coaches.calls != 0 || strings.Contains(body, "Otto") {
				t.Errorf("%s reached the side-by-side view", test.name)
			}
			continue
		}
		for _, want := range []string{"Availability of all coaches", `id="availability-overview-grid"`, `data-wg-item="31"`, `data-wg-item="41"`, "Otto Other", `data-testid="availability-legend"`, `href="/en/groups/3/coaches/8/availability?part=11&amp;week=all"`, "badge-secondary"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: overview missing %q", test.name, want)
			}
		}
		if strings.Contains(body, "data-wg-draggable") || strings.Contains(body, "data-move-url") {
			t.Errorf("%s: overview must be read only", test.name)
		}
	}
}

func TestAvailabilityWithoutSeasons(t *testing.T) {
	dependencies, coaches, _ := coachDependencies(t, groupMemberAs(service.GroupRoleCoach))
	coaches.parts = service.AvailabilityParts{}
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/coaches/7/availability", "", false)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `data-testid="availability-no-parts"`) || strings.Contains(body, `id="availability-grid"`) {
		t.Fatalf("no seasons = %d", response.Code)
	}
	page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/coaches", "", false).Body.String()
	if !strings.Contains(page, `data-testid="coaches-ages-none"`) {
		t.Error("coaches page without seasons must explain the missing ages")
	}
}
