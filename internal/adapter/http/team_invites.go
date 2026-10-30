package httpadapter

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

// TeamInvitesView is the invite card of a team page: the create form (the
// team is fixed) and the team's invites. Staff and the team's coaches see it.
type TeamInvitesView struct {
	CreateURL string
	Roles     []SelectOptionView
	Expiries  []SelectOptionView
	MaxUses   string
	Error     string
	Rows      []InviteRowView
	// Created is the code of the invite just created, shown once on top.
	Created string
}

// registerTeamInviteRoutesFor registers the writes of the team page's invite
// card under one language prefix.
func registerTeamInviteRoutesFor(mux *http.ServeMux, prefix string, groups *groupHandler) {
	mux.HandleFunc("GET "+prefix+"/groups/{group}/teams/{team}/invites", groups.teamInvites)
	mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/invites", groups.createTeamInvite)
	mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/invites/{inviteID}/revoke", groups.revokeTeamInvite)
}

func (h *groupHandler) teamInvites(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	if h.dependencies.Invites == nil || !current.access.CanCreateInvite(service.GroupRolePlayer, teamID) {
		h.renderAccessDenied(writer, request, current.language)
		return
	}
	h.renderSection(writer, request, current, groupSectionTeamInvites, teamID, FormData{}, nil, http.StatusOK)
}

// loadTeamInvitesTab fills the Invites tab of a team.
func (h *groupHandler) loadTeamInvitesTab(writer http.ResponseWriter, request *http.Request, current groupRequest, team service.Team, form FormData, view *GroupDetailView) bool {
	if team.Archived() || !current.access.CanCreateInvite(service.GroupRolePlayer, team.ID) {
		h.renderAccessDenied(writer, request, current.language)
		return false
	}
	detail := &TeamDetailView{ID: team.ID, Name: team.Name}
	if !h.loadTeamInvites(writer, request, current, detail, h.groupPath(current.language, current.slug, "/teams/"+team.Slug), form) {
		return false
	}
	view.Team = detail
	return true
}

func (h *groupHandler) createTeamInvite(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	if h.dependencies.Invites == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	_ = request.ParseForm()
	values := map[string]string{
		"role":       strings.TrimSpace(request.PostFormValue("role")),
		"expires_in": strings.TrimSpace(request.PostFormValue("expires_in")),
		"max_uses":   strings.TrimSpace(request.PostFormValue("max_uses")),
		"team_id":    strconv.FormatInt(teamID, 10),
		"invite":     "1",
	}
	form := FormData{Submitted: true, Values: values}
	input, err := inviteInput(values)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeamInvites, teamID, err, h.inviteFormError(current, form, err))
		return
	}
	invite, err := h.dependencies.Invites.Create(request.Context(), current.actor(), input)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeamInvites, teamID, err, h.inviteFormError(current, form, err))
		return
	}
	created := FormData{Submitted: true, Values: map[string]string{"created": invite.Code}}
	h.renderSection(writer, request, current, groupSectionTeamInvites, teamID, created, []Alert{{Level: "success", Message: h.t(current.language, "invites.created")}}, http.StatusOK)
}

func (h *groupHandler) revokeTeamInvite(writer http.ResponseWriter, request *http.Request) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	if h.dependencies.Invites == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	inviteID, err := pathPositiveID(request, "inviteID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	if _, err := h.dependencies.Invites.Revoke(request.Context(), current.actor(), inviteID); err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeamInvites, teamID, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionTeamInvites, teamID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "invites.revoked")}}, http.StatusOK)
}

// teamFormData drops the submitted values of the invite card, so a failed or
// finished invite write does not blank the team edit form.
func teamFormData(form FormData) FormData {
	if form.Value("invite") != "" || form.Value("created") != "" {
		return FormData{}
	}
	return form
}

// loadTeamInvites fills the invite card of a team page. It leaves the card
// out (nil) for accounts that may not invite to the team and for archived
// teams.
func (h *groupHandler) loadTeamInvites(writer http.ResponseWriter, request *http.Request, current groupRequest, team *TeamDetailView, base string, form FormData) bool {
	access := current.access
	if h.dependencies.Invites == nil || team.Archived || !access.CanCreateInvite(service.GroupRolePlayer, team.ID) {
		return true
	}
	language := current.language
	listings, err := h.dependencies.Invites.List(request.Context(), access)
	if err != nil {
		h.renderLoadError(writer, request, language, err)
		return false
	}
	card := &TeamInvitesView{
		CreateURL: base + "/invites",
		MaxUses:   form.Value("max_uses"),
		Error:     form.Error("role"),
		Created:   form.Value("created"),
	}
	selectedRole := form.Value("role")
	if selectedRole == "" {
		selectedRole = service.GroupRolePlayer
	}
	for _, role := range service.RosterRoles() {
		if access.CanCreateInvite(role, team.ID) {
			card.Roles = append(card.Roles, SelectOptionView{Value: role, Label: h.roleLabel(language, role), Selected: role == selectedRole})
		}
	}
	selectedExpiry := form.Value("expires_in")
	card.Expiries = append(card.Expiries, SelectOptionView{Value: "", Label: h.t(language, "invites.expires.never"), Selected: selectedExpiry == ""})
	for _, days := range inviteExpiryDays {
		value := strconv.Itoa(days)
		card.Expiries = append(card.Expiries, SelectOptionView{Value: value, Label: h.t(language, "invites.expires.days_"+value), Selected: value == selectedExpiry})
	}
	now := h.dependencies.Invites.Now()
	for _, listing := range listings {
		invite := listing.Invite
		if invite.Team() != team.ID {
			continue
		}
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
			RevokeURL:   base + "/invites/" + strconv.FormatInt(invite.ID, 10) + "/revoke",
		}
		if invite.MaxUses != nil {
			row.Uses += " / " + strconv.Itoa(*invite.MaxUses)
		}
		if invite.ExpiresAt != nil {
			row.Expires = time.Unix(*invite.ExpiresAt, 0).UTC().Format("2006-01-02 15:04 MST")
		}
		card.Rows = append(card.Rows, row)
	}
	team.Invites = card
	return true
}
