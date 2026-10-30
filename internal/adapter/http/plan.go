package httpadapter

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// Plan page sections.
const (
	groupSectionPlan     = "plan"
	groupSectionPlanSlot = "plan-slot"
	planGridID           = "plan-grid"
	planGridStep         = 15
	planGridDefaultStart = 7 * 60
	planGridDefaultEnd   = 21 * 60
)

// PlanUseCases aggregates the weekly plan operations consumed by HTTP
// handlers.
type PlanUseCases interface {
	Board(ctx context.Context, access *service.GroupAccess, partID int64) (service.PlanBoard, error)
	Slot(ctx context.Context, access *service.GroupAccess, slotID int64) (service.PlanSlot, error)
	Create(ctx context.Context, actor service.GroupActor, partID int64, input service.PlanSlotInput) (service.PlanSlot, error)
	Update(ctx context.Context, actor service.GroupActor, slotID, expectedVersion int64, input service.PlanSlotInput) (service.PlanSlot, error)
	Move(ctx context.Context, actor service.GroupActor, slotID, expectedVersion int64, move service.PlanSlotMove) (service.PlanSlot, error)
	Delete(ctx context.Context, actor service.GroupActor, slotID, expectedVersion int64) error
	Copy(ctx context.Context, actor service.GroupActor, targetPartID, sourcePartID int64, replace bool) (service.PlanCopyResult, error)
}

// PlanView is the resource slot plan of one season part.
type PlanView struct {
	L       map[string]string
	CanEdit bool
	// Empty is true when the group has no season part yet.
	Empty        bool
	SeasonsURL   string
	ResourcesURL string
	PickerURL    string
	PartID       int64
	Parts        []SelectOptionView
	SeasonName   string
	PartName     string
	PartDates    string
	Alternating  bool
	// SwitchURL and SeasonVersion drive the A/B weeks switch, shown to
	// those who manage seasons (CanEdit).
	SwitchURL     string
	SeasonVersion int64
	Week          string
	WeekLabel     string
	// NoResources is true when the group has no active resource, so slots
	// cannot be added.
	NoResources bool
	// ResourceFilter lists the resource columns to choose from when there
	// are more than the grid shows at once.
	ResourceFilter []PlanResourceOptionView
	Grid           *WeekGridView
	Slots          []PlanSlotRowView
	Warnings       []PlanWarningView
	Copy           *PlanCopyFormView
}

// PlanResourceOptionView is one resource of the column filter.
type PlanResourceOptionView struct {
	Value   string
	Label   string
	Checked bool
}

// PlanSlotRowView is one slot in the slot list.
type PlanSlotRowView struct {
	ID       int64
	Day      string
	Time     string
	Resource string
	Archived bool
	Week     string
	Ages     string
	MaxTeams string
	Warning  bool
	URL      string
}

// PlanWarningView is one capacity warning.
type PlanWarningView struct {
	Text string
}

// PlanCopyFormView is the "copy from" form.
type PlanCopyFormView struct {
	ActionURL string
	Sources   []SelectOptionView
	// SlotCount is the number of slots the copy replaces.
	SlotCount   int
	ReplaceText string
}

// PlanSlotDetailView is one slot's page: details, and the edit form for
// those who edit the plan.
type PlanSlotDetailView struct {
	L          map[string]string
	ID         int64
	Version    int64
	CanEdit    bool
	PlanURL    string
	ActionURL  string
	DeleteURL  string
	SeasonName string
	PartName   string
	Row        PlanSlotRowView
	Form       *PlanSlotFormView
}

// PlanSlotFormView holds the values and errors of the slot form.
type PlanSlotFormView struct {
	Resources     []SelectOptionView
	Weeks         []SelectOptionView
	Days          []SelectOptionView
	Alternating   bool
	Start         string
	End           string
	MinAge        string
	MaxAge        string
	MaxTeams      string
	ResourceError string
	WeekError     string
	TimeError     string
	AgesError     string
	MaxTeamsError string
}

// registerPlanRoutes registers each group's resource slot plan.
func registerPlanRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{group}/plan", groups.plan)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/plan/slots/create", groups.createPlanSlot)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/plan/slots/move", groups.movePlanSlot)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/plan/copy", groups.copyPlan)
		mux.HandleFunc("GET "+prefix+"/groups/{group}/plan/slots/{slotID}", groups.planSlot)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/plan/slots/{slotID}", groups.updatePlanSlot)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/plan/slots/{slotID}/delete", groups.deletePlanSlot)
	}
}

// planQuery is the plan page state carried in the URL: the part, the A/B
// week, and the chosen resource columns.
type planQuery struct {
	part      int64
	week      string
	resources []int64
}

func parsePlanQuery(request *http.Request) planQuery {
	values := request.URL.Query()
	query := planQuery{}
	if part, err := strconv.ParseInt(strings.TrimSpace(values.Get("part")), 10, 64); err == nil && part > 0 {
		query.part = part
	}
	if week := values.Get("week"); week == service.SeasonWeekA || week == service.SeasonWeekB {
		query.week = week
	}
	for _, raw := range values["r"] {
		if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && id > 0 && !slices.Contains(query.resources, id) && len(query.resources) < WeekGridMaxResources {
			query.resources = append(query.resources, id)
		}
	}
	return query
}

func (q planQuery) encode() string {
	values := url.Values{}
	if q.part > 0 {
		values.Set("part", strconv.FormatInt(q.part, 10))
	}
	if q.week != "" {
		values.Set("week", q.week)
	}
	for _, id := range q.resources {
		values.Add("r", strconv.FormatInt(id, 10))
	}
	if encoded := values.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}

func (h *groupHandler) planURL(language locale.Code, groupSlug string, suffix string, query planQuery) string {
	return h.groupPath(language, groupSlug, "/plan"+suffix) + query.encode()
}

func (h *groupHandler) plan(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.planRequest(writer, request)
	if !ok {
		return
	}
	h.renderPlan(writer, request, current, parsePlanQuery(request), nil, http.StatusOK)
}

func (h *groupHandler) createPlanSlot(writer http.ResponseWriter, request *http.Request) {
	h.changePlanGrid(writer, request, false)
}

func (h *groupHandler) movePlanSlot(writer http.ResponseWriter, request *http.Request) {
	h.changePlanGrid(writer, request, true)
}

// changePlanGrid applies a move, resize, or new slot from the week grid and
// answers with the re-rendered grid (HTMX) or a redirect back to the plan.
func (h *groupHandler) changePlanGrid(writer http.ResponseWriter, request *http.Request, move bool) {
	current, ok := h.planRequest(writer, request)
	if !ok {
		return
	}
	query := parsePlanQuery(request)
	if query.part == 0 {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	board, err := h.dependencies.Plan.Board(request.Context(), current.access, query.part)
	if err != nil {
		h.renderPlanLoadError(writer, request, current.language, err)
		return
	}
	layout := h.planLayout(current, board, query)
	change, err := ParseWeekGridChange(request, layout.config, move)
	if err != nil {
		h.planGridResult(writer, request, current, query, err)
		return
	}
	resourceID, err := strconv.ParseInt(change.Resource, 10, 64)
	if err != nil || resourceID <= 0 {
		h.planGridResult(writer, request, current, query, ErrInvalidWeekGridChange)
		return
	}
	ctx := request.Context()
	if move {
		slotID, version, ok := parsePlanItemID(change.ItemID)
		if !ok {
			h.planGridResult(writer, request, current, query, ErrInvalidWeekGridChange)
			return
		}
		_, err = h.dependencies.Plan.Move(ctx, current.actor(), slotID, version, service.PlanSlotMove{
			ResourceID: resourceID, Weekday: change.Weekday, StartMinute: change.StartMinute, EndMinute: change.EndMinute,
		})
	} else {
		week := change.Week
		if week == "" {
			week = service.SeasonWeekAll
		}
		_, err = h.dependencies.Plan.Create(ctx, current.actor(), query.part, service.PlanSlotInput{
			ResourceID: resourceID, Week: week, Weekday: change.Weekday, StartMinute: change.StartMinute, EndMinute: change.EndMinute,
		})
	}
	h.planGridResult(writer, request, current, query, err)
}

// planGridResult answers a grid change: HTMX gets the grid, the slot list,
// and an alert (422 with the unchanged grid on failure); a plain form post
// is redirected back on success.
func (h *groupHandler) planGridResult(writer http.ResponseWriter, request *http.Request, current groupRequest, query planQuery, err error) {
	if err != nil && (errors.Is(err, service.ErrGroupNotFound) || errors.Is(err, service.ErrSeasonNotFound)) {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	var alert Alert
	status := http.StatusOK
	if err != nil {
		message, failure := planErrorMessage(err)
		if message == "" {
			h.renderError(writer, request, http.StatusInternalServerError)
			return
		}
		alert, status = Alert{Level: "error", Message: h.t(current.language, message)}, formFailureStatus(request, failure)
	} else {
		if !IsHTMX(request) {
			http.Redirect(writer, request, h.planURL(current.language, current.slug, "", query), http.StatusSeeOther)
			return
		}
		alert = Alert{Level: "success", Message: h.t(current.language, "plan.saved")}
	}
	if !IsHTMX(request) {
		h.renderPlan(writer, request, current, query, []Alert{alert}, status)
		return
	}
	view, ok := h.planPage(writer, request, current, query)
	if !ok {
		return
	}
	writer.Header().Add("Vary", "HX-Request")
	page := PageData{Language: string(current.language), CSRFToken: csrfToken(request), Alerts: []Alert{alert}, Group: view}
	if err := h.renderer.renderStatus(writer, "plan-swap", page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *groupHandler) copyPlan(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.planRequest(writer, request)
	if !ok {
		return
	}
	query := parsePlanQuery(request)
	if query.part == 0 {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	_ = request.ParseForm()
	sourceID, err := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("source_part_id")), 10, 64)
	if err != nil || sourceID <= 0 {
		sourceID = -1
	}
	replace := request.PostFormValue("replace") == "true"
	result, err := h.dependencies.Plan.Copy(request.Context(), current.actor(), query.part, sourceID, replace)
	if err != nil {
		if sourceID == -1 && errors.Is(err, service.ErrSeasonNotFound) {
			err = service.ErrInvalidPlanCopy
		}
		h.planWriteError(writer, request, current, query, err)
		return
	}
	alerts := []Alert{{Level: "success", Message: h.tv(current.language, "plan.copy.done", map[string]string{"count": strconv.Itoa(result.Copied)})}}
	if result.SkippedArchived > 0 {
		alerts = append(alerts, Alert{Level: "warning", Message: h.tv(current.language, "plan.copy.skipped_archived", map[string]string{"count": strconv.Itoa(result.SkippedArchived)})})
	}
	if result.Merged > 0 {
		alerts = append(alerts, Alert{Level: "info", Message: h.tv(current.language, "plan.copy.merged", map[string]string{"count": strconv.Itoa(result.Merged)})})
	}
	h.renderPlan(writer, request, current, query, alerts, http.StatusOK)
}

func (h *groupHandler) planSlot(writer http.ResponseWriter, request *http.Request) {
	current, slotID, ok := h.planSlotRequest(writer, request)
	if !ok {
		return
	}
	h.renderPlanSlot(writer, request, current, slotID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) updatePlanSlot(writer http.ResponseWriter, request *http.Request) {
	current, slotID, ok := h.planSlotRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := planSlotForm(request)
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.planSlotWriteError(writer, request, current, slotID, service.ErrOptimisticLockConflict, form)
		return
	}
	slot, err := h.dependencies.Plan.Update(request.Context(), current.actor(), slotID, version, input)
	if err != nil {
		h.planSlotWriteError(writer, request, current, slotID, err, form)
		return
	}
	h.redirectLocalized(writer, request, current.language, h.planRoute(current.slug, slot))
}

func (h *groupHandler) deletePlanSlot(writer http.ResponseWriter, request *http.Request) {
	current, slotID, ok := h.planSlotRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.planSlotWriteError(writer, request, current, slotID, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	slot, err := h.dependencies.Plan.Slot(request.Context(), current.access, slotID)
	if err != nil {
		h.planSlotWriteError(writer, request, current, slotID, err, FormData{})
		return
	}
	if err := h.dependencies.Plan.Delete(request.Context(), current.actor(), slotID, version); err != nil {
		h.planSlotWriteError(writer, request, current, slotID, err, FormData{})
		return
	}
	h.redirectLocalized(writer, request, current.language, h.planRoute(current.slug, slot))
}

// planRoute is the unlocalized plan route showing slot's part and week.
func (h *groupHandler) planRoute(groupSlug string, slot service.PlanSlot) string {
	query := planQuery{part: slot.SeasonPartID}
	if slot.Week == service.SeasonWeekA || slot.Week == service.SeasonWeekB {
		query.week = slot.Week
	}
	return "/groups/" + groupSlug + "/plan" + query.encode()
}

// planRequest authorizes a plan page: outsiders get 404 from groupRequest,
// members who may not see the plan (players and parents) get access denied.
func (h *groupHandler) planRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Plan == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanViewPlan() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) planSlotRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.planRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	slotID, err := pathPositiveID(request, "slotID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	return current, slotID, true
}

// planWriteError renders the plan page again with the error of a failed
// copy.
func (h *groupHandler) planWriteError(writer http.ResponseWriter, request *http.Request, current groupRequest, query planQuery, err error) {
	if errors.Is(err, service.ErrGroupNotFound) || errors.Is(err, service.ErrSeasonNotFound) {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	message, status := planErrorMessage(err)
	if message == "" {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	h.renderPlan(writer, request, current, query, []Alert{{Level: "error", Message: h.t(current.language, message)}}, formFailureStatus(request, status))
}

func (h *groupHandler) planSlotWriteError(writer http.ResponseWriter, request *http.Request, current groupRequest, slotID int64, err error, form FormData) {
	if errors.Is(err, service.ErrGroupNotFound) || errors.Is(err, service.ErrSeasonNotFound) || errors.Is(err, service.ErrPlanSlotNotFound) {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	message, status := planErrorMessage(err)
	if message == "" {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	if field := planErrorField(err); field != "" && form.Submitted {
		form.Errors = []FieldError{{Field: field, Message: h.t(current.language, message)}}
	}
	h.renderPlanSlot(writer, request, current, slotID, form, []Alert{{Level: "error", Message: h.t(current.language, message)}}, formFailureStatus(request, status))
}

// planErrorMessage maps a plan write error to its message and status; an
// empty message means an unexpected error.
func planErrorMessage(err error) (string, int) {
	switch {
	case errors.Is(err, ErrInvalidWeekGridChange):
		return "plan.error.invalid_change", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrGroupForbidden):
		return "groups.error.forbidden", http.StatusForbidden
	case errors.Is(err, service.ErrOptimisticLockConflict):
		return "groups.error.conflict", http.StatusConflict
	case errors.Is(err, service.ErrPlanSlotNotFound), errors.Is(err, service.ErrRecordNotFound):
		return "plan.error.slot_not_found", http.StatusNotFound
	case errors.Is(err, service.ErrResourceNotFound):
		return "plan.error.resource_not_found", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrResourceArchived):
		return "plan.error.resource_archived", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidPlanSlotWeek):
		return "plan.error.invalid_week", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidPlanSlotTime):
		return "plan.error.invalid_time", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidPlanSlotAges):
		return "plan.error.invalid_ages", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidPlanSlotTeams):
		return "plan.error.invalid_max_teams", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrPlanFull):
		return "plan.error.full", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidPlanCopy):
		return "plan.error.invalid_copy", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrPlanCopyNotConfirmed):
		return "plan.error.copy_confirm", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidGroup):
		return "groups.error.generic", http.StatusUnprocessableEntity
	}
	return "", 0
}

func planErrorField(err error) string {
	switch {
	case errors.Is(err, service.ErrResourceNotFound), errors.Is(err, service.ErrResourceArchived):
		return "resource_id"
	case errors.Is(err, service.ErrInvalidPlanSlotWeek):
		return "week"
	case errors.Is(err, service.ErrInvalidPlanSlotTime):
		return "time"
	case errors.Is(err, service.ErrInvalidPlanSlotAges):
		return "ages"
	case errors.Is(err, service.ErrInvalidPlanSlotTeams):
		return "max_teams"
	}
	return ""
}

func (h *groupHandler) renderPlanLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrSeasonNotFound), errors.Is(err, service.ErrPlanSlotNotFound):
		h.renderGroupNotFound(writer, request, language)
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, language)
	default:
		h.renderLoadError(writer, request, language, err)
	}
}

// renderPlan renders the plan section as a full page or HTMX fragment.
func (h *groupHandler) renderPlan(writer http.ResponseWriter, request *http.Request, current groupRequest, query planQuery, alerts []Alert, status int) {
	view, ok := h.planPage(writer, request, current, query)
	if !ok {
		return
	}
	h.renderGroupPage(writer, request, current, view, FormData{}, alerts, status)
}

// planPage loads the group shell and the plan view; it renders the error
// itself and returns false on failure.
func (h *groupHandler) planPage(writer http.ResponseWriter, request *http.Request, current groupRequest, query planQuery) (*GroupDetailView, bool) {
	ctx := request.Context()
	group, err := h.dependencies.Groups.Group(ctx, current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return nil, false
	}
	board, err := h.dependencies.Plan.Board(ctx, current.access, query.part)
	if err != nil {
		h.renderPlanLoadError(writer, request, current.language, err)
		return nil, false
	}
	view := h.groupShell(current, group, groupSectionPlan)
	layout := h.planLayout(current, board, query)
	if layout.view.Empty {
		view.Plan = layout.view
		return view, true
	}
	layout.config.CSRFToken = csrfToken(request)
	grid, err := BuildWeekGrid(layout.config, layout.items, h.renderer.WeekGridTranslator(current.language))
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return nil, false
	}
	layout.view.Grid = grid
	view.Plan = layout.view
	return view, true
}

// planLayoutResult is the plan view without its grid, the grid frame, and
// the grid items, so change requests validate against the grid the user
// saw.
type planLayoutResult struct {
	view   *PlanView
	config WeekGridConfig
	items  []WeekGridItem
}

func (h *groupHandler) planLayout(current groupRequest, board service.PlanBoard, query planQuery) planLayoutResult {
	language := current.language
	canEdit := current.access.CanManagePlan()
	view := &PlanView{
		L:            h.labels[language],
		CanEdit:      canEdit,
		SeasonsURL:   h.groupPath(language, current.slug, "/seasons"),
		ResourcesURL: h.groupPath(language, current.slug, "/resources"),
		PickerURL:    h.groupPath(language, current.slug, "/plan"),
	}
	if !board.Found {
		view.Empty = true
		return planLayoutResult{view: view}
	}
	part := board.Part
	query.part = part.ID
	view.PartID = part.ID
	view.SeasonName, view.PartName = board.Season.Name, part.Name
	view.PartDates = service.FormatSeasonDate(part.StartsOn) + " – " + service.FormatSeasonDate(part.EndsOn)
	view.Alternating = part.AlternatingWeeks
	view.SwitchURL = h.groupPath(language, current.slug, "/seasons/"+strconv.FormatInt(board.Season.ID, 10)+"/parts/"+strconv.FormatInt(part.ID, 10)+"/alternating")
	view.SeasonVersion = board.Season.Version
	for _, season := range board.Seasons {
		for _, candidate := range season.Parts {
			view.Parts = append(view.Parts, SelectOptionView{
				Value:    strconv.FormatInt(candidate.ID, 10),
				Label:    season.Name + ": " + candidate.Name,
				Selected: candidate.ID == part.ID,
			})
		}
	}
	week := ""
	if part.AlternatingWeeks {
		week = cmp.Or(query.week, service.SeasonWeekA)
		view.WeekLabel = h.t(language, "plan.week."+strings.ToLower(week))
	}
	query.week = week
	view.Week = week

	resources := make(map[int64]service.Resource, len(board.Resources))
	for _, resource := range board.Resources {
		resources[resource.ID] = resource
	}
	// Columns: active resources, then archived ones that still hold slots
	// of this part. The grid shows at most WeekGridMaxResources at once.
	var candidates []service.Resource
	for _, resource := range board.Resources {
		holdsSlot := slices.ContainsFunc(board.Slots, func(slot service.PlanSlot) bool { return slot.ResourceID == resource.ID })
		if !resource.Archived() || holdsSlot {
			candidates = append(candidates, resource)
		}
	}
	var columns []service.Resource
	for _, resource := range candidates {
		if slices.Contains(query.resources, resource.ID) && len(columns) < WeekGridMaxResources {
			columns = append(columns, resource)
		}
	}
	if len(columns) == 0 {
		columns = candidates[:min(len(candidates), WeekGridMaxResources)]
		query.resources = nil
	}
	if len(candidates) > WeekGridMaxResources {
		for _, resource := range candidates {
			view.ResourceFilter = append(view.ResourceFilter, PlanResourceOptionView{
				Value:   strconv.FormatInt(resource.ID, 10),
				Label:   h.planResourceLabel(language, resource),
				Checked: slices.ContainsFunc(columns, func(column service.Resource) bool { return column.ID == resource.ID }),
			})
		}
	}
	view.NoResources = !slices.ContainsFunc(columns, func(resource service.Resource) bool { return !resource.Archived() })

	var shown []service.PlanSlot
	for _, slot := range board.Slots {
		if week == "" || slot.InWeek(week) {
			shown = append(shown, slot)
		}
	}
	warningTexts := map[int64][]string{}
	for _, warning := range board.Warnings {
		if week != "" && warning.Week != service.SeasonWeekAll && warning.Week != week {
			continue
		}
		text := h.planWarningText(language, warning, resources[warning.ResourceID], part.AlternatingWeeks)
		view.Warnings = append(view.Warnings, PlanWarningView{Text: text})
		for _, id := range warning.SlotIDs {
			warningTexts[id] = append(warningTexts[id], text)
		}
	}

	config := WeekGridConfig{
		ID:          planGridID,
		StartMinute: planGridDefaultStart,
		EndMinute:   planGridDefaultEnd,
		Step:        planGridStep,
		Week:        week,
	}
	for _, slot := range shown {
		config.StartMinute = min(config.StartMinute, slot.StartMinute/60*60)
		config.EndMinute = max(config.EndMinute, min((slot.EndMinute+59)/60*60, 24*60))
	}
	for _, resource := range columns {
		config.Resources = append(config.Resources, WeekGridResource{Key: strconv.FormatInt(resource.ID, 10), Label: h.planResourceLabel(language, resource)})
	}
	if part.AlternatingWeeks {
		weekA, weekB := query, query
		weekA.week, weekB.week = service.SeasonWeekA, service.SeasonWeekB
		config.WeekAURL = h.planURL(language, current.slug, "", weekA)
		config.WeekBURL = h.planURL(language, current.slug, "", weekB)
	}
	if canEdit && !view.NoResources {
		config.MoveURL = h.planURL(language, current.slug, "/slots/move", query)
		config.CreateURL = h.planURL(language, current.slug, "/slots/create", query)
	}

	items := make([]WeekGridItem, 0, len(shown))
	for _, slot := range shown {
		resource := resources[slot.ResourceID]
		row := h.planSlotRow(language, current.slug, slot, resource, part.AlternatingWeeks)
		row.Warning = len(warningTexts[slot.ID]) > 0
		view.Slots = append(view.Slots, row)
		item := WeekGridItem{
			ID:          planItemID(slot),
			Weekday:     slot.Weekday,
			StartMinute: slot.StartMinute,
			EndMinute:   slot.EndMinute,
			Resource:    strconv.FormatInt(slot.ResourceID, 10),
			Label:       row.Ages,
			Detail:      strings.Join(nonEmpty(row.Week, row.MaxTeams), " · "),
			Variant:     WeekGridPrimary,
			Draggable:   canEdit && !resource.Archived(),
			Warning:     row.Warning,
			WarningText: strings.Join(warningTexts[slot.ID], " "),
		}
		if slot.Week != service.SeasonWeekAll {
			item.Variant = WeekGridSecondary
		}
		if resource.Archived() {
			item.Variant = WeekGridNeutral
		}
		items = append(items, item)
	}

	if canEdit {
		copyForm := &PlanCopyFormView{ActionURL: h.planURL(language, current.slug, "/copy", query), SlotCount: len(board.Slots)}
		for index, source := range board.CopySources {
			copyForm.Sources = append(copyForm.Sources, SelectOptionView{
				Value: strconv.FormatInt(source.Part.ID, 10),
				Label: h.tv(language, "plan.copy.source."+source.Kind, map[string]string{
					"season": source.Season.Name, "part": source.Part.Name, "count": strconv.Itoa(source.SlotCount),
				}),
				Selected: index == 0,
			})
		}
		copyForm.ReplaceText = h.tv(language, "plan.copy.replace", map[string]string{"count": strconv.Itoa(len(board.Slots))})
		view.Copy = copyForm
	}
	return planLayoutResult{view: view, config: config, items: items}
}

func (h *groupHandler) planResourceLabel(language locale.Code, resource service.Resource) string {
	if resource.ID == 0 {
		return h.t(language, "plan.resource.unknown")
	}
	if resource.Archived() {
		return h.tv(language, "plan.resource.archived", map[string]string{"name": resource.Name})
	}
	return resource.Name
}

func (h *groupHandler) planSlotRow(language locale.Code, groupSlug string, slot service.PlanSlot, resource service.Resource, alternating bool) PlanSlotRowView {
	row := PlanSlotRowView{
		ID:       slot.ID,
		Day:      h.t(language, "weekgrid.day."+strconv.Itoa(slot.Weekday)),
		Time:     service.FormatPlanMinute(slot.StartMinute) + "–" + service.FormatPlanMinute(slot.EndMinute),
		Resource: h.planResourceLabel(language, resource),
		Archived: resource.Archived(),
		URL:      h.groupPath(language, groupSlug, "/plan/slots/"+strconv.FormatInt(slot.ID, 10)),
	}
	if alternating || slot.Week != service.SeasonWeekAll {
		row.Week = h.t(language, "plan.slot_week."+strings.ToLower(slot.Week))
	}
	switch {
	case slot.MinAge != nil && slot.MaxAge != nil:
		row.Ages = h.tv(language, "plan.ages.range", map[string]string{"min": strconv.Itoa(*slot.MinAge), "max": strconv.Itoa(*slot.MaxAge)})
	case slot.MinAge != nil:
		row.Ages = h.tv(language, "plan.ages.from", map[string]string{"min": strconv.Itoa(*slot.MinAge)})
	case slot.MaxAge != nil:
		row.Ages = h.tv(language, "plan.ages.to", map[string]string{"max": strconv.Itoa(*slot.MaxAge)})
	default:
		row.Ages = h.t(language, "plan.ages.any")
	}
	if slot.MaxTeams != nil {
		row.MaxTeams = h.tv(language, "plan.max_teams.value", map[string]string{"count": strconv.Itoa(*slot.MaxTeams)})
	}
	return row
}

func (h *groupHandler) planWarningText(language locale.Code, warning service.PlanWarning, resource service.Resource, alternating bool) string {
	values := map[string]string{
		"resource": h.planResourceLabel(language, resource),
		"day":      h.t(language, "weekgrid.day."+strconv.Itoa(warning.Weekday)),
		"from":     service.FormatPlanMinute(warning.StartMinute),
		"to":       service.FormatPlanMinute(warning.EndMinute),
		"teams":    strconv.Itoa(warning.Teams),
		"limit":    strconv.Itoa(warning.Limit),
	}
	text := h.tv(language, "plan.warning.capacity", values)
	if alternating {
		text += " (" + h.t(language, "plan.slot_week."+strings.ToLower(warning.Week)) + ")"
	}
	return text
}

// renderPlanSlot renders one slot's page.
func (h *groupHandler) renderPlanSlot(writer http.ResponseWriter, request *http.Request, current groupRequest, slotID int64, form FormData, alerts []Alert, status int) {
	ctx := request.Context()
	group, err := h.dependencies.Groups.Group(ctx, current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	slot, err := h.dependencies.Plan.Slot(ctx, current.access, slotID)
	if err != nil {
		h.renderPlanLoadError(writer, request, current.language, err)
		return
	}
	board, err := h.dependencies.Plan.Board(ctx, current.access, slot.SeasonPartID)
	if err != nil {
		h.renderPlanLoadError(writer, request, current.language, err)
		return
	}
	language := current.language
	var resource service.Resource
	for _, candidate := range board.Resources {
		if candidate.ID == slot.ResourceID {
			resource = candidate
		}
	}
	slotURL := h.groupPath(language, current.slug, "/plan/slots/"+strconv.FormatInt(slot.ID, 10))
	detail := &PlanSlotDetailView{
		L:          h.labels[language],
		ID:         slot.ID,
		Version:    slot.Version,
		CanEdit:    current.access.CanManagePlan(),
		PlanURL:    h.path(language, h.planRoute(current.slug, slot)),
		ActionURL:  slotURL,
		DeleteURL:  slotURL + "/delete",
		SeasonName: board.Season.Name,
		PartName:   board.Part.Name,
		Row:        h.planSlotRow(language, current.slug, slot, resource, board.Part.AlternatingWeeks),
	}
	if detail.CanEdit {
		detail.Form = h.planSlotFormView(language, board, slot, form)
	}
	view := h.groupShell(current, group, groupSectionPlanSlot)
	view.PlanSlot = detail
	h.renderGroupPage(writer, request, current, view, form, alerts, status)
}

// planSlotFormView fills the slot form from submitted values after a failed
// write, else from the stored slot. Its resource choices are the active
// resources plus the slot's own (which may be archived).
func (h *groupHandler) planSlotFormView(language locale.Code, board service.PlanBoard, slot service.PlanSlot, form FormData) *PlanSlotFormView {
	view := &PlanSlotFormView{Alternating: board.Part.AlternatingWeeks}
	resourceID := strconv.FormatInt(slot.ResourceID, 10)
	week, weekday := slot.Week, strconv.Itoa(slot.Weekday)
	view.Start, view.End = service.FormatPlanMinute(slot.StartMinute), service.FormatPlanMinute(slot.EndMinute%(24*60))
	view.MinAge, view.MaxAge, view.MaxTeams = optionalString(slot.MinAge), optionalString(slot.MaxAge), optionalString(slot.MaxTeams)
	if form.Submitted {
		resourceID, week, weekday = form.Value("resource_id"), form.Value("week"), form.Value("weekday")
		view.Start, view.End = form.Value("start"), form.Value("end")
		view.MinAge, view.MaxAge, view.MaxTeams = form.Value("min_age"), form.Value("max_age"), form.Value("max_teams")
		view.ResourceError = form.Error("resource_id")
		view.WeekError = form.Error("week")
		view.TimeError = form.Error("time")
		view.AgesError = form.Error("ages")
		view.MaxTeamsError = form.Error("max_teams")
	}
	for _, resource := range board.Resources {
		if resource.Archived() && resource.ID != slot.ResourceID {
			continue
		}
		value := strconv.FormatInt(resource.ID, 10)
		view.Resources = append(view.Resources, SelectOptionView{Value: value, Label: h.planResourceLabel(language, resource), Selected: value == resourceID})
	}
	weeks := []string{service.SeasonWeekAll}
	if board.Part.AlternatingWeeks {
		weeks = []string{service.SeasonWeekAll, service.SeasonWeekA, service.SeasonWeekB}
	}
	for _, option := range weeks {
		view.Weeks = append(view.Weeks, SelectOptionView{Value: option, Label: h.t(language, "plan.slot_week."+strings.ToLower(option)), Selected: option == week})
	}
	for day := 1; day <= 7; day++ {
		value := strconv.Itoa(day)
		view.Days = append(view.Days, SelectOptionView{Value: value, Label: h.t(language, "weekgrid.day."+value), Selected: value == weekday})
	}
	return view
}

// planSlotForm reads slot input. Unparseable numbers and times become -1,
// which validation refuses; empty ages and team limits are none.
func planSlotForm(request *http.Request) (service.PlanSlotInput, FormData) {
	values := map[string]string{}
	for _, field := range []string{"resource_id", "week", "weekday", "start", "end", "min_age", "max_age", "max_teams"} {
		values[field] = strings.TrimSpace(request.PostFormValue(field))
	}
	resourceID, err := strconv.ParseInt(values["resource_id"], 10, 64)
	if err != nil {
		resourceID = -1
	}
	weekday, err := strconv.Atoi(values["weekday"])
	if err != nil {
		weekday = -1
	}
	end := parsePlanTime(values["end"])
	if end == 0 {
		// 00:00 as an end is midnight at the end of the day.
		end = 24 * 60
	}
	input := service.PlanSlotInput{
		ResourceID:  resourceID,
		Week:        values["week"],
		Weekday:     weekday,
		StartMinute: parsePlanTime(values["start"]),
		EndMinute:   end,
		MinAge:      optionalNumber(values["min_age"]),
		MaxAge:      optionalNumber(values["max_age"]),
		MaxTeams:    optionalNumber(values["max_teams"]),
	}
	return input, FormData{Submitted: true, Values: values}
}

// parsePlanTime reads HH:MM as minutes after midnight, or -1.
func parsePlanTime(raw string) int {
	hours, minutes, ok := strings.Cut(raw, ":")
	if !ok || len(hours) == 0 || len(hours) > 2 || len(minutes) != 2 {
		return -1
	}
	hour, err := strconv.Atoi(hours)
	if err != nil || hour < 0 || hour > 24 {
		return -1
	}
	minute, err := strconv.Atoi(minutes)
	if err != nil || minute < 0 || minute > 59 || (hour == 24 && minute != 0) {
		return -1
	}
	return hour*60 + minute
}

// optionalNumber reads an optional positive number: empty is 0 (none),
// anything unparseable or not positive is -1.
func optionalNumber(raw string) int {
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return -1
	}
	return value
}

func optionalString(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

// planItemID makes a slot's grid item ID: its ID and revision, so a move
// is revision-checked like a form.
func planItemID(slot service.PlanSlot) string {
	return strconv.FormatInt(slot.ID, 10) + "-" + strconv.FormatInt(slot.Version, 10)
}

func parsePlanItemID(raw string) (int64, int64, bool) {
	rawID, rawVersion, ok := strings.Cut(raw, "-")
	if !ok {
		return 0, 0, false
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, false
	}
	version, err := strconv.ParseInt(rawVersion, 10, 64)
	if err != nil || version < 0 {
		return 0, 0, false
	}
	return id, version, true
}

func nonEmpty(values ...string) []string {
	return slices.DeleteFunc(values, func(value string) bool { return value == "" })
}

// tv translates a message with placeholder values.
func (h *groupHandler) tv(language locale.Code, id string, values map[string]string) string {
	if h.renderer == nil || h.renderer.catalogs == nil {
		return id
	}
	text, err := h.renderer.catalogs.Translate(language, id, values)
	if err != nil {
		return id
	}
	return text
}
