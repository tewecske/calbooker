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

const (
	maxTeamNameLength        = 64
	maxTeamDescriptionLength = 255
	// MinTeamBirthYear and MaxTeamBirthYear bound a team's birth-year range.
	MinTeamBirthYear = 1900
	MaxTeamBirthYear = 2200
)

var (
	// ErrTeamNotFound identifies a team that does not exist in the group or
	// that the account may not see.
	ErrTeamNotFound = errors.New("service: team not found")
	// ErrInvalidTeamName identifies a team name outside storage limits.
	ErrInvalidTeamName = errors.New("service: invalid team name")
	// ErrInvalidTeamBirthYears identifies a birth-year range that is out of
	// bounds or reversed.
	ErrInvalidTeamBirthYears = errors.New("service: invalid team birth years")
	// ErrInvalidTeamDescription identifies a description outside storage limits.
	ErrInvalidTeamDescription = errors.New("service: invalid team description")
	// ErrInvalidRosterRole identifies a roster role other than player or parent.
	ErrInvalidRosterRole = errors.New("service: invalid roster role")
	// ErrRosterRoleMissing identifies a member who lacks the group role the
	// roster entry needs (a player must hold the player role, a parent the
	// parent role).
	ErrRosterRoleMissing = errors.New("service: member lacks the roster role")
	// ErrRosterEntryNotFound identifies a missing roster entry.
	ErrRosterEntryNotFound = errors.New("service: roster entry not found")
	// ErrNilTeamRepository identifies missing team persistence.
	ErrNilTeamRepository = errors.New("service: nil team repository")
)

// Team is a birth-year range plus a name within a group. It persists across
// seasons; archived teams are kept for history.
type Team struct {
	ID      int64
	GroupID int64
	Name    string
	// Slug addresses the team in URLs; unique within the group.
	Slug          string
	BirthYearFrom int
	BirthYearTo   int
	Description   *string
	ArchivedAt    *int64
	CreatedAt     int64
	Version       int64
}

// Archived reports whether the team is archived.
func (t Team) Archived() bool {
	return t.ArchivedAt != nil
}

// TeamSummary is one row of the team list.
type TeamSummary struct {
	Team    Team
	Players int
	Parents int
}

// TeamInput is the editable part of a team.
type TeamInput struct {
	Name string
	// Slug is the edited URL slug. Empty keeps the current slug on update
	// and derives one from the name on create.
	Slug          string
	BirthYearFrom int
	BirthYearTo   int
	Description   string
}

// TeamRosterEntry is one player or parent on a team.
type TeamRosterEntry struct {
	UserID      int64
	Role        string
	Kind        UserKind
	Username    *string
	DisplayName *string
	Email       *string
	// ChildUserIDs are the players on the team this parent is a guardian
	// of. Linked is true when the parent is on the roster only through
	// those links (derived from guardians, not a stored roster entry).
	ChildUserIDs []int64
	Linked       bool
}

// TeamDetail is a team with its roster and, for accounts that may change the
// roster, the members who can be added.
type TeamDetail struct {
	Team   Team
	Roster []TeamRosterEntry
	// Candidates are members holding the player or parent role; empty
	// unless the account may manage the roster.
	Candidates []GroupMember
}

// TeamRepository persists teams and rosters. Every method is scoped to one
// group.
type TeamRepository interface {
	CreateTeam(context.Context, Team) (Team, error)
	// FindTeam returns ErrTeamNotFound for a team outside the group.
	FindTeam(ctx context.Context, groupID, teamID int64) (Team, error)
	// FindTeamBySlug returns ErrTeamNotFound for an unknown slug.
	FindTeamBySlug(ctx context.Context, groupID int64, slug string) (Team, error)
	// TeamSlugTaken reports whether a team of the group other than exceptID
	// uses slug.
	TeamSlugTaken(ctx context.Context, groupID int64, slug string, exceptID int64) (bool, error)
	ListTeams(ctx context.Context, groupID int64) ([]TeamSummary, error)
	// UpdateTeam writes name, birth years, description, and archive state at
	// the expected revision; stale writes classify through ClassifyStaleWrite.
	UpdateTeam(ctx context.Context, team Team, expectedVersion int64) (Team, error)
	ListRoster(ctx context.Context, groupID, teamID int64) ([]TeamRosterEntry, error)
	// AddRosterEntry is idempotent and returns ErrRosterRoleMissing when the
	// member lacks the matching group role.
	AddRosterEntry(ctx context.Context, groupID, teamID, userID int64, role string, createdAt int64) error
	// RemoveRosterEntry returns ErrRosterEntryNotFound when nothing matched.
	RemoveRosterEntry(ctx context.Context, groupID, teamID, userID int64, role string) error
}

// TeamService owns team and roster use cases. Authorization comes from the
// caller's GroupAccess.
type TeamService struct {
	teams       TeamRepository
	memberships GroupMembershipRepository
	users       UserRepository
	auditor     AuditRecorder
	now         func() time.Time
}

// NewTeamService constructs team use cases with explicit dependencies.
func NewTeamService(teams TeamRepository, memberships GroupMembershipRepository, users UserRepository) (*TeamService, error) {
	if teams == nil {
		return nil, ErrNilTeamRepository
	}
	if memberships == nil {
		return nil, ErrNilMembershipRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &TeamService{teams: teams, memberships: memberships, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *TeamService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// List returns the teams the account may see, active ones first.
func (s *TeamService) List(ctx context.Context, access *GroupAccess) ([]TeamSummary, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	teams, err := s.teams.ListTeams(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	visible := make([]TeamSummary, 0, len(teams))
	for _, team := range teams {
		if access.CanViewTeam(team.Team.ID) {
			visible = append(visible, team)
		}
	}
	return visible, nil
}

// Create adds a team. Admins and managers only.
func (s *TeamService) Create(ctx context.Context, actor GroupActor, input TeamInput) (Team, error) {
	if err := s.authorize(ctx, actor, actor.Access.CanCreateTeam()); err != nil {
		return Team{}, err
	}
	team, err := buildTeam(input)
	if err != nil {
		return Team{}, err
	}
	team.GroupID = actor.Access.GroupID()
	team.CreatedAt = s.now().Unix()
	var created Team
	for attempt := 0; ; attempt++ {
		if team.Slug, err = s.newSlug(ctx, team, input.Slug); err != nil {
			return Team{}, err
		}
		created, err = s.teams.CreateTeam(ctx, team)
		if errors.Is(err, ErrSlugTaken) && input.Slug == "" && attempt < slugCreateAttempts {
			continue
		}
		break
	}
	if err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return Team{}, err
		}
		return Team{}, fmt.Errorf("create team: %w", err)
	}
	s.record(ctx, actor, AuditActionTeamCreated, created, "")
	return created, nil
}

// Detail returns a visible team with its roster. Emails stay hidden unless
// the account may see them for this team.
func (s *TeamService) Detail(ctx context.Context, access *GroupAccess, teamID int64) (TeamDetail, error) {
	if err := s.validate(ctx); err != nil {
		return TeamDetail{}, err
	}
	if !access.CanView() {
		return TeamDetail{}, ErrGroupNotFound
	}
	if teamID <= 0 || !access.CanViewTeam(teamID) {
		return TeamDetail{}, ErrTeamNotFound
	}
	team, err := s.teams.FindTeam(ctx, access.GroupID(), teamID)
	if err != nil {
		return TeamDetail{}, err
	}
	roster, err := s.teams.ListRoster(ctx, access.GroupID(), teamID)
	if err != nil {
		return TeamDetail{}, fmt.Errorf("list team roster: %w", err)
	}
	if !access.CanViewTeamEmails(teamID) {
		for index := range roster {
			if roster[index].UserID != access.UserID() {
				roster[index].Email = nil
			}
		}
	}
	detail := TeamDetail{Team: team, Roster: roster}
	if access.CanManageRoster(teamID) {
		members, err := s.memberships.ListMembers(ctx, access.GroupID())
		if err != nil {
			return TeamDetail{}, fmt.Errorf("list roster candidates: %w", err)
		}
		for _, member := range members {
			if member.HasRole(GroupRolePlayer) || member.HasRole(GroupRoleParent) {
				detail.Candidates = append(detail.Candidates, member)
			}
		}
	}
	return detail, nil
}

// Update changes a team's name, birth years, and description at the expected
// revision. Admins and managers only.
func (s *TeamService) Update(ctx context.Context, actor GroupActor, teamID, expectedVersion int64, input TeamInput) (Team, error) {
	team, err := s.findEditable(ctx, actor, teamID, expectedVersion)
	if err != nil {
		return Team{}, err
	}
	changed, err := buildTeam(input)
	if err != nil {
		return Team{}, err
	}
	team.Name, team.BirthYearFrom, team.BirthYearTo, team.Description = changed.Name, changed.BirthYearFrom, changed.BirthYearTo, changed.Description
	if strings.TrimSpace(input.Slug) != "" {
		if team.Slug, err = s.checkSlug(ctx, team, input.Slug); err != nil {
			return Team{}, err
		}
	}
	updated, err := s.update(ctx, team, expectedVersion)
	if err != nil {
		return Team{}, err
	}
	s.record(ctx, actor, AuditActionTeamUpdated, updated, "")
	return updated, nil
}

// SetArchived archives or restores a team at the expected revision. Admins
// and managers only.
func (s *TeamService) SetArchived(ctx context.Context, actor GroupActor, teamID, expectedVersion int64, archived bool) (Team, error) {
	team, err := s.findEditable(ctx, actor, teamID, expectedVersion)
	if err != nil {
		return Team{}, err
	}
	if team.Archived() == archived {
		return team, nil
	}
	action := AuditActionTeamRestored
	team.ArchivedAt = nil
	if archived {
		now := s.now().Unix()
		team.ArchivedAt = &now
		action = AuditActionTeamArchived
	}
	updated, err := s.update(ctx, team, expectedVersion)
	if err != nil {
		return Team{}, err
	}
	s.record(ctx, actor, action, updated, "")
	return updated, nil
}

// AddToRoster puts a member on a team as a player or parent. The member must
// hold the matching group role. Admins, managers, and the team's coaches.
func (s *TeamService) AddToRoster(ctx context.Context, actor GroupActor, teamID, userID int64, role string) error {
	team, err := s.findRosterTeam(ctx, actor, teamID, userID, role)
	if err != nil {
		return err
	}
	if err := s.teams.AddRosterEntry(ctx, actor.Access.GroupID(), teamID, userID, role, s.now().Unix()); err != nil {
		if errors.Is(err, ErrRosterRoleMissing) || errors.Is(err, ErrTeamNotFound) {
			return err
		}
		return fmt.Errorf("add roster entry: %w", err)
	}
	s.record(ctx, actor, AuditActionRosterAdded, team, "member "+strconv.FormatInt(userID, 10)+": "+role)
	return nil
}

// RemoveFromRoster takes a player or parent off a team. Admins, managers,
// and the team's coaches.
func (s *TeamService) RemoveFromRoster(ctx context.Context, actor GroupActor, teamID, userID int64, role string) error {
	team, err := s.findRosterTeam(ctx, actor, teamID, userID, role)
	if err != nil {
		return err
	}
	if err := s.teams.RemoveRosterEntry(ctx, actor.Access.GroupID(), teamID, userID, role); err != nil {
		if errors.Is(err, ErrRosterEntryNotFound) {
			return err
		}
		return fmt.Errorf("remove roster entry: %w", err)
	}
	s.record(ctx, actor, AuditActionRosterRemoved, team, "member "+strconv.FormatInt(userID, 10)+": "+role)
	return nil
}

func (s *TeamService) findRosterTeam(ctx context.Context, actor GroupActor, teamID, userID int64, role string) (Team, error) {
	if err := s.validate(ctx); err != nil {
		return Team{}, err
	}
	if !actor.Access.CanView() {
		return Team{}, ErrGroupNotFound
	}
	if teamID <= 0 || !actor.Access.CanViewTeam(teamID) {
		return Team{}, ErrTeamNotFound
	}
	if !actor.Access.CanManageRoster(teamID) {
		return Team{}, ErrGroupForbidden
	}
	if role != GroupRolePlayer && role != GroupRoleParent {
		return Team{}, ErrInvalidRosterRole
	}
	if userID <= 0 {
		return Team{}, ErrMembershipNotFound
	}
	return s.teams.FindTeam(ctx, actor.Access.GroupID(), teamID)
}

func (s *TeamService) findEditable(ctx context.Context, actor GroupActor, teamID, expectedVersion int64) (Team, error) {
	if err := s.validate(ctx); err != nil {
		return Team{}, err
	}
	if !actor.Access.CanView() {
		return Team{}, ErrGroupNotFound
	}
	if teamID <= 0 || !actor.Access.CanViewTeam(teamID) {
		return Team{}, ErrTeamNotFound
	}
	if !actor.Access.CanEditTeam(teamID) {
		return Team{}, ErrGroupForbidden
	}
	if expectedVersion < 0 {
		return Team{}, ErrInvalidGroup
	}
	return s.teams.FindTeam(ctx, actor.Access.GroupID(), teamID)
}

// newSlug validates the slug typed for a new team, or derives a free one
// from its name.
func (s *TeamService) newSlug(ctx context.Context, team Team, typed string) (string, error) {
	if strings.TrimSpace(typed) != "" {
		return s.checkSlug(ctx, team, typed)
	}
	slug, err := uniqueSlug(team.Name, "team", func(candidate string) (bool, error) {
		return s.teams.TeamSlugTaken(ctx, team.GroupID, candidate, 0)
	})
	if err != nil {
		return "", fmt.Errorf("find team slug: %w", err)
	}
	return slug, nil
}

// checkSlug validates a typed slug and refuses one another team of the group
// uses.
func (s *TeamService) checkSlug(ctx context.Context, team Team, typed string) (string, error) {
	slug, err := normalizeSlugInput(typed)
	if err != nil {
		return "", err
	}
	taken, err := s.teams.TeamSlugTaken(ctx, team.GroupID, slug, team.ID)
	if err != nil {
		return "", fmt.Errorf("check team slug: %w", err)
	}
	if taken {
		return "", ErrSlugTaken
	}
	return slug, nil
}

// BySlug returns a team of the group the account may see. Unknown slugs and
// teams the account may not see both answer ErrTeamNotFound.
func (s *TeamService) BySlug(ctx context.Context, access *GroupAccess, slug string) (Team, error) {
	if err := s.validate(ctx); err != nil {
		return Team{}, err
	}
	if !access.CanView() {
		return Team{}, ErrGroupNotFound
	}
	if ValidateSlug(slug) != nil {
		return Team{}, ErrTeamNotFound
	}
	team, err := s.teams.FindTeamBySlug(ctx, access.GroupID(), slug)
	if err != nil {
		return Team{}, err
	}
	if !access.CanViewTeam(team.ID) {
		return Team{}, ErrTeamNotFound
	}
	return team, nil
}

// ByID returns a team of the group the account may see; it backs the
// redirect from the old numeric team URLs.
func (s *TeamService) ByID(ctx context.Context, access *GroupAccess, teamID int64) (Team, error) {
	if err := s.validate(ctx); err != nil {
		return Team{}, err
	}
	if !access.CanView() {
		return Team{}, ErrGroupNotFound
	}
	if teamID <= 0 || !access.CanViewTeam(teamID) {
		return Team{}, ErrTeamNotFound
	}
	return s.teams.FindTeam(ctx, access.GroupID(), teamID)
}

// Slugs returns the URL slug of every team of the group by team ID, for
// building links to teams known only by ID. Slugs are not secret, but
// callers only use the ones of teams they already show.
func (s *TeamService) Slugs(ctx context.Context, access *GroupAccess) (map[int64]string, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	teams, err := s.teams.ListTeams(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list team slugs: %w", err)
	}
	slugs := make(map[int64]string, len(teams))
	for _, summary := range teams {
		slugs[summary.Team.ID] = summary.Team.Slug
	}
	return slugs, nil
}

func (s *TeamService) update(ctx context.Context, team Team, expectedVersion int64) (Team, error) {
	updated, err := s.teams.UpdateTeam(ctx, team, expectedVersion)
	switch {
	case err == nil:
		return updated, nil
	case errors.Is(err, ErrSlugTaken):
		return Team{}, err
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrTeamNotFound):
		// A team that vanished is not-found, never a conflict.
		return Team{}, ErrTeamNotFound
	case errors.Is(err, ErrOptimisticLockConflict):
		return Team{}, err
	default:
		return Team{}, fmt.Errorf("update team: %w", err)
	}
}

func (s *TeamService) authorize(ctx context.Context, actor GroupActor, allowed bool) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	return authorizeGroupAction(actor, allowed)
}

func (s *TeamService) record(ctx context.Context, actor GroupActor, action string, team Team, extra string) {
	detail := "team " + strconv.FormatInt(team.ID, 10) + ": " + team.Name
	if action == AuditActionTeamCreated || action == AuditActionTeamUpdated {
		detail += "; slug " + team.Slug + "; birth years " + strconv.Itoa(team.BirthYearFrom) + "-" + strconv.Itoa(team.BirthYearTo)
	}
	if extra != "" {
		detail += "; " + extra
	}
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func (s *TeamService) validate(ctx context.Context) error {
	if s == nil || s.teams == nil || s.memberships == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil team context")
	}
	return nil
}

// buildTeam validates team input. Names and descriptions are trimmed; an
// empty description is stored as none.
func buildTeam(input TeamInput) (Team, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTeamNameLength || hasControl(name) {
		return Team{}, ErrInvalidTeamName
	}
	if input.BirthYearFrom < MinTeamBirthYear || input.BirthYearTo > MaxTeamBirthYear || input.BirthYearFrom > input.BirthYearTo {
		return Team{}, ErrInvalidTeamBirthYears
	}
	team := Team{Name: name, BirthYearFrom: input.BirthYearFrom, BirthYearTo: input.BirthYearTo}
	description := strings.TrimSpace(input.Description)
	if description != "" {
		if utf8.RuneCountInString(description) > maxTeamDescriptionLength || hasControl(description) {
			return Team{}, ErrInvalidTeamDescription
		}
		team.Description = &description
	}
	return team, nil
}

// RosterRoles returns the roles a team roster entry can have.
func RosterRoles() []string {
	return slices.Clone([]string{GroupRolePlayer, GroupRoleParent})
}
