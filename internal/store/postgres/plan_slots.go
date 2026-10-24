package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// PlanSlotRepository persists the resource slots of weekly plans in
// PostgreSQL. Every query filters by group.
type PlanSlotRepository struct {
	adapter *Adapter
}

// NewPlanSlotRepository constructs plan slot storage over db.
func NewPlanSlotRepository(db *sql.DB) (*PlanSlotRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &PlanSlotRepository{adapter: adapter}, nil
}

var _ service.PlanSlotRepository = (*PlanSlotRepository)(nil)

const planSlotColumns = `id, group_id, season_part_id, resource_id, week, weekday, start_minute, end_minute, min_age, max_age, max_teams, created_at, version`

// ListPlanSlots returns a part's slots by weekday, start, resource, and ID.
func (r *PlanSlotRepository) ListPlanSlots(ctx context.Context, groupID, partID int64) ([]service.PlanSlot, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	return listPlanSlots(ctx, db, groupID, partID)
}

// FindPlanSlot returns one slot of the group.
func (r *PlanSlotRepository) FindPlanSlot(ctx context.Context, groupID, slotID int64) (service.PlanSlot, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.PlanSlot{}, err
	}
	if groupID <= 0 || slotID <= 0 {
		return service.PlanSlot{}, service.ErrPlanSlotNotFound
	}
	return scanPlanSlot(db.QueryRowContext(ctx, `SELECT `+planSlotColumns+` FROM plan_slots WHERE group_id = $1 AND id = $2`, groupID, slotID))
}

// CreatePlanSlot inserts one slot.
func (r *PlanSlotRepository) CreatePlanSlot(ctx context.Context, slot service.PlanSlot) (service.PlanSlot, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.PlanSlot{}, err
	}
	return insertPlanSlot(ctx, db, slot)
}

// UpdatePlanSlot writes the slot when expectedVersion is current.
func (r *PlanSlotRepository) UpdatePlanSlot(ctx context.Context, slot service.PlanSlot, expectedVersion int64) (service.PlanSlot, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.PlanSlot{}, err
	}
	updated, err := scanPlanSlot(db.QueryRowContext(ctx, `
		UPDATE plan_slots
		SET resource_id = $1, week = $2, weekday = $3, start_minute = $4, end_minute = $5,
		    min_age = $6, max_age = $7, max_teams = $8, version = version + 1
		WHERE group_id = $9 AND id = $10 AND version = $11
		RETURNING `+planSlotColumns,
		slot.ResourceID, slot.Week, slot.Weekday, slot.StartMinute, slot.EndMinute,
		nullableLimit(slot.MinAge), nullableLimit(slot.MaxAge), nullableLimit(slot.MaxTeams),
		slot.GroupID, slot.ID, expectedVersion))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, service.ErrPlanSlotNotFound) {
		return service.PlanSlot{}, err
	}
	return service.PlanSlot{}, classifyPlanSlotWrite(ctx, db, slot.GroupID, slot.ID)
}

// DeletePlanSlot removes the slot when expectedVersion is current.
func (r *PlanSlotRepository) DeletePlanSlot(ctx context.Context, groupID, slotID, expectedVersion int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM plan_slots WHERE group_id = $1 AND id = $2 AND version = $3`, groupID, slotID, expectedVersion)
	if err != nil {
		return mapPlanSlotError(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return mapPlanSlotError(err)
	} else if rows == 0 {
		return classifyPlanSlotWrite(ctx, db, groupID, slotID)
	}
	return nil
}

// ReplacePlanSlots removes the part's slots and inserts slots in one
// transaction. The part row is locked first, so two copies into the same
// part run one after the other.
func (r *PlanSlotRepository) ReplacePlanSlots(ctx context.Context, groupID, partID int64, slots []service.PlanSlot) (int, error) {
	db, err := r.database(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, mapPlanSlotError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM season_parts WHERE group_id = $1 AND id = $2 FOR UPDATE`, groupID, partID).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, service.ErrSeasonNotFound
		}
		return 0, mapPlanSlotError(err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM plan_slots WHERE group_id = $1 AND season_part_id = $2`, groupID, partID)
	if err != nil {
		return 0, mapPlanSlotError(err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, mapPlanSlotError(err)
	}
	for _, slot := range slots {
		slot.GroupID, slot.SeasonPartID = groupID, partID
		if _, err := insertPlanSlot(ctx, tx, slot); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, mapPlanSlotError(err)
	}
	return int(removed), nil
}

func (r *PlanSlotRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func insertPlanSlot(ctx context.Context, db queryer, slot service.PlanSlot) (service.PlanSlot, error) {
	return scanPlanSlot(db.QueryRowContext(ctx, `
		INSERT INTO plan_slots (group_id, season_part_id, resource_id, week, weekday, start_minute, end_minute, min_age, max_age, max_teams, created_at, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0)
		RETURNING `+planSlotColumns,
		slot.GroupID, slot.SeasonPartID, slot.ResourceID, slot.Week, slot.Weekday, slot.StartMinute, slot.EndMinute,
		nullableLimit(slot.MinAge), nullableLimit(slot.MaxAge), nullableLimit(slot.MaxTeams), slot.CreatedAt))
}

func listPlanSlots(ctx context.Context, db queryer, groupID, partID int64) ([]service.PlanSlot, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+planSlotColumns+`
		FROM plan_slots
		WHERE group_id = $1 AND season_part_id = $2
		ORDER BY weekday, start_minute, resource_id, id
	`, groupID, partID)
	if err != nil {
		return nil, mapPlanSlotError(err)
	}
	defer func() { _ = rows.Close() }()
	slots := make([]service.PlanSlot, 0)
	for rows.Next() {
		slot, err := scanPlanSlot(rows)
		if err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPlanSlotError(err)
	}
	return slots, nil
}

func scanPlanSlot(row rowScanner) (service.PlanSlot, error) {
	var (
		slot                    service.PlanSlot
		minAge, maxAge, maxTeam sql.NullInt64
	)
	if err := row.Scan(&slot.ID, &slot.GroupID, &slot.SeasonPartID, &slot.ResourceID, &slot.Week, &slot.Weekday,
		&slot.StartMinute, &slot.EndMinute, &minAge, &maxAge, &maxTeam, &slot.CreatedAt, &slot.Version); err != nil {
		return service.PlanSlot{}, mapPlanSlotError(err)
	}
	if slot.ID <= 0 || slot.GroupID <= 0 || slot.SeasonPartID <= 0 || slot.ResourceID <= 0 || slot.Version < 0 ||
		(slot.Week != service.SeasonWeekA && slot.Week != service.SeasonWeekB && slot.Week != service.SeasonWeekAll) {
		return service.PlanSlot{}, ErrInvalidData
	}
	slot.MinAge = nullableInt(minAge)
	slot.MaxAge = nullableInt(maxAge)
	slot.MaxTeams = nullableInt(maxTeam)
	return slot, nil
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

func classifyPlanSlotWrite(ctx context.Context, db queryer, groupID, slotID int64) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM plan_slots WHERE group_id = $1 AND id = $2)`, groupID, slotID).Scan(&exists); err != nil {
		return mapPlanSlotError(err)
	}
	return service.ClassifyStaleWrite(!exists)
}

func mapPlanSlotError(err error) error {
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
		return service.ErrPlanSlotNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "plan_slots_part_fk":
			return service.ErrSeasonNotFound
		case "plan_slots_resource_fk":
			return service.ErrResourceNotFound
		}
	}
	return ClassifyError(err)
}
