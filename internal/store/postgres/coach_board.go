package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tewecske/calbooker/internal/service"
)

var _ service.CoachBoardRepository = (*SeasonRepository)(nil)

// ReplaceSeasonCoaches replaces a season's coach assignments in one
// transaction. It locks the season row, so board saves, single assignment
// changes (AssignSeasonCoach, RemoveSeasonCoach), and season edits of the
// same season run one after another; edit sees the assignments as they are
// under the lock and decides (for example by revision) what to write.
func (r *SeasonRepository) ReplaceSeasonCoaches(ctx context.Context, groupID, seasonID, createdAt int64, edit func([]service.CoachAssignment) ([]service.CoachAssignment, error)) (service.CoachAssignmentChanges, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.CoachAssignmentChanges{}, err
	}
	if groupID <= 0 || seasonID <= 0 {
		return service.CoachAssignmentChanges{}, service.ErrSeasonNotFound
	}
	if edit == nil {
		return service.CoachAssignmentChanges{}, ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.CoachAssignmentChanges{}, mapSeasonError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockSeason(ctx, tx, groupID, seasonID, "FOR UPDATE"); err != nil {
		return service.CoachAssignmentChanges{}, err
	}
	current, err := listSeasonAssignments(ctx, tx, groupID, seasonID)
	if err != nil {
		return service.CoachAssignmentChanges{}, err
	}
	desired, err := edit(current)
	if err != nil {
		return service.CoachAssignmentChanges{}, err
	}
	diff := service.DiffCoachAssignments(current, desired)
	for _, assignment := range diff.Remove {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM season_team_coaches WHERE group_id = $1 AND season_id = $2 AND team_id = $3 AND coach_user_id = $4
		`, groupID, seasonID, assignment.TeamID, assignment.CoachUserID); err != nil {
			return service.CoachAssignmentChanges{}, mapSeasonError(err)
		}
	}
	for _, assignment := range diff.Unhead {
		if _, err := tx.ExecContext(ctx, `
			UPDATE season_team_coaches SET is_head = false
			WHERE group_id = $1 AND season_id = $2 AND team_id = $3 AND coach_user_id = $4
		`, groupID, seasonID, assignment.TeamID, assignment.CoachUserID); err != nil {
			return service.CoachAssignmentChanges{}, mapSeasonError(err)
		}
	}
	for _, assignment := range diff.Upsert {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO season_team_coaches (group_id, season_id, team_id, coach_user_id, is_head, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (season_id, team_id, coach_user_id) DO UPDATE SET is_head = EXCLUDED.is_head
		`, groupID, seasonID, assignment.TeamID, assignment.CoachUserID, assignment.Head, createdAt); err != nil {
			return service.CoachAssignmentChanges{}, mapSeasonError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return service.CoachAssignmentChanges{}, mapSeasonError(err)
	}
	return diff.Changes, nil
}

// lockSeason locks the season row with mode ("FOR UPDATE" or "FOR NO KEY
// UPDATE"); ErrSeasonNotFound when the group has no such season.
func lockSeason(ctx context.Context, db queryer, groupID, seasonID int64, mode string) error {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM seasons WHERE group_id = $1 AND id = $2 `+mode, groupID, seasonID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrSeasonNotFound
	}
	return mapSeasonError(err)
}

func listSeasonAssignments(ctx context.Context, db queryer, groupID, seasonID int64) ([]service.CoachAssignment, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT team_id, coach_user_id, is_head FROM season_team_coaches
		WHERE group_id = $1 AND season_id = $2
		ORDER BY team_id, coach_user_id
	`, groupID, seasonID)
	if err != nil {
		return nil, mapSeasonError(err)
	}
	defer func() { _ = rows.Close() }()
	assignments := make([]service.CoachAssignment, 0)
	for rows.Next() {
		assignment := service.CoachAssignment{SeasonID: seasonID}
		if err := rows.Scan(&assignment.TeamID, &assignment.CoachUserID, &assignment.Head); err != nil {
			return nil, mapSeasonError(err)
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, mapSeasonError(err)
	}
	return assignments, nil
}
