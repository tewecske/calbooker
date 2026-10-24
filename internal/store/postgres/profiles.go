package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// ProfileRepository persists child profiles and guardian links in
// PostgreSQL. It is also the GroupAccess source of a parent's children and
// their teams. Every query filters by group.
type ProfileRepository struct {
	adapter *Adapter
}

// NewProfileRepository constructs profile storage over db.
func NewProfileRepository(db *sql.DB) (*ProfileRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &ProfileRepository{adapter: adapter}, nil
}

var (
	_ service.ProfileRepository   = (*ProfileRepository)(nil)
	_ service.ChildRelationSource = (*ProfileRepository)(nil)
)

const profileSelect = `
	SELECT cp.user_id, cp.group_id, COALESCE(u.display_name, ''), cp.birth_year, cp.created_by, cp.created_at, cp.version
	FROM child_profiles cp
	JOIN users u ON u.id = cp.user_id AND u.kind = 'profile'`

// CreateProfile inserts the profile user, its child_profiles row, and its
// membership with the player role; then the optional guardian link and team
// roster entry. Everything commits together.
func (r *ProfileRepository) CreateProfile(ctx context.Context, profile service.ChildProfile, guardianUserID, teamID int64) (service.ChildProfile, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.ChildProfile{}, err
	}
	if profile.GroupID <= 0 || profile.DisplayName == "" {
		return service.ChildProfile{}, ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroupMembers(ctx, tx, profile.GroupID); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	var userID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO users (display_name, kind, created_at) VALUES ($1, 'profile', $2) RETURNING id
	`, profile.DisplayName, profile.CreatedAt).Scan(&userID); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO child_profiles (user_id, group_id, birth_year, created_by, created_at) VALUES ($1, $2, $3, $4, $5)
	`, userID, profile.GroupID, profile.BirthYear, profile.CreatedBy, profile.CreatedAt); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	if _, err := insertMembership(ctx, tx, service.GroupMembership{GroupID: profile.GroupID, UserID: userID, Roles: []string{service.GroupRolePlayer}, CreatedAt: profile.CreatedAt}); err != nil {
		return service.ChildProfile{}, err
	}
	if guardianUserID > 0 {
		if err := insertGuardian(ctx, tx, profile.GroupID, guardianUserID, userID, profile.CreatedBy, profile.CreatedAt); err != nil {
			return service.ChildProfile{}, err
		}
	}
	if teamID > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_members (group_id, team_id, user_id, role, created_at) VALUES ($1, $2, $3, 'player', $4)
		`, profile.GroupID, teamID, userID, profile.CreatedAt); err != nil {
			return service.ChildProfile{}, mapTeamError(err)
		}
	}
	created, err := scanProfile(tx.QueryRowContext(ctx, profileSelect+` WHERE cp.group_id = $1 AND cp.user_id = $2`, profile.GroupID, userID))
	if err != nil {
		return service.ChildProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	return created, nil
}

// FindProfile returns one profile of the group.
func (r *ProfileRepository) FindProfile(ctx context.Context, groupID, userID int64) (service.ChildProfile, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.ChildProfile{}, err
	}
	if groupID <= 0 || userID <= 0 {
		return service.ChildProfile{}, service.ErrProfileNotFound
	}
	return scanProfile(db.QueryRowContext(ctx, profileSelect+` WHERE cp.group_id = $1 AND cp.user_id = $2`, groupID, userID))
}

// UpdateProfile writes the name and birth year when expectedVersion is
// current.
func (r *ProfileRepository) UpdateProfile(ctx context.Context, profile service.ChildProfile, expectedVersion int64) (service.ChildProfile, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.ChildProfile{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE child_profiles SET birth_year = $1, version = version + 1
		WHERE group_id = $2 AND user_id = $3 AND version = $4
	`, profile.BirthYear, profile.GroupID, profile.UserID, expectedVersion)
	if err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	} else if rows == 0 {
		return service.ChildProfile{}, classifyProfileWrite(ctx, tx, profile.GroupID, profile.UserID)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET display_name = $1, version = version + 1 WHERE id = $2 AND kind = 'profile'
	`, profile.DisplayName, profile.UserID); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	updated, err := scanProfile(tx.QueryRowContext(ctx, profileSelect+` WHERE cp.group_id = $1 AND cp.user_id = $2`, profile.GroupID, profile.UserID))
	if err != nil {
		return service.ChildProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	return updated, nil
}

// DeleteProfile removes the profile's users row at the expected revision;
// its membership, roles, roster entries, and guardian links cascade.
func (r *ProfileRepository) DeleteProfile(ctx context.Context, groupID, userID, expectedVersion int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapProfileError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM users u
		WHERE u.id = $2 AND u.kind = 'profile'
			AND EXISTS (SELECT 1 FROM child_profiles cp WHERE cp.user_id = u.id AND cp.group_id = $1 AND cp.version = $3)
	`, groupID, userID, expectedVersion)
	if err != nil {
		return mapProfileError(err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return mapProfileError(err)
	} else if rows == 0 {
		return classifyProfileWrite(ctx, tx, groupID, userID)
	}
	return mapProfileError(tx.Commit())
}

// ListChildren returns the group's profiles and every member with a
// guardian, with guardians and player teams, ordered by name.
func (r *ProfileRepository) ListChildren(ctx context.Context, groupID int64) ([]service.ChildEntry, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT u.id, u.kind, u.username, u.display_name,
			cp.user_id IS NOT NULL, cp.birth_year, cp.created_by, cp.created_at, cp.version
		FROM group_members gm
		JOIN users u ON u.id = gm.user_id
		LEFT JOIN child_profiles cp ON cp.user_id = gm.user_id AND cp.group_id = gm.group_id AND u.kind = 'profile'
		WHERE gm.group_id = $1
			AND (cp.user_id IS NOT NULL OR EXISTS (SELECT 1 FROM guardians g WHERE g.group_id = gm.group_id AND g.child_user_id = gm.user_id))
		ORDER BY lower(u.display_name), lower(u.username), u.id
	`, groupID)
	if err != nil {
		return nil, mapProfileError(err)
	}
	children := make([]service.ChildEntry, 0)
	index := make(map[int64]int)
	for rows.Next() {
		var (
			child              service.ChildEntry
			kind               string
			username, display  sql.NullString
			isProfile          bool
			birthYear          sql.NullInt64
			createdBy          sql.NullInt64
			createdAt, version sql.NullInt64
		)
		if err := rows.Scan(&child.Person.UserID, &kind, &username, &display, &isProfile, &birthYear, &createdBy, &createdAt, &version); err != nil {
			_ = rows.Close()
			return nil, mapProfileError(err)
		}
		child.Person.Kind = service.UserKind(kind)
		child.Person.Username = nullableString(username)
		child.Person.DisplayName = nullableString(display)
		if isProfile {
			child.Profile = &service.ChildProfile{
				UserID: child.Person.UserID, GroupID: groupID, BirthYear: int(birthYear.Int64),
				CreatedBy: nullableInt64(createdBy), CreatedAt: createdAt.Int64, Version: version.Int64,
			}
			if display.Valid {
				child.Profile.DisplayName = display.String
			}
		}
		index[child.Person.UserID] = len(children)
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, mapProfileError(err)
	}
	_ = rows.Close()

	if err := r.attachGuardians(ctx, db, groupID, children, index); err != nil {
		return nil, err
	}
	if err := r.attachTeams(ctx, db, groupID, children, index); err != nil {
		return nil, err
	}
	return children, nil
}

func (r *ProfileRepository) attachGuardians(ctx context.Context, db *sql.DB, groupID int64, children []service.ChildEntry, index map[int64]int) error {
	rows, err := db.QueryContext(ctx, `
		SELECT g.child_user_id, u.id, u.kind, u.username, u.display_name
		FROM guardians g
		JOIN users u ON u.id = g.parent_user_id
		WHERE g.group_id = $1
		ORDER BY lower(u.display_name), lower(u.username), u.id
	`, groupID)
	if err != nil {
		return mapProfileError(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			childID           int64
			parent            service.PersonRef
			kind              string
			username, display sql.NullString
		)
		if err := rows.Scan(&childID, &parent.UserID, &kind, &username, &display); err != nil {
			return mapProfileError(err)
		}
		parent.Kind = service.UserKind(kind)
		parent.Username = nullableString(username)
		parent.DisplayName = nullableString(display)
		if position, ok := index[childID]; ok {
			children[position].Guardians = append(children[position].Guardians, parent)
		}
	}
	return mapProfileError(rows.Err())
}

func (r *ProfileRepository) attachTeams(ctx context.Context, db *sql.DB, groupID int64, children []service.ChildEntry, index map[int64]int) error {
	rows, err := db.QueryContext(ctx, `
		SELECT tm.user_id, t.id, t.name
		FROM team_members tm
		JOIN teams t ON t.group_id = tm.group_id AND t.id = tm.team_id
		WHERE tm.group_id = $1 AND tm.role = 'player'
		ORDER BY t.birth_year_from DESC, lower(t.name), t.id
	`, groupID)
	if err != nil {
		return mapProfileError(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			userID int64
			team   service.TeamRef
		)
		if err := rows.Scan(&userID, &team.ID, &team.Name); err != nil {
			return mapProfileError(err)
		}
		if position, ok := index[userID]; ok {
			children[position].Teams = append(children[position].Teams, team)
		}
	}
	return mapProfileError(rows.Err())
}

// AddGuardian links parent to child; linking twice is a no-op.
func (r *ProfileRepository) AddGuardian(ctx context.Context, groupID, parentUserID, childUserID int64, createdBy *int64, createdAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapProfileError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertGuardian(ctx, tx, groupID, parentUserID, childUserID, createdBy, createdAt); err != nil {
		return err
	}
	return mapProfileError(tx.Commit())
}

// RemoveGuardian deletes one link.
func (r *ProfileRepository) RemoveGuardian(ctx context.Context, groupID, parentUserID, childUserID int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
		DELETE FROM guardians WHERE group_id = $1 AND parent_user_id = $2 AND child_user_id = $3
	`, groupID, parentUserID, childUserID)
	if err != nil {
		return mapProfileError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapProfileError(err)
	}
	if rows == 0 {
		return service.ErrGuardianNotFound
	}
	return nil
}

// ChildUserIDs returns the members parentUserID is a guardian of.
func (r *ProfileRepository) ChildUserIDs(ctx context.Context, groupID, parentUserID int64) ([]int64, error) {
	return r.ids(ctx, `
		SELECT child_user_id FROM guardians WHERE group_id = $1 AND parent_user_id = $2 ORDER BY child_user_id
	`, groupID, parentUserID)
}

// TeamIDs returns the teams on which a child of parentUserID is a player.
func (r *ProfileRepository) TeamIDs(ctx context.Context, groupID, parentUserID int64) ([]int64, error) {
	return r.ids(ctx, `
		SELECT DISTINCT tm.team_id
		FROM guardians g
		JOIN team_members tm ON tm.group_id = g.group_id AND tm.user_id = g.child_user_id AND tm.role = 'player'
		WHERE g.group_id = $1 AND g.parent_user_id = $2
		ORDER BY tm.team_id
	`, groupID, parentUserID)
}

func (r *ProfileRepository) ids(ctx context.Context, query string, args ...any) ([]int64, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, mapProfileError(err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, mapProfileError(err)
		}
		ids = append(ids, id)
	}
	return ids, mapProfileError(rows.Err())
}

func (r *ProfileRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func insertGuardian(ctx context.Context, tx *sql.Tx, groupID, parentUserID, childUserID int64, createdBy *int64, createdAt int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO guardians (group_id, parent_user_id, child_user_id, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING
	`, groupID, parentUserID, childUserID, createdBy, createdAt)
	return mapProfileError(err)
}

// classifyProfileWrite reports a vanished profile as not-found and an
// existing one as a stale revision.
func classifyProfileWrite(ctx context.Context, tx *sql.Tx, groupID, userID int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM child_profiles WHERE group_id = $1 AND user_id = $2)
	`, groupID, userID).Scan(&exists); err != nil {
		return mapProfileError(err)
	}
	if !exists {
		return service.ErrProfileNotFound
	}
	return service.ClassifyStaleWrite(false)
}

func scanProfile(row rowScanner) (service.ChildProfile, error) {
	var (
		profile   service.ChildProfile
		createdBy sql.NullInt64
	)
	if err := row.Scan(&profile.UserID, &profile.GroupID, &profile.DisplayName, &profile.BirthYear, &createdBy, &profile.CreatedAt, &profile.Version); err != nil {
		return service.ChildProfile{}, mapProfileError(err)
	}
	if profile.UserID <= 0 || profile.GroupID <= 0 || profile.Version < 0 {
		return service.ChildProfile{}, ErrInvalidData
	}
	profile.CreatedBy = nullableInt64(createdBy)
	return profile, nil
}

func mapProfileError(err error) error {
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
		return service.ErrProfileNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "guardians_parent_fk":
			return service.ErrGuardianNeedsParentRole
		case "guardians_child_fk":
			return service.ErrMembershipNotFound
		case "guardians_not_self":
			return service.ErrInvalidGuardian
		case "child_profiles_group_id_fk":
			return service.ErrGroupNotFound
		case "child_profiles_birth_year_valid":
			return service.ErrInvalidProfileBirthYear
		}
	}
	return ClassifyError(err)
}
