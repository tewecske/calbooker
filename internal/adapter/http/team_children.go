package httpadapter

import (
	"net/http"
	"strconv"

	"github.com/tewecske/calbooker/internal/service"
)

// TeamChildrenView is the "Add my child" card of a team page: shown to
// parents of the team, with the form for a new child and the account's
// existing child profiles.
type TeamChildrenView struct {
	AddURL    string
	Name      string
	BirthYear string
	NameError string
	YearError string
	// Prompt is true right after joining with a parent invite.
	Prompt   bool
	Children []TeamChildRowView
}

// TeamChildRowView is one child profile of the account.
type TeamChildRowView struct {
	UserID    int64
	Label     string
	BirthYear string
	Version   int64
	OnTeam    bool
	EditURL   string
	JoinURL   string
	LeaveURL  string
}

// registerTeamChildRoutes registers the parent's child routes of a team page.
func registerTeamChildRoutes(mux *http.ServeMux, prefix string, groups *groupHandler) {
	mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/children", groups.addTeamChild)
	mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/children/{childID}", groups.updateTeamChild)
	mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/children/{childID}/join", groups.joinTeamChild)
	mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/children/{childID}/leave", groups.leaveTeamChild)
}

// teamChildRequest resolves the team (404 unless visible) and the profile
// use cases.
func (h *groupHandler) teamChildRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, ProfileUseCases, bool) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, nil, false
	}
	children := h.dependencies.Profiles
	if children == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, 0, nil, false
	}
	_ = request.ParseForm()
	return current, teamID, children, true
}

func (h *groupHandler) addTeamChild(writer http.ResponseWriter, request *http.Request) {
	current, teamID, children, ok := h.teamChildRequest(writer, request)
	if !ok {
		return
	}
	input, form := profileForm(request)
	form.Values["child_form"] = "1"
	if _, err := children.AddChildToTeam(request.Context(), current.actor(), teamID, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, err, h.profileFormErrors(current, form, err))
		return
	}
	h.renderTeamChildAfterWrite(writer, request, current, teamID, "teams.children.added")
}

func (h *groupHandler) updateTeamChild(writer http.ResponseWriter, request *http.Request) {
	current, teamID, _, ok := h.teamChildRequest(writer, request)
	if !ok {
		return
	}
	childID, err := pathPositiveID(request, "childID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	input, _ := profileForm(request)
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if _, err := h.dependencies.Profiles.UpdateProfile(request.Context(), current.actor(), childID, version, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, err, FormData{})
		return
	}
	h.renderTeamChildAfterWrite(writer, request, current, teamID, "teams.children.saved")
}

func (h *groupHandler) joinTeamChild(writer http.ResponseWriter, request *http.Request) {
	h.changeTeamChild(writer, request, true)
}

func (h *groupHandler) leaveTeamChild(writer http.ResponseWriter, request *http.Request) {
	h.changeTeamChild(writer, request, false)
}

func (h *groupHandler) changeTeamChild(writer http.ResponseWriter, request *http.Request, join bool) {
	current, teamID, children, ok := h.teamChildRequest(writer, request)
	if !ok {
		return
	}
	childID, err := pathPositiveID(request, "childID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	message := "teams.children.joined"
	if join {
		err = children.JoinChildToTeam(request.Context(), current.actor(), teamID, childID)
	} else {
		message = "teams.children.left"
		err = children.RemoveChildFromTeam(request.Context(), current.actor(), teamID, childID)
	}
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, err, FormData{})
		return
	}
	h.renderTeamChildAfterWrite(writer, request, current, teamID, message)
}

// renderTeamChildAfterWrite rebuilds the caller's access (a new profile is
// linked to them, which the request's access was built without) and renders
// the team page.
func (h *groupHandler) renderTeamChildAfterWrite(writer http.ResponseWriter, request *http.Request, current groupRequest, teamID int64, message string) {
	access, err := h.dependencies.GroupAccess.For(request.Context(), current.user.ID, current.access.GroupID())
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	current.access = access
	h.renderSection(writer, request, current, groupSectionTeam, teamID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

// loadTeamChildren fills the "Add my child" card. It stays nil for accounts
// that are not parents of the team and for archived teams.
func (h *groupHandler) loadTeamChildren(writer http.ResponseWriter, request *http.Request, current groupRequest, team *TeamDetailView, base string, form FormData) bool {
	access := current.access
	if h.dependencies.Profiles == nil || team.Archived || !access.CanAddChildToTeam(team.ID) {
		return true
	}
	overview, err := h.dependencies.Profiles.Children(request.Context(), access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	card := &TeamChildrenView{
		AddURL:    base + "/children",
		Prompt:    request.URL.Query().Get("add_child") == "1",
		Name:      form.Value("display_name"),
		BirthYear: form.Value("birth_year"),
		NameError: form.Error("display_name"),
		YearError: form.Error("birth_year"),
	}
	for _, child := range overview.Children {
		if child.Profile == nil || !access.IsGuardianOf(child.Person.UserID) {
			continue
		}
		row := TeamChildRowView{
			UserID:    child.Person.UserID,
			Label:     h.memberLabel(current.language, child.Person.UserID, child.Person.DisplayName, child.Person.Username, nil),
			BirthYear: strconv.Itoa(child.Profile.BirthYear),
			Version:   child.Profile.Version,
		}
		for _, onTeam := range child.Teams {
			if onTeam.ID == team.ID {
				row.OnTeam = true
			}
		}
		childBase := base + "/children/" + strconv.FormatInt(row.UserID, 10)
		row.EditURL, row.JoinURL, row.LeaveURL = childBase, childBase+"/join", childBase+"/leave"
		card.Children = append(card.Children, row)
	}
	team.AddChild = card
	return true
}
