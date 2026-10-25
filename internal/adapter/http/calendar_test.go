package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

// calendarEventsStub answers List like EventService: events overlapping
// [From, To) that the account sees (GroupAccess.CanViewEvent), with the
// team filter of the query.
type calendarEventsStub struct {
	*eventsUseCaseStub
	all []service.Event
}

func (s *calendarEventsStub) List(_ context.Context, access *service.GroupAccess, query service.EventQuery) ([]service.Event, error) {
	s.calls++
	s.lastQuery = query
	visible := make([]service.Event, 0)
	for _, event := range s.all {
		if !event.EndsAt.After(query.From) || !event.StartsAt.Before(query.To) || !access.CanViewEvent(event) {
			continue
		}
		if query.TeamID > 0 && !slices.Contains(event.TeamIDs(), query.TeamID) {
			continue
		}
		visible = append(visible, event)
	}
	return visible, nil
}

// visibleTeamsStub answers Teams.List like TeamService: only visible teams.
type visibleTeamsStub struct {
	*teamsUseCaseStub
}

func (s visibleTeamsStub) List(_ context.Context, access *service.GroupAccess) ([]service.TeamSummary, error) {
	visible := make([]service.TeamSummary, 0)
	for _, summary := range s.summaries {
		if access.CanViewTeam(summary.Team.ID) {
			visible = append(visible, summary)
		}
	}
	return visible, nil
}

func calendarEvent(id int64, kind, starts, ends string, teamIDs ...int64) service.Event {
	teams := make([]service.EventTeam, 0, len(teamIDs))
	for _, teamID := range teamIDs {
		teams = append(teams, service.EventTeam{ID: teamID, Name: calendarTeamNames[teamID]})
	}
	location := "City park"
	return service.Event{
		ID: id, GroupID: 3, Kind: kind, Title: "Event " + strconv.FormatInt(id, 10),
		StartsAt: budapestTime(starts), EndsAt: budapestTime(ends), Location: &location,
		SignupMode: service.EventSignupOpen, Teams: teams, Version: 1,
	}
}

var calendarTeamNames = map[int64]string{1: "Lions", 2: "Tigers", 3: "Bears", 4: "Wolves"}

func onResource(event service.Event, id int64, name string) service.Event {
	event.ResourceID, event.ResourceName, event.Location = &id, &name, nil
	return event
}

// calendarFixture is the week of Monday 2026-10-19 in Europe/Budapest, whose
// Sunday is the night the clocks go back (03:00 summer time is 02:00 winter
// time).
func calendarFixture() []service.Event {
	cancelledAt := int64(1)
	extra := calendarEvent(3, service.EventKindChampionship, "2026-10-24T09:00", "2026-10-24T16:00", 3)
	extra.Coaches = []service.EventCoach{{UserID: 8}}
	extra = onResource(extra, 32, "Hall")
	cancelled := calendarEvent(4, service.EventKindOther, "2026-10-22T18:00", "2026-10-22T19:00", 4)
	cancelled.CancelledAt = &cancelledAt
	location, _ := time.LoadLocation("Europe/Budapest")
	// 01:30 summer time to 03:00 winter time: two and a half hours.
	dst := calendarEvent(5, service.EventKindTraining, "2026-10-25T01:30", "2026-10-25T01:30", 1, 2)
	dst.EndsAt = time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC).In(location)
	return []service.Event{
		onResource(calendarEvent(1, service.EventKindTraining, "2026-10-19T17:00", "2026-10-19T18:30", 1), 31, "Court 1"),
		calendarEvent(2, service.EventKindMatch, "2026-10-21T18:00", "2026-10-21T19:30", 2),
		extra,
		cancelled,
		dst,
		calendarEvent(6, service.EventKindTraining, "2026-10-23T22:00", "2026-10-24T02:00", 2),
		// Next week and the week before: never shown for this week.
		calendarEvent(7, service.EventKindTraining, "2026-10-26T00:00", "2026-10-26T01:00", 1),
		calendarEvent(8, service.EventKindTraining, "2026-10-18T22:00", "2026-10-19T00:00", 1),
	}
}

func calendarDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *calendarEventsStub) {
	t.Helper()
	dependencies, _, teams := groupTestDependencies(t, access)
	for _, id := range []int64{1, 2, 3, 4} {
		teams.summaries = append(teams.summaries, service.TeamSummary{Team: service.Team{ID: id, GroupID: 3, Name: calendarTeamNames[id]}})
	}
	dependencies.Teams = visibleTeamsStub{teams}
	dependencies.Resources = &resourcesUseCaseStub{list: []service.Resource{
		{ID: 31, GroupID: 3, Name: "Court 1", Kind: service.ResourceKindCourt},
		{ID: 32, GroupID: 3, Name: "Hall", Kind: service.ResourceKindHall},
		{ID: 33, GroupID: 3, Name: "Pool", Kind: service.ResourceKindPool},
	}}
	events := &calendarEventsStub{eventsUseCaseStub: &eventsUseCaseStub{}, all: calendarFixture()}
	dependencies.Events = events
	return dependencies, events
}

var calendarEventIDPattern = regexp.MustCompile(`data-wg-item="event-(\d+)-\d"`)

// shownEventIDs returns the events on the grid, and checks that the day
// list shows the same ones.
func shownEventIDs(t *testing.T, body string) []int64 {
	t.Helper()
	collect := func(pattern *regexp.Regexp) []int64 {
		ids := make([]int64, 0)
		for _, match := range pattern.FindAllStringSubmatch(body, -1) {
			id, _ := strconv.ParseInt(match[1], 10, 64)
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return slices.Compact(ids)
	}
	grid := collect(calendarEventIDPattern)
	list := collect(regexp.MustCompile(`<li class="list-row px-0" data-event-id="(\d+)"`))
	if !slices.Equal(grid, list) {
		t.Errorf("grid shows %v, day list %v", grid, list)
	}
	return grid
}

func calendarCoach() *service.GroupAccess {
	// User 8 coaches Lions this season and is an extra coach of event 3.
	return service.NewGroupAccess(3, 8, service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{1}})
}

func calendarParent() *service.GroupAccess {
	// On Wolves as a parent; their child plays for Bears.
	return groupAccessAs(service.GroupAccessGrants{
		Member: true, Roles: []string{service.GroupRoleParent},
		OwnTeams:     []service.TeamRole{{TeamID: 4, Role: service.GroupRoleParent}},
		ChildTeamIDs: []int64{3}, ChildUserIDs: []int64{20},
	})
}

func TestCalendarShowsEachRoleItsEvents(t *testing.T) {
	tests := []struct {
		name      string
		access    *service.GroupAccess
		want      []int64
		teams     []string
		resources []string
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), want: []int64{1, 2, 3, 4, 5, 6}, teams: []string{"1", "2", "3", "4"}, resources: []string{"31", "32", "33"}},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), want: []int64{1, 2, 3, 4, 5, 6}, teams: []string{"1", "2", "3", "4"}, resources: []string{"31", "32", "33"}},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), want: []int64{1, 2, 3, 4, 5, 6}, teams: []string{"1", "2", "3", "4"}, resources: []string{"31", "32", "33"}},
		{name: "coach of a team and extra coach", access: calendarCoach(), want: []int64{1, 3, 5}, teams: []string{"1"}, resources: []string{"31", "32", "33"}},
		{name: "player", access: playerOfTeam(2), want: []int64{2, 5, 6}, teams: []string{"2"}},
		{name: "parent with a child in another team", access: calendarParent(), want: []int64{3, 4}, teams: []string{"3", "4"}, resources: []string{"32"}},
		{name: "member without teams", access: groupMemberAs(service.GroupRolePlayer), want: []int64{}},
		{name: "member without roles", access: groupMemberAs(), want: []int64{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _ := calendarDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-21", "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			if got := shownEventIDs(t, body); !slices.Equal(got, test.want) {
				t.Errorf("shown = %v, want %v", got, test.want)
			}
			if got := optionValues(body, "calendar-filter-team"); !slices.Equal(got, test.teams) {
				t.Errorf("team options = %v, want %v", got, test.teams)
			}
			if got := optionValues(body, "calendar-filter-resource"); !slices.Equal(got, test.resources) {
				t.Errorf("resource options = %v, want %v", got, test.resources)
			}
			if len(test.want) == 0 && !strings.Contains(body, `data-testid="calendar-empty"`) {
				t.Error("empty week notice missing")
			}
		})
	}
}

// optionValues returns the non-empty option values of the select with id.
func optionValues(body, id string) []string {
	start := strings.Index(body, `id="`+id+`"`)
	if start < 0 {
		return nil
	}
	rest := body[start:]
	rest = rest[:strings.Index(rest, "</select>")]
	values := make([]string, 0)
	for _, match := range regexp.MustCompile(`<option value="([^"]*)"`).FindAllStringSubmatch(rest, -1) {
		if match[1] != "" {
			values = append(values, match[1])
		}
	}
	return values
}

func TestCalendarFiltersNeverWiden(t *testing.T) {
	tests := []struct {
		name   string
		access *service.GroupAccess
		query  string
		want   []int64
		kept   string
	}{
		{name: "admin by team", access: groupMemberAs(service.GroupRoleAdmin), query: "team=1", want: []int64{1, 5}, kept: "team=1"},
		{name: "admin by resource", access: groupMemberAs(service.GroupRoleAdmin), query: "resource=31", want: []int64{1}, kept: "resource=31"},
		{name: "admin by kind", access: groupMemberAs(service.GroupRoleAdmin), query: "kind=training", want: []int64{1, 5, 6}, kept: "kind=training"},
		{name: "admin by team and kind", access: groupMemberAs(service.GroupRoleAdmin), query: "team=2&kind=match", want: []int64{2}, kept: "kind=match&team=2"},
		{name: "coach by own team", access: calendarCoach(), query: "team=1", want: []int64{1, 5}, kept: "team=1"},
		{name: "coach by a team they do not see", access: calendarCoach(), query: "team=3", want: []int64{1, 3, 5}},
		{name: "coach by an unused resource", access: calendarCoach(), query: "resource=33", want: []int64{}, kept: "resource=33"},
		{name: "player by another team", access: playerOfTeam(2), query: "team=1", want: []int64{2, 5, 6}},
		{name: "player by a resource not on their events", access: playerOfTeam(2), query: "resource=31", want: []int64{2, 5, 6}},
		{name: "player by kind", access: playerOfTeam(2), query: "kind=championship", want: []int64{}, kept: "kind=championship"},
		{name: "parent by child's team", access: calendarParent(), query: "team=3", want: []int64{3}, kept: "team=3"},
		{name: "parent by resource of child's event", access: calendarParent(), query: "resource=32", want: []int64{3}, kept: "resource=32"},
		{name: "unknown kind and bad IDs", access: playerOfTeam(2), query: "kind=party&team=x&resource=-1", want: []int64{2, 5, 6}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _ := calendarDependencies(t, test.access)
			body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-19&"+test.query, "", false).Body.String()
			if got := shownEventIDs(t, body); !slices.Equal(got, test.want) {
				t.Errorf("shown = %v, want %v", got, test.want)
			}
			next := `href="/en/groups/3/calendar?` + strings.TrimPrefix(strings.ReplaceAll("&"+test.kept+"&week=2026-10-26", "&&", "&"), "&") + `"`
			if test.kept != "" && !strings.Contains(body, strings.ReplaceAll(next, "&", "&amp;")) {
				t.Errorf("next week link does not keep the filter: want %s", next)
			}
			if test.kept == "" && strings.Contains(body, `data-testid="calendar-clear"`) {
				t.Error("a dropped filter still shows as applied")
			}
		})
	}
}

func TestCalendarWeekInGroupTimeZone(t *testing.T) {
	dependencies, events := calendarDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-25", "", false).Body.String()
	// Any day of the week selects its Monday; the week runs from local
	// midnight on Monday (summer time) to local midnight on the next Monday
	// (winter time), 169 hours.
	if from, to := events.lastQuery.From.UTC(), events.lastQuery.To.UTC(); !from.Equal(time.Date(2026, 10, 18, 22, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("week = %s .. %s", from, to)
	}
	for _, want := range []string{
		`data-week="2026-10-19"`,
		// 01:30 on the night the clocks go back, to 03:00 winter time.
		`data-wg-item="event-5-7" data-weekday="7" data-start="90" data-end="180"`,
		// Across midnight: split into Friday and Saturday.
		`data-wg-item="event-6-5" data-weekday="5" data-start="1320" data-end="1440"`,
		`data-wg-item="event-6-6" data-weekday="6" data-start="0" data-end="120"`,
		`href="/en/groups/3/calendar?week=2026-10-12"`,
		`href="/en/groups/3/calendar?week=2026-10-26"`,
		"Oct 19 – Oct 25, 2026",
		"Spans several days: 2026-10-23 22:00 – 2026-10-24 02:00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("calendar missing %q", want)
		}
	}
	if strings.Contains(body, `data-wg-item="event-7-`) || strings.Contains(body, `data-wg-item="event-8-`) {
		t.Error("events of the neighboring weeks are shown")
	}

	// The spring change: the week of 2026-03-29 is 167 hours.
	serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-03-29", "", false)
	if from, to := events.lastQuery.From.UTC(), events.lastQuery.To.UTC(); !from.Equal(time.Date(2026, 3, 22, 23, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("spring week = %s .. %s", from, to)
	}

	// No week or a bad one is the current week.
	for _, target := range []string{"/en/groups/3/calendar", "/en/groups/3/calendar?week=yesterday", "/en/groups/3/calendar?week=1900-01-01"} {
		body := serveGroups(t, dependencies, http.MethodGet, target, "", false).Body.String()
		today, _ := service.LocalDate(time.Now(), "Europe/Budapest")
		week, _ := service.NewCalendarWeek(today, "Europe/Budapest")
		if !strings.Contains(body, `data-week="`+service.FormatSeasonDate(week.Monday)+`"`) || !strings.Contains(body, `data-calendar-today`) {
			t.Errorf("%s: not the current week", target)
		}
	}
}

func TestCalendarMarksCancelledEventsAndLinksDetails(t *testing.T) {
	dependencies, _ := calendarDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-19", "", false).Body.String()
	for _, want := range []string{
		`class="wg-item wg-item-secondary wg-row-`, // other
		`<a class="wg-item-label wg-item-link" href="/en/groups/3/events/1" data-wg-link>Event 1</a>`,
		`<span class="wg-item-detail">Lions · Court 1</span>`,
		`<span class="badge badge-xs">Cancelled</span>`,
		`data-event-id="4" data-event-cancelled="true"`,
		`<a class="link font-semibold line-through" href="/en/groups/3/events/4">Event 4</a>`,
		`hx-boost="true" hx-target="#page-content"`,
		`data-section="calendar" aria-current="page"`,
		`data-testid="calendar-today"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("calendar missing %q", want)
		}
	}
	if !regexp.MustCompile(`wg-item-muted"\s+data-wg-item="event-4-4"`).MatchString(body) || regexp.MustCompile(`wg-item-muted"\s+data-wg-item="event-1-1"`).MatchString(body) {
		t.Error("only the cancelled event is muted")
	}
	if strings.Contains(body, "data-calendar-status") {
		t.Error("a sign-up status badge is shown before sign-up exists")
	}
}

func TestCalendarFragmentAndLanguages(t *testing.T) {
	dependencies, _ := calendarDependencies(t, playerOfTeam(2))
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-19&team=2", "", true)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "<html") || !strings.Contains(body, `id="page-content"`) || !strings.Contains(body, `id="alerts"`) || !strings.Contains(body, `id="calendar-heading"`) {
		t.Fatalf("fragment status = %d", response.Code)
	}
	if got := shownEventIDs(t, body); !slices.Equal(got, []int64{2, 5, 6}) {
		t.Errorf("fragment shows %v", got)
	}
	hungarian := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/3/calendar?week=2026-10-19", "", false).Body.String()
	for _, want := range []string{"Heti naptár", "Minden csapat", `href="/hu/groups/3/events/2"`, "Mérkőzés"} {
		if !strings.Contains(hungarian, want) {
			t.Errorf("Hungarian calendar missing %q", want)
		}
	}
}

func TestCalendarGroupSwitcherKeepsTheWeek(t *testing.T) {
	dependencies, _ := calendarDependencies(t, playerOfTeam(2))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-21&team=2&kind=match", "", false).Body.String()
	if strings.Contains(body, `data-testid="calendar-groups"`) {
		t.Fatal("switcher shown for a single group")
	}
	groups := dependencies.Groups.(*groupsUseCaseStub)
	groups.summaries = []service.GroupSummary{{Group: service.Group{ID: 3, Name: "Lions Club"}}, {Group: service.Group{ID: 9, Name: "Swim Club"}}}
	body = serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-21&team=2&kind=match", "", false).Body.String()
	for _, want := range []string{
		`<a href="/en/groups/9/calendar?kind=match&amp;week=2026-10-19" data-calendar-group>Swim Club</a>`,
		`<a href="/en/groups/3/calendar?kind=match&amp;week=2026-10-19" class="menu-active" aria-current="page" data-calendar-group>Lions Club</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("switcher missing %q", want)
		}
	}
}

func TestCalendarOutsidersAndFailures(t *testing.T) {
	dependencies, events := calendarDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar", "", false); response.Code != http.StatusNotFound || events.calls != 0 {
		t.Fatalf("non-member status = %d, calls = %d", response.Code, events.calls)
	}
	dependencies, _ = calendarDependencies(t, groupAccessAs(service.GroupAccessGrants{}))
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("access without membership status = %d", response.Code)
	}
	dependencies, _ = calendarDependencies(t, groupMemberAs())
	dependencies.Events = nil
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar", "", false); response.Code != http.StatusInternalServerError {
		t.Fatalf("missing events status = %d", response.Code)
	}
	handler, _ := newGroupsTestHandler(t, settingsStubDependencies(t))
	mux := http.NewServeMux()
	registerGroupRoutes(mux, handler)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/groups/3/calendar", nil))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("anonymous status = %d", response.Code)
	}
}

func TestCalendarNavigationForEveryMember(t *testing.T) {
	for _, access := range []*service.GroupAccess{groupMemberAs(), playerOfTeam(4), calendarParent(), calendarCoach(), groupMemberAs(service.GroupRoleAdmin)} {
		dependencies, _ := calendarDependencies(t, access)
		if body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false).Body.String(); !strings.Contains(body, `href="/en/groups/3/calendar" data-section="calendar"`) {
			t.Errorf("roles %v: calendar tab missing", access.Roles())
		}
	}
}

func TestCalendarGridConfigCoversEveryEvent(t *testing.T) {
	week, err := service.NewCalendarWeek(time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), "Europe/Budapest")
	if err != nil {
		t.Fatal(err)
	}
	config := calendarGridConfig(week, nil, "", "")
	if config.StartMinute != 8*60 || config.EndMinute != 20*60 {
		t.Fatalf("default range = %d-%d", config.StartMinute, config.EndMinute)
	}
	config = calendarGridConfig(week, []WeekGridItem{{StartMinute: 6*60 + 45, EndMinute: 7 * 60}, {StartMinute: 21 * 60, EndMinute: 23*60 + 10}}, "", "")
	if config.StartMinute != 6*60 || config.EndMinute != 24*60 {
		t.Fatalf("widened range = %d-%d", config.StartMinute, config.EndMinute)
	}
	if calendarVariant(service.EventKindMatch) != WeekGridInfo || calendarVariant("x") != WeekGridSecondary {
		t.Fatal("variants")
	}
}

// calendarAnswersStub answers CalendarAnswers like ParticipationService:
// only the account's own rows and its children's, whatever is stored.
type calendarAnswersStub struct {
	*participationUseCaseStub
	stored []service.CalendarAnswer
	err    error
	ranges [][2]time.Time
}

func (s *calendarAnswersStub) CalendarAnswers(_ context.Context, access *service.GroupAccess, from, to time.Time) ([]service.CalendarAnswer, error) {
	s.calls++
	s.ranges = append(s.ranges, [2]time.Time{from, to})
	if s.err != nil {
		return nil, s.err
	}
	var own []service.CalendarAnswer
	for _, answer := range s.stored {
		if answer.UserID == access.UserID() || slices.Contains(access.ChildUserIDs(), answer.UserID) {
			answer.Self = answer.UserID == access.UserID()
			own = append(own, answer)
		}
	}
	return own, nil
}

func calendarAnswerFixture() []service.CalendarAnswer {
	anna := "Anna"
	return []service.CalendarAnswer{
		{EventID: 2, UserID: 7, Status: service.ParticipantSignedUp},
		{EventID: 6, UserID: 7, Status: service.ParticipantDeclined},
		{EventID: 3, UserID: 20, Status: service.ParticipantNominated, DisplayName: &anna},
		{EventID: 2, UserID: 99, Status: service.ParticipantDeclined},
		// Event 1 is not visible to the player.
		{EventID: 1, UserID: 7, Status: service.ParticipantSignedUp},
	}
}

func TestCalendarShowsOwnAndChildAnswers(t *testing.T) {
	statuses := regexp.MustCompile(`data-calendar-status="(\w+)"`)
	t.Run("player sees own answers only", func(t *testing.T) {
		dependencies, _ := calendarDependencies(t, playerOfTeam(2))
		answers := &calendarAnswersStub{participationUseCaseStub: &participationUseCaseStub{}, stored: calendarAnswerFixture()}
		dependencies.Participation = answers
		body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-19", "", false).Body.String()
		if answers.calls != 1 {
			t.Errorf("answer queries = %d, want 1", answers.calls)
		}
		var got []string
		for _, match := range statuses.FindAllStringSubmatch(body, -1) {
			got = append(got, match[1])
		}
		got = slices.Compact(slices.Sorted(slices.Values(got)))
		if !slices.Equal(got, []string{"declined", "signed_up"}) {
			t.Errorf("badges = %v, want the player's two answers", got)
		}
		for _, want := range []string{">Coming</span>", ">Not coming</span>", "Your answer:"} {
			if !strings.Contains(body, want) {
				t.Errorf("calendar missing %q", want)
			}
		}
		if strings.Contains(body, "Anna") || strings.Contains(body, "data-calendar-child") {
			t.Error("another member's answer is shown")
		}
	})
	t.Run("parent sees the child's answer with the name", func(t *testing.T) {
		dependencies, _ := calendarDependencies(t, calendarParent())
		answers := &calendarAnswersStub{participationUseCaseStub: &participationUseCaseStub{}, stored: calendarAnswerFixture()}
		dependencies.Participation = answers
		body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-19", "", false).Body.String()
		if !strings.Contains(body, `data-calendar-status="nominated" data-calendar-child="true">Anna: Nominated</span>`) {
			t.Errorf("child badge missing in %d bytes", len(body))
		}
		if len(statuses.FindAllString(body, -1)) != 1 {
			t.Errorf("badges = %d, want the child answer once", len(statuses.FindAllString(body, -1)))
		}
	})
	t.Run("hungarian labels", func(t *testing.T) {
		dependencies, _ := calendarDependencies(t, calendarParent())
		dependencies.Participation = &calendarAnswersStub{participationUseCaseStub: &participationUseCaseStub{}, stored: calendarAnswerFixture()}
		body := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/3/calendar?week=2026-10-19", "", false).Body.String()
		if !strings.Contains(body, "Anna: ") || strings.Contains(body, "Anna: Nominated") {
			t.Error("child badge is not localized")
		}
	})
	t.Run("a failing query is an error page", func(t *testing.T) {
		dependencies, _ := calendarDependencies(t, playerOfTeam(2))
		dependencies.Participation = &calendarAnswersStub{participationUseCaseStub: &participationUseCaseStub{}, err: errors.New("down")}
		if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar?week=2026-10-19", "", false); response.Code != http.StatusInternalServerError {
			t.Errorf("status = %d", response.Code)
		}
	})
	t.Run("a non-member still gets 404 without a query", func(t *testing.T) {
		dependencies, _ := calendarDependencies(t, groupAccessAs(service.GroupAccessGrants{}))
		answers := &calendarAnswersStub{participationUseCaseStub: &participationUseCaseStub{}, stored: calendarAnswerFixture()}
		dependencies.Participation = answers
		if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/calendar", "", false); response.Code != http.StatusNotFound || answers.calls != 0 {
			t.Errorf("status = %d, queries = %d", response.Code, answers.calls)
		}
	})
}
