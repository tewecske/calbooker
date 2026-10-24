package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// GroupRepository persists groups in PostgreSQL.
type GroupRepository struct {
	adapter *Adapter
}

// NewGroupRepository constructs a PostgreSQL group repository over db.
func NewGroupRepository(db *sql.DB) (*GroupRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &GroupRepository{adapter: adapter}, nil
}

var _ service.GroupRepository = (*GroupRepository)(nil)

const groupColumns = `id, name, name_norm, time_zone, created_by, created_at, version`

const groupSelect = `SELECT ` + groupColumns + ` FROM groups`

// CreateGroup inserts one group and returns database-generated fields.
func (r *GroupRepository) CreateGroup(ctx context.Context, group service.Group) (service.Group, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Group{}, err
	}
	return createGroup(ctx, db, group)
}

// FindGroupByID returns one group by its generated identifier.
func (r *GroupRepository) FindGroupByID(ctx context.Context, id int64) (service.Group, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Group{}, err
	}
	if id <= 0 {
		return service.Group{}, service.ErrGroupNotFound
	}
	return findGroup(db.QueryRowContext(ctx, groupSelect+" WHERE id = $1", id))
}

// groupSummarySelect lists groups with member counts and the account's roles
// ($1). The join decides whether non-member groups are included.
const groupSummarySelect = `
	SELECT g.id, g.name, g.name_norm, g.time_zone, g.created_by, g.created_at, g.version,
		(SELECT count(*) FROM group_members counted WHERE counted.group_id = g.id),
		(SELECT string_agg(r.role, ',') FROM group_member_roles r WHERE r.group_id = g.id AND r.user_id = $1)
	FROM groups g`

// ListGroupsForUser returns the groups the account belongs to with member
// counts and the account's roles, sorted by normalized name.
func (r *GroupRepository) ListGroupsForUser(ctx context.Context, userID int64) ([]service.GroupSummary, error) {
	if userID <= 0 {
		return nil, service.ErrInvalidUserID
	}
	return r.listSummaries(ctx, groupSummarySelect+`
		JOIN group_members gm ON gm.group_id = g.id AND gm.user_id = $1
		ORDER BY g.name_norm, g.id`, userID)
}

// ListAllGroupsForUser returns every group with member counts and the
// account's real roles. Roles are empty for groups the account does not
// belong to, so superuser access is never reported as a group role.
func (r *GroupRepository) ListAllGroupsForUser(ctx context.Context, userID int64) ([]service.GroupSummary, error) {
	if userID <= 0 {
		return nil, service.ErrInvalidUserID
	}
	return r.listSummaries(ctx, groupSummarySelect+` ORDER BY g.name_norm, g.id`, userID)
}

func (r *GroupRepository) listSummaries(ctx context.Context, query string, userID int64) ([]service.GroupSummary, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, mapGroupError(err)
	}
	defer func() { _ = rows.Close() }()

	summaries := make([]service.GroupSummary, 0)
	for rows.Next() {
		var (
			summary     service.GroupSummary
			createdBy   sql.NullInt64
			roles       sql.NullString
			memberCount int64
		)
		if err := rows.Scan(
			&summary.Group.ID, &summary.Group.Name, &summary.Group.NameNorm, &summary.Group.TimeZone,
			&createdBy, &summary.Group.CreatedAt, &summary.Group.Version, &memberCount, &roles,
		); err != nil {
			return nil, mapGroupError(err)
		}
		summary.Group.CreatedBy = nullableInt64(createdBy)
		summary.Roles = splitList(roles)
		summary.MemberCount = int(memberCount)
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, mapGroupError(err)
	}
	return summaries, nil
}

// UpdateGroup replaces editable group fields when expectedVersion is current.
func (r *GroupRepository) UpdateGroup(ctx context.Context, group service.Group, expectedVersion int64) (service.Group, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Group{}, err
	}
	updated, scanErr := findGroup(db.QueryRowContext(ctx, `
		UPDATE groups
		SET name = $1, name_norm = $2, time_zone = $3, version = version + 1
		WHERE id = $4 AND version = $5
		RETURNING `+groupColumns,
		group.Name, group.NameNorm, group.TimeZone, group.ID, expectedVersion))
	if scanErr == nil {
		return updated, nil
	}
	if !errors.Is(scanErr, service.ErrGroupNotFound) {
		return service.Group{}, scanErr
	}
	return service.Group{}, r.classifyMissingWrite(ctx, db, group.ID)
}

// classifyMissingWrite reports a vanished group as not-found and an existing
// one as a stale revision.
func (r *GroupRepository) classifyMissingWrite(ctx context.Context, db *sql.DB, id int64) error {
	if id <= 0 {
		return service.ErrGroupNotFound
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM groups WHERE id = $1)", id).Scan(&exists); err != nil {
		return mapGroupError(err)
	}
	if !exists {
		return service.ErrGroupNotFound
	}
	return service.ErrOptimisticLockConflict
}

func (r *GroupRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

// GroupMembershipRepository persists group memberships and roles in
// PostgreSQL.
type GroupMembershipRepository struct {
	adapter *Adapter
}

// NewGroupMembershipRepository constructs membership storage over db.
func NewGroupMembershipRepository(db *sql.DB) (*GroupMembershipRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &GroupMembershipRepository{adapter: adapter}, nil
}

var (
	_ service.GroupMembershipRepository = (*GroupMembershipRepository)(nil)
	_ service.GroupAccessRepository     = (*GroupMembershipRepository)(nil)
)

// CreateGroupWithAdmin inserts a group and its creator as administrator in
// one transaction.
func (r *GroupMembershipRepository) CreateGroupWithAdmin(ctx context.Context, group service.Group, userID int64) (service.Group, service.GroupMembership, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Group{}, service.GroupMembership{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.Group{}, service.GroupMembership{}, mapGroupError(err)
	}
	defer func() { _ = tx.Rollback() }()

	created, err := createGroup(ctx, tx, group)
	if err != nil {
		return service.Group{}, service.GroupMembership{}, err
	}
	membership, err := insertMembership(ctx, tx, service.GroupMembership{
		GroupID: created.ID, UserID: userID, Roles: []string{service.GroupRoleAdmin}, CreatedAt: created.CreatedAt,
	})
	if err != nil {
		return service.Group{}, service.GroupMembership{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.Group{}, service.GroupMembership{}, mapGroupError(err)
	}
	return created, membership, nil
}

// CreateMembership adds one member with its roles in one transaction.
func (r *GroupMembershipRepository) CreateMembership(ctx context.Context, membership service.GroupMembership) (service.GroupMembership, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupMembership{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	defer func() { _ = tx.Rollback() }()
	created, err := insertMembership(ctx, tx, membership)
	if err != nil {
		return service.GroupMembership{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	return created, nil
}

// FindMembership returns one membership with its roles or a not-found outcome.
func (r *GroupMembershipRepository) FindMembership(ctx context.Context, groupID, userID int64) (service.GroupMembership, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupMembership{}, err
	}
	if groupID <= 0 || userID <= 0 {
		return service.GroupMembership{}, service.ErrMembershipNotFound
	}
	return findMembership(ctx, db, groupID, userID)
}

// ListMembers returns the roster with display data, roles, and team IDs,
// administrators first, then by name.
func (r *GroupMembershipRepository) ListMembers(ctx context.Context, groupID int64) ([]service.GroupMember, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT gm.user_id, gm.version, u.kind, u.username, u.display_name, u.email,
			(SELECT string_agg(r.role, ',') FROM group_member_roles r WHERE r.group_id = gm.group_id AND r.user_id = gm.user_id),
			(SELECT string_agg(DISTINCT tm.team_id::text, ',') FROM team_members tm WHERE tm.group_id = gm.group_id AND tm.user_id = gm.user_id),
			EXISTS (SELECT 1 FROM group_member_roles a WHERE a.group_id = gm.group_id AND a.user_id = gm.user_id AND a.role = 'admin')
		FROM group_members gm
		JOIN users u ON u.id = gm.user_id
		WHERE gm.group_id = $1
		ORDER BY 9 DESC, lower(u.display_name), lower(u.username), gm.user_id
	`, groupID)
	if err != nil {
		return nil, mapMembershipError(err)
	}
	defer func() { _ = rows.Close() }()

	members := make([]service.GroupMember, 0)
	for rows.Next() {
		var (
			member      service.GroupMember
			kind        string
			username    sql.NullString
			displayName sql.NullString
			email       sql.NullString
			roles       sql.NullString
			teams       sql.NullString
			isAdmin     bool
		)
		if err := rows.Scan(&member.UserID, &member.Version, &kind, &username, &displayName, &email, &roles, &teams, &isAdmin); err != nil {
			return nil, mapMembershipError(err)
		}
		member.Kind = service.UserKind(kind)
		member.Username = nullableString(username)
		member.DisplayName = nullableString(displayName)
		member.Email = nullableString(email)
		member.Roles = sortedRoles(splitList(roles))
		if member.TeamIDs, err = splitIDs(teams); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, mapMembershipError(err)
	}
	return members, nil
}

// CountMembers returns the number of memberships in a group.
func (r *GroupMembershipRepository) CountMembers(ctx context.Context, groupID int64) (int, error) {
	db, err := r.database(ctx)
	if err != nil {
		return 0, err
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM group_members WHERE group_id = $1", groupID).Scan(&count); err != nil {
		return 0, mapMembershipError(err)
	}
	return count, nil
}

// AddMemberRole adds role to a member and bumps the membership revision when
// expectedVersion is current. Adding a held role only bumps the revision.
func (r *GroupMembershipRepository) AddMemberRole(ctx context.Context, groupID, userID int64, role string, expectedVersion int64) (service.GroupMembership, error) {
	return r.changeRole(ctx, groupID, userID, expectedVersion, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO group_member_roles (group_id, user_id, role, created_at)
			VALUES ($1, $2, $3, extract(epoch FROM now())::bigint)
			ON CONFLICT DO NOTHING
		`, groupID, userID, role)
		return mapMembershipError(err)
	})
}

// RemoveMemberRole removes role from a member and bumps the membership
// revision when expectedVersion is current. The group's last administrator
// keeps the admin role.
func (r *GroupMembershipRepository) RemoveMemberRole(ctx context.Context, groupID, userID int64, role string, expectedVersion int64) (service.GroupMembership, error) {
	return r.changeRole(ctx, groupID, userID, expectedVersion, func(tx *sql.Tx) error {
		if role == service.GroupRoleAdmin {
			if err := ensureAnotherAdmin(ctx, tx, groupID, userID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `
			DELETE FROM group_member_roles WHERE group_id = $1 AND user_id = $2 AND role = $3
		`, groupID, userID, role)
		return mapMembershipError(err)
	})
}

// changeRole serializes role writes per group, applies the revision check on
// the membership row, runs change, and returns the re-read membership.
func (r *GroupMembershipRepository) changeRole(ctx context.Context, groupID, userID, expectedVersion int64, change func(*sql.Tx) error) (service.GroupMembership, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupMembership{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroupMembers(ctx, tx, groupID); err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	if err := bumpMembership(ctx, tx, groupID, userID, expectedVersion); err != nil {
		return service.GroupMembership{}, err
	}
	if err := change(tx); err != nil {
		return service.GroupMembership{}, err
	}
	updated, err := findMembership(ctx, tx, groupID, userID)
	if err != nil {
		return service.GroupMembership{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	return updated, nil
}

// DeleteMembership removes one membership, its roles, and its roster entries
// when its expected version is current. Removing the final administrator is
// refused inside the same transaction.
func (r *GroupMembershipRepository) DeleteMembership(ctx context.Context, groupID, userID, expectedVersion int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapMembershipError(err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockGroupMembers(ctx, tx, groupID); err != nil {
		return mapMembershipError(err)
	}
	var isAdmin bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM group_member_roles WHERE group_id = $1 AND user_id = $2 AND role = 'admin')
	`, groupID, userID).Scan(&isAdmin); err != nil {
		return mapMembershipError(err)
	}
	if isAdmin {
		if err := ensureAnotherAdmin(ctx, tx, groupID, userID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `
		DELETE FROM group_members
		WHERE group_id = $1 AND user_id = $2 AND version = $3
	`, groupID, userID, expectedVersion)
	if err != nil {
		return mapMembershipError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapMembershipError(err)
	}
	if rows == 0 {
		return classifyMembershipWrite(ctx, tx, groupID, userID)
	}
	return mapMembershipError(tx.Commit())
}

// FindMemberAccess returns the member's roles and own teams for GroupAccess.
func (r *GroupMembershipRepository) FindMemberAccess(ctx context.Context, groupID, userID int64) (service.MemberAccess, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.MemberAccess{}, err
	}
	membership, err := r.FindMembership(ctx, groupID, userID)
	if err != nil {
		return service.MemberAccess{}, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT team_id, role FROM team_members
		WHERE group_id = $1 AND user_id = $2
		ORDER BY team_id, role
	`, groupID, userID)
	if err != nil {
		return service.MemberAccess{}, mapMembershipError(err)
	}
	defer func() { _ = rows.Close() }()
	access := service.MemberAccess{Roles: membership.Roles}
	for rows.Next() {
		var team service.TeamRole
		if err := rows.Scan(&team.TeamID, &team.Role); err != nil {
			return service.MemberAccess{}, mapMembershipError(err)
		}
		access.OwnTeams = append(access.OwnTeams, team)
	}
	if err := rows.Err(); err != nil {
		return service.MemberAccess{}, mapMembershipError(err)
	}
	return access, nil
}

// GroupExists reports whether the group exists.
func (r *GroupMembershipRepository) GroupExists(ctx context.Context, groupID int64) (bool, error) {
	db, err := r.database(ctx)
	if err != nil {
		return false, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM groups WHERE id = $1)", groupID).Scan(&exists); err != nil {
		return false, mapGroupError(err)
	}
	return exists, nil
}

func (r *GroupMembershipRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

type groupExecutor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func createGroup(ctx context.Context, executor groupExecutor, group service.Group) (service.Group, error) {
	if group.TimeZone == "" {
		group.TimeZone = service.DefaultGroupTimeZone
	}
	return findGroup(executor.QueryRowContext(ctx, `
		INSERT INTO groups (name, name_norm, time_zone, created_by, created_at, version)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+groupColumns,
		group.Name, group.NameNorm, group.TimeZone, group.CreatedBy, group.CreatedAt, group.Version))
}

func findGroup(row rowScanner) (service.Group, error) {
	var (
		group     service.Group
		createdBy sql.NullInt64
	)
	if err := row.Scan(&group.ID, &group.Name, &group.NameNorm, &group.TimeZone, &createdBy, &group.CreatedAt, &group.Version); err != nil {
		return service.Group{}, mapGroupError(err)
	}
	if group.ID <= 0 || group.Version < 0 || group.Name == "" || group.NameNorm == "" || group.TimeZone == "" {
		return service.Group{}, ErrInvalidData
	}
	group.CreatedBy = nullableInt64(createdBy)
	return group, nil
}

func insertMembership(ctx context.Context, executor groupExecutor, membership service.GroupMembership) (service.GroupMembership, error) {
	var id int64
	if err := executor.QueryRowContext(ctx, `
		INSERT INTO group_members (group_id, user_id, created_at, version)
		VALUES ($1, $2, $3, 0)
		RETURNING id
	`, membership.GroupID, membership.UserID, membership.CreatedAt).Scan(&id); err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	for _, role := range membership.Roles {
		if !service.ValidGroupRole(role) {
			return service.GroupMembership{}, ErrInvalidData
		}
		if _, err := executor.ExecContext(ctx, `
			INSERT INTO group_member_roles (group_id, user_id, role, created_at)
			VALUES ($1, $2, $3, $4)
		`, membership.GroupID, membership.UserID, role, membership.CreatedAt); err != nil {
			return service.GroupMembership{}, mapMembershipError(err)
		}
	}
	return findMembership(ctx, executor, membership.GroupID, membership.UserID)
}

func findMembership(ctx context.Context, executor groupExecutor, groupID, userID int64) (service.GroupMembership, error) {
	var (
		membership service.GroupMembership
		roles      sql.NullString
	)
	err := executor.QueryRowContext(ctx, `
		SELECT gm.id, gm.group_id, gm.user_id, gm.created_at, gm.version,
			(SELECT string_agg(r.role, ',') FROM group_member_roles r WHERE r.group_id = gm.group_id AND r.user_id = gm.user_id)
		FROM group_members gm
		WHERE gm.group_id = $1 AND gm.user_id = $2
	`, groupID, userID).Scan(&membership.ID, &membership.GroupID, &membership.UserID, &membership.CreatedAt, &membership.Version, &roles)
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	if membership.ID <= 0 || membership.GroupID <= 0 || membership.UserID <= 0 || membership.Version < 0 {
		return service.GroupMembership{}, ErrInvalidData
	}
	membership.Roles = sortedRoles(splitList(roles))
	return membership, nil
}

// bumpMembership advances the membership revision when expectedVersion is
// current, classifying a miss as stale or vanished.
func bumpMembership(ctx context.Context, tx *sql.Tx, groupID, userID, expectedVersion int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE group_members SET version = version + 1
		WHERE group_id = $1 AND user_id = $2 AND version = $3
	`, groupID, userID, expectedVersion)
	if err != nil {
		return mapMembershipError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapMembershipError(err)
	}
	if rows == 0 {
		return classifyMembershipWrite(ctx, tx, groupID, userID)
	}
	return nil
}

// classifyMembershipWrite reports a vanished membership as not-found, never
// as a conflict, and an existing one as a stale revision.
func classifyMembershipWrite(ctx context.Context, tx *sql.Tx, groupID, userID int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2)
	`, groupID, userID).Scan(&exists); err != nil {
		return mapMembershipError(err)
	}
	if !exists {
		return service.ErrMembershipNotFound
	}
	return service.ClassifyStaleWrite(false)
}

func ensureAnotherAdmin(ctx context.Context, tx *sql.Tx, groupID, excludeUserID int64) error {
	var otherAdmins int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM group_member_roles
		WHERE group_id = $1 AND user_id <> $2 AND role = 'admin'
	`, groupID, excludeUserID).Scan(&otherAdmins); err != nil {
		return mapMembershipError(err)
	}
	if otherAdmins == 0 {
		return service.ErrLastGroupAdmin
	}
	return nil
}

func lockGroupMembers(ctx context.Context, tx *sql.Tx, groupID int64) error {
	_, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('calbooker:group:' || $1::bigint::text, 0))
	`, groupID)
	return err
}

func splitList(value sql.NullString) []string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return strings.Split(value.String, ",")
}

func splitIDs(value sql.NullString) ([]int64, error) {
	parts := splitList(value)
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, ErrInvalidData
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// sortedRoles orders roles like service.GroupRoles.
func sortedRoles(roles []string) []string {
	sorted := make([]string, 0, len(roles))
	for _, role := range service.GroupRoles() {
		for _, held := range roles {
			if held == role {
				sorted = append(sorted, role)
				break
			}
		}
	}
	return sorted
}

func mapGroupError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, service.ErrGroupNotFound) || errors.Is(err, sql.ErrNoRows) {
		return service.ErrGroupNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "group_members_group_user_unique":
			return service.ErrMembershipExists
		}
		return ErrConflict
	}
	return ClassifyError(err)
}

func mapMembershipError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, service.ErrMembershipNotFound) || errors.Is(err, sql.ErrNoRows) {
		return service.ErrMembershipNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return service.ErrMembershipExists
	}
	return ClassifyError(err)
}
