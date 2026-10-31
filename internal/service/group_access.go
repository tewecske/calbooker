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
	// CalledUpEventIDs are the events the member or one of their children is
	// called up for (#47): the member sees them although they are not events
	// of a visible team.
	CalledUpEventIDs []int64
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
	calledUp  []int64
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
		calledUp:  slices.Clone(grants.CalledUpEventIDs),
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

// IsFamilyOnly reports whether the member holds only the player and parent
// roles (and is not a site administrator). Their start page is the calendar
// and their navigation leaves out the staff pages (#46).
func (a *GroupAccess) IsFamilyOnly() bool {
	if !a.IsMember() || a.siteAdmin || len(a.roles) == 0 {
		return false
	}
	for _, role := range a.roles {
		if role != GroupRolePlayer && role != GroupRoleParent {
			return false
		}
	}
	return true
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

// CanAddChildToTeam reports whether the account may add a child profile of
// their own to the team (#62): a member with the parent role who is on that
// team as a parent, either by a roster entry or because a linked child
// plays there. Whether the team is archived is the use case's rule.
func (a *GroupAccess) CanAddChildToTeam(teamID int64) bool {
	if !a.IsMember() || teamID <= 0 || !a.HasRole(GroupRoleParent) {
		return false
	}
	return slices.Contains(a.ownTeams, TeamRole{TeamID: teamID, Role: GroupRoleParent}) || slices.Contains(a.children, teamID)
}

// CanRemoveChildFromTeam reports whether the account may take the child
// userID off the team: the child's guardians, on any team they can see.
// Staff use the roster (CanManageRoster).
func (a *GroupAccess) CanRemoveChildFromTeam(teamID, userID int64) bool {
	return a.IsGuardianOf(userID) && a.CanViewTeam(teamID)
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

// CanManageKinds reports whether the account may add, rename, reorder,
// archive, and delete the group's event kinds and resource kinds (admins,
// managers, and site administrators). Everyone who sees the group sees the
// kind names on the events and resources that use them.
func (a *GroupAccess) CanManageKinds() bool {
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

// CanCreateEvents reports whether the account may create events at all:
// admins, managers, site administrators, and coaches of a team in the
// current season.
func (a *GroupAccess) CanCreateEvents() bool {
	return a.CanManageGroup() || (a.HasRole(GroupRoleCoach) && len(a.coached) > 0)
}

// CanCreateEvent reports whether the account may put an event on teamIDs:
// admins, managers, and site administrators for any teams; coaches only when
// they coach every one of the teams in the current season.
func (a *GroupAccess) CanCreateEvent(teamIDs []int64) bool {
	if a.CanManageGroup() {
		return true
	}
	if len(teamIDs) == 0 {
		return false
	}
	for _, teamID := range teamIDs {
		if !a.CoachesTeam(teamID) {
			return false
		}
	}
	return true
}

// CanEditEvent reports whether the account may edit, cancel, or delete the
// event: the same rule as CanCreateEvent for the event's teams.
func (a *GroupAccess) CanEditEvent(event Event) bool {
	return event.GroupID == a.GroupID() && a.CanCreateEvent(event.TeamIDs())
}

// CanViewEvent reports whether the event is visible (the Visibility rule):
// admins, managers, and site administrators see every event; others see the
// events of their own teams, the teams they coach, their children's teams,
// the events they are an extra coach of, and the events they or their
// children are called up for (#47). A call-up shows that one event only, not
// the called-up player's access to its teams.
func (a *GroupAccess) CanViewEvent(event Event) bool {
	if !a.CanView() || event.GroupID != a.groupID {
		return false
	}
	if a.SeesAllTeams() {
		return true
	}
	visible := a.VisibleTeamIDs()
	for _, teamID := range event.TeamIDs() {
		if slices.Contains(visible, teamID) {
			return true
		}
	}
	return a.IsMember() && (slices.Contains(event.CoachUserIDs(), a.userID) || slices.Contains(a.calledUp, event.ID))
}

// CalledUpEventIDs returns the events the member or their children are
// called up for.
func (a *GroupAccess) CalledUpEventIDs() []int64 {
	if !a.IsMember() {
		return nil
	}
	return slices.Clone(a.calledUp)
}

// CanViewCoaches reports whether the account sees the coaches page: staff
// (every coach) and coaches (only themselves).
func (a *GroupAccess) CanViewCoaches() bool {
	return a.CanManageGroup() || a.HasRole(GroupRoleCoach)
}

// CanManageQualifications reports whether the account may set and clear
// coach qualifications (admins, managers, and site administrators).
func (a *GroupAccess) CanManageQualifications() bool {
	return a.CanManageGroup()
}

// CanViewQualification reports whether the account sees the qualification
// of coachUserID: staff, and the coach themself (read only).
func (a *GroupAccess) CanViewQualification(coachUserID int64) bool {
	return a.CanManageQualifications() || a.isCoachSelf(coachUserID)
}

// CanViewAllAvailability reports whether the account sees every coach's
// availability side by side (admins, managers, and site administrators).
func (a *GroupAccess) CanViewAllAvailability() bool {
	return a.CanManageGroup()
}

// CanViewAvailability reports whether the account sees the availability of
// coachUserID: staff, and the coach themself. Coaches do not see other
// coaches' availability.
func (a *GroupAccess) CanViewAvailability(coachUserID int64) bool {
	return a.CanViewAllAvailability() || a.isCoachSelf(coachUserID)
}

// CanEditAvailability reports whether the account may change the
// availability of coachUserID: staff, and the coach themself.
func (a *GroupAccess) CanEditAvailability(coachUserID int64) bool {
	return a.CanManageGroup() || a.isCoachSelf(coachUserID)
}

func (a *GroupAccess) isCoachSelf(coachUserID int64) bool {
	return coachUserID > 0 && coachUserID == a.UserID() && a.HasRole(GroupRoleCoach)
}

// CanManagePlan reports whether the account may edit the weekly plans of
// season parts (resource slots, and later trainings): admins, managers, and
// site administrators.
func (a *GroupAccess) CanManagePlan() bool {
	return a.CanManageGroup()
}

// CanViewPlan reports whether the account sees the weekly plans: those who
// edit them, and coaches (read only). A coach's availability never changes
// the plan by itself.
func (a *GroupAccess) CanViewPlan() bool {
	return a.CanManagePlan() || a.HasRole(GroupRoleCoach)
}

// CanRespondFor reports whether the account may give userID's own answer to
// the event (#9): a player for themself, a linked parent for their child.
// Whether userID plays on one of the event's teams is checked by the
// participation service.
func (a *GroupAccess) CanRespondFor(event Event, userID int64) bool {
	if userID <= 0 || !a.CanViewEvent(event) {
		return false
	}
	if userID == a.userID {
		return a.HasRole(GroupRolePlayer)
	}
	return a.IsGuardianOf(userID)
}

// CanManageParticipants reports whether the account may change other
// players' answers, nominate players, see participant emails, and record
// attendance for the event: admins, managers, and site administrators, and
// the event's coaches (coaches of one of its teams in the current season,
// its extra coaches, and teamCoachIDs, the season coaches of its teams in
// the season containing the event, from EventDetail.TeamCoaches).
func (a *GroupAccess) CanManageParticipants(event Event, teamCoachIDs []int64) bool {
	if !a.CanViewEvent(event) {
		return false
	}
	if a.CanManageGroup() {
		return true
	}
	if !a.HasRole(GroupRoleCoach) {
		return false
	}
	if slices.ContainsFunc(event.TeamIDs(), a.CoachesTeam) {
		return true
	}
	return slices.Contains(event.CoachUserIDs(), a.userID) || slices.Contains(teamCoachIDs, a.userID)
}

// CanNominate reports whether the account may nominate players for the
// event: those who manage its participants, for nominated events only.
func (a *GroupAccess) CanNominate(event Event, teamCoachIDs []int64) bool {
	return event.SignupMode == EventSignupNominated && a.CanManageParticipants(event, teamCoachIDs)
}

// CanRecordAttendance reports whether the account may confirm who attended
// the event (when, is the participation service's rule).
func (a *GroupAccess) CanRecordAttendance(event Event, teamCoachIDs []int64) bool {
	return a.CanManageParticipants(event, teamCoachIDs)
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

// CallUpSource lists the events users are called up for (#47).
type CallUpSource interface {
	CalledUpEventIDs(ctx context.Context, groupID int64, userIDs []int64) ([]int64, error)
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
	callUps    CallUpSource
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

// SetCallUps attaches the source of call-ups. It is intended to be called
// once during startup.
func (s *GroupAccessService) SetCallUps(source CallUpSource) {
	if s != nil {
		s.callUps = source
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
	if grants.Member && s.callUps != nil && (slices.Contains(grants.Roles, GroupRolePlayer) || len(grants.ChildUserIDs) > 0) {
		ids := append([]int64{userID}, grants.ChildUserIDs...)
		if grants.CalledUpEventIDs, err = s.callUps.CalledUpEventIDs(ctx, groupID, ids); err != nil {
			return nil, fmt.Errorf("find call-ups: %w", err)
		}
	}
	return NewGroupAccess(groupID, userID, grants), nil
}

// CanViewStatistics reports whether the account opens the participation
// statistics (#11): every member sees at least their own numbers, and site
// administrators see everything. Non-members get a not-found answer from
// GroupAccessService.
func (a *GroupAccess) CanViewStatistics() bool {
	return a.CanView()
}

// StatisticsTeamIDs returns the teams whose statistics the account sees as
// a team: every team (all) for admins, managers, and site administrators;
// the teams they coach this season for coaches; none for players and
// parents, who see only their own (or their children's) numbers.
func (a *GroupAccess) StatisticsTeamIDs() (ids []int64, all bool) {
	switch {
	case !a.CanView():
		return nil, false
	case a.SeesAllTeams():
		return nil, true
	case a.HasRole(GroupRoleCoach):
		return slices.Clone(a.coached), false
	}
	return nil, false
}

// CanViewTeamStatistics reports whether the account sees the team's
// statistics.
func (a *GroupAccess) CanViewTeamStatistics(teamID int64) bool {
	ids, all := a.StatisticsTeamIDs()
	return all || slices.Contains(ids, teamID)
}

// StatisticsPlayerScope returns whose per-player numbers the account sees:
// every player (all); the players of the listed teams (the teams a coach
// coaches); and the listed users (the account itself and its children).
func (a *GroupAccess) StatisticsPlayerScope() (all bool, teamIDs, userIDs []int64) {
	if !a.CanView() {
		return false, nil, nil
	}
	teamIDs, all = a.StatisticsTeamIDs()
	if all {
		return true, nil, nil
	}
	if a.IsMember() {
		userIDs = append([]int64{a.userID}, a.childIDs...)
	}
	return false, teamIDs, userIDs
}
