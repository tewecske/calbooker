package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// GroupCreator creates a group owned by the acting account.
type GroupCreator interface {
	Create(context.Context, int64, string) (service.GroupSummary, error)
}

// GroupLister lists groups visible to the acting account.
type GroupLister interface {
	List(context.Context, int64) ([]service.GroupSummary, error)
}

// GroupViewer returns one group's detail for a member.
type GroupViewer interface {
	Detail(context.Context, int64, int64) (service.GroupDetail, error)
}

// GroupJoiner joins a group by redeeming an invite code.
type GroupJoiner interface {
	Join(context.Context, int64, string) (service.GroupMembership, error)
}

// GroupRenamer renames a group using an expected revision.
type GroupRenamer interface {
	Rename(context.Context, int64, int64, int64, string) (service.Group, error)
}

// GroupInviter rotates a group invite code.
type GroupInviter interface {
	RotateInvite(context.Context, int64, int64) (string, error)
}

// JoinRateLimitedGroupJoiner is the rate-limited invite redemption operation.
type JoinRateLimitedGroupJoiner interface {
	Join(context.Context, int64, string) (service.GroupMembership, error)
}

// GroupUseCases aggregates the group operations consumed by HTTP handlers.
type GroupUseCases interface {
	GroupCreator
	GroupLister
	GroupViewer
	GroupJoiner
	GroupRenamer
	ChangeRole(context.Context, int64, int64, int64, int64, string) (service.GroupMembership, error)
	RemoveMember(context.Context, int64, int64, int64, int64) error
	Leave(context.Context, int64, int64) error
}

type groupHandler struct {
	auth          *authHandler
	renderer      *PageRenderer
	settings      *SettingsRenderer
	dependencies  Dependencies
	authenticator *SessionAuthenticator
}

func newGroupHandler(renderer *PageRenderer, dependencies Dependencies) *groupHandler {
	authenticator, _ := NewSessionAuthenticator(dependencies.Users, dependencies.Sessions, dependencies.SessionCookie)
	return &groupHandler{
		auth:          newAuthHandler(renderer, dependencies),
		renderer:      renderer,
		settings:      NewSettingsRenderer(renderer, dependencies),
		dependencies:  dependencies,
		authenticator: authenticator,
	}
}

func (h *groupHandler) list(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
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
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_ = request.ParseForm()
	name := request.PostFormValue("name")
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	summary, err := h.dependencies.Groups.Create(request.Context(), user.ID, name)
	if err != nil {
		form := FormData{Submitted: true, Values: map[string]string{"name": name}}
		message := "groups.error.generic"
		if errors.Is(err, service.ErrInvalidGroupName) {
			form.Errors = []FieldError{{Field: "name", Message: h.t(language, "groups.error.invalid_name")}}
			message = "groups.error.invalid_name"
		}
		h.renderGroups(writer, request, language, user, nil, form, []Alert{{Level: "error", Message: h.t(language, message)}}, http.StatusUnprocessableEntity, true)
		return
	}
	h.redirectLocalized(writer, request, language, "/groups/"+strconv.FormatInt(summary.Group.ID, 10))
}

func (h *groupHandler) detail(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	groupID, err := pathID(request)
	if err != nil {
		h.renderGroupNotFound(writer, request, language, user)
		return
	}
	detail, err := h.dependencies.Groups.Detail(request.Context(), user.ID, groupID)
	if err != nil {
		if errors.Is(err, service.ErrGroupNotFound) {
			h.renderGroupNotFound(writer, request, language, user)
			return
		}
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	h.renderGroupDetail(writer, request, language, user, detail, FormData{}, nil, http.StatusOK)
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
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
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

func (h *groupHandler) rename(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	groupID, err := pathID(request)
	if err != nil {
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
		return
	}
	_ = request.ParseForm()
	version, versionErr := parseVersion(request.PostFormValue("version"))
	if versionErr != nil {
		h.renderGroupDetailError(writer, request, language, user, groupID, FormData{Submitted: true, Errors: []FieldError{{Field: "name", Message: h.t(language, "groups.error.invalid_name")}}}, "groups.error.invalid_name", http.StatusUnprocessableEntity)
		return
	}
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	name := request.PostFormValue("name")
	if _, err := h.dependencies.Groups.Rename(request.Context(), user.ID, groupID, version, name); err != nil {
		h.renderGroupWriteError(writer, request, language, user, groupID, err, FormData{Submitted: true, Values: map[string]string{"name": name}})
		return
	}
	h.renderDetail(writer, request, language, user, groupID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) rotateInvite(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	groupID, err := pathID(request)
	if err != nil {
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
		return
	}
	if h.dependencies.GroupInvites == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	if _, err := h.dependencies.GroupInvites.RotateInvite(request.Context(), user.ID, groupID); err != nil {
		h.renderGroupWriteError(writer, request, language, user, groupID, err, FormData{})
		return
	}
	h.renderDetail(writer, request, language, user, groupID, FormData{}, []Alert{{Level: "success", Message: h.t(language, "groups.invite.rotated")}}, http.StatusOK)
}

func (h *groupHandler) changeRole(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	groupID, err := pathID(request)
	if err != nil {
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
		return
	}
	targetUserID, err := pathMemberID(request)
	if err != nil {
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
		return
	}
	_ = request.ParseForm()
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	version, ok := h.requireVersion(writer, request, language, user, groupID)
	if !ok {
		return
	}
	_, err = h.dependencies.Groups.ChangeRole(request.Context(), user.ID, groupID, targetUserID, version, request.PostFormValue("role"))
	if err != nil {
		h.renderGroupWriteError(writer, request, language, user, groupID, err, FormData{})
		return
	}
	h.renderDetail(writer, request, language, user, groupID, FormData{}, []Alert{{Level: "success", Message: h.t(language, "groups.roster.updated")}}, http.StatusOK)
}

func (h *groupHandler) removeMember(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	groupID, err := pathID(request)
	if err != nil {
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
		return
	}
	targetUserID, err := pathMemberID(request)
	if err != nil {
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
		return
	}
	_ = request.ParseForm()
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	version, ok := h.requireVersion(writer, request, language, user, groupID)
	if !ok {
		return
	}
	err = h.dependencies.Groups.RemoveMember(request.Context(), user.ID, groupID, targetUserID, version)
	if err != nil {
		h.renderGroupWriteError(writer, request, language, user, groupID, err, FormData{})
		return
	}
	h.renderDetail(writer, request, language, user, groupID, FormData{}, []Alert{{Level: "success", Message: h.t(language, "groups.roster.removed")}}, http.StatusOK)
}

func (h *groupHandler) leave(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	groupID, err := pathID(request)
	if err != nil {
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
		return
	}
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	if err := h.dependencies.Groups.Leave(request.Context(), user.ID, groupID); err != nil {
		h.renderGroupWriteError(writer, request, language, user, groupID, err, FormData{})
		return
	}
	h.redirectLocalized(writer, request, language, "/groups")
}

func (h *groupHandler) renderGroupWriteError(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, groupID int64, err error, form FormData) {
	switch {
	case errors.Is(err, service.ErrOptimisticLockConflict):
		h.renderGroupDetailError(writer, request, language, user, groupID, form, "groups.error.conflict", http.StatusConflict)
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrMembershipNotFound):
		h.renderGroupError(writer, request, language, user, service.ErrGroupNotFound)
	case errors.Is(err, service.ErrNotGroupAdmin):
		h.renderGroupDetailError(writer, request, language, user, groupID, form, "groups.error.not_admin", http.StatusForbidden)
	case errors.Is(err, service.ErrLastGroupAdmin):
		h.renderGroupDetailError(writer, request, language, user, groupID, form, "groups.error.last_admin", http.StatusUnprocessableEntity)
	case errors.Is(err, service.ErrInvalidGroupName):
		h.renderGroupDetailError(writer, request, language, user, groupID, form, "groups.error.invalid_name", http.StatusUnprocessableEntity)
	default:
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) renderGroupDetailError(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, groupID int64, form FormData, message string, status int) {
	detail, err := h.dependencies.Groups.Detail(request.Context(), user.ID, groupID)
	if err != nil {
		h.renderGroupError(writer, request, language, user, err)
		return
	}
	h.renderGroupDetail(writer, request, language, user, detail, form, []Alert{{Level: "error", Message: h.t(language, message)}}, status)
}

func (h *groupHandler) renderGroupError(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, err error) {
	if errors.Is(err, service.ErrGroupNotFound) {
		h.renderGroupNotFound(writer, request, language, user)
		return
	}
	h.renderError(writer, request, http.StatusInternalServerError)
}

func (h *groupHandler) renderGroupNotFound(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User) {
	if err := h.renderer.RenderState(writer, request, language, PageStateNotFound); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) renderGroups(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, summaries []service.GroupSummary, form FormData, alerts []Alert, status int, creating bool) {
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
		Account:          h.accountMenu(language, user),
		Navigation:       primaryNavigation(h.renderer, language, true),
		Groups: &GroupsView{
			ListURL:     h.path(language, "/groups"),
			CreateURL:   h.path(language, "/groups"),
			JoinURL:     h.path(language, "/groups/join"),
			Creating:    creating,
			Summaries:   h.groupSummaryViews(language, summaries),
			CreateLabel: h.t(language, "groups.create"),
			JoinLabel:   h.t(language, "groups.join"),
			MineLabel:   h.groupListLabel(language, user),
		},
	}
	if err := h.settings.Render(writer, request, page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) renderGroupDetail(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, detail service.GroupDetail, form FormData, alerts []Alert, status int) {
	request = loweredRequest(request)
	page := PageData{
		Language:         string(language),
		Title:            detail.Group.Name + " | " + h.t(language, "groups.title"),
		Heading:          detail.Group.Name,
		Kind:             "group-detail",
		CSRFToken:        csrfToken(request),
		Form:             form,
		Template:         "groups",
		FragmentTemplate: "groups-fragment",
		Alerts:           alerts,
		Labels:           h.auth.formLabels(language),
		Account:          h.accountMenu(language, user),
		Navigation:       primaryNavigation(h.renderer, language, true),
		Group:            h.groupDetailView(language, user, detail),
	}
	if err := h.settings.Render(writer, request, page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

// renderDetail re-reads the group and renders the detail view for a
// non-GET request. It never exposes the mutation request as a read.
func (h *groupHandler) renderDetail(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, groupID int64, form FormData, alerts []Alert, status int) {
	if h.dependencies.Groups == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	detail, err := h.dependencies.Groups.Detail(request.Context(), user.ID, groupID)
	if err != nil {
		h.renderGroupError(writer, request, language, user, err)
		return
	}
	h.renderGroupDetail(writer, request, language, user, detail, form, alerts, status)
}

func loweredRequest(request *http.Request) *http.Request {
	clone := request.Clone(request.Context())
	clone.Method = http.MethodGet
	return clone
}

// groupSummaryViews maps domain summaries to list rows with localized role
// labels. A superuser who is not a member gets the non-member label so access
// is never presented as a group role.
func (h *groupHandler) groupSummaryViews(language locale.Code, summaries []service.GroupSummary) []GroupSummaryView {
	views := make([]GroupSummaryView, 0, len(summaries))
	for _, summary := range summaries {
		view := GroupSummaryView{Group: summary.Group, MemberCount: summary.MemberCount}
		switch summary.Role {
		case service.GroupRoleAdmin:
			view.RoleLabel = h.t(language, "groups.role.admin")
		case service.GroupRoleMember:
			view.RoleLabel = h.t(language, "groups.role.member")
		default:
			view.RoleLabel = h.t(language, "groups.role.none")
		}
		views = append(views, view)
	}
	return views
}

// groupListLabel names the list section. Site administrators see every group,
// so the heading must not claim they all belong to the account.
func (h *groupHandler) groupListLabel(language locale.Code, user service.User) string {
	if user.IsAdmin {
		return h.t(language, "groups.all")
	}
	return h.t(language, "groups.mine")
}

func (h *groupHandler) groupDetailView(language locale.Code, user service.User, detail service.GroupDetail) *GroupDetailView {
	base := h.path(language, "/groups/"+strconv.FormatInt(detail.Group.ID, 10))
	view := &GroupDetailView{
		GroupID:     detail.Group.ID,
		Name:        detail.Group.Name,
		Version:     detail.Group.Version,
		MemberCount: detail.MemberCount,
		IsAdmin:     detail.IsAdmin,
		IsMember:    detail.IsMember,
		InviteCode:  detail.InviteCode,
		ListURL:     h.path(language, "/groups"),
		RenameURL:   base + "/rename",
		InviteURL:   base + "/invite",
		LeaveURL:    base + "/leave",
		MembersURL:  base + "/members",
	}
	for _, member := range detail.Members {
		label := groupMemberLabel(member)
		memberView := GroupMemberView{
			UserID:     member.UserID,
			Role:       member.Role,
			Version:    member.Version,
			Label:      label,
			IsAdmin:    member.Role == service.GroupRoleAdmin,
			IsSelf:     member.UserID == user.ID,
			PromoteURL: base + "/members/" + strconv.FormatInt(member.UserID, 10) + "/role",
			RemoveURL:  base + "/members/" + strconv.FormatInt(member.UserID, 10) + "/remove",
		}
		if view.IsAdmin {
			if memberView.IsAdmin {
				memberView.RoleAction = "demote"
				memberView.RoleLabel = h.t(language, "groups.roster.demote")
			} else {
				memberView.RoleAction = "promote"
				memberView.RoleLabel = h.t(language, "groups.roster.promote")
			}
		}
		view.Members = append(view.Members, memberView)
	}
	return view
}

func (h *groupHandler) authenticatedUser(writer http.ResponseWriter, request *http.Request) (service.User, locale.Code, bool) {
	return authenticatePageUser(writer, request, h.authenticator, h.dependencies.Users)
}

func (h *groupHandler) accountMenu(language locale.Code, user service.User) *AccountMenu {
	return settingsAccountMenu(language, user)
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

func (h *groupHandler) renderError(writer http.ResponseWriter, request *http.Request, status int) {
	renderSimpleError(writer, request, status)
}

func (h *groupHandler) t(language locale.Code, id string) string {
	return settingsTranslate(h.settings, language, id)
}

func pathID(request *http.Request) (int64, error) {
	value := strings.TrimSpace(request.PathValue("id"))
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, service.ErrGroupNotFound
	}
	return id, nil
}

func pathMemberID(request *http.Request) (int64, error) {
	value := strings.TrimSpace(request.PathValue("memberID"))
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, service.ErrGroupNotFound
	}
	return id, nil
}

func parseVersion(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version < 0 {
		return 0, service.ErrInvalidGroup
	}
	return version, nil
}

// requireVersion reads the membership revision a roster form was rendered
// with. A missing or malformed revision is rejected rather than read as 0,
// which would match a never-edited membership and skip the stale-write check.
func (h *groupHandler) requireVersion(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, groupID int64) (int64, bool) {
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderGroupDetailError(writer, request, language, user, groupID, FormData{}, "groups.error.conflict", http.StatusUnprocessableEntity)
		return 0, false
	}
	return version, true
}

func groupMemberLabel(member service.GroupMember) string {
	if member.DisplayName != nil && strings.TrimSpace(*member.DisplayName) != "" {
		return *member.DisplayName
	}
	if member.Username != nil && strings.TrimSpace(*member.Username) != "" {
		return *member.Username
	}
	if member.Email != nil && strings.TrimSpace(*member.Email) != "" {
		return *member.Email
	}
	return "Account"
}
