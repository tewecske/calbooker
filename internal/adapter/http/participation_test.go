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

type participationUseCaseStub struct {
	roster     service.ParticipationRoster
	rosterErr  error
	writeErr   error
	calls      int
	lastEvent  int64
	lastUser   int64
	lastStatus string
	lastMarks  []service.AttendanceMark
}

func (s *participationUseCaseStub) Roster(_ context.Context, _ *service.GroupAccess, detail service.EventDetail) (service.ParticipationRoster, error) {
	s.calls++
	roster := s.roster
	roster.Event = detail.Event
	return roster, s.rosterErr
}

func (s *participationUseCaseStub) Respond(_ context.Context, _ service.GroupActor, eventID, userID int64, status string) (*service.Participant, error) {
	s.calls++
	s.lastEvent, s.lastUser, s.lastStatus = eventID, userID, status
	if s.writeErr != nil {
		return nil, s.writeErr
	}
	return &service.Participant{EventID: eventID, UserID: userID, Status: status}, nil
}

func (s *participationUseCaseStub) RecordAttendance(_ context.Context, _ service.GroupActor, eventID int64, marks []service.AttendanceMark) error {
	s.calls++
	s.lastEvent, s.lastMarks = eventID, marks
	return s.writeErr
}

func (s *participationUseCaseStub) CalendarAnswers(context.Context, *service.GroupAccess, time.Time, time.Time) ([]service.CalendarAnswer, error) {
	s.calls++
	return nil, nil
}

func participationDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *eventsUseCaseStub, *participationUseCaseStub) {
	t.Helper()
	dependencies, events := eventDependencies(t, access)
	events.detail.Event.SignupMode = service.EventSignupOpen
	yes := true
	participation := &participationUseCaseStub{roster: service.ParticipationRoster{
		Taken: 2, Phase: service.ParticipationOpen,
		Entries: []service.ParticipationEntry{
			{Player: service.EventPlayer{UserID: 7, Kind: service.UserKindGuest, DisplayName: eventStr("Gus Guest")}, OnEventTeam: true, Self: true, Status: service.ParticipantSignedUp, Choices: []string{service.ParticipantDeclined}},
			{Player: service.EventPlayer{UserID: 20, Kind: service.UserKindProfile, DisplayName: eventStr("Anna")}, OnEventTeam: true, Child: true, Choices: []string{service.ParticipantSignedUp, service.ParticipantDeclined}},
			{Player: service.EventPlayer{UserID: 21, Kind: service.UserKindAccount, DisplayName: eventStr("Walker"), Email: eventStr("walker@example.test")}, Attended: &yes},
		},
	}}
	dependencies.Participation = participation
	return dependencies, events, participation
}

func TestEventPageShowsParticipants(t *testing.T) {
	dependencies, _, participation := participationDependencies(t, playerOfTeam(4))
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/5", "", false)
	body := response.Body.String()
	if response.Code != http.StatusOK || participation.calls != 1 {
		t.Fatalf("status = %d, roster calls %d", response.Code, participation.calls)
	}
	for _, want := range []string{
		`id="participation-heading"`, "2 coming", "Sign-up is open.",
		`data-testid="participant-7" data-participant-id="7" data-status="signed_up"`, "Gus Guest", "You", "Guest", "Coming",
		`aria-label="Can&#39;t come – Gus Guest"`, `action="/en/groups/3/events/5/participation"`,
		`data-testid="participant-20"`, "Your child", "Child profile", `aria-label="I&#39;ll be there – Anna"`,
		`data-testid="participant-21" data-participant-id="21" data-status="" data-attended="yes"`, "Walk-in", "Attended", "Not on the event&#39;s teams",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("participants card missing %q", want)
		}
	}
	for _, unwanted := range []string{"walker@example.test", `data-testid="attendance-form"`, `data-choice="none"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("player sees %q", unwanted)
		}
	}

	// Staff see emails, manage labels, and the attendance form after the event.
	participation.roster.CanManage, participation.roster.ShowEmails, participation.roster.AttendanceOpen = true, true, true
	participation.roster.Phase = service.ParticipationEnded
	participation.roster.Entries[0].CanRemove = true
	participation.roster.Entries[0].Choices = nil
	participation.roster.Entries[1].Choices = []string{service.ParticipantSignedUp}
	participation.roster.WalkIns = []service.EventPlayer{{UserID: 30, DisplayName: eventStr("Late Larry")}}
	participation.roster.AttendanceRecorded = true
	body = serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/5", "", false).Body.String()
	for _, want := range []string{
		"walker@example.test", "Mark as coming", `aria-label="Clear answer – Gus Guest"`, "The event has ended.",
		`data-testid="attendance-form"`, `action="/en/groups/3/events/5/attendance"`, `name="listed" value="7"`,
		`id="attended-7" class="checkbox" type="checkbox" name="attended" value="7" checked`,
		`id="attended-20" class="checkbox" type="checkbox" name="attended" value="20">`,
		`id="attended-21" class="checkbox" type="checkbox" name="attended" value="21" checked`,
		`<option value="30">Late Larry</option>`, `data-testid="attendance-recorded"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("staff card missing %q", want)
		}
	}

	// A limit shows the places; Hungarian has its own labels.
	limit := 10
	participation.roster.Limit = &limit
	body = serveGroups(t, dependencies, http.MethodGet, "/hu/groups/3/events/5", "", false).Body.String()
	for _, want := range []string{"Résztvevők", "10 helyből 2 foglalt", "Jelenlét mentése"} {
		if !strings.Contains(body, want) {
			t.Errorf("Hungarian card missing %q", want)
		}
	}

	// Without the use cases the page still renders, without the card.
	dependencies.Participation = nil
	if body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/5", "", false).Body.String(); strings.Contains(body, `id="participation-heading"`) || !strings.Contains(body, "Autumn cup") {
		t.Fatal("event page without participation")
	}
	dependencies.Participation = participation
	participation.rosterErr = service.ErrEventNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/events/5", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("roster not found = %d, want 404", response.Code)
	}
}

func TestParticipationRespondRoute(t *testing.T) {
	dependencies, _, participation := participationDependencies(t, playerOfTeam(4))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=7&status=signed_up", true)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Answer saved.") || !strings.Contains(body, `id="alerts"`) || !strings.Contains(body, `id="participation-heading"`) {
		t.Fatalf("respond = %d", response.Code)
	}
	if participation.lastEvent != 5 || participation.lastUser != 7 || participation.lastStatus != service.ParticipantSignedUp {
		t.Fatalf("respond args = %d %d %q", participation.lastEvent, participation.lastUser, participation.lastStatus)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=20&status=none", true)
	if participation.lastUser != 20 || participation.lastStatus != "" {
		t.Fatalf("remove args = %d %q", participation.lastUser, participation.lastStatus)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=20", true)
	if participation.lastStatus != "invalid" {
		t.Fatalf("missing status passed as %q", participation.lastStatus)
	}
	calls := participation.calls
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=abc&status=signed_up", true); response.Code != http.StatusUnprocessableEntity || participation.calls != calls+1 {
		// The page is re-rendered (one roster call) without a write.
		t.Fatalf("bad user = %d, calls %d", response.Code, participation.calls-calls)
	}

	for _, test := range []struct {
		err  error
		text string
	}{
		{service.ErrEventFull, "The event is full."},
		{service.ErrSignupDeadlinePassed, "deadline has passed"},
		{service.ErrParticipationStarted, "has started"},
		{service.ErrParticipationCancelled, "is cancelled"},
		{service.ErrInvalidParticipantStatus, "not possible for this event"},
		{service.ErrParticipantNotEligible, "Only players of the event"},
	} {
		participation.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=7&status=signed_up", true)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, test.text) || !strings.Contains(body, `id="alerts"`) || !strings.Contains(body, `id="participation-heading"`) {
			t.Errorf("error %v = %d, want 422 with %q", test.err, response.Code, test.text)
		}
	}
	participation.writeErr = service.ErrGroupForbidden
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=8&status=signed_up", false); response.Code != http.StatusForbidden {
		t.Fatalf("forbidden = %d, want 403", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=8&status=signed_up", true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("HTMX forbidden = %d, want 422", response.Code)
	}
	participation.writeErr = service.ErrEventNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=7&status=signed_up", false); response.Code != http.StatusNotFound {
		t.Fatalf("hidden event = %d, want 404", response.Code)
	}
	participation.writeErr = nil
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/participation", "user_id=7&status=signed_up", false); response.Code != http.StatusOK {
		t.Fatalf("plain form post = %d", response.Code)
	}
}

func TestParticipationAttendanceRoute(t *testing.T) {
	dependencies, _, participation := participationDependencies(t, coachOfTeam(4))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/attendance", "listed=7&listed=20&listed=21&attended=7&attended=21&walk_in=30", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Attendance saved.") {
		t.Fatalf("attendance = %d", response.Code)
	}
	want := []service.AttendanceMark{{UserID: 7, Attended: true}, {UserID: 20}, {UserID: 21, Attended: true}, {UserID: 30, Attended: true}}
	if participation.lastEvent != 5 || !slices.Equal(participation.lastMarks, want) {
		t.Fatalf("marks = %+v", participation.lastMarks)
	}
	// A walk-in who is already listed is not added twice.
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/attendance", "listed=7&walk_in=7", true)
	if !slices.Equal(participation.lastMarks, []service.AttendanceMark{{UserID: 7}}) {
		t.Fatalf("listed walk-in marks = %+v", participation.lastMarks)
	}
	participation.lastMarks = nil
	for _, form := range []string{"listed=7&attended=8", "listed=7&walk_in=x"} {
		if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/attendance", form, true); response.Code != http.StatusUnprocessableEntity || participation.lastMarks != nil {
			t.Errorf("%q = %d, marks %+v", form, response.Code, participation.lastMarks)
		}
	}
	participation.writeErr = service.ErrAttendanceTooEarly
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/attendance", "listed=7&attended=7", true); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "only after the event ends") {
		t.Fatalf("too early = %d", response.Code)
	}
}

func TestParticipationRoutesOutsiderGetsNotFound(t *testing.T) {
	dependencies, _, participation := participationDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, target := range []string{"/en/groups/3/events/5/participation", "/en/groups/3/events/5/attendance", "/hu/groups/3/events/5/participation"} {
		response := serveGroups(t, dependencies, http.MethodPost, target, "user_id=7&status=signed_up&listed=7&attended=7", true)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
			t.Errorf("%s = %d, want 404", target, response.Code)
		}
	}
	if participation.calls != 0 {
		t.Fatal("outsider requests reached the use cases")
	}
	dependencies, _, _ = participationDependencies(t, playerOfTeam(4))
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/abc/participation", "user_id=7&status=signed_up", true); response.Code != http.StatusNotFound {
		t.Fatalf("bad event id = %d", response.Code)
	}
}

func TestEventDeleteWithParticipantsRefused(t *testing.T) {
	dependencies, events, _ := participationDependencies(t, coachOfTeam(4))
	events.writeErr = service.ErrEventHasParticipants
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/events/5/delete", "version=2", true)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Cancel it instead.") {
		t.Fatalf("delete with participants = %d", response.Code)
	}
}
