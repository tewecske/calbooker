package httpadapter

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

type eventsUseCaseStub struct {
	list        []service.Event
	detail      service.EventDetail
	detailErr   error
	options     service.EventFormOptions
	optionsErr  error
	writeErr    error
	calls       int
	writes      int
	lastQuery   service.EventQuery
	lastInput   service.EventInput
	lastID      int64
	lastVersion int64
	lastCancel  bool
}

func (s *eventsUseCaseStub) List(_ context.Context, _ *service.GroupAccess, query service.EventQuery) ([]service.Event, error) {
	s.calls++
	s.lastQuery = query
	return s.list, nil
}

func (s *eventsUseCaseStub) Detail(_ context.Context, access *service.GroupAccess, eventID int64) (service.EventDetail, error) {
	s.calls++
	s.lastID = eventID
	if s.detailErr != nil {
		return service.EventDetail{}, s.detailErr
	}
	detail := s.detail
	detail.CanEdit = access.CanEditEvent(detail.Event)
	if !detail.CanEdit {
		detail.Warnings = nil
	}
	return detail, nil
}

func (s *eventsUseCaseStub) FormOptions(context.Context, *service.GroupAccess, *service.Event) (service.EventFormOptions, error) {
	s.calls++
	return s.options, s.optionsErr
}

func (s *eventsUseCaseStub) write() error {
	s.calls++
	s.writes++
	return s.writeErr
}

func (s *eventsUseCaseStub) Create(_ context.Context, _ service.GroupActor, input service.EventInput) (service.Event, []service.EventWarning, error) {
	s.lastInput = input
	if err := s.write(); err != nil {
		return service.Event{}, nil, err
	}
	return service.Event{ID: 99, GroupID: 3}, nil, nil
}

func (s *eventsUseCaseStub) Update(_ context.Context, _ service.GroupActor, eventID, expectedVersion int64, input service.EventInput) (service.Event, []service.EventWarning, error) {
	s.lastInput, s.lastID, s.lastVersion = input, eventID, expectedVersion
	if err := s.write(); err != nil {
		return service.Event{}, nil, err
	}
	return s.detail.Event, nil, nil
}

func (s *eventsUseCaseStub) SetCancelled(_ context.Context, _ service.GroupActor, eventID, expectedVersion int64, cancelled bool) (service.Event, error) {
	s.lastID, s.lastVersion, s.lastCancel = eventID, expectedVersion, cancelled
	return s.detail.Event, s.write()
}

func (s *eventsUseCaseStub) Delete(_ context.Context, _ service.GroupActor, eventID, expectedVersion int64) error {
	s.lastID, s.lastVersion = eventID, expectedVersion
	return s.write()
}

func budapestTime(raw string) time.Time {
	location, _ := time.LoadLocation("Europe/Budapest")
	parsed, err := time.ParseInLocation(service.EventTimeLayout, raw, location)
	if err != nil {
		panic(err)
	}
	return parsed
}

func eventFixture() service.Event {
	location := "City stadium"
	limit := 18
	deadline := budapestTime("2026-11-06T20:00")
	carla := "Carla Coach"
	return service.Event{
		ID: 5, GroupID: 3, Kind: service.EventKindChampionship, Title: "Autumn cup",
		StartsAt: budapestTime("2026-11-07T09:00"), EndsAt: budapestTime("2026-11-07T16:00"),
		Location: &location, SignupMode: service.EventSignupNominated, MaxParticipants: &limit, SignupDeadline: &deadline,
		Teams: []service.EventTeam{{ID: 4, Name: "Lions"}}, Coaches: []service.EventCoach{{UserID: 9, DisplayName: &carla}}, Version: 2,
	}
}

func eventDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *eventsUseCaseStub) {
	t.Helper()
	dependencies, _, teams := groupTestDependencies(t, access)
	teams.summaries = []service.TeamSummary{{Team: service.Team{ID: 4, GroupID: 3, Name: "Lions"}}}
	event := eventFixture()
	head := "Hanna Head"
	court := service.Resource{ID: 31, GroupID: 3, Name: "Court 1", Kind: service.ResourceKindCourt}
	events := &eventsUseCaseStub{
		list: []service.Event{event},
		detail: service.EventDetail{
			Event: event, TimeZone: "Europe/Budapest",
			TeamCoaches: []service.EventCoach{{UserID: 8, TeamID: 4, DisplayName: &head}},
			Warnings: []service.EventWarning{
				{Kind: service.EventWarningResourceCapacity, ResourceName: "Court 1", Limit: 2, Teams: 3},
				{Kind: service.EventWarningCoachBusy, Coach: service.EventCoach{UserID: 8, DisplayName: &head}, OtherEventID: 6, OtherTitle: "Tigers match", OtherStartsAt: budapestTime("2026-11-07T10:00")},
			},
		},
		options: service.EventFormOptions{
			TimeZone:  "Europe/Budapest",
			Teams:     []service.Team{{ID: 4, GroupID: 3, Name: "Lions"}, {ID: 5, GroupID: 3, Name: "Tigers"}},
			Resources: []service.Resource{court},
			Coaches:   []service.GroupMember{{UserID: 9, Roles: []string{service.GroupRoleCoach}, DisplayName: eventStr("Carla Coach")}},
		},
	}
	dependencies.Events = events
	return dependencies, events
}

func eventStr(value string) *string { return &value }

func coachOfTeam(teamIDs ...int64) *service.GroupAccess {
	return groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: teamIDs})
}

func playerOfTeam(teamID int64) *service.GroupAccess {
	return groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRolePlayer}, OwnTeams: []service.TeamRole{{TeamID: teamID, Role: service.GroupRolePlayer}}})
}

func TestEventsListByRole(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantCreate bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantCreate: true},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), wantCreate: true},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantCreate: true},
		{name: "coach of the team", access: coachOfTeam(4), wantCreate: true},
		{name: "coach without a team", access: groupMemberAs(service.GroupRoleCoach)},
		{name: "player", access: playerOfTeam(4)},
		{name: "parent", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, ChildTeamIDs: []int64{4}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, events := eventDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events?team=4", "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			for _, want := range []string{`id="group-events-heading"`, `data-event-id="5"`, "Autumn cup", "2026-11-07 09:00 – 16:00", "Championship", "City stadium", "Lions", `data-section="events" aria-current="page"`, `href="/en/groups/3/events/5"`, "Europe/Budapest", `<option value="4" selected>Lions</option>`} {
				if !strings.Contains(body, want) {
					t.Errorf("events page missing %q", want)
				}
			}
			if strings.Contains(body, `id="event-new-title"`) != test.wantCreate {
				t.Errorf("create form shown = %v, want %v", !test.wantCreate, test.wantCreate)
			}
			if events.lastQuery.TeamID != 4 || events.lastQuery.From.IsZero() || events.lastQuery.Descending {
				t.Errorf("query = %+v", events.lastQuery)
			}
		})
	}
	dependencies, events := eventDependencies(t, playerOfTeam(4))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events?when=past", "", false).Body.String()
	if !strings.Contains(body, "Past events") || events.lastQuery.To.IsZero() || !events.lastQuery.Descending || events.lastQuery.Limit != pastEventsLimit {
		t.Fatalf("past list query = %+v", events.lastQuery)
	}
	events.list = nil
	if body := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/3/events", "", false).Body.String(); !strings.Contains(body, `data-testid="events-empty"`) || !strings.Contains(body, "Közelgő események") {
		t.Fatal("empty Hungarian list missing its notice")
	}
}

func TestEventsNavigationForEveryMember(t *testing.T) {
	for _, access := range []*service.GroupAccess{groupMemberAs(), playerOfTeam(4), groupMemberAs(service.GroupRoleParent), groupMemberAs(service.GroupRoleAdmin)} {
		dependencies, _ := eventDependencies(t, access)
		if body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false).Body.String(); !strings.Contains(body, `data-section="events"`) {
			t.Errorf("roles %v: events tab missing", access.Roles())
		}
	}
}

func TestEventRoutesOutsiderGetsNotFound(t *testing.T) {
	dependencies, events := eventDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, test := range []struct{ method, target string }{
		{http.MethodGet, "/en/groups/3/events"},
		{http.MethodPost, "/en/groups/3/events"},
		{http.MethodGet, "/en/groups/3/events/5"},
		{http.MethodPost, "/en/groups/3/events/5"},
		{http.MethodPost, "/en/groups/3/events/5/cancel"},
		{http.MethodPost, "/en/groups/3/events/5/delete"},
		{http.MethodGet, "/hu/groups/3/events/5"},
	} {
		response := serveGroups(t, dependencies, test.method, test.target, "version=1&title=X&team_id=4", false)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
			t.Errorf("%s %s = %d, want 404 without the group name", test.method, test.target, response.Code)
		}
	}
	if events.calls != 0 {
		t.Fatal("outsider requests reached the use cases")
	}
}

func TestEventDetailByRole(t *testing.T) {
	tests := []struct {
		name     string
		access   *service.GroupAccess
		wantEdit bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantEdit: true},
		{name: "coach of the team", access: coachOfTeam(4), wantEdit: true},
		{name: "player of the team", access: playerOfTeam(4)},
		{name: "parent of a child on the team", access: groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, ChildTeamIDs: []int64{4}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _ := eventDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/5", "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			for _, want := range []string{`id="event-heading"`, "Autumn cup", "2026-11-07 09:00 – 16:00", "City stadium", "Coaches nominate", "18", "2026-11-06 20:00", "Hanna Head", "Carla Coach", `data-section="events" aria-current="page"`} {
				if !strings.Contains(body, want) {
					t.Errorf("event page missing %q", want)
				}
			}
			if strings.Contains(body, `id="event-edit-title"`) != test.wantEdit {
				t.Errorf("edit form shown = %v, want %v", !test.wantEdit, test.wantEdit)
			}
			warnings := strings.Contains(body, `data-testid="event-warnings"`)
			if warnings != test.wantEdit {
				t.Errorf("warnings shown = %v, want %v", warnings, test.wantEdit)
			}
			if test.wantEdit {
				for _, want := range []string{"Court 1 allows 2 teams at a time, but 3 teams use it during this event.", "Hanna Head is also a coach of Tigers match (2026-11-07 10:00)."} {
					if !strings.Contains(body, want) {
						t.Errorf("warning missing %q", want)
					}
				}
				if !strings.Contains(body, `value="2026-11-07T09:00"`) || !strings.Contains(body, `id="event-edit-team-4" class="checkbox" type="checkbox" name="team_id" value="4" checked`) || !strings.Contains(body, `id="event-edit-coach-9" class="checkbox" type="checkbox" name="coach_user_id" value="9" checked`) {
					t.Error("edit form not prefilled from the event")
				}
			}
		})
	}
	dependencies, events := eventDependencies(t, playerOfTeam(7))
	events.detailErr = service.ErrEventNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/5", "", false); response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Autumn cup") {
		t.Fatalf("hidden event = %d, want 404", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/abc", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("bad event id = %d, want 404", response.Code)
	}
}

const eventFormBody = "kind=championship&title=Autumn+cup&starts_at=2026-11-07T09%3A00&ends_at=2026-11-07T16%3A00" +
	"&team_id=4&team_id=5&resource_id=&location=City+stadium&signup_mode=nominated&max_participants=18" +
	"&signup_deadline=2026-11-06T20%3A00&coach_user_id=9"

func TestEventCreate(t *testing.T) {
	dependencies, events := eventDependencies(t, coachOfTeam(4, 5))
	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events", eventFormBody, false)
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/en/groups/3/events/99" {
		t.Fatalf("create = %d %q, want 303 to the new event", created.Code, created.Header().Get("Location"))
	}
	input := events.lastInput
	if input.Kind != "championship" || input.Title != "Autumn cup" || service.FormatEventLocalTime(input.StartsAt) != "2026-11-07T09:00" ||
		service.FormatEventLocalTime(input.EndsAt) != "2026-11-07T16:00" || !slices.Equal(input.TeamIDs, []int64{4, 5}) || input.ResourceID != 0 ||
		input.Location != "City stadium" || input.SignupMode != "nominated" || input.MaxParticipants != 18 ||
		service.FormatEventLocalTime(input.SignupDeadline) != "2026-11-06T20:00" || !slices.Equal(input.CoachUserIDs, []int64{9}) {
		t.Fatalf("input = %+v", input)
	}
	if htmx := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events", eventFormBody, true); htmx.Code != http.StatusOK || htmx.Header().Get("HX-Redirect") != "/en/groups/3/events/99" {
		t.Fatalf("HTMX create = %d %q", htmx.Code, htmx.Header().Get("HX-Redirect"))
	}
	// Garbage numbers and times are passed on as invalid, not as empty.
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events", "resource_id=x&max_participants=many&signup_deadline=soon&team_id=abc&starts_at=later", true)
	if bad := events.lastInput; bad.ResourceID != -1 || bad.MaxParticipants != -1 || bad.SignupDeadline.IsZero() || !slices.Equal(bad.TeamIDs, []int64{-1}) || !bad.StartsAt.IsZero() {
		t.Fatalf("garbage input = %+v", bad)
	}

	for _, test := range []struct {
		err   error
		text  string
		field string
	}{
		{service.ErrInvalidEventTitle, "up to 128 characters", `id="event-new-title-error"`},
		{service.ErrInvalidEventEnd, "end after it starts", `id="event-new-ends-at-error"`},
		{&service.EventFieldError{Field: "starts_at", Err: service.ErrEventTimeSkipped}, "clocks move forward", `id="event-new-starts-at-error"`},
		{&service.EventFieldError{Field: "signup_deadline", Err: service.ErrEventTimeSkipped}, "clocks move forward", `id="event-new-signup-deadline-error"`},
		{service.ErrEventTeamsRequired, "at least one team", `id="event-new-teams-error"`},
		{service.ErrEventPlaceRequired, "not both", `id="event-new-place-error"`},
		{service.ErrEventResourceArchived, "archived", `id="event-new-place-error"`},
		{service.ErrInvalidEventMaxParticipants, "from 1 to 999", `id="event-new-max-participants-error"`},
		{service.ErrInvalidEventSignupDeadline, "before the event starts", `id="event-new-signup-deadline-error"`},
		{service.ErrEventCoachRoleMissing, "coach role", `id="event-new-coaches-error"`},
	} {
		events.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events", eventFormBody, true)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, test.text) || !strings.Contains(body, test.field) {
			t.Errorf("error %v = %d, want 422 with %q on %s", test.err, response.Code, test.text, test.field)
		}
		if !strings.Contains(body, `value="Autumn cup"`) || !strings.Contains(body, `value="2026-11-06T20:00"`) || !strings.Contains(body, `name="team_id" value="5" checked`) || !strings.Contains(body, `name="coach_user_id" value="9" checked`) {
			t.Errorf("error %v: submitted values not kept", test.err)
		}
	}
}

func TestEventCreateForbidden(t *testing.T) {
	dependencies, events := eventDependencies(t, coachOfTeam(5))
	events.writeErr = service.ErrGroupForbidden
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events", eventFormBody, false); response.Code != http.StatusForbidden {
		t.Fatalf("coach of another team create = %d, want 403", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events", eventFormBody, true); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `id="alerts"`) {
		t.Fatalf("HTMX forbidden create = %d, want 422 with alerts", response.Code)
	}
}

func TestEventUpdateCancelDelete(t *testing.T) {
	dependencies, events := eventDependencies(t, coachOfTeam(4))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5", eventFormBody+"&version=2", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Event saved.") || events.lastID != 5 || events.lastVersion != 2 {
		t.Fatalf("update = %d, id %d version %d", response.Code, events.lastID, events.lastVersion)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5", eventFormBody, true); response.Code != http.StatusUnprocessableEntity || events.writes != 1 {
		t.Fatalf("update without version = %d, writes %d", response.Code, events.writes)
	}
	events.writeErr = service.ErrOptimisticLockConflict
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5", eventFormBody+"&version=1", false); response.Code != http.StatusConflict {
		t.Fatalf("stale update = %d, want 409", response.Code)
	}
	events.writeErr = service.ErrEventNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5", eventFormBody+"&version=2", false); response.Code != http.StatusNotFound {
		t.Fatalf("vanished update = %d, want 404", response.Code)
	}
	events.writeErr = nil

	response = serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/cancel", "version=2&cancelled=true", true)
	if response.Code != http.StatusOK || !events.lastCancel || !strings.Contains(response.Body.String(), "Event cancelled.") {
		t.Fatalf("cancel = %d %v", response.Code, events.lastCancel)
	}
	response = serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/cancel", "version=3&cancelled=false", true)
	if response.Code != http.StatusOK || events.lastCancel || !strings.Contains(response.Body.String(), "Event reinstated.") {
		t.Fatalf("restore = %d %v", response.Code, events.lastCancel)
	}
	deleted := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/delete", "version=3", false)
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/en/groups/3/events" || events.lastVersion != 3 {
		t.Fatalf("delete = %d %q", deleted.Code, deleted.Header().Get("Location"))
	}
	events.writeErr = service.ErrGroupForbidden
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/delete", "version=3", false); response.Code != http.StatusForbidden {
		t.Fatalf("forbidden delete = %d, want 403", response.Code)
	}
}

func TestEventCancelledShown(t *testing.T) {
	dependencies, events := eventDependencies(t, playerOfTeam(4))
	cancelledAt := int64(1)
	events.list[0].CancelledAt = &cancelledAt
	events.detail.Event.CancelledAt = &cancelledAt
	if body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events", "", false).Body.String(); !strings.Contains(body, `data-event-id="5" data-event-cancelled="true"`) {
		t.Fatal("cancelled event not marked in the list")
	}
	if body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/5", "", false).Body.String(); !strings.Contains(body, `data-testid="event-cancelled-badge"`) {
		t.Fatal("cancelled event not marked on its page")
	}
}

func TestFormatEventRange(t *testing.T) {
	if got := formatEventRange(budapestTime("2026-10-24T22:00"), budapestTime("2026-10-25T06:00"), "Europe/Budapest"); got != "2026-10-24 22:00 – 2026-10-25 06:00" {
		t.Fatalf("overnight range = %q", got)
	}
	if got := formatEventInstant(budapestTime("2026-10-25T03:30"), "Europe/Budapest"); got != "2026-10-25 03:30" {
		t.Fatalf("fall-back instant = %q", got)
	}
}
