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
	"unicode/utf8"
)

const (
	maxGroupNameLength = 64
	// GroupRoleAdmin can manage a group and its memberships.
	GroupRoleAdmin = "admin"
	// GroupRoleMember can view group details and leave voluntarily.
	GroupRoleMember           = "member"
	groupInviteCodeLength     = 12
	groupInviteAlphabet       = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	groupInviteCollisionTries = 5
)

var (
	// ErrInvalidGroup identifies missing or malformed group input.
	ErrInvalidGroup = errors.New("service: invalid group")
	// ErrInvalidGroupName identifies a group name outside storage limits.
	ErrInvalidGroupName = errors.New("service: invalid group name")
	// ErrGroupNotFound identifies a group that does not exist.
	ErrGroupNotFound = errors.New("service: group not found")
	// ErrNotGroupMember identifies an account that does not belong to a group.
	ErrNotGroupMember = errors.New("service: not a group member")
	// ErrNotGroupAdmin identifies an account without administrator rights.
	ErrNotGroupAdmin = errors.New("service: not a group administrator")
	// ErrGroupInviteNotFound identifies an unknown or rotated invite code.
	ErrGroupInviteNotFound = errors.New("service: group invite not found")
	// ErrLastGroupAdmin prevents removing or demoting a group's final administrator.
	ErrLastGroupAdmin = errors.New("service: last group administrator")
	// ErrMembershipNotFound identifies a missing group membership.
	ErrMembershipNotFound = errors.New("service: group membership not found")
	// ErrMembershipExists identifies a duplicate membership for the same account.
	ErrMembershipExists = errors.New("service: group membership exists")
	// ErrGroupInviteTaken identifies an invite code already used by another group.
	ErrGroupInviteTaken = errors.New("service: group invite already in use")
	// ErrNilGroupRepository identifies missing group persistence.
	ErrNilGroupRepository = errors.New("service: nil group repository")
	// ErrNilMembershipRepository identifies missing membership persistence.
	ErrNilMembershipRepository = errors.New("service: nil membership repository")
	// ErrGroupInviteGeneration identifies unavailable secure randomness.
	ErrGroupInviteGeneration = errors.New("service: group invite generation failed")
)

// Group is a shared membership container with an optimistic-lock revision.
type Group struct {
	ID         int64
	Name       string
	NameNorm   string
	InviteCode string
	CreatedBy  *int64
	CreatedAt  int64
	Version    int64
}

// GroupMembership is one account's role within a group.
type GroupMembership struct {
	ID        int64
	GroupID   int64
	UserID    int64
	Role      string
	CreatedAt int64
	Version   int64
}

// GroupMember is a membership joined with display data for rosters.
type GroupMember struct {
	UserID      int64
	Role        string
	Version     int64
	Username    *string
	DisplayName *string
	Email       *string
}

// GroupSummary is a group with aggregate counts safe to show non-members.
type GroupSummary struct {
	Group       Group
	MemberCount int
	Role        string
}

// GroupRepository persists groups. Update requires the caller's expected
// revision so stale writes are reported as conflicts.
type GroupRepository interface {
	CreateGroup(context.Context, Group) (Group, error)
	FindGroupByID(context.Context, int64) (Group, error)
	FindGroupByInviteCode(context.Context, string) (Group, error)
	ListGroupsForUser(context.Context, int64) ([]GroupSummary, error)
	// ListAllGroupsForUser returns every group with the account's real
	// membership role when present. It backs site-administrator listing.
	ListAllGroupsForUser(context.Context, int64) ([]GroupSummary, error)
	UpdateGroup(context.Context, Group, int64) (Group, error)
}

// GroupMembershipRepository persists memberships and enforces one-membership
// uniqueness. Write methods must run as a single transaction so a group always
// keeps at least one administrator.
type GroupMembershipRepository interface {
	// CreateGroupWithAdmin inserts a group and its first administrator
	// membership atomically.
	CreateGroupWithAdmin(context.Context, Group, int64, string) (Group, GroupMembership, error)
	CreateMembership(context.Context, GroupMembership) (GroupMembership, error)
	FindMembership(context.Context, int64, int64) (GroupMembership, error)
	ListMembers(context.Context, int64) ([]GroupMember, error)
	CountMembers(context.Context, int64) (int, error)
	UpdateMembershipRole(context.Context, GroupMembership, int64) (GroupMembership, error)
	DeleteMembership(context.Context, int64, int64, int64) error
}

// GroupService owns group creation, listing, detail visibility, and name
// changes.
type GroupService struct {
	groups      GroupRepository
	memberships GroupMembershipRepository
	users       UserRepository
	auditor     AuditRecorder
}

// NewGroupService constructs group use cases with explicit dependencies.
func NewGroupService(groups GroupRepository, memberships GroupMembershipRepository, users UserRepository) (*GroupService, error) {
	if groups == nil {
		return nil, ErrNilGroupRepository
	}
	if memberships == nil {
		return nil, ErrNilMembershipRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &GroupService{groups: groups, memberships: memberships, users: users}, nil
}

// Create validates the name, generates a unique invite code, and creates the
// group with its creator as the first administrator.
func (s *GroupService) Create(ctx context.Context, userID int64, name string) (GroupSummary, error) {
	if err := s.validate(ctx); err != nil {
		return GroupSummary{}, err
	}
	if userID <= 0 {
		return GroupSummary{}, ErrInvalidUserID
	}
	normalized, err := normalizeGroupName(name)
	if err != nil {
		return GroupSummary{}, err
	}
	creator, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return GroupSummary{}, fmt.Errorf("find group creator: %w", err)
	}
	if creator.ID != userID || creator.IsGuest {
		return GroupSummary{}, ErrInvalidGroup
	}

	var lastErr error
	for attempt := 0; attempt < groupInviteCollisionTries; attempt++ {
		code, err := newGroupInviteCode()
		if err != nil {
			return GroupSummary{}, err
		}
		group := Group{Name: strings.TrimSpace(name), NameNorm: normalized, InviteCode: code, CreatedBy: &userID}
		created, membership, err := s.memberships.CreateGroupWithAdmin(ctx, group, userID, GroupRoleAdmin)
		if err == nil {
			if err := validateCreatedGroup(created, membership, userID); err != nil {
				return GroupSummary{}, err
			}
			return GroupSummary{Group: created, MemberCount: 1, Role: GroupRoleAdmin}, nil
		}
		if !errors.Is(err, ErrGroupInviteTaken) {
			return GroupSummary{}, fmt.Errorf("create group: %w", err)
		}
		lastErr = err
	}
	return GroupSummary{}, fmt.Errorf("create group: %w", lastErr)
}

// List returns the groups the account belongs to with member counts and roles.
// A site administrator sees every group; roles still reflect real memberships
// so the list never presents superuser access as a group role.
func (s *GroupService) List(ctx context.Context, userID int64) ([]GroupSummary, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrInvalidUserID
	}
	administrator, err := s.actorIsGlobalAdmin(ctx, userID)
	if err != nil {
		return nil, err
	}
	var summaries []GroupSummary
	if administrator {
		summaries, err = s.groups.ListAllGroupsForUser(ctx, userID)
	} else {
		summaries, err = s.groups.ListGroupsForUser(ctx, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	for index := range summaries {
		if summaries[index].MemberCount < 1 {
			summaries[index].MemberCount = 1
		}
	}
	return summaries, nil
}

// Detail returns the full group roster to members and to site administrators.
// Other non-members receive a not-found outcome so the group's private details
// stay hidden. The current invite code is withheld from ordinary members.
func (s *GroupService) Detail(ctx context.Context, userID, groupID int64) (GroupDetail, error) {
	if err := s.validate(ctx); err != nil {
		return GroupDetail{}, err
	}
	if userID <= 0 || groupID <= 0 {
		return GroupDetail{}, ErrInvalidGroup
	}
	membership, membershipErr := s.memberships.FindMembership(ctx, groupID, userID)
	isMember := membershipErr == nil
	if membershipErr != nil && !errors.Is(membershipErr, ErrMembershipNotFound) {
		return GroupDetail{}, fmt.Errorf("find group membership: %w", membershipErr)
	}
	administrator, err := s.actorIsGlobalAdmin(ctx, userID)
	if err != nil {
		return GroupDetail{}, err
	}
	if !isMember && !administrator {
		return GroupDetail{}, ErrGroupNotFound
	}
	group, err := s.groups.FindGroupByID(ctx, groupID)
	if err != nil {
		return GroupDetail{}, fmt.Errorf("find group: %w", err)
	}
	count, err := s.memberships.CountMembers(ctx, groupID)
	if err != nil {
		return GroupDetail{}, fmt.Errorf("count group members: %w", err)
	}
	viewerRole := ""
	if isMember {
		viewerRole = membership.Role
	}
	detail := GroupDetail{
		Group:       group,
		MemberCount: count,
		ViewerRole:  viewerRole,
		IsMember:    isMember,
		// A site administrator holds administrator rights without a group
		// membership, so the roster and invite code stay fully available.
		IsAdmin: administrator || viewerRole == GroupRoleAdmin,
	}
	if detail.IsAdmin {
		detail.InviteCode = group.InviteCode
	}
	members, err := s.memberships.ListMembers(ctx, groupID)
	if err != nil {
		return GroupDetail{}, fmt.Errorf("list group members: %w", err)
	}
	// Anyone with the shareable invite code can join, so ordinary members see
	// only names; email addresses stay with group administrators.
	if !detail.IsAdmin {
		for i := range members {
			if members[i].UserID != userID {
				members[i].Email = nil
			}
		}
	}
	detail.Members = members
	return detail, nil
}

// Rename changes a group name using the caller's expected revision. Only
// administrators may rename.
func (s *GroupService) Rename(ctx context.Context, userID, groupID, expectedVersion int64, name string) (Group, error) {
	if err := s.validate(ctx); err != nil {
		return Group{}, err
	}
	if userID <= 0 || groupID <= 0 {
		return Group{}, ErrInvalidGroup
	}
	if expectedVersion < 0 {
		return Group{}, ErrInvalidGroup
	}
	normalized, err := normalizeGroupName(name)
	if err != nil {
		return Group{}, err
	}
	asSiteAdmin, err := s.requireAdmin(ctx, userID, groupID)
	if err != nil {
		return Group{}, err
	}
	group, err := s.groups.FindGroupByID(ctx, groupID)
	if err != nil {
		return Group{}, fmt.Errorf("find group for rename: %w", err)
	}
	group.Name = strings.TrimSpace(name)
	group.NameNorm = normalized
	updated, err := s.groups.UpdateGroup(ctx, group, expectedVersion)
	if err != nil {
		if errors.Is(err, ErrOptimisticLockConflict) || errors.Is(err, ErrGroupNotFound) {
			return Group{}, err
		}
		return Group{}, fmt.Errorf("rename group: %w", err)
	}
	if asSiteAdmin {
		s.recordSiteAdminAction(ctx, userID, groupID, AuditActionGroupRenamed, "name: "+updated.Name)
	}
	return updated, nil
}

// RotateInvite replaces a group's invite code and immediately invalidates the
// previous code. Only administrators may rotate.
func (s *GroupService) RotateInvite(ctx context.Context, userID, groupID int64) (string, error) {
	if err := s.validate(ctx); err != nil {
		return "", err
	}
	if userID <= 0 || groupID <= 0 {
		return "", ErrInvalidGroup
	}
	asSiteAdmin, err := s.requireAdmin(ctx, userID, groupID)
	if err != nil {
		return "", err
	}
	group, err := s.groups.FindGroupByID(ctx, groupID)
	if err != nil {
		return "", fmt.Errorf("find group for invite rotation: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < groupInviteCollisionTries; attempt++ {
		code, err := newGroupInviteCode()
		if err != nil {
			return "", err
		}
		group.InviteCode = code
		updated, err := s.groups.UpdateGroup(ctx, group, group.Version)
		if err == nil {
			if asSiteAdmin {
				s.recordSiteAdminAction(ctx, userID, groupID, AuditActionGroupInvite, "")
			}
			return updated.InviteCode, nil
		}
		if errors.Is(err, ErrGroupInviteTaken) {
			lastErr = err
			continue
		}
		if errors.Is(err, ErrOptimisticLockConflict) || errors.Is(err, ErrGroupNotFound) {
			return "", err
		}
		return "", fmt.Errorf("rotate group invite: %w", err)
	}
	return "", fmt.Errorf("rotate group invite: %w", lastErr)
}

// Join redeems an invite code as an ordinary member. Redemption is idempotent:
// an existing member keeps their role instead of gaining a duplicate row.
func (s *GroupService) Join(ctx context.Context, userID int64, code string) (GroupMembership, error) {
	if err := s.validate(ctx); err != nil {
		return GroupMembership{}, err
	}
	if userID <= 0 {
		return GroupMembership{}, ErrInvalidUserID
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if normalized == "" {
		return GroupMembership{}, ErrGroupInviteNotFound
	}
	group, err := s.groups.FindGroupByInviteCode(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			return GroupMembership{}, ErrGroupInviteNotFound
		}
		return GroupMembership{}, fmt.Errorf("find invite group: %w", err)
	}
	existing, err := s.memberships.FindMembership(ctx, group.ID, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrMembershipNotFound) {
		return GroupMembership{}, fmt.Errorf("find join membership: %w", err)
	}
	created, err := s.memberships.CreateMembership(ctx, GroupMembership{
		GroupID:   group.ID,
		UserID:    userID,
		Role:      GroupRoleMember,
		CreatedAt: time.Now().Unix(),
		Version:   0,
	})
	if err != nil {
		if errors.Is(err, ErrMembershipExists) {
			existing, lookupErr := s.memberships.FindMembership(ctx, group.ID, userID)
			if lookupErr == nil {
				return existing, nil
			}
		}
		return GroupMembership{}, fmt.Errorf("create join membership: %w", err)
	}
	return created, nil
}

// ChangeRole promotes or demotes a member. Only administrators may change
// roles and the last administrator can never be demoted.
func (s *GroupService) ChangeRole(ctx context.Context, actorUserID, groupID, targetUserID, expectedVersion int64, role string) (GroupMembership, error) {
	if err := s.validate(ctx); err != nil {
		return GroupMembership{}, err
	}
	if actorUserID <= 0 || groupID <= 0 || targetUserID <= 0 || expectedVersion < 0 {
		return GroupMembership{}, ErrInvalidGroup
	}
	if role != GroupRoleAdmin && role != GroupRoleMember {
		return GroupMembership{}, ErrInvalidGroup
	}
	asSiteAdmin, err := s.requireAdmin(ctx, actorUserID, groupID)
	if err != nil {
		return GroupMembership{}, err
	}
	target, err := s.memberships.FindMembership(ctx, groupID, targetUserID)
	if err != nil {
		if errors.Is(err, ErrMembershipNotFound) {
			return GroupMembership{}, ErrMembershipNotFound
		}
		return GroupMembership{}, fmt.Errorf("find role target: %w", err)
	}
	if target.Role == role {
		return target, nil
	}
	target.Role = role
	if _, err := s.memberships.UpdateMembershipRole(ctx, target, expectedVersion); err != nil {
		if errors.Is(err, ErrLastGroupAdmin) || errors.Is(err, ErrOptimisticLockConflict) || errors.Is(err, ErrMembershipNotFound) {
			return GroupMembership{}, err
		}
		return GroupMembership{}, fmt.Errorf("change group role: %w", err)
	}
	updated, err := s.memberships.FindMembership(ctx, groupID, targetUserID)
	if err != nil {
		return GroupMembership{}, fmt.Errorf("reread group role: %w", err)
	}
	if asSiteAdmin {
		s.recordSiteAdminAction(ctx, actorUserID, groupID, AuditActionGroupRole, "member "+strconv.FormatInt(targetUserID, 10)+": "+role)
	}
	return updated, nil
}

// RemoveMember removes another account from a group. Only administrators may
// remove members and the last administrator can never be removed.
func (s *GroupService) RemoveMember(ctx context.Context, actorUserID, groupID, targetUserID, expectedVersion int64) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if actorUserID <= 0 || groupID <= 0 || targetUserID <= 0 || expectedVersion < 0 {
		return ErrInvalidGroup
	}
	if actorUserID == targetUserID {
		return ErrInvalidGroup
	}
	asSiteAdmin, err := s.requireAdmin(ctx, actorUserID, groupID)
	if err != nil {
		return err
	}
	if _, err := s.memberships.FindMembership(ctx, groupID, targetUserID); err != nil {
		if errors.Is(err, ErrMembershipNotFound) {
			return ErrMembershipNotFound
		}
		return fmt.Errorf("find removal target: %w", err)
	}
	if err := s.memberships.DeleteMembership(ctx, groupID, targetUserID, expectedVersion); err != nil {
		if errors.Is(err, ErrLastGroupAdmin) || errors.Is(err, ErrOptimisticLockConflict) || errors.Is(err, ErrMembershipNotFound) {
			return err
		}
		return fmt.Errorf("remove group member: %w", err)
	}
	if asSiteAdmin {
		s.recordSiteAdminAction(ctx, actorUserID, groupID, AuditActionGroupRemoved, "member "+strconv.FormatInt(targetUserID, 10))
	}
	return nil
}

// Leave removes the acting account from a group. The last administrator cannot
// leave until another administrator exists.
func (s *GroupService) Leave(ctx context.Context, userID, groupID int64) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if userID <= 0 || groupID <= 0 {
		return ErrInvalidGroup
	}
	membership, err := s.memberships.FindMembership(ctx, groupID, userID)
	if err != nil {
		if errors.Is(err, ErrMembershipNotFound) {
			return ErrMembershipNotFound
		}
		return fmt.Errorf("find leave membership: %w", err)
	}
	if err := s.memberships.DeleteMembership(ctx, groupID, userID, membership.Version); err != nil {
		if errors.Is(err, ErrLastGroupAdmin) || errors.Is(err, ErrOptimisticLockConflict) || errors.Is(err, ErrMembershipNotFound) {
			return err
		}
		return fmt.Errorf("leave group: %w", err)
	}
	return nil
}

// requireAdmin authorizes a group management action. Group administrators and
// site administrators both pass; the returned membership is zero for a site
// administrator acting without a group membership.
// requireAdmin authorizes a group-administrator action. It reports whether
// the actor relies on site-administrator rights rather than group membership,
// in which case the completed action must be audited.
func (s *GroupService) requireAdmin(ctx context.Context, userID, groupID int64) (bool, error) {
	membership, err := s.memberships.FindMembership(ctx, groupID, userID)
	if err != nil && !errors.Is(err, ErrMembershipNotFound) {
		return false, fmt.Errorf("find group admin: %w", err)
	}
	if err == nil && membership.Role == GroupRoleAdmin {
		return false, nil
	}
	administrator, err := s.actorIsGlobalAdmin(ctx, userID)
	if err != nil {
		return false, err
	}
	if !administrator {
		return false, ErrNotGroupAdmin
	}
	return true, nil
}

// SetAuditor attaches the administrator audit trail. It is intended to be
// called once during startup.
func (s *GroupService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// recordSiteAdminAction audits a group change made by a site administrator who
// is not a group administrator. Audit failures reach the security log through
// the audit service and never undo the completed change.
func (s *GroupService) recordSiteAdminAction(ctx context.Context, actorUserID, groupID int64, action, detail string) {
	if s.auditor == nil {
		return
	}
	record := AuditRecord{ActorUserID: actorUserID, Action: action, TargetType: "group", TargetID: strconv.FormatInt(groupID, 10), Detail: detail}
	if actor, err := s.users.FindUserByID(ctx, actorUserID); err == nil && actor.Email != nil {
		record.ActorEmail = *actor.Email
	}
	_ = s.auditor.Record(ctx, record)
}

// actorIsGlobalAdmin reports whether the account is a non-guest site
// administrator with management rights over every group.
func (s *GroupService) actorIsGlobalAdmin(ctx context.Context, userID int64) (bool, error) {
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("find group actor: %w", err)
	}
	return user.IsAdmin && !user.IsGuest, nil
}

func (s *GroupService) validate(ctx context.Context) error {
	if s == nil || s.groups == nil || s.memberships == nil || s.users == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil group context")
	}
	return nil
}

func normalizeGroupName(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > maxGroupNameLength {
		return "", ErrInvalidGroupName
	}
	for _, character := range trimmed {
		if character < 0x20 || character == 0x7f {
			return "", ErrInvalidGroupName
		}
	}
	return strings.ToLower(trimmed), nil
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

func validateCreatedGroup(group Group, membership GroupMembership, userID int64) error {
	if group.ID <= 0 || group.Version < 0 || len(group.InviteCode) != groupInviteCodeLength {
		return ErrInvalidGroup
	}
	if membership.GroupID != group.ID || membership.UserID != userID || membership.Role != GroupRoleAdmin {
		return ErrInvalidGroup
	}
	return nil
}

// GroupDetail contains group data plus the roster that only members may see.
type GroupDetail struct {
	Group       Group
	MemberCount int
	ViewerRole  string
	IsMember    bool
	IsAdmin     bool
	InviteCode  string
	Members     []GroupMember
}
