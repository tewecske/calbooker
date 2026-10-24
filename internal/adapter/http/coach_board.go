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

// groupSectionCoachBoard is the coach assignment board of a season.
const groupSectionCoachBoard = "coach-board"

// CoachBoardUseCases aggregates the coach board operations consumed by HTTP
// handlers.
type CoachBoardUseCases interface {
	Board(ctx context.Context, access *service.GroupAccess, request service.CoachBoardRequest) (service.CoachBoard, error)
	Save(ctx context.Context, actor service.GroupActor, seasonID int64, revision string, assignments []service.CoachAssignment) (service.CoachAssignmentChanges, error)
}

// CoachBoardView is the coach assignment board of a season. The board is a
// form: each assignment is a hidden "a" value "<team>:<coach>", head coach
// marks are "head" checkboxes, suggestion explanations ride along as
// "suggested" values, and the page posts the whole set to DraftURL (to
// re-render, add, remove, or suggest) or ActionURL (to save).
type CoachBoardView struct {
	L              map[string]string
	CSRFToken      string
	CanEdit        bool
	SeasonName     string
	SeasonDates    string
	SeasonURL      string
	ActionURL      string
	DraftURL       string
	Revision       string
	Changed        bool
	Stale          bool
	PreviousSeason string
	StartOverText  string
	Teams          []CoachBoardTeamView
	Pool           []CoachBoardCoachView
	// AddOptions are the coaches offered by each team's add select.
	AddOptions []SelectOptionView
	Warnings   []string
}

// CoachBoardTeamView is one team on the board.
type CoachBoardTeamView struct {
	ID       int64
	Name     string
	Ages     string
	URL      string
	Archived bool
	AddLabel string
	Chips    []CoachBoardChipView
}

// CoachBoardChipView is one coach on a team.
type CoachBoardChipView struct {
	Key            string
	CoachID        int64
	Label          string
	Head           bool
	New            bool
	Explanation    string
	SuggestedValue string
	Warnings       []string
	HeadLabel      string
	RemoveLabel    string
}

// CoachBoardCoachView is one card of the coach pool.
type CoachBoardCoachView struct {
	ID             int64
	Label          string
	Qualification  string
	Teams          string
	NoAvailability bool
}

// registerCoachBoardRoutes registers each season's coach board.
func registerCoachBoardRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/seasons/{seasonID}/coach-board", groups.coachBoard)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/seasons/{seasonID}/coach-board", groups.saveCoachBoard)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/seasons/{seasonID}/coach-board/draft", groups.coachBoardDraft)
	}
}

func (h *groupHandler) coachBoard(writer http.ResponseWriter, request *http.Request) {
	current, seasonID, ok := h.coachBoardRequest(writer, request)
	if !ok {
		return
	}
	h.renderCoachBoard(writer, request, current, service.CoachBoardRequest{SeasonID: seasonID}, nil, http.StatusOK)
}

// coachBoardDraft re-renders the board with the posted draft: after a drag,
// an added or removed coach, or a suggestion. Nothing is saved.
func (h *groupHandler) coachBoardDraft(writer http.ResponseWriter, request *http.Request) {
	current, seasonID, ok := h.coachBoardRequest(writer, request)
	if !ok {
		return
	}
	if !current.access.CanManageSeasons() {
		h.renderCoachBoardError(writer, request, current, seasonID, nil, service.ErrGroupForbidden)
		return
	}
	draft, err := parseCoachBoardDraft(request)
	if err != nil {
		h.renderCoachBoardError(writer, request, current, seasonID, nil, err)
		return
	}
	suggest := service.CoachSuggestNone
	switch request.PostFormValue("suggest") {
	case service.CoachSuggestFill:
		suggest = service.CoachSuggestFill
	case service.CoachSuggestReplace:
		suggest = service.CoachSuggestReplace
	}
	boardRequest := service.CoachBoardRequest{SeasonID: seasonID, Draft: &draft, Suggest: suggest}
	board, err := h.dependencies.CoachBoard.Board(request.Context(), current.access, boardRequest)
	if err != nil {
		h.renderCoachBoardError(writer, request, current, seasonID, nil, err)
		return
	}
	var alerts []Alert
	switch {
	case suggest != service.CoachSuggestNone && board.Suggested > 0:
		alerts = append(alerts, Alert{Level: "info", Message: h.tv(current.language, "coach_board.suggested", map[string]string{"count": strconv.Itoa(board.Suggested)})})
	case suggest != service.CoachSuggestNone:
		alerts = append(alerts, Alert{Level: "info", Message: h.t(current.language, "coach_board.suggested_none")})
	}
	if board.Stale() {
		alerts = append(alerts, Alert{Level: "warning", Message: h.t(current.language, "coach_board.stale")})
	}
	h.respondCoachBoard(writer, request, current, board, alerts, http.StatusOK)
}

func (h *groupHandler) saveCoachBoard(writer http.ResponseWriter, request *http.Request) {
	current, seasonID, ok := h.coachBoardRequest(writer, request)
	if !ok {
		return
	}
	draft, err := parseCoachBoardDraft(request)
	if err != nil {
		h.renderCoachBoardError(writer, request, current, seasonID, nil, err)
		return
	}
	changes, err := h.dependencies.CoachBoard.Save(request.Context(), current.actor(), seasonID, draft.Revision, draft.Assignments)
	if err != nil {
		h.renderCoachBoardError(writer, request, current, seasonID, &draft, err)
		return
	}
	if !IsHTMX(request) {
		h.redirectLocalized(writer, request, current.language, "/groups/"+strconv.FormatInt(current.access.GroupID(), 10)+"/seasons/"+strconv.FormatInt(seasonID, 10)+"/coach-board")
		return
	}
	message := h.t(current.language, "coach_board.no_changes")
	if changes.Total() > 0 {
		message = h.tv(current.language, "coach_board.saved", map[string]string{
			"added": strconv.Itoa(changes.Added), "removed": strconv.Itoa(changes.Removed), "head": strconv.Itoa(changes.HeadChanged),
		})
	}
	board, err := h.dependencies.CoachBoard.Board(request.Context(), current.access, service.CoachBoardRequest{SeasonID: seasonID})
	if err != nil {
		h.renderCoachBoardError(writer, request, current, seasonID, nil, err)
		return
	}
	h.respondCoachBoard(writer, request, current, board, []Alert{{Level: "success", Message: message}}, http.StatusOK)
}

// coachBoardRequest authorizes a coach board page: outsiders get 404 from
// groupRequest, members who may not see seasons (players and parents) get
// access denied.
func (h *groupHandler) coachBoardRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	if h.dependencies.CoachBoard == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, 0, false
	}
	if !current.access.CanViewSeasons() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	seasonID, err := pathPositiveID(request, "seasonID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	return current, seasonID, true
}

// parseCoachBoardDraft reads the board form. Malformed values are refused
// (ErrInvalidCoachBoard); unknown teams and coaches are left to the service.
func parseCoachBoardDraft(request *http.Request) (service.CoachBoardDraft, error) {
	if err := request.ParseForm(); err != nil {
		return service.CoachBoardDraft{}, ErrInvalidCoachBoard
	}
	form := request.PostForm
	draft := service.CoachBoardDraft{Revision: strings.TrimSpace(form.Get("revision"))}
	if len(draft.Revision) > 64 {
		return service.CoachBoardDraft{}, ErrInvalidCoachBoard
	}
	if len(form["a"])+len(form["add"]) > service.MaxCoachBoardAssignments || len(form["head"]) > service.MaxCoachBoardAssignments || len(form["suggested"]) > service.MaxCoachBoardAssignments {
		return service.CoachBoardDraft{}, service.ErrCoachBoardTooLarge
	}
	heads := make(map[[2]int64]bool)
	for _, raw := range form["head"] {
		team, coach, ok := parseCoachBoardKey(raw)
		if !ok {
			return service.CoachBoardDraft{}, ErrInvalidCoachBoard
		}
		heads[[2]int64{team, coach}] = true
	}
	removed := [2]int64{}
	if raw := form.Get("remove"); raw != "" {
		team, coach, ok := parseCoachBoardKey(raw)
		if !ok {
			return service.CoachBoardDraft{}, ErrInvalidCoachBoard
		}
		removed = [2]int64{team, coach}
	}
	// "add" holds the per-team add selects ("" when nothing is chosen).
	values := append(form["a"][:len(form["a"]):len(form["a"])], form["add"]...)
	for _, raw := range values {
		if raw == "" {
			continue
		}
		team, coach, ok := parseCoachBoardKey(raw)
		if !ok {
			return service.CoachBoardDraft{}, ErrInvalidCoachBoard
		}
		key := [2]int64{team, coach}
		if key == removed {
			continue
		}
		draft.Assignments = append(draft.Assignments, service.CoachAssignment{TeamID: team, CoachUserID: coach, Head: heads[key]})
	}
	for _, raw := range form["suggested"] {
		suggested, ok := parseCoachSuggestion(raw)
		if !ok {
			return service.CoachBoardDraft{}, ErrInvalidCoachBoard
		}
		draft.Suggestions = append(draft.Suggestions, suggested)
	}
	return draft, nil
}

// ErrInvalidCoachBoard identifies a malformed coach board form.
var ErrInvalidCoachBoard = errors.New("http: invalid coach board form")

// parseCoachBoardKey reads "<team>:<coach>".
func parseCoachBoardKey(raw string) (int64, int64, bool) {
	teamRaw, coachRaw, found := strings.Cut(strings.TrimSpace(raw), ":")
	if !found {
		return 0, 0, false
	}
	team, err := strconv.ParseInt(teamRaw, 10, 64)
	if err != nil || team <= 0 {
		return 0, 0, false
	}
	coach, err := strconv.ParseInt(coachRaw, 10, 64)
	if err != nil || coach <= 0 {
		return 0, 0, false
	}
	return team, coach, true
}

func coachBoardKey(teamID, coachID int64) string {
	return strconv.FormatInt(teamID, 10) + ":" + strconv.FormatInt(coachID, 10)
}

// parseCoachSuggestion reads "<team>:<coach>:p:<load>" (previous season)
// or "<team>:<coach>:q:<load>:<min>:<max>" (qualification).
func parseCoachSuggestion(raw string) (service.SuggestedAssignment, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 4 {
		return service.SuggestedAssignment{}, false
	}
	team, coach, ok := parseCoachBoardKey(parts[0] + ":" + parts[1])
	if !ok {
		return service.SuggestedAssignment{}, false
	}
	numbers := make([]int, 0, 3)
	for _, value := range parts[3:] {
		number, err := strconv.Atoi(value)
		if err != nil || number < 0 || number > service.MaxCoachBoardAssignments {
			return service.SuggestedAssignment{}, false
		}
		numbers = append(numbers, number)
	}
	suggested := service.SuggestedAssignment{Assignment: service.CoachAssignment{TeamID: team, CoachUserID: coach}}
	switch {
	case parts[2] == "p" && len(numbers) == 1:
		suggested.Suggestion = service.CoachSuggestion{Reason: service.CoachSuggestionPreviousSeason, Load: numbers[0]}
	case parts[2] == "q" && len(numbers) == 3:
		suggested.Suggestion = service.CoachSuggestion{Reason: service.CoachSuggestionQualification, Load: numbers[0], Qualification: service.AgeRange{Min: numbers[1], Max: numbers[2]}}
	default:
		return service.SuggestedAssignment{}, false
	}
	return suggested, true
}

func encodeCoachSuggestion(teamID, coachID int64, suggestion service.CoachSuggestion) string {
	key := coachBoardKey(teamID, coachID)
	if suggestion.Reason == service.CoachSuggestionPreviousSeason {
		return key + ":p:" + strconv.Itoa(suggestion.Load)
	}
	return key + ":q:" + strconv.Itoa(suggestion.Load) + ":" + strconv.Itoa(suggestion.Qualification.Min) + ":" + strconv.Itoa(suggestion.Qualification.Max)
}

// renderCoachBoardError answers a failed draft or save. Validation errors
// and conflicts show the board again with the posted draft (422 for HTMX)
// and an error alert; a season that is gone is 404.
func (h *groupHandler) renderCoachBoardError(writer http.ResponseWriter, request *http.Request, current groupRequest, seasonID int64, draft *service.CoachBoardDraft, err error) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrSeasonNotFound), errors.Is(err, service.ErrRecordNotFound):
		h.renderGroupNotFound(writer, request, current.language)
		return
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, current.language)
		return
	}
	message, status := coachBoardErrorMessage(err)
	if message == "" {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	boardRequest := service.CoachBoardRequest{SeasonID: seasonID, Draft: draft}
	board, loadErr := h.dependencies.CoachBoard.Board(request.Context(), current.access, boardRequest)
	if loadErr != nil {
		// The posted draft itself may be what failed; show the saved board.
		board, loadErr = h.dependencies.CoachBoard.Board(request.Context(), current.access, service.CoachBoardRequest{SeasonID: seasonID})
		if loadErr != nil {
			h.renderCoachBoardLoadError(writer, request, current.language, loadErr)
			return
		}
	}
	h.respondCoachBoard(writer, request, current, board, []Alert{{Level: "error", Message: h.t(current.language, message)}}, formFailureStatus(request, status))
}

// coachBoardErrorMessage maps a board error to its message and status; an
// empty message means an unexpected error.
func coachBoardErrorMessage(err error) (string, int) {
	switch {
	case errors.Is(err, service.ErrOptimisticLockConflict):
		return "coach_board.stale", http.StatusConflict
	case errors.Is(err, service.ErrCoachBoardTwoHeads):
		return "coach_board.error.two_heads", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrCoachBoardTooLarge):
		return "coach_board.error.too_large", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrTeamNotFound):
		return "coach_board.error.team_not_found", http.StatusUnprocessableEntity
	case errors.Is(err, service.ErrSeasonCoachRoleMissing):
		return "coach_board.error.coach_role", http.StatusUnprocessableEntity
	case errors.Is(err, ErrInvalidCoachBoard), errors.Is(err, service.ErrInvalidGroup):
		return "groups.error.generic", http.StatusUnprocessableEntity
	}
	return "", 0
}

func (h *groupHandler) renderCoachBoardLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrSeasonNotFound):
		h.renderGroupNotFound(writer, request, language)
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, language)
	default:
		h.renderLoadError(writer, request, language, err)
	}
}

// renderCoachBoard loads the board and renders the page.
func (h *groupHandler) renderCoachBoard(writer http.ResponseWriter, request *http.Request, current groupRequest, boardRequest service.CoachBoardRequest, alerts []Alert, status int) {
	board, err := h.dependencies.CoachBoard.Board(request.Context(), current.access, boardRequest)
	if err != nil {
		h.renderCoachBoardLoadError(writer, request, current.language, err)
		return
	}
	h.respondCoachBoard(writer, request, current, board, alerts, status)
}

// respondCoachBoard answers with the board: HTMX requests from the board get
// the board fragment with out-of-band alerts, everything else the full page.
func (h *groupHandler) respondCoachBoard(writer http.ResponseWriter, request *http.Request, current groupRequest, board service.CoachBoard, alerts []Alert, status int) {
	group, err := h.dependencies.Groups.Group(request.Context(), current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	view := h.groupShell(current, group, groupSectionCoachBoard)
	view.CoachBoard = h.coachBoardView(current, board)
	view.CoachBoard.CSRFToken = csrfToken(request)
	if IsHTMX(request) && request.Header.Get("HX-Target") == "coach-board" {
		writer.Header().Add("Vary", "HX-Request")
		page := PageData{Language: string(current.language), CSRFToken: csrfToken(request), Alerts: alerts, Group: view}
		if err := h.renderer.renderStatus(writer, "coach-board-swap", page, status); err != nil {
			h.renderError(writer, request, http.StatusInternalServerError)
		}
		return
	}
	h.renderGroupPage(writer, request, current, view, FormData{}, alerts, status)
}

func (h *groupHandler) coachBoardView(current groupRequest, board service.CoachBoard) *CoachBoardView {
	language := current.language
	groupID := current.access.GroupID()
	seasonPath := "/seasons/" + strconv.FormatInt(board.Season.ID, 10)
	view := &CoachBoardView{
		L:           h.labels[language],
		CanEdit:     board.CanEdit,
		SeasonName:  board.Season.Name,
		SeasonDates: service.FormatSeasonDate(board.Season.StartsOn) + " – " + service.FormatSeasonDate(board.Season.EndsOn),
		SeasonURL:   h.groupPath(language, groupID, seasonPath),
		ActionURL:   h.groupPath(language, groupID, seasonPath+"/coach-board"),
		DraftURL:    h.groupPath(language, groupID, seasonPath+"/coach-board/draft"),
		Revision:    board.Revision,
		Changed:     board.Changed,
		Stale:       board.Stale(),
	}
	if board.HasPrevious {
		view.PreviousSeason = board.PreviousSeason.Name
	}
	view.StartOverText = h.t(language, "coach_board.start_over_confirm")
	labels := make(map[int64]string)
	for _, coach := range board.Coaches {
		label := h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, nil)
		labels[coach.UserID] = label
		card := CoachBoardCoachView{
			ID:             coach.UserID,
			Label:          label,
			Teams:          h.tv(language, "coach_board.coach_teams", map[string]string{"count": strconv.Itoa(coach.Teams)}),
			NoAvailability: !coach.HasAvailability,
		}
		if coach.Qualification != nil {
			card.Qualification = h.tv(language, "coach_board.ages", map[string]string{"min": strconv.Itoa(coach.Qualification.MinAge), "max": strconv.Itoa(coach.Qualification.MaxAge)})
		}
		view.Pool = append(view.Pool, card)
		view.AddOptions = append(view.AddOptions, SelectOptionView{Value: strconv.FormatInt(coach.UserID, 10), Label: label})
	}
	teamNames := make(map[int64]string)
	for _, team := range board.Teams {
		teamNames[team.Team.ID] = team.Team.Name
		column := CoachBoardTeamView{
			ID:       team.Team.ID,
			Name:     team.Team.Name,
			URL:      h.groupPath(language, groupID, "/teams/"+strconv.FormatInt(team.Team.ID, 10)),
			Archived: team.Team.Archived(),
			AddLabel: h.tv(language, "coach_board.add_label", map[string]string{"team": team.Team.Name}),
		}
		if board.CanEdit {
			column.Ages = h.tv(language, "coach_board.ages", map[string]string{"min": strconv.Itoa(team.Ages.Min), "max": strconv.Itoa(team.Ages.Max)})
		}
		for _, assignment := range team.Assignments {
			label, ok := labels[assignment.CoachUserID]
			if !ok {
				label = h.memberLabel(language, assignment.CoachUserID, assignment.DisplayName, assignment.Username, nil)
			}
			names := map[string]string{"coach": label, "team": team.Team.Name}
			chip := CoachBoardChipView{
				Key:         coachBoardKey(team.Team.ID, assignment.CoachUserID),
				CoachID:     assignment.CoachUserID,
				Label:       label,
				Head:        assignment.Head,
				New:         board.CanEdit && !assignment.Saved,
				HeadLabel:   h.tv(language, "coach_board.head_label", names),
				RemoveLabel: h.tv(language, "coach_board.remove_label", names),
			}
			if suggestion := assignment.Suggestion; suggestion != nil {
				chip.SuggestedValue = encodeCoachSuggestion(team.Team.ID, assignment.CoachUserID, *suggestion)
				chip.Explanation = h.coachSuggestionText(language, *suggestion)
			}
			for _, warning := range assignment.Warnings {
				chip.Warnings = append(chip.Warnings, h.coachWarningText(language, warning))
			}
			column.Chips = append(column.Chips, chip)
		}
		view.Teams = append(view.Teams, column)
	}
	for _, warning := range board.Warnings {
		view.Warnings = append(view.Warnings, h.tv(language, "coach_board.warning_line", map[string]string{
			"team": teamNames[warning.TeamID], "coach": labels[warning.CoachUserID], "warning": h.coachWarningText(language, warning),
		}))
	}
	return view
}

func (h *groupHandler) coachSuggestionText(language locale.Code, suggestion service.CoachSuggestion) string {
	if suggestion.Reason == service.CoachSuggestionPreviousSeason {
		return h.t(language, "coach_board.reason.previous")
	}
	return h.tv(language, "coach_board.reason.qualification", map[string]string{
		"min": strconv.Itoa(suggestion.Qualification.Min), "max": strconv.Itoa(suggestion.Qualification.Max), "count": strconv.Itoa(suggestion.Load),
	})
}

func (h *groupHandler) coachWarningText(language locale.Code, warning service.CoachBoardWarning) string {
	switch warning.Kind {
	case service.CoachWarningQualificationMismatch:
		return h.tv(language, "coach_board.warning.qualification_mismatch", map[string]string{
			"min": strconv.Itoa(warning.Qualification.Min), "max": strconv.Itoa(warning.Qualification.Max),
			"team_min": strconv.Itoa(warning.TeamAges.Min), "team_max": strconv.Itoa(warning.TeamAges.Max),
		})
	case service.CoachWarningNoAvailability:
		return h.t(language, "coach_board.warning.no_availability")
	default:
		return h.t(language, "coach_board.warning.no_qualification")
	}
}
