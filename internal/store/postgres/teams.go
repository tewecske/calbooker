package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// TeamRepository persists teams and rosters in PostgreSQL. Every query
// filters by group.
type TeamRepository struct {
	adapter *Adapter
}

// NewTeamRepository constructs team storage over db.
func NewTeamRepository(db *sql.DB) (*TeamRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &TeamRepository{adapter: adapter}, nil
}

var _ service.TeamRepository = (*TeamRepository)(nil)

const teamColumns = `id, group_id, name, birth_year_from, birth_year_to, description, archived_at, created_at, version`

// CreateTeam inserts one team.
func (r *TeamRepository) CreateTeam(ctx context.Context, team service.Team) (service.Team, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Team{}, err
	}
	return scanTeam(db.QueryRowContext(ctx, `
		INSERT INTO teams (group_id, name, birth_year_from, birth_year_to, description, archived_at, created_at, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0)
		RETURNING `+teamColumns,
		team.GroupID, team.Name, team.BirthYearFrom, team.BirthYearTo, team.Description, team.ArchivedAt, team.CreatedAt))
}

// FindTeam returns one team of the group.
func (r *TeamRepository) FindTeam(ctx context.Context, groupID, teamID int64) (service.Team, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Team{}, err
	}
	if groupID <= 0 || teamID <= 0 {
		return service.Team{}, service.ErrTeamNotFound
	}
	return scanTeam(db.QueryRowContext(ctx, `SELECT `+teamColumns+` FROM teams WHERE group_id = $1 AND id = $2`, groupID, teamID))
}

// ListTeams returns the group's teams with roster counts: active teams first,
// then by birth years and name.
func (r *TeamRepository) ListTeams(ctx context.Context, groupID int64) ([]service.TeamSummary, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT t.id, t.group_id, t.name, t.birth_year_from, t.birth_year_to, t.description, t.archived_at, t.created_at, t.version,
			count(tm.user_id) FILTER (WHERE tm.role = 'player'),
			count(tm.user_id) FILTER (WHERE tm.role = 'parent')
		FROM teams t
		LEFT JOIN team_members tm ON tm.team_id = t.id AND tm.group_id = t.group_id
		WHERE t.group_id = $1
		GROUP BY t.id
		ORDER BY (t.archived_at IS NOT NULL), t.birth_year_from DESC, t.birth_year_to DESC, lower(t.name), t.id
	`, groupID)
	if err != nil {
		return nil, mapTeamError(err)
	}
	defer func() { _ = rows.Close() }()
	teams := make([]service.TeamSummary, 0)
	for rows.Next() {
		var (
			summary              service.TeamSummary
			description          sql.NullString
			archivedAt           sql.NullInt64
			players, parentCount int64
		)
		if err := rows.Scan(&summary.Team.ID, &summary.Team.GroupID, &summary.Team.Name, &summary.Team.BirthYearFrom, &summary.Team.BirthYearTo,
			&description, &archivedAt, &summary.Team.CreatedAt, &summary.Team.Version, &players, &parentCount); err != nil {
			return nil, mapTeamError(err)
		}
		summary.Team.Description = nullableString(description)
		summary.Team.ArchivedAt = nullableInt64(archivedAt)
		summary.Players = int(players)
		summary.Parents = int(parentCount)
		teams = append(teams, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, mapTeamError(err)
	}
	return teams, nil
}

// UpdateTeam writes the editable fields when expectedVersion is current.
func (r *TeamRepository) UpdateTeam(ctx context.Context, team service.Team, expectedVersion int64) (service.Team, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Team{}, err
	}
	updated, err := scanTeam(db.QueryRowContext(ctx, `
		UPDATE teams
		SET name = $1, birth_year_from = $2, birth_year_to = $3, description = $4, archived_at = $5, version = version + 1
		WHERE group_id = $6 AND id = $7 AND version = $8
		RETURNING `+teamColumns,
		team.Name, team.BirthYearFrom, team.BirthYearTo, team.Description, team.ArchivedAt, team.GroupID, team.ID, expectedVersion))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, service.ErrTeamNotFound) {
		return service.Team{}, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE group_id = $1 AND id = $2)`, team.GroupID, team.ID).Scan(&exists); err != nil {
		return service.Team{}, mapTeamError(err)
	}
	return service.Team{}, service.ClassifyStaleWrite(!exists)
}

// ListRoster returns the team's players and parents with display data.
func (r *TeamRepository) ListRoster(ctx context.Context, groupID, teamID int64) ([]service.TeamRosterEntry, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT tm.user_id, tm.role, u.kind, u.username, u.display_name, u.email
		FROM team_members tm
		JOIN users u ON u.id = tm.user_id
		WHERE tm.group_id = $1 AND tm.team_id = $2
		ORDER BY (tm.role = 'player') DESC, lower(u.display_name), lower(u.username), tm.user_id
	`, groupID, teamID)
	if err != nil {
		return nil, mapTeamError(err)
	}
	defer func() { _ = rows.Close() }()
	roster := make([]service.TeamRosterEntry, 0)
	for rows.Next() {
		var (
			entry                    service.TeamRosterEntry
			kind                     string
			username, display, email sql.NullString
		)
		if err := rows.Scan(&entry.UserID, &entry.Role, &kind, &username, &display, &email); err != nil {
			return nil, mapTeamError(err)
		}
		entry.Kind = service.UserKind(kind)
		entry.Username = nullableString(username)
		entry.DisplayName = nullableString(display)
		entry.Email = nullableString(email)
		roster = append(roster, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, mapTeamError(err)
	}
	return roster, nil
}

// AddRosterEntry puts a member on the team. The foreign key to the member's
// group roles refuses members without the matching role.
func (r *TeamRepository) AddRosterEntry(ctx context.Context, groupID, teamID, userID int64, role string, createdAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO team_members (group_id, team_id, user_id, role, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING
	`, groupID, teamID, userID, role, createdAt)
	return mapTeamError(err)
}

// RemoveRosterEntry takes a member off the team.
func (r *TeamRepository) RemoveRosterEntry(ctx context.Context, groupID, teamID, userID int64, role string) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
		DELETE FROM team_members WHERE group_id = $1 AND team_id = $2 AND user_id = $3 AND role = $4
	`, groupID, teamID, userID, role)
	if err != nil {
		return mapTeamError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapTeamError(err)
	}
	if rows == 0 {
		return service.ErrRosterEntryNotFound
	}
	return nil
}

func (r *TeamRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanTeam(row rowScanner) (service.Team, error) {
	var (
		team        service.Team
		description sql.NullString
		archivedAt  sql.NullInt64
	)
	if err := row.Scan(&team.ID, &team.GroupID, &team.Name, &team.BirthYearFrom, &team.BirthYearTo, &description, &archivedAt, &team.CreatedAt, &team.Version); err != nil {
		return service.Team{}, mapTeamError(err)
	}
	if team.ID <= 0 || team.GroupID <= 0 || team.Version < 0 || team.Name == "" {
		return service.Team{}, ErrInvalidData
	}
	team.Description = nullableString(description)
	team.ArchivedAt = nullableInt64(archivedAt)
	return team, nil
}

func mapTeamError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrTeamNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		switch pgErr.ConstraintName {
		case "team_members_role_fk":
			return service.ErrRosterRoleMissing
		case "team_members_team_fk":
			return service.ErrTeamNotFound
		}
	}
	return ClassifyError(err)
}
