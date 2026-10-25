package httpadapter

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// Training plan page sections.
const (
	groupSectionTrainingPlan = "training-plan"
	trainingGridID           = "training-plan-grid"
)

// TrainingPlanUseCases aggregates the training plan operations consumed by
// HTTP handlers.
type TrainingPlanUseCases interface {
	Board(ctx context.Context, access *service.GroupAccess, request service.TrainingBoardRequest) (service.TrainingBoard, error)
	Save(ctx context.Context, actor service.GroupActor, partID int64, revision string, trainings []service.PlanTraining) (service.TrainingChanges, error)
}

// ErrInvalidTrainingPlanForm identifies a malformed training plan form.
var ErrInvalidTrainingPlanForm = errors.New("http: invalid training plan form")

// TrainingPlanView is the training plan board of one season part. The board
// is a form: each training is a hidden "t" value
// "<id>:<team>:<resource>:<week>:<weekday>:<start>:<end>:<reason>", and the
// page posts the whole set to DraftURL (to re-render, add, remove, move, or
// suggest) or ActionURL (to save). The week grid sits beside the form and
// includes its values in its move requests.
type TrainingPlanView struct {
	L         map[string]string
	CSRFToken string
	CanEdit   bool
	// Empty is true when the group has no season part yet.
	Empty        bool
	SeasonsURL   string
	ResourcesURL string
	PickerURL    string
	ActionURL    string
	DraftURL     string
	DiscardURL   string
	// GenerateURL opens the page that generates events from the saved plan.
	GenerateURL string
	PartID      int64
	Parts       []SelectOptionView
	SeasonName  string
	PartName    string
	PartDates   string
	Alternating bool
	Week        string
	Revision    string
	Changed     bool
	Stale       bool
	// PreviousSeason names the season the suggestion copies from.
	PreviousSeason string
	StartOverText  string
	NoResources    bool
	ResourceFilter []PlanResourceOptionView
	Grid           *WeekGridView
	// Values are the hidden draft values of every training of the part.
	Values   []string
	Teams    []TrainingTeamView
	Rows     []TrainingRowView
	Warnings []string
	Add      *TrainingAddView
}

// TrainingTeamView is one team card.
type TrainingTeamView struct {
	ID       int64
	Name     string
	Ages     string
	URL      string
	Archived bool
	Coaches  string
	NoCoach  bool
	Count    string
}

// TrainingRowView is one training of the shown week in the list.
type TrainingRowView struct {
	Index       int
	Team        string
	Day         string
	Time        string
	Resource    string
	Week        string
	Slot        string
	Coaches     string
	New         bool
	Explanation string
	Warnings    []string
	RemoveLabel string
}

// TrainingAddView is the form that adds a training without dragging.
type TrainingAddView struct {
	Teams     []SelectOptionView
	Days      []SelectOptionView
	Resources []SelectOptionView
	Starts    []SelectOptionView
	Ends      []SelectOptionView
}

// registerTrainingPlanRoutes registers each group's training plan board.
func registerTrainingPlanRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/training-plan", groups.trainingPlan)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/training-plan", groups.saveTrainingPlan)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/training-plan/draft", groups.trainingPlanDraft)
	}
}

func (h *groupHandler) trainingPlanURL(language locale.Code, groupID int64, suffix string, query planQuery) string {
	return h.groupPath(language, groupID, "/training-plan"+suffix) + query.encode()
}

func (h *groupHandler) trainingPlan(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.trainingPlanRequest(writer, request)
	if !ok {
		return
	}
	h.renderTrainingPlan(writer, request, current, parsePlanQuery(request), service.TrainingBoardRequest{}, nil, http.StatusOK)
}

// trainingPlanRequest authorizes a training plan page: outsiders get 404
// from groupRequest, members who may not see the plan (players and parents)
// get access denied.
func (h *groupHandler) trainingPlanRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.TrainingPlan == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanViewPlan() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

// trainingPlanDraft re-renders the board with the posted draft: after a
// drag, an added or removed training, or a suggestion. Nothing is saved.
func (h *groupHandler) trainingPlanDraft(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.trainingPlanRequest(writer, request)
	if !ok {
		return
	}
	query := parsePlanQuery(request)
	if !current.access.CanManagePlan() {
		h.renderTrainingPlanError(writer, request, current, query, nil, service.ErrGroupForbidden)
		return
	}
	draft, err := parseTrainingDraft(request)
	if err != nil {
		h.renderTrainingPlanError(writer, request, current, query, nil, err)
		return
	}
	suggest := service.TrainingSuggestNone
	switch request.PostFormValue("suggest") {
	case service.TrainingSuggestFill:
		suggest = service.TrainingSuggestFill
	case service.TrainingSuggestReplace:
		suggest = service.TrainingSuggestReplace
	}
	if raw := request.PostFormValue("remove"); raw != "" {
		index, err := strconv.Atoi(raw)
		if err != nil || index < 0 || index >= len(draft.Items) {
			h.renderTrainingPlanError(writer, request, current, query, &draft, ErrInvalidTrainingPlanForm)
			return
		}
		draft.Items = slices.Delete(draft.Items, index, index+1)
	}
	move := request.PostFormValue("item") != ""
	// A team dragged from the pool carries the grid position; the add form
	// carries "add_*" fields and its button.
	dragged := request.PostFormValue("add_team") != "" && request.PostFormValue("weekday") != ""
	if move || dragged || request.PostFormValue("add") != "" {
		if !move && !dragged {
			for _, field := range []string{"weekday", "resource", "start", "end"} {
				request.PostForm.Set(field, request.PostFormValue("add_"+field))
			}
		}
		draft, err = h.applyTrainingGridChange(request, current, query, draft)
		if err != nil {
			h.renderTrainingPlanError(writer, request, current, query, &draft, err)
			return
		}
	}
	boardRequest := service.TrainingBoardRequest{PartID: query.part, Draft: &draft, Suggest: suggest}
	board, err := h.dependencies.TrainingPlan.Board(request.Context(), current.access, boardRequest)
	if err != nil {
		h.renderTrainingPlanError(writer, request, current, query, nil, err)
		return
	}
	var alerts []Alert
	switch {
	case suggest != service.TrainingSuggestNone && board.Suggested > 0:
		alerts = append(alerts, Alert{Level: "info", Message: h.tv(current.language, "training_plan.suggested", map[string]string{"count": strconv.Itoa(board.Suggested)})})
	case suggest != service.TrainingSuggestNone:
		alerts = append(alerts, Alert{Level: "info", Message: h.t(current.language, "training_plan.suggested_none")})
	}
	if suggest != service.TrainingSuggestNone && board.Unplaced > 0 {
		alerts = append(alerts, Alert{Level: "warning", Message: h.tv(current.language, "training_plan.unplaced", map[string]string{"count": strconv.Itoa(board.Unplaced)})})
	}
	if board.Stale() {
		alerts = append(alerts, Alert{Level: "warning", Message: h.t(current.language, "training_plan.stale")})
	}
	h.respondTrainingPlan(writer, request, current, query, board, alerts, http.StatusOK)
}

// applyTrainingGridChange applies a move of the training named by "item", or
// a new training of the team "add_team", at the grid position the request
// carries. The position is validated against the grid the draft produces.
func (h *groupHandler) applyTrainingGridChange(request *http.Request, current groupRequest, query planQuery, draft service.TrainingDraft) (service.TrainingDraft, error) {
	board, err := h.dependencies.TrainingPlan.Board(request.Context(), current.access, service.TrainingBoardRequest{PartID: query.part, Draft: &draft})
	if err != nil {
		return draft, err
	}
	layout := h.trainingLayout(current, board, query)
	move := request.PostFormValue("item") != ""
	change, err := ParseWeekGridChange(request, layout.config, move)
	if err != nil {
		return draft, ErrInvalidWeekGridChange
	}
	resourceID, err := strconv.ParseInt(change.Resource, 10, 64)
	if err != nil || resourceID <= 0 {
		return draft, ErrInvalidWeekGridChange
	}
	// The board may have dropped items of the draft; work on its list.
	items := make([]service.TrainingDraftItem, 0, len(board.Trainings))
	for _, row := range board.Trainings {
		items = append(items, service.TrainingDraftItem{PlanTraining: row.Training, Reason: row.Reason})
	}
	draft.Items = items
	if move {
		index, err := strconv.Atoi(change.ItemID)
		if err != nil || index < 0 || index >= len(items) {
			return draft, ErrInvalidWeekGridChange
		}
		item := &draft.Items[index]
		item.ResourceID, item.Weekday, item.StartMinute, item.EndMinute = resourceID, change.Weekday, change.StartMinute, change.EndMinute
		item.Reason = ""
		return draft, nil
	}
	teamID, err := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("add_team")), 10, 64)
	if err != nil || teamID <= 0 {
		return draft, ErrInvalidTrainingPlanForm
	}
	week := change.Week
	if week == "" {
		week = service.SeasonWeekAll
	}
	draft.Items = append(draft.Items, service.TrainingDraftItem{PlanTraining: service.PlanTraining{
		TeamID: teamID, ResourceID: resourceID, Week: week, Weekday: change.Weekday, StartMinute: change.StartMinute, EndMinute: change.EndMinute,
	}})
	return draft, nil
}

func (h *groupHandler) saveTrainingPlan(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.trainingPlanRequest(writer, request)
	if !ok {
		return
	}
	query := parsePlanQuery(request)
	if query.part == 0 {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	draft, err := parseTrainingDraft(request)
	if err != nil {
		h.renderTrainingPlanError(writer, request, current, query, nil, err)
		return
	}
	trainings := make([]service.PlanTraining, 0, len(draft.Items))
	for _, item := range draft.Items {
		trainings = append(trainings, item.PlanTraining)
	}
	changes, err := h.dependencies.TrainingPlan.Save(request.Context(), current.actor(), query.part, draft.Revision, trainings)
	if err != nil {
		h.renderTrainingPlanError(writer, request, current, query, &draft, err)
		return
	}
	if !IsHTMX(request) {
		http.Redirect(writer, request, h.trainingPlanURL(current.language, current.access.GroupID(), "", query), http.StatusSeeOther)
		return
	}
	message := h.t(current.language, "training_plan.no_changes")
	if changes.Total() > 0 {
		message = h.tv(current.language, "training_plan.saved", map[string]string{
			"added": strconv.Itoa(changes.Added), "moved": strconv.Itoa(changes.Moved), "removed": strconv.Itoa(changes.Removed),
		})
	}
	board, err := h.dependencies.TrainingPlan.Board(request.Context(), current.access, service.TrainingBoardRequest{PartID: query.part})
	if err != nil {
		h.renderTrainingPlanError(writer, request, current, query, nil, err)
		return
	}
	h.respondTrainingPlan(writer, request, current, query, board, []Alert{{Level: "success", Message: message}}, http.StatusOK)
}

// parseTrainingDraft reads the board form. Malformed values are refused.
func parseTrainingDraft(request *http.Request) (service.TrainingDraft, error) {
	if err := request.ParseForm(); err != nil {
		return service.TrainingDraft{}, ErrInvalidTrainingPlanForm
	}
	form := request.PostForm
	draft := service.TrainingDraft{Revision: strings.TrimSpace(form.Get("revision"))}
	if len(draft.Revision) > 64 {
		return service.TrainingDraft{}, ErrInvalidTrainingPlanForm
	}
	if len(form["t"]) > service.MaxTrainingsPerPart {
		return service.TrainingDraft{}, service.ErrTrainingPlanTooLarge
	}
	for _, raw := range form["t"] {
		item, ok := parseTrainingValue(raw)
		if !ok {
			return service.TrainingDraft{}, ErrInvalidTrainingPlanForm
		}
		draft.Items = append(draft.Items, item)
	}
	return draft, nil
}

// trainingValue encodes a training as a hidden form value.
func trainingValue(training service.PlanTraining, reason string) string {
	return strings.Join([]string{
		strconv.FormatInt(training.ID, 10), strconv.FormatInt(training.TeamID, 10), strconv.FormatInt(training.ResourceID, 10),
		training.Week, strconv.Itoa(training.Weekday), strconv.Itoa(training.StartMinute), strconv.Itoa(training.EndMinute), trainingReasonCode(reason),
	}, ":")
}

func trainingReasonCode(reason string) string {
	switch reason {
	case service.TrainingSuggestionPreviousSeason:
		return "p"
	case service.TrainingSuggestionSlot:
		return "s"
	}
	return ""
}

func parseTrainingValue(raw string) (service.TrainingDraftItem, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 8 {
		return service.TrainingDraftItem{}, false
	}
	numbers := make([]int64, 0, 6)
	for _, index := range []int{0, 1, 2, 4, 5, 6} {
		number, err := strconv.ParseInt(parts[index], 10, 64)
		if err != nil || number < 0 || number > 1<<40 {
			return service.TrainingDraftItem{}, false
		}
		numbers = append(numbers, number)
	}
	week := parts[3]
	if week != service.SeasonWeekA && week != service.SeasonWeekB && week != service.SeasonWeekAll {
		return service.TrainingDraftItem{}, false
	}
	item := service.TrainingDraftItem{PlanTraining: service.PlanTraining{
		ID: numbers[0], TeamID: numbers[1], ResourceID: numbers[2], Week: week,
		Weekday: int(numbers[3]), StartMinute: int(numbers[4]), EndMinute: int(numbers[5]),
	}}
	switch parts[7] {
	case "p":
		item.Reason = service.TrainingSuggestionPreviousSeason
	case "s":
		item.Reason = service.TrainingSuggestionSlot
	case "":
	default:
		return service.TrainingDraftItem{}, false
	}
	return item, true
}

// renderTrainingPlanError answers a failed draft or save. Validation errors
// and conflicts show the board again with the posted draft (422 for HTMX)
// and an error alert; a part that is gone is 404.
func (h *groupHandler) renderTrainingPlanError(writer http.ResponseWriter, request *http.Request, current groupRequest, query planQuery, draft *service.TrainingDraft, err error) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrSeasonNotFound), errors.Is(err, service.ErrRecordNotFound):
		h.renderGroupNotFound(writer, request, current.language)
		return
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, current.language)
		return
	}
	message, status := trainingPlanErrorMessage(err)
	if message == "" {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	board, loadErr := h.dependencies.TrainingPlan.Board(request.Context(), current.access, service.TrainingBoardRequest{PartID: query.part, Draft: draft})
	if loadErr != nil {
		// The posted draft itself may be what failed; show the saved board.
		board, loadErr = h.dependencies.TrainingPlan.Board(request.Context(), current.access, service.TrainingBoardRequest{PartID: query.part})
		if loadErr != nil {
			h.renderTrainingPlanLoadError(writer, request, current.language, loadErr)
			return
		}
	}
	h.respondTrainingPlan(writer, request, current, query, board, []Alert{{Level: "error", Message: h.t(current.language, message)}}, formFailureStatus(request, status))
}

// trainingPlanErrorMessage maps a board error to its message and status; an
// empty message means an unexpected error.
func trainingPlanErrorMessage(err error) (string, int) {
	switch {
	case errors.Is(err, service.ErrOptimisticLockConflict):
		return "training_plan.stale", http.StatusConflict
	case errors.Is(err, ErrInvalidWeekGridChange):
		return "training_plan.error.invalid_change", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidTrainingTime):
		return "training_plan.error.invalid_time", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrInvalidTrainingWeek):
		return "training_plan.error.invalid_week", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrTrainingPlanTooLarge):
		return "training_plan.error.too_large", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrTeamNotFound):
		return "training_plan.error.team_not_found", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrResourceNotFound):
		return "training_plan.error.resource_not_found", http.StatusUnprocessableEntity
	case errors.Is(err, ErrInvalidTrainingPlanForm), errors.Is(err, service.ErrInvalidGroup):
		return "groups.error.generic", http.StatusUnprocessableEntity
	}
	return "", 0
}

func (h *groupHandler) renderTrainingPlanLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrSeasonNotFound):
		h.renderGroupNotFound(writer, request, language)
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, language)
	default:
		h.renderLoadError(writer, request, language, err)
	}
}

// renderTrainingPlan loads the board and renders the page.
func (h *groupHandler) renderTrainingPlan(writer http.ResponseWriter, request *http.Request, current groupRequest, query planQuery, boardRequest service.TrainingBoardRequest, alerts []Alert, status int) {
	boardRequest.PartID = query.part
	board, err := h.dependencies.TrainingPlan.Board(request.Context(), current.access, boardRequest)
	if err != nil {
		h.renderTrainingPlanLoadError(writer, request, current.language, err)
		return
	}
	h.respondTrainingPlan(writer, request, current, query, board, alerts, status)
}

// respondTrainingPlan answers with the board: HTMX posts get the board
// fragment with out-of-band alerts (retargeted at the board, whichever
// element sent the request), everything else the full page.
func (h *groupHandler) respondTrainingPlan(writer http.ResponseWriter, request *http.Request, current groupRequest, query planQuery, board service.TrainingBoard, alerts []Alert, status int) {
	group, err := h.dependencies.Groups.Group(request.Context(), current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	view := h.groupShell(current, group, groupSectionTrainingPlan)
	layout := h.trainingLayout(current, board, query)
	plan := layout.view
	plan.CSRFToken = csrfToken(request)
	if !plan.Empty {
		layout.config.CSRFToken = plan.CSRFToken
		grid, err := BuildWeekGrid(layout.config, layout.items, h.renderer.WeekGridTranslator(current.language))
		if err != nil {
			h.renderError(writer, request, http.StatusInternalServerError)
			return
		}
		plan.Grid = grid
		plan.Add = h.trainingAddView(current.language, board, layout.config)
	}
	view.TrainingPlan = plan
	if IsHTMX(request) && request.Method == http.MethodPost {
		writer.Header().Add("Vary", "HX-Request")
		writer.Header().Set("HX-Retarget", "#training-plan-board")
		writer.Header().Set("HX-Reswap", "outerHTML")
		page := PageData{Language: string(current.language), CSRFToken: csrfToken(request), Alerts: alerts, Group: view}
		if err := h.renderer.renderStatus(writer, "training-plan-swap", page, status); err != nil {
			h.renderError(writer, request, http.StatusInternalServerError)
		}
		return
	}
	h.renderGroupPage(writer, request, current, view, FormData{}, alerts, status)
}

type trainingLayoutResult struct {
	view   *TrainingPlanView
	config WeekGridConfig
	items  []WeekGridItem
}

// trainingLayout builds the board view, the grid frame, and the grid items.
// Change requests validate against the frame the draft produces.
func (h *groupHandler) trainingLayout(current groupRequest, board service.TrainingBoard, query planQuery) trainingLayoutResult {
	language := current.language
	groupID := current.access.GroupID()
	view := &TrainingPlanView{
		L:            h.labels[language],
		CanEdit:      board.CanEdit,
		SeasonsURL:   h.groupPath(language, groupID, "/seasons"),
		ResourcesURL: h.groupPath(language, groupID, "/resources"),
		PickerURL:    h.groupPath(language, groupID, "/training-plan"),
	}
	if !board.Found {
		view.Empty = true
		return trainingLayoutResult{view: view}
	}
	part := board.Part
	query.part = part.ID
	view.PartID = part.ID
	view.SeasonName, view.PartName = board.Season.Name, part.Name
	view.PartDates = service.FormatSeasonDate(part.StartsOn) + " – " + service.FormatSeasonDate(part.EndsOn)
	view.Alternating = part.AlternatingWeeks
	view.Revision = board.Revision
	view.Changed = board.Changed
	view.Stale = board.Stale()
	if board.HasPrevious {
		view.PreviousSeason = board.PreviousSeason.Name
	}
	view.StartOverText = h.t(language, "training_plan.start_over_confirm")
	for _, season := range board.Seasons {
		for _, candidate := range season.Parts {
			view.Parts = append(view.Parts, SelectOptionView{
				Value: strconv.FormatInt(candidate.ID, 10), Label: season.Name + ": " + candidate.Name, Selected: candidate.ID == part.ID,
			})
		}
	}
	week := ""
	if part.AlternatingWeeks {
		week = cmp.Or(query.week, service.SeasonWeekA)
	}
	query.week = week
	view.Week = week
	view.ActionURL = h.trainingPlanURL(language, groupID, "", query)
	view.DraftURL = h.trainingPlanURL(language, groupID, "/draft", query)
	view.DiscardURL = h.trainingPlanURL(language, groupID, "", query)
	view.GenerateURL = h.eventGenerationURL(language, groupID) + "?part=" + strconv.FormatInt(part.ID, 10)

	resources := make(map[int64]service.Resource, len(board.Resources))
	for _, resource := range board.Resources {
		resources[resource.ID] = resource
	}
	teams := make(map[int64]service.TrainingBoardTeam, len(board.Teams))
	for _, team := range board.Teams {
		teams[team.Team.ID] = team
	}
	// Columns: active resources, then archived ones that still hold slots or
	// trainings of this part. The grid shows at most WeekGridMaxResources.
	var candidates []service.Resource
	for _, resource := range board.Resources {
		holds := slices.ContainsFunc(board.Slots, func(slot service.PlanSlot) bool { return slot.ResourceID == resource.ID }) ||
			slices.ContainsFunc(board.Trainings, func(row service.BoardTraining) bool { return row.Training.ResourceID == resource.ID })
		if !resource.Archived() || holds {
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
	}
	if len(candidates) > WeekGridMaxResources {
		for _, resource := range candidates {
			view.ResourceFilter = append(view.ResourceFilter, PlanResourceOptionView{
				Value: strconv.FormatInt(resource.ID, 10), Label: h.planResourceLabel(language, resource),
				Checked: slices.ContainsFunc(columns, func(column service.Resource) bool { return column.ID == resource.ID }),
			})
		}
	}
	view.NoResources = !slices.ContainsFunc(columns, func(resource service.Resource) bool { return !resource.Archived() })

	inWeek := func(training service.PlanTraining) bool {
		return week == "" || training.Week == service.SeasonWeekAll || training.Week == week
	}
	config := WeekGridConfig{
		ID: trainingGridID, StartMinute: planGridDefaultStart, EndMinute: planGridDefaultEnd, Step: planGridStep, Week: week,
		DragHint: h.t(language, "training_plan.drag_hint"),
	}
	var shownSlots []service.PlanSlot
	for _, slot := range board.Slots {
		if week == "" || slot.InWeek(week) {
			shownSlots = append(shownSlots, slot)
			config.StartMinute = min(config.StartMinute, slot.StartMinute/60*60)
			config.EndMinute = max(config.EndMinute, min((slot.EndMinute+59)/60*60, 24*60))
		}
	}
	for _, row := range board.Trainings {
		if inWeek(row.Training) {
			config.StartMinute = min(config.StartMinute, row.Training.StartMinute/60*60)
			config.EndMinute = max(config.EndMinute, min((row.Training.EndMinute+59)/60*60, 24*60))
		}
	}
	for _, resource := range columns {
		config.Resources = append(config.Resources, WeekGridResource{Key: strconv.FormatInt(resource.ID, 10), Label: h.planResourceLabel(language, resource)})
	}
	if part.AlternatingWeeks {
		weekA, weekB := query, query
		weekA.week, weekB.week = service.SeasonWeekA, service.SeasonWeekB
		config.WeekAURL = h.trainingPlanURL(language, groupID, "", weekA)
		config.WeekBURL = h.trainingPlanURL(language, groupID, "", weekB)
	}
	if board.CanEdit && !view.NoResources {
		config.MoveURL = view.DraftURL
	}

	var items []WeekGridItem
	for _, slot := range shownSlots {
		row := h.planSlotRow(language, groupID, slot, resources[slot.ResourceID], part.AlternatingWeeks)
		items = append(items, WeekGridItem{
			ID: "s" + strconv.FormatInt(slot.ID, 10), Weekday: slot.Weekday, StartMinute: slot.StartMinute, EndMinute: slot.EndMinute,
			Resource: strconv.FormatInt(slot.ResourceID, 10), Label: row.Ages, Detail: strings.Join(nonEmpty(row.Week, row.MaxTeams), " · "),
			Variant: WeekGridNeutral, Background: true,
		})
	}
	canEdit := board.CanEdit
	for _, row := range board.Trainings {
		training := row.Training
		view.Values = append(view.Values, trainingValue(training, row.Reason))
		team := teams[training.TeamID]
		resource := resources[training.ResourceID]
		coaches := h.trainingCoaches(language, team.Coaches)
		var warnings []string
		for _, warning := range row.Warnings {
			if !board.CanEdit {
				break
			}
			warnings = append(warnings, h.trainingWarningText(language, warning, board, teams))
		}
		if !inWeek(training) {
			continue
		}
		item := WeekGridItem{
			ID: strconv.Itoa(row.Index), Weekday: training.Weekday, StartMinute: training.StartMinute, EndMinute: training.EndMinute,
			Resource: strconv.FormatInt(training.ResourceID, 10), Label: team.Team.Name, Detail: coaches,
			Variant: WeekGridPrimary, Draggable: canEdit, Warning: len(warnings) > 0, WarningText: strings.Join(warnings, " "),
		}
		if canEdit && !row.Saved {
			item.Badges = []string{h.t(language, "training_plan.new")}
		}
		if training.Week != service.SeasonWeekAll {
			item.Variant = WeekGridSecondary
		}
		items = append(items, item)
		tr := TrainingRowView{
			Index:       row.Index,
			Team:        team.Team.Name,
			Day:         h.t(language, "weekgrid.day."+strconv.Itoa(training.Weekday)),
			Time:        service.FormatPlanMinute(training.StartMinute) + "–" + service.FormatPlanMinute(training.EndMinute),
			Resource:    h.planResourceLabel(language, resource),
			Coaches:     coaches,
			New:         canEdit && !row.Saved,
			Warnings:    warnings,
			RemoveLabel: h.tv(language, "training_plan.remove_label", map[string]string{"team": team.Team.Name}),
		}
		if part.AlternatingWeeks {
			tr.Week = h.t(language, "plan.slot_week."+strings.ToLower(training.Week))
		}
		if row.HasSlot {
			slotRow := h.planSlotRow(language, groupID, row.Slot, resource, part.AlternatingWeeks)
			tr.Slot = slotRow.Time
		}
		if row.Reason != "" {
			tr.Explanation = h.t(language, "training_plan.reason."+row.Reason)
		}
		view.Rows = append(view.Rows, tr)
	}
	for _, team := range board.Teams {
		card := TrainingTeamView{
			ID: team.Team.ID, Name: team.Team.Name, URL: h.groupPath(language, groupID, "/teams/"+strconv.FormatInt(team.Team.ID, 10)),
			Archived: team.Team.Archived(), Coaches: h.trainingCoaches(language, team.Coaches), NoCoach: len(team.Coaches) == 0,
			Count: h.tv(language, "training_plan.team_trainings", map[string]string{"count": strconv.Itoa(team.Trainings)}),
			Ages:  h.tv(language, "coach_board.ages", map[string]string{"min": strconv.Itoa(team.Ages.Min), "max": strconv.Itoa(team.Ages.Max)}),
		}
		view.Teams = append(view.Teams, card)
	}
	for _, warning := range board.Warnings {
		if !board.CanEdit || warning.Index < 0 || warning.Index >= len(board.Trainings) {
			continue
		}
		row := board.Trainings[warning.Index]
		training := row.Training
		team := teams[training.TeamID]
		view.Warnings = append(view.Warnings, h.tv(language, "training_plan.warning_line", map[string]string{
			"team":     team.Team.Name,
			"day":      h.t(language, "weekgrid.day."+strconv.Itoa(training.Weekday)),
			"time":     service.FormatPlanMinute(training.StartMinute) + "–" + service.FormatPlanMinute(training.EndMinute),
			"resource": h.planResourceLabel(language, resources[training.ResourceID]),
			"warning":  h.trainingWarningText(language, warning, board, teams),
		}))
	}
	return trainingLayoutResult{view: view, config: config, items: items}
}

// trainingAddView builds the selects of the add form.
func (h *groupHandler) trainingAddView(language locale.Code, board service.TrainingBoard, config WeekGridConfig) *TrainingAddView {
	if !board.CanEdit || config.MoveURL == "" {
		return nil
	}
	add := &TrainingAddView{}
	for _, team := range board.Teams {
		if !team.Team.Archived() {
			add.Teams = append(add.Teams, SelectOptionView{Value: strconv.FormatInt(team.Team.ID, 10), Label: team.Team.Name})
		}
	}
	for day := 1; day <= 7; day++ {
		add.Days = append(add.Days, SelectOptionView{Value: strconv.Itoa(day), Label: h.t(language, "weekgrid.day."+strconv.Itoa(day))})
	}
	for _, resource := range config.Resources {
		add.Resources = append(add.Resources, SelectOptionView{Value: resource.Key, Label: resource.Label})
	}
	defaultStart := min(max(16*60, config.StartMinute), config.EndMinute-config.Step)
	for minute := config.StartMinute; minute <= config.EndMinute; minute += config.Step {
		option := SelectOptionView{Value: strconv.Itoa(minute), Label: service.FormatPlanMinute(minute % (24 * 60))}
		if minute == 24*60 {
			option.Label = "24:00"
		}
		if minute < config.EndMinute {
			start := option
			start.Selected = minute == defaultStart
			add.Starts = append(add.Starts, start)
		}
		if minute > config.StartMinute {
			end := option
			end.Selected = minute == min(defaultStart+90, config.EndMinute)
			add.Ends = append(add.Ends, end)
		}
	}
	return add
}

func (h *groupHandler) trainingCoaches(language locale.Code, coaches []service.TrainingBoardCoach) string {
	names := make([]string, 0, len(coaches))
	for _, coach := range coaches {
		names = append(names, h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, nil))
	}
	return strings.Join(names, ", ")
}

// trainingWarningText explains one warning.
func (h *groupHandler) trainingWarningText(language locale.Code, warning service.TrainingWarning, board service.TrainingBoard, teams map[int64]service.TrainingBoardTeam) string {
	values := map[string]string{
		"teams": strconv.Itoa(warning.Teams),
		"limit": strconv.Itoa(warning.Limit),
	}
	if warning.CoachUserID > 0 {
		for _, team := range teams {
			for _, coach := range team.Coaches {
				if coach.UserID == warning.CoachUserID {
					values["coach"] = h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, nil)
				}
			}
		}
	}
	if warning.Other >= 0 && warning.Other < len(board.Trainings) {
		other := board.Trainings[warning.Other].Training
		values["other"] = h.t(language, "weekgrid.day_short."+strconv.Itoa(other.Weekday)) + " " +
			service.FormatPlanMinute(other.StartMinute) + "–" + service.FormatPlanMinute(other.EndMinute)
		values["other_team"] = teams[other.TeamID].Team.Name
	}
	if warning.Kind == service.TrainingWarningSlotAges {
		values["team_ages"] = h.tv(language, "coach_board.ages", map[string]string{"min": strconv.Itoa(warning.TeamAges.Min), "max": strconv.Itoa(warning.TeamAges.Max)})
		switch {
		case warning.SlotAges.Min > 0 && warning.SlotAges.Max > 0:
			values["slot_ages"] = h.tv(language, "plan.ages.range", map[string]string{"min": strconv.Itoa(warning.SlotAges.Min), "max": strconv.Itoa(warning.SlotAges.Max)})
		case warning.SlotAges.Min > 0:
			values["slot_ages"] = h.tv(language, "plan.ages.from", map[string]string{"min": strconv.Itoa(warning.SlotAges.Min)})
		default:
			values["slot_ages"] = h.tv(language, "plan.ages.to", map[string]string{"max": strconv.Itoa(warning.SlotAges.Max)})
		}
	}
	return h.tv(language, "training_plan.warning."+warning.Kind, values)
}
