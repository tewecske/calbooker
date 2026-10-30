package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// slugCreateAttempts bounds retries when two requests pick the same slug.
const slugCreateAttempts = 3

const (
	maxGroupNameLength = 64
	maxTimeZoneLength  = 64
	// DefaultGroupTimeZone is the time zone of a new group unless its creator
	// picks another.
	DefaultGroupTimeZone = "Europe/Budapest"
)

var (
	// ErrInvalidGroup identifies missing or malformed group input.
	ErrInvalidGroup = errors.New("service: invalid group")
	// ErrInvalidGroupName identifies a group name outside storage limits.
	ErrInvalidGroupName = errors.New("service: invalid group name")
	// ErrInvalidTimeZone identifies a time zone that is not an IANA name.
	ErrInvalidTimeZone = errors.New("service: invalid time zone")
	// ErrGroupNotFound identifies a group that does not exist or that the
	// account may not see.
	ErrGroupNotFound = errors.New("service: group not found")
	// ErrGroupForbidden identifies a member whose roles do not allow the action.
	ErrGroupForbidden = errors.New("service: group action not allowed")
	// ErrGroupRequiresAccount identifies a guest trying to create a group.
	ErrGroupRequiresAccount = errors.New("service: creating a group needs a full account")
	// ErrInvalidGroupRole identifies an unknown group role.
	ErrInvalidGroupRole = errors.New("service: invalid group role")
	// ErrRoleNeedsAccount identifies a staff role offered to a guest or profile.
	ErrRoleNeedsAccount = errors.New("service: role needs a full account")
	// ErrLastGroupAdmin prevents removing a group's final administrator.
	ErrLastGroupAdmin = errors.New("service: last group administrator")
	// ErrMembershipNotFound identifies a missing group membership.
	ErrMembershipNotFound = errors.New("service: group membership not found")
	// ErrMembershipExists identifies a duplicate membership for the same account.
	ErrMembershipExists = errors.New("service: group membership exists")
	// ErrNilGroupRepository identifies missing group persistence.
	ErrNilGroupRepository = errors.New("service: nil group repository")
	// ErrNilMembershipRepository identifies missing membership persistence.
	ErrNilMembershipRepository = errors.New("service: nil membership repository")
)

// Stable group audit action codes. Every admin and manager action in a group
// is recorded with the group as target.
const (
	AuditActionGroupCreated     = "group.created"
	AuditActionGroupSettings    = "group.settings_updated"
	AuditActionGroupRoleAdded   = "group.role_added"
	AuditActionGroupRoleRemoved = "group.role_removed"
	AuditActionGroupRemoved     = "group.member_removed"
	AuditActionTeamCreated      = "group.team_created"
	AuditActionTeamUpdated      = "group.team_updated"
	AuditActionTeamArchived     = "group.team_archived"
	AuditActionTeamRestored     = "group.team_restored"
	AuditActionRosterAdded      = "group.roster_added"
	AuditActionRosterRemoved    = "group.roster_removed"
)

// Group is a club with an optimistic-lock revision.
type Group struct {
	ID   int64
	Name string
	// Slug addresses the group in URLs; unique site-wide.
	Slug      string
	NameNorm  string
	TimeZone  string
	CreatedBy *int64
	CreatedAt int64
	Version   int64
}

// GroupMembership is one account's membership and roles within a group.
// Version changes with every role change.
type GroupMembership struct {
	ID        int64
	GroupID   int64
	UserID    int64
	Roles     []string
	CreatedAt int64
	Version   int64
}

// GroupMember is a membership joined with display data for member lists.
type GroupMember struct {
	UserID      int64
	Roles       []string
	Version     int64
	Kind        UserKind
	Username    *string
	DisplayName *string
	Email       *string
	// TeamIDs are the teams the member is on as a player or parent.
	TeamIDs []int64
}

// HasRole reports whether the member holds role.
func (m GroupMember) HasRole(role string) bool {
	return slices.Contains(m.Roles, role)
}

// GroupSummary is one row of the account's group list.
type GroupSummary struct {
	Group       Group
	MemberCount int
	// Roles are the account's roles; empty for a site administrator who is
	// not a member.
	Roles []string
}

// GroupOverview is what a member sees on the group's main page.
type GroupOverview struct {
	Group       Group
	MemberCount int
}

// GroupActor is the account acting in a group: its access, and the client
// origin recorded with audit entries.
type GroupActor struct {
	Access *GroupAccess
	Origin string
}

// GroupRepository persists groups. Update requires the caller's expected
// revision so stale writes are reported as conflicts.
type GroupRepository interface {
	FindGroupByID(context.Context, int64) (Group, error)
	// FindGroupBySlug returns ErrGroupNotFound for an unknown slug.
	FindGroupBySlug(context.Context, string) (Group, error)
	// GroupSlugTaken reports whether a group other than exceptID uses slug.
	GroupSlugTaken(ctx context.Context, slug string, exceptID int64) (bool, error)
	ListGroupsForUser(context.Context, int64) ([]GroupSummary, error)
	// ListAllGroupsForUser returns every group with the account's real
	// roles when present. It backs site-administrator listing.
	ListAllGroupsForUser(context.Context, int64) ([]GroupSummary, error)
	UpdateGroup(context.Context, Group, int64) (Group, error)
}

// GroupMembershipRepository persists memberships and roles. Role and member
// removal run in one transaction that keeps at least one administrator.
type GroupMembershipRepository interface {
	// CreateGroupWithAdmin inserts a group and its creator as administrator
	// atomically.
	CreateGroupWithAdmin(context.Context, Group, int64) (Group, GroupMembership, error)
	// CreateMembership adds a member with roles.
	CreateMembership(context.Context, GroupMembership) (GroupMembership, error)
	FindMembership(context.Context, int64, int64) (GroupMembership, error)
	ListMembers(context.Context, int64) ([]GroupMember, error)
	CountMembers(context.Context, int64) (int, error)
	// AddMemberRole adds role at the expected membership revision.
	AddMemberRole(ctx context.Context, groupID, userID int64, role string, expectedVersion int64) (GroupMembership, error)
	// RemoveMemberRole removes role at the expected membership revision and
	// refuses to remove the last administrator.
	RemoveMemberRole(ctx context.Context, groupID, userID int64, role string, expectedVersion int64) (GroupMembership, error)
	// DeleteMembership removes a member at the expected revision and refuses
	// to remove the last administrator.
	DeleteMembership(ctx context.Context, groupID, userID, expectedVersion int64) error
}

// GroupService owns group creation, listing, settings, and membership
// roles. Invites live in InviteService.
type GroupService struct {
	groups      GroupRepository
	memberships GroupMembershipRepository
	users       UserRepository
	auditor     AuditRecorder
	now         func() time.Time
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
	return &GroupService{groups: groups, memberships: memberships, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *GroupService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Create validates the name and time zone and creates the group with its
// creator as the first administrator. Only full users create groups.
func (s *GroupService) Create(ctx context.Context, userID int64, origin, name, timeZone string) (GroupSummary, error) {
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
	zone := DefaultGroupTimeZone
	if strings.TrimSpace(timeZone) != "" {
		if zone, err = NormalizeTimeZone(timeZone); err != nil {
			return GroupSummary{}, err
		}
	}
	creator, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return GroupSummary{}, fmt.Errorf("find group creator: %w", err)
	}
	if creator.ID != userID {
		return GroupSummary{}, ErrInvalidGroup
	}
	if !creator.IsFullUser() {
		return GroupSummary{}, ErrGroupRequiresAccount
	}

	var (
		created    Group
		membership GroupMembership
		inserted   bool
	)
	for attempt := 0; attempt < slugCreateAttempts; attempt++ {
		slug, err := uniqueSlug(name, "group", func(candidate string) (bool, error) {
			return s.groups.GroupSlugTaken(ctx, candidate, 0)
		})
		if err != nil {
			return GroupSummary{}, fmt.Errorf("find group slug: %w", err)
		}
		group := Group{Name: strings.TrimSpace(name), Slug: slug, NameNorm: normalized, TimeZone: zone, CreatedBy: &userID, CreatedAt: s.now().Unix()}
		created, membership, err = s.memberships.CreateGroupWithAdmin(ctx, group, userID)
		if errors.Is(err, ErrSlugTaken) {
			continue
		}
		if err != nil {
			return GroupSummary{}, fmt.Errorf("create group: %w", err)
		}
		inserted = true
		break
	}
	if !inserted {
		return GroupSummary{}, ErrSlugTaken
	}
	if err := validateCreatedGroup(created, membership, userID); err != nil {
		return GroupSummary{}, err
	}
	s.record(ctx, userID, origin, created.ID, AuditActionGroupCreated, "name: "+created.Name+"; slug: "+created.Slug+"; time zone: "+created.TimeZone)
	return GroupSummary{Group: created, MemberCount: 1, Roles: []string{GroupRoleAdmin}}, nil
}

// List returns the groups the account belongs to with member counts and
// roles. A site administrator sees every group; roles still reflect real
// memberships.
func (s *GroupService) List(ctx context.Context, userID int64) ([]GroupSummary, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrInvalidUserID
	}
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find group actor: %w", err)
	}
	var summaries []GroupSummary
	if user.IsAdmin && user.IsFullUser() {
		summaries, err = s.groups.ListAllGroupsForUser(ctx, userID)
	} else {
		summaries, err = s.groups.ListGroupsForUser(ctx, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	for index := range summaries {
		summaries[index].Roles = sortGroupRoles(summaries[index].Roles)
	}
	return summaries, nil
}

// Overview returns the group's main page data.
func (s *GroupService) Overview(ctx context.Context, access *GroupAccess) (GroupOverview, error) {
	if err := s.validate(ctx); err != nil {
		return GroupOverview{}, err
	}
	if !access.CanView() {
		return GroupOverview{}, ErrGroupNotFound
	}
	group, err := s.groups.FindGroupByID(ctx, access.GroupID())
	if err != nil {
		return GroupOverview{}, err
	}
	count, err := s.memberships.CountMembers(ctx, group.ID)
	if err != nil {
		return GroupOverview{}, fmt.Errorf("count group members: %w", err)
	}
	return GroupOverview{Group: group, MemberCount: count}, nil
}

// Group returns the group's stored settings to anyone who may see it.
func (s *GroupService) Group(ctx context.Context, access *GroupAccess) (Group, error) {
	if err := s.validate(ctx); err != nil {
		return Group{}, err
	}
	if !access.CanView() {
		return Group{}, ErrGroupNotFound
	}
	return s.groups.FindGroupByID(ctx, access.GroupID())
}

// UpdateSettings changes the group's name and time zone at the caller's
// expected revision. Admins and managers only.
func (s *GroupService) UpdateSettings(ctx context.Context, actor GroupActor, expectedVersion int64, name, slug, timeZone string) (Group, error) {
	if err := s.authorize(ctx, actor, actor.Access.CanManageGroup()); err != nil {
		return Group{}, err
	}
	if expectedVersion < 0 {
		return Group{}, ErrInvalidGroup
	}
	normalized, err := normalizeGroupName(name)
	if err != nil {
		return Group{}, err
	}
	zone, err := NormalizeTimeZone(timeZone)
	if err != nil {
		return Group{}, err
	}
	group, err := s.groups.FindGroupByID(ctx, actor.Access.GroupID())
	if err != nil {
		return Group{}, err
	}
	if strings.TrimSpace(slug) != "" {
		if group.Slug, err = normalizeSlugInput(slug); err != nil {
			return Group{}, err
		}
		taken, err := s.groups.GroupSlugTaken(ctx, group.Slug, group.ID)
		if err != nil {
			return Group{}, fmt.Errorf("check group slug: %w", err)
		}
		if taken {
			return Group{}, ErrSlugTaken
		}
	}
	group.Name = strings.TrimSpace(name)
	group.NameNorm = normalized
	group.TimeZone = zone
	updated, err := s.groups.UpdateGroup(ctx, group, expectedVersion)
	if err != nil {
		if IsWriteConflict(err) || errors.Is(err, ErrGroupNotFound) || errors.Is(err, ErrSlugTaken) {
			return Group{}, err
		}
		return Group{}, fmt.Errorf("update group settings: %w", err)
	}
	s.recordActor(ctx, actor, AuditActionGroupSettings, "name: "+updated.Name+"; slug: "+updated.Slug+"; time zone: "+updated.TimeZone)
	return updated, nil
}

// BySlug resolves a group slug to the group. It does not check access: the
// caller builds the account's GroupAccess for the returned ID, which answers
// ErrGroupNotFound to accounts that may not see the group.
func (s *GroupService) BySlug(ctx context.Context, slug string) (Group, error) {
	if err := s.validate(ctx); err != nil {
		return Group{}, err
	}
	if ValidateSlug(slug) != nil {
		return Group{}, ErrGroupNotFound
	}
	return s.groups.FindGroupBySlug(ctx, slug)
}

// Leave removes the acting member from the group. The last administrator
// cannot leave until another administrator exists.
func (s *GroupService) Leave(ctx context.Context, access *GroupAccess) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if !access.CanView() {
		return ErrGroupNotFound
	}
	if !access.IsMember() {
		return ErrMembershipNotFound
	}
	membership, err := s.memberships.FindMembership(ctx, access.GroupID(), access.UserID())
	if err != nil {
		return err
	}
	if err := s.memberships.DeleteMembership(ctx, access.GroupID(), access.UserID(), membership.Version); err != nil {
		if errors.Is(err, ErrLastGroupAdmin) || IsWriteConflict(err) || errors.Is(err, ErrMembershipNotFound) {
			return err
		}
		return fmt.Errorf("leave group: %w", err)
	}
	return nil
}

// Members returns the members the account may see. Admins and managers see
// everyone with email addresses. Others see themselves and the members of
// their visible teams by name; coaches also see their teams' emails.
func (s *GroupService) Members(ctx context.Context, access *GroupAccess) ([]GroupMember, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	members, err := s.memberships.ListMembers(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	if access.CanViewAllMembers() {
		return members, nil
	}
	visibleTeams := access.VisibleTeamIDs()
	visible := make([]GroupMember, 0, len(members))
	for _, member := range members {
		self := member.UserID == access.UserID()
		shared := make([]int64, 0, len(member.TeamIDs))
		for _, teamID := range member.TeamIDs {
			if slices.Contains(visibleTeams, teamID) {
				shared = append(shared, teamID)
			}
		}
		if !self && len(shared) == 0 {
			continue
		}
		emailVisible := self
		for _, teamID := range shared {
			if access.CanViewTeamEmails(teamID) {
				emailVisible = true
			}
		}
		if !emailVisible {
			member.Email = nil
		}
		member.TeamIDs = shared
		visible = append(visible, member)
	}
	return visible, nil
}

// AddRole grants role to a member at the expected membership revision. Admin,
// manager, and coach need a full account; only admins grant admin.
func (s *GroupService) AddRole(ctx context.Context, actor GroupActor, targetUserID, expectedVersion int64, role string) (GroupMembership, error) {
	if !ValidGroupRole(role) {
		if err := s.authorize(ctx, actor, actor.Access.CanManageGroup()); err != nil {
			return GroupMembership{}, err
		}
		return GroupMembership{}, ErrInvalidGroupRole
	}
	if err := s.authorize(ctx, actor, actor.Access.CanManageRole(role)); err != nil {
		return GroupMembership{}, err
	}
	if targetUserID <= 0 || expectedVersion < 0 {
		return GroupMembership{}, ErrInvalidGroup
	}
	target, err := s.memberships.FindMembership(ctx, actor.Access.GroupID(), targetUserID)
	if err != nil {
		return GroupMembership{}, err
	}
	if slices.Contains(target.Roles, role) {
		return target, nil
	}
	if role != GroupRolePlayer {
		user, err := s.users.FindUserByID(ctx, targetUserID)
		if err != nil {
			return GroupMembership{}, fmt.Errorf("find role target: %w", err)
		}
		// Staff roles need a full account; a child profile is only ever a
		// player.
		if (GroupRoleNeedsAccount(role) && !user.IsFullUser()) || user.Kind == UserKindProfile {
			return GroupMembership{}, ErrRoleNeedsAccount
		}
	}
	updated, err := s.memberships.AddMemberRole(ctx, actor.Access.GroupID(), targetUserID, role, expectedVersion)
	if err != nil {
		if IsWriteConflict(err) || errors.Is(err, ErrMembershipNotFound) {
			return GroupMembership{}, err
		}
		return GroupMembership{}, fmt.Errorf("add group role: %w", err)
	}
	s.recordActor(ctx, actor, AuditActionGroupRoleAdded, "member "+strconv.FormatInt(targetUserID, 10)+": "+role)
	return updated, nil
}

// RemoveRole takes role from a member at the expected membership revision.
// The last administrator keeps the admin role.
func (s *GroupService) RemoveRole(ctx context.Context, actor GroupActor, targetUserID, expectedVersion int64, role string) (GroupMembership, error) {
	if !ValidGroupRole(role) {
		if err := s.authorize(ctx, actor, actor.Access.CanManageGroup()); err != nil {
			return GroupMembership{}, err
		}
		return GroupMembership{}, ErrInvalidGroupRole
	}
	if err := s.authorize(ctx, actor, actor.Access.CanManageRole(role)); err != nil {
		return GroupMembership{}, err
	}
	if targetUserID <= 0 || expectedVersion < 0 {
		return GroupMembership{}, ErrInvalidGroup
	}
	target, err := s.memberships.FindMembership(ctx, actor.Access.GroupID(), targetUserID)
	if err != nil {
		return GroupMembership{}, err
	}
	if !slices.Contains(target.Roles, role) {
		return target, nil
	}
	updated, err := s.memberships.RemoveMemberRole(ctx, actor.Access.GroupID(), targetUserID, role, expectedVersion)
	if err != nil {
		if errors.Is(err, ErrLastGroupAdmin) || IsWriteConflict(err) || errors.Is(err, ErrMembershipNotFound) {
			return GroupMembership{}, err
		}
		return GroupMembership{}, fmt.Errorf("remove group role: %w", err)
	}
	s.recordActor(ctx, actor, AuditActionGroupRoleRemoved, "member "+strconv.FormatInt(targetUserID, 10)+": "+role)
	return updated, nil
}

// RemoveMember removes another member from the group. Removing an
// administrator needs the right to manage the admin role, and the last
// administrator is never removed.
func (s *GroupService) RemoveMember(ctx context.Context, actor GroupActor, targetUserID, expectedVersion int64) error {
	if err := s.authorize(ctx, actor, actor.Access.CanRemoveMember()); err != nil {
		return err
	}
	if targetUserID <= 0 || expectedVersion < 0 || targetUserID == actor.Access.UserID() {
		return ErrInvalidGroup
	}
	target, err := s.memberships.FindMembership(ctx, actor.Access.GroupID(), targetUserID)
	if err != nil {
		return err
	}
	if slices.Contains(target.Roles, GroupRoleAdmin) && !actor.Access.CanManageRole(GroupRoleAdmin) {
		return ErrGroupForbidden
	}
	user, err := s.users.FindUserByID(ctx, targetUserID)
	if err != nil {
		return fmt.Errorf("find removed member: %w", err)
	}
	if user.Kind == UserKindProfile {
		return ErrProfileMembership
	}
	if err := s.memberships.DeleteMembership(ctx, actor.Access.GroupID(), targetUserID, expectedVersion); err != nil {
		if errors.Is(err, ErrLastGroupAdmin) || IsWriteConflict(err) || errors.Is(err, ErrMembershipNotFound) {
			return err
		}
		return fmt.Errorf("remove group member: %w", err)
	}
	s.recordActor(ctx, actor, AuditActionGroupRemoved, "member "+strconv.FormatInt(targetUserID, 10))
	return nil
}

// authorize answers not-found to accounts that may not see the group and
// forbidden to members whose roles do not allow the action.
func (s *GroupService) authorize(ctx context.Context, actor GroupActor, allowed bool) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	return authorizeGroupAction(actor, allowed)
}

func authorizeGroupAction(actor GroupActor, allowed bool) error {
	if !actor.Access.CanView() {
		return ErrGroupNotFound
	}
	if !allowed {
		return ErrGroupForbidden
	}
	return nil
}

func (s *GroupService) recordActor(ctx context.Context, actor GroupActor, action, detail string) {
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail = strings.TrimSpace(detail + " (site administrator)")
	}
	s.record(ctx, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

// record stores one group audit entry with a snapshot of the actor's email.
// Audit failures reach the security log through the audit service and never
// undo the completed change.
func (s *GroupService) record(ctx context.Context, actorUserID int64, origin string, groupID int64, action, detail string) {
	recordGroupAudit(ctx, s.auditor, s.users, actorUserID, origin, groupID, action, detail)
}

func recordGroupAudit(ctx context.Context, auditor AuditRecorder, users UserRepository, actorUserID int64, origin string, groupID int64, action, detail string) {
	if auditor == nil {
		return
	}
	record := AuditRecord{
		ActorUserID: actorUserID,
		Action:      action,
		TargetType:  "group",
		TargetID:    strconv.FormatInt(groupID, 10),
		Detail:      truncateRunes(detail, maxAuditDetailLength),
		IP:          truncateRunes(origin, maxAuditOrigin),
	}
	if users != nil {
		if actor, err := users.FindUserByID(ctx, actorUserID); err == nil && actor.Email != nil {
			record.ActorEmail = *actor.Email
		}
	}
	_ = auditor.Record(ctx, record)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func (s *GroupService) validate(ctx context.Context) error {
	if s == nil || s.groups == nil || s.memberships == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil group context")
	}
	return nil
}

// NormalizeTimeZone validates an IANA time zone name against the embedded
// time zone database. "Local" is refused because it depends on the server.
func NormalizeTimeZone(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > maxTimeZoneLength || name == "Local" {
		return "", ErrInvalidTimeZone
	}
	if _, err := time.LoadLocation(name); err != nil {
		return "", ErrInvalidTimeZone
	}
	return name, nil
}

// CommonTimeZones returns the time zones offered in group settings. Any
// other valid IANA name is accepted as well.
func CommonTimeZones() []string {
	return []string{
		"Europe/Budapest", "Europe/Vienna", "Europe/Bratislava", "Europe/Prague", "Europe/Warsaw",
		"Europe/Bucharest", "Europe/Belgrade", "Europe/Zagreb", "Europe/Ljubljana", "Europe/Berlin",
		"Europe/Amsterdam", "Europe/Brussels", "Europe/Paris", "Europe/Madrid", "Europe/Rome",
		"Europe/Zurich", "Europe/Stockholm", "Europe/Oslo", "Europe/Copenhagen", "Europe/Helsinki",
		"Europe/Athens", "Europe/Kyiv", "Europe/Istanbul", "Europe/London", "Europe/Dublin",
		"Europe/Lisbon", "UTC", "America/New_York", "America/Chicago", "America/Denver",
		"America/Los_Angeles", "America/Toronto", "America/Sao_Paulo", "Asia/Tokyo", "Asia/Shanghai",
		"Asia/Singapore", "Asia/Dubai", "Asia/Kolkata", "Australia/Sydney", "Pacific/Auckland",
	}
}

func normalizeGroupName(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > maxGroupNameLength || hasControl(trimmed) {
		return "", ErrInvalidGroupName
	}
	return strings.ToLower(trimmed), nil
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func validateCreatedGroup(group Group, membership GroupMembership, userID int64) error {
	if group.ID <= 0 || group.Version < 0 {
		return ErrInvalidGroup
	}
	if membership.GroupID != group.ID || membership.UserID != userID || !slices.Contains(membership.Roles, GroupRoleAdmin) {
		return ErrInvalidGroup
	}
	return nil
}
