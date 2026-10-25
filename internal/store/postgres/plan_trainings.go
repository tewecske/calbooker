package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// PlanTrainingRepository persists the trainings of weekly plans in
// PostgreSQL. Every query filters by group.
type PlanTrainingRepository struct {
	adapter *Adapter
}

// NewPlanTrainingRepository constructs training plan storage over db.
func NewPlanTrainingRepository(db *sql.DB) (*PlanTrainingRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &PlanTrainingRepository{adapter: adapter}, nil
}

var _ service.TrainingPlanRepository = (*PlanTrainingRepository)(nil)

const planTrainingColumns = `id, group_id, season_part_id, team_id, resource_id, COALESCE(plan_slot_id, 0), week, weekday, start_minute, end_minute, created_at`

// ListPlanTrainings returns a part's trainings by weekday, start, team, and
// ID.
func (r *PlanTrainingRepository) ListPlanTrainings(ctx context.Context, groupID, partID int64) ([]service.PlanTraining, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	return listPlanTrainings(ctx, db, groupID, partID)
}

// ReplacePlanTrainings locks the part row, so board saves of the same part
// (and slot copies, which lock it too) run one after another; edit sees the
// trainings as they are under the lock and decides (for example by revision)
// what to write. Removed trainings are deleted first, then changed ones are
// updated in place (so the events generated from them keep their link), then
// new ones are inserted; the unique key is checked at commit.
func (r *PlanTrainingRepository) ReplacePlanTrainings(ctx context.Context, groupID, partID, createdAt int64, edit func([]service.PlanTraining) ([]service.PlanTraining, error)) (service.TrainingChanges, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.TrainingChanges{}, err
	}
	if groupID <= 0 || partID <= 0 {
		return service.TrainingChanges{}, service.ErrSeasonNotFound
	}
	if edit == nil {
		return service.TrainingChanges{}, ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.TrainingChanges{}, mapPlanTrainingError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM season_parts WHERE group_id = $1 AND id = $2 FOR UPDATE`, groupID, partID).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.TrainingChanges{}, service.ErrSeasonNotFound
		}
		return service.TrainingChanges{}, mapPlanTrainingError(err)
	}
	current, err := listPlanTrainings(ctx, tx, groupID, partID)
	if err != nil {
		return service.TrainingChanges{}, err
	}
	desired, err := edit(current)
	if err != nil {
		return service.TrainingChanges{}, err
	}
	diff := service.DiffPlanTrainings(current, desired)
	for _, training := range diff.Delete {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plan_trainings WHERE group_id = $1 AND season_part_id = $2 AND id = $3`, groupID, partID, training.ID); err != nil {
			return service.TrainingChanges{}, mapPlanTrainingError(err)
		}
	}
	for _, training := range diff.Update {
		if _, err := tx.ExecContext(ctx, `
			UPDATE plan_trainings
			SET team_id = $1, resource_id = $2, plan_slot_id = $3, week = $4, weekday = $5, start_minute = $6, end_minute = $7
			WHERE group_id = $8 AND season_part_id = $9 AND id = $10
		`, training.TeamID, training.ResourceID, nullableID(training.PlanSlotID), training.Week, training.Weekday, training.StartMinute, training.EndMinute,
			groupID, partID, training.ID); err != nil {
			return service.TrainingChanges{}, mapPlanTrainingError(err)
		}
	}
	for _, training := range diff.Insert {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO plan_trainings (group_id, season_part_id, team_id, resource_id, plan_slot_id, week, weekday, start_minute, end_minute, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, groupID, partID, training.TeamID, training.ResourceID, nullableID(training.PlanSlotID), training.Week, training.Weekday,
			training.StartMinute, training.EndMinute, createdAt); err != nil {
			return service.TrainingChanges{}, mapPlanTrainingError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return service.TrainingChanges{}, mapPlanTrainingError(err)
	}
	return diff.Changes, nil
}

func (r *PlanTrainingRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

// nullableID stores 0 as NULL.
func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func listPlanTrainings(ctx context.Context, db queryer, groupID, partID int64) ([]service.PlanTraining, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+planTrainingColumns+`
		FROM plan_trainings
		WHERE group_id = $1 AND season_part_id = $2
		ORDER BY weekday, start_minute, team_id, id
	`, groupID, partID)
	if err != nil {
		return nil, mapPlanTrainingError(err)
	}
	defer func() { _ = rows.Close() }()
	trainings := make([]service.PlanTraining, 0)
	for rows.Next() {
		var training service.PlanTraining
		if err := rows.Scan(&training.ID, &training.GroupID, &training.SeasonPartID, &training.TeamID, &training.ResourceID, &training.PlanSlotID,
			&training.Week, &training.Weekday, &training.StartMinute, &training.EndMinute, &training.CreatedAt); err != nil {
			return nil, mapPlanTrainingError(err)
		}
		if training.ID <= 0 || training.GroupID <= 0 || training.SeasonPartID <= 0 || training.TeamID <= 0 || training.ResourceID <= 0 ||
			(training.Week != service.SeasonWeekA && training.Week != service.SeasonWeekB && training.Week != service.SeasonWeekAll) {
			return nil, ErrInvalidData
		}
		trainings = append(trainings, training)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPlanTrainingError(err)
	}
	return trainings, nil
}

func mapPlanTrainingError(err error) error {
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
		return service.ErrRecordNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "plan_trainings_part_fk":
			return service.ErrSeasonNotFound
		case "plan_trainings_team_fk":
			return service.ErrTeamNotFound
		case "plan_trainings_resource_fk":
			return service.ErrResourceNotFound
		case "plan_trainings_slot_fk":
			// The slot was deleted after the board read it: the plan changed.
			return service.ErrOptimisticLockConflict
		}
	}
	return ClassifyError(err)
}
