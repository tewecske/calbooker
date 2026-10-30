package httpadapter

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// groupSectionStatistics is the participation statistics page (#11).
const groupSectionStatistics = "statistics"

// StatisticsUseCases aggregates the participation statistics consumed by
// HTTP handlers.
type StatisticsUseCases interface {
	Report(ctx context.Context, access *service.GroupAccess, query service.StatisticsQuery) (service.StatisticsReport, error)
}

// StatisticsView is the statistics page: the period chooser and the tables
// the account may see.
type StatisticsView struct {
	ActionURL string
	Tabs      []StatisticsTabView
	Period    string
	// Heading names the period; Dates are its first and last day.
	Heading string
	Dates   string
	// PreviousURL and NextURL step weeks and months.
	PreviousURL string
	NextURL     string
	// Picker chooses a season or part; PickerName is its form field.
	Picker     []SelectOptionView
	PickerName string
	HasPeriod  bool
	TimeZone   string
	Intro      string
	ShowTeams  bool
	Teams      []StatisticsTeamRowView
	Players    []StatisticsPlayerRowView
}

// StatisticsTabView is one period kind.
type StatisticsTabView struct {
	Kind    string
	Label   string
	URL     string
	Current bool
}

// StatisticsTeamRowView is one team's row.
type StatisticsTeamRowView struct {
	ID       int64
	Name     string
	URL      string
	Players  int
	Events   int
	SignedUp int
	Attended int
	Rate     string
}

// StatisticsPlayerRowView is one player's row.
type StatisticsPlayerRowView struct {
	UserID   int64
	Label    string
	Email    string
	Teams    string
	Self     bool
	Child    bool
	Events   int
	SignedUp int
	Attended int
	Rate     string
}

// registerStatisticsRoutes registers each group's statistics page.
func registerStatisticsRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{group}/statistics", groups.statistics)
	}
}

func (h *groupHandler) statistics(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.statisticsRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionStatistics, 0, FormData{}, nil, http.StatusOK)
}

// statisticsRequest authorizes the page: outsiders get 404 from
// groupRequest; every member sees at least their own numbers.
func (h *groupHandler) statisticsRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Statistics == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanViewStatistics() {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

// parseStatisticsQuery reads the period from the query string; anything
// unknown falls back to the current month, the current season, or the
// current part.
func parseStatisticsQuery(request *http.Request) service.StatisticsQuery {
	values := request.URL.Query()
	query := service.StatisticsQuery{Period: service.StatisticsPeriodMonth}
	if period := values.Get("period"); slices.Contains(service.StatisticsPeriods(), period) {
		query.Period = period
	}
	query.Date = service.ParseSeasonDate(strings.TrimSpace(values.Get("date")))
	if id, err := strconv.ParseInt(strings.TrimSpace(values.Get("season")), 10, 64); err == nil && id > 0 {
		query.SeasonID = id
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(values.Get("part")), 10, 64); err == nil && id > 0 {
		query.PartID = id
	}
	return query
}

func (h *groupHandler) statisticsURL(language locale.Code, groupSlug string, values url.Values) string {
	target := h.groupPath(language, groupSlug, "/statistics")
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	return target
}

// loadStatistics fills the statistics page.
func (h *groupHandler) loadStatistics(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, view *GroupDetailView) bool {
	if h.dependencies.Statistics == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	language := current.language
	report, err := h.dependencies.Statistics.Report(request.Context(), current.access, parseStatisticsQuery(request))
	if err != nil {
		h.renderLoadError(writer, request, language, err)
		return false
	}
	period := report.Period
	stats := &StatisticsView{
		ActionURL: h.groupPath(language, current.slug, "/statistics"),
		Period:    period.Kind,
		HasPeriod: report.HasPeriod,
		TimeZone:  report.TimeZone,
		ShowTeams: report.ShowTeams,
	}
	for _, kind := range service.StatisticsPeriods() {
		stats.Tabs = append(stats.Tabs, StatisticsTabView{
			Kind:    kind,
			Label:   h.t(language, "statistics.period_"+kind),
			URL:     h.statisticsURL(language, current.slug, url.Values{"period": {kind}}),
			Current: kind == period.Kind,
		})
	}
	switch {
	case !report.HasPeriod:
		stats.Heading = h.t(language, "statistics.period_"+period.Kind)
	case period.Kind == service.StatisticsPeriodWeek || period.Kind == service.StatisticsPeriodMonth:
		stats.Heading = h.t(language, "statistics.period_"+period.Kind) + " " + service.FormatSeasonDate(period.StartsOn)
		if period.Kind == service.StatisticsPeriodMonth {
			stats.Heading = period.StartsOn.Format("2006-01")
		}
		stats.PreviousURL = h.statisticsURL(language, current.slug, url.Values{"period": {period.Kind}, "date": {service.FormatSeasonDate(period.Previous)}})
		stats.NextURL = h.statisticsURL(language, current.slug, url.Values{"period": {period.Kind}, "date": {service.FormatSeasonDate(period.Next)}})
	default:
		stats.Heading = period.Name
	}
	if report.HasPeriod {
		stats.Dates = service.FormatSeasonDate(period.StartsOn) + " – " + service.FormatSeasonDate(period.EndsOn)
	}
	switch period.Kind {
	case service.StatisticsPeriodSeason:
		stats.PickerName = "season"
		for _, season := range report.Seasons {
			stats.Picker = append(stats.Picker, SelectOptionView{Value: strconv.FormatInt(season.ID, 10), Label: season.Name, Selected: season.ID == period.SeasonID})
		}
	case service.StatisticsPeriodPart:
		stats.PickerName = "part"
		for _, season := range report.Seasons {
			for _, part := range season.Parts {
				stats.Picker = append(stats.Picker, SelectOptionView{Value: strconv.FormatInt(part.ID, 10), Label: season.Name + ": " + part.Name, Selected: part.ID == period.PartID})
			}
		}
	}
	intro := "statistics.intro_own"
	switch {
	case current.access.CanManageGroup():
		intro = "statistics.intro_staff"
	case report.ShowTeams:
		intro = "statistics.intro_coach"
	}
	stats.Intro = h.t(language, intro)
	for _, team := range report.Teams {
		stats.Teams = append(stats.Teams, StatisticsTeamRowView{
			ID: team.TeamID, Name: team.Name, URL: h.teamPath(current, team.TeamID, ""),
			Players: team.Players, Events: team.Events, SignedUp: team.SignedUp, Attended: team.Attended, Rate: rateLabel(team.Rate()),
		})
	}
	for _, player := range report.Players {
		row := StatisticsPlayerRowView{
			UserID: player.UserID, Label: h.memberLabel(language, player.UserID, player.DisplayName, player.Username, player.Email), Teams: player.Teams,
			Self: player.UserID == current.access.UserID(), Child: current.access.IsGuardianOf(player.UserID),
			Events: player.Events, SignedUp: player.SignedUp, Attended: player.Attended, Rate: rateLabel(player.Rate()),
		}
		if player.Email != nil {
			row.Email = *player.Email
		}
		stats.Players = append(stats.Players, row)
	}
	view.Statistics = stats
	return true
}

// rateLabel writes a whole-percent rate, or a dash when there was nothing
// to attend.
func rateLabel(percent int, ok bool) string {
	if !ok {
		return "–"
	}
	return strconv.Itoa(percent) + "%"
}
