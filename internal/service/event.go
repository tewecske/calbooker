package service

import (
	"cmp"
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
	maxEventTitleLength    = 128
	maxEventLocationLength = 255
	// MaxEventTeams bounds how many teams one event may have.
	MaxEventTeams = 50
	// MaxEventCoaches bounds how many extra coaches one event may have.
	MaxEventCoaches = 20
	// MaxEventParticipants bounds the optional participant limit.
	MaxEventParticipants = 999
	// MaxEventDuration is the longest event (a tournament weekend or camp).
	MaxEventDuration = 31 * 24 * time.Hour
	// EventTimeLayout is the wire and form format of local event times
	// (the value of an <input type="datetime-local">).
	EventTimeLayout = "2006-01-02T15:04"
	// maxEventListLimit bounds one list query.
	maxEventListLimit = 500
)

// Event kinds, stored as stable codes and labelled per language.
const (
	EventKindTraining     = "training"
	EventKindMatch        = "match"
	EventKindChampionship = "championship"
	EventKindOther        = "other"
)

var eventKindOrder = []string{EventKindTraining, EventKindMatch, EventKindChampionship, EventKindOther}

// EventKinds returns every event kind in display order.
func EventKinds() []string {
	return slices.Clone(eventKindOrder)
}

// ValidEventKind reports whether kind is an event kind.
func ValidEventKind(kind string) bool {
	return slices.Contains(eventKindOrder, kind)
}

// Sign-up modes (#9): players opt in, or a coach nominates players who can
// then opt out.
const (
	EventSignupOpen      = "open"
	EventSignupNominated = "nominated"
)

// EventSignupModes returns every sign-up mode in display order.
func EventSignupModes() []string {
	return []string{EventSignupOpen, EventSignupNominated}
}

// Stable event audit action codes; the group is the target.
const (
	AuditActionEventCreated   = "group.event.created"
	AuditActionEventUpdated   = "group.event.updated"
	AuditActionEventCancelled = "group.event.cancelled"
	AuditActionEventRestored  = "group.event.restored"
	AuditActionEventDeleted   = "group.event.deleted"
)

// Event warning kinds. Warnings never block a write.
const (
	// EventWarningResourceCapacity: more teams use the resource at some
	// moment of the event than its max_concurrent_teams allows.
	EventWarningResourceCapacity = "resource_capacity"
	// EventWarningCoachBusy: one of the event's coaches is also a coach of
	// another event that overlaps it.
	EventWarningCoachBusy = "coach_busy"
)

var (
	// ErrEventNotFound identifies an event that does not exist in the group
	// or that the account may not see.
	ErrEventNotFound = errors.New("service: event not found")
	// ErrInvalidEventKind identifies a kind outside EventKinds.
	ErrInvalidEventKind = errors.New("service: invalid event kind")
	// ErrInvalidEventTitle identifies a title outside storage limits.
	ErrInvalidEventTitle = errors.New("service: invalid event title")
	// ErrInvalidEventStart identifies a missing or out-of-range start.
	ErrInvalidEventStart = errors.New("service: invalid event start")
	// ErrInvalidEventEnd identifies a missing end, an end not after the
	// start, or an event longer than MaxEventDuration.
	ErrInvalidEventEnd = errors.New("service: invalid event end")
	// ErrEventTimeSkipped identifies a wall-clock time that does not exist
	// in the group's time zone because the clocks move forward then. It is
	// wrapped in an EventFieldError naming the field.
	ErrEventTimeSkipped = errors.New("service: local time skipped by a clock change")
	// ErrEventTeamsRequired identifies an event without teams.
	ErrEventTeamsRequired = errors.New("service: event needs a team")
	// ErrInvalidEventTeams identifies too many teams, or a team that is not
	// in the group.
	ErrInvalidEventTeams = errors.New("service: invalid event teams")
	// ErrEventPlaceRequired identifies an event with neither or both of a
	// resource and a location.
	ErrEventPlaceRequired = errors.New("service: event needs a resource or a location")
	// ErrInvalidEventResource identifies a resource that is not in the group.
	ErrInvalidEventResource = errors.New("service: invalid event resource")
	// ErrEventResourceArchived identifies an archived resource chosen for a
	// new event, or newly chosen for an existing one.
	ErrEventResourceArchived = errors.New("service: event resource archived")
	// ErrInvalidEventLocation identifies a location outside storage limits.
	ErrInvalidEventLocation = errors.New("service: invalid event location")
	// ErrInvalidEventSignupMode identifies a mode outside EventSignupModes.
	ErrInvalidEventSignupMode = errors.New("service: invalid event sign-up mode")
	// ErrInvalidEventMaxParticipants identifies a limit outside
	// 1..MaxEventParticipants.
	ErrInvalidEventMaxParticipants = errors.New("service: invalid event participant limit")
	// ErrInvalidEventSignupDeadline identifies a deadline that is not before
	// the start.
	ErrInvalidEventSignupDeadline = errors.New("service: invalid event sign-up deadline")
	// ErrInvalidEventCoaches identifies too many extra coaches or invalid IDs.
	ErrInvalidEventCoaches = errors.New("service: invalid event coaches")
	// ErrEventCoachRoleMissing identifies an extra coach without the coach
	// role.
	ErrEventCoachRoleMissing = errors.New("service: event coach lacks the coach role")
	// ErrNilEventRepository identifies missing event persistence.
	ErrNilEventRepository = errors.New("service: nil event repository")
	// ErrNilEventResources identifies a missing resource source.
	ErrNilEventResources = errors.New("service: nil event resource source")
)

// EventFieldError names the time field ("starts_at", "ends_at",
// "signup_deadline") a time error is about.
type EventFieldError struct {
	Field string
	Err   error
}

func (e *EventFieldError) Error() string {
	return "event " + e.Field + ": " + e.Err.Error()
}

func (e *EventFieldError) Unwrap() error {
	return e.Err
}

// Event is a training, match, championship, or other event of one or more
// teams of a group. StartsAt, EndsAt, and SignupDeadline are instants; the
// form shows them in the group's time zone. Version covers the event, its
// teams, and its extra coaches.
type Event struct {
	ID         int64
	GroupID    int64
	Kind       string
	Title      string
	StartsAt   time.Time
	EndsAt     time.Time
	ResourceID *int64
	// ResourceName is filled on reads for display.
	ResourceName *string
	Location     *string
	SignupMode   string
	// MaxParticipants and SignupDeadline are optional; #9 enforces them.
	MaxParticipants  *int
	SignupDeadline   *time.Time
	CancelledAt      *int64
	PlanTrainingID   *int64
	GeneratedForWeek *time.Time
	EditedAt         *int64
	CreatedAt        int64
	Version          int64
	// Teams are the event's teams, by name.
	Teams []EventTeam
	// Coaches are the extra coaches (TeamID 0), on top of the teams' season
	// coaches.
	Coaches []EventCoach
}

// EventTeam is one team of an event.
type EventTeam struct {
	ID   int64
	Name string
}

// EventCoach is a coach of an event: an extra coach (TeamID 0) or a season
// coach of one of its teams. Emails are never part of it.
type EventCoach struct {
	UserID      int64
	TeamID      int64
	DisplayName *string
	Username    *string
}

// Cancelled reports whether the event is cancelled.
func (e Event) Cancelled() bool {
	return e.CancelledAt != nil
}

// Generated reports whether the event was generated from the weekly plan.
func (e Event) Generated() bool {
	return e.PlanTrainingID != nil
}

// TeamIDs returns the IDs of the event's teams.
func (e Event) TeamIDs() []int64 {
	ids := make([]int64, 0, len(e.Teams))
	for _, team := range e.Teams {
		ids = append(ids, team.ID)
	}
	return ids
}

// CoachUserIDs returns the IDs of the event's extra coaches.
func (e Event) CoachUserIDs() []int64 {
	ids := make([]int64, 0, len(e.Coaches))
	for _, coach := range e.Coaches {
		ids = append(ids, coach.UserID)
	}
	return ids
}

// EventInput is the editable part of an event. StartsAt, EndsAt, and
// SignupDeadline are wall-clock times in the group's time zone: only their
// date and clock fields count (ParseEventLocalTime builds them); the zero
// time is missing. ResourceID 0 and an empty Location are none;
// MaxParticipants 0 is no limit.
type EventInput struct {
	Kind            string
	Title           string
	StartsAt        time.Time
	EndsAt          time.Time
	TeamIDs         []int64
	ResourceID      int64
	Location        string
	SignupMode      string
	MaxParticipants int
	SignupDeadline  time.Time
	CoachUserIDs    []int64
}

// EventQuery selects events. Zero From or To leave that side open; an event
// matches when it overlaps [From, To).
type EventQuery struct {
	From time.Time
	To   time.Time
	// TeamID keeps only events of the team.
	TeamID int64
	// Descending orders by start, latest first (default: earliest first).
	Descending bool
	// Limit caps the result (default and maximum 500).
	Limit int
	// ActiveOnly drops cancelled events.
	ActiveOnly bool
	// ExcludeID drops one event (the event being checked for warnings).
	ExcludeID int64
}

// EventScope limits a query to what an account sees: every event (All), or
// events of TeamIDs plus the events UserID is an extra coach of.
type EventScope struct {
	All     bool
	TeamIDs []int64
	UserID  int64
}

// EventWarning is a soft problem of an event, shown with it.
type EventWarning struct {
	Kind string
	// Resource capacity: the resource, its limit, and the most teams using
	// it at one moment of the event.
	ResourceName string
	Limit        int
	Teams        int
	// Coach busy: the coach and the other event.
	Coach         EventCoach
	OtherEventID  int64
	OtherTitle    string
	OtherStartsAt time.Time
}

// EventDetail is an event with what its page shows.
type EventDetail struct {
	Event Event
	// TimeZone is the group's time zone, for showing the times.
	TimeZone string
	// TeamCoaches are the season coaches of the event's teams in the season
	// containing the event's first day.
	TeamCoaches []EventCoach
	// Warnings are computed for accounts that may edit the event.
	Warnings []EventWarning
	CanEdit  bool
}

// EventFormOptions fills the event form for an account.
type EventFormOptions struct {
	TimeZone string
	// Teams the account may put on an event (plus the edited event's own).
	Teams []Team
	// Resources not archived (plus the edited event's own).
	Resources []Resource
	// Coaches are members with the coach role, for extra coaches.
	Coaches []GroupMember
}

// EventRepository persists events, their teams, and their extra coaches.
// Every method is scoped to one group.
type EventRepository interface {
	GroupTimeZone(ctx context.Context, groupID int64) (string, error)
	// CreateEvent inserts the event with its teams and coaches; foreign keys
	// give ErrTeamNotFound, ErrResourceNotFound, and ErrEventCoachRoleMissing.
	CreateEvent(context.Context, Event) (Event, error)
	// FindEvent returns ErrEventNotFound for an event outside the group.
	FindEvent(ctx context.Context, groupID, eventID int64) (Event, error)
	ListEvents(ctx context.Context, groupID int64, query EventQuery, scope EventScope) ([]Event, error)
	// UpdateEvent writes the event, its teams, and its coaches at the
	// expected revision; stale writes classify through ClassifyStaleWrite.
	UpdateEvent(ctx context.Context, event Event, expectedVersion int64) (Event, error)
	// DeleteEvent removes the event at the expected revision.
	DeleteEvent(ctx context.Context, groupID, eventID, expectedVersion int64) error
	// TeamCoachesOn returns the season coaches of every team in the season
	// containing day (TeamID set), none when no season contains it.
	TeamCoachesOn(ctx context.Context, groupID int64, day time.Time) ([]EventCoach, error)
}

// EventResourceSource is the part of ResourceService events use.
type EventResourceSource interface {
	Find(ctx context.Context, access *GroupAccess, resourceID int64) (Resource, error)
	Usable(ctx context.Context, access *GroupAccess, resourceID int64) (Resource, error)
	ActiveResources(ctx context.Context, access *GroupAccess) ([]Resource, error)
}

// EventService owns event use cases. Authorization comes from the caller's
// GroupAccess.
type EventService struct {
	events      EventRepository
	teams       TeamRepository
	resources   EventResourceSource
	memberships GroupMembershipRepository
	users       UserRepository
	auditor     AuditRecorder
	now         func() time.Time
}

// NewEventService constructs event use cases with explicit dependencies.
func NewEventService(events EventRepository, teams TeamRepository, resources EventResourceSource, memberships GroupMembershipRepository, users UserRepository) (*EventService, error) {
	switch {
	case events == nil:
		return nil, ErrNilEventRepository
	case teams == nil:
		return nil, ErrNilTeamRepository
	case resources == nil:
		return nil, ErrNilEventResources
	case memberships == nil:
		return nil, ErrNilMembershipRepository
	case users == nil:
		return nil, ErrNilUserRepository
	}
	return &EventService{events: events, teams: teams, resources: resources, memberships: memberships, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *EventService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// List returns the events the account sees that match query, cancelled ones
// included. Every account that sees the group; a team the account does not
// see matches nothing.
func (s *EventService) List(ctx context.Context, access *GroupAccess, query EventQuery) ([]Event, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	if query.TeamID < 0 || (query.TeamID > 0 && !access.CanViewTeam(query.TeamID)) {
		return []Event{}, nil
	}
	if query.Limit <= 0 || query.Limit > maxEventListLimit {
		query.Limit = maxEventListLimit
	}
	query.ActiveOnly, query.ExcludeID = false, 0
	events, err := s.events.ListEvents(ctx, access.GroupID(), query, eventScope(access))
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	visible := events[:0]
	for _, event := range events {
		if access.CanViewEvent(event) {
			visible = append(visible, event)
		}
	}
	return visible, nil
}

// ListBetween returns the visible events overlapping [from, to), earliest
// first, cancelled ones included. It backs the weekly calendars (#10).
func (s *EventService) ListBetween(ctx context.Context, access *GroupAccess, from, to time.Time) ([]Event, error) {
	return s.List(ctx, access, EventQuery{From: from, To: to})
}

// Find returns one visible event; events the account may not see are
// ErrEventNotFound.
func (s *EventService) Find(ctx context.Context, access *GroupAccess, eventID int64) (Event, error) {
	if err := s.validate(ctx); err != nil {
		return Event{}, err
	}
	if !access.CanView() {
		return Event{}, ErrGroupNotFound
	}
	return s.find(ctx, access, eventID)
}

// Detail returns a visible event with its teams' coaches and, for accounts
// that may edit it, its warnings.
func (s *EventService) Detail(ctx context.Context, access *GroupAccess, eventID int64) (EventDetail, error) {
	event, err := s.Find(ctx, access, eventID)
	if err != nil {
		return EventDetail{}, err
	}
	zone, err := s.timeZone(ctx, access.GroupID())
	if err != nil {
		return EventDetail{}, err
	}
	detail := EventDetail{Event: event, TimeZone: zone, CanEdit: access.CanEditEvent(event)}
	seasonCoaches, err := s.seasonCoaches(ctx, access.GroupID(), zone, event.StartsAt)
	if err != nil {
		return EventDetail{}, err
	}
	teams := event.TeamIDs()
	for _, coach := range seasonCoaches {
		if slices.Contains(teams, coach.TeamID) {
			detail.TeamCoaches = append(detail.TeamCoaches, coach)
		}
	}
	if detail.CanEdit {
		if detail.Warnings, err = s.warnings(ctx, access, event, seasonCoaches); err != nil {
			return EventDetail{}, err
		}
	}
	return detail, nil
}

// FormOptions returns what the event form offers the account: for a new
// event (event nil) the teams it may use, active resources, and coaches;
// for an edited event also that event's own teams and resource.
func (s *EventService) FormOptions(ctx context.Context, access *GroupAccess, event *Event) (EventFormOptions, error) {
	if err := s.validate(ctx); err != nil {
		return EventFormOptions{}, err
	}
	allowed := access.CanCreateEvents()
	if event != nil {
		allowed = access.CanEditEvent(*event)
	}
	if err := authorizeGroupAction(GroupActor{Access: access}, allowed); err != nil {
		return EventFormOptions{}, err
	}
	zone, err := s.timeZone(ctx, access.GroupID())
	if err != nil {
		return EventFormOptions{}, err
	}
	options := EventFormOptions{TimeZone: zone}
	var ownTeams []int64
	var ownResource int64
	if event != nil {
		ownTeams = event.TeamIDs()
		if event.ResourceID != nil {
			ownResource = *event.ResourceID
		}
	}
	teams, err := s.teams.ListTeams(ctx, access.GroupID())
	if err != nil {
		return EventFormOptions{}, fmt.Errorf("list event teams: %w", err)
	}
	for _, summary := range teams {
		team := summary.Team
		own := slices.Contains(ownTeams, team.ID)
		if own || (!team.Archived() && access.CanCreateEvent([]int64{team.ID})) {
			options.Teams = append(options.Teams, team)
		}
	}
	if options.Resources, err = s.resources.ActiveResources(ctx, access); err != nil {
		return EventFormOptions{}, fmt.Errorf("list event resources: %w", err)
	}
	if ownResource > 0 && !slices.ContainsFunc(options.Resources, func(r Resource) bool { return r.ID == ownResource }) {
		if resource, err := s.resources.Find(ctx, access, ownResource); err == nil {
			options.Resources = append(options.Resources, resource)
		}
	}
	members, err := s.memberships.ListMembers(ctx, access.GroupID())
	if err != nil {
		return EventFormOptions{}, fmt.Errorf("list event coaches: %w", err)
	}
	for _, member := range members {
		if member.HasRole(GroupRoleCoach) {
			member.Email = nil
			options.Coaches = append(options.Coaches, member)
		}
	}
	return options, nil
}

// Create adds an event. Admins, managers, and site administrators for any
// teams; coaches for teams they all coach. It returns the event's warnings.
func (s *EventService) Create(ctx context.Context, actor GroupActor, input EventInput) (Event, []EventWarning, error) {
	if err := s.validate(ctx); err != nil {
		return Event{}, nil, err
	}
	if err := authorizeGroupAction(actor, actor.Access.CanCreateEvents()); err != nil {
		return Event{}, nil, err
	}
	zone, err := s.timeZone(ctx, actor.Access.GroupID())
	if err != nil {
		return Event{}, nil, err
	}
	event, err := buildEvent(input, zone)
	if err != nil {
		return Event{}, nil, err
	}
	if !actor.Access.CanCreateEvent(event.TeamIDs()) {
		return Event{}, nil, ErrGroupForbidden
	}
	if err := s.checkResource(ctx, actor.Access, event.ResourceID, nil); err != nil {
		return Event{}, nil, err
	}
	event.GroupID = actor.Access.GroupID()
	event.CreatedAt = s.now().Unix()
	created, err := s.events.CreateEvent(ctx, event)
	if err != nil {
		return Event{}, nil, mapEventWriteError("create event", err)
	}
	s.record(ctx, actor, AuditActionEventCreated, created)
	return created, s.writeWarnings(ctx, actor.Access, zone, created), nil
}

// Update replaces an event's fields, teams, and extra coaches at the
// expected revision. A coach must coach every team the event had and every
// team it gets. Editing a generated event marks it edited by hand.
func (s *EventService) Update(ctx context.Context, actor GroupActor, eventID, expectedVersion int64, input EventInput) (Event, []EventWarning, error) {
	existing, err := s.findEditable(ctx, actor, eventID, expectedVersion)
	if err != nil {
		return Event{}, nil, err
	}
	zone, err := s.timeZone(ctx, actor.Access.GroupID())
	if err != nil {
		return Event{}, nil, err
	}
	event, err := buildEvent(input, zone)
	if err != nil {
		return Event{}, nil, err
	}
	if !actor.Access.CanCreateEvent(event.TeamIDs()) {
		return Event{}, nil, ErrGroupForbidden
	}
	if err := s.checkResource(ctx, actor.Access, event.ResourceID, existing.ResourceID); err != nil {
		return Event{}, nil, err
	}
	event.ID, event.GroupID, event.CreatedAt, event.Version = existing.ID, existing.GroupID, existing.CreatedAt, existing.Version
	event.CancelledAt, event.PlanTrainingID, event.GeneratedForWeek, event.EditedAt = existing.CancelledAt, existing.PlanTrainingID, existing.GeneratedForWeek, existing.EditedAt
	s.markEdited(&event)
	updated, err := s.update(ctx, event, expectedVersion)
	if err != nil {
		return Event{}, nil, err
	}
	s.record(ctx, actor, AuditActionEventUpdated, updated)
	return updated, s.writeWarnings(ctx, actor.Access, zone, updated), nil
}

// SetCancelled cancels or restores an event at the expected revision. A
// cancelled event stays and is shown as cancelled; cancelling a generated
// event marks it edited by hand, so regenerating keeps it cancelled.
func (s *EventService) SetCancelled(ctx context.Context, actor GroupActor, eventID, expectedVersion int64, cancelled bool) (Event, error) {
	event, err := s.findEditable(ctx, actor, eventID, expectedVersion)
	if err != nil {
		return Event{}, err
	}
	if event.Cancelled() == cancelled {
		return event, nil
	}
	action := AuditActionEventRestored
	event.CancelledAt = nil
	if cancelled {
		now := s.now().Unix()
		event.CancelledAt = &now
		action = AuditActionEventCancelled
	}
	s.markEdited(&event)
	updated, err := s.update(ctx, event, expectedVersion)
	if err != nil {
		return Event{}, err
	}
	s.record(ctx, actor, action, updated)
	return updated, nil
}

// Delete removes an event at the expected revision with its teams and
// extra coaches.
func (s *EventService) Delete(ctx context.Context, actor GroupActor, eventID, expectedVersion int64) error {
	event, err := s.findEditable(ctx, actor, eventID, expectedVersion)
	if err != nil {
		return err
	}
	err = s.events.DeleteEvent(ctx, event.GroupID, event.ID, expectedVersion)
	switch {
	case err == nil:
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrEventNotFound):
		return ErrEventNotFound
	case errors.Is(err, ErrOptimisticLockConflict):
		return err
	default:
		return fmt.Errorf("delete event: %w", err)
	}
	s.record(ctx, actor, AuditActionEventDeleted, event)
	return nil
}

func (s *EventService) find(ctx context.Context, access *GroupAccess, eventID int64) (Event, error) {
	if eventID <= 0 {
		return Event{}, ErrEventNotFound
	}
	event, err := s.events.FindEvent(ctx, access.GroupID(), eventID)
	if err != nil {
		if errors.Is(err, ErrEventNotFound) {
			return Event{}, err
		}
		return Event{}, fmt.Errorf("find event: %w", err)
	}
	if !access.CanViewEvent(event) {
		return Event{}, ErrEventNotFound
	}
	return event, nil
}

// findEditable loads an event for a write: not visible is not-found, visible
// but not editable is forbidden.
func (s *EventService) findEditable(ctx context.Context, actor GroupActor, eventID, expectedVersion int64) (Event, error) {
	if err := s.validate(ctx); err != nil {
		return Event{}, err
	}
	if !actor.Access.CanView() {
		return Event{}, ErrGroupNotFound
	}
	event, err := s.find(ctx, actor.Access, eventID)
	if err != nil {
		return Event{}, err
	}
	if !actor.Access.CanEditEvent(event) {
		return Event{}, ErrGroupForbidden
	}
	if expectedVersion < 0 {
		return Event{}, ErrInvalidGroup
	}
	return event, nil
}

// checkResource validates the chosen resource: it must be in the group, and
// not archived unless it is the event's existing resource.
func (s *EventService) checkResource(ctx context.Context, access *GroupAccess, chosen, existing *int64) error {
	if chosen == nil {
		return nil
	}
	var err error
	if existing != nil && *existing == *chosen {
		_, err = s.resources.Find(ctx, access, *chosen)
	} else {
		_, err = s.resources.Usable(ctx, access, *chosen)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrResourceNotFound):
		return ErrInvalidEventResource
	case errors.Is(err, ErrResourceArchived):
		return ErrEventResourceArchived
	default:
		return fmt.Errorf("find event resource: %w", err)
	}
}

func (s *EventService) markEdited(event *Event) {
	if event.Generated() {
		now := s.now().Unix()
		event.EditedAt = &now
	}
}

func (s *EventService) update(ctx context.Context, event Event, expectedVersion int64) (Event, error) {
	updated, err := s.events.UpdateEvent(ctx, event, expectedVersion)
	switch {
	case err == nil:
		return updated, nil
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrEventNotFound):
		// An event that vanished is not-found, never a conflict.
		return Event{}, ErrEventNotFound
	case errors.Is(err, ErrOptimisticLockConflict):
		return Event{}, err
	default:
		return Event{}, mapEventWriteError("update event", err)
	}
}

// writeWarnings computes the warnings after a write; a failure to compute
// them never fails the completed write.
func (s *EventService) writeWarnings(ctx context.Context, access *GroupAccess, zone string, event Event) []EventWarning {
	coaches, err := s.seasonCoaches(ctx, access.GroupID(), zone, event.StartsAt)
	if err != nil {
		return nil
	}
	warnings, err := s.warnings(ctx, access, event, coaches)
	if err != nil {
		return nil
	}
	return warnings
}

// warnings finds the resource over its concurrent-team limit and coaches
// busy in another overlapping event. Cancelled events have none and count
// for none. Coaches are the extra coaches plus the season coaches of the
// teams, in the season containing this event's first day.
func (s *EventService) warnings(ctx context.Context, access *GroupAccess, event Event, seasonCoaches []EventCoach) ([]EventWarning, error) {
	if event.Cancelled() {
		return nil, nil
	}
	others, err := s.events.ListEvents(ctx, access.GroupID(), EventQuery{From: event.StartsAt, To: event.EndsAt, ActiveOnly: true, ExcludeID: event.ID, Limit: maxEventListLimit}, EventScope{All: true})
	if err != nil {
		return nil, fmt.Errorf("list overlapping events: %w", err)
	}
	var warnings []EventWarning
	if event.ResourceID != nil {
		resource, err := s.resources.Find(ctx, access, *event.ResourceID)
		if err != nil && !errors.Is(err, ErrResourceNotFound) {
			return nil, fmt.Errorf("find event resource: %w", err)
		}
		if err == nil && resource.MaxConcurrentTeams != nil {
			if peak := peakResourceTeams(event, others); peak > *resource.MaxConcurrentTeams {
				warnings = append(warnings, EventWarning{Kind: EventWarningResourceCapacity, ResourceName: resource.Name, Limit: *resource.MaxConcurrentTeams, Teams: peak})
			}
		}
	}
	byTeam := make(map[int64][]EventCoach)
	for _, coach := range seasonCoaches {
		byTeam[coach.TeamID] = append(byTeam[coach.TeamID], coach)
	}
	mine := eventCoachSet(event, byTeam)
	for _, other := range others {
		theirs := eventCoachSet(other, byTeam)
		for _, coach := range mine {
			if _, busy := theirs[coach.UserID]; busy {
				warnings = append(warnings, EventWarning{Kind: EventWarningCoachBusy, Coach: coach, OtherEventID: other.ID, OtherTitle: other.Title, OtherStartsAt: other.StartsAt})
			}
		}
	}
	slices.SortStableFunc(warnings, func(a, b EventWarning) int {
		if a.Kind != b.Kind {
			// Resource capacity first, then busy coaches.
			if a.Kind == EventWarningResourceCapacity {
				return -1
			}
			return 1
		}
		if c := a.OtherStartsAt.Compare(b.OtherStartsAt); c != 0 {
			return c
		}
		return cmp.Compare(a.Coach.UserID, b.Coach.UserID)
	})
	return warnings, nil
}

// eventCoachSet returns the event's coaches by user: extra coaches and the
// season coaches of its teams.
func eventCoachSet(event Event, byTeam map[int64][]EventCoach) map[int64]EventCoach {
	set := make(map[int64]EventCoach)
	for _, teamID := range event.TeamIDs() {
		for _, coach := range byTeam[teamID] {
			if _, ok := set[coach.UserID]; !ok {
				set[coach.UserID] = coach
			}
		}
	}
	for _, coach := range event.Coaches {
		if _, ok := set[coach.UserID]; !ok {
			set[coach.UserID] = coach
		}
	}
	return set
}

// peakResourceTeams returns the most teams using the event's resource at
// one moment of the event: its own teams plus those of overlapping events on
// the same resource.
func peakResourceTeams(event Event, others []Event) int {
	type change struct {
		at    time.Time
		delta int
	}
	changes := []change{{event.StartsAt, len(event.Teams)}, {event.EndsAt, -len(event.Teams)}}
	for _, other := range others {
		if other.ResourceID == nil || *other.ResourceID != *event.ResourceID || other.Cancelled() {
			continue
		}
		starts, ends := maxTime(other.StartsAt, event.StartsAt), minTime(other.EndsAt, event.EndsAt)
		if !starts.Before(ends) {
			continue
		}
		changes = append(changes, change{starts, len(other.Teams)}, change{ends, -len(other.Teams)})
	}
	// Ends before starts at the same instant: back-to-back events do not
	// overlap.
	slices.SortFunc(changes, func(a, b change) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return cmp.Compare(a.delta, b.delta)
	})
	peak, current := 0, 0
	for _, step := range changes {
		current += step.delta
		peak = max(peak, current)
	}
	return peak
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *EventService) seasonCoaches(ctx context.Context, groupID int64, zone string, startsAt time.Time) ([]EventCoach, error) {
	day, err := LocalDate(startsAt, zone)
	if err != nil {
		return nil, err
	}
	coaches, err := s.events.TeamCoachesOn(ctx, groupID, day)
	if err != nil {
		return nil, fmt.Errorf("list event team coaches: %w", err)
	}
	return coaches, nil
}

func (s *EventService) timeZone(ctx context.Context, groupID int64) (string, error) {
	zone, err := s.events.GroupTimeZone(ctx, groupID)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			return "", err
		}
		return "", fmt.Errorf("find group time zone: %w", err)
	}
	return zone, nil
}

func (s *EventService) record(ctx context.Context, actor GroupActor, action string, event Event) {
	detail := "event " + strconv.FormatInt(event.ID, 10) + ": " + event.Title + "; " + event.Kind +
		"; " + event.StartsAt.UTC().Format(time.RFC3339) + " to " + event.EndsAt.UTC().Format(time.RFC3339)
	if action == AuditActionEventCreated || action == AuditActionEventUpdated {
		detail += "; teams " + joinIDs(event.TeamIDs())
		if event.ResourceID != nil {
			detail += "; resource " + strconv.FormatInt(*event.ResourceID, 10)
		} else {
			detail += "; location"
		}
		if coaches := event.CoachUserIDs(); len(coaches) > 0 {
			detail += "; extra coaches " + joinIDs(coaches)
		}
		detail += "; sign-up " + event.SignupMode
	}
	if event.Generated() {
		detail += "; generated"
	}
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

func (s *EventService) validate(ctx context.Context) error {
	if s == nil || s.events == nil || s.teams == nil || s.resources == nil || s.memberships == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil event context")
	}
	return nil
}

func eventScope(access *GroupAccess) EventScope {
	if access.SeesAllTeams() {
		return EventScope{All: true}
	}
	return EventScope{TeamIDs: access.VisibleTeamIDs(), UserID: access.UserID()}
}

func mapEventWriteError(operation string, err error) error {
	switch {
	case errors.Is(err, ErrTeamNotFound):
		return ErrInvalidEventTeams
	case errors.Is(err, ErrResourceNotFound):
		return ErrInvalidEventResource
	case errors.Is(err, ErrEventCoachRoleMissing), errors.Is(err, ErrEventNotFound), errors.Is(err, ErrOptimisticLockConflict):
		return err
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

// buildEvent validates event input and converts its wall-clock times in
// zone to instants (EventInstant).
func buildEvent(input EventInput, zone string) (Event, error) {
	kind := strings.TrimSpace(input.Kind)
	if !ValidEventKind(kind) {
		return Event{}, ErrInvalidEventKind
	}
	title := strings.TrimSpace(input.Title)
	if title == "" || utf8.RuneCountInString(title) > maxEventTitleLength || hasControl(title) {
		return Event{}, ErrInvalidEventTitle
	}
	if !validEventWallTime(input.StartsAt) {
		return Event{}, ErrInvalidEventStart
	}
	if !validEventWallTime(input.EndsAt) {
		return Event{}, ErrInvalidEventEnd
	}
	starts, err := EventInstant(input.StartsAt, zone)
	if err != nil {
		return Event{}, eventTimeError("starts_at", err)
	}
	ends, err := EventInstant(input.EndsAt, zone)
	if err != nil {
		return Event{}, eventTimeError("ends_at", err)
	}
	if !starts.Before(ends) || ends.Sub(starts) > MaxEventDuration {
		return Event{}, ErrInvalidEventEnd
	}
	event := Event{Kind: kind, Title: title, StartsAt: starts, EndsAt: ends}
	teamIDs, ok := uniquePositiveIDs(input.TeamIDs)
	switch {
	case !ok || len(teamIDs) > MaxEventTeams:
		return Event{}, ErrInvalidEventTeams
	case len(teamIDs) == 0:
		return Event{}, ErrEventTeamsRequired
	}
	for _, id := range teamIDs {
		event.Teams = append(event.Teams, EventTeam{ID: id})
	}
	location := strings.TrimSpace(input.Location)
	switch {
	case input.ResourceID < 0:
		return Event{}, ErrInvalidEventResource
	case (input.ResourceID > 0) == (location != ""):
		return Event{}, ErrEventPlaceRequired
	case input.ResourceID > 0:
		resourceID := input.ResourceID
		event.ResourceID = &resourceID
	default:
		if utf8.RuneCountInString(location) > maxEventLocationLength || hasControl(location) {
			return Event{}, ErrInvalidEventLocation
		}
		event.Location = &location
	}
	event.SignupMode = strings.TrimSpace(input.SignupMode)
	if !slices.Contains(EventSignupModes(), event.SignupMode) {
		return Event{}, ErrInvalidEventSignupMode
	}
	switch {
	case input.MaxParticipants == 0:
	case input.MaxParticipants < 1 || input.MaxParticipants > MaxEventParticipants:
		return Event{}, ErrInvalidEventMaxParticipants
	default:
		limit := input.MaxParticipants
		event.MaxParticipants = &limit
	}
	if !input.SignupDeadline.IsZero() {
		if !validEventWallTime(input.SignupDeadline) {
			return Event{}, ErrInvalidEventSignupDeadline
		}
		deadline, err := EventInstant(input.SignupDeadline, zone)
		if err != nil {
			return Event{}, eventTimeError("signup_deadline", err)
		}
		if !deadline.Before(starts) {
			return Event{}, ErrInvalidEventSignupDeadline
		}
		event.SignupDeadline = &deadline
	}
	coachIDs, ok := uniquePositiveIDs(input.CoachUserIDs)
	if !ok || len(coachIDs) > MaxEventCoaches {
		return Event{}, ErrInvalidEventCoaches
	}
	for _, id := range coachIDs {
		event.Coaches = append(event.Coaches, EventCoach{UserID: id})
	}
	return event, nil
}

func eventTimeError(field string, err error) error {
	if errors.Is(err, ErrEventTimeSkipped) {
		return &EventFieldError{Field: field, Err: err}
	}
	return err
}

func validEventWallTime(wall time.Time) bool {
	return !wall.IsZero() && wall.Year() >= minSeasonYear && wall.Year() <= maxSeasonYear
}

// uniquePositiveIDs sorts and deduplicates ids; false when one is not
// positive.
func uniquePositiveIDs(ids []int64) ([]int64, bool) {
	unique := slices.Clone(ids)
	for _, id := range unique {
		if id <= 0 {
			return nil, false
		}
	}
	slices.Sort(unique)
	return slices.Compact(unique), true
}

// ParseEventLocalTime reads a YYYY-MM-DDTHH:MM wall-clock time (seconds
// allowed and dropped); it returns the zero time for anything else, which
// validation refuses. Only the date and clock fields of the result count.
func ParseEventLocalTime(raw string) time.Time {
	value := strings.TrimSpace(raw)
	if len(value) == len(EventTimeLayout)+3 {
		value = value[:len(EventTimeLayout)]
	}
	parsed, err := time.Parse(EventTimeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// FormatEventLocalTime writes a wall-clock time as YYYY-MM-DDTHH:MM; "" for
// the zero time.
func FormatEventLocalTime(wall time.Time) string {
	if wall.IsZero() {
		return ""
	}
	return wall.Format(EventTimeLayout)
}

// EventLocalTime returns the wall-clock time of instant in the IANA time
// zone, in the form EventInstant reads (fields only, UTC location).
func EventLocalTime(instant time.Time, timeZone string) (time.Time, error) {
	location, err := eventLocation(timeZone)
	if err != nil {
		return time.Time{}, err
	}
	local := instant.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, time.UTC), nil
}

// EventInstant converts a wall-clock time in the IANA time zone to an
// instant. Daylight-saving transitions:
//
//   - A time the clocks skip (in Europe/Budapest 02:00–02:59 on the last
//     Sunday of March) does not exist: ErrEventTimeSkipped. The form asks
//     for another time rather than silently moving the event.
//   - A time that happens twice (02:00–02:59 on the last Sunday of October)
//     is read as its first occurrence, still on summer time.
//
// Either way an event keeps the wall-clock time it was entered with; an
// event across the change lasts an hour less or more.
func EventInstant(wall time.Time, timeZone string) (time.Time, error) {
	location, err := eventLocation(timeZone)
	if err != nil {
		return time.Time{}, err
	}
	naive := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, time.UTC)
	var (
		found  bool
		result time.Time
	)
	// Every offset in force within a day of the time is a candidate; a
	// candidate counts when it reads back as the same wall-clock time.
	for _, probe := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		_, offset := naive.Add(probe).In(location).Zone()
		candidate := naive.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(location)
		if local.Year() != naive.Year() || local.Month() != naive.Month() || local.Day() != naive.Day() || local.Hour() != naive.Hour() || local.Minute() != naive.Minute() {
			continue
		}
		if !found || candidate.Before(result) {
			found, result = true, candidate
		}
	}
	if !found {
		return time.Time{}, ErrEventTimeSkipped
	}
	return result.In(location), nil
}

func eventLocation(timeZone string) (*time.Location, error) {
	if timeZone == "" || timeZone == "Local" {
		return nil, ErrInvalidTimeZone
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return nil, ErrInvalidTimeZone
	}
	return location, nil
}
