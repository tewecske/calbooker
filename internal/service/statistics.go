package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Statistics periods (#11). Weeks start on Monday; every period is in the
// group's time zone.
const (
	StatisticsPeriodWeek   = "week"
	StatisticsPeriodMonth  = "month"
	StatisticsPeriodPart   = "part"
	StatisticsPeriodSeason = "season"
)

// StatisticsPeriods returns every period kind in display order.
func StatisticsPeriods() []string {
	return []string{StatisticsPeriodWeek, StatisticsPeriodMonth, StatisticsPeriodPart, StatisticsPeriodSeason}
}

var (
	// ErrNilStatisticsRepository identifies missing statistics persistence.
	ErrNilStatisticsRepository = errors.New("service: nil statistics repository")
	// ErrNilStatisticsSeasons identifies a missing season source.
	ErrNilStatisticsSeasons = errors.New("service: nil statistics season source")
	// ErrInvalidStatisticsPeriod identifies an unknown period kind.
	ErrInvalidStatisticsPeriod = errors.New("service: invalid statistics period")
)

// StatisticsFilter is what the aggregation queries read. Events count when
// they are not cancelled, start in [From, To), and ended by Until, so an
// event that has not been held yet is neither an opportunity nor an absence.
type StatisticsFilter struct {
	GroupID int64
	From    time.Time
	To      time.Time
	Until   time.Time
	// AllTeams, TeamIDs, and UserIDs narrow the rows (see
	// GroupAccess.StatisticsPlayerScope). For team statistics only AllTeams
	// and TeamIDs apply.
	AllTeams bool
	TeamIDs  []int64
	UserIDs  []int64
}

// TeamStatistics is one team's numbers in a period. Opportunities are the
// pairs of a held event of the team and a current player of the team; a
// player's answer and confirmed attendance are read from those pairs.
type TeamStatistics struct {
	TeamID        int64
	Name          string
	Players       int
	Events        int
	Opportunities int
	SignedUp      int
	Attended      int
}

// Rate is the share of opportunities that were attended, in whole percent;
// false when there were none.
func (t TeamStatistics) Rate() (int, bool) { return attendanceRate(t.Attended, t.Opportunities) }

// PlayerStatistics is one player's numbers in a period, over the held
// events of the teams the player is on now. Attendance at events of other
// teams (walk-ins) is not counted.
type PlayerStatistics struct {
	UserID      int64
	DisplayName *string
	Username    *string
	Email       *string
	// Teams are the names of the teams the player is on, joined with ", ".
	Teams    string
	Events   int
	SignedUp int
	Attended int
}

// Rate is attended events over held events, in whole percent; false when
// there were no events.
func (p PlayerStatistics) Rate() (int, bool) { return attendanceRate(p.Attended, p.Events) }

func attendanceRate(attended, total int) (int, bool) {
	if total <= 0 {
		return 0, false
	}
	return int(math.Round(float64(attended) * 100 / float64(total))), true
}

// StatisticsRepository aggregates participation in PostgreSQL. Every method
// is scoped to one group and counts in the database.
type StatisticsRepository interface {
	// TeamStatistics returns the group's teams that are not archived (all of
	// them, or those in filter.TeamIDs), by name.
	TeamStatistics(ctx context.Context, filter StatisticsFilter) ([]TeamStatistics, error)
	// PlayerStatistics returns the players in scope (every player on a
	// team, the players of filter.TeamIDs, and filter.UserIDs), by name.
	PlayerStatistics(ctx context.Context, filter StatisticsFilter) ([]PlayerStatistics, error)
}

// StatisticsSeasons is the part of the season store statistics reads.
type StatisticsSeasons interface {
	GroupTimeZone(ctx context.Context, groupID int64) (string, error)
	ListSeasons(ctx context.Context, groupID int64) ([]Season, error)
}

// StatisticsQuery chooses the period: a kind and, for weeks and months, any
// date inside it (zero: today); for seasons and parts, the ID (zero: the
// current one, else the latest).
type StatisticsQuery struct {
	Period   string
	Date     time.Time
	SeasonID int64
	PartID   int64
}

// StatisticsPeriod is the period a report covers.
type StatisticsPeriod struct {
	Kind string
	// StartsOn and EndsOn are the first and last calendar day (group time
	// zone); From and To are the instants [From, To).
	StartsOn time.Time
	EndsOn   time.Time
	From     time.Time
	To       time.Time
	// Name is the season or part name for those kinds.
	Name     string
	SeasonID int64
	PartID   int64
	// Previous and Next are the first days of the neighbouring weeks or
	// months; zero for seasons and parts.
	Previous time.Time
	Next     time.Time
}

// StatisticsReport is the statistics page: the period and the tables the
// account may see.
type StatisticsReport struct {
	Period   StatisticsPeriod
	TimeZone string
	// Seasons are the group's seasons for the pickers, latest first.
	Seasons []Season
	// HasPeriod is false when a season or part period was asked for but the
	// group has none; the tables are then empty.
	HasPeriod bool
	// ShowTeams: the account sees team rows (staff and coaches).
	ShowTeams bool
	Teams     []TeamStatistics
	Players   []PlayerStatistics
}

// StatisticsService answers the participation statistics (#11).
type StatisticsService struct {
	statistics StatisticsRepository
	seasons    StatisticsSeasons
	now        func() time.Time
}

// NewStatisticsService constructs the statistics use cases.
func NewStatisticsService(statistics StatisticsRepository, seasons StatisticsSeasons) (*StatisticsService, error) {
	switch {
	case statistics == nil:
		return nil, ErrNilStatisticsRepository
	case seasons == nil:
		return nil, ErrNilStatisticsSeasons
	}
	return &StatisticsService{statistics: statistics, seasons: seasons, now: time.Now}, nil
}

// Report returns the statistics of one period for the account. Admins,
// managers, and site administrators see every team and player; coaches see
// the teams they coach and those teams' players; players and parents see
// only their own and their children's numbers. Email addresses show for
// staff only.
func (s *StatisticsService) Report(ctx context.Context, access *GroupAccess, query StatisticsQuery) (StatisticsReport, error) {
	if s == nil || s.statistics == nil || s.seasons == nil || s.now == nil {
		return StatisticsReport{}, ErrInvalidGroup
	}
	if ctx == nil {
		return StatisticsReport{}, errors.New("service: nil statistics context")
	}
	if !access.CanViewStatistics() {
		return StatisticsReport{}, ErrGroupNotFound
	}
	zone, err := s.seasons.GroupTimeZone(ctx, access.GroupID())
	if err != nil {
		return StatisticsReport{}, fmt.Errorf("find group time zone: %w", err)
	}
	seasons, err := s.seasons.ListSeasons(ctx, access.GroupID())
	if err != nil {
		return StatisticsReport{}, fmt.Errorf("list seasons: %w", err)
	}
	now := s.now()
	today, err := LocalDate(now, zone)
	if err != nil {
		return StatisticsReport{}, err
	}
	period, found, err := resolveStatisticsPeriod(query, seasons, today, zone)
	if err != nil {
		return StatisticsReport{}, err
	}
	teamIDs, allTeams := access.StatisticsTeamIDs()
	report := StatisticsReport{
		Period: period, TimeZone: zone, Seasons: seasons, HasPeriod: found,
		ShowTeams: allTeams || len(teamIDs) > 0, Teams: []TeamStatistics{}, Players: []PlayerStatistics{},
	}
	if !found {
		return report, nil
	}
	allPlayers, playerTeamIDs, userIDs := access.StatisticsPlayerScope()
	filter := StatisticsFilter{GroupID: access.GroupID(), From: period.From, To: period.To, Until: now, AllTeams: allPlayers, TeamIDs: playerTeamIDs, UserIDs: userIDs}
	if report.ShowTeams {
		if report.Teams, err = s.statistics.TeamStatistics(ctx, filter); err != nil {
			return StatisticsReport{}, fmt.Errorf("team statistics: %w", err)
		}
	}
	if report.Players, err = s.statistics.PlayerStatistics(ctx, filter); err != nil {
		return StatisticsReport{}, fmt.Errorf("player statistics: %w", err)
	}
	if !access.CanViewAllMembers() {
		for i := range report.Players {
			report.Players[i].Email = nil
		}
	}
	return report, nil
}

// resolveStatisticsPeriod turns the query into instants in the group's zone.
// found is false for a season or part period of a group without seasons.
func resolveStatisticsPeriod(query StatisticsQuery, seasons []Season, today time.Time, zone string) (StatisticsPeriod, bool, error) {
	location, err := eventLocation(zone)
	if err != nil {
		return StatisticsPeriod{}, false, err
	}
	period := StatisticsPeriod{Kind: query.Period}
	date := SeasonDate(query.Date)
	if date.IsZero() {
		date = today
	}
	switch query.Period {
	case StatisticsPeriodWeek:
		period.StartsOn = mondayOf(date)
		period.EndsOn = period.StartsOn.AddDate(0, 0, 6)
		period.Previous, period.Next = period.StartsOn.AddDate(0, 0, -7), period.StartsOn.AddDate(0, 0, 7)
	case StatisticsPeriodMonth:
		period.StartsOn = time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
		period.EndsOn = period.StartsOn.AddDate(0, 1, -1)
		period.Previous, period.Next = period.StartsOn.AddDate(0, -1, 0), period.StartsOn.AddDate(0, 1, 0)
	case StatisticsPeriodSeason:
		season, ok := pickSeason(seasons, query.SeasonID, today)
		if !ok {
			return period, false, nil
		}
		period.StartsOn, period.EndsOn, period.Name, period.SeasonID = season.StartsOn, season.EndsOn, season.Name, season.ID
	case StatisticsPeriodPart:
		season, part, ok := pickPart(seasons, query.PartID, today)
		if !ok {
			return period, false, nil
		}
		period.StartsOn, period.EndsOn, period.Name, period.SeasonID, period.PartID = part.StartsOn, part.EndsOn, season.Name+": "+part.Name, season.ID, part.ID
	default:
		return StatisticsPeriod{}, false, ErrInvalidStatisticsPeriod
	}
	period.From = localMidnight(period.StartsOn, location)
	period.To = localMidnight(period.EndsOn.AddDate(0, 0, 1), location)
	return period, true, nil
}

// localMidnight is the instant a calendar date starts in location.
func localMidnight(day time.Time, location *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).UTC()
}

// pickSeason returns the season with id, else the one containing today, else
// the latest (seasons are latest first).
func pickSeason(seasons []Season, id int64, today time.Time) (Season, bool) {
	if id > 0 {
		for _, season := range seasons {
			if season.ID == id {
				return season, true
			}
		}
		return Season{}, false
	}
	for _, season := range seasons {
		if season.Contains(today) {
			return season, true
		}
	}
	if len(seasons) > 0 {
		return seasons[0], true
	}
	return Season{}, false
}

// pickPart returns the part with id, else the one containing today, else
// the first part of the current or latest season.
func pickPart(seasons []Season, id int64, today time.Time) (Season, SeasonPart, bool) {
	if id > 0 {
		for _, season := range seasons {
			if part, ok := season.Part(id); ok {
				return season, part, true
			}
		}
		return Season{}, SeasonPart{}, false
	}
	season, ok := pickSeason(seasons, 0, today)
	if !ok || len(season.Parts) == 0 {
		return Season{}, SeasonPart{}, false
	}
	if part, ok := season.PartOn(today); ok {
		return season, part, true
	}
	return season, season.Parts[0], true
}
