package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// ParticipantRepository persists event participants (sign-up answers and
// confirmed attendance) in PostgreSQL. Every query filters by group, and
// every write locks the event row first, so concurrent sign-ups cannot pass
// the participant limit.
type ParticipantRepository struct {
	adapter *Adapter
}

// NewParticipantRepository constructs participant storage over db.
func NewParticipantRepository(db *sql.DB) (*ParticipantRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &ParticipantRepository{adapter: adapter}, nil
}

var _ service.ParticipantRepository = (*ParticipantRepository)(nil)

// ListEventPlayers returns the group's members with the player role plus
// every member with a row for the event, with the teams they play on, by
// name.
func (r *ParticipantRepository) ListEventPlayers(ctx context.Context, groupID, eventID int64) ([]service.EventPlayer, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT gm.user_id, u.kind, u.display_name, u.username, u.email,
			EXISTS (SELECT 1 FROM group_member_roles gr WHERE gr.group_id = gm.group_id AND gr.user_id = gm.user_id AND gr.role = 'player'),
			(SELECT string_agg(tm.team_id::text, ',' ORDER BY tm.team_id) FROM team_members tm
				WHERE tm.group_id = gm.group_id AND tm.user_id = gm.user_id AND tm.role = 'player')
		FROM group_members gm
		JOIN users u ON u.id = gm.user_id
		WHERE gm.group_id = $1
			AND (EXISTS (SELECT 1 FROM group_member_roles gr WHERE gr.group_id = gm.group_id AND gr.user_id = gm.user_id AND gr.role = 'player')
				OR EXISTS (SELECT 1 FROM event_participants p WHERE p.group_id = gm.group_id AND p.event_id = $2 AND p.user_id = gm.user_id))
		ORDER BY lower(coalesce(u.display_name, u.username, '')), gm.user_id
	`, groupID, eventID)
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	players := make([]service.EventPlayer, 0)
	for rows.Next() {
		var (
			player                   service.EventPlayer
			kind                     string
			display, username, email sql.NullString
			teams                    sql.NullString
		)
		if err := rows.Scan(&player.UserID, &kind, &display, &username, &email, &player.IsPlayer, &teams); err != nil {
			return nil, mapParticipantError(err)
		}
		player.Kind = service.UserKind(kind)
		player.DisplayName, player.Username, player.Email = nullableString(display), nullableString(username), nullableString(email)
		if player.TeamIDs, err = splitIDs(teams); err != nil {
			return nil, err
		}
		players = append(players, player)
	}
	if err := rows.Err(); err != nil {
		return nil, mapParticipantError(err)
	}
	return players, nil
}

// ListParticipants returns the event's rows ordered by member.
func (r *ParticipantRepository) ListParticipants(ctx context.Context, groupID, eventID int64) ([]service.Participant, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT event_id, user_id, status, called_up, attended, updated_by, updated_at
		FROM event_participants
		WHERE group_id = $1 AND event_id = $2
		ORDER BY user_id
	`, groupID, eventID)
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	participants := make([]service.Participant, 0)
	for rows.Next() {
		participant, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	if err := rows.Err(); err != nil {
		return nil, mapParticipantError(err)
	}
	return participants, nil
}

// ChangeParticipant locks the event row, reads the member's row and the
// places taken, and writes what decide returns in one transaction.
func (r *ParticipantRepository) ChangeParticipant(ctx context.Context, groupID, eventID, userID int64, decide service.ParticipantDecision) (*service.Participant, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if decide == nil {
		return nil, ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = tx.Rollback() }()
	event, err := lockParticipationEvent(ctx, tx, groupID, eventID)
	if err != nil {
		return nil, err
	}
	state := service.ParticipationState{Event: event}
	row := tx.QueryRowContext(ctx, `
		SELECT event_id, user_id, status, called_up, attended, updated_by, updated_at
		FROM event_participants WHERE group_id = $1 AND event_id = $2 AND user_id = $3
	`, groupID, eventID, userID)
	switch current, err := scanParticipant(row); {
	case err == nil:
		state.Current = &current
	case errors.Is(err, service.ErrEventNotFound):
		// No row yet (sql.ErrNoRows).
	default:
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM event_participants
		WHERE group_id = $1 AND event_id = $2 AND status IN ('signed_up', 'nominated')
	`, groupID, eventID).Scan(&state.Taken); err != nil {
		return nil, mapParticipantError(err)
	}
	next, err := decide(state)
	if err != nil {
		return nil, err
	}
	switch {
	case next == nil:
		if _, err := tx.ExecContext(ctx, `DELETE FROM event_participants WHERE group_id = $1 AND event_id = $2 AND user_id = $3`, groupID, eventID, userID); err != nil {
			return nil, mapParticipantError(err)
		}
	case next == state.Current:
		// Nothing changed.
	default:
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_participants (group_id, event_id, user_id, status, called_up, attended, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (event_id, user_id) DO UPDATE SET status = EXCLUDED.status, called_up = EXCLUDED.called_up, attended = EXCLUDED.attended,
				updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
		`, groupID, eventID, userID, nullableStatus(next.Status), next.CalledUp, nullableBool(next.Attended), next.UpdatedBy, next.UpdatedAt); err != nil {
			return nil, mapParticipantError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, mapParticipantError(err)
	}
	if next == nil {
		return nil, nil
	}
	written := *next
	written.EventID, written.UserID = eventID, userID
	return &written, nil
}

// RecordAttendance locks the event row, runs check, and stores the marks in
// one transaction.
func (r *ParticipantRepository) RecordAttendance(ctx context.Context, groupID, eventID int64, marks []service.AttendanceMark, by, at int64, check func(service.ParticipationEvent) error) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapParticipantError(err)
	}
	defer func() { _ = tx.Rollback() }()
	event, err := lockParticipationEvent(ctx, tx, groupID, eventID)
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(event); err != nil {
			return err
		}
	}
	for _, mark := range marks {
		if mark.Attended {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO event_participants (group_id, event_id, user_id, status, attended, updated_by, updated_at)
				VALUES ($1, $2, $3, NULL, true, $4, $5)
				ON CONFLICT (event_id, user_id) DO UPDATE SET attended = true, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
				WHERE event_participants.attended IS DISTINCT FROM true
			`, groupID, eventID, mark.UserID, by, at)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE event_participants SET attended = false, updated_by = $4, updated_at = $5
				WHERE group_id = $1 AND event_id = $2 AND user_id = $3 AND attended IS DISTINCT FROM false
			`, groupID, eventID, mark.UserID, by, at)
		}
		if err != nil {
			return mapParticipantError(err)
		}
	}
	// A walk-in marked absent has nothing left to keep.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM event_participants WHERE group_id = $1 AND event_id = $2 AND status IS NULL AND attended = false
	`, groupID, eventID); err != nil {
		return mapParticipantError(err)
	}
	return mapParticipantError(tx.Commit())
}

// ListConfirmedAttendance returns attended rows of the group's events that
// are not cancelled and start in [from, to), earliest first.
func (r *ParticipantRepository) ListConfirmedAttendance(ctx context.Context, groupID int64, from, to time.Time) ([]service.AttendanceRecord, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.event_id, p.user_id, e.starts_at,
			(SELECT string_agg(et.team_id::text, ',' ORDER BY et.team_id) FROM event_teams et WHERE et.group_id = e.group_id AND et.event_id = e.id)
		FROM event_participants p
		JOIN events e ON e.group_id = p.group_id AND e.id = p.event_id
		WHERE p.group_id = $1 AND p.attended AND e.cancelled_at IS NULL AND e.starts_at >= $2 AND e.starts_at < $3
		ORDER BY e.starts_at, p.event_id, p.user_id
	`, groupID, from, to)
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	records := make([]service.AttendanceRecord, 0)
	for rows.Next() {
		var (
			record service.AttendanceRecord
			teams  sql.NullString
		)
		if err := rows.Scan(&record.EventID, &record.UserID, &record.StartsAt, &teams); err != nil {
			return nil, mapParticipantError(err)
		}
		if record.TeamIDs, err = splitIDs(teams); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, mapParticipantError(err)
	}
	return records, nil
}

// ListAnswers returns the answers of userIDs for the group's events that
// overlap [from, to), with the members' names, ordered by event and member.
func (r *ParticipantRepository) ListAnswers(ctx context.Context, groupID int64, userIDs []int64, from, to time.Time) ([]service.CalendarAnswer, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return []service.CalendarAnswer{}, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.event_id, p.user_id, p.status, u.display_name, u.username
		FROM event_participants p
		JOIN events e ON e.group_id = p.group_id AND e.id = p.event_id
		JOIN users u ON u.id = p.user_id
		WHERE p.group_id = $1 AND p.user_id = ANY($2::bigint[]) AND p.status IS NOT NULL
			AND e.ends_at > $3 AND e.starts_at < $4
		ORDER BY p.event_id, p.user_id
	`, groupID, userIDs, from, to)
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	answers := make([]service.CalendarAnswer, 0)
	for rows.Next() {
		var (
			answer            service.CalendarAnswer
			display, username sql.NullString
		)
		if err := rows.Scan(&answer.EventID, &answer.UserID, &answer.Status, &display, &username); err != nil {
			return nil, mapParticipantError(err)
		}
		answer.DisplayName, answer.Username = nullableString(display), nullableString(username)
		answers = append(answers, answer)
	}
	if err := rows.Err(); err != nil {
		return nil, mapParticipantError(err)
	}
	return answers, nil
}

// ListOverlaps returns the user's other events that overlap [from, to): the
// ones the user is signed up or called up for (signed up or nominated) and
// the ones of the user's teams the user has not declined or been left out
// of. Cancelled events and excludeEventID are skipped.
func (r *ParticipantRepository) ListOverlaps(ctx context.Context, groupID, userID int64, from, to time.Time, excludeEventID int64) ([]service.OverlapEvent, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT e.id, e.title, e.starts_at, e.ends_at
		FROM events e
		LEFT JOIN event_participants p ON p.group_id = e.group_id AND p.event_id = e.id AND p.user_id = $2
		WHERE e.group_id = $1 AND e.id <> $5 AND e.cancelled_at IS NULL AND e.starts_at < $4 AND e.ends_at > $3
			AND (p.status IN ('signed_up', 'nominated')
				OR (p.status IS NULL AND EXISTS (
					SELECT 1 FROM event_teams et
					JOIN team_members tm ON tm.group_id = et.group_id AND tm.team_id = et.team_id AND tm.user_id = $2 AND tm.role = 'player'
					WHERE et.group_id = e.group_id AND et.event_id = e.id)))
		ORDER BY e.starts_at, e.id
	`, groupID, userID, from, to, excludeEventID)
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	overlaps := make([]service.OverlapEvent, 0)
	for rows.Next() {
		var overlap service.OverlapEvent
		if err := rows.Scan(&overlap.EventID, &overlap.Title, &overlap.StartsAt, &overlap.EndsAt); err != nil {
			return nil, mapParticipantError(err)
		}
		overlaps = append(overlaps, overlap)
	}
	if err := rows.Err(); err != nil {
		return nil, mapParticipantError(err)
	}
	return overlaps, nil
}

// CalledUpEventIDs returns the events of the group the users are called up
// for, in ascending order.
func (r *ParticipantRepository) CalledUpEventIDs(ctx context.Context, groupID int64, userIDs []int64) ([]int64, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT event_id FROM event_participants
		WHERE group_id = $1 AND called_up AND user_id = ANY($2::bigint[])
		ORDER BY event_id
	`, groupID, idsOrEmpty(userIDs))
	if err != nil {
		return nil, mapParticipantError(err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, mapParticipantError(err)
		}
		ids = append(ids, id)
	}
	return ids, mapParticipantError(rows.Err())
}

func (r *ParticipantRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

// lockParticipationEvent locks the event row for the rest of the
// transaction and returns what the participation rules read.
func lockParticipationEvent(ctx context.Context, tx *sql.Tx, groupID, eventID int64) (service.ParticipationEvent, error) {
	var (
		event       service.ParticipationEvent
		limit       sql.NullInt32
		deadline    sql.NullTime
		cancelledAt sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT starts_at, ends_at, signup_mode, max_participants, signup_deadline, cancelled_at
		FROM events WHERE group_id = $1 AND id = $2
		FOR UPDATE
	`, groupID, eventID).Scan(&event.StartsAt, &event.EndsAt, &event.SignupMode, &limit, &deadline, &cancelledAt); err != nil {
		return service.ParticipationEvent{}, mapParticipantError(err)
	}
	if limit.Valid {
		value := int(limit.Int32)
		event.MaxParticipants = &value
	}
	if deadline.Valid {
		value := deadline.Time
		event.SignupDeadline = &value
	}
	event.Cancelled = cancelledAt.Valid
	return event, nil
}

// eventHasParticipants reports whether the event has any participant row.
func eventHasParticipants(ctx context.Context, db queryer, groupID, eventID int64) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM event_participants WHERE group_id = $1 AND event_id = $2)`, groupID, eventID).Scan(&exists); err != nil {
		return false, mapParticipantError(err)
	}
	return exists, nil
}

func scanParticipant(row rowScanner) (service.Participant, error) {
	var (
		participant service.Participant
		status      sql.NullString
		attended    sql.NullBool
		updatedBy   sql.NullInt64
	)
	if err := row.Scan(&participant.EventID, &participant.UserID, &status, &participant.CalledUp, &attended, &updatedBy, &participant.UpdatedAt); err != nil {
		return service.Participant{}, mapParticipantError(err)
	}
	if status.Valid {
		participant.Status = status.String
	}
	if attended.Valid {
		value := attended.Bool
		participant.Attended = &value
	}
	participant.UpdatedBy = nullableInt64(updatedBy)
	return participant, nil
}

func nullableStatus(status string) any {
	if status == "" {
		return nil
	}
	return status
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func mapParticipantError(err error) error {
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
		return service.ErrEventNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "event_participants_event_fk":
			return service.ErrEventNotFound
		case "event_participants_member_fk":
			return service.ErrMembershipNotFound
		case "event_participants_status_allowed", "event_participants_answer_or_attendance", "event_participants_called_up_answer":
			return service.ErrInvalidParticipantStatus
		}
	}
	return ClassifyError(err)
}
