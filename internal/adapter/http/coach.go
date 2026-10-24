package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// Coach page sections.
const (
	groupSectionCoaches              = "coaches"
	groupSectionAvailability         = "availability"
	groupSectionAvailabilityOverview = "availability-overview"
)

// The availability grid shows 06:00-22:00 in 15-minute rows; a click adds
// two hours.
const (
	availabilityGridID        = "availability-grid"
	availabilityGridStart     = 6 * 60
	availabilityGridEnd       = 22 * 60
	availabilityGridStep      = 15
	availabilityGridNewLength = 120
)

// availabilityCoachVariants color coaches apart in the side-by-side view.
var availabilityCoachVariants = []WeekGridVariant{WeekGridPrimary, WeekGridSecondary, WeekGridAccent, WeekGridInfo, WeekGridSuccess, WeekGridWarning, WeekGridError, WeekGridNeutral}

// CoachUseCases aggregates the coach qualification and availability
// operations consumed by HTTP handlers.
type CoachUseCases interface {
	Coaches(ctx context.Context, access *service.GroupAccess) ([]service.Coach, error)
	SetQualification(ctx context.Context, actor service.GroupActor, coachUserID int64, minAge, maxAge int) (service.CoachQualification, error)
	ClearQualification(ctx context.Context, actor service.GroupActor, coachUserID int64) error
	Parts(ctx context.Context, access *service.GroupAccess) (service.AvailabilityParts, error)
	Availability(ctx context.Context, access *service.GroupAccess, partID int64) (service.PartAvailability, error)
	CoachAvailability(ctx context.Context, access *service.GroupAccess, partID, coachUserID int64) (service.PartAvailability, error)
	PreviousPart(ctx context.Context, access *service.GroupAccess, partID int64) (service.Season, service.SeasonPart, bool, error)
	AddAvailability(ctx context.Context, actor service.GroupActor, partID, coachUserID int64, input service.AvailabilityInput) error
	MoveAvailability(ctx context.Context, actor service.GroupActor, partID, coachUserID, windowID int64, input service.AvailabilityInput) error
	DeleteAvailability(ctx context.Context, actor service.GroupActor, partID, coachUserID, windowID int64) error
	CopyPreviousAvailability(ctx context.Context, actor service.GroupActor, partID, coachUserID int64) (int, error)
}

// CoachesView is the coaches page: qualifications and team ages.
type CoachesView struct {
	CanManage bool
	// OverviewURL links staff to every coach's availability.
	OverviewURL string
	Rows        []CoachRowView
	// AgeSeason names the season the team ages are computed for; empty
	// when the group has no seasons.
	AgeSeason string
	AgesIntro string
	Teams     []TeamAgeView
}

// CoachRowView is one coach with their qualification form.
type CoachRowView struct {
	UserID             int64
	Label              string
	Email              string
	IsSelf             bool
	HasQualification   bool
	QualificationLabel string
	MinAge             string
	MaxAge             string
	Error              string
	ActionURL          string
	ClearURL           string
	AvailabilityURL    string
}

// TeamAgeView is one team with its age in the season and, for staff, the
// coaches whose qualification covers it.
type TeamAgeView struct {
	ID       int64
	Name     string
	URL      string
	AgeLabel string
	Coaches  []string
}

// AvailabilityView is a coach's availability page, or with Overview the
// side-by-side view of every coach.
type AvailabilityView struct {
	Overview    bool
	CoachID     int64
	Heading     string
	BackURL     string
	OverviewURL string
	PickerURL   string
	Parts       []SelectOptionView
	HasPart     bool
	PartLabel   string
	PartID      int64
	Week        string
	WeekTabs    []WeekGridTab
	Grid        *WeekGridView
	Windows     []AvailabilityWindowView
	DeleteURL   string
	// CopyURL and CopySource offer the copy from the previous part; empty
	// for the group's first part.
	CopyURL    string
	CopySource string
	CopyHelp   string
	Legend     []AvailabilityLegendView
}

// AvailabilityWindowView is one stored window in the list under the grid.
type AvailabilityWindowView struct {
	ID        int64
	WeekLabel string
	DayName   string
	TimeLabel string
	Counts    bool
}

// AvailabilityLegendView names one coach of the side-by-side view.
type AvailabilityLegendView struct {
	Label      string
	URL        string
	BadgeClass string
	Windows    int
}

// availabilitySelection is the part and week chosen through the query
// string.
type availabilitySelection struct {
	partID int64
	week   string
}

func parseAvailabilitySelection(request *http.Request) availabilitySelection {
	query := request.URL.Query()
	selection := availabilitySelection{week: query.Get("week")}
	if id, err := strconv.ParseInt(strings.TrimSpace(query.Get("part")), 10, 64); err == nil && id > 0 {
		selection.partID = id
	}
	return selection
}

func (s availabilitySelection) query() string {
	values := url.Values{}
	values.Set("part", strconv.FormatInt(s.partID, 10))
	values.Set("week", s.week)
	return values.Encode()
}

// registerCoachRoutes registers each group's coach pages and the
// availability routes.
func registerCoachRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/coaches", groups.coaches)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/coaches/{coachID}/qualification", groups.setQualification)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/coaches/{coachID}/qualification/clear", groups.clearQualification)
		mux.HandleFunc("GET "+prefix+"/groups/{id}/coaches/availability", groups.availabilityOverview)
		mux.HandleFunc("GET "+prefix+"/groups/{id}/coaches/{coachID}/availability", groups.availability)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/coaches/{coachID}/availability/create", groups.createAvailability)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/coaches/{coachID}/availability/move", groups.moveAvailability)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/coaches/{coachID}/availability/delete", groups.deleteAvailability)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/coaches/{coachID}/availability/copy", groups.copyAvailability)
	}
}

func (h *groupHandler) coaches(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.coachRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionCoaches, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) setQualification(writer http.ResponseWriter, request *http.Request) {
	current, coachID, ok := h.coachPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	rawMin, rawMax := strings.TrimSpace(request.PostFormValue("min_age")), strings.TrimSpace(request.PostFormValue("max_age"))
	form := FormData{Submitted: true, Values: map[string]string{"coach_user_id": strconv.FormatInt(coachID, 10), "min_age": rawMin, "max_age": rawMax}}
	minAge, minErr := strconv.Atoi(rawMin)
	maxAge, maxErr := strconv.Atoi(rawMax)
	err := service.ErrInvalidQualification
	if minErr == nil && maxErr == nil {
		_, err = h.dependencies.Coaches.SetQualification(request.Context(), current.actor(), coachID, minAge, maxAge)
	}
	if err != nil {
		h.renderCoachWriteError(writer, request, current, groupSectionCoaches, 0, err, form)
		return
	}
	h.renderSection(writer, request, current, groupSectionCoaches, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "coaches.qualification_saved")}}, http.StatusOK)
}

func (h *groupHandler) clearQualification(writer http.ResponseWriter, request *http.Request) {
	current, coachID, ok := h.coachPathRequest(writer, request)
	if !ok {
		return
	}
	if err := h.dependencies.Coaches.ClearQualification(request.Context(), current.actor(), coachID); err != nil {
		h.renderCoachWriteError(writer, request, current, groupSectionCoaches, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionCoaches, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "coaches.qualification_cleared")}}, http.StatusOK)
}

func (h *groupHandler) availabilityOverview(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.coachRequest(writer, request)
	if !ok {
		return
	}
	if !current.access.CanViewAllAvailability() {
		h.renderAccessDenied(writer, request, current.language)
		return
	}
	h.renderSection(writer, request, current, groupSectionAvailabilityOverview, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) availability(writer http.ResponseWriter, request *http.Request) {
	current, coachID, ok := h.coachPathRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionAvailability, coachID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createAvailability(writer http.ResponseWriter, request *http.Request) {
	h.changeAvailability(writer, request, false)
}

func (h *groupHandler) moveAvailability(writer http.ResponseWriter, request *http.Request) {
	h.changeAvailability(writer, request, true)
}

// changeAvailability applies a week grid change (a new window, or a moved
// or resized one) and answers with the re-rendered grid.
func (h *groupHandler) changeAvailability(writer http.ResponseWriter, request *http.Request, move bool) {
	current, coachID, ok := h.coachPathRequest(writer, request)
	if !ok {
		return
	}
	ctx := request.Context()
	selection := parseAvailabilitySelection(request)
	if selection.partID <= 0 {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	// The part decides the grid the client saw (A/B weeks or not).
	partView, err := h.dependencies.Coaches.CoachAvailability(ctx, current.access, selection.partID, coachID)
	if err != nil {
		h.renderCoachWriteError(writer, request, current, groupSectionAvailability, coachID, err, FormData{})
		return
	}
	selection.week = availabilityWeek(partView.Part, selection.week)
	config := h.availabilityGridConfig(current.language, current.access.GroupID(), coachID, selection, partView.Part, true, csrfToken(request))
	change, err := ParseWeekGridChange(request, config, move)
	if err != nil {
		h.respondAvailabilityChange(writer, request, current, coachID, selection, "coaches.availability.error.invalid", err)
		return
	}
	input := service.AvailabilityInput{Week: selection.week, Weekday: change.Weekday, StartMinute: change.StartMinute, EndMinute: change.EndMinute}
	message := "coaches.availability.added"
	if move {
		message = "coaches.availability.moved"
		windowID, parseErr := strconv.ParseInt(change.ItemID, 10, 64)
		if parseErr != nil {
			windowID = 0
		}
		err = h.dependencies.Coaches.MoveAvailability(ctx, current.actor(), selection.partID, coachID, windowID, input)
	} else {
		err = h.dependencies.Coaches.AddAvailability(ctx, current.actor(), selection.partID, coachID, input)
	}
	if err != nil {
		h.respondAvailabilityChange(writer, request, current, coachID, selection, "", err)
		return
	}
	h.respondAvailabilityChange(writer, request, current, coachID, selection, message, nil)
}

func (h *groupHandler) deleteAvailability(writer http.ResponseWriter, request *http.Request) {
	current, coachID, ok := h.coachPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	selection := parseAvailabilitySelection(request)
	windowID, err := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("window")), 10, 64)
	if err != nil || windowID <= 0 {
		windowID = 0
	}
	if err := h.dependencies.Coaches.DeleteAvailability(request.Context(), current.actor(), selection.partID, coachID, windowID); err != nil {
		h.renderCoachWriteError(writer, request, current, groupSectionAvailability, coachID, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionAvailability, coachID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "coaches.availability.deleted")}}, http.StatusOK)
}

func (h *groupHandler) copyAvailability(writer http.ResponseWriter, request *http.Request) {
	current, coachID, ok := h.coachPathRequest(writer, request)
	if !ok {
		return
	}
	selection := parseAvailabilitySelection(request)
	if _, err := h.dependencies.Coaches.CopyPreviousAvailability(request.Context(), current.actor(), selection.partID, coachID); err != nil {
		h.renderCoachWriteError(writer, request, current, groupSectionAvailability, coachID, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionAvailability, coachID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "coaches.availability.copied")}}, http.StatusOK)
}

// respondAvailabilityChange answers a grid change: HTMX gets the grid alone
// (outer swap) with an alert, 422 when the change was refused; a plain form
// post is redirected back on success and gets the page on failure.
func (h *groupHandler) respondAvailabilityChange(writer http.ResponseWriter, request *http.Request, current groupRequest, coachID int64, selection availabilitySelection, message string, failure error) {
	status := http.StatusOK
	alert := Alert{Level: "success"}
	if failure != nil {
		status = http.StatusUnprocessableEntity
		alert.Level = "error"
		if message == "" {
			var notFound bool
			message, notFound = availabilityErrorMessage(failure)
			switch {
			case notFound:
				h.renderGroupNotFound(writer, request, current.language)
				return
			case message == "":
				h.renderCoachWriteError(writer, request, current, groupSectionAvailability, coachID, failure, FormData{})
				return
			}
		}
	}
	alert.Message = h.t(current.language, message)
	if !IsHTMX(request) {
		if failure == nil {
			h.redirectLocalized(writer, request, current.language, "/groups/"+strconv.FormatInt(current.access.GroupID(), 10)+"/coaches/"+strconv.FormatInt(coachID, 10)+"/availability?"+selection.query())
			return
		}
		h.renderSection(writer, request, current, groupSectionAvailability, coachID, FormData{}, []Alert{alert}, status)
		return
	}
	group, err := h.dependencies.Groups.Group(request.Context(), current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	view := h.groupShell(current, group, groupSectionAvailability)
	if !h.loadAvailability(writer, request, current, group.ID, coachID, view) {
		return
	}
	page := PageData{
		Language:  string(current.language),
		CSRFToken: csrfToken(request),
		Alerts:    []Alert{alert},
		Group:     view,
	}
	writer.Header().Add("Vary", "HX-Request")
	if err := h.renderer.renderStatus(writer, "group-availability-swap", page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

// availabilityErrorMessage maps a refused availability change to its
// message; notFound marks errors answered with the not-found page.
func availabilityErrorMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrCoachNotFound), errors.Is(err, service.ErrSeasonNotFound):
		return "", true
	case errors.Is(err, service.ErrGroupForbidden):
		return "groups.error.forbidden", false
	case errors.Is(err, service.ErrInvalidAvailability):
		return "coaches.availability.error.invalid", false
	case errors.Is(err, service.ErrAvailabilityWeek):
		return "coaches.availability.error.week", false
	case errors.Is(err, service.ErrAvailabilityNotFound):
		return "coaches.availability.error.not_found", false
	case errors.Is(err, service.ErrTooManyAvailabilityWindows):
		return "coaches.availability.error.too_many", false
	case errors.Is(err, service.ErrNoPreviousSeasonPart):
		return "coaches.availability.no_previous", false
	case errors.Is(err, service.ErrNothingToCopy):
		return "coaches.availability.error.nothing_to_copy", false
	case errors.Is(err, service.ErrInvalidQualification):
		return "coaches.error.invalid_qualification", false
	}
	return "", false
}

// renderCoachWriteError maps coach errors to their outcome and leaves the
// rest to renderWriteError.
func (h *groupHandler) renderCoachWriteError(writer http.ResponseWriter, request *http.Request, current groupRequest, section string, id int64, err error, form FormData) {
	message, notFound := availabilityErrorMessage(err)
	switch {
	case notFound:
		h.renderGroupNotFound(writer, request, current.language)
	case message == "" || errors.Is(err, service.ErrGroupForbidden):
		h.renderWriteError(writer, request, current, section, id, err, form)
	default:
		status := http.StatusUnprocessableEntity
		if errors.Is(err, service.ErrAvailabilityNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, service.ErrInvalidQualification) && form.Submitted {
			form.Errors = []FieldError{{Field: "min_age", Message: h.t(current.language, message)}}
		}
		h.renderSection(writer, request, current, section, id, form, []Alert{{Level: "error", Message: h.t(current.language, message)}}, formFailureStatus(request, status))
	}
}

func (h *groupHandler) renderCoachLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrCoachNotFound), errors.Is(err, service.ErrSeasonNotFound):
		h.renderGroupNotFound(writer, request, language)
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, language)
	default:
		h.renderLoadError(writer, request, language, err)
	}
}

// coachRequest authorizes a coach page: outsiders get 404 from
// groupRequest, members who are neither staff nor coaches get access
// denied.
func (h *groupHandler) coachRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Coaches == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanViewCoaches() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) coachPathRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.coachRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	coachID, err := pathPositiveID(request, "coachID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	return current, coachID, true
}

// loadCoaches fills the coaches page.
func (h *groupHandler) loadCoaches(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Coaches == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	ctx := request.Context()
	language := current.language
	coaches, err := h.dependencies.Coaches.Coaches(ctx, current.access)
	if err != nil {
		h.renderCoachLoadError(writer, request, language, err)
		return false
	}
	parts, err := h.dependencies.Coaches.Parts(ctx, current.access)
	if err != nil {
		h.renderCoachLoadError(writer, request, language, err)
		return false
	}
	list := &CoachesView{CanManage: current.access.CanManageQualifications()}
	if current.access.CanViewAllAvailability() {
		list.OverviewURL = h.groupPath(language, groupID, "/coaches/availability")
	}
	for _, coach := range coaches {
		base := h.groupPath(language, groupID, "/coaches/"+strconv.FormatInt(coach.UserID, 10))
		row := CoachRowView{
			UserID:          coach.UserID,
			Label:           h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, coach.Email),
			IsSelf:          coach.UserID == current.access.UserID(),
			ActionURL:       base + "/qualification",
			ClearURL:        base + "/qualification/clear",
			AvailabilityURL: base + "/availability",
		}
		if coach.Email != nil {
			row.Email = *coach.Email
		}
		if coach.Qualification != nil {
			row.HasQualification = true
			row.QualificationLabel = h.ageLabel(language, coach.Qualification.Range())
			row.MinAge, row.MaxAge = strconv.Itoa(coach.Qualification.MinAge), strconv.Itoa(coach.Qualification.MaxAge)
		}
		if form.Submitted && form.Value("coach_user_id") == strconv.FormatInt(coach.UserID, 10) {
			row.MinAge, row.MaxAge, row.Error = form.Value("min_age"), form.Value("max_age"), form.Error("min_age")
		}
		list.Rows = append(list.Rows, row)
	}
	if parts.HasDefault && h.dependencies.Teams != nil {
		season := parts.DefaultSeason
		list.AgeSeason = season.Name
		list.AgesIntro = h.translate(language, "coaches.ages_intro", map[string]string{"season": season.Name})
		teams, err := h.dependencies.Teams.List(ctx, current.access)
		if err != nil {
			h.renderLoadError(writer, request, language, err)
			return false
		}
		for _, summary := range teams {
			team := summary.Team
			if team.Archived() {
				continue
			}
			ages := service.TeamAgeRange(team, season)
			row := TeamAgeView{ID: team.ID, Name: team.Name, URL: h.groupPath(language, groupID, "/teams/"+strconv.FormatInt(team.ID, 10)), AgeLabel: h.ageLabel(language, ages)}
			if list.CanManage {
				for _, coach := range coaches {
					if coach.Qualification != nil && coach.Qualification.Covers(ages) {
						row.Coaches = append(row.Coaches, h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, coach.Email))
					}
				}
			}
			list.Teams = append(list.Teams, row)
		}
	}
	view.Coaches = list
	return true
}

// loadAvailability fills one coach's availability page.
func (h *groupHandler) loadAvailability(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID, coachID int64, view *GroupDetailView) bool {
	ctx := request.Context()
	language := current.language
	selection := parseAvailabilitySelection(request)
	page := &AvailabilityView{
		CoachID: coachID,
		BackURL: h.groupPath(language, groupID, "/coaches"),
	}
	if current.access.CanViewAllAvailability() {
		page.OverviewURL = h.groupPath(language, groupID, "/coaches/availability")
	}
	base := h.groupPath(language, groupID, "/coaches/"+strconv.FormatInt(coachID, 10)+"/availability")
	page.PickerURL = base
	if !h.availabilityParts(writer, request, current, &selection, page) {
		return false
	}
	if !current.access.CanViewAvailability(coachID) {
		h.renderAccessDenied(writer, request, language)
		return false
	}
	var availability service.PartAvailability
	if page.HasPart {
		var err error
		if availability, err = h.dependencies.Coaches.CoachAvailability(ctx, current.access, selection.partID, coachID); err != nil {
			h.renderCoachLoadError(writer, request, language, err)
			return false
		}
	}
	coaches, err := h.dependencies.Coaches.Coaches(ctx, current.access)
	if err != nil {
		h.renderCoachLoadError(writer, request, language, err)
		return false
	}
	label := ""
	for _, coach := range coaches {
		if coach.UserID == coachID {
			label = h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, coach.Email)
		}
	}
	if label == "" {
		h.renderGroupNotFound(writer, request, language)
		return false
	}
	if coachID == current.access.UserID() {
		page.Heading = h.t(language, "coaches.availability.heading_self")
	} else {
		page.Heading = h.translate(language, "coaches.availability.heading", map[string]string{"name": label})
	}
	if !page.HasPart {
		view.Availability = page
		return true
	}
	part := availability.Part
	selection.week = availabilityWeek(part, selection.week)
	page.Week = selection.week
	page.WeekTabs = h.availabilityWeekTabs(language, base, selection, part)
	page.DeleteURL = base + "/delete?" + selection.query()
	editable := current.access.CanEditAvailability(coachID)
	config := h.availabilityGridConfig(language, groupID, coachID, selection, part, editable, csrfToken(request))
	items := h.availabilityItems(language, availability, availability.Windows, selection.week, editable, "", WeekGridPrimary)
	grid, err := BuildWeekGrid(config, items, h.renderer.WeekGridTranslator(language))
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	page.Grid = grid
	for _, window := range availability.Windows {
		page.Windows = append(page.Windows, AvailabilityWindowView{
			ID:        window.ID,
			WeekLabel: h.t(language, "coaches.availability.week."+strings.ToLower(window.Week)),
			DayName:   h.t(language, "weekgrid.day."+strconv.Itoa(window.Weekday)),
			TimeLabel: service.FormatMinute(window.StartMinute) + "–" + service.FormatMinute(window.EndMinute),
			Counts:    availability.Applies(window),
		})
	}
	if editable {
		previousSeason, previous, found, err := h.dependencies.Coaches.PreviousPart(ctx, current.access, part.ID)
		if err != nil {
			h.renderCoachLoadError(writer, request, language, err)
			return false
		}
		if found {
			page.CopyURL = base + "/copy?" + selection.query()
			page.CopySource = previousSeason.Name + " · " + previous.Name
			page.CopyHelp = h.translate(language, "coaches.availability.copy_help", map[string]string{"source": page.CopySource})
		}
	}
	view.Availability = page
	return true
}

// loadAvailabilityOverview fills the side-by-side view of every coach's
// availability in a part.
func (h *groupHandler) loadAvailabilityOverview(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, view *GroupDetailView) bool {
	ctx := request.Context()
	language := current.language
	selection := parseAvailabilitySelection(request)
	base := h.groupPath(language, groupID, "/coaches/availability")
	page := &AvailabilityView{
		Overview:  true,
		Heading:   h.t(language, "coaches.availability.overview_heading"),
		BackURL:   h.groupPath(language, groupID, "/coaches"),
		PickerURL: base,
	}
	if !h.availabilityParts(writer, request, current, &selection, page) {
		return false
	}
	if !page.HasPart {
		view.Availability = page
		return true
	}
	availability, err := h.dependencies.Coaches.Availability(ctx, current.access, selection.partID)
	if err != nil {
		h.renderCoachLoadError(writer, request, language, err)
		return false
	}
	coaches, err := h.dependencies.Coaches.Coaches(ctx, current.access)
	if err != nil {
		h.renderCoachLoadError(writer, request, language, err)
		return false
	}
	part := availability.Part
	selection.week = availabilityWeek(part, selection.week)
	page.Week = selection.week
	page.WeekTabs = h.availabilityWeekTabs(language, base, selection, part)
	var items []WeekGridItem
	for index, coach := range coaches {
		label := h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, coach.Email)
		variant := availabilityCoachVariants[index%len(availabilityCoachVariants)]
		windows := availability.ForCoach(coach.UserID)
		coachItems := h.availabilityItems(language, availability, windows, selection.week, false, label, variant)
		items = append(items, coachItems...)
		page.Legend = append(page.Legend, AvailabilityLegendView{
			Label:      label,
			URL:        h.groupPath(language, groupID, "/coaches/"+strconv.FormatInt(coach.UserID, 10)+"/availability?"+selection.query()),
			BadgeClass: "badge-" + string(variant),
			Windows:    len(coachItems),
		})
	}
	config := h.availabilityGridConfig(language, groupID, 0, selection, part, false, "")
	config.ID = "availability-overview-grid"
	grid, err := BuildWeekGrid(config, items, h.renderer.WeekGridTranslator(language))
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	page.Grid = grid
	view.Availability = page
	return true
}

// availabilityParts resolves the chosen part (or the default one) and fills
// the part picker. A chosen part that is not in the group answers 404.
func (h *groupHandler) availabilityParts(writer http.ResponseWriter, request *http.Request, current groupRequest, selection *availabilitySelection, page *AvailabilityView) bool {
	parts, err := h.dependencies.Coaches.Parts(request.Context(), current.access)
	if err != nil {
		h.renderCoachLoadError(writer, request, current.language, err)
		return false
	}
	if selection.partID == 0 && parts.HasDefault {
		selection.partID = parts.Default.ID
	}
	for _, season := range parts.Seasons {
		for _, part := range season.Parts {
			label := season.Name + " · " + part.Name
			selected := part.ID == selection.partID
			if selected {
				page.HasPart, page.PartLabel, page.PartID = true, label, part.ID
			}
			page.Parts = append(page.Parts, SelectOptionView{Value: strconv.FormatInt(part.ID, 10), Label: label, Selected: selected})
		}
	}
	if selection.partID != 0 && !page.HasPart {
		h.renderGroupNotFound(writer, request, current.language)
		return false
	}
	return true
}

// availabilityWeek returns the week tab to show: "A" (default), "B", or
// "all" in alternating parts; always "all" otherwise.
func availabilityWeek(part service.SeasonPart, week string) string {
	if !part.AlternatingWeeks {
		return service.SeasonWeekAll
	}
	switch week {
	case service.SeasonWeekB, service.SeasonWeekAll:
		return week
	default:
		return service.SeasonWeekA
	}
}

func (h *groupHandler) availabilityWeekTabs(language locale.Code, base string, selection availabilitySelection, part service.SeasonPart) []WeekGridTab {
	if !part.AlternatingWeeks {
		return nil
	}
	tabs := make([]WeekGridTab, 0, 3)
	for _, week := range []string{service.SeasonWeekA, service.SeasonWeekB, service.SeasonWeekAll} {
		target := selection
		target.week = week
		tabs = append(tabs, WeekGridTab{Label: h.t(language, "coaches.availability.week."+strings.ToLower(week)), URL: base + "?" + target.query(), Current: week == selection.week})
	}
	return tabs
}

// availabilityGridConfig is the grid of a part and week tab. The grid's own
// week is "A" or "B" on those tabs of an alternating part and empty
// otherwise; the tab travels in the change URLs.
func (h *groupHandler) availabilityGridConfig(language locale.Code, groupID, coachID int64, selection availabilitySelection, part service.SeasonPart, editable bool, csrf string) WeekGridConfig {
	config := WeekGridConfig{
		ID:              availabilityGridID,
		StartMinute:     availabilityGridStart,
		EndMinute:       availabilityGridEnd,
		Step:            availabilityGridStep,
		DefaultDuration: availabilityGridNewLength,
		CSRFToken:       csrf,
	}
	if part.AlternatingWeeks && (selection.week == service.SeasonWeekA || selection.week == service.SeasonWeekB) {
		config.Week = selection.week
	}
	if editable && coachID > 0 {
		base := h.groupPath(language, groupID, "/coaches/"+strconv.FormatInt(coachID, 10)+"/availability")
		target := selection
		target.partID = part.ID
		config.MoveURL = base + "/move?" + target.query()
		config.CreateURL = base + "/create?" + target.query()
	}
	return config
}

// availabilityItems turns windows into grid items for a week tab. On an A
// or B tab the week's own windows are editable and every-week windows are
// shown fixed; on the every-week tab (and in parts without A/B weeks) the
// every-week windows are editable. Windows that do not count in the part
// are only listed.
func (h *groupHandler) availabilityItems(language locale.Code, availability service.PartAvailability, windows []service.AvailabilityWindow, week string, editable bool, label string, variant WeekGridVariant) []WeekGridItem {
	itemLabel := label
	if itemLabel == "" {
		itemLabel = h.t(language, "coaches.availability.item")
	}
	everyWeek := h.t(language, "coaches.availability.week.all")
	items := make([]WeekGridItem, 0, len(windows))
	for _, window := range windows {
		if !availability.Applies(window) {
			continue
		}
		item := WeekGridItem{
			ID:          strconv.FormatInt(window.ID, 10),
			Weekday:     window.Weekday,
			StartMinute: window.StartMinute,
			EndMinute:   window.EndMinute,
			Label:       itemLabel,
			Variant:     variant,
		}
		switch {
		case window.Week == week:
			item.Draggable = editable
		case week != service.SeasonWeekAll && window.Week == service.SeasonWeekAll:
			item.Detail = everyWeek
			if label == "" {
				item.Variant = WeekGridNeutral
			}
		default:
			continue
		}
		items = append(items, item)
	}
	return items
}

// ageLabel writes an age range: "9–10 years", or "10 years" for one age.
func (h *groupHandler) ageLabel(language locale.Code, ages service.AgeRange) string {
	if ages.Min == ages.Max {
		return h.translate(language, "coaches.age_single", map[string]string{"age": strconv.Itoa(ages.Min)})
	}
	return h.translate(language, "coaches.age_range", map[string]string{"min": strconv.Itoa(ages.Min), "max": strconv.Itoa(ages.Max)})
}

// translate resolves a message with placeholders; the ID on failure.
func (h *groupHandler) translate(language locale.Code, id string, values map[string]string) string {
	if h.renderer == nil || h.renderer.catalogs == nil {
		return id
	}
	text, err := h.renderer.catalogs.Translate(language, id, values)
	if err != nil {
		return id
	}
	return text
}
