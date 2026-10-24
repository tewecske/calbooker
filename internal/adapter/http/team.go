package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/service"
)

// TeamUseCases aggregates the team operations consumed by HTTP handlers.
type TeamUseCases interface {
	List(ctx context.Context, access *service.GroupAccess) ([]service.TeamSummary, error)
	Create(ctx context.Context, actor service.GroupActor, input service.TeamInput) (service.Team, error)
	Detail(ctx context.Context, access *service.GroupAccess, teamID int64) (service.TeamDetail, error)
	Update(ctx context.Context, actor service.GroupActor, teamID, expectedVersion int64, input service.TeamInput) (service.Team, error)
	SetArchived(ctx context.Context, actor service.GroupActor, teamID, expectedVersion int64, archived bool) (service.Team, error)
	AddToRoster(ctx context.Context, actor service.GroupActor, teamID, userID int64, role string) error
	RemoveFromRoster(ctx context.Context, actor service.GroupActor, teamID, userID int64, role string) error
}

// TeamsView is a group's team list. CreateForm is nil unless the account
// may create teams.
type TeamsView struct {
	CreateForm *TeamFormView
	Active     []TeamRowView
	Archived   []TeamRowView
}

// TeamFormView holds the values and errors of the team create or edit form.
type TeamFormView struct {
	L                map[string]string
	Prefix           string
	ActionURL        string
	Name             string
	BirthYearFrom    string
	BirthYearTo      string
	Description      string
	NameError        string
	YearsError       string
	DescriptionError string
}

// RosterListView is the players or parents of a team with the removal form
// the account may use.
type RosterListView struct {
	Rows        []RosterRowView
	CanManage   bool
	RemoveURL   string
	RemoveLabel string
	EmptyLabel  string
	// ParentOfLabel introduces the children of a parent row.
	ParentOfLabel string
	CSRFToken     string
}

// TeamRowView is one team list row.
type TeamRowView struct {
	ID          int64
	Name        string
	Years       string
	Description string
	URL         string
	Players     int
	Parents     int
}

// TeamDetailView is one team with its roster and the forms the account may
// use.
type TeamDetailView struct {
	ID              int64
	Name            string
	Years           string
	Description     string
	Archived        bool
	Version         int64
	CanEdit         bool
	CanManageRoster bool
	ShowEmails      bool
	TeamsURL        string
	EditURL         string
	ArchiveURL      string
	RosterAddURL    string
	RosterRemoveURL string
	EditForm        *TeamFormView
	PlayerList      RosterListView
	ParentList      RosterListView
	Candidates      []RosterCandidateView
}

// RosterRowView is one player or parent on a team.
type RosterRowView struct {
	UserID int64
	Label  string
	Email  string
	Role   string
	// Linked is true for a parent shown only through a guardian link; it
	// is removed by unlinking, not from the roster.
	Linked bool
	// ParentOf names the players on this team the parent is a guardian of.
	ParentOf string
}

// RosterCandidateView is one member and role that can be added to the roster;
// Value is "<user ID>:<role>".
type RosterCandidateView struct {
	Value string
	Label string
}

// registerTeamRoutes registers each group's team list, team pages, and
// roster routes.
func registerTeamRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/teams", groups.teams)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams", groups.createTeam)
		mux.HandleFunc("GET "+prefix+"/groups/{id}/teams/{teamID}", groups.team)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams/{teamID}", groups.updateTeam)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams/{teamID}/archive", groups.archiveTeam)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams/{teamID}/roster/add", groups.addToRoster)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams/{teamID}/roster/remove", groups.removeFromRoster)
	}
}

func (h *groupHandler) teams(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.teamRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionTeams, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createTeam(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.teamRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := teamForm(request)
	team, err := h.dependencies.Teams.Create(request.Context(), current.actor(), input)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeams, 0, err, h.teamFormErrors(current, form, err))
		return
	}
	h.redirectLocalized(writer, request, current.language, "/groups/"+strconv.FormatInt(current.access.GroupID(), 10)+"/teams/"+strconv.FormatInt(team.ID, 10))
}

func (h *groupHandler) team(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionTeam, teamID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) updateTeam(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := teamForm(request)
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, service.ErrOptimisticLockConflict, form)
		return
	}
	if _, err := h.dependencies.Teams.Update(request.Context(), current.actor(), teamID, version, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, err, h.teamFormErrors(current, form, err))
		return
	}
	h.renderSection(writer, request, current, groupSectionTeam, teamID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "teams.saved")}}, http.StatusOK)
}

func (h *groupHandler) archiveTeam(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	archived := request.PostFormValue("archived") == "true"
	if _, err := h.dependencies.Teams.SetArchived(request.Context(), current.actor(), teamID, version, archived); err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, err, FormData{})
		return
	}
	message := "teams.restored"
	if archived {
		message = "teams.archived_done"
	}
	h.renderSection(writer, request, current, groupSectionTeam, teamID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

func (h *groupHandler) addToRoster(writer http.ResponseWriter, request *http.Request) {
	h.changeRoster(writer, request, true)
}

func (h *groupHandler) removeFromRoster(writer http.ResponseWriter, request *http.Request) {
	h.changeRoster(writer, request, false)
}

func (h *groupHandler) changeRoster(writer http.ResponseWriter, request *http.Request, add bool) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	userID, role, ok := parseRosterEntry(request.PostFormValue("entry"))
	if !ok {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, service.ErrInvalidRosterRole, FormData{})
		return
	}
	var err error
	message := "teams.roster.added"
	if add {
		err = h.dependencies.Teams.AddToRoster(request.Context(), current.actor(), teamID, userID, role)
	} else {
		message = "teams.roster.removed"
		err = h.dependencies.Teams.RemoveFromRoster(request.Context(), current.actor(), teamID, userID, role)
	}
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionTeam, teamID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

func (h *groupHandler) teamRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Teams == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) teamPathRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.teamRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	teamID, err := pathPositiveID(request, "teamID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	return current, teamID, true
}

func (h *groupHandler) loadTeams(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Teams == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	teams, err := h.dependencies.Teams.List(request.Context(), current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	list := &TeamsView{}
	if current.access.CanCreateTeam() {
		list.CreateForm = h.teamFormView(current, "team-new", h.groupPath(current.language, groupID, "/teams"), nil, form)
	}
	for _, summary := range teams {
		row := TeamRowView{
			ID:      summary.Team.ID,
			Name:    summary.Team.Name,
			Years:   teamYears(summary.Team),
			URL:     h.groupPath(current.language, groupID, "/teams/"+strconv.FormatInt(summary.Team.ID, 10)),
			Players: summary.Players,
			Parents: summary.Parents,
		}
		if summary.Team.Description != nil {
			row.Description = *summary.Team.Description
		}
		if summary.Team.Archived() {
			list.Archived = append(list.Archived, row)
		} else {
			list.Active = append(list.Active, row)
		}
	}
	view.Teams = list
	return true
}

func (h *groupHandler) loadTeam(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID, teamID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Teams == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	detail, err := h.dependencies.Teams.Detail(request.Context(), current.access, teamID)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	language := current.language
	base := h.groupPath(language, groupID, "/teams/"+strconv.FormatInt(teamID, 10))
	team := &TeamDetailView{
		ID:              detail.Team.ID,
		Name:            detail.Team.Name,
		Years:           teamYears(detail.Team),
		Archived:        detail.Team.Archived(),
		Version:         detail.Team.Version,
		CanEdit:         current.access.CanEditTeam(teamID),
		CanManageRoster: current.access.CanManageRoster(teamID),
		ShowEmails:      current.access.CanViewTeamEmails(teamID),
		TeamsURL:        h.groupPath(language, groupID, "/teams"),
		EditURL:         base,
		ArchiveURL:      base + "/archive",
		RosterAddURL:    base + "/roster/add",
		RosterRemoveURL: base + "/roster/remove",
	}
	if detail.Team.Description != nil {
		team.Description = *detail.Team.Description
	}
	if team.CanEdit {
		team.EditForm = h.teamFormView(current, "team-edit", base, &detail.Team, form)
	}
	csrf := csrfToken(request)
	team.PlayerList = RosterListView{CanManage: team.CanManageRoster, RemoveURL: team.RosterRemoveURL, RemoveLabel: h.t(language, "teams.roster.remove"), EmptyLabel: h.t(language, "teams.roster.no_players"), CSRFToken: csrf}
	team.ParentList = RosterListView{CanManage: team.CanManageRoster, RemoveURL: team.RosterRemoveURL, RemoveLabel: h.t(language, "teams.roster.remove"), EmptyLabel: h.t(language, "teams.roster.no_parents"), ParentOfLabel: h.t(language, "teams.roster.parent_of"), CSRFToken: csrf}
	onRoster := make(map[string]bool, len(detail.Roster))
	playerLabels := make(map[int64]string)
	for _, entry := range detail.Roster {
		if entry.Role == service.GroupRolePlayer {
			playerLabels[entry.UserID] = h.memberLabel(language, entry.UserID, entry.DisplayName, entry.Username, entry.Email)
		}
	}
	for _, entry := range detail.Roster {
		row := RosterRowView{UserID: entry.UserID, Label: h.memberLabel(language, entry.UserID, entry.DisplayName, entry.Username, entry.Email), Role: entry.Role, Linked: entry.Linked}
		if entry.Email != nil {
			row.Email = *entry.Email
		}
		children := make([]string, 0, len(entry.ChildUserIDs))
		for _, childID := range entry.ChildUserIDs {
			if label, ok := playerLabels[childID]; ok {
				children = append(children, label)
			}
		}
		row.ParentOf = strings.Join(children, ", ")
		if !entry.Linked {
			onRoster[rosterValue(entry.UserID, entry.Role)] = true
		}
		if entry.Role == service.GroupRoleParent {
			team.ParentList.Rows = append(team.ParentList.Rows, row)
		} else {
			team.PlayerList.Rows = append(team.PlayerList.Rows, row)
		}
	}
	for _, member := range detail.Candidates {
		for _, role := range service.RosterRoles() {
			value := rosterValue(member.UserID, role)
			if !member.HasRole(role) || onRoster[value] {
				continue
			}
			label := h.memberLabel(language, member.UserID, member.DisplayName, member.Username, member.Email) + " (" + h.roleLabel(language, role) + ")"
			team.Candidates = append(team.Candidates, RosterCandidateView{Value: value, Label: label})
		}
	}
	view.Team = team
	return true
}

// teamFormView fills the team form from submitted values after a failed
// write, else from the stored team (nil for a new team).
func (h *groupHandler) teamFormView(current groupRequest, prefix, actionURL string, team *service.Team, form FormData) *TeamFormView {
	view := &TeamFormView{L: h.labels[current.language], Prefix: prefix, ActionURL: actionURL}
	switch {
	case form.Submitted:
		view.Name = form.Value("name")
		view.BirthYearFrom = form.Value("birth_year_from")
		view.BirthYearTo = form.Value("birth_year_to")
		view.Description = form.Value("description")
		view.NameError = form.Error("name")
		view.YearsError = form.Error("birth_year_from")
		view.DescriptionError = form.Error("description")
	case team != nil:
		view.Name = team.Name
		view.BirthYearFrom = strconv.Itoa(team.BirthYearFrom)
		view.BirthYearTo = strconv.Itoa(team.BirthYearTo)
		if team.Description != nil {
			view.Description = *team.Description
		}
	}
	return view
}

// teamFormErrors attaches the field error for a team validation failure.
func (h *groupHandler) teamFormErrors(current groupRequest, form FormData, err error) FormData {
	switch {
	case errors.Is(err, service.ErrInvalidTeamName):
		form.Errors = []FieldError{{Field: "name", Message: h.t(current.language, "teams.error.invalid_name")}}
	case errors.Is(err, service.ErrInvalidTeamBirthYears):
		form.Errors = []FieldError{{Field: "birth_year_from", Message: h.t(current.language, "teams.error.invalid_birth_years")}}
	case errors.Is(err, service.ErrInvalidTeamDescription):
		form.Errors = []FieldError{{Field: "description", Message: h.t(current.language, "teams.error.invalid_description")}}
	}
	return form
}

// teamForm reads team input. Unparseable years become 0, which validation
// refuses.
func teamForm(request *http.Request) (service.TeamInput, FormData) {
	values := map[string]string{
		"name":            request.PostFormValue("name"),
		"birth_year_from": strings.TrimSpace(request.PostFormValue("birth_year_from")),
		"birth_year_to":   strings.TrimSpace(request.PostFormValue("birth_year_to")),
		"description":     request.PostFormValue("description"),
	}
	from, _ := strconv.Atoi(values["birth_year_from"])
	to, _ := strconv.Atoi(values["birth_year_to"])
	input := service.TeamInput{Name: values["name"], BirthYearFrom: from, BirthYearTo: to, Description: values["description"]}
	return input, FormData{Submitted: true, Values: values}
}

func parseRosterEntry(raw string) (int64, string, bool) {
	idText, role, found := strings.Cut(strings.TrimSpace(raw), ":")
	if !found {
		return 0, "", false
	}
	userID, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || userID <= 0 || (role != service.GroupRolePlayer && role != service.GroupRoleParent) {
		return 0, "", false
	}
	return userID, role, true
}

func rosterValue(userID int64, role string) string {
	return strconv.FormatInt(userID, 10) + ":" + role
}

func teamYears(team service.Team) string {
	if team.BirthYearFrom == team.BirthYearTo {
		return strconv.Itoa(team.BirthYearFrom)
	}
	return strconv.Itoa(team.BirthYearFrom) + "–" + strconv.Itoa(team.BirthYearTo)
}
