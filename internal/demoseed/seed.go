package demoseed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TimeZone is the zone of every demo group (the schema default).
const TimeZone = "Europe/Budapest"

const ledgerTable = "schema_migrations"

// SeasonSpec is the demo season, derived from today's date.
type SeasonSpec struct {
	Name         string
	Starts, Ends time.Time
	// Part1 alternates A/B weeks; Part2 does not.
	Part1Name, Part2Name string
	Part1Ends, Part2From time.Time
}

// Season returns the sports year containing today: 1 September to 30 June,
// split into an autumn part (alternating weeks, to 31 January) and a spring
// part. Dates are midnight UTC so they store as plain DATE values.
func Season(today time.Time) SeasonSpec {
	year := today.Year()
	if today.Month() < time.September {
		year--
	}
	date := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	return SeasonSpec{
		Name:      fmt.Sprintf("%d/%02d", year, (year+1)%100),
		Starts:    date(year, time.September, 1),
		Ends:      date(year+1, time.June, 30),
		Part1Name: "Autumn",
		Part2Name: "Spring",
		Part1Ends: date(year+1, time.January, 31),
		Part2From: date(year+1, time.February, 1),
	}
}

// EventSpec is one demo event relative to today.
type EventSpec struct {
	DayOffset int
	Kind      string // training or match
	// Nominated events only list the players the coach picked.
	Nominated bool
}

// EventSpecs are the events of every team: four past (confirmed attendance)
// and three upcoming. Past events end before today starts.
func EventSpecs() []EventSpec {
	return []EventSpec{
		{DayOffset: -14, Kind: "training"},
		{DayOffset: -9, Kind: "training"},
		{DayOffset: -6, Kind: "match"},
		{DayOffset: -3, Kind: "training"},
		{DayOffset: 2, Kind: "training"},
		{DayOffset: 5, Kind: "match", Nominated: true},
		{DayOffset: 7, Kind: "training"},
	}
}

// SlotSpec is one resource slot of a season part; Resource indexes the two
// field resources.
type SlotSpec struct {
	Resource         int
	Week             string
	Weekday          int // ISO, Monday = 1
	StartMin, EndMin int
	MaxTeams         int // 0 = unlimited
	MinAge, MaxAge   int // 0 = any
}

// Slots returns the plan slots of a season part (1 or 2).
func Slots(part int) []SlotSpec {
	if part == 1 {
		return []SlotSpec{
			{Resource: 0, Week: "A", Weekday: 2, StartMin: 16 * 60, EndMin: 17*60 + 30, MaxTeams: 2},
			{Resource: 0, Week: "B", Weekday: 2, StartMin: 17 * 60, EndMin: 18*60 + 30, MaxTeams: 2},
			{Resource: 0, Week: "all", Weekday: 4, StartMin: 16 * 60, EndMin: 17*60 + 30, MaxTeams: 2},
			{Resource: 1, Week: "all", Weekday: 2, StartMin: 17*60 + 30, EndMin: 19 * 60, MaxTeams: 1},
			{Resource: 1, Week: "all", Weekday: 4, StartMin: 17*60 + 30, EndMin: 19 * 60, MaxTeams: 1, MinAge: 10, MaxAge: 16},
			{Resource: 1, Week: "A", Weekday: 6, StartMin: 9 * 60, EndMin: 11 * 60},
		}
	}
	return []SlotSpec{
		{Resource: 0, Week: "all", Weekday: 2, StartMin: 16 * 60, EndMin: 17*60 + 30, MaxTeams: 2},
		{Resource: 0, Week: "all", Weekday: 4, StartMin: 16 * 60, EndMin: 17*60 + 30, MaxTeams: 2},
		{Resource: 1, Week: "all", Weekday: 2, StartMin: 17*60 + 30, EndMin: 19 * 60, MaxTeams: 1},
		{Resource: 1, Week: "all", Weekday: 4, StartMin: 17*60 + 30, EndMin: 19 * 60, MaxTeams: 1},
		{Resource: 1, Week: "all", Weekday: 6, StartMin: 9 * 60, EndMin: 11 * 60},
	}
}

// AvailabilityWindow is a weekly window in which a coach can train a team.
type AvailabilityWindow struct {
	Week             string
	Weekday          int
	StartMin, EndMin int
}

// Availability returns the windows of the n-th (0-based) coach in part.
func Availability(coachIndex, part int) []AvailabilityWindow {
	windows := []AvailabilityWindow{
		{Week: "all", Weekday: 2, StartMin: 16 * 60, EndMin: 20 * 60},
		{Week: "all", Weekday: 4, StartMin: 16 * 60, EndMin: 20 * 60},
	}
	if coachIndex%2 == 1 {
		week := "all"
		if part == 1 {
			week = "A"
		}
		windows = append(windows, AvailabilityWindow{Week: week, Weekday: 6, StartMin: 9 * 60, EndMin: 13 * 60})
	}
	return windows
}

// Wipe empties every table of the connection's current schema except the
// migration ledger and restarts identity counters.
func Wipe(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT quote_ident(tablename) FROM pg_tables
		WHERE schemaname = current_schema() AND tablename <> $1 ORDER BY tablename`, ledgerTable)
	if err != nil {
		return 0, fmt.Errorf("list tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return 0, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(tables) == 0 {
		return 0, errors.New("no tables found; apply migrations first")
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		return 0, fmt.Errorf("truncate: %w", err)
	}
	return len(tables), nil
}

// Seed wipes the database and inserts the demo data in one transaction.
// subjects maps every account email (see Accounts) to its issuer subject.
// now fixes "today" so tests are repeatable.
func Seed(ctx context.Context, db *sql.DB, subjects map[string]string, now time.Time) error {
	for _, a := range Accounts() {
		if subjects[a.Email] == "" && !a.SiteAdmin {
			return fmt.Errorf("no issuer subject for %s", a.Email)
		}
	}
	loc, err := time.LoadLocation(TimeZone)
	if err != nil {
		return fmt.Errorf("load time zone: %w", err)
	}
	if _, err := Wipe(ctx, db); err != nil {
		return err
	}
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	s := &seeder{ctx: ctx, tx: transaction, now: now, loc: loc, users: map[string]int64{}}
	s.run(subjects)
	if s.err != nil {
		_ = transaction.Rollback()
		return s.err
	}
	return transaction.Commit()
}

// seeder collects the first error so the layout code reads straight through.
type seeder struct {
	ctx   context.Context
	tx    *sql.Tx
	now   time.Time
	loc   *time.Location
	users map[string]int64
	err   error
}

func (s *seeder) exec(query string, args ...any) {
	if s.err != nil {
		return
	}
	if _, err := s.tx.ExecContext(s.ctx, query, args...); err != nil {
		s.err = fmt.Errorf("%s: %w", firstLine(query), err)
	}
}

func (s *seeder) insert(query string, args ...any) int64 {
	if s.err != nil {
		return 0
	}
	var id int64
	if err := s.tx.QueryRowContext(s.ctx, query+" RETURNING id", args...).Scan(&id); err != nil {
		s.err = fmt.Errorf("%s: %w", firstLine(query), err)
	}
	return id
}

func firstLine(query string) string {
	query = strings.TrimSpace(query)
	line, _, _ := strings.Cut(query, "\n")
	return strings.TrimSpace(line)
}

func (s *seeder) run(subjects map[string]string) {
	unix := s.now.Unix()
	for _, a := range Accounts() {
		if subjects[a.Email] == "" {
			continue // the site admin, when the provider has no such account
		}
		id := s.insert(`
			INSERT INTO users (email, display_name, kind, is_admin, created_at, email_verified_at)
			VALUES ($1, $2, 'account', $3, $4, $4)`, a.Email, a.DisplayName, a.SiteAdmin, unix)
		s.users[a.Email] = id
		s.exec(`
			INSERT INTO oauth_identities (user_id, provider, subject, email, created_at)
			VALUES ($1, 'sso', $2, $3, $4)`, id, subjects[a.Email], a.Email, unix)
	}
	for _, g := range Layout() {
		s.group(g)
	}
}

func (s *seeder) group(g GroupPlan) {
	unix := s.now.Unix()
	groupID := s.insert(`
		INSERT INTO groups (name, slug, name_norm, time_zone, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, g.Name, g.Slug, strings.ToLower(g.Name), TimeZone, s.users[g.Admin()], unix)
	for _, m := range g.Members {
		userID := s.users[m.Email]
		s.exec(`INSERT INTO group_members (group_id, user_id, created_at) VALUES ($1, $2, $3)`, groupID, userID, unix)
		for _, role := range m.Roles {
			s.exec(`INSERT INTO group_member_roles (group_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)`, groupID, userID, role, unix)
		}
	}

	resources := []int64{
		s.insert(`INSERT INTO resources (group_id, name, kind, description, max_concurrent_teams, created_at)
			VALUES ($1, 'Field 1', 'field', 'Main pitch', 2, $2)`, groupID, unix),
		s.insert(`INSERT INTO resources (group_id, name, kind, description, max_concurrent_teams, created_at)
			VALUES ($1, 'Field 2', 'field', 'Small pitch', 1, $2)`, groupID, unix),
	}

	season := Season(s.now.In(s.loc))
	seasonID := s.insert(`INSERT INTO seasons (group_id, name, starts_on, ends_on, created_at) VALUES ($1, $2, $3, $4, $5)`,
		groupID, season.Name, season.Starts, season.Ends, unix)
	partIDs := [2]int64{
		s.insert(`INSERT INTO season_parts (group_id, season_id, position, name, starts_on, ends_on, alternating_weeks, created_at)
			VALUES ($1, $2, 1, $3, $4, $5, true, $6)`, groupID, seasonID, season.Part1Name, season.Starts, season.Part1Ends, unix),
		s.insert(`INSERT INTO season_parts (group_id, season_id, position, name, starts_on, ends_on, alternating_weeks, created_at)
			VALUES ($1, $2, 2, $3, $4, $5, false, $6)`, groupID, seasonID, season.Part2Name, season.Part2From, season.Ends, unix),
	}
	for p, partID := range partIDs {
		for _, slot := range Slots(p + 1) {
			s.exec(`INSERT INTO plan_slots (group_id, season_part_id, resource_id, week, weekday, start_minute, end_minute, min_age, max_age, max_teams, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				groupID, partID, resources[slot.Resource], slot.Week, slot.Weekday, slot.StartMin, slot.EndMin,
				nullInt(slot.MinAge), nullInt(slot.MaxAge), nullInt(slot.MaxTeams), unix)
		}
	}

	// Guardians come first: a parent's role row must exist, which it does
	// once the roles above are inserted.
	for t, team := range g.Teams {
		teamID := s.insert(`INSERT INTO teams (group_id, name, slug, birth_year_from, birth_year_to, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			groupID, team.Name, team.Slug, team.BirthYearFrom, team.BirthYearTo, unix)
		for i, player := range team.Players {
			s.exec(`INSERT INTO team_members (group_id, team_id, user_id, role, created_at) VALUES ($1, $2, $3, 'player', $4)`,
				groupID, teamID, s.users[player], unix)
			parent := team.Parents[i]
			// Parents are on the roster only through guardians (derived at read time).
			s.exec(`INSERT INTO guardians (group_id, parent_user_id, child_user_id, created_by, created_at) VALUES ($1, $2, $3, $4, $5)`,
				groupID, s.users[parent], s.users[player], s.users[g.Admin()], unix)
		}
		for c, coach := range team.Coaches {
			s.exec(`INSERT INTO season_team_coaches (group_id, season_id, team_id, coach_user_id, is_head, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
				groupID, seasonID, teamID, s.users[coach], coach == team.Head, unix)
			s.exec(`INSERT INTO coach_qualifications (group_id, coach_user_id, min_age, max_age, updated_at) VALUES ($1, $2, $3, $4, $5)`,
				groupID, s.users[coach], team.CoachMinAge, team.CoachMaxAge, unix)
			for p, partID := range partIDs {
				for _, w := range Availability(c, p+1) {
					s.exec(`INSERT INTO coach_availability (group_id, season_part_id, coach_user_id, week, weekday, start_minute, end_minute, created_at)
						VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, groupID, partID, s.users[coach], w.Week, w.Weekday, w.StartMin, w.EndMin, unix)
				}
			}
		}
		s.events(groupID, teamID, t, resources, team)
	}
}

func (s *seeder) events(groupID, teamID int64, teamIndex int, resources []int64, team TeamPlan) {
	unix := s.now.Unix()
	today := s.now.In(s.loc)
	start := 16 * 60
	if teamIndex == 1 {
		start = 17*60 + 30
	}
	for i, spec := range EventSpecs() {
		day := time.Date(today.Year(), today.Month(), today.Day()+spec.DayOffset, 0, 0, 0, 0, s.loc)
		starts := day.Add(time.Duration(start) * time.Minute)
		ends := starts.Add(90 * time.Minute)
		past := spec.DayOffset < 0

		title := team.Name + " training"
		var resource any = resources[teamIndex%len(resources)]
		var location any
		var deadline any
		mode := "open"
		if spec.Kind == "match" {
			title = team.Name + " match"
			resource, location = nil, "City Stadium"
		}
		if spec.Nominated {
			mode = "nominated"
		}
		if !past && spec.Kind == "training" {
			deadline = starts.Add(-2 * time.Hour)
		}
		eventID := s.insert(`
			INSERT INTO events (group_id, kind, title, starts_at, ends_at, resource_id, location, signup_mode, signup_deadline, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			groupID, spec.Kind, title, starts, ends, resource, location, mode, deadline, unix)
		s.exec(`INSERT INTO event_teams (group_id, event_id, team_id) VALUES ($1, $2, $3)`, groupID, eventID, teamID)
		for _, coach := range team.Coaches {
			s.exec(`INSERT INTO event_coaches (group_id, event_id, coach_user_id) VALUES ($1, $2, $3)`, groupID, eventID, s.users[coach])
		}
		for p, player := range team.Players {
			status, attended := Answer(spec, i, p)
			if status == "" && attended == nil {
				continue
			}
			var statusArg any
			if status != "" {
				statusArg = status
			}
			s.exec(`INSERT INTO event_participants (group_id, event_id, user_id, status, attended, updated_by, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, groupID, eventID, s.users[player], statusArg, attended, s.users[team.Head], unix)
		}
	}
}

// Answer returns the demo sign-up status and confirmed attendance of the
// p-th player (0-based) for the i-th event of a team. An empty status with a
// nil attendance means the player has no row.
func Answer(spec EventSpec, eventIndex, player int) (status string, attended *bool) {
	if spec.Nominated {
		if player < 3 {
			return "nominated", nil
		}
		return "", nil
	}
	declined := (player+eventIndex)%5 == 4
	if declined {
		return "declined", nil
	}
	if (player+eventIndex)%7 == 6 && spec.DayOffset > 0 {
		return "", nil // has not answered yet
	}
	if spec.DayOffset >= 0 {
		return "signed_up", nil
	}
	came := player != 3 || eventIndex%2 == 0
	return "signed_up", &came
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
