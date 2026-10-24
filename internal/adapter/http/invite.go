package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

// InviteUseCases aggregates the invite operations consumed by HTTP handlers.
type InviteUseCases interface {
	List(ctx context.Context, access *service.GroupAccess) ([]service.GroupInviteListing, error)
	Create(ctx context.Context, actor service.GroupActor, input service.InviteInput) (service.GroupInvite, error)
	Revoke(ctx context.Context, actor service.GroupActor, inviteID int64) (service.GroupInvite, error)
	// Now is the clock invite states are computed with.
	Now() int64
}

// inviteExpiryDays are the validity choices offered on the invite form; the
// server accepts any value up to service.MaxInviteDays.
var inviteExpiryDays = []int{1, 7, 30, 90}

// InvitesView is the invite page: the create form and the invites the
// account may manage.
type InvitesView struct {
	CreateURL   string
	Roles       []SelectOptionView
	Teams       []SelectOptionView
	AllowNoTeam bool
	Expiries    []SelectOptionView
	MaxUses     string
	Error       string
	Rows        []InviteRowView
	// Created is the code of the invite just created, shown once on top.
	Created string
}

// SelectOptionView is one option of a select element.
type SelectOptionView struct {
	Value    string
	Label    string
	Selected bool
}

// InviteRowView is one invite with its state.
type InviteRowView struct {
	ID          int64
	Code        string
	Role        string
	RoleLabel   string
	TeamName    string
	Uses        string
	Expires     string
	Status      string
	StatusLabel string
	Active      bool
	RevokeURL   string
}

// registerInviteRoutes registers each group's invite page and its writes.
func registerInviteRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/invites", groups.invites)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/invites", groups.createInvite)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/invites/{inviteID}/revoke", groups.revokeInvite)
	}
}

func (h *groupHandler) invites(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.inviteRequest(writer, request)
	if !ok {
		return
	}
	if !current.access.CanManageInvites() {
		h.renderAccessDenied(writer, request, current.language)
		return
	}
	h.renderSection(writer, request, current, groupSectionInvites, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createInvite(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.inviteRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	values := map[string]string{
		"role":       strings.TrimSpace(request.PostFormValue("role")),
		"team_id":    strings.TrimSpace(request.PostFormValue("team_id")),
		"expires_in": strings.TrimSpace(request.PostFormValue("expires_in")),
		"max_uses":   strings.TrimSpace(request.PostFormValue("max_uses")),
	}
	form := FormData{Submitted: true, Values: values}
	input, err := inviteInput(values)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionInvites, 0, err, h.inviteFormError(current, form, err))
		return
	}
	invite, err := h.dependencies.Invites.Create(request.Context(), current.actor(), input)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionInvites, 0, err, h.inviteFormError(current, form, err))
		return
	}
	created := FormData{Submitted: true, Values: map[string]string{"created": invite.Code}}
	h.renderSection(writer, request, current, groupSectionInvites, 0, created, []Alert{{Level: "success", Message: h.t(current.language, "invites.created")}}, http.StatusOK)
}

func (h *groupHandler) revokeInvite(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.inviteRequest(writer, request)
	if !ok {
		return
	}
	inviteID, err := pathPositiveID(request, "inviteID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	if _, err := h.dependencies.Invites.Revoke(request.Context(), current.actor(), inviteID); err != nil {
		h.renderWriteError(writer, request, current, groupSectionInvites, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionInvites, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "invites.revoked")}}, http.StatusOK)
}

func (h *groupHandler) inviteRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Invites == nil || h.dependencies.Teams == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) loadInvites(writer http.ResponseWriter, request *http.Request, current groupRequest, group service.Group, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Invites == nil || h.dependencies.Teams == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	ctx := request.Context()
	access := current.access
	listings, err := h.dependencies.Invites.List(ctx, access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	teams, err := h.dependencies.Teams.List(ctx, access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	language := current.language
	invites := &InvitesView{
		CreateURL:   h.groupPath(language, group.ID, "/invites"),
		AllowNoTeam: access.CanCreateInvite(service.GroupRolePlayer, 0),
		MaxUses:     form.Value("max_uses"),
		Error:       form.Error("role"),
		Created:     form.Value("created"),
	}
	selectedTeam := form.Value("team_id")
	invitableTeams := make([]service.Team, 0, len(teams))
	for _, summary := range teams {
		if summary.Team.Archived() || !access.CanCreateInvite(service.GroupRolePlayer, summary.Team.ID) {
			continue
		}
		invitableTeams = append(invitableTeams, summary.Team)
		value := strconv.FormatInt(summary.Team.ID, 10)
		invites.Teams = append(invites.Teams, SelectOptionView{Value: value, Label: summary.Team.Name + " (" + teamYears(summary.Team) + ")", Selected: value == selectedTeam})
	}
	selectedRole := form.Value("role")
	if selectedRole == "" {
		selectedRole = service.GroupRolePlayer
	}
	for _, role := range service.GroupRoles() {
		allowed := access.CanCreateInvite(role, 0)
		for _, team := range invitableTeams {
			allowed = allowed || access.CanCreateInvite(role, team.ID)
		}
		if allowed {
			invites.Roles = append(invites.Roles, SelectOptionView{Value: role, Label: h.roleLabel(language, role), Selected: role == selectedRole})
		}
	}
	selectedExpiry := form.Value("expires_in")
	invites.Expiries = append(invites.Expiries, SelectOptionView{Value: "", Label: h.t(language, "invites.expires.never"), Selected: selectedExpiry == ""})
	for _, days := range inviteExpiryDays {
		value := strconv.Itoa(days)
		invites.Expiries = append(invites.Expiries, SelectOptionView{Value: value, Label: h.t(language, "invites.expires.days_"+value), Selected: value == selectedExpiry})
	}
	location, err := time.LoadLocation(group.TimeZone)
	if err != nil {
		location = time.UTC
	}
	now := h.dependencies.Invites.Now()
	for _, listing := range listings {
		invite := listing.Invite
		status := invite.Status(now)
		row := InviteRowView{
			ID:          invite.ID,
			Code:        invite.Code,
			Role:        invite.Role,
			RoleLabel:   h.roleLabel(language, invite.Role),
			Uses:        strconv.Itoa(invite.Uses),
			Status:      string(status),
			StatusLabel: h.t(language, "invites.status."+string(status)),
			Active:      status == service.InviteActive,
			RevokeURL:   h.groupPath(language, group.ID, "/invites/"+strconv.FormatInt(invite.ID, 10)+"/revoke"),
		}
		if listing.TeamName != nil {
			row.TeamName = *listing.TeamName
		}
		if invite.MaxUses != nil {
			row.Uses += " / " + strconv.Itoa(*invite.MaxUses)
		}
		if invite.ExpiresAt != nil {
			row.Expires = time.Unix(*invite.ExpiresAt, 0).In(location).Format("2006-01-02 15:04")
		}
		invites.Rows = append(invites.Rows, row)
	}
	view.Invites = invites
	return true
}

// inviteInput parses the invite form. Malformed numbers are invalid input,
// never silently zero.
func inviteInput(values map[string]string) (service.InviteInput, error) {
	input := service.InviteInput{Role: values["role"]}
	for _, field := range []struct {
		name   string
		target *int
	}{{"expires_in", &input.ExpiresInDays}, {"max_uses", &input.MaxUses}} {
		if values[field.name] == "" {
			continue
		}
		value, err := strconv.Atoi(values[field.name])
		if err != nil || value < 0 {
			return service.InviteInput{}, service.ErrInvalidInvite
		}
		*field.target = value
	}
	if raw := values["team_id"]; raw != "" {
		teamID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || teamID <= 0 {
			return service.InviteInput{}, service.ErrTeamNotFound
		}
		input.TeamID = teamID
	}
	return input, nil
}

// inviteFormError attaches the field error of a failed invite creation.
func (h *groupHandler) inviteFormError(current groupRequest, form FormData, err error) FormData {
	switch {
	case errors.Is(err, service.ErrInvalidInvite):
		form.Errors = []FieldError{{Field: "role", Message: h.t(current.language, "invites.error.invalid")}}
	case errors.Is(err, service.ErrInvalidGroupRole):
		form.Errors = []FieldError{{Field: "role", Message: h.t(current.language, "groups.error.invalid_role")}}
	}
	return form
}
