package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// GroupUseCases aggregates the group operations consumed by HTTP handlers.
// Group-scoped operations take the caller's GroupAccess, which they check
// again before acting.
type GroupUseCases interface {
	Create(ctx context.Context, userID int64, origin, name, timeZone string) (service.GroupSummary, error)
	List(ctx context.Context, userID int64) ([]service.GroupSummary, error)
	Overview(ctx context.Context, access *service.GroupAccess) (service.GroupOverview, error)
	Group(ctx context.Context, access *service.GroupAccess) (service.Group, error)
	UpdateSettings(ctx context.Context, actor service.GroupActor, expectedVersion int64, name, timeZone string) (service.Group, error)
	RotateInvite(ctx context.Context, actor service.GroupActor) (string, error)
	Leave(ctx context.Context, access *service.GroupAccess) error
	Members(ctx context.Context, access *service.GroupAccess) ([]service.GroupMember, error)
	AddRole(ctx context.Context, actor service.GroupActor, targetUserID, expectedVersion int64, role string) (service.GroupMembership, error)
	RemoveRole(ctx context.Context, actor service.GroupActor, targetUserID, expectedVersion int64, role string) (service.GroupMembership, error)
	RemoveMember(ctx context.Context, actor service.GroupActor, targetUserID, expectedVersion int64) error
}

// GroupAccessProvider builds the caller's access to one group. It answers
// service.ErrGroupNotFound to accounts that may not see the group.
type GroupAccessProvider interface {
	For(ctx context.Context, userID, groupID int64) (*service.GroupAccess, error)
}

// JoinRateLimitedGroupJoiner is the rate-limited invite redemption operation.
type JoinRateLimitedGroupJoiner interface {
	Join(context.Context, int64, string) (service.GroupMembership, error)
}

// Group page sections.
const (
	groupSectionOverview = "overview"
	groupSectionMembers  = "members"
	groupSectionTeams    = "teams"
	groupSectionTeam     = "team"
	groupSectionSettings = "settings"
)

// GroupsView contains the group list and join/create affordances.
type GroupsView struct {
	L         map[string]string
	ListURL   string
	CreateURL string
	JoinURL   string
	Creating  bool
	// CanCreate is false for guests, who see why instead of the form.
	CanCreate  bool
	UpgradeURL string
	Summaries  []GroupSummaryView
	MineLabel  string
	TimeZones  []TimeZoneOption
}

// GroupSummaryView is one group list row with localized role labels.
type GroupSummaryView struct {
	ID          int64
	Name        string
	URL         string
	MemberCount int
	RoleLabels  []string
}

// TimeZoneOption is one entry of the time zone picker.
type TimeZoneOption struct {
	Value    string
	Selected bool
}

// GroupDetailView is the shell of every group page: heading, section
// navigation, and the current section's data.
type GroupDetailView struct {
	L          map[string]string
	GroupID    int64
	Name       string
	Section    string
	Nav        []GroupNavItem
	RoleLabels []string
	ListURL    string
	Overview   *GroupOverviewView
	Settings   *GroupSettingsView
	Members    *GroupMembersView
	Teams      *TeamsView
	Team       *TeamDetailView
}

// GroupNavItem is one group section link.
type GroupNavItem struct {
	ID      string
	Label   string
	URL     string
	Current bool
}

// GroupOverviewView is the group's main page.
type GroupOverviewView struct {
	MemberCount int
	TimeZone    string
	InviteCode  string
	InviteURL   string
	IsMember    bool
	LeaveURL    string
}

// GroupSettingsView is the settings form for admins and managers.
type GroupSettingsView struct {
	ActionURL string
	Name      string
	Version   int64
	TimeZones []TimeZoneOption
}

// GroupMembersView is the member list.
type GroupMembersView struct {
	ShowEmails bool
	CanManage  bool
	Members    []GroupMemberView
}

// GroupMemberView is one member row and its available actions.
type GroupMemberView struct {
	UserID        int64
	Label         string
	Email         string
	IsSelf        bool
	IsGuest       bool
	Version       int64
	Roles         []RoleBadgeView
	AddableRoles  []RoleOptionView
	AddRoleURL    string
	RemoveRoleURL string
	RemoveURL     string
	CanRemove     bool
}

// RoleBadgeView is one held role.
type RoleBadgeView struct {
	Role      string
	Label     string
	CanRemove bool
}

// RoleOptionView is one role that can be chosen.
type RoleOptionView struct {
	Role  string
	Label string
}

type groupHandler struct {
	auth          *authHandler
	renderer      *PageRenderer
	settings      *SettingsRenderer
	dependencies  Dependencies
	authenticator *SessionAuthenticator
	labels        map[locale.Code]map[string]string
}

// groupRequest is an authenticated request to one group the account may see.
type groupRequest struct {
	user     service.User
	language locale.Code
	access   *service.GroupAccess
	origin   string
}

func (g groupRequest) actor() service.GroupActor {
	return service.GroupActor{Access: g.access, Origin: g.origin}
}

// registerGroupRoutes registers the group list, join, group pages,
// membership routes, and the team routes of each group.
func registerGroupRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups", groups.list)
		mux.HandleFunc("POST "+prefix+"/groups", groups.create)
		mux.HandleFunc("GET "+prefix+"/groups/join", groups.join)
		mux.HandleFunc("POST "+prefix+"/groups/join", groups.join)
		mux.HandleFunc("GET "+prefix+"/groups/{id}", groups.overview)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/invite", groups.rotateInvite)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/leave", groups.leave)
		mux.HandleFunc("GET "+prefix+"/groups/{id}/settings", groups.settingsPage)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/settings", groups.updateSettings)
		mux.HandleFunc("GET "+prefix+"/groups/{id}/members", groups.members)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/members/{memberID}/roles/add", groups.addRole)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/members/{memberID}/roles/remove", groups.removeRole)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/members/{memberID}/remove", groups.removeMember)
	}
	registerTeamRoutes(mux, groups)
}

func newGroupHandler(renderer *PageRenderer, dependencies Dependencies) *groupHandler {
	authenticator, _ := NewSessionAuthenticator(dependencies.Users, dependencies.Sessions, dependencies.SessionCookie)
	return &groupHandler{
		auth:          newAuthHandler(renderer, dependencies),
		renderer:      renderer,
		settings:      NewSettingsRenderer(renderer, dependencies),
		dependencies:  dependencies,
		authenticator: authenticator,
		labels:        groupLabels(renderer),
	}
}

// groupLabels collects every groups.* and teams.* message without
// placeholders per language for templates, keyed with underscores
// ("groups.members.heading" becomes "groups_members_heading").
func groupLabels(renderer *PageRenderer) map[locale.Code]map[string]string {
	labels := make(map[locale.Code]map[string]string)
	if renderer == nil || renderer.catalogs == nil {
		return labels
	}
	for _, language := range locale.Codes() {
		values := make(map[string]string)
		for _, id := range renderer.catalogs.Keys(language) {
			if !strings.HasPrefix(id, "groups.") && !strings.HasPrefix(id, "teams.") {
				continue
			}
			if text, err := renderer.catalogs.Translate(language, id, nil); err == nil {
				values[strings.ReplaceAll(id, ".", "_")] = text
			}
		}
		labels[language] = values
	}
	return labels
}

func (h *groupHandler) list(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	summaries, err := h.dependencies.Groups.List(request.Context(), user.ID)
	if err != nil {
		h.renderGroups(writer, request, language, user, nil, FormData{}, []Alert{{Level: "error", Message: h.t(language, "groups.error.generic")}}, http.StatusInternalServerError, false)
		return
	}
	h.renderGroups(writer, request, language, user, summaries, FormData{}, nil, http.StatusOK, false)
}

func (h *groupHandler) create(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	name := request.PostFormValue("name")
	timeZone := request.PostFormValue("time_zone")
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	summary, err := h.dependencies.Groups.Create(request.Context(), user.ID, requestOrigin(request), name, timeZone)
	if err != nil {
		form := FormData{Submitted: true, Values: map[string]string{"name": name, "time_zone": timeZone}}
		message := "groups.error.generic"
		status := http.StatusUnprocessableEntity
		switch {
		case errors.Is(err, service.ErrInvalidGroupName):
			form.Errors = []FieldError{{Field: "name", Message: h.t(language, "groups.error.invalid_name")}}
			message = "groups.error.invalid_name"
		case errors.Is(err, service.ErrInvalidTimeZone):
			form.Errors = []FieldError{{Field: "time_zone", Message: h.t(language, "groups.error.invalid_time_zone")}}
			message = "groups.error.invalid_time_zone"
		case errors.Is(err, service.ErrGroupRequiresAccount):
			message = "groups.error.requires_account"
			status = formFailureStatus(request, http.StatusForbidden)
		}
		h.renderGroups(writer, request, language, user, nil, form, []Alert{{Level: "error", Message: h.t(language, message)}}, status, true)
		return
	}
	h.redirectLocalized(writer, request, language, "/groups/"+strconv.FormatInt(summary.Group.ID, 10))
}

func (h *groupHandler) join(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method == http.MethodGet {
		h.renderGroups(writer, request, language, user, nil, FormData{}, nil, http.StatusOK, false)
		return
	}
	_ = request.ParseForm()
	code := request.PostFormValue("code")
	if h.dependencies.JoinGroup == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	membership, err := h.dependencies.JoinGroup.Join(request.Context(), user.ID, code)
	if err != nil {
		form := FormData{Submitted: true, Values: map[string]string{"code": code}}
		message := "groups.error.generic"
		status := http.StatusUnprocessableEntity
		switch {
		case errors.Is(err, service.ErrGroupInviteNotFound):
			form.Errors = []FieldError{{Field: "code", Message: h.t(language, "groups.error.invalid_code")}}
			message = "groups.error.invalid_code"
		case errors.Is(err, service.ErrMembershipExists):
			message = "groups.error.already_member"
		case errors.Is(err, service.ErrRateLimited):
			message = "groups.error.rate_limited"
			status = http.StatusTooManyRequests
		}
		h.renderGroups(writer, request, language, user, nil, form, []Alert{{Level: "error", Message: h.t(language, message)}}, status, false)
		return
	}
	h.redirectLocalized(writer, request, language, "/groups/"+strconv.FormatInt(membership.GroupID, 10))
}

func (h *groupHandler) overview(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionOverview, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) settingsPage(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	if !current.access.CanManageGroup() {
		h.renderAccessDenied(writer, request, current.language)
		return
	}
	h.renderSection(writer, request, current, groupSectionSettings, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) updateSettings(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	name := request.PostFormValue("name")
	timeZone := request.PostFormValue("time_zone")
	form := FormData{Submitted: true, Values: map[string]string{"name": name, "time_zone": timeZone}}
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionSettings, 0, service.ErrOptimisticLockConflict, form)
		return
	}
	if _, err := h.dependencies.Groups.UpdateSettings(request.Context(), current.actor(), version, name, timeZone); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidGroupName):
			form.Errors = []FieldError{{Field: "name", Message: h.t(current.language, "groups.error.invalid_name")}}
		case errors.Is(err, service.ErrInvalidTimeZone):
			form.Errors = []FieldError{{Field: "time_zone", Message: h.t(current.language, "groups.error.invalid_time_zone")}}
		}
		h.renderWriteError(writer, request, current, groupSectionSettings, 0, err, form)
		return
	}
	h.renderSection(writer, request, current, groupSectionSettings, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "groups.settings.saved")}}, http.StatusOK)
}

func (h *groupHandler) rotateInvite(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	if _, err := h.dependencies.Groups.RotateInvite(request.Context(), current.actor()); err != nil {
		h.renderWriteError(writer, request, current, groupSectionOverview, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionOverview, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "groups.invite.rotated")}}, http.StatusOK)
}

func (h *groupHandler) leave(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	if err := h.dependencies.Groups.Leave(request.Context(), current.access); err != nil {
		h.renderWriteError(writer, request, current, groupSectionOverview, 0, err, FormData{})
		return
	}
	h.redirectLocalized(writer, request, current.language, "/groups")
}

func (h *groupHandler) members(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionMembers, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) addRole(writer http.ResponseWriter, request *http.Request) {
	h.changeRole(writer, request, true)
}

func (h *groupHandler) removeRole(writer http.ResponseWriter, request *http.Request) {
	h.changeRole(writer, request, false)
}

func (h *groupHandler) changeRole(writer http.ResponseWriter, request *http.Request, add bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	targetUserID, err := pathPositiveID(request, "memberID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionMembers, 0, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	role := strings.TrimSpace(request.PostFormValue("role"))
	message := "groups.members.role_added"
	if add {
		_, err = h.dependencies.Groups.AddRole(request.Context(), current.actor(), targetUserID, version, role)
	} else {
		message = "groups.members.role_removed"
		_, err = h.dependencies.Groups.RemoveRole(request.Context(), current.actor(), targetUserID, version, role)
	}
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionMembers, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionMembers, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

func (h *groupHandler) removeMember(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return
	}
	targetUserID, err := pathPositiveID(request, "memberID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionMembers, 0, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if err := h.dependencies.Groups.RemoveMember(request.Context(), current.actor(), targetUserID, version); err != nil {
		h.renderWriteError(writer, request, current, groupSectionMembers, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionMembers, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "groups.members.removed")}}, http.StatusOK)
}

// groupRequest authenticates the account, reads the group ID, and builds its
// access. Accounts that may not see the group get the not-found page.
func (h *groupHandler) groupRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Groups == nil || h.dependencies.GroupAccess == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	groupID, err := pathPositiveID(request, "id")
	if err != nil {
		h.renderGroupNotFound(writer, request, language)
		return groupRequest{}, false
	}
	access, err := h.dependencies.GroupAccess.For(request.Context(), user.ID, groupID)
	if err != nil {
		if errors.Is(err, service.ErrGroupNotFound) {
			h.renderGroupNotFound(writer, request, language)
			return groupRequest{}, false
		}
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !access.CanView() {
		h.renderGroupNotFound(writer, request, language)
		return groupRequest{}, false
	}
	return groupRequest{user: user, language: language, access: access, origin: requestOrigin(request)}, true
}

// renderWriteError maps a failed write to its page outcome. HTMX requests
// get 422 so the fragment and its alert swap in.
func (h *groupHandler) renderWriteError(writer http.ResponseWriter, request *http.Request, current groupRequest, section string, teamID int64, err error, form FormData) {
	message, status := "", http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrTeamNotFound):
		h.renderGroupNotFound(writer, request, current.language)
		return
	case errors.Is(err, service.ErrGroupForbidden):
		message, status = "groups.error.forbidden", http.StatusForbidden
	case errors.Is(err, service.ErrOptimisticLockConflict):
		message, status = "groups.error.conflict", http.StatusConflict
	case errors.Is(err, service.ErrMembershipNotFound), errors.Is(err, service.ErrRecordNotFound):
		message, status = "groups.error.member_not_found", http.StatusNotFound
	case errors.Is(err, service.ErrRosterEntryNotFound):
		message, status = "teams.error.roster_entry_not_found", http.StatusNotFound
	case errors.Is(err, service.ErrLastGroupAdmin):
		message = "groups.error.last_admin"
	case errors.Is(err, service.ErrRoleNeedsAccount):
		message = "groups.error.role_needs_account"
	case errors.Is(err, service.ErrInvalidGroupRole), errors.Is(err, service.ErrInvalidRosterRole):
		message = "groups.error.invalid_role"
	case errors.Is(err, service.ErrInvalidGroupName):
		message = "groups.error.invalid_name"
	case errors.Is(err, service.ErrInvalidTimeZone):
		message = "groups.error.invalid_time_zone"
	case errors.Is(err, service.ErrInvalidTeamName):
		message = "teams.error.invalid_name"
	case errors.Is(err, service.ErrInvalidTeamBirthYears):
		message = "teams.error.invalid_birth_years"
	case errors.Is(err, service.ErrInvalidTeamDescription):
		message = "teams.error.invalid_description"
	case errors.Is(err, service.ErrRosterRoleMissing):
		message = "teams.error.roster_role_missing"
	case errors.Is(err, service.ErrInvalidGroup):
		message = "groups.error.generic"
	default:
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	h.renderSection(writer, request, current, section, teamID, form, []Alert{{Level: "error", Message: h.t(current.language, message)}}, formFailureStatus(request, status))
}

// renderSection loads one group section for the caller's access and renders
// it as a full page or HTMX fragment.
func (h *groupHandler) renderSection(writer http.ResponseWriter, request *http.Request, current groupRequest, section string, teamID int64, form FormData, alerts []Alert, status int) {
	ctx := request.Context()
	group, err := h.dependencies.Groups.Group(ctx, current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	view := h.groupShell(current, group, section)
	switch section {
	case groupSectionOverview:
		overview, err := h.dependencies.Groups.Overview(ctx, current.access)
		if err != nil {
			h.renderLoadError(writer, request, current.language, err)
			return
		}
		view.Overview = &GroupOverviewView{
			MemberCount: overview.MemberCount,
			TimeZone:    overview.Group.TimeZone,
			InviteCode:  overview.InviteCode,
			InviteURL:   h.groupPath(current.language, group.ID, "/invite"),
			IsMember:    current.access.IsMember(),
			LeaveURL:    h.groupPath(current.language, group.ID, "/leave"),
		}
	case groupSectionSettings:
		name := group.Name
		zone := group.TimeZone
		if form.Submitted {
			name, zone = form.Value("name"), form.Value("time_zone")
		}
		view.Settings = &GroupSettingsView{
			ActionURL: h.groupPath(current.language, group.ID, "/settings"),
			Name:      name,
			Version:   group.Version,
			TimeZones: timeZoneOptions(zone),
		}
	case groupSectionMembers:
		members, err := h.dependencies.Groups.Members(ctx, current.access)
		if err != nil {
			h.renderLoadError(writer, request, current.language, err)
			return
		}
		view.Members = h.membersView(current, group.ID, members)
	case groupSectionTeams:
		if !h.loadTeams(writer, request, current, group.ID, form, view) {
			return
		}
	case groupSectionTeam:
		if !h.loadTeam(writer, request, current, group.ID, teamID, form, view) {
			return
		}
	}
	h.renderGroupPage(writer, request, current, view, form, alerts, status)
}

func (h *groupHandler) renderLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	if errors.Is(err, service.ErrGroupNotFound) || errors.Is(err, service.ErrTeamNotFound) {
		h.renderGroupNotFound(writer, request, language)
		return
	}
	h.renderError(writer, request, http.StatusInternalServerError)
}

func (h *groupHandler) groupShell(current groupRequest, group service.Group, section string) *GroupDetailView {
	language := current.language
	view := &GroupDetailView{
		L:          h.labels[language],
		GroupID:    group.ID,
		Name:       group.Name,
		Section:    section,
		RoleLabels: h.roleLabels(language, current.access.Roles()),
		ListURL:    h.path(language, "/groups"),
	}
	items := []struct{ id, label, route string }{
		{groupSectionOverview, "groups.nav.overview", ""},
		{groupSectionMembers, "groups.nav.members", "/members"},
		{groupSectionTeams, "groups.nav.teams", "/teams"},
	}
	if current.access.CanManageGroup() {
		items = append(items, struct{ id, label, route string }{groupSectionSettings, "groups.nav.settings", "/settings"})
	}
	for _, item := range items {
		currentSection := section == item.id || (section == groupSectionTeam && item.id == groupSectionTeams)
		view.Nav = append(view.Nav, GroupNavItem{ID: item.id, Label: h.t(language, item.label), URL: h.groupPath(language, group.ID, item.route), Current: currentSection})
	}
	return view
}

func (h *groupHandler) membersView(current groupRequest, groupID int64, members []service.GroupMember) *GroupMembersView {
	language := current.language
	access := current.access
	view := &GroupMembersView{ShowEmails: access.CanViewAllMembers(), CanManage: access.CanManageGroup()}
	for _, member := range members {
		base := h.groupPath(language, groupID, "/members/"+strconv.FormatInt(member.UserID, 10))
		row := GroupMemberView{
			UserID:        member.UserID,
			Label:         h.memberLabel(language, member.UserID, member.DisplayName, member.Username, member.Email),
			IsSelf:        member.UserID == access.UserID(),
			IsGuest:       member.Kind == service.UserKindGuest,
			Version:       member.Version,
			AddRoleURL:    base + "/roles/add",
			RemoveRoleURL: base + "/roles/remove",
			RemoveURL:     base + "/remove",
		}
		if member.Email != nil {
			row.Email = *member.Email
		}
		for _, role := range member.Roles {
			row.Roles = append(row.Roles, RoleBadgeView{Role: role, Label: h.roleLabel(language, role), CanRemove: access.CanManageRole(role)})
		}
		for _, role := range service.GroupRoles() {
			if member.HasRole(role) || !access.CanManageRole(role) {
				continue
			}
			if service.GroupRoleNeedsAccount(role) && member.Kind != service.UserKindAccount {
				continue
			}
			row.AddableRoles = append(row.AddableRoles, RoleOptionView{Role: role, Label: h.roleLabel(language, role)})
		}
		row.CanRemove = !row.IsSelf && access.CanRemoveMember() && (!member.HasRole(service.GroupRoleAdmin) || access.CanManageRole(service.GroupRoleAdmin))
		view.Members = append(view.Members, row)
	}
	return view
}

func (h *groupHandler) renderGroupPage(writer http.ResponseWriter, request *http.Request, current groupRequest, view *GroupDetailView, form FormData, alerts []Alert, status int) {
	request = loweredRequest(request)
	page := PageData{
		Language:         string(current.language),
		Title:            view.Name + " | " + h.t(current.language, "groups.title"),
		Heading:          view.Name,
		Kind:             "group-detail",
		CSRFToken:        csrfToken(request),
		Form:             form,
		Template:         "groups",
		FragmentTemplate: "groups-fragment",
		Alerts:           alerts,
		Labels:           h.auth.formLabels(current.language),
		Account:          settingsAccountMenu(current.language, current.user),
		Navigation:       primaryNavigation(h.renderer, current.language, true),
		Group:            view,
	}
	if err := h.settings.Render(writer, request, page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) renderGroups(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, summaries []service.GroupSummary, form FormData, alerts []Alert, status int, creating bool) {
	zone := service.DefaultGroupTimeZone
	if form.Submitted && form.Value("time_zone") != "" {
		zone = form.Value("time_zone")
	}
	view := &GroupsView{
		L:          h.labels[language],
		ListURL:    h.path(language, "/groups"),
		CreateURL:  h.path(language, "/groups"),
		JoinURL:    h.path(language, "/groups/join"),
		Creating:   creating,
		CanCreate:  user.IsFullUser(),
		UpgradeURL: h.path(language, "/guest/upgrade"),
		MineLabel:  h.t(language, "groups.mine"),
		TimeZones:  timeZoneOptions(zone),
	}
	if user.IsAdmin {
		view.MineLabel = h.t(language, "groups.all")
	}
	for _, summary := range summaries {
		labels := h.roleLabels(language, summary.Roles)
		if len(labels) == 0 {
			labels = []string{h.t(language, "groups.role.none")}
		}
		view.Summaries = append(view.Summaries, GroupSummaryView{
			ID:          summary.Group.ID,
			Name:        summary.Group.Name,
			URL:         h.groupPath(language, summary.Group.ID, ""),
			MemberCount: summary.MemberCount,
			RoleLabels:  labels,
		})
	}
	page := PageData{
		Language:         string(language),
		Title:            h.t(language, "groups.title"),
		Heading:          h.t(language, "groups.heading"),
		Kind:             "groups",
		CSRFToken:        csrfToken(request),
		Form:             form,
		Template:         "groups",
		FragmentTemplate: "groups-fragment",
		Alerts:           alerts,
		Labels:           h.auth.formLabels(language),
		Account:          settingsAccountMenu(language, user),
		Navigation:       primaryNavigation(h.renderer, language, true),
		Groups:           view,
	}
	if err := h.settings.Render(writer, request, page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) renderGroupNotFound(writer http.ResponseWriter, request *http.Request, language locale.Code) {
	if err := h.renderer.RenderState(writer, request, language, PageStateNotFound); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) renderAccessDenied(writer http.ResponseWriter, request *http.Request, language locale.Code) {
	if err := h.renderer.RenderState(writer, request, language, PageStateAccessDenied); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) roleLabels(language locale.Code, roles []string) []string {
	labels := make([]string, 0, len(roles))
	for _, role := range roles {
		labels = append(labels, h.roleLabel(language, role))
	}
	return labels
}

func (h *groupHandler) roleLabel(language locale.Code, role string) string {
	return h.t(language, "groups.role."+role)
}

func timeZoneOptions(selected string) []TimeZoneOption {
	zones := service.CommonTimeZones()
	if selected != "" && !slices.Contains(zones, selected) {
		zones = append([]string{selected}, zones...)
	}
	options := make([]TimeZoneOption, 0, len(zones))
	for _, zone := range zones {
		options = append(options, TimeZoneOption{Value: zone, Selected: zone == selected})
	}
	return options
}

func loweredRequest(request *http.Request) *http.Request {
	clone := request.Clone(request.Context())
	clone.Method = http.MethodGet
	return clone
}

func (h *groupHandler) authenticatedUser(writer http.ResponseWriter, request *http.Request) (service.User, locale.Code, bool) {
	return authenticatePageUser(writer, request, h.authenticator, h.dependencies.Users)
}

func (h *groupHandler) redirectLocalized(writer http.ResponseWriter, request *http.Request, language locale.Code, route string) {
	if IsHTMX(request) {
		path, err := locale.Path(language, route)
		if err != nil {
			h.renderError(writer, request, http.StatusInternalServerError)
			return
		}
		writer.Header().Set("HX-Redirect", path)
		writer.WriteHeader(http.StatusOK)
		return
	}
	redirectLocalized(writer, request, language, route)
}

func (h *groupHandler) path(language locale.Code, route string) string {
	path, _ := locale.Path(language, route)
	return path
}

func (h *groupHandler) groupPath(language locale.Code, groupID int64, suffix string) string {
	return h.path(language, "/groups/"+strconv.FormatInt(groupID, 10)+suffix)
}

func (h *groupHandler) renderError(writer http.ResponseWriter, request *http.Request, status int) {
	renderSimpleError(writer, request, status)
}

func (h *groupHandler) t(language locale.Code, id string) string {
	return settingsTranslate(h.settings, language, id)
}

func pathPositiveID(request *http.Request, name string) (int64, error) {
	value := strings.TrimSpace(request.PathValue(name))
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, service.ErrGroupNotFound
	}
	return id, nil
}

// parseVersion reads the revision a form was rendered with. A missing or
// malformed revision is rejected rather than read as 0, which would match a
// never-edited record and skip the stale-write check.
func parseVersion(raw string) (int64, error) {
	version, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || version < 0 {
		return 0, service.ErrInvalidGroup
	}
	return version, nil
}

// memberLabel names a member by display name, username, or email, and
// falls back to a numbered label for guests and profiles without any.
func (h *groupHandler) memberLabel(language locale.Code, userID int64, displayName, username, email *string) string {
	for _, value := range []*string{displayName, username, email} {
		if value != nil && strings.TrimSpace(*value) != "" {
			return *value
		}
	}
	return h.t(language, "groups.members.unnamed") + " #" + strconv.FormatInt(userID, 10)
}

// pathID reads the positive "id" path value.
func pathID(request *http.Request) (int64, error) {
	return pathPositiveID(request, "id")
}
