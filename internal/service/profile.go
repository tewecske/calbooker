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

const maxProfileNameLength = 64

var (
	// ErrProfileCannotSignIn identifies a child profile used where an account
	// would sign in, join, or be upgraded. Profiles never sign in.
	ErrProfileCannotSignIn = errors.New("service: profiles never sign in")
	// ErrProfileNotFound identifies a child profile outside the group or one
	// the account may not see.
	ErrProfileNotFound = errors.New("service: child profile not found")
	// ErrInvalidProfileName identifies a profile name outside storage limits.
	ErrInvalidProfileName = errors.New("service: invalid child profile name")
	// ErrInvalidProfileBirthYear identifies a birth year out of range.
	ErrInvalidProfileBirthYear = errors.New("service: invalid child profile birth year")
	// ErrProfileMembership identifies an attempt to remove a profile from the
	// group as a member: profiles are deleted instead.
	ErrProfileMembership = errors.New("service: child profiles are deleted, not removed")
	// ErrGuardianNotFound identifies a missing guardian link.
	ErrGuardianNotFound = errors.New("service: guardian link not found")
	// ErrGuardianNeedsParentRole identifies a guardian who lacks the parent
	// role in the group.
	ErrGuardianNeedsParentRole = errors.New("service: guardian needs the parent role")
	// ErrInvalidGuardian identifies a link of a member to themselves.
	ErrInvalidGuardian = errors.New("service: invalid guardian link")
	// ErrNilProfileRepository identifies missing profile persistence.
	ErrNilProfileRepository = errors.New("service: nil child profile repository")
)

// Profile and guardian audit action codes.
const (
	AuditActionProfileCreated  = "group.profile_created"
	AuditActionProfileUpdated  = "group.profile_updated"
	AuditActionProfileDeleted  = "group.profile_deleted"
	AuditActionGuardianAdded   = "group.guardian_added"
	AuditActionGuardianRemoved = "group.guardian_removed"
)

// ChildProfile is a child without a login: a users row with kind 'profile'
// that belongs to one group and holds the player role there.
type ChildProfile struct {
	UserID      int64
	GroupID     int64
	DisplayName string
	BirthYear   int
	CreatedBy   *int64
	CreatedAt   int64
	Version     int64
}

// ProfileInput is a new or changed child profile. GuardianUserID and TeamID
// apply on creation by staff only (0 means none).
type ProfileInput struct {
	DisplayName    string
	BirthYear      int
	GuardianUserID int64
	TeamID         int64
}

// PersonRef names a member for display.
type PersonRef struct {
	UserID      int64
	Kind        UserKind
	Username    *string
	DisplayName *string
}

// TeamRef names a team for display.
type TeamRef struct {
	ID   int64
	Name string
}

// ChildEntry is one child of the group: a profile, or a member account with
// at least one guardian.
type ChildEntry struct {
	Person    PersonRef
	Profile   *ChildProfile
	Guardians []PersonRef
	Teams     []TeamRef
}

// GuardianOf reports whether userID is one of the child's guardians.
func (c ChildEntry) GuardianOf(userID int64) bool {
	return slices.ContainsFunc(c.Guardians, func(p PersonRef) bool { return p.UserID == userID })
}

// ChildrenOverview is the children page: the children the account sees and,
// for staff, the members and teams to choose from.
type ChildrenOverview struct {
	Children []ChildEntry
	// Members and Teams are filled for staff only.
	Members []GroupMember
	Teams   []TeamSummary
}

// ProfileRepository persists child profiles and guardian links. Every
// method is scoped to one group.
type ProfileRepository interface {
	// CreateProfile inserts the profile user, its membership with the player
	// role, and optionally a guardian link and a team roster entry, in one
	// transaction.
	CreateProfile(ctx context.Context, profile ChildProfile, guardianUserID, teamID int64) (ChildProfile, error)
	// FindProfile returns ErrProfileNotFound outside the group.
	FindProfile(ctx context.Context, groupID, userID int64) (ChildProfile, error)
	UpdateProfile(ctx context.Context, profile ChildProfile, expectedVersion int64) (ChildProfile, error)
	// DeleteProfile removes the profile user at the expected revision.
	DeleteProfile(ctx context.Context, groupID, userID, expectedVersion int64) error
	ListChildren(ctx context.Context, groupID int64) ([]ChildEntry, error)
	// AddGuardian is idempotent. It returns ErrGuardianNeedsParentRole or
	// ErrMembershipNotFound when the parent or child does not qualify.
	AddGuardian(ctx context.Context, groupID, parentUserID, childUserID int64, createdBy *int64, createdAt int64) error
	RemoveGuardian(ctx context.Context, groupID, parentUserID, childUserID int64) error
}

// ProfileService owns child profiles and guardian links.
type ProfileService struct {
	profiles    ProfileRepository
	memberships GroupMembershipRepository
	teams       TeamRepository
	users       UserRepository
	auditor     AuditRecorder
	now         func() time.Time
}

// NewProfileService constructs profile and guardian use cases.
func NewProfileService(profiles ProfileRepository, memberships GroupMembershipRepository, teams TeamRepository, users UserRepository) (*ProfileService, error) {
	if profiles == nil {
		return nil, ErrNilProfileRepository
	}
	if memberships == nil {
		return nil, ErrNilMembershipRepository
	}
	if teams == nil {
		return nil, ErrNilTeamRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &ProfileService{profiles: profiles, memberships: memberships, teams: teams, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *ProfileService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Children returns the children the account sees: every child for staff,
// the account's own children for parents.
func (s *ProfileService) Children(ctx context.Context, access *GroupAccess) (ChildrenOverview, error) {
	if err := s.authorize(ctx, GroupActor{Access: access}, access.CanViewChildren()); err != nil {
		return ChildrenOverview{}, err
	}
	children, err := s.profiles.ListChildren(ctx, access.GroupID())
	if err != nil {
		return ChildrenOverview{}, fmt.Errorf("list children: %w", err)
	}
	overview := ChildrenOverview{}
	for _, child := range children {
		if access.CanManageGuardians() || access.IsGuardianOf(child.Person.UserID) {
			overview.Children = append(overview.Children, child)
		}
	}
	if access.CanManageGuardians() {
		if overview.Members, err = s.memberships.ListMembers(ctx, access.GroupID()); err != nil {
			return ChildrenOverview{}, fmt.Errorf("list guardian candidates: %w", err)
		}
		if overview.Teams, err = s.teams.ListTeams(ctx, access.GroupID()); err != nil {
			return ChildrenOverview{}, fmt.Errorf("list profile teams: %w", err)
		}
	}
	return overview, nil
}

// CreateProfile adds a child profile. A parent's profile is linked to the
// parent; staff may pick a guardian and a team.
func (s *ProfileService) CreateProfile(ctx context.Context, actor GroupActor, input ProfileInput) (ChildProfile, error) {
	if err := s.authorize(ctx, actor, actor.Access.CanCreateProfile()); err != nil {
		return ChildProfile{}, err
	}
	name, err := normalizeProfileName(input.DisplayName)
	if err != nil {
		return ChildProfile{}, err
	}
	if err := validateBirthYear(input.BirthYear); err != nil {
		return ChildProfile{}, err
	}
	guardianID, teamID := input.GuardianUserID, input.TeamID
	if !actor.Access.CanManageGuardians() {
		if teamID != 0 || (guardianID != 0 && guardianID != actor.Access.UserID()) {
			return ChildProfile{}, ErrGroupForbidden
		}
		guardianID = actor.Access.UserID()
	}
	if guardianID < 0 || teamID < 0 {
		return ChildProfile{}, ErrInvalidGuardian
	}
	if teamID > 0 {
		if _, err := s.teams.FindTeam(ctx, actor.Access.GroupID(), teamID); err != nil {
			return ChildProfile{}, err
		}
	}
	profile := ChildProfile{GroupID: actor.Access.GroupID(), DisplayName: name, BirthYear: input.BirthYear, CreatedAt: s.now().Unix()}
	if userID := actor.Access.UserID(); userID > 0 {
		profile.CreatedBy = &userID
	}
	created, err := s.profiles.CreateProfile(ctx, profile, guardianID, teamID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGuardianNeedsParentRole), errors.Is(err, ErrMembershipNotFound), errors.Is(err, ErrTeamNotFound):
			return ChildProfile{}, err
		}
		return ChildProfile{}, fmt.Errorf("create child profile: %w", err)
	}
	detail := profileAuditDetail(created)
	if guardianID > 0 {
		detail += "; guardian " + strconv.FormatInt(guardianID, 10)
	}
	if teamID > 0 {
		detail += "; team " + strconv.FormatInt(teamID, 10)
	}
	s.record(ctx, actor, AuditActionProfileCreated, detail)
	return created, nil
}

// UpdateProfile renames a profile or changes its birth year at the expected
// revision. Staff and the profile's guardians.
func (s *ProfileService) UpdateProfile(ctx context.Context, actor GroupActor, userID, expectedVersion int64, input ProfileInput) (ChildProfile, error) {
	profile, err := s.findEditable(ctx, actor, userID, expectedVersion)
	if err != nil {
		return ChildProfile{}, err
	}
	name, err := normalizeProfileName(input.DisplayName)
	if err != nil {
		return ChildProfile{}, err
	}
	if err := validateBirthYear(input.BirthYear); err != nil {
		return ChildProfile{}, err
	}
	profile.DisplayName, profile.BirthYear = name, input.BirthYear
	updated, err := s.profiles.UpdateProfile(ctx, profile, expectedVersion)
	if err != nil {
		return ChildProfile{}, classifyProfileWrite(err)
	}
	s.record(ctx, actor, AuditActionProfileUpdated, profileAuditDetail(updated))
	return updated, nil
}

// DeleteProfile removes a profile with its membership, roster entries, and
// guardian links. Staff and the profile's guardians.
func (s *ProfileService) DeleteProfile(ctx context.Context, actor GroupActor, userID, expectedVersion int64) error {
	profile, err := s.findEditable(ctx, actor, userID, expectedVersion)
	if err != nil {
		return err
	}
	if err := s.profiles.DeleteProfile(ctx, actor.Access.GroupID(), userID, expectedVersion); err != nil {
		return classifyProfileWrite(err)
	}
	s.record(ctx, actor, AuditActionProfileDeleted, profileAuditDetail(profile))
	return nil
}

// AddGuardian links a parent (a member with the parent role) to a child
// (any other member). Staff only.
func (s *ProfileService) AddGuardian(ctx context.Context, actor GroupActor, parentUserID, childUserID int64) error {
	if err := s.authorize(ctx, actor, actor.Access.CanManageGuardians()); err != nil {
		return err
	}
	if parentUserID <= 0 || childUserID <= 0 || parentUserID == childUserID {
		return ErrInvalidGuardian
	}
	if err := s.profiles.AddGuardian(ctx, actor.Access.GroupID(), parentUserID, childUserID, positiveID(actor.Access.UserID()), s.now().Unix()); err != nil {
		switch {
		case errors.Is(err, ErrGuardianNeedsParentRole), errors.Is(err, ErrMembershipNotFound):
			return err
		}
		return fmt.Errorf("add guardian: %w", err)
	}
	s.record(ctx, actor, AuditActionGuardianAdded, guardianAuditDetail(parentUserID, childUserID))
	return nil
}

// RemoveGuardian unlinks a parent from a child. Staff only.
func (s *ProfileService) RemoveGuardian(ctx context.Context, actor GroupActor, parentUserID, childUserID int64) error {
	if err := s.authorize(ctx, actor, actor.Access.CanManageGuardians()); err != nil {
		return err
	}
	if parentUserID <= 0 || childUserID <= 0 {
		return ErrGuardianNotFound
	}
	if err := s.profiles.RemoveGuardian(ctx, actor.Access.GroupID(), parentUserID, childUserID); err != nil {
		if errors.Is(err, ErrGuardianNotFound) {
			return err
		}
		return fmt.Errorf("remove guardian: %w", err)
	}
	s.record(ctx, actor, AuditActionGuardianRemoved, guardianAuditDetail(parentUserID, childUserID))
	return nil
}

func (s *ProfileService) findEditable(ctx context.Context, actor GroupActor, userID, expectedVersion int64) (ChildProfile, error) {
	if err := s.validate(ctx); err != nil {
		return ChildProfile{}, err
	}
	if !actor.Access.CanView() {
		return ChildProfile{}, ErrGroupNotFound
	}
	if userID <= 0 || !actor.Access.CanEditProfile(userID) {
		return ChildProfile{}, ErrProfileNotFound
	}
	if expectedVersion < 0 {
		return ChildProfile{}, ErrInvalidGroup
	}
	return s.profiles.FindProfile(ctx, actor.Access.GroupID(), userID)
}

func (s *ProfileService) authorize(ctx context.Context, actor GroupActor, allowed bool) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	return authorizeGroupAction(actor, allowed)
}

func (s *ProfileService) record(ctx context.Context, actor GroupActor, action, detail string) {
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func (s *ProfileService) validate(ctx context.Context) error {
	if s == nil || s.profiles == nil || s.memberships == nil || s.teams == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil profile context")
	}
	return nil
}

// classifyProfileWrite keeps a vanished profile not-found and a stale one a
// conflict.
func classifyProfileWrite(err error) error {
	switch {
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrProfileNotFound):
		return ErrProfileNotFound
	case errors.Is(err, ErrOptimisticLockConflict):
		return err
	default:
		return fmt.Errorf("write child profile: %w", err)
	}
}

func normalizeProfileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxProfileNameLength || hasControl(name) {
		return "", ErrInvalidProfileName
	}
	return name, nil
}

func validateBirthYear(year int) error {
	if year < MinTeamBirthYear || year > MaxTeamBirthYear {
		return ErrInvalidProfileBirthYear
	}
	return nil
}

// profileAuditDetail names a profile by ID only: a child's name and birth
// year stay out of the audit trail.
func profileAuditDetail(profile ChildProfile) string {
	return "profile " + strconv.FormatInt(profile.UserID, 10)
}

func guardianAuditDetail(parentUserID, childUserID int64) string {
	return "parent " + strconv.FormatInt(parentUserID, 10) + "; child " + strconv.FormatInt(childUserID, 10)
}

func positiveID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}
