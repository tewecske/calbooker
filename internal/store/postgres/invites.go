package postgres

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// maxListedInvites bounds the invite page.
const maxListedInvites = 200

// InviteRepository persists group invites in PostgreSQL. Every group-scoped
// query filters by group.
type InviteRepository struct {
	adapter *Adapter
}

// NewInviteRepository constructs invite storage over db.
func NewInviteRepository(db *sql.DB) (*InviteRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &InviteRepository{adapter: adapter}, nil
}

var _ service.GroupInviteRepository = (*InviteRepository)(nil)

const inviteColumns = `id, group_id, code, role, team_id, expires_at, max_uses, uses, created_by, created_at, revoked_at`

// CreateInvite inserts one invite. A duplicate code is reported as taken so
// the caller can generate another.
func (r *InviteRepository) CreateInvite(ctx context.Context, invite service.GroupInvite) (service.GroupInvite, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupInvite{}, err
	}
	if invite.GroupID <= 0 || invite.Code == "" || !service.ValidGroupRole(invite.Role) {
		return service.GroupInvite{}, ErrInvalidData
	}
	return scanInvite(db.QueryRowContext(ctx, `
		INSERT INTO group_invites (group_id, code, role, team_id, expires_at, max_uses, uses, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8)
		RETURNING `+inviteColumns,
		invite.GroupID, invite.Code, invite.Role, invite.TeamID, invite.ExpiresAt, invite.MaxUses, invite.CreatedBy, invite.CreatedAt))
}

// ListInvites returns the group's invites with team names: unrevoked first,
// newest first.
func (r *InviteRepository) ListInvites(ctx context.Context, groupID int64) ([]service.GroupInviteListing, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT i.id, i.group_id, i.code, i.role, i.team_id, i.expires_at, i.max_uses, i.uses, i.created_by, i.created_at, i.revoked_at, t.name
		FROM group_invites i
		LEFT JOIN teams t ON t.group_id = i.group_id AND t.id = i.team_id
		WHERE i.group_id = $1
		ORDER BY (i.revoked_at IS NOT NULL), i.created_at DESC, i.id DESC
		LIMIT $2
	`, groupID, maxListedInvites)
	if err != nil {
		return nil, mapInviteError(err)
	}
	defer func() { _ = rows.Close() }()
	listings := make([]service.GroupInviteListing, 0)
	for rows.Next() {
		var (
			listing  service.GroupInviteListing
			teamName sql.NullString
		)
		invite, err := scanInviteFields(rows.Scan, &teamName)
		if err != nil {
			return nil, err
		}
		listing.Invite = invite
		listing.TeamName = nullableString(teamName)
		listings = append(listings, listing)
	}
	if err := rows.Err(); err != nil {
		return nil, mapInviteError(err)
	}
	return listings, nil
}

// FindInvite returns one invite of the group.
func (r *InviteRepository) FindInvite(ctx context.Context, groupID, inviteID int64) (service.GroupInvite, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupInvite{}, err
	}
	if groupID <= 0 || inviteID <= 0 {
		return service.GroupInvite{}, service.ErrGroupInviteNotFound
	}
	return scanInvite(db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM group_invites WHERE group_id = $1 AND id = $2`, groupID, inviteID))
}

// FindInviteByCode returns the invite with code, in whatever state.
func (r *InviteRepository) FindInviteByCode(ctx context.Context, code string) (service.GroupInvite, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupInvite{}, err
	}
	if code == "" {
		return service.GroupInvite{}, service.ErrGroupInviteNotFound
	}
	return scanInvite(db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM group_invites WHERE code = $1`, code))
}

// RevokeInvite marks the invite revoked at revokedAt unless it already is.
func (r *InviteRepository) RevokeInvite(ctx context.Context, groupID, inviteID, revokedAt int64) (service.GroupInvite, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GroupInvite{}, err
	}
	return scanInvite(db.QueryRowContext(ctx, `
		UPDATE group_invites SET revoked_at = COALESCE(revoked_at, $3)
		WHERE group_id = $1 AND id = $2
		RETURNING `+inviteColumns, groupID, inviteID, revokedAt))
}

// RedeemInvite locks the invite, re-checks it at now, and grants its
// membership, role, and team entry in one transaction. A use is counted only
// when the account gained something.
func (r *InviteRepository) RedeemInvite(ctx context.Context, inviteID, userID, now int64) (service.InviteRedemption, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.InviteRedemption{}, err
	}
	if inviteID <= 0 || userID <= 0 {
		return service.InviteRedemption{}, service.ErrGroupInviteNotFound
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.InviteRedemption{}, mapInviteError(err)
	}
	defer func() { _ = tx.Rollback() }()

	invite, err := scanInvite(tx.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM group_invites WHERE id = $1 FOR UPDATE`, inviteID))
	if err != nil {
		return service.InviteRedemption{}, err
	}
	switch invite.Status(now) {
	case service.InviteRevoked:
		return service.InviteRedemption{}, service.ErrInviteRevoked
	case service.InviteExpired:
		return service.InviteRedemption{}, service.ErrInviteExpired
	case service.InviteUsedUp:
		return service.InviteRedemption{}, service.ErrInviteUsedUp
	}
	if err := lockGroupMembers(ctx, tx, invite.GroupID); err != nil {
		return service.InviteRedemption{}, mapMembershipError(err)
	}
	changed := false
	membership, err := findMembership(ctx, tx, invite.GroupID, userID)
	switch {
	case errors.Is(err, service.ErrMembershipNotFound):
		if _, err := insertMembership(ctx, tx, service.GroupMembership{GroupID: invite.GroupID, UserID: userID, Roles: []string{invite.Role}, CreatedAt: now}); err != nil {
			return service.InviteRedemption{}, err
		}
		changed = true
	case err != nil:
		return service.InviteRedemption{}, err
	case !slices.Contains(membership.Roles, invite.Role):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO group_member_roles (group_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)
		`, invite.GroupID, userID, invite.Role, now); err != nil {
			return service.InviteRedemption{}, mapMembershipError(err)
		}
		if err := bumpMembership(ctx, tx, invite.GroupID, userID, membership.Version); err != nil {
			return service.InviteRedemption{}, err
		}
		changed = true
	}
	teamID := invite.Team()
	if teamID > 0 {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO team_members (group_id, team_id, user_id, role, created_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING
		`, invite.GroupID, teamID, userID, invite.Role, now)
		if err != nil {
			return service.InviteRedemption{}, mapTeamError(err)
		}
		if added, err := result.RowsAffected(); err != nil {
			return service.InviteRedemption{}, mapTeamError(err)
		} else if added > 0 {
			changed = true
		}
	}
	if changed {
		if _, err := tx.ExecContext(ctx, `UPDATE group_invites SET uses = uses + 1 WHERE id = $1`, invite.ID); err != nil {
			return service.InviteRedemption{}, mapInviteError(err)
		}
	}
	membership, err = findMembership(ctx, tx, invite.GroupID, userID)
	if err != nil {
		return service.InviteRedemption{}, err
	}
	redemption := service.InviteRedemption{Membership: membership, TeamID: teamID, Changed: changed}
	if err := tx.QueryRowContext(ctx, `SELECT slug FROM groups WHERE id = $1`, invite.GroupID).Scan(&redemption.GroupSlug); err != nil {
		return service.InviteRedemption{}, mapInviteError(err)
	}
	if teamID > 0 {
		if err := tx.QueryRowContext(ctx, `SELECT slug FROM teams WHERE group_id = $1 AND id = $2`, invite.GroupID, teamID).Scan(&redemption.TeamSlug); err != nil {
			return service.InviteRedemption{}, mapInviteError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return service.InviteRedemption{}, mapInviteError(err)
	}
	return redemption, nil
}

func (r *InviteRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanInvite(row rowScanner) (service.GroupInvite, error) {
	return scanInviteFields(row.Scan)
}

// scanInviteFields scans the invite columns followed by extra destinations.
func scanInviteFields(scan func(...any) error, extra ...any) (service.GroupInvite, error) {
	var (
		invite                       service.GroupInvite
		teamID, expiresAt, createdBy sql.NullInt64
		revokedAt                    sql.NullInt64
		maxUses                      sql.NullInt32
	)
	destinations := append([]any{&invite.ID, &invite.GroupID, &invite.Code, &invite.Role, &teamID, &expiresAt, &maxUses, &invite.Uses, &createdBy, &invite.CreatedAt, &revokedAt}, extra...)
	if err := scan(destinations...); err != nil {
		return service.GroupInvite{}, mapInviteError(err)
	}
	if invite.ID <= 0 || invite.GroupID <= 0 || invite.Code == "" || !service.ValidGroupRole(invite.Role) || invite.Uses < 0 {
		return service.GroupInvite{}, ErrInvalidData
	}
	invite.TeamID = nullableInt64(teamID)
	invite.ExpiresAt = nullableInt64(expiresAt)
	invite.CreatedBy = nullableInt64(createdBy)
	invite.RevokedAt = nullableInt64(revokedAt)
	if maxUses.Valid {
		limit := int(maxUses.Int32)
		invite.MaxUses = &limit
	}
	return invite, nil
}

func mapInviteError(err error) error {
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
		return service.ErrGroupInviteNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "group_invites_code_unique":
			return service.ErrGroupInviteTaken
		case pgErr.Code == "23503" && pgErr.ConstraintName == "group_invites_team_fk":
			return service.ErrTeamNotFound
		case pgErr.Code == "23503" && pgErr.ConstraintName == "group_invites_group_id_fk":
			return service.ErrGroupNotFound
		}
	}
	return ClassifyError(err)
}
