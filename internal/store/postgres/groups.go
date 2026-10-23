package postgres

import (
	"context"
	"database/sql"
	"errors"

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

const groupSelect = `
	SELECT id, name, name_norm, invite_code, created_by, created_at, version
	FROM groups`

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
	return findGroup(ctx, db.QueryRowContext(ctx, groupSelect+" WHERE id = $1", id))
}

// FindGroupByInviteCode returns the group that owns one active invite code.
func (r *GroupRepository) FindGroupByInviteCode(ctx context.Context, code string) (service.Group, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Group{}, err
	}
	if code == "" {
		return service.Group{}, service.ErrGroupInviteNotFound
	}
	return findGroup(ctx, db.QueryRowContext(ctx, groupSelect+" WHERE invite_code = $1", code))
}

// ListGroupsForUser returns the groups the account belongs to with member
// counts and the account's role, sorted by normalized name.
func (r *GroupRepository) ListGroupsForUser(ctx context.Context, userID int64) ([]service.GroupSummary, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, service.ErrInvalidUserID
	}
	rows, err := db.QueryContext(ctx, `
		SELECT g.id, g.name, g.name_norm, g.invite_code, g.created_by, g.created_at, g.version,
			gm.role, (SELECT count(*) FROM group_members counted WHERE counted.group_id = g.id)
		FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		WHERE gm.user_id = $1
		ORDER BY g.name_norm, g.id
	`, userID)
	if err != nil {
		return nil, mapGroupError(err)
	}
	defer func() { _ = rows.Close() }()

	summaries := make([]service.GroupSummary, 0)
	for rows.Next() {
		var (
			summary     service.GroupSummary
			createdBy   sql.NullInt64
			memberCount int64
		)
		if err := rows.Scan(
			&summary.Group.ID, &summary.Group.Name, &summary.Group.NameNorm, &summary.Group.InviteCode,
			&createdBy, &summary.Group.CreatedAt, &summary.Group.Version, &summary.Role, &memberCount,
		); err != nil {
			return nil, mapGroupError(err)
		}
		summary.Group.CreatedBy = nullableInt64(createdBy)
		summary.MemberCount = int(memberCount)
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, mapGroupError(err)
	}
	return summaries, nil
}

// ListAllGroupsForUser returns every group with member counts and the account's
// real membership role when one exists. Role is empty for groups the account
// does not belong to, so superuser access is never reported as a group role.
func (r *GroupRepository) ListAllGroupsForUser(ctx context.Context, userID int64) ([]service.GroupSummary, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, service.ErrInvalidUserID
	}
	rows, err := db.QueryContext(ctx, `
		SELECT g.id, g.name, g.name_norm, g.invite_code, g.created_by, g.created_at, g.version,
			gm.role, (SELECT count(*) FROM group_members counted WHERE counted.group_id = g.id)
		FROM groups g
		LEFT JOIN group_members gm ON gm.group_id = g.id AND gm.user_id = $1
		ORDER BY g.name_norm, g.id
	`, userID)
	if err != nil {
		return nil, mapGroupError(err)
	}
	defer func() { _ = rows.Close() }()

	summaries := make([]service.GroupSummary, 0)
	for rows.Next() {
		var (
			summary     service.GroupSummary
			createdBy   sql.NullInt64
			role        sql.NullString
			memberCount int64
		)
		if err := rows.Scan(
			&summary.Group.ID, &summary.Group.Name, &summary.Group.NameNorm, &summary.Group.InviteCode,
			&createdBy, &summary.Group.CreatedAt, &summary.Group.Version, &role, &memberCount,
		); err != nil {
			return nil, mapGroupError(err)
		}
		summary.Group.CreatedBy = nullableInt64(createdBy)
		if role.Valid {
			summary.Role = role.String
		}
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
	updated, scanErr := findGroup(ctx, db.QueryRowContext(ctx, `
		UPDATE groups
		SET name = $1, name_norm = $2, invite_code = $3, version = version + 1
		WHERE id = $4 AND version = $5
		RETURNING id, name, name_norm, invite_code, created_by, created_at, version
	`, group.Name, group.NameNorm, group.InviteCode, group.ID, expectedVersion))
	if scanErr == nil {
		return updated, nil
	}
	if !errors.Is(scanErr, service.ErrGroupNotFound) {
		return service.Group{}, scanErr
	}
	return service.Group{}, r.classifyMissingWrite(ctx, db, group.ID)
}

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

// GroupMembershipRepository persists group memberships in PostgreSQL.
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

var _ service.GroupMembershipRepository = (*GroupMembershipRepository)(nil)

// CreateGroupWithAdmin inserts a group and its first administrator membership in
// one transaction. A duplicate invite code is reported as taken so callers can
// regenerate it.
func (r *GroupMembershipRepository) CreateGroupWithAdmin(ctx context.Context, group service.Group, userID int64, role string) (service.Group, service.GroupMembership, error) {
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
	membership, err := scanMembership(tx.QueryRowContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, created_at, version)
		VALUES ($1, $2, $3, $4, 0)
		RETURNING id, group_id, user_id, role, created_at, version
	`, created.ID, userID, role, created.CreatedAt))
	if err != nil {
		return service.Group{}, service.GroupMembership{}, mapGroupError(err)
	}
	if err := tx.Commit(); err != nil {
		return service.Group{}, service.GroupMembership{}, mapGroupError(err)
	}
	return created, membership, nil
}

// CreateMembership adds one ordinary membership. The caller decides the role;
// joining by invite always uses the member role.
func (r *GroupMembershipRepository) CreateMembership(ctx context.Context, membership service.GroupMembership) (service.GroupMembership, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupMembership{}, err
	}
	created, err := scanMembership(db.QueryRowContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, created_at, version)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, group_id, user_id, role, created_at, version
	`, membership.GroupID, membership.UserID, membership.Role, membership.CreatedAt, membership.Version))
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	return created, nil
}

// FindMembership returns one membership or a not-found outcome.
func (r *GroupMembershipRepository) FindMembership(ctx context.Context, groupID, userID int64) (service.GroupMembership, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupMembership{}, err
	}
	if groupID <= 0 || userID <= 0 {
		return service.GroupMembership{}, service.ErrMembershipNotFound
	}
	membership, err := scanMembership(db.QueryRowContext(ctx, `
		SELECT id, group_id, user_id, role, created_at, version
		FROM group_members
		WHERE group_id = $1 AND user_id = $2
	`, groupID, userID))
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	return membership, nil
}

// ListMembers returns the roster with display data ordered by role then label.
func (r *GroupMembershipRepository) ListMembers(ctx context.Context, groupID int64) ([]service.GroupMember, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT gm.user_id, gm.role, gm.version, u.username, u.display_name, u.email
		FROM group_members gm
		JOIN users u ON u.id = gm.user_id
		WHERE gm.group_id = $1
		ORDER BY (gm.role = 'admin') DESC, lower(u.display_name), lower(u.username), gm.user_id
	`, groupID)
	if err != nil {
		return nil, mapMembershipError(err)
	}
	defer func() { _ = rows.Close() }()

	members := make([]service.GroupMember, 0)
	for rows.Next() {
		var (
			member      service.GroupMember
			username    sql.NullString
			displayName sql.NullString
			email       sql.NullString
		)
		if err := rows.Scan(&member.UserID, &member.Role, &member.Version, &username, &displayName, &email); err != nil {
			return nil, mapMembershipError(err)
		}
		member.Username = nullableString(username)
		member.DisplayName = nullableString(displayName)
		member.Email = nullableString(email)
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

// UpdateMembershipRole changes one membership role when its expected version is
// current and the change leaves at least one administrator. Demoting the final
// administrator is refused inside the same transaction.
func (r *GroupMembershipRepository) UpdateMembershipRole(ctx context.Context, membership service.GroupMembership, expectedVersion int64) (service.GroupMembership, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupMembership{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockGroupMembers(ctx, tx, membership.GroupID); err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	var currentRole string
	err = tx.QueryRowContext(ctx, `
		SELECT role FROM group_members
		WHERE group_id = $1 AND user_id = $2
		FOR UPDATE
	`, membership.GroupID, membership.UserID).Scan(&currentRole)
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	if currentRole == service.GroupRoleAdmin && membership.Role != service.GroupRoleAdmin {
		if err := ensureAnotherAdmin(ctx, tx, membership.GroupID, membership.UserID); err != nil {
			return service.GroupMembership{}, err
		}
	}
	updated, err := scanMembership(tx.QueryRowContext(ctx, `
		UPDATE group_members
		SET role = $1, version = version + 1
		WHERE group_id = $2 AND user_id = $3 AND version = $4
		RETURNING id, group_id, user_id, role, created_at, version
	`, membership.Role, membership.GroupID, membership.UserID, expectedVersion))
	if errors.Is(err, sql.ErrNoRows) {
		// The membership row was locked above, so a missing update row means the
		// caller's revision is stale rather than the record having vanished.
		return service.GroupMembership{}, service.ErrOptimisticLockConflict
	}
	if err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	if err := tx.Commit(); err != nil {
		return service.GroupMembership{}, mapMembershipError(err)
	}
	return updated, nil
}

// DeleteMembership removes one membership when its expected version is current.
// Removing the final administrator is refused inside the same transaction.
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
	var currentRole string
	err = tx.QueryRowContext(ctx, `
		SELECT role FROM group_members
		WHERE group_id = $1 AND user_id = $2
		FOR UPDATE
	`, groupID, userID).Scan(&currentRole)
	if err != nil {
		return mapMembershipError(err)
	}
	if currentRole == service.GroupRoleAdmin {
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
		return service.ErrOptimisticLockConflict
	}
	return mapMembershipError(tx.Commit())
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
	created, err := findGroup(ctx, executor.QueryRowContext(ctx, `
		INSERT INTO groups (name, name_norm, invite_code, created_by, created_at, version)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, name_norm, invite_code, created_by, created_at, version
	`, group.Name, group.NameNorm, group.InviteCode, group.CreatedBy, group.CreatedAt, group.Version))
	if err != nil {
		return service.Group{}, err
	}
	return created, nil
}

func findGroup(_ context.Context, row rowScanner) (service.Group, error) {
	var (
		group     service.Group
		createdBy sql.NullInt64
	)
	if err := row.Scan(&group.ID, &group.Name, &group.NameNorm, &group.InviteCode, &createdBy, &group.CreatedAt, &group.Version); err != nil {
		return service.Group{}, mapGroupError(err)
	}
	if group.ID <= 0 || group.Version < 0 || group.Name == "" || group.NameNorm == "" || group.InviteCode == "" {
		return service.Group{}, ErrInvalidData
	}
	group.CreatedBy = nullableInt64(createdBy)
	return group, nil
}

func scanMembership(row rowScanner) (service.GroupMembership, error) {
	var membership service.GroupMembership
	if err := row.Scan(&membership.ID, &membership.GroupID, &membership.UserID, &membership.Role, &membership.CreatedAt, &membership.Version); err != nil {
		return service.GroupMembership{}, err
	}
	if membership.ID <= 0 || membership.GroupID <= 0 || membership.UserID <= 0 || membership.Version < 0 {
		return service.GroupMembership{}, ErrInvalidData
	}
	if membership.Role != service.GroupRoleAdmin && membership.Role != service.GroupRoleMember {
		return service.GroupMembership{}, ErrInvalidData
	}
	return membership, nil
}

func ensureAnotherAdmin(ctx context.Context, tx *sql.Tx, groupID, excludeUserID int64) error {
	var otherAdmins int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM group_members
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
	rows, err := tx.QueryContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('calbooker:group:' || $1::bigint::text, 0))
	`, groupID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := rows.Scan(new(any)); err != nil {
			return err
		}
	}
	return rows.Err()
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
		case "groups_invite_code_key", "groups_invite_code_unique_idx":
			return service.ErrGroupInviteTaken
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
