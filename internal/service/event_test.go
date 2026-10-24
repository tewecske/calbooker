package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// eventRepositoryStub keeps events of group 5 in memory.
type eventRepositoryStub struct {
	zone            string
	events          map[int64]Event
	coaches         []EventCoach
	nextID          int64
	createErr       error
	updateErr       error
	deleteErr       error
	expectedVersion int64
	coachDays       []time.Time
	scopes          []EventScope
}

func newEventStub(events ...Event) *eventRepositoryStub {
	stub := &eventRepositoryStub{zone: "Europe/Budapest", events: make(map[int64]Event), nextID: 100}
	for _, event := range events {
		stub.events[event.ID] = event
	}
	return stub
}

func (r *eventRepositoryStub) GroupTimeZone(context.Context, int64) (string, error) {
	return r.zone, nil
}

func (r *eventRepositoryStub) CreateEvent(_ context.Context, event Event) (Event, error) {
	if r.createErr != nil {
		return Event{}, r.createErr
	}
	event.ID = r.nextID
	r.nextID++
	r.events[event.ID] = event
	return event, nil
}

func (r *eventRepositoryStub) FindEvent(_ context.Context, groupID, eventID int64) (Event, error) {
	event, ok := r.events[eventID]
	if !ok || event.GroupID != groupID {
		return Event{}, ErrEventNotFound
	}
	return event, nil
}

func (r *eventRepositoryStub) ListEvents(_ context.Context, groupID int64, query EventQuery, scope EventScope) ([]Event, error) {
	r.scopes = append(r.scopes, scope)
	list := make([]Event, 0)
	for _, event := range r.events {
		switch {
		case event.GroupID != groupID,
			!query.From.IsZero() && !event.EndsAt.After(query.From),
			!query.To.IsZero() && !event.StartsAt.Before(query.To),
			query.TeamID > 0 && !slices.Contains(event.TeamIDs(), query.TeamID),
			query.ActiveOnly && event.Cancelled(),
			query.ExcludeID == event.ID:
			continue
		}
		if !scope.All && !slices.ContainsFunc(event.TeamIDs(), func(id int64) bool { return slices.Contains(scope.TeamIDs, id) }) && !slices.Contains(event.CoachUserIDs(), scope.UserID) {
			continue
		}
		list = append(list, event)
	}
	slices.SortFunc(list, func(a, b Event) int { return a.StartsAt.Compare(b.StartsAt) })
	return list, nil
}

func (r *eventRepositoryStub) UpdateEvent(_ context.Context, event Event, expectedVersion int64) (Event, error) {
	r.expectedVersion = expectedVersion
	if r.updateErr != nil {
		return Event{}, r.updateErr
	}
	event.Version++
	r.events[event.ID] = event
	return event, nil
}

func (r *eventRepositoryStub) DeleteEvent(_ context.Context, _, eventID, expectedVersion int64) error {
	r.expectedVersion = expectedVersion
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.events, eventID)
	return nil
}

func (r *eventRepositoryStub) TeamCoachesOn(_ context.Context, _ int64, on time.Time) ([]EventCoach, error) {
	r.coachDays = append(r.coachDays, on)
	return slices.Clone(r.coaches), nil
}

var (
	budapest = mustLocation("Europe/Budapest")
	eventNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
)

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

// wall builds a wall-clock time as EventInput expects it.
func wall(raw string) time.Time {
	parsed := ParseEventLocalTime(raw)
	if parsed.IsZero() {
		panic("bad wall time " + raw)
	}
	return parsed
}

func at(raw string) time.Time {
	parsed, err := time.ParseInLocation(EventTimeLayout, raw, budapest)
	if err != nil {
		panic(err)
	}
	return parsed
}

func eventTeams(ids ...int64) []EventTeam {
	teams := make([]EventTeam, 0, len(ids))
	for _, id := range ids {
		teams = append(teams, EventTeam{ID: id, Name: "Team " + strconv.FormatInt(id, 10)})
	}
	return teams
}

func storedEvent(id int64, teamIDs ...int64) Event {
	location := "Clubhouse"
	return Event{
		ID: id, GroupID: 5, Kind: EventKindTraining, Title: "Practice", StartsAt: at("2026-11-02T17:00"), EndsAt: at("2026-11-02T18:30"),
		Location: &location, SignupMode: EventSignupOpen, Teams: eventTeams(teamIDs...), Version: 4,
	}
}

func validEventInput(teamIDs ...int64) EventInput {
	return EventInput{
		Kind: EventKindChampionship, Title: " Autumn cup ", StartsAt: wall("2026-11-07T09:00"), EndsAt: wall("2026-11-07T16:00"),
		TeamIDs: teamIDs, Location: " City stadium ", SignupMode: EventSignupNominated,
	}
}

func childParentActor(teamIDs ...int64) GroupActor {
	return actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildTeamIDs: teamIDs, ChildUserIDs: []int64{20}})
}

type eventTestSetup struct {
	service   *EventService
	events    *eventRepositoryStub
	resources *resourceRepositoryStub
	auditor   *auditRecorderStub
}

func newTestEventService(t *testing.T, events *eventRepositoryStub, members ...GroupMember) eventTestSetup {
	t.Helper()
	archivedAt := int64(1)
	two := 2
	resources := newResourceStub(
		Resource{ID: 31, GroupID: 5, Name: "Court 1", Kind: ResourceKindCourt, MaxConcurrentTeams: &two},
		Resource{ID: 32, GroupID: 5, Name: "Old hall", Kind: ResourceKindHall, ArchivedAt: &archivedAt},
		Resource{ID: 33, GroupID: 6, Name: "Elsewhere", Kind: ResourceKindField},
	)
	resourceService, _ := newTestResourceService(t, resources)
	teams := newTeamStub(
		Team{ID: 1, GroupID: 5, Name: "Lions"},
		Team{ID: 2, GroupID: 5, Name: "Tigers"},
		Team{ID: 3, GroupID: 5, Name: "Old team", ArchivedAt: &archivedAt},
	)
	memberships := newMembershipStub()
	memberships.members = members
	service, err := NewEventService(events, teams, resourceService, memberships, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewEventService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	service.now = func() time.Time { return eventNow }
	return eventTestSetup{service: service, events: events, resources: resources, auditor: auditor}
}

func TestNewEventServiceRejectsNilDependencies(t *testing.T) {
	resources, _ := NewResourceService(newResourceStub(), usersWith())
	for name, build := range map[string]func() error{
		"events": func() error {
			_, err := NewEventService(nil, newTeamStub(), resources, newMembershipStub(), usersWith())
			return err
		},
		"teams": func() error {
			_, err := NewEventService(newEventStub(), nil, resources, newMembershipStub(), usersWith())
			return err
		},
		"resources": func() error {
			_, err := NewEventService(newEventStub(), newTeamStub(), nil, newMembershipStub(), usersWith())
			return err
		},
		"memberships": func() error {
			_, err := NewEventService(newEventStub(), newTeamStub(), resources, nil, usersWith())
			return err
		},
		"users": func() error {
			_, err := NewEventService(newEventStub(), newTeamStub(), resources, newMembershipStub(), nil)
			return err
		},
	} {
		if err := build(); err == nil {
			t.Errorf("nil %s accepted", name)
		}
	}
	var missing *EventService
	if _, err := missing.Find(context.Background(), adminActor().Access, 1); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("nil service error = %v", err)
	}
}

func TestEventInstantAcrossDaylightSaving(t *testing.T) {
	tests := []struct {
		name    string
		wall    string
		want    string // UTC
		wantErr error
	}{
		{name: "winter time", wall: "2026-12-01T18:00", want: "2026-12-01T17:00:00Z"},
		{name: "summer time", wall: "2026-07-01T18:00", want: "2026-07-01T16:00:00Z"},
		{name: "spring forward: last minute before the gap", wall: "2027-03-28T01:59", want: "2027-03-28T00:59:00Z"},
		{name: "spring forward: skipped time", wall: "2027-03-28T02:30", wantErr: ErrEventTimeSkipped},
		{name: "spring forward: start of the gap", wall: "2027-03-28T02:00", wantErr: ErrEventTimeSkipped},
		{name: "spring forward: first time after the gap", wall: "2027-03-28T03:00", want: "2027-03-28T01:00:00Z"},
		{name: "fall back: before the repeated hour", wall: "2026-10-25T01:30", want: "2026-10-24T23:30:00Z"},
		{name: "fall back: repeated time is its first occurrence", wall: "2026-10-25T02:30", want: "2026-10-25T00:30:00Z"},
		{name: "fall back: after the repeated hour", wall: "2026-10-25T03:30", want: "2026-10-25T02:30:00Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instant, err := EventInstant(wall(test.wall), "Europe/Budapest")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("EventInstant() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				return
			}
			if got := instant.UTC().Format(time.RFC3339); got != test.want {
				t.Fatalf("EventInstant() = %s, want %s", got, test.want)
			}
			back, err := EventLocalTime(instant, "Europe/Budapest")
			if err != nil || FormatEventLocalTime(back) != test.wall {
				t.Fatalf("EventLocalTime() = %q, %v, want %q", FormatEventLocalTime(back), err, test.wall)
			}
		})
	}
	if _, err := EventInstant(wall("2026-12-01T18:00"), "Mars/Base"); !errors.Is(err, ErrInvalidTimeZone) {
		t.Fatalf("unknown zone error = %v", err)
	}
	if _, err := EventInstant(wall("2026-12-01T18:00"), "Local"); !errors.Is(err, ErrInvalidTimeZone) {
		t.Fatalf("Local zone error = %v", err)
	}
	// Other zones follow their own rules: New York springs forward on
	// 2027-03-14.
	if _, err := EventInstant(wall("2027-03-14T02:30"), "America/New_York"); !errors.Is(err, ErrEventTimeSkipped) {
		t.Fatalf("New York gap error = %v", err)
	}
	if _, err := EventInstant(wall("2027-03-28T02:30"), "America/New_York"); err != nil {
		t.Fatalf("Budapest gap in New York error = %v", err)
	}
}

func TestEventOnDaylightSavingNightsKeepsWallClock(t *testing.T) {
	setup := newTestEventService(t, newEventStub())
	// A night tournament across the fall-back change: 22:00 to 06:00 lasts
	// nine hours, and both ends keep their wall-clock times.
	input := validEventInput(1)
	input.StartsAt, input.EndsAt = wall("2026-10-24T22:00"), wall("2026-10-25T06:00")
	created, _, err := setup.service.Create(context.Background(), adminActor(), input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.EndsAt.Sub(created.StartsAt) != 9*time.Hour {
		t.Fatalf("fall-back night lasts %v, want 9h", created.EndsAt.Sub(created.StartsAt))
	}
	if got := formatWall(t, created.EndsAt); got != "2026-10-25T06:00" {
		t.Fatalf("end wall clock = %s", got)
	}
	// The same night across spring-forward lasts seven hours.
	input.StartsAt, input.EndsAt = wall("2027-03-27T22:00"), wall("2027-03-28T06:00")
	created, _, err = setup.service.Create(context.Background(), adminActor(), input)
	if err != nil || created.EndsAt.Sub(created.StartsAt) != 7*time.Hour {
		t.Fatalf("spring-forward night = %v, %v, want 7h", created.EndsAt.Sub(created.StartsAt), err)
	}
	// A skipped start time is refused with the field named.
	input.StartsAt = wall("2027-03-28T02:15")
	_, _, err = setup.service.Create(context.Background(), adminActor(), input)
	var fieldErr *EventFieldError
	if !errors.Is(err, ErrEventTimeSkipped) || !errors.As(err, &fieldErr) || fieldErr.Field != "starts_at" {
		t.Fatalf("skipped start error = %v", err)
	}
	input = validEventInput(1)
	input.SignupDeadline = wall("2027-03-28T02:15")
	input.StartsAt, input.EndsAt = wall("2027-03-29T09:00"), wall("2027-03-29T10:00")
	if _, _, err = setup.service.Create(context.Background(), adminActor(), input); !errors.As(err, &fieldErr) || fieldErr.Field != "signup_deadline" {
		t.Fatalf("skipped deadline error = %v", err)
	}
}

func formatWall(t *testing.T, instant time.Time) string {
	t.Helper()
	local, err := EventLocalTime(instant, "Europe/Budapest")
	if err != nil {
		t.Fatal(err)
	}
	return FormatEventLocalTime(local)
}

func TestBuildEventValidation(t *testing.T) {
	with := func(mutate func(*EventInput)) EventInput {
		input := validEventInput(2, 1, 1)
		mutate(&input)
		return input
	}
	tests := []struct {
		name    string
		input   EventInput
		wantErr error
	}{
		{name: "valid", input: with(func(*EventInput) {})},
		{name: "resource instead of location", input: with(func(i *EventInput) { i.Location, i.ResourceID = "", 31 })},
		{name: "limit and deadline", input: with(func(i *EventInput) { i.MaxParticipants, i.SignupDeadline = 999, wall("2026-11-06T20:00") })},
		{name: "unknown kind", input: with(func(i *EventInput) { i.Kind = "party" }), wantErr: ErrInvalidEventKind},
		{name: "empty title", input: with(func(i *EventInput) { i.Title = " " }), wantErr: ErrInvalidEventTitle},
		{name: "long title", input: with(func(i *EventInput) { i.Title = strings.Repeat("a", 129) }), wantErr: ErrInvalidEventTitle},
		{name: "control character", input: with(func(i *EventInput) { i.Title = "a\nb" }), wantErr: ErrInvalidEventTitle},
		{name: "missing start", input: with(func(i *EventInput) { i.StartsAt = time.Time{} }), wantErr: ErrInvalidEventStart},
		{name: "start out of range", input: with(func(i *EventInput) { i.StartsAt = wall("1899-01-01T00:00") }), wantErr: ErrInvalidEventStart},
		{name: "missing end", input: with(func(i *EventInput) { i.EndsAt = time.Time{} }), wantErr: ErrInvalidEventEnd},
		{name: "end equals start", input: with(func(i *EventInput) { i.EndsAt = i.StartsAt }), wantErr: ErrInvalidEventEnd},
		{name: "end before start", input: with(func(i *EventInput) { i.EndsAt = wall("2026-11-07T08:00") }), wantErr: ErrInvalidEventEnd},
		{name: "longer than 31 days", input: with(func(i *EventInput) { i.EndsAt = wall("2026-12-08T09:01") }), wantErr: ErrInvalidEventEnd},
		{name: "no teams", input: with(func(i *EventInput) { i.TeamIDs = nil }), wantErr: ErrEventTeamsRequired},
		{name: "bad team id", input: with(func(i *EventInput) { i.TeamIDs = []int64{1, -1} }), wantErr: ErrInvalidEventTeams},
		{name: "neither resource nor location", input: with(func(i *EventInput) { i.Location = "  " }), wantErr: ErrEventPlaceRequired},
		{name: "both resource and location", input: with(func(i *EventInput) { i.ResourceID = 31 }), wantErr: ErrEventPlaceRequired},
		{name: "bad resource id", input: with(func(i *EventInput) { i.Location, i.ResourceID = "", -1 }), wantErr: ErrInvalidEventResource},
		{name: "long location", input: with(func(i *EventInput) { i.Location = strings.Repeat("a", 256) }), wantErr: ErrInvalidEventLocation},
		{name: "unknown sign-up mode", input: with(func(i *EventInput) { i.SignupMode = "lottery" }), wantErr: ErrInvalidEventSignupMode},
		{name: "limit too high", input: with(func(i *EventInput) { i.MaxParticipants = 1000 }), wantErr: ErrInvalidEventMaxParticipants},
		{name: "negative limit", input: with(func(i *EventInput) { i.MaxParticipants = -1 }), wantErr: ErrInvalidEventMaxParticipants},
		{name: "deadline at the start", input: with(func(i *EventInput) { i.SignupDeadline = i.StartsAt }), wantErr: ErrInvalidEventSignupDeadline},
		{name: "deadline after the start", input: with(func(i *EventInput) { i.SignupDeadline = wall("2026-11-07T10:00") }), wantErr: ErrInvalidEventSignupDeadline},
		{name: "unparseable deadline", input: with(func(i *EventInput) { i.SignupDeadline = time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC) }), wantErr: ErrInvalidEventSignupDeadline},
		{name: "bad coach id", input: with(func(i *EventInput) { i.CoachUserIDs = []int64{0} }), wantErr: ErrInvalidEventCoaches},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := buildEvent(test.input, "Europe/Budapest")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("buildEvent() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				return
			}
			if event.Title != "Autumn cup" || !slices.Equal(event.TeamIDs(), []int64{1, 2}) || !event.StartsAt.Equal(at("2026-11-07T09:00")) {
				t.Fatalf("event = %+v", event)
			}
			if test.input.ResourceID == 0 && (event.Location == nil || *event.Location != "City stadium" || event.ResourceID != nil) {
				t.Fatalf("location = %v, resource = %v", event.Location, event.ResourceID)
			}
		})
	}
}

func TestEventServiceCreatePermissions(t *testing.T) {
	tests := []struct {
		name    string
		actor   GroupActor
		teams   []int64
		wantErr error
	}{
		{name: "admin for any team", actor: adminActor(), teams: []int64{1, 2}},
		{name: "manager for any team", actor: actorWith(memberWith(GroupRoleManager)), teams: []int64{2}},
		{name: "site administrator", actor: siteAdminActor(), teams: []int64{1}},
		{name: "coach of the team", actor: coachOf(1), teams: []int64{1}},
		{name: "coach of both teams", actor: coachOf(1, 2), teams: []int64{1, 2}},
		{name: "coach of another team", actor: coachOf(2), teams: []int64{1}, wantErr: ErrGroupForbidden},
		{name: "coach with one team not theirs", actor: coachOf(1), teams: []int64{1, 2}, wantErr: ErrGroupForbidden},
		{name: "coach without a team this season", actor: actorWith(memberWith(GroupRoleCoach)), teams: []int64{1}, wantErr: ErrGroupForbidden},
		{name: "player", actor: playerOn(1), teams: []int64{1}, wantErr: ErrGroupForbidden},
		{name: "parent of a child on the team", actor: childParentActor(1), teams: []int64{1}, wantErr: ErrGroupForbidden},
		{name: "non-member", actor: actorWith(GroupAccessGrants{}), teams: []int64{1}, wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := newTestEventService(t, newEventStub())
			created, _, err := setup.service.Create(context.Background(), test.actor, validEventInput(test.teams...))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if len(setup.events.events) != 0 || len(setup.auditor.records) != 0 {
					t.Fatal("refused create stored or audited an event")
				}
				return
			}
			if created.GroupID != 5 || created.Kind != EventKindChampionship || created.SignupMode != EventSignupNominated || created.CreatedAt != eventNow.Unix() {
				t.Fatalf("created = %+v", created)
			}
			if len(setup.auditor.records) != 1 {
				t.Fatalf("audit records = %d", len(setup.auditor.records))
			}
			record := setup.auditor.records[0]
			if record.Action != AuditActionEventCreated || record.TargetType != "group" || record.TargetID != "5" || record.IP != "192.0.2.1" {
				t.Fatalf("audit record = %+v", record)
			}
			if !strings.Contains(record.Detail, "event 100: Autumn cup; championship; 2026-11-07T08:00:00Z to 2026-11-07T15:00:00Z; teams ") || !strings.Contains(record.Detail, "; location") || strings.Contains(record.Detail, "City stadium") {
				t.Fatalf("audit detail = %q", record.Detail)
			}
			if strings.Contains(record.Detail, "(site administrator)") != (test.name == "site administrator") {
				t.Fatalf("site administrator marker in %q", record.Detail)
			}
		})
	}
}

func TestEventServiceCreateResources(t *testing.T) {
	setup := newTestEventService(t, newEventStub())
	input := validEventInput(1)
	input.Location = ""
	for _, test := range []struct {
		resource int64
		wantErr  error
	}{
		{31, nil},
		{32, ErrEventResourceArchived},
		{33, ErrInvalidEventResource},
		{99, ErrInvalidEventResource},
	} {
		input.ResourceID = test.resource
		created, _, err := setup.service.Create(context.Background(), adminActor(), input)
		if !errors.Is(err, test.wantErr) {
			t.Errorf("resource %d error = %v, want %v", test.resource, err, test.wantErr)
		}
		if err == nil && (created.ResourceID == nil || *created.ResourceID != 31) {
			t.Errorf("created resource = %v", created.ResourceID)
		}
	}
	// Foreign-key failures from storage become validation errors.
	for stored, want := range map[error]error{
		ErrTeamNotFound:          ErrInvalidEventTeams,
		ErrResourceNotFound:      ErrInvalidEventResource,
		ErrEventCoachRoleMissing: ErrEventCoachRoleMissing,
	} {
		setup.events.createErr = stored
		input.ResourceID = 31
		if _, _, err := setup.service.Create(context.Background(), adminActor(), input); !errors.Is(err, want) {
			t.Errorf("storage error %v became %v, want %v", stored, err, want)
		}
	}
}

func TestEventVisibilityPerRole(t *testing.T) {
	ownTeam := storedEvent(1, 1)
	otherTeam := storedEvent(2, 2)
	extraCoach := storedEvent(3, 2)
	extraCoach.Coaches = []EventCoach{{UserID: 8}}
	tests := []struct {
		name    string
		actor   GroupActor
		visible []int64
	}{
		{name: "admin", actor: adminActor(), visible: []int64{1, 2, 3}},
		{name: "manager", actor: actorWith(memberWith(GroupRoleManager)), visible: []int64{1, 2, 3}},
		{name: "site administrator", actor: siteAdminActor(), visible: []int64{1, 2, 3}},
		{name: "coach of the team", actor: coachOf(1), visible: []int64{1}},
		{name: "coach of another team", actor: actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{9}}), visible: nil},
		{name: "player of the team", actor: playerOn(1), visible: []int64{1}},
		{name: "player of another team", actor: playerOn(2), visible: []int64{2, 3}},
		{name: "parent of a child on the team", actor: childParentActor(1), visible: []int64{1}},
		{name: "extra coach", actor: GroupActor{Access: NewGroupAccess(5, 8, GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}})}, visible: []int64{3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := newTestEventService(t, newEventStub(ownTeam, otherTeam, extraCoach))
			for _, id := range []int64{1, 2, 3} {
				_, err := setup.service.Find(context.Background(), test.actor.Access, id)
				want := slices.Contains(test.visible, id)
				if (err == nil) != want || (!want && !errors.Is(err, ErrEventNotFound)) {
					t.Errorf("Find(%d) error = %v, want visible %v", id, err, want)
				}
			}
			listed, err := setup.service.ListBetween(context.Background(), test.actor.Access, at("2026-11-01T00:00"), at("2026-11-03T00:00"))
			if err != nil {
				t.Fatalf("ListBetween() error = %v", err)
			}
			ids := make([]int64, 0)
			for _, event := range listed {
				ids = append(ids, event.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, append([]int64{}, test.visible...)) {
				t.Errorf("listed = %v, want %v", ids, test.visible)
			}
		})
	}
	setup := newTestEventService(t, newEventStub(ownTeam))
	if _, err := setup.service.Find(context.Background(), actorWith(GroupAccessGrants{}).Access, 1); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("non-member Find error = %v", err)
	}
	if _, err := setup.service.List(context.Background(), actorWith(GroupAccessGrants{}).Access, EventQuery{}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("non-member List error = %v", err)
	}
	// A player filtering by a team they do not see gets nothing, and the
	// store is never asked.
	listed, err := setup.service.List(context.Background(), playerOn(2).Access, EventQuery{TeamID: 1})
	if err != nil || len(listed) != 0 || len(setup.events.scopes) != 0 {
		t.Fatalf("hidden team filter = %v, %v", listed, err)
	}
	if _, err := setup.service.List(context.Background(), playerOn(2).Access, EventQuery{}); err != nil {
		t.Fatal(err)
	}
	if scope := setup.events.scopes[0]; scope.All || !slices.Equal(scope.TeamIDs, []int64{2}) || scope.UserID != 7 {
		t.Fatalf("player scope = %+v", scope)
	}
}

func TestEventServiceUpdate(t *testing.T) {
	ctx := context.Background()
	t.Run("coach edits an event of their team", func(t *testing.T) {
		setup := newTestEventService(t, newEventStub(storedEvent(1, 1)))
		updated, _, err := setup.service.Update(ctx, coachOf(1), 1, 4, validEventInput(1))
		if err != nil || updated.Title != "Autumn cup" || updated.Version != 5 || setup.events.expectedVersion != 4 || updated.EditedAt != nil {
			t.Fatalf("Update() = %+v, %v", updated, err)
		}
		if len(setup.auditor.records) != 1 || setup.auditor.records[0].Action != AuditActionEventUpdated {
			t.Fatalf("audit = %+v", setup.auditor.records)
		}
	})
	for _, test := range []struct {
		name    string
		actor   GroupActor
		stored  Event
		teams   []int64
		wantErr error
	}{
		{name: "coach adds a team they do not coach", actor: coachOf(1), stored: storedEvent(1, 1), teams: []int64{1, 2}, wantErr: ErrGroupForbidden},
		{name: "coach of one of two teams", actor: coachOf(1), stored: storedEvent(1, 1, 2), teams: []int64{1}, wantErr: ErrGroupForbidden},
		{name: "coach of another team", actor: coachOf(2), stored: storedEvent(1, 1), teams: []int64{2}, wantErr: ErrEventNotFound},
		{name: "player of the team", actor: playerOn(1), stored: storedEvent(1, 1), teams: []int64{1}, wantErr: ErrGroupForbidden},
		{name: "parent of a child on the team", actor: childParentActor(1), stored: storedEvent(1, 1), teams: []int64{1}, wantErr: ErrGroupForbidden},
		{name: "non-member", actor: actorWith(GroupAccessGrants{}), stored: storedEvent(1, 1), teams: []int64{1}, wantErr: ErrGroupNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			setup := newTestEventService(t, newEventStub(test.stored))
			if _, _, err := setup.service.Update(ctx, test.actor, 1, 4, validEventInput(test.teams...)); !errors.Is(err, test.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, test.wantErr)
			}
			if len(setup.auditor.records) != 0 || setup.events.events[1].Title != "Practice" {
				t.Fatal("refused update changed or audited the event")
			}
		})
	}
	t.Run("editing a generated event marks it edited", func(t *testing.T) {
		generated := storedEvent(1, 1)
		plan := int64(77)
		week := day("2026-11-02")
		generated.PlanTrainingID, generated.GeneratedForWeek = &plan, &week
		setup := newTestEventService(t, newEventStub(generated))
		updated, _, err := setup.service.Update(ctx, adminActor(), 1, 4, validEventInput(1))
		if err != nil || updated.EditedAt == nil || *updated.EditedAt != eventNow.Unix() || updated.PlanTrainingID == nil || *updated.PlanTrainingID != 77 || updated.GeneratedForWeek == nil {
			t.Fatalf("generated update = %+v, %v", updated, err)
		}
		if !strings.Contains(setup.auditor.records[0].Detail, "; generated") {
			t.Fatalf("audit detail = %q", setup.auditor.records[0].Detail)
		}
	})
	t.Run("an event keeps its archived resource but cannot move to one", func(t *testing.T) {
		stored := storedEvent(1, 1)
		archived := int64(32)
		stored.ResourceID, stored.Location = &archived, nil
		setup := newTestEventService(t, newEventStub(stored))
		input := validEventInput(1)
		input.Location, input.ResourceID = "", 32
		if _, _, err := setup.service.Update(ctx, adminActor(), 1, 4, input); err != nil {
			t.Fatalf("keeping the archived resource error = %v", err)
		}
		other := storedEvent(2, 1)
		setup.events.events[2] = other
		if _, _, err := setup.service.Update(ctx, adminActor(), 2, 4, input); !errors.Is(err, ErrEventResourceArchived) {
			t.Fatalf("moving to an archived resource error = %v", err)
		}
	})
	t.Run("stale and vanished writes", func(t *testing.T) {
		setup := newTestEventService(t, newEventStub(storedEvent(1, 1)))
		setup.events.updateErr = ErrOptimisticLockConflict
		if _, _, err := setup.service.Update(ctx, adminActor(), 1, 3, validEventInput(1)); !errors.Is(err, ErrOptimisticLockConflict) {
			t.Fatalf("stale error = %v", err)
		}
		setup.events.updateErr = ErrRecordNotFound
		if _, _, err := setup.service.Update(ctx, adminActor(), 1, 4, validEventInput(1)); !errors.Is(err, ErrEventNotFound) || IsWriteConflict(err) {
			t.Fatalf("vanished error = %v", err)
		}
		if _, _, err := setup.service.Update(ctx, adminActor(), 99, 4, validEventInput(1)); !errors.Is(err, ErrEventNotFound) {
			t.Fatalf("missing error = %v", err)
		}
		if _, _, err := setup.service.Update(ctx, adminActor(), 1, -1, validEventInput(1)); !errors.Is(err, ErrInvalidGroup) {
			t.Fatalf("negative version error = %v", err)
		}
	})
}

func TestEventServiceCancelAndDelete(t *testing.T) {
	ctx := context.Background()
	generated := storedEvent(1, 1)
	plan := int64(77)
	generated.PlanTrainingID = &plan
	setup := newTestEventService(t, newEventStub(generated))
	cancelled, err := setup.service.SetCancelled(ctx, coachOf(1), 1, 4, true)
	if err != nil || !cancelled.Cancelled() || cancelled.EditedAt == nil {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	// Cancelling again changes nothing.
	if _, err := setup.service.SetCancelled(ctx, coachOf(1), 1, 5, true); err != nil || len(setup.auditor.records) != 1 {
		t.Fatalf("repeated cancel = %v, records %d", err, len(setup.auditor.records))
	}
	restored, err := setup.service.SetCancelled(ctx, adminActor(), 1, 5, false)
	if err != nil || restored.Cancelled() {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	actions := []string{setup.auditor.records[0].Action, setup.auditor.records[1].Action}
	if !slices.Equal(actions, []string{AuditActionEventCancelled, AuditActionEventRestored}) {
		t.Fatalf("actions = %v", actions)
	}
	if _, err := setup.service.SetCancelled(ctx, playerOn(1), 1, 6, true); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("player cancel error = %v", err)
	}
	if _, err := setup.service.SetCancelled(ctx, coachOf(2), 1, 6, true); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("other coach cancel error = %v", err)
	}
	if err := setup.service.Delete(ctx, childParentActor(1), 1, 6); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("parent delete error = %v", err)
	}
	setup.events.deleteErr = ErrOptimisticLockConflict
	if err := setup.service.Delete(ctx, adminActor(), 1, 5); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	setup.events.deleteErr = ErrRecordNotFound
	if err := setup.service.Delete(ctx, adminActor(), 1, 6); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("vanished delete error = %v", err)
	}
	setup.events.deleteErr = nil
	if err := setup.service.Delete(ctx, coachOf(1), 1, 6); err != nil || len(setup.events.events) != 0 {
		t.Fatalf("delete = %v", err)
	}
	if last := setup.auditor.records[len(setup.auditor.records)-1]; last.Action != AuditActionEventDeleted || !strings.Contains(last.Detail, "event 1: Practice") {
		t.Fatalf("delete audit = %+v", last)
	}
}

func onCourt(event Event) Event {
	court := int64(31)
	event.ResourceID, event.Location = &court, nil
	return event
}

func TestEventWarnings(t *testing.T) {
	ctx := context.Background()
	carla, dora := "Carla", "Dora"
	t.Run("resource over its concurrent-team limit", func(t *testing.T) {
		// Court 1 allows two teams. A one-team event overlaps the second
		// half; a cancelled one and one that starts when ours ends do not
		// count.
		overlapping := onCourt(storedEvent(2, 2))
		overlapping.StartsAt, overlapping.EndsAt = at("2026-11-02T18:00"), at("2026-11-02T19:00")
		cancelledAt := int64(1)
		cancelled := onCourt(storedEvent(3, 2))
		cancelled.CancelledAt = &cancelledAt
		backToBack := onCourt(storedEvent(4, 2))
		backToBack.StartsAt, backToBack.EndsAt = at("2026-11-02T18:30"), at("2026-11-02T20:00")
		setup := newTestEventService(t, newEventStub(overlapping, cancelled, backToBack))
		input := validEventInput(1, 3)
		input.Location, input.ResourceID = "", 31
		input.StartsAt, input.EndsAt = wall("2026-11-02T17:00"), wall("2026-11-02T18:30")
		_, warnings, err := setup.service.Create(ctx, adminActor(), input)
		if err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 1 || warnings[0].Kind != EventWarningResourceCapacity || warnings[0].ResourceName != "Court 1" || warnings[0].Limit != 2 || warnings[0].Teams != 3 {
			t.Fatalf("warnings = %+v", warnings)
		}
		// Within the limit there is no warning.
		delete(setup.events.events, 100)
		input.TeamIDs = []int64{1}
		if _, warnings, _ := setup.service.Create(ctx, adminActor(), input); len(warnings) != 0 {
			t.Fatalf("within-limit warnings = %+v", warnings)
		}
	})
	t.Run("coach busy in another event", func(t *testing.T) {
		// Carla coaches team 1 this season and is an extra coach of an
		// overlapping event of team 2; Dora coaches team 2.
		other := storedEvent(2, 2)
		other.Title = "Tigers match"
		other.Coaches = []EventCoach{{UserID: 30, DisplayName: &carla}}
		elsewhere := storedEvent(3, 2)
		elsewhere.StartsAt, elsewhere.EndsAt = at("2026-11-03T17:00"), at("2026-11-03T18:00")
		events := newEventStub(other, elsewhere)
		events.coaches = []EventCoach{{UserID: 30, TeamID: 1, DisplayName: &carla}, {UserID: 31, TeamID: 2, DisplayName: &dora}}
		setup := newTestEventService(t, events)
		input := validEventInput(1)
		input.StartsAt, input.EndsAt = wall("2026-11-02T17:30"), wall("2026-11-02T19:00")
		created, warnings, err := setup.service.Create(ctx, coachOf(1), input)
		if err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 1 || warnings[0].Kind != EventWarningCoachBusy || warnings[0].Coach.UserID != 30 || warnings[0].OtherEventID != 2 || warnings[0].OtherTitle != "Tigers match" {
			t.Fatalf("warnings = %+v", warnings)
		}
		if got := events.coachDays[0].Format(SeasonDateLayout); got != "2026-11-02" {
			t.Fatalf("season coaches asked for %s", got)
		}
		// Dora as an extra coach is busy too: she coaches team 2.
		input.CoachUserIDs = []int64{31}
		if _, warnings, _ := setup.service.Update(ctx, coachOf(1), created.ID, 0, input); len(warnings) != 2 {
			t.Fatalf("extra coach warnings = %+v", warnings)
		}
		// The event page shows the warnings to those who edit it only.
		detail, err := setup.service.Detail(ctx, coachOf(1).Access, created.ID)
		if err != nil || len(detail.Warnings) != 2 || !detail.CanEdit || len(detail.TeamCoaches) != 1 || detail.TeamCoaches[0].UserID != 30 || detail.TimeZone != "Europe/Budapest" {
			t.Fatalf("coach detail = %+v, %v", detail, err)
		}
		detail, err = setup.service.Detail(ctx, playerOn(1).Access, created.ID)
		if err != nil || len(detail.Warnings) != 0 || detail.CanEdit {
			t.Fatalf("player detail = %+v, %v", detail, err)
		}
		// A cancelled event has no warnings.
		cancelled, _ := setup.service.SetCancelled(ctx, adminActor(), created.ID, 1, true)
		if detail, _ := setup.service.Detail(ctx, adminActor().Access, cancelled.ID); len(detail.Warnings) != 0 {
			t.Fatalf("cancelled warnings = %+v", detail.Warnings)
		}
	})
}

func TestEventFormOptions(t *testing.T) {
	ctx := context.Background()
	carla := "Carla"
	members := []GroupMember{
		{UserID: 30, Roles: []string{GroupRoleCoach}, DisplayName: &carla, Email: strPtr("carla@example.test")},
		{UserID: 40, Roles: []string{GroupRolePlayer}},
	}
	setup := newTestEventService(t, newEventStub(), members...)
	options, err := setup.service.FormOptions(ctx, adminActor().Access, nil)
	if err != nil {
		t.Fatal(err)
	}
	teamNames := func(teams []Team) []string {
		names := make([]string, 0, len(teams))
		for _, team := range teams {
			names = append(names, team.Name)
		}
		return names
	}
	if !slices.Equal(teamNames(options.Teams), []string{"Lions", "Tigers"}) || len(options.Resources) != 1 || options.Resources[0].ID != 31 || options.TimeZone != "Europe/Budapest" {
		t.Fatalf("admin options = %+v", options)
	}
	if len(options.Coaches) != 1 || options.Coaches[0].UserID != 30 || options.Coaches[0].Email != nil {
		t.Fatalf("coach candidates = %+v", options.Coaches)
	}
	options, err = setup.service.FormOptions(ctx, coachOf(2).Access, nil)
	if err != nil || !slices.Equal(teamNames(options.Teams), []string{"Tigers"}) {
		t.Fatalf("coach options = %+v, %v", options, err)
	}
	// Editing keeps the event's archived team and resource on offer.
	stored := onCourt(storedEvent(1, 3))
	archived := int64(32)
	stored.ResourceID = &archived
	options, err = setup.service.FormOptions(ctx, adminActor().Access, &stored)
	if err != nil || !slices.Contains(teamNames(options.Teams), "Old team") || len(options.Resources) != 2 {
		t.Fatalf("edit options = %+v, %v", options, err)
	}
	for _, actor := range []GroupActor{playerOn(1), childParentActor(1), actorWith(memberWith(GroupRoleCoach))} {
		if _, err := setup.service.FormOptions(ctx, actor.Access, nil); !errors.Is(err, ErrGroupForbidden) {
			t.Errorf("roles %v: FormOptions error = %v", actor.Access.Roles(), err)
		}
	}
	if _, err := setup.service.FormOptions(ctx, actorWith(GroupAccessGrants{}).Access, nil); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("non-member FormOptions error = %v", err)
	}
}

func TestPeakResourceTeams(t *testing.T) {
	event := onCourt(storedEvent(1, 1, 2))
	inside := onCourt(storedEvent(2, 3))
	inside.StartsAt, inside.EndsAt = at("2026-11-02T17:15"), at("2026-11-02T17:45")
	later := onCourt(storedEvent(3, 4))
	later.StartsAt, later.EndsAt = at("2026-11-02T18:00"), at("2026-11-02T20:00")
	otherCourt := storedEvent(4, 5)
	// inside and later never overlap each other, so the peak is 3, not 4.
	if peak := peakResourceTeams(event, []Event{inside, later, otherCourt}); peak != 3 {
		t.Fatalf("peak = %d, want 3", peak)
	}
}

func TestGroupAccessEventQuestions(t *testing.T) {
	event := storedEvent(1, 1, 2)
	tests := []struct {
		name                        string
		access                      *GroupAccess
		createsAny, createsOne, all bool
		edits, views                bool
	}{
		{name: "admin", access: adminActor().Access, createsAny: true, createsOne: true, all: true, edits: true, views: true},
		{name: "site administrator", access: siteAdminActor().Access, createsAny: true, createsOne: true, all: true, edits: true, views: true},
		{name: "coach of team 1", access: coachOf(1).Access, createsAny: true, createsOne: true, views: true},
		{name: "coach of both teams", access: coachOf(1, 2).Access, createsAny: true, createsOne: true, all: true, edits: true, views: true},
		{name: "coach without teams", access: actorWith(memberWith(GroupRoleCoach)).Access},
		{name: "player of team 2", access: playerOn(2).Access, views: true},
		{name: "parent of a child on team 1", access: childParentActor(1).Access, views: true},
		{name: "member of another team", access: playerOn(9).Access},
		{name: "non-member", access: actorWith(GroupAccessGrants{}).Access},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := test.access
			if access.CanCreateEvents() != test.createsAny || access.CanCreateEvent([]int64{1}) != test.createsOne || access.CanCreateEvent([]int64{1, 2}) != test.all {
				t.Errorf("create answers = %v %v %v", access.CanCreateEvents(), access.CanCreateEvent([]int64{1}), access.CanCreateEvent([]int64{1, 2}))
			}
			if access.CanEditEvent(event) != test.edits || access.CanViewEvent(event) != test.views {
				t.Errorf("edit %v view %v", access.CanEditEvent(event), access.CanViewEvent(event))
			}
		})
	}
	other := event
	other.GroupID = 6
	if adminActor().Access.CanViewEvent(other) || adminActor().Access.CanEditEvent(other) {
		t.Fatal("an event of another group is visible")
	}
	if coachOf(1).Access.CanCreateEvent(nil) {
		t.Fatal("a coach may create an event without teams")
	}
}
