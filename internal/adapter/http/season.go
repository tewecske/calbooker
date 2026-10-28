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

// Season page sections.
const (
	groupSectionSeasons = "seasons"
	groupSectionSeason  = "season"
)

// SeasonUseCases aggregates the season operations consumed by HTTP handlers.
type SeasonUseCases interface {
	List(ctx context.Context, access *service.GroupAccess) ([]service.Season, error)
	Find(ctx context.Context, access *service.GroupAccess, seasonID int64) (service.Season, error)
	Current(ctx context.Context, access *service.GroupAccess) (service.Season, bool, error)
	Create(ctx context.Context, actor service.GroupActor, input service.SeasonInput) (service.Season, error)
	Update(ctx context.Context, actor service.GroupActor, seasonID, expectedVersion int64, input service.SeasonInput) (service.Season, error)
	Delete(ctx context.Context, actor service.GroupActor, seasonID, expectedVersion int64) error
	SetPartAlternating(ctx context.Context, actor service.GroupActor, seasonID, partID, expectedVersion int64, alternating bool) (service.Season, error)
	SeasonCoaches(ctx context.Context, access *service.GroupAccess, seasonID int64) ([]service.SeasonCoach, error)
	TeamCoaching(ctx context.Context, access *service.GroupAccess, teamID int64) (service.TeamCoaching, error)
	AssignCoach(ctx context.Context, actor service.GroupActor, assignment service.CoachAssignment) error
	RemoveCoach(ctx context.Context, actor service.GroupActor, seasonID, teamID, coachUserID int64) error
}

// SeasonsView is a group's season list. CreateForm is nil unless the
// account may manage seasons.
type SeasonsView struct {
	CreateForm *SeasonFormView
	Rows       []SeasonRowView
}

// SeasonRowView is one season with its parts.
type SeasonRowView struct {
	ID       int64
	Name     string
	StartsOn string
	EndsOn   string
	URL      string
	Current  bool
	Parts    []SeasonPartView
}

// SeasonPartView is one season part.
type SeasonPartView struct {
	ID          int64
	Position    int
	Name        string
	StartsOn    string
	EndsOn      string
	Alternating bool
}

// SeasonDetailView is one season with its coach assignments and the forms
// the account may use.
type SeasonDetailView struct {
	SeasonRowView
	Version   int64
	CanEdit   bool
	ListURL   string
	DeleteURL string
	// CoachBoardURL is the season's coach assignment board.
	CoachBoardURL string
	EditForm      *SeasonFormView
	Coaches       []SeasonCoachRowView
}

// SeasonCoachRowView is one coach assignment.
type SeasonCoachRowView struct {
	SeasonID int64
	TeamID   int64
	UserID   int64
	TeamName string
	TeamURL  string
	Label    string
	Email    string
	IsHead   bool
}

// SeasonFormView holds the values and errors of the season create or edit
// form. Parts always has two entries; the second is optional.
type SeasonFormView struct {
	L          map[string]string
	Prefix     string
	ActionURL  string
	Name       string
	StartsOn   string
	EndsOn     string
	NameError  string
	DatesError string
	Parts      []SeasonPartFormView
}

// SeasonPartFormView is one part's fields in the season form.
type SeasonPartFormView struct {
	L           map[string]string
	Prefix      string
	Position    int
	Legend      string
	Optional    bool
	Name        string
	StartsOn    string
	EndsOn      string
	Alternating bool
	// PlanURL is set for a stored part: its A/B weeks are then shown read
	// only, with a link to the plan page where staff switch them.
	PlanURL    string
	NameError  string
	DatesError string
}

// TeamCoachesView is the coaches card of a team page.
type TeamCoachesView struct {
	L          map[string]string
	CSRFToken  string
	CanManage  bool
	SeasonsURL string
	AddURL     string
	RemoveURL  string
	HeadURL    string
	Seasons    []TeamCoachSeasonView
	// SeasonOptions and Candidates fill the assignment form.
	SeasonOptions []SelectOptionView
	Candidates    []SelectOptionView
}

// TeamCoachSeasonView is one season's coaches of a team.
type TeamCoachSeasonView struct {
	ID       int64
	Name     string
	StartsOn string
	EndsOn   string
	Current  bool
	Coaches  []SeasonCoachRowView
}

// registerSeasonRoutes registers each group's season pages and the team
// coach routes.
func registerSeasonRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/seasons", groups.seasons)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/seasons", groups.createSeason)
		mux.HandleFunc("GET "+prefix+"/groups/{id}/seasons/{seasonID}", groups.season)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/seasons/{seasonID}", groups.updateSeason)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/seasons/{seasonID}/delete", groups.deleteSeason)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/seasons/{seasonID}/parts/{partID}/alternating", groups.setPartAlternating)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams/{teamID}/coaches/add", groups.assignCoach)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams/{teamID}/coaches/head", groups.setHeadCoach)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/teams/{teamID}/coaches/remove", groups.removeCoach)
	}
}

func (h *groupHandler) seasons(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.seasonRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionSeasons, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createSeason(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.seasonRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := seasonForm(request)
	season, err := h.dependencies.Seasons.Create(request.Context(), current.actor(), input)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionSeasons, 0, err, h.seasonFormErrors(current.language, form, err))
		return
	}
	h.redirectLocalized(writer, request, current.language, "/groups/"+strconv.FormatInt(current.access.GroupID(), 10)+"/seasons/"+strconv.FormatInt(season.ID, 10))
}

func (h *groupHandler) season(writer http.ResponseWriter, request *http.Request) {
	current, seasonID, ok := h.seasonPathRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionSeason, seasonID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) updateSeason(writer http.ResponseWriter, request *http.Request) {
	current, seasonID, ok := h.seasonPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := seasonForm(request)
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionSeason, seasonID, service.ErrOptimisticLockConflict, form)
		return
	}
	if _, err := h.dependencies.Seasons.Update(request.Context(), current.actor(), seasonID, version, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionSeason, seasonID, err, h.seasonFormErrors(current.language, form, err))
		return
	}
	h.renderSection(writer, request, current, groupSectionSeason, seasonID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "seasons.saved")}}, http.StatusOK)
}

func (h *groupHandler) deleteSeason(writer http.ResponseWriter, request *http.Request) {
	current, seasonID, ok := h.seasonPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionSeason, seasonID, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if err := h.dependencies.Seasons.Delete(request.Context(), current.actor(), seasonID, version); err != nil {
		h.renderWriteError(writer, request, current, groupSectionSeason, seasonID, err, FormData{})
		return
	}
	h.redirectLocalized(writer, request, current.language, "/groups/"+strconv.FormatInt(current.access.GroupID(), 10)+"/seasons")
}

func (h *groupHandler) assignCoach(writer http.ResponseWriter, request *http.Request) {
	h.changeCoach(writer, request, "add")
}

func (h *groupHandler) setHeadCoach(writer http.ResponseWriter, request *http.Request) {
	h.changeCoach(writer, request, "head")
}

func (h *groupHandler) removeCoach(writer http.ResponseWriter, request *http.Request) {
	h.changeCoach(writer, request, "remove")
}

// changeCoach adds, re-marks, or removes a season coach of the team and
// renders the team page again.
func (h *groupHandler) changeCoach(writer http.ResponseWriter, request *http.Request, action string) {
	current, teamID, ok := h.teamPathRequest(writer, request)
	if !ok {
		return
	}
	if h.dependencies.Seasons == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	_ = request.ParseForm()
	seasonID, seasonErr := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("season_id")), 10, 64)
	coachID, coachErr := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("coach_user_id")), 10, 64)
	if seasonErr != nil || seasonID <= 0 {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, service.ErrSeasonNotFound, FormData{})
		return
	}
	if coachErr != nil || coachID <= 0 {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, service.ErrSeasonCoachRoleMissing, FormData{})
		return
	}
	var (
		err     error
		message string
	)
	switch action {
	case "remove":
		message = "seasons.coaches.removed"
		err = h.dependencies.Seasons.RemoveCoach(request.Context(), current.actor(), seasonID, teamID, coachID)
	case "head":
		head := request.PostFormValue("head") == "true"
		message = "seasons.coaches.head_cleared"
		if head {
			message = "seasons.coaches.head_set"
		}
		err = h.dependencies.Seasons.AssignCoach(request.Context(), current.actor(), service.CoachAssignment{SeasonID: seasonID, TeamID: teamID, CoachUserID: coachID, Head: head})
	default:
		message = "seasons.coaches.assigned"
		err = h.dependencies.Seasons.AssignCoach(request.Context(), current.actor(), service.CoachAssignment{SeasonID: seasonID, TeamID: teamID, CoachUserID: coachID, Head: request.PostFormValue("head") == "true"})
	}
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionTeam, teamID, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionTeam, teamID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

// seasonRequest authorizes a season page: outsiders get 404 from
// groupRequest, members who may not see seasons get access denied.
func (h *groupHandler) seasonRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Seasons == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanViewSeasons() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) seasonPathRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.seasonRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	seasonID, err := pathPositiveID(request, "seasonID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	return current, seasonID, true
}

func (h *groupHandler) loadSeasons(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Seasons == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	seasons, err := h.dependencies.Seasons.List(request.Context(), current.access)
	if err != nil {
		h.renderSeasonLoadError(writer, request, current.language, err)
		return false
	}
	currentID, ok := h.currentSeasonID(writer, request, current)
	if !ok {
		return false
	}
	list := &SeasonsView{}
	if current.access.CanManageSeasons() {
		list.CreateForm = h.seasonFormView(current.language, "season-new", h.groupPath(current.language, groupID, "/seasons"), nil, form)
	}
	for _, season := range seasons {
		list.Rows = append(list.Rows, h.seasonRow(current.language, groupID, season, currentID))
	}
	view.Seasons = list
	return true
}

func (h *groupHandler) loadSeason(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID, seasonID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Seasons == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	season, err := h.dependencies.Seasons.Find(request.Context(), current.access, seasonID)
	if err != nil {
		h.renderSeasonLoadError(writer, request, current.language, err)
		return false
	}
	coaches, err := h.dependencies.Seasons.SeasonCoaches(request.Context(), current.access, seasonID)
	if err != nil {
		h.renderSeasonLoadError(writer, request, current.language, err)
		return false
	}
	currentID, ok := h.currentSeasonID(writer, request, current)
	if !ok {
		return false
	}
	row := h.seasonRow(current.language, groupID, season, currentID)
	detail := &SeasonDetailView{
		SeasonRowView: row,
		Version:       season.Version,
		CanEdit:       current.access.CanManageSeasons(),
		ListURL:       h.groupPath(current.language, groupID, "/seasons"),
		DeleteURL:     row.URL + "/delete",
		CoachBoardURL: row.URL + "/coach-board",
	}
	if detail.CanEdit {
		detail.EditForm = h.seasonFormView(current.language, "season-edit", row.URL, &season, form)
	}
	for _, coach := range coaches {
		detail.Coaches = append(detail.Coaches, h.seasonCoachRow(current.language, groupID, coach))
	}
	view.Season = detail
	return true
}

// loadTeamCoaches fills the coaches card of a team page. Without season
// use cases the card is left out.
func (h *groupHandler) loadTeamCoaches(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID, teamID int64, team *TeamDetailView) bool {
	if h.dependencies.Seasons == nil {
		return true
	}
	coaching, err := h.dependencies.Seasons.TeamCoaching(request.Context(), current.access, teamID)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return false
	}
	language := current.language
	base := h.groupPath(language, groupID, "/teams/"+strconv.FormatInt(teamID, 10)+"/coaches")
	coaches := &TeamCoachesView{
		L:          h.labels[language],
		CSRFToken:  csrfToken(request),
		CanManage:  current.access.CanManageSeasons(),
		SeasonsURL: h.groupPath(language, groupID, "/seasons"),
		AddURL:     base + "/add",
		RemoveURL:  base + "/remove",
		HeadURL:    base + "/head",
	}
	if len(coaching.Seasons) == 0 && !coaches.CanManage {
		return true
	}
	bySeason := make(map[int64][]SeasonCoachRowView)
	for _, coach := range coaching.Coaches {
		bySeason[coach.SeasonID] = append(bySeason[coach.SeasonID], h.seasonCoachRow(language, groupID, coach))
	}
	selected := coaching.CurrentSeasonID
	if selected == 0 && len(coaching.Seasons) > 0 {
		selected = coaching.Seasons[0].ID
	}
	for _, season := range coaching.Seasons {
		coaches.Seasons = append(coaches.Seasons, TeamCoachSeasonView{
			ID:       season.ID,
			Name:     season.Name,
			StartsOn: service.FormatSeasonDate(season.StartsOn),
			EndsOn:   service.FormatSeasonDate(season.EndsOn),
			Current:  season.ID == coaching.CurrentSeasonID,
			Coaches:  bySeason[season.ID],
		})
		value := strconv.FormatInt(season.ID, 10)
		coaches.SeasonOptions = append(coaches.SeasonOptions, SelectOptionView{Value: value, Label: season.Name, Selected: season.ID == selected})
	}
	for _, member := range coaching.Candidates {
		coaches.Candidates = append(coaches.Candidates, SelectOptionView{Value: strconv.FormatInt(member.UserID, 10), Label: h.memberLabel(language, member.UserID, member.DisplayName, member.Username, member.Email)})
	}
	team.Coaches = coaches
	return true
}

func (h *groupHandler) currentSeasonID(writer http.ResponseWriter, request *http.Request, current groupRequest) (int64, bool) {
	season, found, err := h.dependencies.Seasons.Current(request.Context(), current.access)
	if err != nil {
		h.renderSeasonLoadError(writer, request, current.language, err)
		return 0, false
	}
	if !found {
		return 0, true
	}
	return season.ID, true
}

func (h *groupHandler) renderSeasonLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	switch {
	case errors.Is(err, service.ErrSeasonNotFound):
		h.renderGroupNotFound(writer, request, language)
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, language)
	default:
		h.renderLoadError(writer, request, language, err)
	}
}

func (h *groupHandler) seasonRow(language locale.Code, groupID int64, season service.Season, currentID int64) SeasonRowView {
	row := SeasonRowView{
		ID:       season.ID,
		Name:     season.Name,
		StartsOn: service.FormatSeasonDate(season.StartsOn),
		EndsOn:   service.FormatSeasonDate(season.EndsOn),
		URL:      h.groupPath(language, groupID, "/seasons/"+strconv.FormatInt(season.ID, 10)),
		Current:  season.ID == currentID,
	}
	for _, part := range season.Parts {
		row.Parts = append(row.Parts, SeasonPartView{
			ID:          part.ID,
			Position:    part.Position,
			Name:        part.Name,
			StartsOn:    service.FormatSeasonDate(part.StartsOn),
			EndsOn:      service.FormatSeasonDate(part.EndsOn),
			Alternating: part.AlternatingWeeks,
		})
	}
	return row
}

func (h *groupHandler) seasonCoachRow(language locale.Code, groupID int64, coach service.SeasonCoach) SeasonCoachRowView {
	row := SeasonCoachRowView{
		SeasonID: coach.SeasonID,
		TeamID:   coach.TeamID,
		UserID:   coach.CoachUserID,
		TeamName: coach.TeamName,
		TeamURL:  h.groupPath(language, groupID, "/teams/"+strconv.FormatInt(coach.TeamID, 10)),
		Label:    h.memberLabel(language, coach.CoachUserID, coach.DisplayName, coach.Username, coach.Email),
		IsHead:   coach.IsHead,
	}
	if coach.Email != nil {
		row.Email = *coach.Email
	}
	return row
}

// seasonFormView fills the season form from submitted values after a
// failed write, else from the stored season (nil for a new season).
func (h *groupHandler) seasonFormView(language locale.Code, prefix, actionURL string, season *service.Season, form FormData) *SeasonFormView {
	labels := h.labels[language]
	view := &SeasonFormView{L: labels, Prefix: prefix, ActionURL: actionURL}
	view.Parts = []SeasonPartFormView{
		{L: labels, Prefix: prefix, Position: 1, Legend: h.t(language, "seasons.part1")},
		{L: labels, Prefix: prefix, Position: 2, Legend: h.t(language, "seasons.part2"), Optional: true},
	}
	switch {
	case form.Submitted:
		view.Name = form.Value("name")
		view.StartsOn = form.Value("starts_on")
		view.EndsOn = form.Value("ends_on")
		view.NameError = form.Error("name")
		view.DatesError = form.Error("starts_on")
		view.setPlanURLs(h, language, season)
		for index := range view.Parts {
			key := "part" + strconv.Itoa(index+1) + "_"
			view.Parts[index].Name = form.Value(key + "name")
			view.Parts[index].StartsOn = form.Value(key + "starts_on")
			view.Parts[index].EndsOn = form.Value(key + "ends_on")
			view.Parts[index].Alternating = form.Value(key+"alternating") == "true"
			view.Parts[index].NameError = form.Error(key + "name")
			view.Parts[index].DatesError = form.Error(key + "starts_on")
		}
	case season != nil:
		view.Name = season.Name
		view.StartsOn = service.FormatSeasonDate(season.StartsOn)
		view.EndsOn = service.FormatSeasonDate(season.EndsOn)
		view.setPlanURLs(h, language, season)
		for _, part := range season.Parts {
			if part.Position < 1 || part.Position > len(view.Parts) {
				continue
			}
			target := &view.Parts[part.Position-1]
			target.Name = part.Name
			target.StartsOn = service.FormatSeasonDate(part.StartsOn)
			target.EndsOn = service.FormatSeasonDate(part.EndsOn)
			target.Alternating = part.AlternatingWeeks
		}
	}
	return view
}

// setPlanURLs links the stored parts of a season to their plan page.
func (view *SeasonFormView) setPlanURLs(h *groupHandler, language locale.Code, season *service.Season) {
	if season == nil {
		return
	}
	for _, part := range season.Parts {
		if part.Position >= 1 && part.Position <= len(view.Parts) {
			view.Parts[part.Position-1].PlanURL = h.groupPath(language, season.GroupID, "/plan") + planQuery{part: part.ID}.encode()
		}
	}
}

// seasonFormErrors attaches the field error for a season validation
// failure.
func (h *groupHandler) seasonFormErrors(language locale.Code, form FormData, err error) FormData {
	field, message := seasonErrorField(err)
	if field != "" {
		form.Errors = []FieldError{{Field: field, Message: h.t(language, message)}}
	}
	return form
}

// seasonErrorField maps a season validation error to its form field and
// message; it returns empty strings for other errors.
func seasonErrorField(err error) (string, string) {
	var partErr *service.SeasonPartError
	part := "part2_"
	if errors.As(err, &partErr) && partErr.Position == 1 {
		part = "part1_"
	}
	switch {
	case errors.Is(err, service.ErrInvalidSeasonName):
		return "name", "seasons.error.invalid_name"
	case errors.Is(err, service.ErrSeasonNameTaken):
		return "name", "seasons.error.name_taken"
	case errors.Is(err, service.ErrInvalidSeasonDates):
		return "starts_on", "seasons.error.invalid_dates"
	case errors.Is(err, service.ErrSeasonOverlap):
		return "starts_on", "seasons.error.overlap"
	case errors.Is(err, service.ErrInvalidSeasonParts):
		return "part1_name", "seasons.error.parts"
	case errors.Is(err, service.ErrInvalidSeasonPartName):
		return part + "name", "seasons.error.invalid_part_name"
	case errors.Is(err, service.ErrInvalidSeasonPartDates):
		return part + "starts_on", "seasons.error.invalid_part_dates"
	case errors.Is(err, service.ErrSeasonPartOutsideSeason):
		return part + "starts_on", "seasons.error.part_outside"
	case errors.Is(err, service.ErrSeasonPartsOverlap):
		return part + "starts_on", "seasons.error.parts_overlap"
	case errors.Is(err, service.ErrSeasonCoachRoleMissing):
		return "coach_user_id", "seasons.error.coach_role_missing"
	}
	return "", ""
}

// seasonForm reads season input. Part 2 counts only when one of its name or
// date fields is filled in; unparseable dates become zero, which validation
// refuses.
func seasonForm(request *http.Request) (service.SeasonInput, FormData) {
	values := map[string]string{
		"name":      request.PostFormValue("name"),
		"starts_on": strings.TrimSpace(request.PostFormValue("starts_on")),
		"ends_on":   strings.TrimSpace(request.PostFormValue("ends_on")),
	}
	input := service.SeasonInput{
		Name:     values["name"],
		StartsOn: service.ParseSeasonDate(values["starts_on"]),
		EndsOn:   service.ParseSeasonDate(values["ends_on"]),
	}
	for position := 1; position <= service.MaxSeasonParts; position++ {
		key := "part" + strconv.Itoa(position) + "_"
		name := request.PostFormValue(key + "name")
		starts := strings.TrimSpace(request.PostFormValue(key + "starts_on"))
		ends := strings.TrimSpace(request.PostFormValue(key + "ends_on"))
		alternating := "false"
		if request.PostFormValue(key+"alternating") == "true" {
			alternating = "true"
		}
		values[key+"name"], values[key+"starts_on"], values[key+"ends_on"], values[key+"alternating"] = name, starts, ends, alternating
		if position > 1 && strings.TrimSpace(name) == "" && starts == "" && ends == "" {
			continue
		}
		input.Parts = append(input.Parts, service.SeasonPartInput{
			Name:             name,
			StartsOn:         service.ParseSeasonDate(starts),
			EndsOn:           service.ParseSeasonDate(ends),
			AlternatingWeeks: alternating == "true",
		})
	}
	return input, FormData{Submitted: true, Values: values}
}

// seasonErrorMessage returns the message of a season validation error, or
// "" for other errors.
func seasonErrorMessage(err error) string {
	_, message := seasonErrorField(err)
	return message
}

// setPartAlternating switches the A/B weeks of one part from the slot plan
// page or the training plan board (return=plan or training-plan). Only
// staff who manage seasons change it, at the season's revision; the page
// the request came from is rendered again.
func (h *groupHandler) setPartAlternating(writer http.ResponseWriter, request *http.Request) {
	current, seasonID, ok := h.seasonPathRequest(writer, request)
	if !ok {
		return
	}
	partID, err := pathPositiveID(request, "partID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	_ = request.ParseForm()
	query := planQuery{part: partID}
	if week := request.PostFormValue("week"); week == service.SeasonWeekA || week == service.SeasonWeekB {
		query.week = week
	}
	back := func(alerts []Alert, status int) {
		if request.PostFormValue("return") == groupSectionTrainingPlan {
			h.renderTrainingPlan(writer, request, current, query, service.TrainingBoardRequest{}, alerts, status)
			return
		}
		h.renderPlan(writer, request, current, query, alerts, status)
	}
	version, err := parseVersion(request.PostFormValue("version"))
	if err == nil {
		_, err = h.dependencies.Seasons.SetPartAlternating(request.Context(), current.actor(), seasonID, partID, version, request.PostFormValue("alternating") == "true")
	} else {
		err = service.ErrOptimisticLockConflict
	}
	if err != nil {
		message, status := "", http.StatusUnprocessableEntity
		switch {
		case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrSeasonNotFound), errors.Is(err, service.ErrRecordNotFound):
			h.renderGroupNotFound(writer, request, current.language)
			return
		case errors.Is(err, service.ErrGroupForbidden):
			message, status = "groups.error.forbidden", http.StatusForbidden
		case errors.Is(err, service.ErrOptimisticLockConflict):
			message, status = "groups.error.conflict", http.StatusConflict
		default:
			h.renderError(writer, request, http.StatusInternalServerError)
			return
		}
		back([]Alert{{Level: "error", Message: h.t(current.language, message)}}, formFailureStatus(request, status))
		return
	}
	back([]Alert{{Level: "success", Message: h.t(current.language, "plan.alternating.saved")}}, http.StatusOK)
}
