package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/service"
)

// ProfileUseCases aggregates the child profile and guardian operations
// consumed by HTTP handlers.
type ProfileUseCases interface {
	Children(ctx context.Context, access *service.GroupAccess) (service.ChildrenOverview, error)
	CreateProfile(ctx context.Context, actor service.GroupActor, input service.ProfileInput) (service.ChildProfile, error)
	UpdateProfile(ctx context.Context, actor service.GroupActor, userID, expectedVersion int64, input service.ProfileInput) (service.ChildProfile, error)
	DeleteProfile(ctx context.Context, actor service.GroupActor, userID, expectedVersion int64) error
	AddGuardian(ctx context.Context, actor service.GroupActor, parentUserID, childUserID int64) error
	RemoveGuardian(ctx context.Context, actor service.GroupActor, parentUserID, childUserID int64) error
}

// ChildrenView is the children page: the children the account sees, the
// profile form, and for staff the guardian link form.
type ChildrenView struct {
	CanCreate      bool
	IsStaff        bool
	CreateURL      string
	LinkURL        string
	UnlinkURL      string
	Name           string
	BirthYear      string
	NameError      string
	YearError      string
	GuardianOption []SelectOptionView
	TeamOptions    []SelectOptionView
	ParentOptions  []SelectOptionView
	ChildOptions   []SelectOptionView
	Children       []ChildRowView
}

// ChildRowView is one child with guardians and teams.
type ChildRowView struct {
	UserID    int64
	Label     string
	IsProfile bool
	BirthYear string
	Version   int64
	Teams     []string
	Guardians []GuardianRowView
	CanEdit   bool
	EditURL   string
	DeleteURL string
}

// GuardianRowView is one guardian of a child.
type GuardianRowView struct {
	UserID int64
	Label  string
}

// registerChildRoutes registers each group's children page, profile writes,
// and guardian links.
func registerChildRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{group}/children", groups.children)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/children", groups.createProfile)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/children/{childID}", groups.updateProfile)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/children/{childID}/delete", groups.deleteProfile)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/guardians/add", groups.addGuardian)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/guardians/remove", groups.removeGuardian)
	}
}

func (h *groupHandler) children(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.childRequest(writer, request)
	if !ok {
		return
	}
	if !current.access.CanViewChildren() {
		h.renderAccessDenied(writer, request, current.language)
		return
	}
	h.renderSection(writer, request, current, groupSectionChildren, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createProfile(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.childRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := profileForm(request)
	for _, field := range []struct {
		name   string
		target *int64
	}{{"guardian_id", &input.GuardianUserID}, {"team_id", &input.TeamID}} {
		raw := strings.TrimSpace(request.PostFormValue(field.name))
		form.Values[field.name] = raw
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			h.renderWriteError(writer, request, current, groupSectionChildren, 0, service.ErrInvalidGuardian, form)
			return
		}
		*field.target = id
	}
	if _, err := h.dependencies.Profiles.CreateProfile(request.Context(), current.actor(), input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionChildren, 0, err, h.profileFormErrors(current, form, err))
		return
	}
	h.renderChildrenAfterWrite(writer, request, current, []Alert{{Level: "success", Message: h.t(current.language, "children.created")}})
}

func (h *groupHandler) updateProfile(writer http.ResponseWriter, request *http.Request) {
	current, childID, ok := h.childPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, _ := profileForm(request)
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionChildren, 0, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if _, err := h.dependencies.Profiles.UpdateProfile(request.Context(), current.actor(), childID, version, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionChildren, 0, err, FormData{})
		return
	}
	h.renderChildrenAfterWrite(writer, request, current, []Alert{{Level: "success", Message: h.t(current.language, "children.saved")}})
}

func (h *groupHandler) deleteProfile(writer http.ResponseWriter, request *http.Request) {
	current, childID, ok := h.childPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionChildren, 0, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if err := h.dependencies.Profiles.DeleteProfile(request.Context(), current.actor(), childID, version); err != nil {
		h.renderWriteError(writer, request, current, groupSectionChildren, 0, err, FormData{})
		return
	}
	h.renderChildrenAfterWrite(writer, request, current, []Alert{{Level: "success", Message: h.t(current.language, "children.deleted")}})
}

func (h *groupHandler) addGuardian(writer http.ResponseWriter, request *http.Request) {
	h.changeGuardian(writer, request, true)
}

func (h *groupHandler) removeGuardian(writer http.ResponseWriter, request *http.Request) {
	h.changeGuardian(writer, request, false)
}

func (h *groupHandler) changeGuardian(writer http.ResponseWriter, request *http.Request, add bool) {
	current, ok := h.childRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	// The Members page posts return=members so it re-renders itself.
	section := groupSectionChildren
	if request.PostFormValue("return") == groupSectionMembers {
		section = groupSectionMembers
	}
	parentID, parentErr := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("parent_id")), 10, 64)
	childID, childErr := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("child_id")), 10, 64)
	if parentErr != nil || childErr != nil || parentID <= 0 || childID <= 0 {
		h.renderWriteError(writer, request, current, section, 0, service.ErrInvalidGuardian, FormData{})
		return
	}
	var err error
	message := "children.linked"
	if add {
		err = h.dependencies.Profiles.AddGuardian(request.Context(), current.actor(), parentID, childID)
	} else {
		message = "children.unlinked"
		err = h.dependencies.Profiles.RemoveGuardian(request.Context(), current.actor(), parentID, childID)
	}
	if err != nil {
		h.renderWriteError(writer, request, current, section, 0, err, FormData{})
		return
	}
	h.renderGuardianAfterWrite(writer, request, current, section, []Alert{{Level: "success", Message: h.t(current.language, message)}})
}

func (h *groupHandler) childRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Profiles == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) childPathRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.childRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	childID, err := pathPositiveID(request, "childID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	return current, childID, true
}

func (h *groupHandler) loadChildren(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Profiles == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	overview, err := h.dependencies.Profiles.Children(request.Context(), current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	language := current.language
	access := current.access
	children := &ChildrenView{
		CanCreate: access.CanCreateProfile(),
		IsStaff:   access.CanManageGuardians(),
		CreateURL: h.groupPath(language, current.slug, "/children"),
		LinkURL:   h.groupPath(language, current.slug, "/guardians/add"),
		UnlinkURL: h.groupPath(language, current.slug, "/guardians/remove"),
		Name:      form.Value("display_name"),
		BirthYear: form.Value("birth_year"),
		NameError: form.Error("display_name"),
		YearError: form.Error("birth_year"),
	}
	if children.IsStaff {
		selectedGuardian := form.Value("guardian_id")
		if !form.Submitted && access.HasRole(service.GroupRoleParent) {
			selectedGuardian = strconv.FormatInt(access.UserID(), 10)
		}
		children.GuardianOption = append(children.GuardianOption, SelectOptionView{Value: "", Label: h.t(language, "children.no_guardian"), Selected: selectedGuardian == ""})
		for _, member := range overview.Members {
			value := strconv.FormatInt(member.UserID, 10)
			label := h.memberLabel(language, member.UserID, member.DisplayName, member.Username, member.Email)
			if member.HasRole(service.GroupRoleParent) {
				children.GuardianOption = append(children.GuardianOption, SelectOptionView{Value: value, Label: label, Selected: value == selectedGuardian})
				children.ParentOptions = append(children.ParentOptions, SelectOptionView{Value: value, Label: label})
			}
			children.ChildOptions = append(children.ChildOptions, SelectOptionView{Value: value, Label: label})
		}
		selectedTeam := form.Value("team_id")
		children.TeamOptions = append(children.TeamOptions, SelectOptionView{Value: "", Label: h.t(language, "children.no_team"), Selected: selectedTeam == ""})
		for _, summary := range overview.Teams {
			if summary.Team.Archived() {
				continue
			}
			value := strconv.FormatInt(summary.Team.ID, 10)
			children.TeamOptions = append(children.TeamOptions, SelectOptionView{Value: value, Label: summary.Team.Name + " (" + teamYears(summary.Team) + ")", Selected: value == selectedTeam})
		}
	}
	for _, child := range overview.Children {
		base := h.groupPath(language, current.slug, "/children/"+strconv.FormatInt(child.Person.UserID, 10))
		row := ChildRowView{
			UserID:    child.Person.UserID,
			Label:     h.memberLabel(language, child.Person.UserID, child.Person.DisplayName, child.Person.Username, nil),
			IsProfile: child.Profile != nil,
			CanEdit:   child.Profile != nil && access.CanEditProfile(child.Person.UserID),
			EditURL:   base,
			DeleteURL: base + "/delete",
		}
		if child.Profile != nil {
			row.BirthYear = strconv.Itoa(child.Profile.BirthYear)
			row.Version = child.Profile.Version
		}
		for _, team := range child.Teams {
			row.Teams = append(row.Teams, team.Name)
		}
		for _, guardian := range child.Guardians {
			row.Guardians = append(row.Guardians, GuardianRowView{
				UserID: guardian.UserID,
				Label:  h.memberLabel(language, guardian.UserID, guardian.DisplayName, guardian.Username, nil),
			})
		}
		children.Children = append(children.Children, row)
	}
	view.Children = children
	return true
}

// renderChildrenAfterWrite rebuilds the caller's access before rendering:
// a write here can change the account's own children (a parent's new
// profile is linked to them), which the request's access was built without.
func (h *groupHandler) renderChildrenAfterWrite(writer http.ResponseWriter, request *http.Request, current groupRequest, alerts []Alert) {
	h.renderGuardianAfterWrite(writer, request, current, groupSectionChildren, alerts)
}

// renderGuardianAfterWrite renders the section a guardian write came from.
func (h *groupHandler) renderGuardianAfterWrite(writer http.ResponseWriter, request *http.Request, current groupRequest, section string, alerts []Alert) {
	access, err := h.dependencies.GroupAccess.For(request.Context(), current.user.ID, current.access.GroupID())
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	current.access = access
	h.renderSection(writer, request, current, section, 0, FormData{}, alerts, http.StatusOK)
}

// profileForm reads the profile name and birth year. An unparseable year
// becomes 0, which validation refuses.
func profileForm(request *http.Request) (service.ProfileInput, FormData) {
	values := map[string]string{
		"display_name": request.PostFormValue("display_name"),
		"birth_year":   strings.TrimSpace(request.PostFormValue("birth_year")),
	}
	year, _ := strconv.Atoi(values["birth_year"])
	return service.ProfileInput{DisplayName: values["display_name"], BirthYear: year}, FormData{Submitted: true, Values: values}
}

// profileFormErrors attaches the field error of a failed profile write.
func (h *groupHandler) profileFormErrors(current groupRequest, form FormData, err error) FormData {
	switch {
	case errors.Is(err, service.ErrInvalidProfileName):
		form.Errors = []FieldError{{Field: "display_name", Message: h.t(current.language, "children.error.invalid_name")}}
	case errors.Is(err, service.ErrInvalidProfileBirthYear):
		form.Errors = []FieldError{{Field: "birth_year", Message: h.t(current.language, "children.error.invalid_birth_year")}}
	}
	return form
}
