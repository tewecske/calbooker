package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// SeasonRepository persists seasons, season parts, and season coaches in
// PostgreSQL. Every query filters by group.
type SeasonRepository struct {
	adapter *Adapter
}

// NewSeasonRepository constructs season storage over db.
func NewSeasonRepository(db *sql.DB) (*SeasonRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &SeasonRepository{adapter: adapter}, nil
}

var _ service.SeasonRepository = (*SeasonRepository)(nil)

const (
	seasonColumns     = `s.id, s.group_id, s.name, s.starts_on, s.ends_on, s.created_at, s.version`
	seasonPartColumns = `p.id, p.group_id, p.season_id, p.position, p.name, p.starts_on, p.ends_on, p.alternating_weeks`
)

// queryer is the part of *sql.DB and *sql.Tx the season queries use.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// GroupTimeZone returns the group's IANA time zone.
func (r *SeasonRepository) GroupTimeZone(ctx context.Context, groupID int64) (string, error) {
	db, err := r.database(ctx)
	if err != nil {
		return "", err
	}
	var zone string
	if err := db.QueryRowContext(ctx, `SELECT time_zone FROM groups WHERE id = $1`, groupID).Scan(&zone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", service.ErrGroupNotFound
		}
		return "", mapSeasonError(err)
	}
	return zone, nil
}

// CreateSeason inserts the season and its parts in one transaction.
func (r *SeasonRepository) CreateSeason(ctx context.Context, season service.Season) (service.Season, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Season{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var seasonID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO seasons (group_id, name, starts_on, ends_on, created_at, version)
		VALUES ($1, $2, $3::date, $4::date, $5, 0)
		RETURNING id
	`, season.GroupID, season.Name, service.FormatSeasonDate(season.StartsOn), service.FormatSeasonDate(season.EndsOn), season.CreatedAt).Scan(&seasonID); err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	if err := writeSeasonParts(ctx, tx, season.GroupID, seasonID, season.Parts, season.CreatedAt); err != nil {
		return service.Season{}, err
	}
	created, err := findSeason(ctx, tx, season.GroupID, seasonID)
	if err != nil {
		return service.Season{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	return created, nil
}

// FindSeason returns one season of the group with its parts.
func (r *SeasonRepository) FindSeason(ctx context.Context, groupID, seasonID int64) (service.Season, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Season{}, err
	}
	if groupID <= 0 || seasonID <= 0 {
		return service.Season{}, service.ErrSeasonNotFound
	}
	return findSeason(ctx, db, groupID, seasonID)
}

// FindSeasonByPart returns the season of the group holding partID.
func (r *SeasonRepository) FindSeasonByPart(ctx context.Context, groupID, partID int64) (service.Season, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Season{}, err
	}
	if groupID <= 0 || partID <= 0 {
		return service.Season{}, service.ErrSeasonNotFound
	}
	var seasonID int64
	if err := db.QueryRowContext(ctx, `SELECT season_id FROM season_parts WHERE group_id = $1 AND id = $2`, groupID, partID).Scan(&seasonID); err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	return findSeason(ctx, db, groupID, seasonID)
}

// ListSeasons returns the group's seasons with their parts, latest first.
func (r *SeasonRepository) ListSeasons(ctx context.Context, groupID int64) ([]service.Season, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	return listSeasons(ctx, db, `WHERE s.group_id = $1 ORDER BY s.starts_on DESC, s.id DESC`, groupID)
}

// SeasonOn returns the season containing day; seasons of a group never
// overlap, so there is at most one.
func (r *SeasonRepository) SeasonOn(ctx context.Context, groupID int64, day time.Time) (service.Season, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Season{}, err
	}
	seasons, err := listSeasons(ctx, db, `WHERE s.group_id = $1 AND s.starts_on <= $2::date AND s.ends_on >= $2::date ORDER BY s.starts_on`, groupID, service.FormatSeasonDate(day))
	if err != nil {
		return service.Season{}, err
	}
	if len(seasons) == 0 {
		return service.Season{}, service.ErrSeasonNotFound
	}
	return seasons[0], nil
}

// UpdateSeason writes the season at the expected revision and its parts by
// position; a part beyond the given ones is deleted.
func (r *SeasonRepository) UpdateSeason(ctx context.Context, season service.Season, expectedVersion int64) (service.Season, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Season{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE seasons SET name = $1, starts_on = $2::date, ends_on = $3::date, version = version + 1
		WHERE group_id = $4 AND id = $5 AND version = $6
	`, season.Name, service.FormatSeasonDate(season.StartsOn), service.FormatSeasonDate(season.EndsOn), season.GroupID, season.ID, expectedVersion)
	if err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return service.Season{}, mapSeasonError(err)
	} else if rows == 0 {
		return service.Season{}, classifySeasonWrite(ctx, tx, season.GroupID, season.ID)
	}
	if err := writeSeasonParts(ctx, tx, season.GroupID, season.ID, season.Parts, time.Now().Unix()); err != nil {
		return service.Season{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM season_parts WHERE group_id = $1 AND season_id = $2 AND position > $3`, season.GroupID, season.ID, len(season.Parts)); err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	updated, err := findSeason(ctx, tx, season.GroupID, season.ID)
	if err != nil {
		return service.Season{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.Season{}, mapSeasonError(err)
	}
	return updated, nil
}

// DeleteSeason removes the season at the expected revision; its parts and
// coach assignments cascade.
func (r *SeasonRepository) DeleteSeason(ctx context.Context, groupID, seasonID, expectedVersion int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapSeasonError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM seasons WHERE group_id = $1 AND id = $2 AND version = $3`, groupID, seasonID, expectedVersion)
	if err != nil {
		return mapSeasonError(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return mapSeasonError(err)
	} else if rows == 0 {
		return classifySeasonWrite(ctx, tx, groupID, seasonID)
	}
	return mapSeasonError(tx.Commit())
}

// ListSeasonCoaches returns the group's coach assignments matching filter,
// latest season first, then by team and with the head coach first.
func (r *SeasonRepository) ListSeasonCoaches(ctx context.Context, groupID int64, filter service.SeasonCoachFilter) ([]service.SeasonCoach, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.group_id, c.season_id, c.team_id, c.coach_user_id, c.is_head, s.name, t.name, u.username, u.display_name, u.email
		FROM season_team_coaches c
		JOIN seasons s ON s.group_id = c.group_id AND s.id = c.season_id
		JOIN teams t ON t.group_id = c.group_id AND t.id = c.team_id
		JOIN users u ON u.id = c.coach_user_id
		WHERE c.group_id = $1 AND ($2::bigint = 0 OR c.season_id = $2::bigint) AND ($3::bigint = 0 OR c.team_id = $3::bigint)
		ORDER BY s.starts_on DESC, s.id, lower(t.name), t.id, c.is_head DESC, lower(u.display_name), lower(u.username), c.coach_user_id
	`, groupID, filter.SeasonID, filter.TeamID)
	if err != nil {
		return nil, mapSeasonError(err)
	}
	defer func() { _ = rows.Close() }()
	coaches := make([]service.SeasonCoach, 0)
	for rows.Next() {
		var (
			coach                    service.SeasonCoach
			username, display, email sql.NullString
		)
		if err := rows.Scan(&coach.GroupID, &coach.SeasonID, &coach.TeamID, &coach.CoachUserID, &coach.IsHead, &coach.SeasonName, &coach.TeamName, &username, &display, &email); err != nil {
			return nil, mapSeasonError(err)
		}
		coach.Username = nullableString(username)
		coach.DisplayName = nullableString(display)
		coach.Email = nullableString(email)
		coaches = append(coaches, coach)
	}
	if err := rows.Err(); err != nil {
		return nil, mapSeasonError(err)
	}
	return coaches, nil
}

// AssignSeasonCoach adds the assignment or updates its head flag. Marking a
// head coach unmarks the team's previous head coach for the season.
func (r *SeasonRepository) AssignSeasonCoach(ctx context.Context, assignment service.CoachAssignment, groupID, createdAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapSeasonError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if assignment.Head {
		if _, err := tx.ExecContext(ctx, `
			UPDATE season_team_coaches SET is_head = false
			WHERE group_id = $1 AND season_id = $2 AND team_id = $3 AND coach_user_id <> $4 AND is_head
		`, groupID, assignment.SeasonID, assignment.TeamID, assignment.CoachUserID); err != nil {
			return mapSeasonError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO season_team_coaches (group_id, season_id, team_id, coach_user_id, is_head, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (season_id, team_id, coach_user_id) DO UPDATE SET is_head = EXCLUDED.is_head
	`, groupID, assignment.SeasonID, assignment.TeamID, assignment.CoachUserID, assignment.Head, createdAt); err != nil {
		return mapSeasonError(err)
	}
	return mapSeasonError(tx.Commit())
}

// RemoveSeasonCoach deletes one assignment.
func (r *SeasonRepository) RemoveSeasonCoach(ctx context.Context, groupID, seasonID, teamID, coachUserID int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
		DELETE FROM season_team_coaches WHERE group_id = $1 AND season_id = $2 AND team_id = $3 AND coach_user_id = $4
	`, groupID, seasonID, teamID, coachUserID)
	if err != nil {
		return mapSeasonError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapSeasonError(err)
	}
	if rows == 0 {
		return service.ErrSeasonCoachNotFound
	}
	return nil
}

// CoachedTeamIDs returns the teams coachUserID coaches in the season
// containing day.
func (r *SeasonRepository) CoachedTeamIDs(ctx context.Context, groupID, coachUserID int64, day time.Time) ([]int64, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT c.team_id
		FROM season_team_coaches c
		JOIN seasons s ON s.group_id = c.group_id AND s.id = c.season_id
		WHERE c.group_id = $1 AND c.coach_user_id = $2 AND s.starts_on <= $3::date AND s.ends_on >= $3::date
		ORDER BY c.team_id
	`, groupID, coachUserID, service.FormatSeasonDate(day))
	if err != nil {
		return nil, mapSeasonError(err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, mapSeasonError(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, mapSeasonError(err)
	}
	return ids, nil
}

func (r *SeasonRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

// writeSeasonParts inserts or updates parts by position.
func writeSeasonParts(ctx context.Context, db queryer, groupID, seasonID int64, parts []service.SeasonPart, createdAt int64) error {
	for _, part := range parts {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO season_parts (group_id, season_id, position, name, starts_on, ends_on, alternating_weeks, created_at)
			VALUES ($1, $2, $3, $4, $5::date, $6::date, $7, $8)
			ON CONFLICT (season_id, position) DO UPDATE
			SET name = EXCLUDED.name, starts_on = EXCLUDED.starts_on, ends_on = EXCLUDED.ends_on, alternating_weeks = EXCLUDED.alternating_weeks
		`, groupID, seasonID, part.Position, part.Name, service.FormatSeasonDate(part.StartsOn), service.FormatSeasonDate(part.EndsOn), part.AlternatingWeeks, createdAt); err != nil {
			return mapSeasonError(err)
		}
	}
	return nil
}

func findSeason(ctx context.Context, db queryer, groupID, seasonID int64) (service.Season, error) {
	seasons, err := listSeasons(ctx, db, `WHERE s.group_id = $1 AND s.id = $2`, groupID, seasonID)
	if err != nil {
		return service.Season{}, err
	}
	if len(seasons) == 0 {
		return service.Season{}, service.ErrSeasonNotFound
	}
	return seasons[0], nil
}

// listSeasons loads the seasons selected by where (which binds the group as
// $1) and attaches their parts.
func listSeasons(ctx context.Context, db queryer, where string, args ...any) ([]service.Season, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+seasonColumns+` FROM seasons s `+where, args...)
	if err != nil {
		return nil, mapSeasonError(err)
	}
	seasons := make([]service.Season, 0)
	index := make(map[int64]int)
	for rows.Next() {
		var season service.Season
		if err := rows.Scan(&season.ID, &season.GroupID, &season.Name, &season.StartsOn, &season.EndsOn, &season.CreatedAt, &season.Version); err != nil {
			_ = rows.Close()
			return nil, mapSeasonError(err)
		}
		if season.ID <= 0 || season.GroupID <= 0 || season.Version < 0 || season.Name == "" {
			_ = rows.Close()
			return nil, ErrInvalidData
		}
		season.StartsOn, season.EndsOn = service.SeasonDate(season.StartsOn), service.SeasonDate(season.EndsOn)
		index[season.ID] = len(seasons)
		seasons = append(seasons, season)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, mapSeasonError(err)
	}
	_ = rows.Close()
	if len(seasons) == 0 {
		return seasons, nil
	}
	partRows, err := db.QueryContext(ctx, `
		SELECT `+seasonPartColumns+`
		FROM season_parts p
		WHERE p.group_id = $1
		ORDER BY p.season_id, p.position
	`, seasons[0].GroupID)
	if err != nil {
		return nil, mapSeasonError(err)
	}
	defer func() { _ = partRows.Close() }()
	for partRows.Next() {
		var part service.SeasonPart
		if err := partRows.Scan(&part.ID, &part.GroupID, &part.SeasonID, &part.Position, &part.Name, &part.StartsOn, &part.EndsOn, &part.AlternatingWeeks); err != nil {
			return nil, mapSeasonError(err)
		}
		position, ok := index[part.SeasonID]
		if !ok {
			continue
		}
		part.StartsOn, part.EndsOn = service.SeasonDate(part.StartsOn), service.SeasonDate(part.EndsOn)
		seasons[position].Parts = append(seasons[position].Parts, part)
	}
	if err := partRows.Err(); err != nil {
		return nil, mapSeasonError(err)
	}
	return seasons, nil
}

func classifySeasonWrite(ctx context.Context, db queryer, groupID, seasonID int64) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM seasons WHERE group_id = $1 AND id = $2)`, groupID, seasonID).Scan(&exists); err != nil {
		return mapSeasonError(err)
	}
	return service.ClassifyStaleWrite(!exists)
}

func mapSeasonError(err error) error {
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
		return service.ErrSeasonNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "seasons_no_overlap":
			return service.ErrSeasonOverlap
		case "season_parts_no_overlap":
			return service.ErrSeasonPartsOverlap
		case "seasons_name_unique":
			return service.ErrSeasonNameTaken
		case "season_parts_season_fk", "season_team_coaches_season_fk":
			return service.ErrSeasonNotFound
		case "season_team_coaches_team_fk":
			return service.ErrTeamNotFound
		case "season_team_coaches_coach_fk":
			return service.ErrSeasonCoachRoleMissing
		case "season_team_coaches_one_head":
			// Two head coaches were marked at the same time.
			return service.ErrOptimisticLockConflict
		}
	}
	return ClassifyError(err)
}
