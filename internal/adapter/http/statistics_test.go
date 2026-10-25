package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

type statisticsUseCaseStub struct {
	report service.StatisticsReport
	err    error
	calls  int
	query  service.StatisticsQuery
}

func (s *statisticsUseCaseStub) Report(_ context.Context, _ *service.GroupAccess, query service.StatisticsQuery) (service.StatisticsReport, error) {
	s.calls++
	s.query = query
	return s.report, s.err
}

func statisticsDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *statisticsUseCaseStub) {
	t.Helper()
	dependencies, _, _ := groupTestDependencies(t, access)
	anna := "Anna Player"
	day := func(month time.Month, d int) time.Time { return time.Date(2026, month, d, 0, 0, 0, 0, time.UTC) }
	stub := &statisticsUseCaseStub{report: service.StatisticsReport{
		Period: service.StatisticsPeriod{
			Kind: service.StatisticsPeriodMonth, StartsOn: day(11, 1), EndsOn: day(11, 30),
			Previous: day(10, 1), Next: day(12, 1),
		},
		TimeZone: "Europe/Budapest", HasPeriod: true, ShowTeams: true,
		Teams:   []service.TeamStatistics{{TeamID: 4, Name: "Lions", Players: 2, Events: 3, Opportunities: 6, SignedUp: 2, Attended: 3}},
		Players: []service.PlayerStatistics{{UserID: 7, DisplayName: &anna, Teams: "Lions", Events: 3, SignedUp: 1, Attended: 2}},
	}}
	dependencies.Statistics = stub
	return dependencies, stub
}

func TestStatisticsPage(t *testing.T) {
	dependencies, stub := statisticsDependencies(t, groupMemberAs(service.GroupRoleCoach))
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/statistics?period=month&date=2026-11-15", "", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{
		`id="group-statistics-heading"`, `data-section="statistics" aria-current="page"`, `data-testid="statistics-teams"`, `href="/en/groups/3/teams/4"`,
		"Anna Player", ">2026-11<", "2026-11-01 – 2026-11-30", "Europe/Budapest", ">50%<", ">67%<",
		`href="/en/groups/3/statistics?date=2026-10-01&amp;period=month"`, `data-testid="statistics-tab-week"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("statistics page missing %q", want)
		}
	}
	if stub.query.Period != service.StatisticsPeriodMonth || stub.query.Date.Format("2006-01-02") != "2026-11-15" {
		t.Fatalf("query = %+v", stub.query)
	}
}

func TestStatisticsQueryFallsBack(t *testing.T) {
	dependencies, stub := statisticsDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/statistics?period=decade&date=nonsense&season=-3&part=x", "", false)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if stub.query.Period != service.StatisticsPeriodMonth || !stub.query.Date.IsZero() || stub.query.SeasonID != 0 || stub.query.PartID != 0 {
		t.Fatalf("query = %+v", stub.query)
	}
}

func TestStatisticsOwnNumbersHideTeams(t *testing.T) {
	dependencies, stub := statisticsDependencies(t, groupMemberAs(service.GroupRolePlayer))
	stub.report.ShowTeams, stub.report.Teams = false, nil
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/statistics", "", false).Body.String()
	if strings.Contains(body, `data-testid="statistics-teams"`) || !strings.Contains(body, "Your own numbers") || !strings.Contains(body, `data-testid="statistics-players"`) {
		t.Fatal("a player sees team rows or misses their own table")
	}
}

func TestStatisticsNoSeasonPeriod(t *testing.T) {
	dependencies, stub := statisticsDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	stub.report = service.StatisticsReport{Period: service.StatisticsPeriod{Kind: service.StatisticsPeriodSeason}, TimeZone: "Europe/Budapest", ShowTeams: true}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/statistics?period=season", "", false).Body.String()
	if !strings.Contains(body, `data-testid="statistics-no-seasons"`) || strings.Contains(body, `data-testid="statistics-teams"`) {
		t.Fatal("empty season period page is wrong")
	}
}

func TestStatisticsOutsiderGetsNotFound(t *testing.T) {
	dependencies, stub := statisticsDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, target := range []string{"/en/groups/3/statistics", "/hu/groups/3/statistics?period=week"} {
		response := serveGroups(t, dependencies, http.MethodGet, target, "", false)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
			t.Errorf("GET %s = %d, want 404 without the group name", target, response.Code)
		}
	}
	if stub.calls != 0 {
		t.Fatal("outsider request reached the use case")
	}
}

func TestStatisticsServiceNotFoundIs404(t *testing.T) {
	dependencies, stub := statisticsDependencies(t, groupMemberAs(service.GroupRolePlayer))
	stub.err = service.ErrGroupNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/statistics", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}
