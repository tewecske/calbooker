package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxSeasonNameLength = 64
	// MaxSeasonParts is how many parts a season may have.
	MaxSeasonParts = 2
	// SeasonDateLayout is the wire and form format of season dates.
	SeasonDateLayout = "2006-01-02"
	minSeasonYear    = 1900
	maxSeasonYear    = 2200
)

// Plan week values: a part with alternating weeks has A and B weeks (week 1
// of the part is A); otherwise every week is "all".
const (
	SeasonWeekA   = "A"
	SeasonWeekB   = "B"
	SeasonWeekAll = "all"
)

// Stable season audit action codes; the group is the target.
const (
	AuditActionSeasonCreated       = "group.season.created"
	AuditActionSeasonUpdated       = "group.season.updated"
	AuditActionSeasonDeleted       = "group.season.deleted"
	AuditActionSeasonCoachAssigned = "group.season.coach_assigned"
	AuditActionSeasonCoachRemoved  = "group.season.coach_removed"
)

var (
	// ErrSeasonNotFound identifies a season (or season part) that does not
	// exist in the group or that the account may not see.
	ErrSeasonNotFound = errors.New("service: season not found")
	// ErrInvalidSeasonName identifies a season name outside storage limits.
	ErrInvalidSeasonName = errors.New("service: invalid season name")
	// ErrSeasonNameTaken identifies a name another season of the group uses.
	ErrSeasonNameTaken = errors.New("service: season name taken")
	// ErrInvalidSeasonDates identifies missing dates or a start that is not
	// before the end.
	ErrInvalidSeasonDates = errors.New("service: invalid season dates")
	// ErrSeasonOverlap identifies a season that overlaps another season of
	// the group.
	ErrSeasonOverlap = errors.New("service: season overlaps another season")
	// ErrInvalidSeasonParts identifies a season without parts or with more
	// than MaxSeasonParts.
	ErrInvalidSeasonParts = errors.New("service: invalid number of season parts")
	// ErrInvalidSeasonPartName identifies a part name outside storage limits.
	ErrInvalidSeasonPartName = errors.New("service: invalid season part name")
	// ErrInvalidSeasonPartDates identifies missing part dates or a part start
	// that is not before its end.
	ErrInvalidSeasonPartDates = errors.New("service: invalid season part dates")
	// ErrSeasonPartOutsideSeason identifies a part that is not inside its
	// season.
	ErrSeasonPartOutsideSeason = errors.New("service: season part outside the season")
	// ErrSeasonPartsOverlap identifies a second part that does not start
	// after the first part ends.
	ErrSeasonPartsOverlap = errors.New("service: season parts overlap or are out of order")
	// ErrSeasonCoachRoleMissing identifies a coach assignment for a member
	// without the coach role.
	ErrSeasonCoachRoleMissing = errors.New("service: member lacks the coach role")
	// ErrSeasonCoachNotFound identifies a coach assignment that does not
	// exist.
	ErrSeasonCoachNotFound = errors.New("service: season coach not found")
	// ErrNilSeasonRepository identifies missing season persistence.
	ErrNilSeasonRepository = errors.New("service: nil season repository")
)

// SeasonPartError names the part (1 or 2) a part validation error is about.
type SeasonPartError struct {
	Position int
	Err      error
}

func (e *SeasonPartError) Error() string {
	return "season part " + strconv.Itoa(e.Position) + ": " + e.Err.Error()
}

func (e *SeasonPartError) Unwrap() error {
	return e.Err
}

// Season is a group's season: a date range (calendar days in the group's
// time zone, both ends included) with one or two parts. Version covers the
// season and its parts, which are edited together.
type Season struct {
	ID        int64
	GroupID   int64
	Name      string
	StartsOn  time.Time
	EndsOn    time.Time
	Parts     []SeasonPart
	CreatedAt int64
	Version   int64
}

// SeasonPart is part 1 or 2 of a season. Weekly plans (#7, #13, #14)
// reference a part by (group_id, season_part_id).
type SeasonPart struct {
	ID               int64
	GroupID          int64
	SeasonID         int64
	Position         int
	Name             string
	StartsOn         time.Time
	EndsOn           time.Time
	AlternatingWeeks bool
}

// Contains reports whether day (a calendar date) is inside the season.
func (s Season) Contains(day time.Time) bool {
	return dateInRange(day, s.StartsOn, s.EndsOn)
}

// PartOn returns the part containing day, if any.
func (s Season) PartOn(day time.Time) (SeasonPart, bool) {
	for _, part := range s.Parts {
		if part.Contains(day) {
			return part, true
		}
	}
	return SeasonPart{}, false
}

// Part returns the part with partID.
func (s Season) Part(partID int64) (SeasonPart, bool) {
	for _, part := range s.Parts {
		if part.ID == partID {
			return part, true
		}
	}
	return SeasonPart{}, false
}

// Contains reports whether day (a calendar date) is inside the part.
func (p SeasonPart) Contains(day time.Time) bool {
	return dateInRange(day, p.StartsOn, p.EndsOn)
}

// WeekOn returns the plan week of day in the part: SeasonWeekA or
// SeasonWeekB for alternating parts (the Monday-based week holding the
// part's first day is A), SeasonWeekAll otherwise, and "" outside the part.
func (p SeasonPart) WeekOn(day time.Time) string {
	if !p.Contains(day) {
		return ""
	}
	if !p.AlternatingWeeks {
		return SeasonWeekAll
	}
	weeks := int(mondayOf(SeasonDate(day)).Sub(mondayOf(p.StartsOn)).Hours()) / (24 * 7)
	if weeks%2 == 0 {
		return SeasonWeekA
	}
	return SeasonWeekB
}

// SeasonInput is the editable part of a season. Zero dates are refused.
type SeasonInput struct {
	Name     string
	StartsOn time.Time
	EndsOn   time.Time
	Parts    []SeasonPartInput
}

// SeasonPartInput is one part of a SeasonInput, in order.
type SeasonPartInput struct {
	Name             string
	StartsOn         time.Time
	EndsOn           time.Time
	AlternatingWeeks bool
}

// SeasonCoach is one coach assignment of a team in a season, with display
// data for lists.
type SeasonCoach struct {
	GroupID     int64
	SeasonID    int64
	TeamID      int64
	CoachUserID int64
	IsHead      bool
	SeasonName  string
	TeamName    string
	Username    *string
	DisplayName *string
	Email       *string
}

// SeasonCoachFilter narrows ListSeasonCoaches; zero fields match all.
type SeasonCoachFilter struct {
	SeasonID int64
	TeamID   int64
}

// CoachAssignment assigns a coach to a team in a season. Head marks the
// head coach; a team has at most one per season, so marking a new head
// coach unmarks the previous one.
type CoachAssignment struct {
	SeasonID    int64
	TeamID      int64
	CoachUserID int64
	Head        bool
}

// TeamCoaching is what the team page shows about coaches: the seasons the
// account may see, the coach assignments in them, and, for accounts that
// assign coaches, the members holding the coach role.
type TeamCoaching struct {
	Seasons []Season
	// CurrentSeasonID is the season containing today in the group's time
	// zone, 0 when there is none.
	CurrentSeasonID int64
	Coaches         []SeasonCoach
	Candidates      []GroupMember
}

// SeasonRepository persists seasons, their parts, and coach assignments.
// Every method is scoped to one group.
type SeasonRepository interface {
	GroupTimeZone(ctx context.Context, groupID int64) (string, error)
	// CreateSeason inserts the season and its parts in one transaction;
	// ErrSeasonOverlap and ErrSeasonNameTaken come from constraints.
	CreateSeason(context.Context, Season) (Season, error)
	// FindSeason returns ErrSeasonNotFound for a season outside the group.
	FindSeason(ctx context.Context, groupID, seasonID int64) (Season, error)
	// FindSeasonByPart returns the season holding the part.
	FindSeasonByPart(ctx context.Context, groupID, partID int64) (Season, error)
	// ListSeasons returns the group's seasons, latest first.
	ListSeasons(ctx context.Context, groupID int64) ([]Season, error)
	// SeasonOn returns the season containing day, or ErrSeasonNotFound.
	SeasonOn(ctx context.Context, groupID int64, day time.Time) (Season, error)
	// UpdateSeason writes the season and its parts (by position; a missing
	// part 2 is deleted) at the expected revision; stale writes classify
	// through ClassifyStaleWrite.
	UpdateSeason(ctx context.Context, season Season, expectedVersion int64) (Season, error)
	// DeleteSeason removes the season at the expected revision; parts and
	// coach assignments cascade.
	DeleteSeason(ctx context.Context, groupID, seasonID, expectedVersion int64) error
	ListSeasonCoaches(ctx context.Context, groupID int64, filter SeasonCoachFilter) ([]SeasonCoach, error)
	// AssignSeasonCoach adds or updates an assignment; it returns
	// ErrSeasonNotFound, ErrTeamNotFound, or ErrSeasonCoachRoleMissing from
	// foreign keys.
	AssignSeasonCoach(ctx context.Context, assignment CoachAssignment, groupID, createdAt int64) error
	// RemoveSeasonCoach returns ErrSeasonCoachNotFound when nothing matched.
	RemoveSeasonCoach(ctx context.Context, groupID, seasonID, teamID, coachUserID int64) error
	// CoachedTeamIDs returns the teams coachUserID coaches in the season
	// containing day.
	CoachedTeamIDs(ctx context.Context, groupID, coachUserID int64, day time.Time) ([]int64, error)
}

// SeasonService owns season, season part, and season coach use cases. It
// is also the GroupAccess source of coached teams (TeamIDs). Authorization
// comes from the caller's GroupAccess.
type SeasonService struct {
	seasons     SeasonRepository
	memberships GroupMembershipRepository
	users       UserRepository
	auditor     AuditRecorder
	now         func() time.Time
}

var _ TeamRelationSource = (*SeasonService)(nil)

// NewSeasonService constructs season use cases with explicit dependencies.
func NewSeasonService(seasons SeasonRepository, memberships GroupMembershipRepository, users UserRepository) (*SeasonService, error) {
	if seasons == nil {
		return nil, ErrNilSeasonRepository
	}
	if memberships == nil {
		return nil, ErrNilMembershipRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &SeasonService{seasons: seasons, memberships: memberships, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *SeasonService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// List returns the group's seasons, latest first. Admins, managers, site
// administrators, and coaches.
func (s *SeasonService) List(ctx context.Context, access *GroupAccess) ([]Season, error) {
	if err := s.viewable(ctx, access); err != nil {
		return nil, err
	}
	seasons, err := s.seasons.ListSeasons(ctx, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list seasons: %w", err)
	}
	return seasons, nil
}

// Find returns one season with its parts. Every account that sees the
// group may load it, so events and plans can name their season; the season
// pages also need CanViewSeasons.
func (s *SeasonService) Find(ctx context.Context, access *GroupAccess, seasonID int64) (Season, error) {
	if err := s.validate(ctx); err != nil {
		return Season{}, err
	}
	if !access.CanView() {
		return Season{}, ErrGroupNotFound
	}
	if seasonID <= 0 {
		return Season{}, ErrSeasonNotFound
	}
	return s.seasons.FindSeason(ctx, access.GroupID(), seasonID)
}

// FindPart returns a season part and its season, for features that work
// per part (availability, plan slots, trainings). Same access as Find.
func (s *SeasonService) FindPart(ctx context.Context, access *GroupAccess, partID int64) (Season, SeasonPart, error) {
	if err := s.validate(ctx); err != nil {
		return Season{}, SeasonPart{}, err
	}
	if !access.CanView() {
		return Season{}, SeasonPart{}, ErrGroupNotFound
	}
	if partID <= 0 {
		return Season{}, SeasonPart{}, ErrSeasonNotFound
	}
	season, err := s.seasons.FindSeasonByPart(ctx, access.GroupID(), partID)
	if err != nil {
		return Season{}, SeasonPart{}, err
	}
	part, ok := season.Part(partID)
	if !ok {
		return Season{}, SeasonPart{}, ErrSeasonNotFound
	}
	return season, part, nil
}

// Today returns today's date in the group's time zone.
func (s *SeasonService) Today(ctx context.Context, access *GroupAccess) (time.Time, error) {
	if err := s.validate(ctx); err != nil {
		return time.Time{}, err
	}
	if !access.CanView() {
		return time.Time{}, ErrGroupNotFound
	}
	return s.today(ctx, access.GroupID())
}

// Current returns the season containing today in the group's time zone;
// false when there is none. Every account that sees the group.
func (s *SeasonService) Current(ctx context.Context, access *GroupAccess) (Season, bool, error) {
	if err := s.validate(ctx); err != nil {
		return Season{}, false, err
	}
	if !access.CanView() {
		return Season{}, false, ErrGroupNotFound
	}
	return s.current(ctx, access.GroupID())
}

// TeamIDs returns the teams userID coaches in the group's current season.
// It implements TeamRelationSource for GroupAccessService, which calls it
// only for members holding the coach role.
func (s *SeasonService) TeamIDs(ctx context.Context, groupID, userID int64) ([]int64, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if groupID <= 0 || userID <= 0 {
		return nil, nil
	}
	today, err := s.today(ctx, groupID)
	if err != nil {
		return nil, err
	}
	ids, err := s.seasons.CoachedTeamIDs(ctx, groupID, userID, today)
	if err != nil {
		return nil, fmt.Errorf("list coached teams: %w", err)
	}
	return ids, nil
}

// Create adds a season with its parts. Admins, managers, and site
// administrators.
func (s *SeasonService) Create(ctx context.Context, actor GroupActor, input SeasonInput) (Season, error) {
	if err := s.validate(ctx); err != nil {
		return Season{}, err
	}
	if err := authorizeGroupAction(actor, actor.Access.CanManageSeasons()); err != nil {
		return Season{}, err
	}
	season, err := buildSeason(input)
	if err != nil {
		return Season{}, err
	}
	season.GroupID = actor.Access.GroupID()
	season.CreatedAt = s.now().Unix()
	created, err := s.seasons.CreateSeason(ctx, season)
	if err != nil {
		if errors.Is(err, ErrSeasonOverlap) || errors.Is(err, ErrSeasonNameTaken) || errors.Is(err, ErrSeasonPartsOverlap) {
			return Season{}, err
		}
		return Season{}, fmt.Errorf("create season: %w", err)
	}
	s.record(ctx, actor, AuditActionSeasonCreated, seasonDetail(created, true))
	return created, nil
}

// Update replaces a season's name, dates, and parts at the expected
// revision. Part 1 and 2 keep their IDs; dropping part 2 deletes it and
// whatever later features stored for it.
func (s *SeasonService) Update(ctx context.Context, actor GroupActor, seasonID, expectedVersion int64, input SeasonInput) (Season, error) {
	season, err := s.findEditable(ctx, actor, seasonID, expectedVersion)
	if err != nil {
		return Season{}, err
	}
	changed, err := buildSeason(input)
	if err != nil {
		return Season{}, err
	}
	changed.ID, changed.GroupID, changed.CreatedAt, changed.Version = season.ID, season.GroupID, season.CreatedAt, season.Version
	updated, err := s.seasons.UpdateSeason(ctx, changed, expectedVersion)
	switch {
	case err == nil:
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrSeasonNotFound):
		// A season that vanished is not-found, never a conflict.
		return Season{}, ErrSeasonNotFound
	case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrSeasonOverlap), errors.Is(err, ErrSeasonNameTaken), errors.Is(err, ErrSeasonPartsOverlap):
		return Season{}, err
	default:
		return Season{}, fmt.Errorf("update season: %w", err)
	}
	s.record(ctx, actor, AuditActionSeasonUpdated, seasonDetail(updated, true))
	return updated, nil
}

// SetPartAlternating switches the A/B weeks of one season part at the
// season's expected revision, leaving everything else as stored. It is a
// season edit: the same authorization, revision check, and audit entry as
// Update. Plan items keep their week; for a part that stops alternating the
// plans show A/B items as every-week.
func (s *SeasonService) SetPartAlternating(ctx context.Context, actor GroupActor, seasonID, partID, expectedVersion int64, alternating bool) (Season, error) {
	season, err := s.findEditable(ctx, actor, seasonID, expectedVersion)
	if err != nil {
		return Season{}, err
	}
	input := SeasonInput{Name: season.Name, StartsOn: season.StartsOn, EndsOn: season.EndsOn}
	found := false
	for _, part := range season.Parts {
		raw := SeasonPartInput{Name: part.Name, StartsOn: part.StartsOn, EndsOn: part.EndsOn, AlternatingWeeks: part.AlternatingWeeks}
		if part.ID == partID {
			raw.AlternatingWeeks, found = alternating, true
		}
		input.Parts = append(input.Parts, raw)
	}
	if !found {
		return Season{}, ErrSeasonNotFound
	}
	return s.Update(ctx, actor, seasonID, expectedVersion, input)
}

// Delete removes a season at the expected revision with its parts and
// coach assignments.
func (s *SeasonService) Delete(ctx context.Context, actor GroupActor, seasonID, expectedVersion int64) error {
	season, err := s.findEditable(ctx, actor, seasonID, expectedVersion)
	if err != nil {
		return err
	}
	err = s.seasons.DeleteSeason(ctx, season.GroupID, season.ID, expectedVersion)
	switch {
	case err == nil:
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrSeasonNotFound):
		return ErrSeasonNotFound
	case errors.Is(err, ErrOptimisticLockConflict):
		return err
	default:
		return fmt.Errorf("delete season: %w", err)
	}
	s.record(ctx, actor, AuditActionSeasonDeleted, seasonDetail(season, false))
	return nil
}

// SeasonCoaches returns every coach assignment of a season, by team. The
// same accounts as List.
func (s *SeasonService) SeasonCoaches(ctx context.Context, access *GroupAccess, seasonID int64) ([]SeasonCoach, error) {
	if err := s.viewable(ctx, access); err != nil {
		return nil, err
	}
	if seasonID <= 0 {
		return nil, ErrSeasonNotFound
	}
	if _, err := s.seasons.FindSeason(ctx, access.GroupID(), seasonID); err != nil {
		return nil, err
	}
	coaches, err := s.seasons.ListSeasonCoaches(ctx, access.GroupID(), SeasonCoachFilter{SeasonID: seasonID})
	if err != nil {
		return nil, fmt.Errorf("list season coaches: %w", err)
	}
	if !access.CanViewAllMembers() {
		for index := range coaches {
			if !access.CanViewTeamEmails(coaches[index].TeamID) {
				coaches[index].Email = nil
			}
		}
	}
	return coaches, nil
}

// TeamCoaching returns the coaches of a visible team. Accounts that see
// seasons get every season; players and parents get the current season's
// coaches only. Emails follow CanViewTeamEmails; candidates are listed only
// for accounts that assign coaches.
func (s *SeasonService) TeamCoaching(ctx context.Context, access *GroupAccess, teamID int64) (TeamCoaching, error) {
	if err := s.validate(ctx); err != nil {
		return TeamCoaching{}, err
	}
	if !access.CanView() {
		return TeamCoaching{}, ErrGroupNotFound
	}
	if teamID <= 0 || !access.CanViewTeam(teamID) {
		return TeamCoaching{}, ErrTeamNotFound
	}
	coaching := TeamCoaching{}
	current, found, err := s.current(ctx, access.GroupID())
	if err != nil {
		return TeamCoaching{}, err
	}
	if found {
		coaching.CurrentSeasonID = current.ID
	}
	if access.CanViewSeasons() {
		if coaching.Seasons, err = s.seasons.ListSeasons(ctx, access.GroupID()); err != nil {
			return TeamCoaching{}, fmt.Errorf("list seasons: %w", err)
		}
	} else if found {
		coaching.Seasons = []Season{current}
	}
	if len(coaching.Seasons) > 0 {
		coaches, err := s.seasons.ListSeasonCoaches(ctx, access.GroupID(), SeasonCoachFilter{TeamID: teamID})
		if err != nil {
			return TeamCoaching{}, fmt.Errorf("list team coaches: %w", err)
		}
		for _, coach := range coaches {
			if !access.CanViewSeasons() && coach.SeasonID != coaching.CurrentSeasonID {
				continue
			}
			if !access.CanViewTeamEmails(teamID) && coach.CoachUserID != access.UserID() {
				coach.Email = nil
			}
			coaching.Coaches = append(coaching.Coaches, coach)
		}
	}
	if access.CanManageSeasons() {
		members, err := s.memberships.ListMembers(ctx, access.GroupID())
		if err != nil {
			return TeamCoaching{}, fmt.Errorf("list coach candidates: %w", err)
		}
		for _, member := range members {
			if member.HasRole(GroupRoleCoach) {
				coaching.Candidates = append(coaching.Candidates, member)
			}
		}
	}
	return coaching, nil
}

// AssignCoach adds a coach to a team for a season, or changes whether they
// are the head coach. The member must hold the coach role. Admins,
// managers, and site administrators.
func (s *SeasonService) AssignCoach(ctx context.Context, actor GroupActor, assignment CoachAssignment) error {
	if err := s.authorizeCoachChange(ctx, actor, assignment.SeasonID, assignment.TeamID, assignment.CoachUserID); err != nil {
		return err
	}
	err := s.seasons.AssignSeasonCoach(ctx, assignment, actor.Access.GroupID(), s.now().Unix())
	switch {
	case err == nil:
	case errors.Is(err, ErrSeasonNotFound), errors.Is(err, ErrTeamNotFound), errors.Is(err, ErrSeasonCoachRoleMissing), errors.Is(err, ErrOptimisticLockConflict):
		return err
	default:
		return fmt.Errorf("assign season coach: %w", err)
	}
	detail := coachDetail(assignment.SeasonID, assignment.TeamID, assignment.CoachUserID)
	if assignment.Head {
		detail += "; head coach"
	}
	s.record(ctx, actor, AuditActionSeasonCoachAssigned, detail)
	return nil
}

// RemoveCoach takes a coach off a team for a season.
func (s *SeasonService) RemoveCoach(ctx context.Context, actor GroupActor, seasonID, teamID, coachUserID int64) error {
	if err := s.authorizeCoachChange(ctx, actor, seasonID, teamID, coachUserID); err != nil {
		return err
	}
	if err := s.seasons.RemoveSeasonCoach(ctx, actor.Access.GroupID(), seasonID, teamID, coachUserID); err != nil {
		if errors.Is(err, ErrSeasonCoachNotFound) {
			return err
		}
		return fmt.Errorf("remove season coach: %w", err)
	}
	s.record(ctx, actor, AuditActionSeasonCoachRemoved, coachDetail(seasonID, teamID, coachUserID))
	return nil
}

func (s *SeasonService) authorizeCoachChange(ctx context.Context, actor GroupActor, seasonID, teamID, coachUserID int64) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if !actor.Access.CanView() {
		return ErrGroupNotFound
	}
	if teamID <= 0 || !actor.Access.CanViewTeam(teamID) {
		return ErrTeamNotFound
	}
	if !actor.Access.CanManageSeasons() {
		return ErrGroupForbidden
	}
	if seasonID <= 0 {
		return ErrSeasonNotFound
	}
	if coachUserID <= 0 {
		return ErrSeasonCoachRoleMissing
	}
	return nil
}

func (s *SeasonService) current(ctx context.Context, groupID int64) (Season, bool, error) {
	today, err := s.today(ctx, groupID)
	if err != nil {
		return Season{}, false, err
	}
	season, err := s.seasons.SeasonOn(ctx, groupID, today)
	switch {
	case err == nil:
		return season, true, nil
	case errors.Is(err, ErrSeasonNotFound):
		return Season{}, false, nil
	default:
		return Season{}, false, fmt.Errorf("find current season: %w", err)
	}
}

func (s *SeasonService) today(ctx context.Context, groupID int64) (time.Time, error) {
	zone, err := s.seasons.GroupTimeZone(ctx, groupID)
	if err != nil {
		return time.Time{}, fmt.Errorf("find group time zone: %w", err)
	}
	return LocalDate(s.now(), zone)
}

func (s *SeasonService) viewable(ctx context.Context, access *GroupAccess) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if !access.CanView() {
		return ErrGroupNotFound
	}
	if !access.CanViewSeasons() {
		return ErrGroupForbidden
	}
	return nil
}

func (s *SeasonService) findEditable(ctx context.Context, actor GroupActor, seasonID, expectedVersion int64) (Season, error) {
	if err := s.validate(ctx); err != nil {
		return Season{}, err
	}
	if !actor.Access.CanView() {
		return Season{}, ErrGroupNotFound
	}
	if seasonID <= 0 {
		return Season{}, ErrSeasonNotFound
	}
	if !actor.Access.CanManageSeasons() {
		return Season{}, ErrGroupForbidden
	}
	if expectedVersion < 0 {
		return Season{}, ErrInvalidGroup
	}
	return s.seasons.FindSeason(ctx, actor.Access.GroupID(), seasonID)
}

func (s *SeasonService) record(ctx context.Context, actor GroupActor, action, detail string) {
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func (s *SeasonService) validate(ctx context.Context) error {
	if s == nil || s.seasons == nil || s.memberships == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil season context")
	}
	return nil
}

func seasonDetail(season Season, withParts bool) string {
	detail := "season " + strconv.FormatInt(season.ID, 10) + ": " + season.Name + "; " + FormatSeasonDate(season.StartsOn) + " to " + FormatSeasonDate(season.EndsOn)
	if withParts {
		for _, part := range season.Parts {
			detail += "; part " + strconv.Itoa(part.Position) + " " + FormatSeasonDate(part.StartsOn) + " to " + FormatSeasonDate(part.EndsOn)
			if part.AlternatingWeeks {
				detail += " A/B"
			}
		}
	}
	return detail
}

func coachDetail(seasonID, teamID, coachUserID int64) string {
	return "season " + strconv.FormatInt(seasonID, 10) + "; team " + strconv.FormatInt(teamID, 10) + "; coach " + strconv.FormatInt(coachUserID, 10)
}

// buildSeason validates season input: a name, a start before the end, and
// one or two named parts, each inside the season with a start before its
// end, the second starting after the first ends.
func buildSeason(input SeasonInput) (Season, error) {
	name, ok := seasonName(input.Name)
	if !ok {
		return Season{}, ErrInvalidSeasonName
	}
	starts, ends := SeasonDate(input.StartsOn), SeasonDate(input.EndsOn)
	if !validSeasonRange(starts, ends) {
		return Season{}, ErrInvalidSeasonDates
	}
	if len(input.Parts) < 1 || len(input.Parts) > MaxSeasonParts {
		return Season{}, ErrInvalidSeasonParts
	}
	season := Season{Name: name, StartsOn: starts, EndsOn: ends}
	for index, raw := range input.Parts {
		position := index + 1
		partName, ok := seasonName(raw.Name)
		if !ok {
			return Season{}, &SeasonPartError{Position: position, Err: ErrInvalidSeasonPartName}
		}
		part := SeasonPart{Position: position, Name: partName, StartsOn: SeasonDate(raw.StartsOn), EndsOn: SeasonDate(raw.EndsOn), AlternatingWeeks: raw.AlternatingWeeks}
		switch {
		case !validSeasonRange(part.StartsOn, part.EndsOn):
			return Season{}, &SeasonPartError{Position: position, Err: ErrInvalidSeasonPartDates}
		case part.StartsOn.Before(starts) || part.EndsOn.After(ends):
			return Season{}, &SeasonPartError{Position: position, Err: ErrSeasonPartOutsideSeason}
		case index > 0 && !part.StartsOn.After(season.Parts[index-1].EndsOn):
			return Season{}, &SeasonPartError{Position: position, Err: ErrSeasonPartsOverlap}
		}
		season.Parts = append(season.Parts, part)
	}
	return season, nil
}

func seasonName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxSeasonNameLength || hasControl(name) {
		return "", false
	}
	return name, true
}

func validSeasonRange(starts, ends time.Time) bool {
	if starts.IsZero() || ends.IsZero() || !starts.Before(ends) {
		return false
	}
	return starts.Year() >= minSeasonYear && ends.Year() <= maxSeasonYear
}

// SeasonDate returns t's calendar date as midnight UTC, the representation
// of season dates. The zero time stays zero.
func SeasonDate(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// ParseSeasonDate reads a YYYY-MM-DD date; it returns the zero time for
// anything else, which validation refuses.
func ParseSeasonDate(raw string) time.Time {
	parsed, err := time.Parse(SeasonDateLayout, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// FormatSeasonDate writes a season date as YYYY-MM-DD; "" for the zero time.
func FormatSeasonDate(day time.Time) string {
	if day.IsZero() {
		return ""
	}
	return day.Format(SeasonDateLayout)
}

// LocalDate returns the calendar date of instant in the IANA time zone.
func LocalDate(instant time.Time, timeZone string) (time.Time, error) {
	location, err := time.LoadLocation(timeZone)
	if err != nil || timeZone == "" || timeZone == "Local" {
		return time.Time{}, ErrInvalidTimeZone
	}
	return SeasonDate(instant.In(location)), nil
}

func dateInRange(day, starts, ends time.Time) bool {
	day = SeasonDate(day)
	return !day.IsZero() && !day.Before(starts) && !day.After(ends)
}

func mondayOf(day time.Time) time.Time {
	offset := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -offset)
}
