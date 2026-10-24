package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type statisticsRepositoryStub struct {
	teams   []TeamStatistics
	players []PlayerStatistics
	teamErr error
	filters []StatisticsFilter
	calls   int
}

func (s *statisticsRepositoryStub) TeamStatistics(_ context.Context, filter StatisticsFilter) ([]TeamStatistics, error) {
	s.calls++
	s.filters = append(s.filters, filter)
	return s.teams, s.teamErr
}

func (s *statisticsRepositoryStub) PlayerStatistics(_ context.Context, filter StatisticsFilter) ([]PlayerStatistics, error) {
	s.calls++
	s.filters = append(s.filters, filter)
	return s.players, nil
}

type statisticsSeasonsStub struct {
	zone    string
	seasons []Season
}

func (s statisticsSeasonsStub) GroupTimeZone(context.Context, int64) (string, error) {
	return s.zone, nil
}

func (s statisticsSeasonsStub) ListSeasons(context.Context, int64) ([]Season, error) {
	return s.seasons, nil
}

func statisticsDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func statisticsSeasons() []Season {
	return []Season{
		{ID: 2, Name: "2026/27", StartsOn: statisticsDay(2026, 8, 15), EndsOn: statisticsDay(2027, 6, 15), Parts: []SeasonPart{
			{ID: 21, SeasonID: 2, Position: 1, Name: "Autumn", StartsOn: statisticsDay(2026, 8, 15), EndsOn: statisticsDay(2026, 12, 20)},
			{ID: 22, SeasonID: 2, Position: 2, Name: "Spring", StartsOn: statisticsDay(2027, 1, 10), EndsOn: statisticsDay(2027, 6, 15)},
		}},
		{ID: 1, Name: "2025/26", StartsOn: statisticsDay(2025, 8, 15), EndsOn: statisticsDay(2026, 6, 15), Parts: []SeasonPart{
			{ID: 11, SeasonID: 1, Position: 1, Name: "Autumn", StartsOn: statisticsDay(2025, 8, 15), EndsOn: statisticsDay(2025, 12, 20)},
		}},
	}
}

func newStatisticsService(t *testing.T, repository *statisticsRepositoryStub, zone string, seasons []Season, now time.Time) *StatisticsService {
	t.Helper()
	service, err := NewStatisticsService(repository, statisticsSeasonsStub{zone: zone, seasons: seasons})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return service
}

func utc(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func TestStatisticsPeriodBoundariesFollowTheGroupZone(t *testing.T) {
	now := utc(2026, 11, 15, 12, 0)
	seasons := statisticsSeasons()
	tests := []struct {
		name         string
		zone         string
		query        StatisticsQuery
		wantFrom     time.Time
		wantTo       time.Time
		wantStarts   string
		wantEnds     string
		wantPrevious string
	}{
		{
			name: "november in Budapest starts an hour before UTC midnight", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodMonth, Date: statisticsDay(2026, 11, 20)},
			wantFrom: utc(2026, 10, 31, 23, 0), wantTo: utc(2026, 11, 30, 23, 0),
			wantStarts: "2026-11-01", wantEnds: "2026-11-30", wantPrevious: "2026-10-01",
		},
		{
			name: "october in Budapest spans the end of summer time", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodMonth, Date: statisticsDay(2026, 10, 1)},
			wantFrom: utc(2026, 9, 30, 22, 0), wantTo: utc(2026, 10, 31, 23, 0),
			wantStarts: "2026-10-01", wantEnds: "2026-10-31", wantPrevious: "2026-09-01",
		},
		{
			name: "the week with the clock change is 169 hours long", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodWeek, Date: statisticsDay(2026, 10, 22)},
			wantFrom: utc(2026, 10, 18, 22, 0), wantTo: utc(2026, 10, 25, 23, 0),
			wantStarts: "2026-10-19", wantEnds: "2026-10-25", wantPrevious: "2026-10-12",
		},
		{
			name: "a Sunday belongs to the week that started on Monday", zone: "UTC",
			query:    StatisticsQuery{Period: StatisticsPeriodWeek, Date: statisticsDay(2026, 11, 8)},
			wantFrom: utc(2026, 11, 2, 0, 0), wantTo: utc(2026, 11, 9, 0, 0),
			wantStarts: "2026-11-02", wantEnds: "2026-11-08", wantPrevious: "2026-10-26",
		},
		{
			name: "west of Greenwich the month starts after UTC midnight", zone: "America/New_York",
			query:    StatisticsQuery{Period: StatisticsPeriodMonth, Date: statisticsDay(2026, 11, 20)},
			wantFrom: utc(2026, 11, 1, 4, 0), wantTo: utc(2026, 12, 1, 5, 0),
			wantStarts: "2026-11-01", wantEnds: "2026-11-30", wantPrevious: "2026-10-01",
		},
		{
			name: "today decides without a date", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodMonth},
			wantFrom: utc(2026, 10, 31, 23, 0), wantTo: utc(2026, 11, 30, 23, 0),
			wantStarts: "2026-11-01", wantEnds: "2026-11-30", wantPrevious: "2026-10-01",
		},
		{
			name: "a season runs from its first to its last day", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodSeason, SeasonID: 1},
			wantFrom: utc(2025, 8, 14, 22, 0), wantTo: utc(2026, 6, 15, 22, 0),
			wantStarts: "2025-08-15", wantEnds: "2026-06-15",
		},
		{
			name: "the current season is the default", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodSeason},
			wantFrom: utc(2026, 8, 14, 22, 0), wantTo: utc(2027, 6, 15, 22, 0),
			wantStarts: "2026-08-15", wantEnds: "2027-06-15",
		},
		{
			name: "a part ends with winter time", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodPart, PartID: 21},
			wantFrom: utc(2026, 8, 14, 22, 0), wantTo: utc(2026, 12, 20, 23, 0),
			wantStarts: "2026-08-15", wantEnds: "2026-12-20",
		},
		{
			name: "the current part is the default", zone: "Europe/Budapest",
			query:    StatisticsQuery{Period: StatisticsPeriodPart},
			wantFrom: utc(2026, 8, 14, 22, 0), wantTo: utc(2026, 12, 20, 23, 0),
			wantStarts: "2026-08-15", wantEnds: "2026-12-20",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &statisticsRepositoryStub{}
			stats := newStatisticsService(t, repository, test.zone, seasons, now)
			report, err := stats.Report(context.Background(), accessFor(memberWith(GroupRoleAdmin)), test.query)
			if err != nil || !report.HasPeriod {
				t.Fatalf("Report() = %+v, %v", report, err)
			}
			period := report.Period
			if !period.From.Equal(test.wantFrom) || !period.To.Equal(test.wantTo) {
				t.Errorf("instants = [%s, %s), want [%s, %s)", period.From, period.To, test.wantFrom, test.wantTo)
			}
			if got := FormatSeasonDate(period.StartsOn); got != test.wantStarts {
				t.Errorf("starts on %s, want %s", got, test.wantStarts)
			}
			if got := FormatSeasonDate(period.EndsOn); got != test.wantEnds {
				t.Errorf("ends on %s, want %s", got, test.wantEnds)
			}
			if test.wantPrevious != "" && FormatSeasonDate(period.Previous) != test.wantPrevious {
				t.Errorf("previous %s, want %s", FormatSeasonDate(period.Previous), test.wantPrevious)
			}
			// Only events that ended by now count.
			if len(repository.filters) == 0 || !repository.filters[0].Until.Equal(now) {
				t.Errorf("filters = %+v, want Until %s", repository.filters, now)
			}
		})
	}
}

func TestStatisticsPeriodErrorsAndEmptyGroups(t *testing.T) {
	now := utc(2026, 11, 15, 12, 0)
	access := accessFor(memberWith(GroupRoleAdmin))
	stats := newStatisticsService(t, &statisticsRepositoryStub{}, "Europe/Budapest", nil, now)
	if _, err := stats.Report(context.Background(), access, StatisticsQuery{Period: "decade"}); !errors.Is(err, ErrInvalidStatisticsPeriod) {
		t.Fatalf("unknown period error = %v", err)
	}
	for _, period := range []string{StatisticsPeriodSeason, StatisticsPeriodPart} {
		repository := &statisticsRepositoryStub{}
		stats := newStatisticsService(t, repository, "Europe/Budapest", nil, now)
		report, err := stats.Report(context.Background(), access, StatisticsQuery{Period: period})
		if err != nil || report.HasPeriod || repository.calls != 0 || len(report.Teams) != 0 {
			t.Fatalf("%s without seasons = %+v, %v, calls %d", period, report, err, repository.calls)
		}
	}
	// An unknown season or part is not silently replaced.
	stats = newStatisticsService(t, &statisticsRepositoryStub{}, "Europe/Budapest", statisticsSeasons(), now)
	for _, query := range []StatisticsQuery{{Period: StatisticsPeriodSeason, SeasonID: 99}, {Period: StatisticsPeriodPart, PartID: 99}} {
		if report, err := stats.Report(context.Background(), access, query); err != nil || report.HasPeriod {
			t.Fatalf("%+v = %+v, %v; want no period", query, report, err)
		}
	}
	bad := newStatisticsService(t, &statisticsRepositoryStub{}, "Mars/Olympus", nil, now)
	if _, err := bad.Report(context.Background(), access, StatisticsQuery{Period: StatisticsPeriodMonth}); !errors.Is(err, ErrInvalidTimeZone) {
		t.Fatalf("bad zone error = %v", err)
	}
}

func TestStatisticsReportScopesByRole(t *testing.T) {
	now := utc(2026, 11, 15, 12, 0)
	email := "anna@example.test"
	players := []PlayerStatistics{{UserID: 7, Email: &email, Events: 4, Attended: 3}}
	query := StatisticsQuery{Period: StatisticsPeriodMonth}
	tests := []struct {
		name      string
		access    *GroupAccess
		teams     bool
		all       bool
		teamIDs   []int64
		userIDs   []int64
		wantEmail bool
	}{
		{name: "admin", access: accessFor(memberWith(GroupRoleAdmin)), teams: true, all: true, wantEmail: true},
		{name: "manager", access: accessFor(memberWith(GroupRoleManager)), teams: true, all: true, wantEmail: true},
		{name: "site administrator", access: accessFor(GroupAccessGrants{SiteAdmin: true}), teams: true, all: true, wantEmail: true},
		{
			name: "coach", access: accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{2, 3}}),
			teams: true, teamIDs: []int64{2, 3}, userIDs: []int64{7},
		},
		{
			name: "coach without teams", access: accessFor(memberWith(GroupRoleCoach)),
			userIDs: []int64{7},
		},
		{
			name: "player", access: accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}}}),
			userIDs: []int64{7},
		},
		{
			name: "parent", access: accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildUserIDs: []int64{20, 21}, ChildTeamIDs: []int64{2}}),
			userIDs: []int64{7, 20, 21},
		},
		{
			name: "coached teams count only with the coach role", access: accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, CoachedTeamIDs: []int64{2}}),
			userIDs: []int64{7},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &statisticsRepositoryStub{
				teams:   []TeamStatistics{{TeamID: 2, Name: "Lions"}},
				players: slices.Clone(players),
			}
			stats := newStatisticsService(t, repository, "Europe/Budapest", nil, now)
			report, err := stats.Report(context.Background(), test.access, query)
			if err != nil {
				t.Fatal(err)
			}
			if report.ShowTeams != test.teams || (len(report.Teams) > 0) != test.teams {
				t.Errorf("teams shown = %v (%d rows), want %v", report.ShowTeams, len(report.Teams), test.teams)
			}
			filter := repository.filters[len(repository.filters)-1]
			if filter.AllTeams != test.all || !slices.Equal(filter.TeamIDs, test.teamIDs) || !slices.Equal(filter.UserIDs, test.userIDs) || filter.GroupID != 5 {
				t.Errorf("filter = %+v, want all %v teams %v users %v", filter, test.all, test.teamIDs, test.userIDs)
			}
			if got := report.Players[0].Email != nil; got != test.wantEmail {
				t.Errorf("email shown = %v, want %v", got, test.wantEmail)
			}
		})
	}
}

func TestStatisticsReportRefusesOutsiders(t *testing.T) {
	repository := &statisticsRepositoryStub{}
	stats := newStatisticsService(t, repository, "Europe/Budapest", nil, utc(2026, 11, 15, 12, 0))
	for _, access := range []*GroupAccess{nil, accessFor(GroupAccessGrants{}), NewGroupAccess(0, 7, GroupAccessGrants{Member: true})} {
		if _, err := stats.Report(context.Background(), access, StatisticsQuery{Period: StatisticsPeriodMonth}); !errors.Is(err, ErrGroupNotFound) {
			t.Errorf("Report(%+v) error = %v, want ErrGroupNotFound", access, err)
		}
	}
	if repository.calls != 0 {
		t.Fatal("an outsider reached the repository")
	}
	repository.teamErr = errors.New("boom")
	if _, err := stats.Report(context.Background(), accessFor(memberWith(GroupRoleAdmin)), StatisticsQuery{Period: StatisticsPeriodMonth}); err == nil {
		t.Fatal("repository failure was swallowed")
	}
}

func TestStatisticsRates(t *testing.T) {
	if rate, ok := (TeamStatistics{Opportunities: 6, Attended: 3}).Rate(); !ok || rate != 50 {
		t.Errorf("team rate = %d, %v", rate, ok)
	}
	if rate, ok := (PlayerStatistics{Events: 3, Attended: 2}).Rate(); !ok || rate != 67 {
		t.Errorf("player rate = %d, %v", rate, ok)
	}
	if _, ok := (PlayerStatistics{Attended: 1}).Rate(); ok {
		t.Error("a rate without events")
	}
	if _, err := NewStatisticsService(nil, statisticsSeasonsStub{}); !errors.Is(err, ErrNilStatisticsRepository) {
		t.Errorf("nil repository error = %v", err)
	}
	if _, err := NewStatisticsService(&statisticsRepositoryStub{}, nil); !errors.Is(err, ErrNilStatisticsSeasons) {
		t.Errorf("nil seasons error = %v", err)
	}
}

func TestGroupAccessStatistics(t *testing.T) {
	coach := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{2}})
	if !coach.CanViewTeamStatistics(2) || coach.CanViewTeamStatistics(3) {
		t.Error("a coach sees exactly the teams they coach")
	}
	player := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}}})
	if player.CanViewTeamStatistics(2) || !player.CanViewStatistics() {
		t.Error("a player sees their numbers but no team table")
	}
	if !accessFor(memberWith(GroupRoleManager)).CanViewTeamStatistics(9) {
		t.Error("a manager sees every team")
	}
	if accessFor(GroupAccessGrants{}).CanViewStatistics() {
		t.Error("a non-member opened the statistics")
	}
}
