package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const (
	groupInviteCodeLength     = 12
	groupInviteAlphabet       = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	groupInviteCollisionTries = 5
	// MaxInviteUses bounds the optional use limit of one invite.
	MaxInviteUses = 10000
	// MaxInviteDays bounds the optional validity of one invite in days.
	MaxInviteDays = 365
)

var (
	// ErrGroupInviteNotFound identifies an unknown invite code, or an invite
	// the account may not see.
	ErrGroupInviteNotFound = errors.New("service: group invite not found")
	// ErrGroupInviteTaken identifies an invite code already in use.
	ErrGroupInviteTaken = errors.New("service: group invite already in use")
	// ErrGroupInviteGeneration identifies unavailable secure randomness.
	ErrGroupInviteGeneration = errors.New("service: group invite generation failed")
	// ErrInviteExpired identifies an invite past its expiry.
	ErrInviteExpired = errors.New("service: group invite expired")
	// ErrInviteUsedUp identifies an invite that reached its use limit.
	ErrInviteUsedUp = errors.New("service: group invite used up")
	// ErrInviteRevoked identifies a revoked invite.
	ErrInviteRevoked = errors.New("service: group invite revoked")
	// ErrInvalidInvite identifies an invite with an out-of-range expiry or
	// use limit, or a team on an invite for a role that has no roster.
	ErrInvalidInvite = errors.New("service: invalid group invite")
	// ErrNilInviteRepository identifies missing invite persistence.
	ErrNilInviteRepository = errors.New("service: nil invite repository")
)

// Invite audit action codes. Audit details name the invite by its id, never
// by its code.
const (
	AuditActionInviteCreated = "group.invite_created"
	AuditActionInviteRevoked = "group.invite_revoked"
)

// InviteStatus is the state of an invite at a point in time.
type InviteStatus string

// Invite states.
const (
	InviteActive  InviteStatus = "active"
	InviteExpired InviteStatus = "expired"
	InviteUsedUp  InviteStatus = "used_up"
	InviteRevoked InviteStatus = "revoked"
)

// GroupInvite is one role-specific invite code. The code is a bearer secret:
// only accounts that may manage the invite see it, and audit entries and
// logs name the invite by ID.
type GroupInvite struct {
	ID        int64
	GroupID   int64
	Code      string
	Role      string
	TeamID    *int64
	ExpiresAt *int64
	MaxUses   *int
	Uses      int
	CreatedBy *int64
	CreatedAt int64
	RevokedAt *int64
}

// Team returns the invite's team ID, or 0 for a group-only invite.
func (i GroupInvite) Team() int64 {
	if i.TeamID == nil {
		return 0
	}
	return *i.TeamID
}

// Status reports the invite's state at now (Unix seconds). A revoked invite
// is revoked even when it also expired or ran out of uses.
func (i GroupInvite) Status(now int64) InviteStatus {
	switch {
	case i.RevokedAt != nil:
		return InviteRevoked
	case i.ExpiresAt != nil && *i.ExpiresAt <= now:
		return InviteExpired
	case i.MaxUses != nil && i.Uses >= *i.MaxUses:
		return InviteUsedUp
	default:
		return InviteActive
	}
}

// err maps a non-active status to its redemption error.
func (s InviteStatus) err() error {
	switch s {
	case InviteRevoked:
		return ErrInviteRevoked
	case InviteExpired:
		return ErrInviteExpired
	case InviteUsedUp:
		return ErrInviteUsedUp
	default:
		return nil
	}
}

// GroupInviteListing is one invite row with display data.
type GroupInviteListing struct {
	Invite   GroupInvite
	TeamName *string
}

// InviteInput is a new invite. TeamID, ExpiresInDays, and MaxUses are
// optional (0 means none).
type InviteInput struct {
	Role          string
	TeamID        int64
	ExpiresInDays int
	MaxUses       int
}

// InviteRedemption is the outcome of joining with an invite code. Changed is
// false when the member already held the role (and team), in which case no
// use was counted.
type InviteRedemption struct {
	Membership GroupMembership
	TeamID     int64
	// GroupSlug and TeamSlug address the group and team in URLs; TeamSlug is
	// empty when the invite has no team.
	GroupSlug string
	TeamSlug  string
	// Role is the role the invite grants.
	Role    string
	Changed bool
}

// GroupInviteRepository persists invites. RedeemInvite re-checks the invite
// inside its transaction, so concurrent redemptions never exceed the use
// limit.
type GroupInviteRepository interface {
	// CreateInvite returns ErrGroupInviteTaken when the code exists.
	CreateInvite(ctx context.Context, invite GroupInvite) (GroupInvite, error)
	ListInvites(ctx context.Context, groupID int64) ([]GroupInviteListing, error)
	// FindInvite returns ErrGroupInviteNotFound outside the group.
	FindInvite(ctx context.Context, groupID, inviteID int64) (GroupInvite, error)
	FindInviteByCode(ctx context.Context, code string) (GroupInvite, error)
	// RevokeInvite marks the invite revoked; revoking twice keeps the first
	// time.
	RevokeInvite(ctx context.Context, groupID, inviteID, revokedAt int64) (GroupInvite, error)
	// RedeemInvite adds the membership, role, and team entry the invite
	// grants and counts one use when anything changed. It returns
	// ErrInviteRevoked, ErrInviteExpired, or ErrInviteUsedUp when the invite
	// stopped being active.
	RedeemInvite(ctx context.Context, inviteID, userID, now int64) (InviteRedemption, error)
}

// InviteService owns invite creation, listing, revocation, and redemption.
type InviteService struct {
	invites GroupInviteRepository
	teams   TeamRepository
	users   UserRepository
	auditor AuditRecorder
	now     func() time.Time
	newCode func() (string, error)
}

// NewInviteService constructs invite use cases with explicit dependencies.
func NewInviteService(invites GroupInviteRepository, teams TeamRepository, users UserRepository) (*InviteService, error) {
	if invites == nil {
		return nil, ErrNilInviteRepository
	}
	if teams == nil {
		return nil, ErrNilTeamRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &InviteService{invites: invites, teams: teams, users: users, now: time.Now, newCode: newGroupInviteCode}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *InviteService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Now returns the service clock in Unix seconds, so views compute invite
// states with the same time as redemption.
func (s *InviteService) Now() int64 {
	if s == nil || s.now == nil {
		return time.Now().Unix()
	}
	return s.now().Unix()
}

// List returns the invites the account may manage: all of them for admins
// and managers (admin invites only for admins), and the player and parent
// invites of their teams for coaches.
func (s *InviteService) List(ctx context.Context, access *GroupAccess) ([]GroupInviteListing, error) {
	if err := s.authorize(ctx, GroupActor{Access: access}, access.CanManageInvites()); err != nil {
		return nil, err
	}
	invites, err := s.invites.ListInvites(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	visible := make([]GroupInviteListing, 0, len(invites))
	for _, listing := range invites {
		if access.CanCreateInvite(listing.Invite.Role, listing.Invite.Team()) {
			visible = append(visible, listing)
		}
	}
	return visible, nil
}

// Create adds an invite for a role and optionally a team, with an optional
// expiry and use limit.
func (s *InviteService) Create(ctx context.Context, actor GroupActor, input InviteInput) (GroupInvite, error) {
	if err := s.authorize(ctx, actor, actor.Access.CanManageInvites()); err != nil {
		return GroupInvite{}, err
	}
	if !ValidGroupRole(input.Role) {
		return GroupInvite{}, ErrInvalidGroupRole
	}
	if input.TeamID < 0 || input.ExpiresInDays < 0 || input.ExpiresInDays > MaxInviteDays || input.MaxUses < 0 || input.MaxUses > MaxInviteUses {
		return GroupInvite{}, ErrInvalidInvite
	}
	if input.TeamID > 0 && input.Role != GroupRolePlayer && input.Role != GroupRoleParent {
		return GroupInvite{}, ErrInvalidInvite
	}
	if input.TeamID > 0 && !actor.Access.CanViewTeam(input.TeamID) {
		return GroupInvite{}, ErrTeamNotFound
	}
	if !actor.Access.CanCreateInvite(input.Role, input.TeamID) {
		return GroupInvite{}, ErrGroupForbidden
	}
	invite := GroupInvite{GroupID: actor.Access.GroupID(), Role: input.Role, CreatedAt: s.now().Unix()}
	var teamName string
	if input.TeamID > 0 {
		team, err := s.teams.FindTeam(ctx, actor.Access.GroupID(), input.TeamID)
		if err != nil {
			return GroupInvite{}, err
		}
		teamID := team.ID
		invite.TeamID = &teamID
		teamName = team.Name
	}
	if userID := actor.Access.UserID(); userID > 0 {
		invite.CreatedBy = &userID
	}
	if input.ExpiresInDays > 0 {
		expires := invite.CreatedAt + int64(input.ExpiresInDays)*24*60*60
		invite.ExpiresAt = &expires
	}
	if input.MaxUses > 0 {
		maxUses := input.MaxUses
		invite.MaxUses = &maxUses
	}
	var lastErr error
	for range groupInviteCollisionTries {
		code, err := s.newCode()
		if err != nil {
			return GroupInvite{}, err
		}
		invite.Code = code
		created, err := s.invites.CreateInvite(ctx, invite)
		if err == nil {
			s.record(ctx, actor, AuditActionInviteCreated, inviteAuditDetail(created, teamName))
			return created, nil
		}
		if !errors.Is(err, ErrGroupInviteTaken) {
			return GroupInvite{}, fmt.Errorf("create invite: %w", err)
		}
		lastErr = err
	}
	return GroupInvite{}, fmt.Errorf("create invite: %w", lastErr)
}

// Revoke ends an invite at once. Accounts revoke the invites they could
// create; others get not-found.
func (s *InviteService) Revoke(ctx context.Context, actor GroupActor, inviteID int64) (GroupInvite, error) {
	if err := s.authorize(ctx, actor, actor.Access.CanManageInvites()); err != nil {
		return GroupInvite{}, err
	}
	if inviteID <= 0 {
		return GroupInvite{}, ErrGroupInviteNotFound
	}
	invite, err := s.invites.FindInvite(ctx, actor.Access.GroupID(), inviteID)
	if err != nil {
		return GroupInvite{}, err
	}
	if !actor.Access.CanCreateInvite(invite.Role, invite.Team()) {
		return GroupInvite{}, ErrGroupInviteNotFound
	}
	if invite.RevokedAt != nil {
		return invite, nil
	}
	revoked, err := s.invites.RevokeInvite(ctx, actor.Access.GroupID(), inviteID, s.now().Unix())
	if err != nil {
		if errors.Is(err, ErrGroupInviteNotFound) {
			return GroupInvite{}, err
		}
		return GroupInvite{}, fmt.Errorf("revoke invite: %w", err)
	}
	s.record(ctx, actor, AuditActionInviteRevoked, "invite "+strconv.FormatInt(revoked.ID, 10)+": "+revoked.Role)
	return revoked, nil
}

// Join redeems an invite code for userID. A new member gets the invite's
// role (and team); an existing member gets the role (and team) added. A
// guest may join only as a player or parent; profiles never join.
func (s *InviteService) Join(ctx context.Context, userID int64, code string) (InviteRedemption, error) {
	if err := s.validate(ctx); err != nil {
		return InviteRedemption{}, err
	}
	if userID <= 0 {
		return InviteRedemption{}, ErrInvalidUserID
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if normalized == "" || len(normalized) > 64 {
		return InviteRedemption{}, ErrGroupInviteNotFound
	}
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return InviteRedemption{}, fmt.Errorf("find joining account: %w", err)
	}
	if user.ID != userID || user.Kind == UserKindProfile {
		return InviteRedemption{}, ErrProfileCannotSignIn
	}
	invite, err := s.invites.FindInviteByCode(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrGroupInviteNotFound) {
			return InviteRedemption{}, err
		}
		return InviteRedemption{}, fmt.Errorf("find invite: %w", err)
	}
	now := s.now().Unix()
	if err := invite.Status(now).err(); err != nil {
		return InviteRedemption{}, err
	}
	if GroupRoleNeedsAccount(invite.Role) && !user.IsFullUser() {
		return InviteRedemption{}, ErrRoleNeedsAccount
	}
	redemption, err := s.invites.RedeemInvite(ctx, invite.ID, userID, now)
	if err != nil {
		switch {
		case errors.Is(err, ErrInviteRevoked), errors.Is(err, ErrInviteExpired), errors.Is(err, ErrInviteUsedUp), errors.Is(err, ErrGroupInviteNotFound):
			return InviteRedemption{}, err
		}
		return InviteRedemption{}, fmt.Errorf("redeem invite: %w", err)
	}
	redemption.Role = invite.Role
	return redemption, nil
}

func (s *InviteService) authorize(ctx context.Context, actor GroupActor, allowed bool) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	return authorizeGroupAction(actor, allowed)
}

func (s *InviteService) record(ctx context.Context, actor GroupActor, action, detail string) {
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func (s *InviteService) validate(ctx context.Context) error {
	if s == nil || s.invites == nil || s.teams == nil || s.users == nil || s.now == nil || s.newCode == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil invite context")
	}
	return nil
}

// inviteAuditDetail describes an invite without its code.
func inviteAuditDetail(invite GroupInvite, teamName string) string {
	detail := "invite " + strconv.FormatInt(invite.ID, 10) + ": " + invite.Role
	if invite.TeamID != nil {
		detail += "; team " + strconv.FormatInt(*invite.TeamID, 10) + ": " + teamName
	}
	if invite.MaxUses != nil {
		detail += "; max uses " + strconv.Itoa(*invite.MaxUses)
	}
	if invite.ExpiresAt != nil {
		detail += "; expires " + time.Unix(*invite.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	return detail
}

func newGroupInviteCode() (string, error) {
	code := make([]byte, groupInviteCodeLength)
	maximum := big.NewInt(int64(len(groupInviteAlphabet)))
	for index := range code {
		value, err := rand.Int(rand.Reader, maximum)
		if err != nil {
			return "", fmt.Errorf("%w: random source unavailable", ErrGroupInviteGeneration)
		}
		code[index] = groupInviteAlphabet[value.Int64()]
	}
	return string(code), nil
}
