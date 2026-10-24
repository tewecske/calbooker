package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Group roles. A member can hold several; permissions are the union.
const (
	GroupRoleAdmin   = "admin"
	GroupRoleManager = "manager"
	GroupRoleCoach   = "coach"
	GroupRolePlayer  = "player"
	GroupRoleParent  = "parent"
)

// groupRoleOrder is the display and storage order of group roles.
var groupRoleOrder = []string{GroupRoleAdmin, GroupRoleManager, GroupRoleCoach, GroupRolePlayer, GroupRoleParent}

// GroupRoles returns every group role in display order.
func GroupRoles() []string {
	return slices.Clone(groupRoleOrder)
}

// ValidGroupRole reports whether role is a group role.
func ValidGroupRole(role string) bool {
	return slices.Contains(groupRoleOrder, role)
}

// GroupRoleNeedsAccount reports whether role may be held only by a full user
// (signed in through single sign-on). Players and parents may be guests.
func GroupRoleNeedsAccount(role string) bool {
	return role == GroupRoleAdmin || role == GroupRoleManager || role == GroupRoleCoach
}

// sortGroupRoles orders roles by groupRoleOrder and drops duplicates and
// unknown values.
func sortGroupRoles(roles []string) []string {
	sorted := make([]string, 0, len(roles))
	for _, role := range groupRoleOrder {
		if slices.Contains(roles, role) {
			sorted = append(sorted, role)
		}
	}
	return sorted
}

// TeamRole is one roster entry of a member: a team and the member's role on it.
type TeamRole struct {
	TeamID int64
	Role   string
}

// GroupAccessGrants is everything a GroupAccess decides from. Only
// GroupAccessService builds it from storage; tests may build it directly.
type GroupAccessGrants struct {
	// Member is true when the account belongs to the group.
	Member bool
	// SiteAdmin is true for a site administrator, who may manage any group
	// without belonging to it. Their group actions are audited.
	SiteAdmin bool
	// Roles are the member's group roles.
	Roles []string
	// OwnTeams are the teams the member is on as a player or parent.
	OwnTeams []TeamRole
	// CoachedTeamIDs are the teams the member coaches in the current season
	// (season_team_coaches of the season containing today in the group's
	// time zone; SeasonService.TeamIDs).
	CoachedTeamIDs []int64
	// ChildTeamIDs are the teams of the member's linked children (the teams
	// on which a child of the member is a player).
	ChildTeamIDs []int64
	// ChildUserIDs are the members the account is a guardian of.
	ChildUserIDs []int64
}

// GroupAccess is the single authorization point for one account in one
// group, built per request. Handlers and use cases ask it questions and never
// check roles themselves.
type GroupAccess struct {
	groupID   int64
	userID    int64
	member    bool
	siteAdmin bool
	roles     []string
	ownTeams  []TeamRole
	coached   []int64
	children  []int64
	childIDs  []int64
}

// NewGroupAccess builds the access of userID in groupID from its grants.
func NewGroupAccess(groupID, userID int64, grants GroupAccessGrants) *GroupAccess {
	return &GroupAccess{
		groupID:   groupID,
		userID:    userID,
		member:    grants.Member,
		siteAdmin: grants.SiteAdmin,
		roles:     sortGroupRoles(grants.Roles),
		ownTeams:  slices.Clone(grants.OwnTeams),
		coached:   slices.Clone(grants.CoachedTeamIDs),
		children:  slices.Clone(grants.ChildTeamIDs),
		childIDs:  slices.Clone(grants.ChildUserIDs),
	}
}

// GroupID returns the group this access is about.
func (a *GroupAccess) GroupID() int64 {
	if a == nil {
		return 0
	}
	return a.groupID
}

// UserID returns the account this access is for.
func (a *GroupAccess) UserID() int64 {
	if a == nil {
		return 0
	}
	return a.userID
}

// CanView reports whether the account may see the group at all. Everyone
// else gets a not-found answer.
func (a *GroupAccess) CanView() bool {
	return a != nil && a.groupID > 0 && a.userID > 0 && (a.member || a.siteAdmin)
}

// IsMember reports whether the account belongs to the group.
func (a *GroupAccess) IsMember() bool {
	return a.CanView() && a.member
}

// IsSiteAdmin reports whether the account acts as a site administrator.
func (a *GroupAccess) IsSiteAdmin() bool {
	return a.CanView() && a.siteAdmin
}

// Roles returns the member's group roles in display order.
func (a *GroupAccess) Roles() []string {
	if !a.CanView() {
		return nil
	}
	return slices.Clone(a.roles)
}

// HasRole reports whether the member holds role.
func (a *GroupAccess) HasRole(role string) bool {
	return a.IsMember() && slices.Contains(a.roles, role)
}

// CanManageGroup reports whether the account may change group settings,
// teams, and member roles (admins, managers, and site administrators).
func (a *GroupAccess) CanManageGroup() bool {
	return a.IsSiteAdmin() || a.HasRole(GroupRoleAdmin) || a.HasRole(GroupRoleManager)
}

// CanManageRole reports whether the account may grant or remove role. Only
// admins grant or remove the admin role; managers handle the others.
func (a *GroupAccess) CanManageRole(role string) bool {
	if !ValidGroupRole(role) {
		return false
	}
	if role == GroupRoleAdmin {
		return a.IsSiteAdmin() || a.HasRole(GroupRoleAdmin)
	}
	return a.CanManageGroup()
}

// CanManageInvites reports whether the account may open the invite page:
// admins, managers, site administrators, and coaches (who see only invites
// for teams they coach).
func (a *GroupAccess) CanManageInvites() bool {
	return a.CanManageGroup() || a.HasRole(GroupRoleCoach)
}

// CanCreateInvite reports whether the account may create, see, and revoke an
// invite for role and, when teamID > 0, that team. Only admins invite
// admins; admins and managers invite any other role; coaches invite players
// and parents to teams they coach. Team invites carry only player or parent.
func (a *GroupAccess) CanCreateInvite(role string, teamID int64) bool {
	if !ValidGroupRole(role) {
		return false
	}
	rosterRole := role == GroupRolePlayer || role == GroupRoleParent
	if teamID > 0 && !rosterRole {
		return false
	}
	if a.CanManageRole(role) {
		return true
	}
	return rosterRole && teamID > 0 && a.CoachesTeam(teamID)
}

// ChildUserIDs returns the members the account is a guardian of.
func (a *GroupAccess) ChildUserIDs() []int64 {
	if !a.IsMember() {
		return nil
	}
	return slices.Clone(a.childIDs)
}

// IsGuardianOf reports whether the account is a linked parent of userID.
func (a *GroupAccess) IsGuardianOf(userID int64) bool {
	return a.IsMember() && userID > 0 && slices.Contains(a.childIDs, userID)
}

// CanCreateProfile reports whether the account may create a child profile:
// admins, managers, site administrators, and members with the parent role
// (whose new profiles are linked to them).
func (a *GroupAccess) CanCreateProfile() bool {
	return a.CanManageGroup() || a.HasRole(GroupRoleParent)
}

// CanEditProfile reports whether the account may change or delete the child
// profile userID: staff and the profile's guardians.
func (a *GroupAccess) CanEditProfile(userID int64) bool {
	return a.CanManageGroup() || a.IsGuardianOf(userID)
}

// CanManageGuardians reports whether the account may link any parent to any
// child and remove links (admins, managers, site administrators).
func (a *GroupAccess) CanManageGuardians() bool {
	return a.CanManageGroup()
}

// CanViewChildren reports whether the account sees the children page.
func (a *GroupAccess) CanViewChildren() bool {
	return a.CanCreateProfile() || len(a.ChildUserIDs()) > 0
}

// CanRemoveMember reports whether the account may remove other members.
func (a *GroupAccess) CanRemoveMember() bool {
	return a.CanManageGroup()
}

// CanViewAllMembers reports whether the account sees the full member list
// with email addresses.
func (a *GroupAccess) CanViewAllMembers() bool {
	return a.CanManageGroup()
}

// CanEditTeam reports whether the account may change a team's name, birth
// years, description, or archive state.
func (a *GroupAccess) CanEditTeam(int64) bool {
	return a.CanManageGroup()
}

// CanCreateTeam reports whether the account may create teams.
func (a *GroupAccess) CanCreateTeam() bool {
	return a.CanManageGroup()
}

// CanManageResources reports whether the account may create, edit, archive,
// and restore resources (admins, managers, and site administrators).
func (a *GroupAccess) CanManageResources() bool {
	return a.CanManageGroup()
}

// CanViewResources reports whether the account sees the resource list:
// those who manage resources, and coaches (read only). Players and parents
// see a resource only where an event or plan names it.
func (a *GroupAccess) CanViewResources() bool {
	return a.CanManageResources() || a.HasRole(GroupRoleCoach)
}

// CanManageSeasons reports whether the account may create, edit, and delete
// seasons and their parts and assign season coaches (admins, managers, and
// site administrators).
func (a *GroupAccess) CanManageSeasons() bool {
	return a.CanManageGroup()
}

// CanViewSeasons reports whether the account sees the season pages and
// every season's coaches: those who manage seasons, and coaches (read
// only). Players and parents see only the current season's coaches on
// their teams.
func (a *GroupAccess) CanViewSeasons() bool {
	return a.CanManageSeasons() || a.HasRole(GroupRoleCoach)
}

// CanManageRoster reports whether the account may add and remove players
// and parents on a team: admins, managers, and the team's coaches.
func (a *GroupAccess) CanManageRoster(teamID int64) bool {
	return a.CanManageGroup() || a.CoachesTeam(teamID)
}

// CoachesTeam reports whether the member is a coach of the team in the
// current season.
func (a *GroupAccess) CoachesTeam(teamID int64) bool {
	return a.HasRole(GroupRoleCoach) && slices.Contains(a.coached, teamID)
}

// SeesAllTeams reports whether every team of the group is visible.
func (a *GroupAccess) SeesAllTeams() bool {
	return a.CanManageGroup()
}

// VisibleTeamIDs returns the teams the member sees without SeesAllTeams:
// their own teams, the teams they coach, and their children's teams.
func (a *GroupAccess) VisibleTeamIDs() []int64 {
	if !a.CanView() {
		return nil
	}
	ids := make([]int64, 0, len(a.ownTeams)+len(a.coached)+len(a.children))
	for _, team := range a.ownTeams {
		ids = append(ids, team.TeamID)
	}
	ids = append(ids, a.coached...)
	ids = append(ids, a.children...)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// CanViewTeam reports whether the team and its roster names are visible.
func (a *GroupAccess) CanViewTeam(teamID int64) bool {
	if a.SeesAllTeams() {
		return true
	}
	return a.CanView() && slices.Contains(a.VisibleTeamIDs(), teamID)
}

// CanViewTeamEmails reports whether email addresses on the team's roster are
// visible: admins and managers, and the team's coaches.
func (a *GroupAccess) CanViewTeamEmails(teamID int64) bool {
	return a.CanManageGroup() || a.CoachesTeam(teamID)
}

// TeamRelationSource lists team IDs related to a member by a feature added
// later: the teams a coach coaches in the current season (#5), or the teams of
// a parent's children (#3).
type TeamRelationSource interface {
	TeamIDs(ctx context.Context, groupID, userID int64) ([]int64, error)
}

// ChildRelationSource lists a parent's children and, through TeamIDs, the
// teams on which those children play (guardians).
type ChildRelationSource interface {
	TeamRelationSource
	ChildUserIDs(ctx context.Context, groupID, parentUserID int64) ([]int64, error)
}

// MemberAccess is the stored part of a member's grants.
type MemberAccess struct {
	Roles    []string
	OwnTeams []TeamRole
}

// GroupAccessRepository reads what GroupAccess needs from storage.
type GroupAccessRepository interface {
	// FindMemberAccess returns ErrMembershipNotFound for non-members.
	FindMemberAccess(ctx context.Context, groupID, userID int64) (MemberAccess, error)
	GroupExists(ctx context.Context, groupID int64) (bool, error)
}

// ErrNilGroupAccessRepository identifies missing access persistence.
var ErrNilGroupAccessRepository = errors.New("service: nil group access repository")

// GroupAccessService builds a GroupAccess per request.
type GroupAccessService struct {
	repository GroupAccessRepository
	users      UserRepository
	coached    TeamRelationSource
	children   ChildRelationSource
}

// NewGroupAccessService constructs the access builder.
func NewGroupAccessService(repository GroupAccessRepository, users UserRepository) (*GroupAccessService, error) {
	if repository == nil {
		return nil, ErrNilGroupAccessRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &GroupAccessService{repository: repository, users: users}, nil
}

// SetCoachedTeams attaches the source of coached teams (season coaches).
// It is intended to be called once during startup.
func (s *GroupAccessService) SetCoachedTeams(source TeamRelationSource) {
	if s != nil {
		s.coached = source
	}
}

// SetChildTeams attaches the source of children and their teams
// (guardians). It is intended to be called once during startup.
func (s *GroupAccessService) SetChildTeams(source ChildRelationSource) {
	if s != nil {
		s.children = source
	}
}

// For builds the access of userID in groupID. Accounts that may not see the
// group, and groups that do not exist, both get ErrGroupNotFound.
func (s *GroupAccessService) For(ctx context.Context, userID, groupID int64) (*GroupAccess, error) {
	if s == nil || s.repository == nil || s.users == nil {
		return nil, ErrInvalidGroup
	}
	if userID <= 0 || groupID <= 0 {
		return nil, ErrGroupNotFound
	}
	grants := GroupAccessGrants{}
	stored, err := s.repository.FindMemberAccess(ctx, groupID, userID)
	switch {
	case err == nil:
		grants.Member = true
		grants.Roles = stored.Roles
		grants.OwnTeams = stored.OwnTeams
	case errors.Is(err, ErrMembershipNotFound):
	default:
		return nil, fmt.Errorf("find group access: %w", err)
	}
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("find group actor: %w", err)
	}
	grants.SiteAdmin = user.IsAdmin && user.IsFullUser()
	if !grants.Member {
		if !grants.SiteAdmin {
			return nil, ErrGroupNotFound
		}
		exists, err := s.repository.GroupExists(ctx, groupID)
		if err != nil {
			return nil, fmt.Errorf("find group: %w", err)
		}
		if !exists {
			return nil, ErrGroupNotFound
		}
	}
	if grants.Member && s.coached != nil && slices.Contains(grants.Roles, GroupRoleCoach) {
		if grants.CoachedTeamIDs, err = s.coached.TeamIDs(ctx, groupID, userID); err != nil {
			return nil, fmt.Errorf("find coached teams: %w", err)
		}
	}
	if grants.Member && s.children != nil {
		if grants.ChildUserIDs, err = s.children.ChildUserIDs(ctx, groupID, userID); err != nil {
			return nil, fmt.Errorf("find children: %w", err)
		}
		if len(grants.ChildUserIDs) > 0 {
			if grants.ChildTeamIDs, err = s.children.TeamIDs(ctx, groupID, userID); err != nil {
				return nil, fmt.Errorf("find children's teams: %w", err)
			}
		}
	}
	return NewGroupAccess(groupID, userID, grants), nil
}
