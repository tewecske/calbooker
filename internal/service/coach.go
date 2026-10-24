package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// Coach qualification limits: whole years.
const (
	MinCoachAge = 0
	MaxCoachAge = 99
)

// Stable coach audit action codes; the group is the target. A coach
// changing their own availability is self-service and not audited.
const (
	AuditActionCoachQualificationSet     = "group.coach.qualification_set"
	AuditActionCoachQualificationCleared = "group.coach.qualification_cleared"
	AuditActionCoachAvailabilityChanged  = "group.coach.availability_changed"
	AuditActionCoachAvailabilityCopied   = "group.coach.availability_copied"
)

var (
	// ErrCoachNotFound identifies an account that is not a coach of the
	// group (it lacks the coach role or is no member).
	ErrCoachNotFound = errors.New("service: coach not found")
	// ErrInvalidQualification identifies ages outside 0..99 or a minimum
	// above the maximum.
	ErrInvalidQualification = errors.New("service: invalid coach qualification")
	// ErrNilCoachRepository identifies missing coach persistence.
	ErrNilCoachRepository = errors.New("service: nil coach repository")
	// ErrNilCoachSeasons identifies a missing season source.
	ErrNilCoachSeasons = errors.New("service: nil coach season source")
)

// AgeRange is an inclusive range of ages in whole years.
type AgeRange struct {
	Min int
	Max int
}

// TeamAgeRange returns the ages of a team's players in a season: the
// season's start year minus the team's birth years (the oldest players were
// born in BirthYearFrom). A team born 2015-2016 is 10-11 in a season
// starting in 2026.
func TeamAgeRange(team Team, season Season) AgeRange {
	year := season.StartsOn.Year()
	from, to := team.BirthYearFrom, team.BirthYearTo
	if from > to {
		from, to = to, from
	}
	return AgeRange{Min: year - to, Max: year - from}
}

// CoachQualification is the age range a coach can coach in a group.
type CoachQualification struct {
	GroupID     int64
	CoachUserID int64
	MinAge      int
	MaxAge      int
}

// Range returns the qualification as an age range.
func (q CoachQualification) Range() AgeRange {
	return AgeRange{Min: q.MinAge, Max: q.MaxAge}
}

// Covers reports whether the whole age range is inside the qualification. A
// team aged 9-11 needs a coach qualified for at least 9 to 11; one qualified
// for 10-14 does not cover it.
func (q CoachQualification) Covers(ages AgeRange) bool {
	return q.MinAge <= ages.Min && ages.Max <= q.MaxAge
}

// Coach is a group member holding the coach role, with their
// qualification when one is set.
type Coach struct {
	UserID        int64
	Username      *string
	DisplayName   *string
	Email         *string
	Qualification *CoachQualification
}

// CoachRepository persists coach qualifications and availability. Every
// method is scoped to one group.
type CoachRepository interface {
	ListQualifications(ctx context.Context, groupID int64) ([]CoachQualification, error)
	// SetQualification adds or replaces a qualification; ErrCoachNotFound
	// when the member lacks the coach role.
	SetQualification(ctx context.Context, qualification CoachQualification, updatedAt int64) error
	// ClearQualification removes a qualification; nothing to remove is not
	// an error.
	ClearQualification(ctx context.Context, groupID, coachUserID int64) error
	// ListAvailability returns the windows of a part, of one coach or of
	// every coach when coachUserID is 0, ordered by coach, week, weekday,
	// and start.
	ListAvailability(ctx context.Context, groupID, partID, coachUserID int64) ([]AvailabilityWindow, error)
	// EditAvailability replaces a coach's windows of a part with what edit
	// returns for the current ones, in one transaction that serializes
	// edits of the coach. ErrCoachNotFound when the member lacks the coach
	// role, ErrSeasonNotFound when the part is not in the group; an error
	// from edit is returned unchanged and nothing is written.
	EditAvailability(ctx context.Context, groupID, partID, coachUserID, createdAt int64, edit func([]AvailabilityWindow) ([]AvailabilityWindow, error)) error
}

// CoachSeasonSource reads seasons and parts for coach use cases.
// SeasonService implements it.
type CoachSeasonSource interface {
	List(ctx context.Context, access *GroupAccess) ([]Season, error)
	FindPart(ctx context.Context, access *GroupAccess, partID int64) (Season, SeasonPart, error)
	Today(ctx context.Context, access *GroupAccess) (time.Time, error)
}

var _ CoachSeasonSource = (*SeasonService)(nil)

// CoachService owns coach qualification and availability use cases.
// Authorization comes from the caller's GroupAccess.
type CoachService struct {
	coaches     CoachRepository
	seasons     CoachSeasonSource
	memberships GroupMembershipRepository
	users       UserRepository
	auditor     AuditRecorder
	now         func() time.Time
}

// NewCoachService constructs coach use cases with explicit dependencies.
func NewCoachService(coaches CoachRepository, seasons CoachSeasonSource, memberships GroupMembershipRepository, users UserRepository) (*CoachService, error) {
	switch {
	case coaches == nil:
		return nil, ErrNilCoachRepository
	case seasons == nil:
		return nil, ErrNilCoachSeasons
	case memberships == nil:
		return nil, ErrNilMembershipRepository
	case users == nil:
		return nil, ErrNilUserRepository
	}
	return &CoachService{coaches: coaches, seasons: seasons, memberships: memberships, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *CoachService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Coaches returns the group's coaches with their qualifications: every
// coach for staff, only themselves for coaches. Emails only for staff and
// for the coach themself.
func (s *CoachService) Coaches(ctx context.Context, access *GroupAccess) ([]Coach, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	if !access.CanViewCoaches() {
		return nil, ErrGroupForbidden
	}
	members, err := s.memberships.ListMembers(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list coaches: %w", err)
	}
	qualifications, err := s.coaches.ListQualifications(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list coach qualifications: %w", err)
	}
	coaches := make([]Coach, 0)
	for _, member := range members {
		if !member.HasRole(GroupRoleCoach) || !access.CanViewQualification(member.UserID) {
			continue
		}
		coach := Coach{UserID: member.UserID, Username: member.Username, DisplayName: member.DisplayName}
		if access.CanViewAllMembers() || member.UserID == access.UserID() {
			coach.Email = member.Email
		}
		if index := slices.IndexFunc(qualifications, func(q CoachQualification) bool { return q.CoachUserID == member.UserID }); index >= 0 {
			qualification := qualifications[index]
			coach.Qualification = &qualification
		}
		coaches = append(coaches, coach)
	}
	return coaches, nil
}

// Qualifications returns every coach qualification of the group, for the
// planning boards. Staff only.
func (s *CoachService) Qualifications(ctx context.Context, access *GroupAccess) ([]CoachQualification, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	if !access.CanManageQualifications() {
		return nil, ErrGroupForbidden
	}
	qualifications, err := s.coaches.ListQualifications(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list coach qualifications: %w", err)
	}
	return qualifications, nil
}

// SetQualification sets the age range a coach can coach. Staff only;
// audited.
func (s *CoachService) SetQualification(ctx context.Context, actor GroupActor, coachUserID int64, minAge, maxAge int) (CoachQualification, error) {
	if err := s.authorizeQualification(ctx, actor, coachUserID); err != nil {
		return CoachQualification{}, err
	}
	if minAge < MinCoachAge || maxAge > MaxCoachAge || minAge > maxAge {
		return CoachQualification{}, ErrInvalidQualification
	}
	qualification := CoachQualification{GroupID: actor.Access.GroupID(), CoachUserID: coachUserID, MinAge: minAge, MaxAge: maxAge}
	if err := s.coaches.SetQualification(ctx, qualification, s.now().Unix()); err != nil {
		if errors.Is(err, ErrCoachNotFound) {
			return CoachQualification{}, err
		}
		return CoachQualification{}, fmt.Errorf("set coach qualification: %w", err)
	}
	s.record(ctx, actor, AuditActionCoachQualificationSet, "coach "+strconv.FormatInt(coachUserID, 10)+"; ages "+strconv.Itoa(minAge)+" to "+strconv.Itoa(maxAge))
	return qualification, nil
}

// ClearQualification removes a coach's qualification. Staff only; audited.
func (s *CoachService) ClearQualification(ctx context.Context, actor GroupActor, coachUserID int64) error {
	if err := s.authorizeQualification(ctx, actor, coachUserID); err != nil {
		return err
	}
	if err := s.coaches.ClearQualification(ctx, actor.Access.GroupID(), coachUserID); err != nil {
		return fmt.Errorf("clear coach qualification: %w", err)
	}
	s.record(ctx, actor, AuditActionCoachQualificationCleared, "coach "+strconv.FormatInt(coachUserID, 10))
	return nil
}

func (s *CoachService) authorizeQualification(ctx context.Context, actor GroupActor, coachUserID int64) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if err := authorizeGroupAction(actor, actor.Access.CanManageQualifications()); err != nil {
		return err
	}
	_, err := s.findCoach(ctx, actor.Access, coachUserID)
	return err
}

// findCoach returns the member coachUserID when they hold the coach role.
func (s *CoachService) findCoach(ctx context.Context, access *GroupAccess, coachUserID int64) (GroupMember, error) {
	if coachUserID <= 0 {
		return GroupMember{}, ErrCoachNotFound
	}
	members, err := s.memberships.ListMembers(ctx, access.GroupID())
	if err != nil {
		return GroupMember{}, fmt.Errorf("list coaches: %w", err)
	}
	for _, member := range members {
		if member.UserID == coachUserID && member.HasRole(GroupRoleCoach) {
			return member, nil
		}
	}
	return GroupMember{}, ErrCoachNotFound
}

func (s *CoachService) record(ctx context.Context, actor GroupActor, action, detail string) {
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func (s *CoachService) validate(ctx context.Context) error {
	if s == nil || s.coaches == nil || s.seasons == nil || s.memberships == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil coach context")
	}
	return nil
}
