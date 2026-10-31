package httpadapter

import (
	"net/http"
	"strconv"

	"github.com/tewecske/calbooker/internal/service"
)

// Team page tabs. The roster is the team page itself; Events and Calendar
// reuse the group's event list and weekly calendar with the team fixed.
// The Invites tab (#47) shows for staff and the team's coaches.
const (
	teamTabRoster   = "roster"
	teamTabEvents   = "events"
	teamTabCalendar = "calendar"
	teamTabInvites  = "invites"
)

// TeamTabsView is the submenu of a team page.
type TeamTabsView struct {
	Name     string
	TeamsURL string
	Tabs     []TeamTabView
}

// TeamTabView is one tab link of the team page.
type TeamTabView struct {
	ID      string
	Label   string
	URL     string
	Current bool
}

// teamTabs builds the team submenu; current is one of the teamTab constants.
func (h *groupHandler) teamTabs(current groupRequest, team service.Team, active string) *TeamTabsView {
	language := current.language
	base := h.groupPath(language, current.slug, "/teams/"+team.Slug)
	tabs := []TeamTabView{
		{ID: teamTabRoster, Label: h.t(language, "teams.tab.roster"), URL: base},
		{ID: teamTabEvents, Label: h.t(language, "teams.tab.events"), URL: base + "/events"},
		{ID: teamTabCalendar, Label: h.t(language, "teams.tab.calendar"), URL: base + "/calendar"},
	}
	if h.dependencies.Invites != nil && !team.Archived() && current.access.CanCreateInvite(service.GroupRolePlayer, team.ID) {
		tabs = append(tabs, TeamTabView{ID: teamTabInvites, Label: h.t(language, "teams.tab.invites"), URL: base + "/invites"})
	}
	for index := range tabs {
		tabs[index].Current = tabs[index].ID == active
	}
	return &TeamTabsView{Name: team.Name, TeamsURL: h.groupPath(language, current.slug, "/teams"), Tabs: tabs}
}

func (h *groupHandler) teamEvents(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamEventsRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionTeamEvents, teamID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) teamCalendar(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamEventsRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionTeamCalendar, teamID, FormData{}, nil, http.StatusOK)
}

// createTeamEvent creates an event from the team's Events tab. The service
// applies the same rules as the group's create form.
func (h *groupHandler) createTeamEvent(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamEventsRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := eventForm(request)
	event, _, err := h.dependencies.Events.Create(request.Context(), current.actor(), input)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeamEvents, teamID, err, h.eventFormErrors(current.language, form, err))
		return
	}
	team, err := h.dependencies.Teams.ByID(request.Context(), current.access, teamID)
	if err != nil {
		h.redirectLocalized(writer, request, current.language, "/groups/"+current.slug+"/events/"+strconv.FormatInt(event.ID, 10))
		return
	}
	h.redirectLocalized(writer, request, current.language, "/groups/"+current.slug+"/teams/"+team.Slug+"/events/"+strconv.FormatInt(event.ID, 10))
}

// teamEventsRequest authorizes a team page tab that lists events: the team
// must be visible to the account (otherwise 404).
func (h *groupHandler) teamEventsRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	if h.dependencies.Events == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, 0, false
	}
	return current, teamID, true
}

// loadTeamTab fills the Events or Calendar tab of a team.
func (h *groupHandler) loadTeamTab(writer http.ResponseWriter, request *http.Request, current groupRequest, group service.Group, section string, teamID int64, form FormData, view *GroupDetailView) bool {
	team, err := h.dependencies.Teams.ByID(request.Context(), current.access, teamID)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	if section == groupSectionTeamInvites {
		view.TeamTabs = h.teamTabs(current, team, teamTabInvites)
		return h.loadTeamInvitesTab(writer, request, current, team, form, view)
	}
	if section == groupSectionTeamCalendar {
		view.TeamTabs = h.teamTabs(current, team, teamTabCalendar)
		return h.loadCalendarScope(writer, request, current, group, teamID, view)
	}
	view.TeamTabs = h.teamTabs(current, team, teamTabEvents)
	return h.loadEventList(writer, request, current, group, teamID, form, view)
}
