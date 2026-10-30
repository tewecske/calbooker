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

// groupSectionEventGeneration is the page that generates events from the
// training plan (#15).
const groupSectionEventGeneration = "event-generation"

// EventGenerationUseCases aggregates the event generation operations
// consumed by HTTP handlers.
type EventGenerationUseCases interface {
	Options(ctx context.Context, access *service.GroupAccess, partID int64) (service.GenerationOptions, error)
	Preview(ctx context.Context, access *service.GroupAccess, request service.GenerationRequest) (service.GenerationPreview, error)
	Generate(ctx context.Context, actor service.GroupActor, request service.GenerationRequest) (service.GenerationPreview, error)
}

// EventGenerationView is the generation page of one season part: the choice
// of weeks, the preview, and after confirming the result.
type EventGenerationView struct {
	L          map[string]string
	CSRFToken  string
	Empty      bool
	SeasonsURL string
	PlanURL    string
	// PickerURL receives the part and week choices (GET); ActionURL the
	// confirmation (POST).
	PickerURL  string
	ActionURL  string
	PartID     int64
	Parts      []SelectOptionView
	SeasonName string
	PartName   string
	PartDates  string
	TimeZone   string
	Weeks      []SelectOptionView
	WeekCounts []SelectOptionView
	Week       string
	WeekCount  int
	// Previewed is true when the chosen weeks are previewed; Done after the
	// events were generated (the items then show what was done).
	Previewed bool
	Done      bool
	// CanConfirm is true when confirming would write something.
	CanConfirm bool
	Counts     []GenerationCountView
	Items      []GenerationItemView
	Kept       []GenerationItemView
}

// GenerationCountView is one count of the summary.
type GenerationCountView struct {
	Action string
	Label  string
	Count  int
}

// GenerationItemView is one line of the preview or result.
type GenerationItemView struct {
	Action      string
	ActionLabel string
	Reason      string
	When        string
	Team        string
	Resource    string
	// URL opens a kept event.
	URL string
}

// registerEventGenerationRoutes registers each group's event generation
// page.
func registerEventGenerationRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{group}/event-generation", groups.eventGeneration)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/event-generation", groups.generateEvents)
	}
}

// eventGenerationRequest authorizes a generation page: outsiders get 404
// from groupRequest, members who may not manage the plan get access denied.
func (h *groupHandler) eventGenerationRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.EventGeneration == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanManagePlan() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

type eventGenerationQuery struct {
	part  int64
	week  string
	weeks int
}

func parseEventGenerationValues(values url.Values) eventGenerationQuery {
	query := eventGenerationQuery{weeks: 1}
	if part, err := strconv.ParseInt(strings.TrimSpace(values.Get("part")), 10, 64); err == nil && part > 0 {
		query.part = part
	}
	query.week = strings.TrimSpace(values.Get("week"))
	if weeks, err := strconv.Atoi(strings.TrimSpace(values.Get("weeks"))); err == nil && weeks >= 1 && weeks <= service.MaxGenerationWeeks {
		query.weeks = weeks
	}
	return query
}

func (q eventGenerationQuery) request() service.GenerationRequest {
	return service.GenerationRequest{PartID: q.part, Week: service.ParseSeasonDate(q.week), Weeks: q.weeks}
}

func (h *groupHandler) eventGenerationURL(language locale.Code, groupSlug string) string {
	return h.groupPath(language, groupSlug, "/event-generation")
}

func (h *groupHandler) eventGeneration(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.eventGenerationRequest(writer, request)
	if !ok {
		return
	}
	query := parseEventGenerationValues(request.URL.Query())
	var preview *service.GenerationPreview
	var alerts []Alert
	status := http.StatusOK
	if query.week != "" {
		result, err := h.dependencies.EventGeneration.Preview(request.Context(), current.access, query.request())
		if err != nil {
			alerts, status = h.eventGenerationFailure(writer, request, current, err)
			if status == 0 {
				return
			}
		} else {
			preview = &result
		}
	}
	h.respondEventGeneration(writer, request, current, query, preview, false, alerts, status)
}

func (h *groupHandler) generateEvents(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.eventGenerationRequest(writer, request)
	if !ok {
		return
	}
	if err := request.ParseForm(); err != nil {
		h.renderError(writer, request, http.StatusBadRequest)
		return
	}
	query := parseEventGenerationValues(request.PostForm)
	if query.part == 0 {
		h.renderGroupNotFound(writer, request, current.language)
		return
	}
	result, err := h.dependencies.EventGeneration.Generate(request.Context(), current.actor(), query.request())
	if err != nil {
		alerts, status := h.eventGenerationFailure(writer, request, current, err)
		if status == 0 {
			return
		}
		h.respondEventGeneration(writer, request, current, query, nil, false, alerts, formFailureStatus(request, status))
		return
	}
	counts := result.Counts
	message := h.t(current.language, "event_generation.nothing_changed")
	if counts.Changed() {
		message = h.tv(current.language, "event_generation.generated", map[string]string{
			"created": strconv.Itoa(counts.Create), "rebuilt": strconv.Itoa(counts.Rebuild), "removed": strconv.Itoa(counts.Remove),
			"kept": strconv.Itoa(counts.Keep),
		})
	}
	h.respondEventGeneration(writer, request, current, query, &result, true, []Alert{{Level: "success", Message: message}}, http.StatusOK)
}

// eventGenerationFailure maps a failed request. Missing records and denied
// access are answered here (status 0); validation errors come back as an
// alert to show on the page.
func (h *groupHandler) eventGenerationFailure(writer http.ResponseWriter, request *http.Request, current groupRequest, err error) ([]Alert, int) {
	switch {
	case errors.Is(err, service.ErrGroupNotFound), errors.Is(err, service.ErrSeasonNotFound), errors.Is(err, service.ErrRecordNotFound):
		h.renderGroupNotFound(writer, request, current.language)
		return nil, 0
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, current.language)
		return nil, 0
	case errors.Is(err, service.ErrInvalidGenerationWeek):
		return []Alert{{Level: "error", Message: h.t(current.language, "event_generation.error.invalid_week")}}, http.StatusUnprocessableEntity
	}
	h.renderError(writer, request, http.StatusInternalServerError)
	return nil, 0
}

func (h *groupHandler) respondEventGeneration(writer http.ResponseWriter, request *http.Request, current groupRequest, query eventGenerationQuery, preview *service.GenerationPreview, done bool, alerts []Alert, status int) {
	options, err := h.dependencies.EventGeneration.Options(request.Context(), current.access, query.part)
	if err != nil {
		_, _ = h.eventGenerationFailure(writer, request, current, err)
		return
	}
	group, err := h.dependencies.Groups.Group(request.Context(), current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return
	}
	view := h.groupShell(current, group, groupSectionEventGeneration)
	view.EventGeneration = h.eventGenerationView(request, current, query, options, preview, done)
	h.renderGroupPage(writer, request, current, view, FormData{}, alerts, status)
}

func (h *groupHandler) eventGenerationView(request *http.Request, current groupRequest, query eventGenerationQuery, options service.GenerationOptions, preview *service.GenerationPreview, done bool) *EventGenerationView {
	language := current.language
	view := &EventGenerationView{
		L:          h.labels[language],
		CSRFToken:  csrfToken(request),
		SeasonsURL: h.groupPath(language, current.slug, "/seasons"),
		PlanURL:    h.groupPath(language, current.slug, "/training-plan"),
		PickerURL:  h.eventGenerationURL(language, current.slug),
		ActionURL:  h.eventGenerationURL(language, current.slug),
		WeekCount:  query.weeks,
	}
	if !options.Found {
		view.Empty = true
		return view
	}
	part := options.Part
	view.PartID = part.ID
	view.SeasonName, view.PartName = options.Season.Name, part.Name
	view.PartDates = service.FormatSeasonDate(part.StartsOn) + " – " + service.FormatSeasonDate(part.EndsOn)
	view.PlanURL += "?part=" + strconv.FormatInt(part.ID, 10)
	for _, season := range options.Seasons {
		for _, candidate := range season.Parts {
			view.Parts = append(view.Parts, SelectOptionView{
				Value: strconv.FormatInt(candidate.ID, 10), Label: season.Name + ": " + candidate.Name, Selected: candidate.ID == part.ID,
			})
		}
	}
	chosen := service.ParseSeasonDate(query.week)
	if chosen.IsZero() {
		chosen = options.DefaultWeek
	}
	view.Week = service.FormatSeasonDate(chosen)
	for _, week := range options.Weeks {
		label := service.FormatSeasonDate(week) + " – " + service.FormatSeasonDate(week.AddDate(0, 0, 6))
		if part.AlternatingWeeks {
			// The week of the part's first day is A even when it starts
			// mid-week.
			first := week
			if first.Before(part.StartsOn) {
				first = part.StartsOn
			}
			label += " (" + h.t(language, "event_generation.week_"+strings.ToLower(part.WeekOn(first))) + ")"
		}
		view.Weeks = append(view.Weeks, SelectOptionView{Value: service.FormatSeasonDate(week), Label: label, Selected: week.Equal(chosen)})
	}
	for count := 1; count <= service.MaxGenerationWeeks; count++ {
		view.WeekCounts = append(view.WeekCounts, SelectOptionView{
			Value: strconv.Itoa(count), Label: h.t(language, "event_generation.weeks."+strconv.Itoa(count)), Selected: count == query.weeks,
		})
	}
	if preview == nil {
		return view
	}
	view.Previewed, view.Done = true, done
	view.TimeZone = preview.TimeZone
	view.CanConfirm = !done && preview.Counts.Changed()
	for _, entry := range []struct {
		action string
		count  int
	}{
		{service.GenerationCreate, preview.Counts.Create}, {service.GenerationRebuild, preview.Counts.Rebuild},
		{service.GenerationRemove, preview.Counts.Remove}, {service.GenerationUnchanged, preview.Counts.Unchanged},
		{service.GenerationKeep, preview.Counts.Keep}, {service.GenerationSkip, preview.Counts.Skip},
	} {
		view.Counts = append(view.Counts, GenerationCountView{Action: entry.action, Label: h.t(language, "event_generation.action."+entry.action+"."+h.generationTense(done)), Count: entry.count})
	}
	for _, item := range preview.Items {
		line := GenerationItemView{
			Action: item.Action, ActionLabel: h.t(language, "event_generation.action."+item.Action+"."+h.generationTense(done)),
			When: h.generationWhen(language, item, preview.TimeZone), Team: item.TeamName, Resource: item.ResourceName,
		}
		if item.Reason != "" {
			line.Reason = h.t(language, "event_generation.reason."+item.Reason)
		}
		if item.Action == service.GenerationKeep {
			line.URL = h.groupPath(language, current.slug, "/events/"+strconv.FormatInt(item.EventID, 10))
			view.Kept = append(view.Kept, line)
			continue
		}
		view.Items = append(view.Items, line)
	}
	return view
}

// generationTense picks the label wording: what will happen (preview) or
// what happened.
func (h *groupHandler) generationTense(done bool) string {
	if done {
		return "done"
	}
	return "preview"
}

// generationWhen writes the weekday, date, and clock times of an item in the
// group's time zone.
func (h *groupHandler) generationWhen(language locale.Code, item service.GenerationItem, zone string) string {
	starts, errStart := service.EventLocalTime(item.StartsAt, zone)
	ends, errEnd := service.EventLocalTime(item.EndsAt, zone)
	if errStart != nil || errEnd != nil || item.StartsAt.IsZero() {
		return ""
	}
	weekday := int(starts.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	text := h.t(language, "weekgrid.day."+strconv.Itoa(weekday)) + " " + starts.Format("2006-01-02") + " " + starts.Format("15:04") + "–" + ends.Format("15:04")
	return text
}
