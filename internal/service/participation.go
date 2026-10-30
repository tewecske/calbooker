package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// Participant answers (event_participants.status). A walk-in a coach added
// after the event without an answer has no status ("").
const (
	// ParticipantSignedUp is "I'll be there" in an open event.
	ParticipantSignedUp = "signed_up"
	// ParticipantDeclined is "Can't come", or a nominated player who opted
	// out.
	ParticipantDeclined = "declined"
	// ParticipantNominated is a player picked by a coach. A called-up
	// player (Participant.CalledUp) is nominated too.
	ParticipantNominated = "nominated"
	// ParticipantLeftOut is a player of the event's teams a coach left out
	// (#47).
	ParticipantLeftOut = "left_out"
)

// ParticipantStatuses returns every answer in display order.
func ParticipantStatuses() []string {
	return []string{ParticipantSignedUp, ParticipantNominated, ParticipantDeclined, ParticipantLeftOut}
}

// Stable participation audit action codes; the group is the target. Only
// changes made with staff or coach rights to someone else's participation
// are audited; a player's or parent's own answer is self-service.
const (
	AuditActionParticipantNominated = "group.event.participant_nominated"
	AuditActionParticipantUpdated   = "group.event.participant_updated"
	AuditActionParticipantRemoved   = "group.event.participant_removed"
	AuditActionAttendanceRecorded   = "group.event.attendance_recorded"
	AuditActionParticipantCalledUp  = "group.event.participant_called_up"
	AuditActionParticipantLeftOut   = "group.event.participant_left_out"
)

// maxAttendanceMarks bounds one attendance submission.
const maxAttendanceMarks = 1000

var (
	// ErrParticipantNotEligible identifies a member who is not a player of
	// the event's teams (sign-up), or not a player of the group (walk-ins).
	ErrParticipantNotEligible = errors.New("service: member cannot take part in the event")
	// ErrInvalidParticipantStatus identifies an answer that is not allowed
	// for the event's sign-up mode or for who gives it.
	ErrInvalidParticipantStatus = errors.New("service: invalid participant status")
	// ErrParticipationCancelled identifies a sign-up or attendance change on
	// a cancelled event.
	ErrParticipationCancelled = errors.New("service: event is cancelled")
	// ErrParticipationStarted identifies a sign-up change after the event
	// started.
	ErrParticipationStarted = errors.New("service: event has started")
	// ErrSignupDeadlinePassed identifies a player's or parent's change after
	// the sign-up deadline.
	ErrSignupDeadlinePassed = errors.New("service: sign-up deadline passed")
	// ErrEventFull identifies a player's or parent's sign-up when the
	// participant limit is reached.
	ErrEventFull = errors.New("service: event is full")
	// ErrAttendanceTooEarly identifies attendance recorded before the event
	// ended.
	ErrAttendanceTooEarly = errors.New("service: event has not ended")
	// ErrInvalidAttendance identifies a malformed attendance submission.
	ErrInvalidAttendance = errors.New("service: invalid attendance")
	// ErrEventHasParticipants identifies deleting an event with participant
	// rows; such events are cancelled instead.
	ErrEventHasParticipants = errors.New("service: event has participants")
	// ErrNilParticipantRepository identifies missing participant persistence.
	ErrNilParticipantRepository = errors.New("service: nil participant repository")
	// ErrNilParticipationEvents identifies a missing event source.
	ErrNilParticipationEvents = errors.New("service: nil participation event source")
)

// Participant is one member's row for one event: the answer and, after the
// event, the confirmed attendance (nil until a coach confirms it).
type Participant struct {
	EventID int64
	UserID  int64
	Status  string
	// CalledUp: a coach called the player up for this event from outside its
	// teams (#47). The status is nominated, or declined after an opt-out.
	CalledUp  bool
	Attended  *bool
	UpdatedBy *int64
	UpdatedAt int64
}

// Counts reports whether the row takes a place against the participant
// limit (signed up or nominated).
func (p *Participant) Counts() bool {
	return p != nil && countsAgainstLimit(p.Status)
}

func countsAgainstLimit(status string) bool {
	return status == ParticipantSignedUp || status == ParticipantNominated
}

// EventPlayer is a member who may appear on an event's participant list: a
// member with the player role, or one with a row for the event. TeamIDs are
// the teams they play on.
type EventPlayer struct {
	UserID      int64
	Kind        UserKind
	DisplayName *string
	Username    *string
	Email       *string
	IsPlayer    bool
	TeamIDs     []int64
}

// ParticipationEvent is the part of an event the participation rules read,
// loaded under the event's row lock.
type ParticipationEvent struct {
	StartsAt        time.Time
	EndsAt          time.Time
	SignupMode      string
	MaxParticipants *int
	SignupDeadline  *time.Time
	Cancelled       bool
}

func participationEventOf(event Event) ParticipationEvent {
	return ParticipationEvent{
		StartsAt: event.StartsAt, EndsAt: event.EndsAt, SignupMode: event.SignupMode,
		MaxParticipants: event.MaxParticipants, SignupDeadline: event.SignupDeadline, Cancelled: event.Cancelled(),
	}
}

// ParticipationState is what a participant change is decided from, read in
// the writing transaction with the event row locked: the event, the
// member's current row (nil for none), and the places taken.
type ParticipationState struct {
	Event   ParticipationEvent
	Current *Participant
	Taken   int
}

// ParticipantDecision decides a change from the locked state. It returns the
// row to write, nil to delete the row, or an error to write nothing.
type ParticipantDecision func(ParticipationState) (*Participant, error)

// AttendanceMark is one player's confirmed attendance.
type AttendanceMark struct {
	UserID   int64
	Attended bool
}

// AttendanceRecord is one confirmed attendance for statistics (#11): the
// event, its teams, the player, and the event's start.
type AttendanceRecord struct {
	EventID  int64
	TeamIDs  []int64
	UserID   int64
	StartsAt time.Time
}

// ParticipantRepository persists event participants. Every method is scoped
// to one group.
type ParticipantRepository interface {
	// ListEventPlayers returns the group's members with the player role
	// plus anyone with a row for the event, by name.
	ListEventPlayers(ctx context.Context, groupID, eventID int64) ([]EventPlayer, error)
	ListParticipants(ctx context.Context, groupID, eventID int64) ([]Participant, error)
	// ChangeParticipant locks the event row, reads the member's row and the
	// places taken, and writes what decide returns, in one transaction. It
	// returns ErrEventNotFound when the event is gone.
	ChangeParticipant(ctx context.Context, groupID, eventID, userID int64, decide ParticipantDecision) (*Participant, error)
	// RecordAttendance locks the event row, runs check on it, and stores the
	// marks in one transaction: attended players get a row (a walk-in
	// without an answer included); absent players' rows are marked absent,
	// and a walk-in marked absent is removed.
	RecordAttendance(ctx context.Context, groupID, eventID int64, marks []AttendanceMark, by, at int64, check func(ParticipationEvent) error) error
	// ListConfirmedAttendance returns attended rows of events of the group
	// that are not cancelled and start in [from, to), earliest first.
	ListConfirmedAttendance(ctx context.Context, groupID int64, from, to time.Time) ([]AttendanceRecord, error)
	// ListAnswers returns the answers (rows with a status) of userIDs for
	// events of the group that overlap [from, to), with the members' names,
	// ordered by event and member.
	ListAnswers(ctx context.Context, groupID int64, userIDs []int64, from, to time.Time) ([]CalendarAnswer, error)
	// ListOverlaps returns the user's other events of the group that overlap
	// [from, to), not cancelled, excluding excludeEventID: events the user
	// is signed up or called up for, and events of the user's teams the user
	// has not declined or been left out of. Earliest first.
	ListOverlaps(ctx context.Context, groupID, userID int64, from, to time.Time, excludeEventID int64) ([]OverlapEvent, error)
	// CalledUpEventIDs returns the events of the group the users are called
	// up for (rows with called_up), for event visibility.
	CalledUpEventIDs(ctx context.Context, groupID int64, userIDs []int64) ([]int64, error)
}

// ParticipationEvents is the part of EventService participation uses.
type ParticipationEvents interface {
	Detail(ctx context.Context, access *GroupAccess, eventID int64) (EventDetail, error)
}

// ParticipationRoster is an event's participant list as one account sees
// it, with what that account may do.
type ParticipationRoster struct {
	Event Event
	// Entries are the players of the event's teams plus everyone with a row,
	// by name.
	Entries []ParticipationEntry
	// Taken is how many are signed up or nominated; Limit is the optional
	// participant limit.
	Taken int
	Limit *int
	// Phase is "open", "closed" (sign-up deadline passed), "started",
	// "ended", or "cancelled".
	Phase string
	// CanManage: the account manages participants (staff and the event's
	// coaches). ShowEmails follows it.
	CanManage  bool
	ShowEmails bool
	// AttendanceOpen: the event ended, is not cancelled, and the account
	// records attendance. AttendanceRecorded: someone confirmed attendance.
	AttendanceOpen     bool
	AttendanceRecorded bool
	// WalkIns are the group's other players, offered as walk-ins while
	// attendance is open.
	WalkIns []EventPlayer
	// CallUps are the group's players outside the event's teams without a
	// row, offered to the event's coaches while the event has not started.
	CallUps []EventPlayer
}

// ParticipationEntry is one player on the list.
type ParticipationEntry struct {
	Player EventPlayer
	// OnEventTeam: the player plays on one of the event's teams.
	OnEventTeam bool
	// CalledUp: a coach called the player up from outside the event's teams.
	CalledUp bool
	Status   string
	Attended *bool
	// Self: the account itself; Child: a linked child of the account.
	Self  bool
	Child bool
	// Choices are the answers the account may give for this player now;
	// CanRemove: the account may clear the player's answer.
	Choices   []string
	CanRemove bool
}

// Participation phases.
const (
	ParticipationOpen      = "open"
	ParticipationClosed    = "closed"
	ParticipationStarted   = "started"
	ParticipationEnded     = "ended"
	ParticipationCancelled = "cancelled"
)

// ParticipationService owns sign-up and attendance use cases. Authorization
// comes from the caller's GroupAccess.
type ParticipationService struct {
	events       ParticipationEvents
	participants ParticipantRepository
	users        UserRepository
	auditor      AuditRecorder
	now          func() time.Time
}

// NewParticipationService constructs participation use cases.
func NewParticipationService(events ParticipationEvents, participants ParticipantRepository, users UserRepository) (*ParticipationService, error) {
	switch {
	case events == nil:
		return nil, ErrNilParticipationEvents
	case participants == nil:
		return nil, ErrNilParticipantRepository
	case users == nil:
		return nil, ErrNilUserRepository
	}
	return &ParticipationService{events: events, participants: participants, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *ParticipationService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Roster returns the event's participant list for the account. detail comes
// from EventService.Detail for the same account; invisible events are
// ErrEventNotFound.
func (s *ParticipationService) Roster(ctx context.Context, access *GroupAccess, detail EventDetail) (ParticipationRoster, error) {
	if err := s.validate(ctx); err != nil {
		return ParticipationRoster{}, err
	}
	event := detail.Event
	if !access.CanView() {
		return ParticipationRoster{}, ErrGroupNotFound
	}
	if !access.CanViewEvent(event) {
		return ParticipationRoster{}, ErrEventNotFound
	}
	players, rows, err := s.load(ctx, access.GroupID(), event.ID)
	if err != nil {
		return ParticipationRoster{}, err
	}
	coachIDs := teamCoachIDs(detail)
	now := s.now()
	state := participationEventOf(event)
	roster := ParticipationRoster{
		Event:     event,
		Limit:     event.MaxParticipants,
		Phase:     participationPhase(state, now),
		CanManage: access.CanManageParticipants(event, coachIDs),
	}
	roster.ShowEmails = roster.CanManage
	roster.AttendanceOpen = roster.Phase == ParticipationEnded && access.CanRecordAttendance(event, coachIDs)
	byUser := make(map[int64]Participant, len(rows))
	for _, row := range rows {
		byUser[row.UserID] = row
		if row.Counts() {
			roster.Taken++
		}
		if row.Attended != nil {
			roster.AttendanceRecorded = true
		}
	}
	teams := event.TeamIDs()
	for _, player := range players {
		onTeam := slices.ContainsFunc(player.TeamIDs, func(id int64) bool { return slices.Contains(teams, id) })
		row, hasRow := byUser[player.UserID]
		if !roster.ShowEmails {
			player.Email = nil
		}
		if !onTeam && !hasRow {
			if roster.AttendanceOpen && player.IsPlayer {
				roster.WalkIns = append(roster.WalkIns, player)
			}
			if roster.CanManage && player.IsPlayer && (roster.Phase == ParticipationOpen || roster.Phase == ParticipationClosed) {
				roster.CallUps = append(roster.CallUps, player)
			}
			continue
		}
		entry := ParticipationEntry{Player: player, OnEventTeam: onTeam, CalledUp: hasRow && row.CalledUp, Self: player.UserID == access.UserID(), Child: access.IsGuardianOf(player.UserID)}
		var current *Participant
		if hasRow {
			entry.Status, entry.Attended = row.Status, row.Attended
			current = &row
		}
		manage := roster.CanManage
		if (onTeam || entry.CalledUp) && (manage || access.CanRespondFor(event, player.UserID)) {
			for _, status := range ParticipantStatuses() {
				if status != entry.Status && checkParticipation(state, now, current, roster.Taken, status, manage) == nil {
					entry.Choices = append(entry.Choices, status)
				}
			}
			entry.CanRemove = manage && entry.Status != "" && checkParticipation(state, now, current, roster.Taken, "", manage) == nil
		}
		roster.Entries = append(roster.Entries, entry)
	}
	return roster, nil
}

// Respond sets userID's answer to the event; status "" removes it. Players
// answer for themselves and linked parents for their children (open events:
// signed up or declined; nominated events: decline, and re-accept after
// declining), until the sign-up deadline or, without one, the start, and only
// while the participant limit allows a new place. Staff and the event's
// coaches set any answer of the mode (nominated events: nominate; open
// events: signed up or declined) or remove it until the start, whatever the
// deadline and the limit. Cancelled events take no changes.
func (s *ParticipationService) Respond(ctx context.Context, actor GroupActor, eventID, userID int64, status string) (*Participant, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	access := actor.Access
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	detail, err := s.events.Detail(ctx, access, eventID)
	if err != nil {
		return nil, err
	}
	event := detail.Event
	manage := access.CanManageParticipants(event, teamCoachIDs(detail))
	self := access.CanRespondFor(event, userID)
	if !manage && !self {
		return nil, ErrGroupForbidden
	}
	if status != "" && !slices.Contains(ParticipantStatuses(), status) {
		return nil, ErrInvalidParticipantStatus
	}
	players, err := s.participants.ListEventPlayers(ctx, access.GroupID(), event.ID)
	if err != nil {
		return nil, fmt.Errorf("list event players: %w", err)
	}
	teams := event.TeamIDs()
	onTeam := slices.ContainsFunc(players, func(player EventPlayer) bool {
		return player.UserID == userID && player.IsPlayer && slices.ContainsFunc(player.TeamIDs, func(id int64) bool { return slices.Contains(teams, id) })
	})
	// Only players of the event's teams are left out; players of other
	// teams answer only when a coach called them up (checked on the row).
	if status == ParticipantLeftOut && !onTeam {
		return nil, ErrParticipantNotEligible
	}
	now := s.now()
	actorID := access.UserID()
	var previous *Participant
	changed := false
	written, err := s.participants.ChangeParticipant(ctx, access.GroupID(), event.ID, userID, func(state ParticipationState) (*Participant, error) {
		previous, changed = state.Current, false
		if !onTeam && (state.Current == nil || !state.Current.CalledUp) {
			return nil, ErrParticipantNotEligible
		}
		if err := checkParticipation(state.Event, now, state.Current, state.Taken, status, manage); err != nil {
			return nil, err
		}
		current := ""
		if state.Current != nil {
			current = state.Current.Status
		}
		if current == status {
			return state.Current, nil
		}
		changed = true
		if status == "" {
			if state.Current != nil && state.Current.Attended != nil {
				// Attendance is kept; only the answer goes.
				row := *state.Current
				row.Status, row.CalledUp, row.UpdatedBy, row.UpdatedAt = "", false, &actorID, now.Unix()
				return &row, nil
			}
			return nil, nil
		}
		row := Participant{EventID: event.ID, UserID: userID, Status: status, UpdatedBy: &actorID, UpdatedAt: now.Unix()}
		if state.Current != nil {
			row.Attended, row.CalledUp = state.Current.Attended, state.Current.CalledUp
		}
		return &row, nil
	})
	if err != nil {
		return nil, mapParticipationError("change participant", err)
	}
	if changed && userID != access.UserID() && !access.IsGuardianOf(userID) {
		action := AuditActionParticipantUpdated
		switch {
		case status == "":
			action = AuditActionParticipantRemoved
		case status == ParticipantNominated:
			action = AuditActionParticipantNominated
		case status == ParticipantLeftOut:
			action = AuditActionParticipantLeftOut
		}
		from := "none"
		if previous != nil && previous.Status != "" {
			from = previous.Status
		}
		to := status
		if to == "" {
			to = "none"
		}
		s.record(ctx, actor, action, event, "member "+strconv.FormatInt(userID, 10)+": "+from+" to "+to)
	}
	return written, nil
}

// OverlapEvent is another event of a player that overlaps an event in time:
// one the player is signed up or called up for, or one of the player's
// teams that the player has not declined or been left out of.
type OverlapEvent struct {
	EventID  int64
	Title    string
	StartsAt time.Time
	EndsAt   time.Time
}

// CallUpResult is the outcome of a call-up: the player's row and the
// player's other events at the same time (a warning, never a block).
type CallUpResult struct {
	Participant *Participant
	Overlaps    []OverlapEvent
}

// CallUp calls a player of the group up for the event (#47): a nomination
// across teams. Only staff and the event's coaches, any mode, only for a
// player who does not play on the event's teams (team players are nominated
// or answer as usual), until the event starts, never for a cancelled event.
// The place counts against the participant limit, but the limit does not
// stop a call-up. The called-up player and linked parents then see the event.
// Overlapping events of the player are returned as a warning.
func (s *ParticipationService) CallUp(ctx context.Context, actor GroupActor, eventID, userID int64) (CallUpResult, error) {
	if err := s.validate(ctx); err != nil {
		return CallUpResult{}, err
	}
	access := actor.Access
	if !access.CanView() {
		return CallUpResult{}, ErrGroupNotFound
	}
	detail, err := s.events.Detail(ctx, access, eventID)
	if err != nil {
		return CallUpResult{}, err
	}
	event := detail.Event
	if !access.CanManageParticipants(event, teamCoachIDs(detail)) {
		return CallUpResult{}, ErrGroupForbidden
	}
	players, err := s.participants.ListEventPlayers(ctx, access.GroupID(), event.ID)
	if err != nil {
		return CallUpResult{}, fmt.Errorf("list event players: %w", err)
	}
	teams := event.TeamIDs()
	if !slices.ContainsFunc(players, func(player EventPlayer) bool {
		return player.UserID == userID && player.IsPlayer && !slices.ContainsFunc(player.TeamIDs, func(id int64) bool { return slices.Contains(teams, id) })
	}) {
		return CallUpResult{}, ErrParticipantNotEligible
	}
	now := s.now()
	actorID := access.UserID()
	changed := false
	written, err := s.participants.ChangeParticipant(ctx, access.GroupID(), event.ID, userID, func(state ParticipationState) (*Participant, error) {
		changed = false
		switch {
		case state.Event.Cancelled:
			return nil, ErrParticipationCancelled
		case !now.Before(state.Event.StartsAt):
			return nil, ErrParticipationStarted
		case state.Current != nil && state.Current.CalledUp:
			return state.Current, nil
		}
		changed = true
		row := Participant{EventID: event.ID, UserID: userID, Status: ParticipantNominated, CalledUp: true, UpdatedBy: &actorID, UpdatedAt: now.Unix()}
		if state.Current != nil {
			row.Attended = state.Current.Attended
		}
		return &row, nil
	})
	if err != nil {
		return CallUpResult{}, mapParticipationError("call up participant", err)
	}
	result := CallUpResult{Participant: written}
	if changed {
		s.record(ctx, actor, AuditActionParticipantCalledUp, event, "member "+strconv.FormatInt(userID, 10))
		if result.Overlaps, err = s.participants.ListOverlaps(ctx, access.GroupID(), userID, event.StartsAt, event.EndsAt, event.ID); err != nil {
			return CallUpResult{}, fmt.Errorf("list overlapping events: %w", err)
		}
	}
	return result, nil
}

// RecordAttendance confirms who attended the event: marks for players on the
// list (a mark for a group player without a row adds a walk-in). Only staff
// and the event's coaches, only after the event ended, never for a cancelled
// event.
func (s *ParticipationService) RecordAttendance(ctx context.Context, actor GroupActor, eventID int64, marks []AttendanceMark) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	access := actor.Access
	if !access.CanView() {
		return ErrGroupNotFound
	}
	detail, err := s.events.Detail(ctx, access, eventID)
	if err != nil {
		return err
	}
	event := detail.Event
	if !access.CanRecordAttendance(event, teamCoachIDs(detail)) {
		return ErrGroupForbidden
	}
	if len(marks) == 0 || len(marks) > maxAttendanceMarks {
		return ErrInvalidAttendance
	}
	seen := make(map[int64]bool, len(marks))
	for _, mark := range marks {
		if mark.UserID <= 0 || seen[mark.UserID] {
			return ErrInvalidAttendance
		}
		seen[mark.UserID] = true
	}
	players, rows, err := s.load(ctx, access.GroupID(), event.ID)
	if err != nil {
		return err
	}
	eligible := make(map[int64]bool, len(players))
	for _, player := range players {
		eligible[player.UserID] = player.IsPlayer
	}
	for _, row := range rows {
		eligible[row.UserID] = true
	}
	for _, mark := range marks {
		if !eligible[mark.UserID] {
			return ErrParticipantNotEligible
		}
	}
	now := s.now()
	err = s.participants.RecordAttendance(ctx, access.GroupID(), event.ID, marks, access.UserID(), now.Unix(), func(locked ParticipationEvent) error {
		switch {
		case locked.Cancelled:
			return ErrParticipationCancelled
		case now.Before(locked.EndsAt):
			return ErrAttendanceTooEarly
		}
		return nil
	})
	if err != nil {
		return mapParticipationError("record attendance", err)
	}
	var attended, absent []int64
	for _, mark := range marks {
		if mark.Attended {
			attended = append(attended, mark.UserID)
		} else {
			absent = append(absent, mark.UserID)
		}
	}
	slices.Sort(attended)
	slices.Sort(absent)
	s.record(ctx, actor, AuditActionAttendanceRecorded, event, "attended "+joinIDsOrNone(attended)+"; absent "+joinIDsOrNone(absent))
	return nil
}

// ConfirmedAttendance returns confirmed attendance of events that are not
// cancelled and start in [from, to), for participation statistics (#11).
// Admins, managers, and site administrators get every event; other accounts
// get the events of the teams they see (VisibleTeamIDs).
func (s *ParticipationService) ConfirmedAttendance(ctx context.Context, access *GroupAccess, from, to time.Time) ([]AttendanceRecord, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil, ErrInvalidAttendance
	}
	records, err := s.participants.ListConfirmedAttendance(ctx, access.GroupID(), from, to)
	if err != nil {
		return nil, fmt.Errorf("list confirmed attendance: %w", err)
	}
	if access.SeesAllTeams() {
		return records, nil
	}
	visible := access.VisibleTeamIDs()
	kept := records[:0]
	for _, record := range records {
		if slices.ContainsFunc(record.TeamIDs, func(id int64) bool { return slices.Contains(visible, id) }) {
			kept = append(kept, record)
		}
	}
	return kept, nil
}

// CalendarAnswer is one answer shown on the calendar: the viewer's own, or
// a linked child's. Names are the member's, for labelling a child's answer.
type CalendarAnswer struct {
	EventID     int64
	UserID      int64
	Status      string
	Self        bool
	DisplayName *string
	Username    *string
}

// CalendarAnswers returns the viewer's own answers and, for a parent, the
// answers of the linked children, for the group's events that overlap
// [from, to), in one query. Nobody else's answers are returned. The caller
// shows them only on events it already listed for the same access, so
// visibility stays with EventService.
func (s *ParticipationService) CalendarAnswers(ctx context.Context, access *GroupAccess, from, to time.Time) ([]CalendarAnswer, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if !access.CanView() {
		return nil, ErrGroupNotFound
	}
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil, ErrInvalidAttendance
	}
	own := access.UserID()
	ids := []int64{own}
	for _, id := range access.ChildUserIDs() {
		if id != own && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	answers, err := s.participants.ListAnswers(ctx, access.GroupID(), ids, from, to)
	if err != nil {
		return nil, fmt.Errorf("list calendar answers: %w", err)
	}
	kept := make([]CalendarAnswer, 0, len(answers))
	for _, answer := range answers {
		if answer.Status == "" || !slices.Contains(ids, answer.UserID) {
			continue
		}
		answer.Self = answer.UserID == own
		kept = append(kept, answer)
	}
	return kept, nil
}

// checkParticipation decides whether the answer may change from current to
// status ("" removes it) at now. manage is true for staff and the event's
// coaches, who are bound by neither the deadline nor the limit.
func checkParticipation(event ParticipationEvent, now time.Time, current *Participant, taken int, status string, manage bool) error {
	switch {
	case event.Cancelled:
		return ErrParticipationCancelled
	case !now.Before(event.StartsAt):
		return ErrParticipationStarted
	case !manage && event.SignupDeadline != nil && !now.Before(*event.SignupDeadline):
		return ErrSignupDeadlinePassed
	}
	if !manage && status == "" {
		return ErrInvalidParticipantStatus
	}
	from := ""
	if current != nil {
		from = current.Status
	}
	if from == status {
		return nil
	}
	nominated := event.SignupMode == EventSignupNominated
	if current != nil && current.CalledUp {
		// A called-up row follows the same rules in every sign-up mode: the
		// player opts out and may take the place back.
		switch {
		case manage && (status == "" || status == ParticipantNominated || status == ParticipantDeclined):
		case !manage && status == ParticipantDeclined && countsAgainstLimit(from):
		case !manage && status == ParticipantNominated && from == ParticipantDeclined:
		default:
			return ErrInvalidParticipantStatus
		}
		if !manage && countsAgainstLimit(status) && !countsAgainstLimit(from) && event.MaxParticipants != nil && taken >= *event.MaxParticipants {
			return ErrEventFull
		}
		return nil
	}
	switch {
	case from == ParticipantLeftOut && !manage:
		return ErrInvalidParticipantStatus
	case manage && status == ParticipantLeftOut:
	case manage && status == "":
	case manage && nominated && (status == ParticipantNominated || status == ParticipantDeclined):
	case manage && !nominated && (status == ParticipantSignedUp || status == ParticipantDeclined):
	case !manage && !nominated && (status == ParticipantSignedUp || status == ParticipantDeclined):
	// A nominated player opts out, and may take the place back.
	case !manage && nominated && status == ParticipantDeclined && countsAgainstLimit(from):
	case !manage && nominated && status == ParticipantNominated && from == ParticipantDeclined:
	default:
		return ErrInvalidParticipantStatus
	}
	if !manage && countsAgainstLimit(status) && !countsAgainstLimit(from) && event.MaxParticipants != nil && taken >= *event.MaxParticipants {
		return ErrEventFull
	}
	return nil
}

func participationPhase(event ParticipationEvent, now time.Time) string {
	switch {
	case event.Cancelled:
		return ParticipationCancelled
	case !now.Before(event.EndsAt):
		return ParticipationEnded
	case !now.Before(event.StartsAt):
		return ParticipationStarted
	case event.SignupDeadline != nil && !now.Before(*event.SignupDeadline):
		return ParticipationClosed
	}
	return ParticipationOpen
}

// teamCoachIDs returns the season coaches of the event's teams.
func teamCoachIDs(detail EventDetail) []int64 {
	ids := make([]int64, 0, len(detail.TeamCoaches))
	for _, coach := range detail.TeamCoaches {
		ids = append(ids, coach.UserID)
	}
	return ids
}

func (s *ParticipationService) load(ctx context.Context, groupID, eventID int64) ([]EventPlayer, []Participant, error) {
	players, err := s.participants.ListEventPlayers(ctx, groupID, eventID)
	if err != nil {
		return nil, nil, fmt.Errorf("list event players: %w", err)
	}
	rows, err := s.participants.ListParticipants(ctx, groupID, eventID)
	if err != nil {
		return nil, nil, fmt.Errorf("list participants: %w", err)
	}
	slices.SortStableFunc(rows, func(a, b Participant) int { return cmp.Compare(a.UserID, b.UserID) })
	return players, rows, nil
}

func (s *ParticipationService) record(ctx context.Context, actor GroupActor, action string, event Event, detail string) {
	text := "event " + strconv.FormatInt(event.ID, 10) + ": " + event.Title + "; " + detail
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		text += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, text)
}

func joinIDsOrNone(ids []int64) string {
	if len(ids) == 0 {
		return "none"
	}
	return joinIDs(ids)
}

func mapParticipationError(operation string, err error) error {
	switch {
	case errors.Is(err, ErrEventNotFound), errors.Is(err, ErrRecordNotFound):
		return ErrEventNotFound
	case errors.Is(err, ErrMembershipNotFound):
		return ErrParticipantNotEligible
	case errors.Is(err, ErrParticipationCancelled), errors.Is(err, ErrParticipationStarted), errors.Is(err, ErrSignupDeadlinePassed),
		errors.Is(err, ErrEventFull), errors.Is(err, ErrInvalidParticipantStatus), errors.Is(err, ErrAttendanceTooEarly),
		errors.Is(err, ErrParticipantNotEligible), errors.Is(err, ErrInvalidAttendance):
		return err
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func (s *ParticipationService) validate(ctx context.Context) error {
	if s == nil || s.events == nil || s.participants == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil participation context")
	}
	return nil
}
