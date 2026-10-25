package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// EventRepository persists events, their teams, and their extra coaches in
// PostgreSQL. Every query filters by group.
type EventRepository struct {
	adapter *Adapter
}

// NewEventRepository constructs event storage over db.
func NewEventRepository(db *sql.DB) (*EventRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &EventRepository{adapter: adapter}, nil
}

var _ service.EventRepository = (*EventRepository)(nil)

const eventColumns = `e.id, e.group_id, e.kind, e.title, e.starts_at, e.ends_at, e.resource_id, r.name, e.location, e.signup_mode,
	e.max_participants, e.signup_deadline, e.cancelled_at, e.plan_training_id, e.generated_for_week, e.edited_at, e.created_at, e.version`

const eventFrom = `FROM events e LEFT JOIN resources r ON r.group_id = e.group_id AND r.id = e.resource_id`

// GroupTimeZone returns the group's IANA time zone.
func (r *EventRepository) GroupTimeZone(ctx context.Context, groupID int64) (string, error) {
	db, err := r.database(ctx)
	if err != nil {
		return "", err
	}
	var zone string
	if err := db.QueryRowContext(ctx, `SELECT time_zone FROM groups WHERE id = $1`, groupID).Scan(&zone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", service.ErrGroupNotFound
		}
		return "", mapEventError(err)
	}
	return zone, nil
}

// CreateEvent inserts the event with its teams and extra coaches in one
// transaction.
func (r *EventRepository) CreateEvent(ctx context.Context, event service.Event) (service.Event, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Event{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.Event{}, mapEventError(err)
	}
	defer func() { _ = tx.Rollback() }()
	eventID, err := insertEvent(ctx, tx, event)
	if err != nil {
		return service.Event{}, err
	}
	created, err := findEvent(ctx, tx, event.GroupID, eventID)
	if err != nil {
		return service.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.Event{}, mapEventError(err)
	}
	return created, nil
}

// FindEvent returns one event of the group with its teams and coaches.
func (r *EventRepository) FindEvent(ctx context.Context, groupID, eventID int64) (service.Event, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Event{}, err
	}
	if groupID <= 0 || eventID <= 0 {
		return service.Event{}, service.ErrEventNotFound
	}
	return findEvent(ctx, db, groupID, eventID)
}

// ListEvents returns the group's events matching query inside scope,
// ordered by start.
func (r *EventRepository) ListEvents(ctx context.Context, groupID int64, query service.EventQuery, scope service.EventScope) ([]service.Event, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	order := "ASC"
	if query.Descending {
		order = "DESC"
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	teamIDs := scope.TeamIDs
	if teamIDs == nil {
		teamIDs = []int64{}
	}
	return listEvents(ctx, db, `
		WHERE e.group_id = $1
			AND ($2::timestamptz IS NULL OR e.ends_at > $2::timestamptz)
			AND ($3::timestamptz IS NULL OR e.starts_at < $3::timestamptz)
			AND ($4::bigint = 0 OR EXISTS (SELECT 1 FROM event_teams et WHERE et.event_id = e.id AND et.team_id = $4::bigint))
			AND (NOT $5::boolean OR e.cancelled_at IS NULL)
			AND ($6::bigint = 0 OR e.id <> $6::bigint)
			AND ($7::boolean
				OR EXISTS (SELECT 1 FROM event_teams et WHERE et.event_id = e.id AND et.team_id = ANY($8::bigint[]))
				OR EXISTS (SELECT 1 FROM event_coaches ec WHERE ec.event_id = e.id AND ec.coach_user_id = $9::bigint))
		ORDER BY e.starts_at `+order+`, e.id `+order+`
		LIMIT $10
	`, groupID, nullableInstant(query.From), nullableInstant(query.To), query.TeamID, query.ActiveOnly, query.ExcludeID,
		scope.All, teamIDs, scope.UserID, limit)
}

// UpdateEvent writes the event at the expected revision and replaces its
// teams and extra coaches.
func (r *EventRepository) UpdateEvent(ctx context.Context, event service.Event, expectedVersion int64) (service.Event, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Event{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.Event{}, mapEventError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE events SET kind = $1, title = $2, starts_at = $3, ends_at = $4, resource_id = $5, location = $6, signup_mode = $7,
			max_participants = $8, signup_deadline = $9, cancelled_at = $10, edited_at = $11, version = version + 1
		WHERE group_id = $12 AND id = $13 AND version = $14
	`, event.Kind, event.Title, event.StartsAt, event.EndsAt, event.ResourceID, event.Location, event.SignupMode,
		nullableLimit(event.MaxParticipants), nullableTime(event.SignupDeadline), event.CancelledAt, event.EditedAt,
		event.GroupID, event.ID, expectedVersion)
	if err != nil {
		return service.Event{}, mapEventError(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return service.Event{}, mapEventError(err)
	} else if rows == 0 {
		return service.Event{}, classifyEventWrite(ctx, tx, event.GroupID, event.ID)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM event_teams WHERE group_id = $1 AND event_id = $2`, event.GroupID, event.ID); err != nil {
		return service.Event{}, mapEventError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM event_coaches WHERE group_id = $1 AND event_id = $2`, event.GroupID, event.ID); err != nil {
		return service.Event{}, mapEventError(err)
	}
	if err := writeEventMembers(ctx, tx, event.GroupID, event.ID, event); err != nil {
		return service.Event{}, err
	}
	updated, err := findEvent(ctx, tx, event.GroupID, event.ID)
	if err != nil {
		return service.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.Event{}, mapEventError(err)
	}
	return updated, nil
}

// DeleteEvent removes the event at the expected revision; its teams and
// coaches cascade. An event with participant rows is refused
// (service.ErrEventHasParticipants).
func (r *EventRepository) DeleteEvent(ctx context.Context, groupID, eventID, expectedVersion int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapEventError(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Participant writes lock the event row too, so no sign-up slips in
	// between the check and the delete.
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM events WHERE group_id = $1 AND id = $2 FOR UPDATE`, groupID, eventID); err != nil {
		return mapEventError(err)
	}
	if has, err := eventHasParticipants(ctx, tx, groupID, eventID); err != nil {
		return err
	} else if has {
		return service.ErrEventHasParticipants
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM events WHERE group_id = $1 AND id = $2 AND version = $3`, groupID, eventID, expectedVersion)
	if err != nil {
		return mapEventError(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return mapEventError(err)
	} else if rows == 0 {
		return classifyEventWrite(ctx, tx, groupID, eventID)
	}
	return mapEventError(tx.Commit())
}

// TeamCoachesOn returns the season coaches of the group's teams in the
// season containing day, head coaches first.
func (r *EventRepository) TeamCoachesOn(ctx context.Context, groupID int64, day time.Time) ([]service.EventCoach, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.team_id, c.coach_user_id, u.display_name, u.username
		FROM season_team_coaches c
		JOIN seasons s ON s.group_id = c.group_id AND s.id = c.season_id
		JOIN users u ON u.id = c.coach_user_id
		WHERE c.group_id = $1 AND s.starts_on <= $2::date AND s.ends_on >= $2::date
		ORDER BY c.team_id, c.is_head DESC, lower(coalesce(u.display_name, u.username, '')), c.coach_user_id
	`, groupID, service.FormatSeasonDate(day))
	if err != nil {
		return nil, mapEventError(err)
	}
	defer func() { _ = rows.Close() }()
	coaches := make([]service.EventCoach, 0)
	for rows.Next() {
		var (
			coach             service.EventCoach
			display, username sql.NullString
		)
		if err := rows.Scan(&coach.TeamID, &coach.UserID, &display, &username); err != nil {
			return nil, mapEventError(err)
		}
		coach.DisplayName, coach.Username = nullableString(display), nullableString(username)
		coaches = append(coaches, coach)
	}
	if err := rows.Err(); err != nil {
		return nil, mapEventError(err)
	}
	return coaches, nil
}

func (r *EventRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

// writeEventMembers inserts the event's teams and extra coaches.
func writeEventMembers(ctx context.Context, db queryer, groupID, eventID int64, event service.Event) error {
	for _, team := range event.Teams {
		if _, err := db.ExecContext(ctx, `INSERT INTO event_teams (group_id, event_id, team_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, groupID, eventID, team.ID); err != nil {
			return mapEventError(err)
		}
	}
	for _, coach := range event.Coaches {
		if _, err := db.ExecContext(ctx, `INSERT INTO event_coaches (group_id, event_id, coach_user_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, groupID, eventID, coach.UserID); err != nil {
			return mapEventError(err)
		}
	}
	return nil
}

func findEvent(ctx context.Context, db queryer, groupID, eventID int64) (service.Event, error) {
	events, err := listEvents(ctx, db, `WHERE e.group_id = $1 AND e.id = $2`, groupID, eventID)
	if err != nil {
		return service.Event{}, err
	}
	if len(events) == 0 {
		return service.Event{}, service.ErrEventNotFound
	}
	return events[0], nil
}

// listEvents loads the events selected by where (which binds the group as
// $1) and attaches their teams and extra coaches.
func listEvents(ctx context.Context, db queryer, where string, args ...any) ([]service.Event, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+eventColumns+` `+eventFrom+` `+where, args...)
	if err != nil {
		return nil, mapEventError(err)
	}
	events := make([]service.Event, 0)
	index := make(map[int64]int)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		index[event.ID] = len(events)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, mapEventError(err)
	}
	_ = rows.Close()
	if len(events) == 0 {
		return events, nil
	}
	ids := make([]int64, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	groupID := events[0].GroupID
	teamRows, err := db.QueryContext(ctx, `
		SELECT et.event_id, t.id, t.name
		FROM event_teams et
		JOIN teams t ON t.group_id = et.group_id AND t.id = et.team_id
		WHERE et.group_id = $1 AND et.event_id = ANY($2::bigint[])
		ORDER BY lower(t.name), t.id
	`, groupID, ids)
	if err != nil {
		return nil, mapEventError(err)
	}
	for teamRows.Next() {
		var (
			eventID int64
			team    service.EventTeam
		)
		if err := teamRows.Scan(&eventID, &team.ID, &team.Name); err != nil {
			_ = teamRows.Close()
			return nil, mapEventError(err)
		}
		if position, ok := index[eventID]; ok {
			events[position].Teams = append(events[position].Teams, team)
		}
	}
	if err := teamRows.Err(); err != nil {
		_ = teamRows.Close()
		return nil, mapEventError(err)
	}
	_ = teamRows.Close()
	coachRows, err := db.QueryContext(ctx, `
		SELECT ec.event_id, u.id, u.display_name, u.username
		FROM event_coaches ec
		JOIN users u ON u.id = ec.coach_user_id
		WHERE ec.group_id = $1 AND ec.event_id = ANY($2::bigint[])
		ORDER BY lower(coalesce(u.display_name, u.username, '')), u.id
	`, groupID, ids)
	if err != nil {
		return nil, mapEventError(err)
	}
	defer func() { _ = coachRows.Close() }()
	for coachRows.Next() {
		var (
			eventID           int64
			coach             service.EventCoach
			display, username sql.NullString
		)
		if err := coachRows.Scan(&eventID, &coach.UserID, &display, &username); err != nil {
			return nil, mapEventError(err)
		}
		coach.DisplayName, coach.Username = nullableString(display), nullableString(username)
		if position, ok := index[eventID]; ok {
			events[position].Coaches = append(events[position].Coaches, coach)
		}
	}
	if err := coachRows.Err(); err != nil {
		return nil, mapEventError(err)
	}
	return events, nil
}

func scanEvent(row rowScanner) (service.Event, error) {
	var (
		event                                     service.Event
		resourceID, cancelledAt, planID, editedAt sql.NullInt64
		resourceName, location                    sql.NullString
		maxParticipants                           sql.NullInt32
		deadline, generatedForWeek                sql.NullTime
	)
	if err := row.Scan(&event.ID, &event.GroupID, &event.Kind, &event.Title, &event.StartsAt, &event.EndsAt, &resourceID, &resourceName, &location,
		&event.SignupMode, &maxParticipants, &deadline, &cancelledAt, &planID, &generatedForWeek, &editedAt, &event.CreatedAt, &event.Version); err != nil {
		return service.Event{}, mapEventError(err)
	}
	if event.ID <= 0 || event.GroupID <= 0 || event.Version < 0 || event.Title == "" || !event.StartsAt.Before(event.EndsAt) {
		return service.Event{}, ErrInvalidData
	}
	event.ResourceID = nullableInt64(resourceID)
	event.ResourceName = nullableString(resourceName)
	event.Location = nullableString(location)
	if maxParticipants.Valid {
		limit := int(maxParticipants.Int32)
		event.MaxParticipants = &limit
	}
	if deadline.Valid {
		value := deadline.Time
		event.SignupDeadline = &value
	}
	if generatedForWeek.Valid {
		week := service.SeasonDate(generatedForWeek.Time)
		event.GeneratedForWeek = &week
	}
	event.CancelledAt = nullableInt64(cancelledAt)
	event.PlanTrainingID = nullableInt64(planID)
	event.EditedAt = nullableInt64(editedAt)
	return event, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInstant(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableDate(value *time.Time) any {
	if value == nil {
		return nil
	}
	return service.FormatSeasonDate(*value)
}

func classifyEventWrite(ctx context.Context, db queryer, groupID, eventID int64) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE group_id = $1 AND id = $2)`, groupID, eventID).Scan(&exists); err != nil {
		return mapEventError(err)
	}
	return service.ClassifyStaleWrite(!exists)
}

func mapEventError(err error) error {
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
		case "events_group_id_fk":
			return service.ErrGroupNotFound
		case "events_resource_fk":
			return service.ErrResourceNotFound
		case "event_teams_team_fk":
			return service.ErrTeamNotFound
		case "event_coaches_coach_fk":
			return service.ErrEventCoachRoleMissing
		case "event_teams_event_fk", "event_coaches_event_fk":
			return service.ErrEventNotFound
		case "events_kind_allowed":
			return service.ErrInvalidEventKind
		case "events_title_nonempty":
			return service.ErrInvalidEventTitle
		case "events_times_ordered":
			return service.ErrInvalidEventEnd
		case "events_place_exclusive":
			return service.ErrEventPlaceRequired
		case "events_location_nonempty":
			return service.ErrInvalidEventLocation
		case "events_signup_mode_allowed":
			return service.ErrInvalidEventSignupMode
		case "events_max_participants_valid":
			return service.ErrInvalidEventMaxParticipants
		case "events_signup_deadline_valid":
			return service.ErrInvalidEventSignupDeadline
		}
	}
	return ClassifyError(err)
}

// insertEvent inserts the event with its teams and extra coaches and returns
// its ID.
func insertEvent(ctx context.Context, tx queryer, event service.Event) (int64, error) {
	var eventID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO events (group_id, kind, title, starts_at, ends_at, resource_id, location, signup_mode, max_participants,
			signup_deadline, cancelled_at, plan_training_id, generated_for_week, edited_at, created_at, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::date, $14, $15, 0)
		RETURNING id
	`, event.GroupID, event.Kind, event.Title, event.StartsAt, event.EndsAt, event.ResourceID, event.Location, event.SignupMode,
		nullableLimit(event.MaxParticipants), nullableTime(event.SignupDeadline), event.CancelledAt, event.PlanTrainingID,
		nullableDate(event.GeneratedForWeek), event.EditedAt, event.CreatedAt).Scan(&eventID); err != nil {
		return 0, mapEventError(err)
	}
	if err := writeEventMembers(ctx, tx, event.GroupID, eventID, event); err != nil {
		return 0, err
	}
	return eventID, nil
}
