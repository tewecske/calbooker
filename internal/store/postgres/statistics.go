package postgres

import (
	"context"
	"database/sql"

	"github.com/tewecske/calbooker/internal/service"
)

// StatisticsRepository aggregates participation (#11) in PostgreSQL. Both
// queries count in the database; they read events that are not cancelled,
// start in [From, To), and ended by Until, and the answers and confirmed
// attendance of the players now on the events' teams.
type StatisticsRepository struct {
	adapter *Adapter
}

// NewStatisticsRepository constructs statistics storage over db.
func NewStatisticsRepository(db *sql.DB) (*StatisticsRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &StatisticsRepository{adapter: adapter}, nil
}

var _ service.StatisticsRepository = (*StatisticsRepository)(nil)

// TeamStatistics returns one row per team that is not archived (every team,
// or those in filter.TeamIDs), by name. Opportunities are held events times
// the team's current players; signed up counts the answers "signed up" and
// "nominated" (the places taken); attended counts confirmed attendance.
func (r *StatisticsRepository) TeamStatistics(ctx context.Context, filter service.StatisticsFilter) ([]service.TeamStatistics, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		WITH held AS (
			SELECT et.team_id, e.id AS event_id
			FROM events e
			JOIN event_teams et ON et.group_id = e.group_id AND et.event_id = e.id
			WHERE e.group_id = $1 AND e.cancelled_at IS NULL
				AND e.starts_at >= $2 AND e.starts_at < $3 AND e.ends_at <= $4
				AND ($5::boolean OR et.team_id = ANY($6::bigint[]))
		), cells AS (
			SELECT held.team_id, p.status, p.attended
			FROM held
			JOIN team_members tm ON tm.group_id = $1 AND tm.team_id = held.team_id AND tm.role = 'player'
			LEFT JOIN event_participants p ON p.group_id = $1 AND p.event_id = held.event_id AND p.user_id = tm.user_id
			WHERE coalesce(p.status, '') <> 'left_out' OR p.attended IS TRUE
		), totals AS (
			SELECT team_id, count(*) AS opportunities,
				count(*) FILTER (WHERE status IN ('signed_up', 'nominated')) AS signed_up,
				count(*) FILTER (WHERE attended) AS attended
			FROM cells GROUP BY team_id
		)
		SELECT t.id, t.name,
			(SELECT count(*) FROM team_members tm WHERE tm.group_id = t.group_id AND tm.team_id = t.id AND tm.role = 'player'),
			(SELECT count(*) FROM held WHERE held.team_id = t.id),
			coalesce(totals.opportunities, 0), coalesce(totals.signed_up, 0), coalesce(totals.attended, 0)
		FROM teams t
		LEFT JOIN totals ON totals.team_id = t.id
		WHERE t.group_id = $1 AND t.archived_at IS NULL AND ($5::boolean OR t.id = ANY($6::bigint[]))
		ORDER BY lower(t.name), t.id
	`, filter.GroupID, filter.From, filter.To, filter.Until, filter.AllTeams, idsOrEmpty(filter.TeamIDs))
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	teams := make([]service.TeamStatistics, 0)
	for rows.Next() {
		var team service.TeamStatistics
		if err := rows.Scan(&team.TeamID, &team.Name, &team.Players, &team.Events, &team.Opportunities, &team.SignedUp, &team.Attended); err != nil {
			return nil, mapParticipantError(err)
		}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		return nil, mapParticipantError(err)
	}
	return teams, nil
}

// PlayerStatistics returns one row per player in scope, by name: with
// filter.AllTeams every player on a team; otherwise the players of
// filter.TeamIDs and the players in filter.UserIDs. Events are the held
// events of the teams the player is on now (counted once even when two of
// the player's teams share an event, and not counted when a coach left the
// player out) plus the held events the player is called up for (#47; not
// counted when the player declined them), so a called-up player attending
// another team's event counts as attending it.
func (r *StatisticsRepository) PlayerStatistics(ctx context.Context, filter service.StatisticsFilter) ([]service.PlayerStatistics, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		WITH scope AS (
			SELECT tm.user_id
			FROM team_members tm
			WHERE tm.group_id = $1 AND tm.role = 'player'
				AND ($5::boolean OR tm.team_id = ANY($6::bigint[]) OR tm.user_id = ANY($7::bigint[]))
			UNION
			SELECT p.user_id
			FROM event_participants p
			WHERE p.group_id = $1 AND p.called_up AND ($5::boolean OR p.user_id = ANY($7::bigint[]))
		), cells AS (
			SELECT scope.user_id, e.id AS event_id, p.status, p.attended
			FROM scope
			JOIN team_members tm ON tm.group_id = $1 AND tm.user_id = scope.user_id AND tm.role = 'player'
			JOIN event_teams et ON et.group_id = $1 AND et.team_id = tm.team_id
			JOIN events e ON e.group_id = $1 AND e.id = et.event_id AND e.cancelled_at IS NULL
				AND e.starts_at >= $2 AND e.starts_at < $3 AND e.ends_at <= $4
			LEFT JOIN event_participants p ON p.group_id = $1 AND p.event_id = e.id AND p.user_id = scope.user_id
			WHERE coalesce(p.status, '') <> 'left_out' OR p.attended IS TRUE
			UNION
			SELECT scope.user_id, e.id, p.status, p.attended
			FROM scope
			JOIN event_participants p ON p.group_id = $1 AND p.user_id = scope.user_id AND p.called_up
			JOIN events e ON e.group_id = $1 AND e.id = p.event_id AND e.cancelled_at IS NULL
				AND e.starts_at >= $2 AND e.starts_at < $3 AND e.ends_at <= $4
			WHERE p.status <> 'declined' OR p.attended IS TRUE
		), totals AS (
			SELECT user_id, count(*) AS events,
				count(*) FILTER (WHERE status IN ('signed_up', 'nominated')) AS signed_up,
				count(*) FILTER (WHERE attended) AS attended
			FROM cells GROUP BY user_id
		)
		SELECT u.id, u.display_name, u.username, u.email,
			coalesce((SELECT string_agg(t.name, ', ' ORDER BY lower(t.name), t.id)
				FROM team_members tm JOIN teams t ON t.group_id = tm.group_id AND t.id = tm.team_id
				WHERE tm.group_id = $1 AND tm.user_id = u.id AND tm.role = 'player' AND t.archived_at IS NULL), ''),
			coalesce(totals.events, 0), coalesce(totals.signed_up, 0), coalesce(totals.attended, 0)
		FROM scope
		JOIN users u ON u.id = scope.user_id
		LEFT JOIN totals ON totals.user_id = scope.user_id
		ORDER BY lower(coalesce(u.display_name, u.username, u.email, '')), u.id
	`, filter.GroupID, filter.From, filter.To, filter.Until, filter.AllTeams, idsOrEmpty(filter.TeamIDs), idsOrEmpty(filter.UserIDs))
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	players := make([]service.PlayerStatistics, 0)
	for rows.Next() {
		var (
			player                   service.PlayerStatistics
			display, username, email sql.NullString
		)
		if err := rows.Scan(&player.UserID, &display, &username, &email, &player.Teams, &player.Events, &player.SignedUp, &player.Attended); err != nil {
			return nil, mapParticipantError(err)
		}
		player.DisplayName, player.Username, player.Email = nullableString(display), nullableString(username), nullableString(email)
		players = append(players, player)
	}
	if err := rows.Err(); err != nil {
		return nil, mapParticipantError(err)
	}
	return players, nil
}

func (r *StatisticsRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

// idsOrEmpty keeps a nil slice from becoming SQL NULL.
func idsOrEmpty(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}
