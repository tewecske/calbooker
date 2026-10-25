package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

// EventGenerationRepository reads and writes the events generated from a
// season part's training plan (#15). Every query filters by group.
type EventGenerationRepository struct {
	adapter *Adapter
}

// NewEventGenerationRepository constructs generated event storage over db.
func NewEventGenerationRepository(db *sql.DB) (*EventGenerationRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &EventGenerationRepository{adapter: adapter}, nil
}

var _ service.EventGenerationRepository = (*EventGenerationRepository)(nil)

// Generate reads the part's trainings and the events generated for the weeks
// and calls plan. With apply, the season part row is locked first (board
// saves and other generations of the part wait) and so are the generated
// events (participant writes lock their event row, so none slips in between
// the check and the delete); the planned deletes and creates then run in the
// same transaction.
func (r *EventGenerationRepository) Generate(ctx context.Context, groupID, partID int64, weeks []time.Time, apply bool, plan func(service.GenerationState) (service.GenerationPlan, error)) error {
	if err := ValidateContext(ctx); err != nil {
		return err
	}
	if r == nil || r.adapter == nil {
		return ErrNilDatabase
	}
	db, err := r.adapter.DB()
	if err != nil {
		return err
	}
	if groupID <= 0 || partID <= 0 {
		return service.ErrSeasonNotFound
	}
	if plan == nil {
		return ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapEventError(err)
	}
	defer func() { _ = tx.Rollback() }()
	lock, lockEvents := "", ""
	if apply {
		lock, lockEvents = " FOR UPDATE", " FOR UPDATE OF e"
	}
	var lockedID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM season_parts WHERE group_id = $1 AND id = $2`+lock, groupID, partID).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrSeasonNotFound
		}
		return mapEventError(err)
	}
	var state service.GenerationState
	if err := tx.QueryRowContext(ctx, `SELECT time_zone FROM groups WHERE id = $1`, groupID).Scan(&state.TimeZone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrGroupNotFound
		}
		return mapEventError(err)
	}
	if state.Trainings, err = listPlanTrainings(ctx, tx, groupID, partID); err != nil {
		return err
	}
	days := make([]string, 0, len(weeks))
	for _, week := range weeks {
		days = append(days, service.FormatSeasonDate(week))
	}
	events, err := listEvents(ctx, tx, `
		WHERE e.group_id = $1 AND e.generated_for_week::text = ANY($2::text[])
		ORDER BY e.starts_at, e.id`+lockEvents, groupID, days)
	if err != nil {
		return err
	}
	participating, err := eventsWithParticipants(ctx, tx, groupID, events)
	if err != nil {
		return err
	}
	for _, event := range events {
		state.Existing = append(state.Existing, service.GeneratedEvent{Event: event, HasParticipants: participating[event.ID]})
	}
	result, err := plan(state)
	if err != nil {
		return err
	}
	if !apply {
		return nil
	}
	for _, eventID := range result.Delete {
		if participating[eventID] {
			// The plan never deletes an event with participants.
			return service.ErrEventHasParticipants
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE group_id = $1 AND id = $2`, groupID, eventID); err != nil {
			return mapEventError(err)
		}
	}
	for _, event := range result.Create {
		if _, err := insertEvent(ctx, tx, event); err != nil {
			return err
		}
	}
	return mapEventError(tx.Commit())
}

// eventsWithParticipants returns the IDs of the events that have any
// participant row.
func eventsWithParticipants(ctx context.Context, db queryer, groupID int64, events []service.Event) (map[int64]bool, error) {
	found := make(map[int64]bool)
	if len(events) == 0 {
		return found, nil
	}
	ids := make([]int64, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT event_id FROM event_participants WHERE group_id = $1 AND event_id = ANY($2::bigint[])`, groupID, ids)
	if err != nil {
		return nil, mapEventError(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, mapEventError(err)
		}
		found[id] = true
	}
	return found, mapEventError(rows.Err())
}
