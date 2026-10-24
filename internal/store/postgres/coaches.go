package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// CoachRepository persists coach qualifications and availability in
// PostgreSQL. Every query filters by group.
type CoachRepository struct {
	adapter *Adapter
}

// NewCoachRepository constructs coach storage over db.
func NewCoachRepository(db *sql.DB) (*CoachRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &CoachRepository{adapter: adapter}, nil
}

var _ service.CoachRepository = (*CoachRepository)(nil)

// ListQualifications returns the group's coach qualifications by coach.
func (r *CoachRepository) ListQualifications(ctx context.Context, groupID int64) ([]service.CoachQualification, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT group_id, coach_user_id, min_age, max_age
		FROM coach_qualifications
		WHERE group_id = $1
		ORDER BY coach_user_id
	`, groupID)
	if err != nil {
		return nil, mapCoachError(err)
	}
	defer func() { _ = rows.Close() }()
	qualifications := make([]service.CoachQualification, 0)
	for rows.Next() {
		var qualification service.CoachQualification
		if err := rows.Scan(&qualification.GroupID, &qualification.CoachUserID, &qualification.MinAge, &qualification.MaxAge); err != nil {
			return nil, mapCoachError(err)
		}
		qualifications = append(qualifications, qualification)
	}
	if err := rows.Err(); err != nil {
		return nil, mapCoachError(err)
	}
	return qualifications, nil
}

// SetQualification adds or replaces a coach's qualification.
func (r *CoachRepository) SetQualification(ctx context.Context, qualification service.CoachQualification, updatedAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO coach_qualifications (group_id, coach_user_id, min_age, max_age, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (group_id, coach_user_id) DO UPDATE
		SET min_age = EXCLUDED.min_age, max_age = EXCLUDED.max_age, updated_at = EXCLUDED.updated_at
	`, qualification.GroupID, qualification.CoachUserID, qualification.MinAge, qualification.MaxAge, updatedAt)
	return mapCoachError(err)
}

// ClearQualification removes a coach's qualification, if any.
func (r *CoachRepository) ClearQualification(ctx context.Context, groupID, coachUserID int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM coach_qualifications WHERE group_id = $1 AND coach_user_id = $2`, groupID, coachUserID)
	return mapCoachError(err)
}

// ListAvailability returns a part's windows of one coach, or of every coach
// when coachUserID is 0.
func (r *CoachRepository) ListAvailability(ctx context.Context, groupID, partID, coachUserID int64) ([]service.AvailabilityWindow, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	return listAvailability(ctx, db, groupID, partID, coachUserID, false)
}

// EditAvailability replaces the coach's windows of the part with edit's
// result. The coach's role row is locked for the transaction, so edits of
// one coach run one after another and never lose each other's changes.
func (r *CoachRepository) EditAvailability(ctx context.Context, groupID, partID, coachUserID, createdAt int64, edit func([]service.AvailabilityWindow) ([]service.AvailabilityWindow, error)) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if edit == nil {
		return ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapCoachError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	if err := tx.QueryRowContext(ctx, `
		SELECT user_id FROM group_member_roles
		WHERE group_id = $1 AND user_id = $2 AND role = 'coach'
		FOR NO KEY UPDATE
	`, groupID, coachUserID).Scan(&locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrCoachNotFound
		}
		return mapCoachError(err)
	}
	var partExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM season_parts WHERE group_id = $1 AND id = $2)`, groupID, partID).Scan(&partExists); err != nil {
		return mapCoachError(err)
	}
	if !partExists {
		return service.ErrSeasonNotFound
	}
	current, err := listAvailability(ctx, tx, groupID, partID, coachUserID, true)
	if err != nil {
		return err
	}
	changed, err := edit(current)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM coach_availability WHERE group_id = $1 AND season_part_id = $2 AND coach_user_id = $3`, groupID, partID, coachUserID); err != nil {
		return mapCoachError(err)
	}
	for _, window := range changed {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO coach_availability (group_id, season_part_id, coach_user_id, week, weekday, start_minute, end_minute, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, groupID, partID, coachUserID, window.Week, window.Weekday, window.StartMinute, window.EndMinute, createdAt); err != nil {
			return mapCoachError(err)
		}
	}
	return mapCoachError(tx.Commit())
}

func (r *CoachRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func listAvailability(ctx context.Context, db queryer, groupID, partID, coachUserID int64, forUpdate bool) ([]service.AvailabilityWindow, error) {
	query := `
		SELECT id, group_id, season_part_id, coach_user_id, week, weekday, start_minute, end_minute
		FROM coach_availability
		WHERE group_id = $1 AND season_part_id = $2 AND ($3::bigint = 0 OR coach_user_id = $3::bigint)
		ORDER BY coach_user_id, CASE week WHEN 'all' THEN 0 WHEN 'A' THEN 1 ELSE 2 END, weekday, start_minute, id`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	rows, err := db.QueryContext(ctx, query, groupID, partID, coachUserID)
	if err != nil {
		return nil, mapCoachError(err)
	}
	defer func() { _ = rows.Close() }()
	windows := make([]service.AvailabilityWindow, 0)
	for rows.Next() {
		var window service.AvailabilityWindow
		if err := rows.Scan(&window.ID, &window.GroupID, &window.SeasonPartID, &window.CoachUserID, &window.Week, &window.Weekday, &window.StartMinute, &window.EndMinute); err != nil {
			return nil, mapCoachError(err)
		}
		windows = append(windows, window)
	}
	if err := rows.Err(); err != nil {
		return nil, mapCoachError(err)
	}
	return windows, nil
}

func mapCoachError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "coach_qualifications_coach_fk", "coach_availability_coach_fk":
			return service.ErrCoachNotFound
		case "coach_availability_part_fk":
			return service.ErrSeasonNotFound
		case "coach_qualifications_ages_valid":
			return service.ErrInvalidQualification
		case "coach_availability_week_valid":
			return service.ErrAvailabilityWeek
		case "coach_availability_weekday_valid", "coach_availability_minutes_valid":
			return service.ErrInvalidAvailability
		}
	}
	return ClassifyError(err)
}
