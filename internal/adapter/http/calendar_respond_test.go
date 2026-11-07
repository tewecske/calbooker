package httpadapter

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

const calendarAgendaPath = "/en/groups/lions/calendar?week=2026-10-19&view=agenda"

func calendarRespondFixture(t *testing.T, access *service.GroupAccess, choices ...service.CalendarChoice) (Dependencies, *participationUseCaseStub) {
	t.Helper()
	dependencies, _ := calendarDependencies(t, access)
	participation := &participationUseCaseStub{choices: choices}
	dependencies.Participation = participation
	return dependencies, participation
}

func respondForm(user int64, status, returnTo string) string {
	values := url.Values{"user_id": {strconv.FormatInt(user, 10)}, "status": {status}}
	if returnTo != "" {
		values.Set("return_to", returnTo)
	}
	return values.Encode()
}

func TestCalendarAgendaShowsAnswerButtons(t *testing.T) {
	anna := "Anna"
	choices := []service.CalendarChoice{
		{EventID: 2, UserID: 7, Self: true, Choices: []string{service.ParticipantSignedUp, service.ParticipantDeclined}},
		{EventID: 6, UserID: 7, Self: true, Current: service.ParticipantSignedUp, Choices: []string{service.ParticipantDeclined}},
		{EventID: 2, UserID: 20, DisplayName: &anna, Choices: []string{service.ParticipantSignedUp, service.ParticipantDeclined}},
	}
	dependencies, _ := calendarRespondFixture(t, playerOfTeam(2), choices...)
	body := serveGroups(t, dependencies, http.MethodGet, calendarAgendaPath, "", false).Body.String()
	for _, want := range []string{
		`/events/2/participation"`,
		`data-calendar-respond="7"`, `data-calendar-respond="20"`,
		`aria-label="I&#39;ll be there – `, `aria-label="Can&#39;t come – Anna"`,
		`name="return_to" value="/en/groups/lions/calendar?week=2026-10-19&amp;view=agenda"`,
		`data-current="true"`, `aria-pressed="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("agenda lacks %q", want)
		}
	}
	if strings.Count(body, `data-current="true"`) != 2 { // event 6 spans two days
		t.Errorf("marked buttons = %d, want 2", strings.Count(body, `data-current="true"`))
	}
}

func TestCalendarAgendaHidesAnswerButtonsWithoutChoices(t *testing.T) {
	dependencies, _ := calendarRespondFixture(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, calendarAgendaPath, "", false).Body.String()
	if strings.Contains(body, "data-calendar-respond") || strings.Contains(body, `name="return_to"`) {
		t.Error("agenda shows answer buttons without choices")
	}
	if !strings.Contains(body, `data-testid="calendar-day-header"`) {
		t.Error("agenda lacks the day header strip")
	}
}

func TestCalendarAnswerReturnsToCalendar(t *testing.T) {
	back := "/en/groups/lions/calendar?week=2026-10-19&view=agenda&team=2"
	dependencies, participation := calendarRespondFixture(t, playerOfTeam(2))

	t.Run("without JavaScript redirects to the same calendar", func(t *testing.T) {
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/2/participation", respondForm(7, "signed_up", back), false)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != back {
			t.Fatalf("answer = %d, Location %q", response.Code, response.Header().Get("Location"))
		}
		if participation.lastEvent != 2 || participation.lastStatus != service.ParticipantSignedUp {
			t.Errorf("respond args = %d %q", participation.lastEvent, participation.lastStatus)
		}
	})
	t.Run("HTMX swaps the calendar with the alerts", func(t *testing.T) {
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/2/participation", respondForm(7, "declined", back), true)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, `data-calendar`) || !strings.Contains(body, "Answer saved.") || !strings.Contains(body, `id="alerts"`) {
			t.Fatalf("answer = %d", response.Code)
		}
		if !strings.Contains(body, `name="week" value="2026-10-19"`) || !strings.Contains(body, `value="agenda"`) {
			t.Error("calendar lost the week or the view")
		}
	})
	t.Run("a refused answer shows the error on the calendar", func(t *testing.T) {
		participation.writeErr = service.ErrSignupDeadlinePassed
		defer func() { participation.writeErr = nil }()
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/2/participation", respondForm(7, "signed_up", back), true)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `data-calendar`) {
			t.Fatalf("refused answer = %d", response.Code)
		}
	})
	t.Run("unsafe return paths are ignored", func(t *testing.T) {
		for _, bad := range []string{
			"https://evil.example/en/groups/lions/calendar",
			"//evil.example/en/groups/lions/calendar",
			"/\\evil.example",
			"/en/groups/other/calendar",
			"/en/groups/lions/settings",
			"/en/groups/lions/teams/a/b/calendar",
			"en/groups/lions/calendar",
		} {
			response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/2/participation", respondForm(7, "signed_up", bad), false)
			if response.Code == http.StatusSeeOther {
				t.Errorf("return_to %q redirected to %q", bad, response.Header().Get("Location"))
			}
		}
	})
}
