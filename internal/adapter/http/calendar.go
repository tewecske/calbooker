package httpadapter

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// groupSectionCalendar is the weekly calendar of a group's events.
const groupSectionCalendar = "calendar"

// Visible hours of the calendar grid: at least calendarDayStart to
// calendarDayEnd, widened to whole hours around the week's events.
const (
	calendarDayStart = 8 * 60
	calendarDayEnd   = 20 * 60
	calendarStep     = 30
	calendarGridID   = "calendar-grid"
)

// CalendarView is one week of a group's events: the grid for wide screens,
// a day list for narrow ones, filters, and week and group navigation.
type CalendarView struct {
	TimeZone    string
	Week        string
	RangeLabel  string
	BaseURL     string
	TodayURL    string
	PreviousURL string
	NextURL     string
	ClearURL    string
	CurrentWeek bool
	Filtered    bool
	// Team, resource, and kind filter choices. Only what the viewer sees is
	// offered; a requested value that is not offered is dropped.
	TeamOptions     []SelectOptionView
	ResourceOptions []SelectOptionView
	KindOptions     []SelectOptionView
	// Groups is the group switcher (the viewer's groups, the same week);
	// empty unless the viewer has more than one group.
	Groups []CalendarGroupView
	Grid   *WeekGridView
	Days   []CalendarDayView
	Empty  bool
}

// CalendarGroupView is one entry of the group switcher.
type CalendarGroupView struct {
	Name    string
	URL     string
	Current bool
}

// CalendarDayView is one day of the day list.
type CalendarDayView struct {
	Weekday   int
	Name      string
	DateLabel string
	Date      string
	Today     bool
	Items     []CalendarItemView
}

// CalendarItemView is one event on one day of the calendar.
type CalendarItemView struct {
	ID        int64
	URL       string
	Title     string
	Kind      string
	KindLabel string
	// Time is the part of the event on this day; When is the whole event,
	// shown when it spans days.
	Time      string
	When      string
	MultiDay  bool
	Teams     string
	Place     string
	Cancelled bool
	// Answers are the viewer's own sign-up answer and their children's
	// (#30), one badge each.
	Answers []CalendarAnswerView
}

// CalendarAnswerView is one answer badge. Label is the status, prefixed
// with the child's name for a child's answer; Code is the stable status.
type CalendarAnswerView struct {
	Code  string
	Label string
	Self  bool
}

// calendarSelection is the validated query of a calendar request.
type calendarSelection struct {
	week   time.Time
	filter service.CalendarFilter
}

// registerCalendarRoutes registers each group's weekly calendar.
func registerCalendarRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/calendar", groups.calendar)
	}
}

func (h *groupHandler) calendar(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.eventRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionCalendar, 0, FormData{}, nil, http.StatusOK)
}

// loadCalendar fills the calendar section. Events come from the event use
// cases, which return only what the account sees; filters narrow that list
// and never widen it.
func (h *groupHandler) loadCalendar(writer http.ResponseWriter, request *http.Request, current groupRequest, group service.Group, view *GroupDetailView) bool {
	if h.dependencies.Events == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	ctx := request.Context()
	language := current.language
	today, err := service.CalendarToday(time.Now(), group.TimeZone)
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	values := request.URL.Query()
	selection := calendarSelection{week: service.ParseCalendarDate(values.Get("week"))}
	if selection.week.IsZero() {
		selection.week = today
	}
	week, err := service.NewCalendarWeek(selection.week, group.TimeZone)
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	selection.week = week.Monday
	events, err := h.dependencies.Events.List(ctx, current.access, service.EventQuery{From: week.From, To: week.To})
	if err != nil {
		h.renderEventLoadError(writer, request, language, err)
		return false
	}

	page := &CalendarView{
		TimeZone:    group.TimeZone,
		Week:        service.FormatSeasonDate(week.Monday),
		BaseURL:     h.groupPath(language, group.ID, "/calendar"),
		CurrentWeek: week.Contains(today),
	}
	teamOptions, ok := h.calendarTeamOptions(writer, request, current)
	if !ok {
		return false
	}
	resourceOptions, ok := h.calendarResourceOptions(writer, request, current, events)
	if !ok {
		return false
	}
	if id := calendarQueryID(values.Get("team")); id > 0 && slices.ContainsFunc(teamOptions, optionHasValue(id)) {
		selection.filter.TeamID = id
	}
	if id := calendarQueryID(values.Get("resource")); id > 0 && slices.ContainsFunc(resourceOptions, optionHasValue(id)) {
		selection.filter.ResourceID = id
	}
	if kind := strings.TrimSpace(values.Get("kind")); service.ValidEventKind(kind) {
		selection.filter.Kind = kind
	}
	page.TeamOptions = selectOptions(teamOptions, strconv.FormatInt(selection.filter.TeamID, 10))
	page.ResourceOptions = selectOptions(resourceOptions, strconv.FormatInt(selection.filter.ResourceID, 10))
	for _, kind := range service.EventKinds() {
		page.KindOptions = append(page.KindOptions, SelectOptionView{Value: kind, Label: h.t(language, "events.kind."+kind), Selected: kind == selection.filter.Kind})
	}
	page.Filtered = selection.filter != service.CalendarFilter{}
	page.PreviousURL = calendarURL(page.BaseURL, week.Previous(), selection.filter)
	page.NextURL = calendarURL(page.BaseURL, week.Next(), selection.filter)
	page.TodayURL = calendarURL(page.BaseURL, time.Time{}, selection.filter)
	page.ClearURL = calendarURL(page.BaseURL, week.Monday, service.CalendarFilter{})

	shown := service.FilterCalendarEvents(events, selection.filter)
	page.Empty = len(shown) == 0
	answers, ok := h.calendarAnswers(writer, request, current, week, shown)
	if !ok {
		return false
	}
	items, days := h.calendarItems(language, group, week, shown, answers)
	grid, err := BuildWeekGrid(calendarGridConfig(week, items, page.PreviousURL, page.NextURL), items, h.renderer.WeekGridTranslator(language))
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	page.Grid = grid
	page.RangeLabel = grid.RangeLabel
	for index, day := range grid.Days {
		date := week.Day(day.Weekday)
		page.Days = append(page.Days, CalendarDayView{
			Weekday:   day.Weekday,
			Name:      day.Name,
			DateLabel: day.DateLabel,
			Date:      service.FormatSeasonDate(date),
			Today:     date.Equal(today),
			Items:     days[index],
		})
	}
	if page.Groups, ok = h.calendarGroups(writer, request, current, group.ID, week.Monday, selection.filter.Kind); !ok {
		return false
	}
	view.Calendar = page
	return true
}

// calendarTeamOptions offers the teams the account sees.
func (h *groupHandler) calendarTeamOptions(writer http.ResponseWriter, request *http.Request, current groupRequest) ([]SelectOptionView, bool) {
	if h.dependencies.Teams == nil {
		return nil, true
	}
	teams, err := h.dependencies.Teams.List(request.Context(), current.access)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return nil, false
	}
	options := make([]SelectOptionView, 0, len(teams))
	for _, summary := range teams {
		options = append(options, SelectOptionView{Value: strconv.FormatInt(summary.Team.ID, 10), Label: summary.Team.Name})
	}
	return options, true
}

// calendarResourceOptions offers the group's resources to accounts that see
// the resource list (staff and coaches), and to everyone the resources named
// on the week's visible events.
func (h *groupHandler) calendarResourceOptions(writer http.ResponseWriter, request *http.Request, current groupRequest, events []service.Event) ([]SelectOptionView, bool) {
	var options []SelectOptionView
	seen := make(map[int64]bool)
	if current.access.CanViewResources() && h.dependencies.Resources != nil {
		resources, err := h.dependencies.Resources.List(request.Context(), current.access)
		if err != nil {
			h.renderEventLoadError(writer, request, current.language, err)
			return nil, false
		}
		for _, resource := range resources {
			seen[resource.ID] = true
			label := resource.Name
			if resource.Archived() {
				label += " (" + h.t(current.language, "events.resource_archived") + ")"
			}
			options = append(options, SelectOptionView{Value: strconv.FormatInt(resource.ID, 10), Label: label})
		}
	}
	for _, resource := range service.CalendarEventResources(events) {
		if !seen[resource.ID] {
			seen[resource.ID] = true
			options = append(options, SelectOptionView{Value: strconv.FormatInt(resource.ID, 10), Label: resource.Name})
		}
	}
	return options, true
}

// calendarGroups lists the account's groups for switching, keeping the week
// and the kind filter; team and resource filters belong to one group and are
// dropped.
func (h *groupHandler) calendarGroups(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, monday time.Time, kind string) ([]CalendarGroupView, bool) {
	if h.dependencies.Groups == nil {
		return nil, true
	}
	summaries, err := h.dependencies.Groups.List(request.Context(), current.user.ID)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return nil, false
	}
	if len(summaries) < 2 {
		return nil, true
	}
	groups := make([]CalendarGroupView, 0, len(summaries))
	for _, summary := range summaries {
		base := h.groupPath(current.language, summary.Group.ID, "/calendar")
		groups = append(groups, CalendarGroupView{
			Name:    summary.Group.Name,
			URL:     calendarURL(base, monday, service.CalendarFilter{Kind: kind}),
			Current: summary.Group.ID == groupID,
		})
	}
	return groups, true
}

// calendarAnswers loads the viewer's own and their children's answers for
// the week with one query and labels them per event. Only events in shown
// (what the viewer sees) get badges. Without participation, no badges.
func (h *groupHandler) calendarAnswers(writer http.ResponseWriter, request *http.Request, current groupRequest, week service.CalendarWeek, shown []service.Event) (map[int64][]CalendarAnswerView, bool) {
	if h.dependencies.Participation == nil || len(shown) == 0 {
		return nil, true
	}
	answers, err := h.dependencies.Participation.CalendarAnswers(request.Context(), current.access, week.From, week.To)
	if err != nil {
		h.renderEventLoadError(writer, request, current.language, err)
		return nil, false
	}
	visible := make(map[int64]bool, len(shown))
	for _, event := range shown {
		visible[event.ID] = true
	}
	byEvent := make(map[int64][]CalendarAnswerView)
	for _, answer := range answers {
		if !visible[answer.EventID] {
			continue
		}
		label := h.t(current.language, "participation.status."+answer.Status)
		if !answer.Self {
			label = h.translate(current.language, "calendar.child_status", map[string]string{
				"name":   h.memberLabel(current.language, answer.UserID, answer.DisplayName, answer.Username, nil),
				"status": label,
			})
		}
		byEvent[answer.EventID] = append(byEvent[answer.EventID], CalendarAnswerView{Code: answer.Status, Label: label, Self: answer.Self})
	}
	return byEvent, true
}

// calendarItems turns the week's events into grid items (one per event and
// day) and the day list's entries.
func (h *groupHandler) calendarItems(language locale.Code, group service.Group, week service.CalendarWeek, events []service.Event, answers map[int64][]CalendarAnswerView) ([]WeekGridItem, [7][]CalendarItemView) {
	var (
		items []WeekGridItem
		days  [7][]CalendarItemView
	)
	cancelled := h.t(language, "events.cancelled")
	for _, event := range events {
		row := h.eventRow(language, group.ID, group.TimeZone, event)
		segments := week.Segments(event)
		for _, segment := range segments {
			entry := CalendarItemView{
				ID:        event.ID,
				URL:       row.URL,
				Title:     row.Title,
				Kind:      row.Kind,
				KindLabel: row.KindLabel,
				Time:      service.FormatMinute(segment.StartMinute) + "–" + service.FormatMinute(segment.EndMinute),
				When:      row.When,
				MultiDay:  len(segments) > 1 || segment.ContinuesBefore || segment.ContinuesAfter,
				Teams:     row.Teams,
				Place:     row.Place,
				Cancelled: row.Cancelled,
				Answers:   answers[event.ID],
			}
			days[segment.Weekday-1] = append(days[segment.Weekday-1], entry)
			item := WeekGridItem{
				ID:          fmt.Sprintf("event-%d-%d", event.ID, segment.Weekday),
				Weekday:     segment.Weekday,
				StartMinute: segment.StartMinute,
				EndMinute:   segment.EndMinute,
				Label:       row.Title,
				Detail:      calendarDetail(row),
				Variant:     calendarVariant(event.Kind),
				URL:         row.URL,
				Muted:       row.Cancelled,
			}
			if row.Cancelled {
				item.Badges = append(item.Badges, cancelled)
			}
			for _, answer := range entry.Answers {
				item.Badges = append(item.Badges, answer.Label)
			}
			items = append(items, item)
		}
	}
	for index := range days {
		slices.SortStableFunc(days[index], func(a, b CalendarItemView) int {
			return cmp.Compare(a.Time, b.Time)
		})
	}
	return items, days
}

func calendarDetail(row EventRowView) string {
	parts := make([]string, 0, 2)
	for _, part := range []string{row.Teams, row.Place} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " · ")
}

// calendarVariant colors events by kind.
func calendarVariant(kind string) WeekGridVariant {
	switch kind {
	case service.EventKindTraining:
		return WeekGridPrimary
	case service.EventKindMatch:
		return WeekGridInfo
	case service.EventKindChampionship:
		return WeekGridAccent
	default:
		return WeekGridSecondary
	}
}

// calendarGridConfig frames a read-only dated week: at least 08:00 to 20:00,
// widened to whole hours so every event fits.
func calendarGridConfig(week service.CalendarWeek, items []WeekGridItem, previousURL, nextURL string) WeekGridConfig {
	start, end := calendarDayStart, calendarDayEnd
	for _, item := range items {
		start = min(start, item.StartMinute/60*60)
		end = max(end, (item.EndMinute+59)/60*60)
	}
	return WeekGridConfig{
		ID:          calendarGridID,
		StartMinute: start,
		EndMinute:   min(end, minutesPerDay),
		Step:        calendarStep,
		FirstDay:    week.Monday,
		PreviousURL: previousURL,
		NextURL:     nextURL,
	}
}

// calendarURL links to the week of monday (zero: the current week) with the
// filter.
func calendarURL(base string, monday time.Time, filter service.CalendarFilter) string {
	values := url.Values{}
	if !monday.IsZero() {
		values.Set("week", service.FormatSeasonDate(monday))
	}
	if filter.TeamID > 0 {
		values.Set("team", strconv.FormatInt(filter.TeamID, 10))
	}
	if filter.ResourceID > 0 {
		values.Set("resource", strconv.FormatInt(filter.ResourceID, 10))
	}
	if filter.Kind != "" {
		values.Set("kind", filter.Kind)
	}
	if len(values) == 0 {
		return base
	}
	return base + "?" + values.Encode()
}

// calendarQueryID reads an optional positive ID; anything else is 0.
func calendarQueryID(raw string) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func optionHasValue(id int64) func(SelectOptionView) bool {
	value := strconv.FormatInt(id, 10)
	return func(option SelectOptionView) bool { return option.Value == value }
}

func selectOptions(options []SelectOptionView, selected string) []SelectOptionView {
	marked := slices.Clone(options)
	for index := range marked {
		marked[index].Selected = marked[index].Value == selected
	}
	return marked
}
