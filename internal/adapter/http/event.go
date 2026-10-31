package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// Event page sections.
const (
	groupSectionEvents = "events"
	groupSectionEvent  = "event"
)

// Display layouts of event times in the group's time zone.
const (
	eventDateLayout = "2006-01-02"
	eventTimeLayout = "15:04"
	// pastEventsLimit bounds the past events list.
	pastEventsLimit = 100
)

// EventUseCases aggregates the event operations consumed by HTTP handlers.
type EventUseCases interface {
	List(ctx context.Context, access *service.GroupAccess, query service.EventQuery) ([]service.Event, error)
	Detail(ctx context.Context, access *service.GroupAccess, eventID int64) (service.EventDetail, error)
	FormOptions(ctx context.Context, access *service.GroupAccess, event *service.Event) (service.EventFormOptions, error)
	Create(ctx context.Context, actor service.GroupActor, input service.EventInput) (service.Event, []service.EventWarning, error)
	Update(ctx context.Context, actor service.GroupActor, eventID, expectedVersion int64, input service.EventInput) (service.Event, []service.EventWarning, error)
	SetCancelled(ctx context.Context, actor service.GroupActor, eventID, expectedVersion int64, cancelled bool) (service.Event, error)
	Delete(ctx context.Context, actor service.GroupActor, eventID, expectedVersion int64) error
}

// EventsView is a group's event list. CreateForm is nil unless the account
// may create events.
type EventsView struct {
	TimeZone     string
	Past         bool
	UpcomingURL  string
	PastURL      string
	FilterURL    string
	TeamOptions  []SelectOptionView
	SelectedTeam string
	Rows         []EventRowView
	CreateForm   *EventFormView
}

// EventRowView is one event in a list.
type EventRowView struct {
	ID    int64
	URL   string
	Title string
	// KindID is the group's event kind; Kind is its system code (empty for
	// kinds an administrator added).
	KindID    int64
	Kind      string
	KindLabel string
	When      string
	Teams     string
	Place     string
	Cancelled bool
}

// EventDetailView is one event with its coaches, warnings, and the forms the
// account may use.
type EventDetailView struct {
	EventRowView
	TimeZone        string
	SignupLabel     string
	MaxParticipants string
	SignupDeadline  string
	Coaches         []EventCoachView
	Warnings        []string
	Generated       bool
	Edited          bool
	CanEdit         bool
	Version         int64
	ListURL         string
	CancelURL       string
	DeleteURL       string
	EditForm        *EventFormView
	// Participation is the sign-up and attendance card (#9).
	Participation *ParticipationView
}

// EventCoachView is one coach of an event.
type EventCoachView struct {
	UserID int64
	Label  string
	Extra  bool
}

// EventCheckOptionView is one checkbox of a multi-choice field.
type EventCheckOptionView struct {
	Value   string
	Label   string
	Checked bool
}

// EventFormView holds the values and errors of the event create or edit
// form.
type EventFormView struct {
	L                    map[string]string
	Prefix               string
	ActionURL            string
	TimeZone             string
	KindOptions          []SelectOptionView
	SignupOptions        []SelectOptionView
	ResourceOptions      []SelectOptionView
	Teams                []EventCheckOptionView
	Coaches              []EventCheckOptionView
	Title                string
	StartsAt             string
	EndsAt               string
	Location             string
	MaxParticipants      string
	SignupDeadline       string
	KindError            string
	TitleError           string
	StartsAtError        string
	EndsAtError          string
	TeamsError           string
	PlaceError           string
	LocationError        string
	SignupModeError      string
	MaxParticipantsError string
	SignupDeadlineError  string
	CoachesError         string
}

// registerEventRoutes registers each group's event list and event pages.
func registerEventRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{group}/events", groups.events)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/events", groups.createEvent)
		mux.HandleFunc("GET "+prefix+"/groups/{group}/events/{eventID}", groups.event)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/events/{eventID}", groups.updateEvent)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/events/{eventID}/cancel", groups.cancelEvent)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/events/{eventID}/delete", groups.deleteEvent)
		// The same pages inside a team (#56).
		mux.HandleFunc("GET "+prefix+"/groups/{group}/teams/{team}/events/{eventID}", groups.event)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/events/{eventID}", groups.updateEvent)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/events/{eventID}/cancel", groups.cancelEvent)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/teams/{team}/events/{eventID}/delete", groups.deleteEvent)
	}
}

func (h *groupHandler) events(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.eventRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionEvents, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createEvent(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.eventRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := eventForm(request)
	event, _, err := h.dependencies.Events.Create(request.Context(), current.actor(), input)
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvents, 0, err, h.eventFormErrors(current.language, form, err))
		return
	}
	// The event page shows the warnings.
	h.redirectLocalized(writer, request, current.language, "/groups/"+current.slug+"/events/"+strconv.FormatInt(event.ID, 10))
}

func (h *groupHandler) event(writer http.ResponseWriter, request *http.Request) {
	current, eventID, ok := h.eventPathRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionEvent, eventID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) updateEvent(writer http.ResponseWriter, request *http.Request) {
	current, eventID, ok := h.eventPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := eventForm(request)
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, service.ErrOptimisticLockConflict, form)
		return
	}
	if _, _, err := h.dependencies.Events.Update(request.Context(), current.actor(), eventID, version, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, err, h.eventFormErrors(current.language, form, err))
		return
	}
	h.renderSection(writer, request, current, groupSectionEvent, eventID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "events.saved")}}, http.StatusOK)
}

func (h *groupHandler) cancelEvent(writer http.ResponseWriter, request *http.Request) {
	current, eventID, ok := h.eventPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	cancelled := request.PostFormValue("cancelled") != "false"
	if _, err := h.dependencies.Events.SetCancelled(request.Context(), current.actor(), eventID, version, cancelled); err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, err, FormData{})
		return
	}
	message := "events.restored"
	if cancelled {
		message = "events.cancelled_message"
	}
	h.renderSection(writer, request, current, groupSectionEvent, eventID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

func (h *groupHandler) deleteEvent(writer http.ResponseWriter, request *http.Request) {
	current, eventID, ok := h.eventPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if err := h.dependencies.Events.Delete(request.Context(), current.actor(), eventID, version); err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, err, FormData{})
		return
	}
	if current.eventTeam != nil {
		h.redirectLocalized(writer, request, current.language, "/groups/"+current.slug+"/teams/"+current.eventTeam.Slug+"/events")
		return
	}
	h.redirectLocalized(writer, request, current.language, "/groups/"+current.slug+"/events")
}

// eventRequest authorizes an event page: outsiders get 404 from
// groupRequest; every member sees the events visible to them.
func (h *groupHandler) eventRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Events == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) eventPathRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.eventRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	eventID, err := pathPositiveID(request, "eventID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	if reference := strings.TrimSpace(request.PathValue("team")); reference != "" {
		// A team-scoped event page: the event must be one of the team's.
		// Visibility stays with GroupAccess, so a called-up player may open
		// it although the team itself is not theirs.
		if h.dependencies.Teams == nil {
			h.renderError(writer, request, http.StatusInternalServerError)
			return groupRequest{}, 0, false
		}
		detail, err := h.dependencies.Events.Detail(request.Context(), current.access, eventID)
		if err != nil {
			h.renderEventLoadError(writer, request, current.language, err)
			return groupRequest{}, 0, false
		}
		team, err := h.dependencies.Teams.ForEvent(request.Context(), current.access, reference, detail.Event)
		if err != nil {
			h.renderGroupNotFound(writer, request, current.language)
			return groupRequest{}, 0, false
		}
		current.eventTeam = &team
	}
	return current, eventID, true
}

// eventLink is the URL of an event page for a link in lists and calendars:
// under the team when exactly one of the viewer's teams is on the event,
// else the group-level page.
func (h *groupHandler) eventLink(current groupRequest, event service.Event) string {
	if teamID := singleViewerTeam(current.access, event); teamID > 0 {
		return h.teamEventLink(current, teamID, event)
	}
	return h.groupPath(current.language, current.slug, "/events/"+strconv.FormatInt(event.ID, 10))
}

// teamEventLink is the URL of an event page under a given team of it.
func (h *groupHandler) teamEventLink(current groupRequest, teamID int64, event service.Event) string {
	return h.teamPath(current, teamID, "/events/"+strconv.FormatInt(event.ID, 10))
}

// singleViewerTeam returns the event's team when it is the only one of its
// teams the viewer can see, else 0.
func singleViewerTeam(access *service.GroupAccess, event service.Event) int64 {
	var found int64
	for _, teamID := range event.TeamIDs() {
		if !access.CanViewTeam(teamID) {
			continue
		}
		if found != 0 {
			return 0
		}
		found = teamID
	}
	return found
}

func (h *groupHandler) loadEvents(writer http.ResponseWriter, request *http.Request, current groupRequest, group service.Group, form FormData, view *GroupDetailView) bool {
	return h.loadEventList(writer, request, current, group, 0, form, view)
}

// loadEventList fills the event list. A positive teamID fixes the list to
// that team (the team page's Events tab): no team filter, links under the
// team, and a create form that preselects the team.
func (h *groupHandler) loadEventList(writer http.ResponseWriter, request *http.Request, current groupRequest, group service.Group, teamID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Events == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	ctx := request.Context()
	language := current.language
	base := h.groupPath(language, group.Slug, "/events")
	if teamID > 0 {
		base = h.teamPath(current, teamID, "/events")
	}
	list := &EventsView{TimeZone: group.TimeZone, UpcomingURL: base, PastURL: base + "?when=past", FilterURL: base}
	values := request.URL.Query()
	list.Past = request.Method == http.MethodGet && values.Get("when") == "past"
	query := service.EventQuery{From: time.Now()}
	if list.Past {
		query = service.EventQuery{To: time.Now(), Descending: true, Limit: pastEventsLimit}
	}
	if teamID > 0 {
		query.TeamID = teamID
	} else if filterID, err := strconv.ParseInt(strings.TrimSpace(values.Get("team")), 10, 64); err == nil && filterID > 0 && request.Method == http.MethodGet {
		query.TeamID = filterID
		list.SelectedTeam = strconv.FormatInt(filterID, 10)
	}
	events, err := h.dependencies.Events.List(ctx, current.access, query)
	if err != nil {
		h.renderEventLoadError(writer, request, language, err)
		return false
	}
	for _, event := range events {
		row := h.eventRow(language, group.Slug, group.TimeZone, event)
		if teamID > 0 {
			row.URL = h.teamEventLink(current, teamID, event)
		}
		list.Rows = append(list.Rows, row)
	}
	if h.dependencies.Teams != nil && teamID == 0 {
		teams, err := h.dependencies.Teams.List(ctx, current.access)
		if err != nil {
			h.renderLoadError(writer, request, language, err)
			return false
		}
		for _, summary := range teams {
			value := strconv.FormatInt(summary.Team.ID, 10)
			list.TeamOptions = append(list.TeamOptions, SelectOptionView{Value: value, Label: summary.Team.Name, Selected: value == list.SelectedTeam})
		}
	}
	if current.access.CanCreateEvents() {
		options, err := h.dependencies.Events.FormOptions(ctx, current.access, nil)
		if err != nil {
			h.renderEventLoadError(writer, request, language, err)
			return false
		}
		if teamID == 0 || slices.ContainsFunc(options.Teams, func(team service.Team) bool { return team.ID == teamID }) {
			list.CreateForm = h.eventFormView(language, "event-new", base, options, nil, form)
			if teamID > 0 && !form.Submitted {
				preselected := strconv.FormatInt(teamID, 10)
				for index := range list.CreateForm.Teams {
					list.CreateForm.Teams[index].Checked = list.CreateForm.Teams[index].Value == preselected
				}
			}
		}
	}
	view.Events = list
	return true
}

func (h *groupHandler) loadEvent(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID, eventID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Events == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	ctx := request.Context()
	language := current.language
	detail, err := h.dependencies.Events.Detail(ctx, current.access, eventID)
	if err != nil {
		h.renderEventLoadError(writer, request, language, err)
		return false
	}
	event := detail.Event
	row := h.eventRow(language, current.slug, detail.TimeZone, event)
	listURL := h.groupPath(language, current.slug, "/events")
	if team := current.eventTeam; team != nil {
		// Team-scoped page: links and forms stay under the team, with its
		// tabs above. A called-up player who is not on the team gets the
		// heading only, since the tabs would be closed to them.
		row.URL = h.groupPath(language, current.slug, "/teams/"+team.Slug+"/events/"+strconv.FormatInt(event.ID, 10))
		if current.access.CanViewTeam(team.ID) {
			view.TeamTabs = h.teamTabs(current, *team, teamTabEvents)
			listURL = h.groupPath(language, current.slug, "/teams/"+team.Slug+"/events")
		} else {
			view.TeamTabs = &TeamTabsView{Name: team.Name, TeamsURL: h.groupPath(language, current.slug, "/teams")}
			listURL = h.groupPath(language, current.slug, "/calendar")
		}
	}
	page := &EventDetailView{
		EventRowView: row,
		TimeZone:     detail.TimeZone,
		SignupLabel:  h.t(language, "events.signup."+event.SignupMode),
		Generated:    event.Generated(),
		Edited:       event.EditedAt != nil,
		CanEdit:      detail.CanEdit,
		Version:      event.Version,
		ListURL:      listURL,
		CancelURL:    row.URL + "/cancel",
		DeleteURL:    row.URL + "/delete",
	}
	if event.MaxParticipants != nil {
		page.MaxParticipants = strconv.Itoa(*event.MaxParticipants)
	}
	if event.SignupDeadline != nil {
		page.SignupDeadline = formatEventInstant(*event.SignupDeadline, detail.TimeZone)
	}
	seen := make(map[int64]bool)
	for _, coach := range detail.TeamCoaches {
		if !seen[coach.UserID] {
			seen[coach.UserID] = true
			page.Coaches = append(page.Coaches, EventCoachView{UserID: coach.UserID, Label: h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, nil)})
		}
	}
	for _, coach := range event.Coaches {
		if !seen[coach.UserID] {
			seen[coach.UserID] = true
			page.Coaches = append(page.Coaches, EventCoachView{UserID: coach.UserID, Label: h.memberLabel(language, coach.UserID, coach.DisplayName, coach.Username, nil), Extra: true})
		}
	}
	for _, warning := range detail.Warnings {
		page.Warnings = append(page.Warnings, h.eventWarningText(language, detail.TimeZone, warning))
	}
	if detail.CanEdit {
		options, err := h.dependencies.Events.FormOptions(ctx, current.access, &event)
		if err != nil {
			h.renderEventLoadError(writer, request, language, err)
			return false
		}
		page.EditForm = h.eventFormView(language, "event-edit", row.URL, options, &event, form)
	}
	if !h.loadParticipation(writer, request, current, detail, page) {
		return false
	}
	view.Event = page
	return true
}

func (h *groupHandler) renderEventLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	switch {
	case errors.Is(err, service.ErrEventNotFound):
		h.renderGroupNotFound(writer, request, language)
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, language)
	default:
		h.renderLoadError(writer, request, language, err)
	}
}

func (h *groupHandler) eventRow(language locale.Code, groupSlug string, zone string, event service.Event) EventRowView {
	row := EventRowView{
		ID:        event.ID,
		URL:       h.groupPath(language, groupSlug, "/events/"+strconv.FormatInt(event.ID, 10)),
		Title:     event.Title,
		KindID:    event.KindID,
		Kind:      event.KindCode,
		KindLabel: event.KindName(string(language)),
		When:      formatEventRange(event.StartsAt, event.EndsAt, zone),
		Cancelled: event.Cancelled(),
	}
	names := make([]string, 0, len(event.Teams))
	for _, team := range event.Teams {
		names = append(names, team.Name)
	}
	row.Teams = strings.Join(names, ", ")
	switch {
	case event.ResourceName != nil:
		row.Place = *event.ResourceName
	case event.Location != nil:
		row.Place = *event.Location
	}
	return row
}

func (h *groupHandler) eventWarningText(language locale.Code, zone string, warning service.EventWarning) string {
	id, values := "", map[string]string{}
	switch warning.Kind {
	case service.EventWarningResourceCapacity:
		id = "events.warning.resource_capacity"
		values["resource"] = warning.ResourceName
		values["limit"] = strconv.Itoa(warning.Limit)
		values["teams"] = strconv.Itoa(warning.Teams)
	case service.EventWarningCoachBusy:
		id = "events.warning.coach_busy"
		values["coach"] = h.memberLabel(language, warning.Coach.UserID, warning.Coach.DisplayName, warning.Coach.Username, nil)
		values["event"] = warning.OtherTitle
		values["time"] = formatEventInstant(warning.OtherStartsAt, zone)
	default:
		return warning.Kind
	}
	if h.renderer != nil && h.renderer.catalogs != nil {
		if text, err := h.renderer.catalogs.Translate(language, id, values); err == nil {
			return text
		}
	}
	return id
}

// eventFormView fills the event form from submitted values after a failed
// write, else from the stored event (nil for a new event).
func (h *groupHandler) eventFormView(language locale.Code, prefix, actionURL string, options service.EventFormOptions, event *service.Event, form FormData) *EventFormView {
	view := &EventFormView{L: h.labels[language], Prefix: prefix, ActionURL: actionURL, TimeZone: options.TimeZone}
	kind, signup, resource := "", service.EventSignupOpen, ""
	var teams, coaches []string
	switch {
	case form.Submitted:
		kind, signup, resource = form.Value("kind"), form.Value("signup_mode"), form.Value("resource_id")
		view.Title, view.StartsAt, view.EndsAt = form.Value("title"), form.Value("starts_at"), form.Value("ends_at")
		view.Location, view.MaxParticipants, view.SignupDeadline = form.Value("location"), form.Value("max_participants"), form.Value("signup_deadline")
		teams, coaches = splitFormIDs(form.Value("team_id")), splitFormIDs(form.Value("coach_user_id"))
		view.KindError, view.TitleError = form.Error("kind"), form.Error("title")
		view.StartsAtError, view.EndsAtError = form.Error("starts_at"), form.Error("ends_at")
		view.TeamsError, view.PlaceError, view.LocationError = form.Error("team_id"), form.Error("resource_id"), form.Error("location")
		view.SignupModeError, view.MaxParticipantsError = form.Error("signup_mode"), form.Error("max_participants")
		view.SignupDeadlineError, view.CoachesError = form.Error("signup_deadline"), form.Error("coach_user_id")
	case event != nil:
		kind, signup, view.Title = strconv.FormatInt(event.KindID, 10), event.SignupMode, event.Title
		view.StartsAt = formatEventFormTime(event.StartsAt, options.TimeZone)
		view.EndsAt = formatEventFormTime(event.EndsAt, options.TimeZone)
		if event.ResourceID != nil {
			resource = strconv.FormatInt(*event.ResourceID, 10)
		}
		if event.Location != nil {
			view.Location = *event.Location
		}
		if event.MaxParticipants != nil {
			view.MaxParticipants = strconv.Itoa(*event.MaxParticipants)
		}
		if event.SignupDeadline != nil {
			view.SignupDeadline = formatEventFormTime(*event.SignupDeadline, options.TimeZone)
		}
		for _, id := range event.TeamIDs() {
			teams = append(teams, strconv.FormatInt(id, 10))
		}
		for _, id := range event.CoachUserIDs() {
			coaches = append(coaches, strconv.FormatInt(id, 10))
		}
	}
	if kind == "" {
		// A new event starts on the group's first training-like kind: the
		// system training kind when it is offered, else the first kind.
		for _, option := range options.Kinds {
			if option.SystemCode == service.EventKindTraining {
				kind = strconv.FormatInt(option.ID, 10)
			}
		}
		if kind == "" && len(options.Kinds) > 0 {
			kind = strconv.FormatInt(options.Kinds[0].ID, 10)
		}
	}
	for _, option := range options.Kinds {
		value := strconv.FormatInt(option.ID, 10)
		label := option.Name(string(language))
		if option.Archived() {
			label += " (" + h.t(language, "kinds.archived_badge") + ")"
		}
		view.KindOptions = append(view.KindOptions, SelectOptionView{Value: value, Label: label, Selected: value == kind})
	}
	for _, mode := range service.EventSignupModes() {
		view.SignupOptions = append(view.SignupOptions, SelectOptionView{Value: mode, Label: h.t(language, "events.signup."+mode), Selected: mode == signup})
	}
	view.ResourceOptions = append(view.ResourceOptions, SelectOptionView{Value: "", Label: h.t(language, "events.resource_none"), Selected: resource == ""})
	for _, item := range options.Resources {
		value := strconv.FormatInt(item.ID, 10)
		label := item.Name
		if item.Archived() {
			label += " (" + h.t(language, "events.resource_archived") + ")"
		}
		view.ResourceOptions = append(view.ResourceOptions, SelectOptionView{Value: value, Label: label, Selected: value == resource})
	}
	for _, team := range options.Teams {
		value := strconv.FormatInt(team.ID, 10)
		view.Teams = append(view.Teams, EventCheckOptionView{Value: value, Label: team.Name, Checked: slices.Contains(teams, value)})
	}
	for _, member := range options.Coaches {
		value := strconv.FormatInt(member.UserID, 10)
		view.Coaches = append(view.Coaches, EventCheckOptionView{Value: value, Label: h.memberLabel(language, member.UserID, member.DisplayName, member.Username, nil), Checked: slices.Contains(coaches, value)})
	}
	return view
}

// eventFormErrors attaches the field error for an event validation failure.
func (h *groupHandler) eventFormErrors(language locale.Code, form FormData, err error) FormData {
	field, message := eventErrorField(err)
	if field != "" {
		form.Errors = []FieldError{{Field: field, Message: h.t(language, message)}}
	}
	return form
}

// eventErrorField maps an event validation error to its form field and
// message; it returns empty strings for other errors.
func eventErrorField(err error) (string, string) {
	var fieldErr *service.EventFieldError
	if errors.As(err, &fieldErr) && errors.Is(err, service.ErrEventTimeSkipped) {
		return fieldErr.Field, "events.error.time_skipped"
	}
	switch {
	case errors.Is(err, service.ErrInvalidEventKind):
		return "kind", "events.error.invalid_kind"
	case errors.Is(err, service.ErrEventKindArchived):
		return "kind", "events.error.kind_archived"
	case errors.Is(err, service.ErrInvalidEventTitle):
		return "title", "events.error.invalid_title"
	case errors.Is(err, service.ErrInvalidEventStart):
		return "starts_at", "events.error.invalid_start"
	case errors.Is(err, service.ErrInvalidEventEnd):
		return "ends_at", "events.error.invalid_end"
	case errors.Is(err, service.ErrEventTeamsRequired):
		return "team_id", "events.error.teams_required"
	case errors.Is(err, service.ErrInvalidEventTeams):
		return "team_id", "events.error.invalid_teams"
	case errors.Is(err, service.ErrEventPlaceRequired):
		return "resource_id", "events.error.place_required"
	case errors.Is(err, service.ErrInvalidEventResource):
		return "resource_id", "events.error.invalid_resource"
	case errors.Is(err, service.ErrEventResourceArchived):
		return "resource_id", "events.error.resource_archived"
	case errors.Is(err, service.ErrInvalidEventLocation):
		return "location", "events.error.invalid_location"
	case errors.Is(err, service.ErrInvalidEventSignupMode):
		return "signup_mode", "events.error.invalid_signup_mode"
	case errors.Is(err, service.ErrInvalidEventMaxParticipants):
		return "max_participants", "events.error.invalid_max_participants"
	case errors.Is(err, service.ErrInvalidEventSignupDeadline):
		return "signup_deadline", "events.error.invalid_signup_deadline"
	case errors.Is(err, service.ErrInvalidEventCoaches):
		return "coach_user_id", "events.error.invalid_coaches"
	case errors.Is(err, service.ErrEventCoachRoleMissing):
		return "coach_user_id", "events.error.coach_role_missing"
	}
	return "", ""
}

// eventErrorMessage returns the message of an event validation error, or ""
// for other errors.
func eventErrorMessage(err error) string {
	_, message := eventErrorField(err)
	return message
}

// eventForm reads event input. Unparseable times become zero and
// unparseable numbers negative, which validation refuses.
func eventForm(request *http.Request) (service.EventInput, FormData) {
	values := map[string]string{
		"kind":             strings.TrimSpace(request.PostFormValue("kind")),
		"title":            request.PostFormValue("title"),
		"starts_at":        strings.TrimSpace(request.PostFormValue("starts_at")),
		"ends_at":          strings.TrimSpace(request.PostFormValue("ends_at")),
		"resource_id":      strings.TrimSpace(request.PostFormValue("resource_id")),
		"location":         request.PostFormValue("location"),
		"signup_mode":      strings.TrimSpace(request.PostFormValue("signup_mode")),
		"max_participants": strings.TrimSpace(request.PostFormValue("max_participants")),
		"signup_deadline":  strings.TrimSpace(request.PostFormValue("signup_deadline")),
	}
	input := service.EventInput{
		KindID:          optionalFormNumber(values["kind"]),
		Title:           values["title"],
		StartsAt:        service.ParseEventLocalTime(values["starts_at"]),
		EndsAt:          service.ParseEventLocalTime(values["ends_at"]),
		ResourceID:      optionalFormNumber(values["resource_id"]),
		Location:        values["location"],
		SignupMode:      values["signup_mode"],
		MaxParticipants: int(optionalFormNumber(values["max_participants"])),
		SignupDeadline:  service.ParseEventLocalTime(values["signup_deadline"]),
	}
	if values["signup_deadline"] != "" && input.SignupDeadline.IsZero() {
		// A deadline that does not parse is refused, not dropped.
		input.SignupDeadline = time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC)
	}
	var teams, coaches []string
	input.TeamIDs, teams = formIDs(request.PostForm["team_id"])
	input.CoachUserIDs, coaches = formIDs(request.PostForm["coach_user_id"])
	values["team_id"], values["coach_user_id"] = strings.Join(teams, ","), strings.Join(coaches, ",")
	return input, FormData{Submitted: true, Values: values}
}

// optionalFormNumber reads an optional whole number: 0 when empty, -1 when
// it does not parse.
func optionalFormNumber(raw string) int64 {
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || value > 1<<31 {
		return -1
	}
	return value
}

// formIDs reads repeated ID fields; a value that does not parse becomes -1,
// which validation refuses.
func formIDs(raw []string) ([]int64, []string) {
	ids := make([]int64, 0, len(raw))
	kept := make([]string, 0, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			id = -1
		}
		ids = append(ids, id)
		kept = append(kept, value)
	}
	return ids, kept
}

func splitFormIDs(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, ",")
}

// formatEventRange shows an event's time in zone: one date with a time range,
// or two date-times when it ends on another day.
func formatEventRange(starts, ends time.Time, zone string) string {
	startWall, err := service.EventLocalTime(starts, zone)
	if err != nil {
		return starts.UTC().Format(time.RFC3339)
	}
	endWall, err := service.EventLocalTime(ends, zone)
	if err != nil {
		return starts.UTC().Format(time.RFC3339)
	}
	from := startWall.Format(eventDateLayout + " " + eventTimeLayout)
	if service.SeasonDate(startWall).Equal(service.SeasonDate(endWall)) {
		return from + " – " + endWall.Format(eventTimeLayout)
	}
	return from + " – " + endWall.Format(eventDateLayout+" "+eventTimeLayout)
}

func formatEventInstant(instant time.Time, zone string) string {
	wall, err := service.EventLocalTime(instant, zone)
	if err != nil {
		return instant.UTC().Format(time.RFC3339)
	}
	return wall.Format(eventDateLayout + " " + eventTimeLayout)
}

func formatEventFormTime(instant time.Time, zone string) string {
	wall, err := service.EventLocalTime(instant, zone)
	if err != nil {
		return ""
	}
	return service.FormatEventLocalTime(wall)
}
