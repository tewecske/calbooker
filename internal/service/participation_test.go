package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// participationEventsStub serves event details of group 5 with the
// visibility rule of the caller's access.
type participationEventsStub struct {
	events  map[int64]Event
	coaches []EventCoach
}

func (s *participationEventsStub) Detail(_ context.Context, access *GroupAccess, eventID int64) (EventDetail, error) {
	event, ok := s.events[eventID]
	if !ok || !access.CanViewEvent(event) {
		return EventDetail{}, ErrEventNotFound
	}
	return EventDetail{Event: event, TimeZone: "Europe/Budapest", TeamCoaches: slices.Clone(s.coaches), CanEdit: access.CanEditEvent(event)}, nil
}

// participantRepositoryStub keeps rows in memory; its mutex plays the event
// row lock.
type participantRepositoryStub struct {
	mu      sync.Mutex
	events  *participationEventsStub
	players []EventPlayer
	rows    map[int64]Participant
	records []AttendanceRecord
	changes int
	// overlaps are the overlapping events returned per user (#47).
	overlaps       map[int64][]OverlapEvent
	overlapQueries []int64
}

func (r *participantRepositoryStub) ListEventPlayers(_ context.Context, groupID, _ int64) ([]EventPlayer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if groupID != 5 {
		return nil, nil
	}
	return slices.Clone(r.players), nil
}

func (r *participantRepositoryStub) ListParticipants(_ context.Context, groupID, _ int64) ([]Participant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := make([]Participant, 0, len(r.rows))
	if groupID != 5 {
		return rows, nil
	}
	for _, row := range r.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (r *participantRepositoryStub) state(eventID int64) (ParticipationEvent, error) {
	event, ok := r.events.events[eventID]
	if !ok {
		return ParticipationEvent{}, ErrEventNotFound
	}
	return participationEventOf(event), nil
}

func (r *participantRepositoryStub) ChangeParticipant(_ context.Context, _, eventID, userID int64, decide ParticipantDecision) (*Participant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	event, err := r.state(eventID)
	if err != nil {
		return nil, err
	}
	state := ParticipationState{Event: event}
	if row, ok := r.rows[userID]; ok {
		state.Current = &row
	}
	for _, row := range r.rows {
		if row.Counts() {
			state.Taken++
		}
	}
	next, err := decide(state)
	if err != nil {
		return nil, err
	}
	r.changes++
	if next == nil {
		delete(r.rows, userID)
		return nil, nil
	}
	written := *next
	written.EventID, written.UserID = eventID, userID
	r.rows[userID] = written
	return &written, nil
}

func (r *participantRepositoryStub) RecordAttendance(_ context.Context, _, eventID int64, marks []AttendanceMark, by, at int64, check func(ParticipationEvent) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	event, err := r.state(eventID)
	if err != nil {
		return err
	}
	if err := check(event); err != nil {
		return err
	}
	for _, mark := range marks {
		attended := mark.Attended
		row, ok := r.rows[mark.UserID]
		switch {
		case ok:
			row.Attended = &attended
		case attended:
			row = Participant{EventID: eventID, UserID: mark.UserID, Attended: &attended}
		default:
			continue
		}
		row.UpdatedBy, row.UpdatedAt = &by, at
		if row.Status == "" && !attended {
			delete(r.rows, mark.UserID)
			continue
		}
		r.rows[mark.UserID] = row
	}
	return nil
}

func (r *participantRepositoryStub) ListConfirmedAttendance(_ context.Context, _ int64, from, to time.Time) ([]AttendanceRecord, error) {
	var kept []AttendanceRecord
	for _, record := range r.records {
		if !record.StartsAt.Before(from) && record.StartsAt.Before(to) {
			kept = append(kept, record)
		}
	}
	return kept, nil
}

func (r *participantRepositoryStub) ListAnswers(_ context.Context, _ int64, userIDs []int64, _, _ time.Time) ([]CalendarAnswer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var answers []CalendarAnswer
	for _, row := range r.rows {
		if slices.Contains(userIDs, row.UserID) {
			answers = append(answers, CalendarAnswer{EventID: row.EventID, UserID: row.UserID, Status: row.Status})
		}
	}
	return answers, nil
}

func (r *participantRepositoryStub) ListOverlaps(_ context.Context, _, userID int64, _, _ time.Time, excludeEventID int64) ([]OverlapEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overlapQueries = append(r.overlapQueries, userID)
	return slices.Clone(r.overlaps[userID]), nil
}

func (r *participantRepositoryStub) CalledUpEventIDs(_ context.Context, _ int64, userIDs []int64) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, 0)
	for _, row := range r.rows {
		if row.CalledUp && slices.Contains(userIDs, row.UserID) {
			ids = append(ids, row.EventID)
		}
	}
	return ids, nil
}

type participationSetup struct {
	service *ParticipationService
	events  *participationEventsStub
	repo    *participantRepositoryStub
	auditor *auditRecorderStub
	now     *time.Time
}

// newParticipationSetup builds event 1 (open, team 1) and event 2
// (nominated, team 1) of group 5. Players: 7 (the actor's own account when
// acting as a player) and 22 (a guest) on team 1, 20 (a child profile) on
// team 1, 23 (a child's own account) on team 1, 21 on team 2. Member 30 is a
// parent without the player role.
func newParticipationSetup(t *testing.T) participationSetup {
	t.Helper()
	events := &participationEventsStub{events: map[int64]Event{1: storedEvent(1, 1), 2: storedEvent(2, 1)}}
	nominated := events.events[2]
	nominated.SignupMode, nominated.Kind = EventSignupNominated, EventKindMatch
	events.events[2] = nominated
	repo := &participantRepositoryStub{
		events: events,
		rows:   make(map[int64]Participant),
		players: []EventPlayer{
			{UserID: 7, Kind: UserKindAccount, DisplayName: strPtr("Me"), Email: strPtr("me@example.test"), IsPlayer: true, TeamIDs: []int64{1}},
			{UserID: 20, Kind: UserKindProfile, DisplayName: strPtr("Anna"), IsPlayer: true, TeamIDs: []int64{1}},
			{UserID: 21, Kind: UserKindAccount, DisplayName: strPtr("Other team"), Email: strPtr("other@example.test"), IsPlayer: true, TeamIDs: []int64{2}},
			{UserID: 22, Kind: UserKindGuest, IsPlayer: true, TeamIDs: []int64{1}},
			{UserID: 23, Kind: UserKindGuest, DisplayName: strPtr("Ben"), IsPlayer: true, TeamIDs: []int64{1}},
			{UserID: 30, Kind: UserKindAccount, DisplayName: strPtr("Parent")},
		},
	}
	service, err := NewParticipationService(events, repo, usersWith(User{ID: 7, Email: strPtr("actor@example.test")}))
	if err != nil {
		t.Fatalf("NewParticipationService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	now := eventNow
	service.now = func() time.Time { return now }
	return participationSetup{service: service, events: events, repo: repo, auditor: auditor, now: &now}
}

func childAccountParent() GroupActor {
	return actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildTeamIDs: []int64{1}, ChildUserIDs: []int64{23}})
}

func extraCoachActor() GroupActor {
	return actorWith(memberWith(GroupRoleCoach))
}

func TestNewParticipationServiceRejectsNilDependencies(t *testing.T) {
	events := &participationEventsStub{}
	repo := &participantRepositoryStub{}
	if _, err := NewParticipationService(nil, repo, usersWith()); !errors.Is(err, ErrNilParticipationEvents) {
		t.Errorf("nil events error = %v", err)
	}
	if _, err := NewParticipationService(events, nil, usersWith()); !errors.Is(err, ErrNilParticipantRepository) {
		t.Errorf("nil repository error = %v", err)
	}
	if _, err := NewParticipationService(events, repo, nil); !errors.Is(err, ErrNilUserRepository) {
		t.Errorf("nil users error = %v", err)
	}
	var missing *ParticipationService
	if _, err := missing.Respond(context.Background(), adminActor(), 1, 7, ParticipantSignedUp); !errors.Is(err, ErrInvalidGroup) {
		t.Errorf("nil service error = %v", err)
	}
}

func TestParticipationRespondWhoMay(t *testing.T) {
	extra := func(s participationSetup) {
		event := s.events.events[1]
		event.Coaches = []EventCoach{{UserID: 7}}
		s.events.events[1] = event
	}
	tests := []struct {
		name    string
		actor   GroupActor
		prepare func(participationSetup)
		event   int64
		user    int64
		status  string
		want    error
		audited string
	}{
		{name: "player signs up", actor: playerOn(1), event: 1, user: 7, status: ParticipantSignedUp},
		{name: "player cannot come", actor: playerOn(1), event: 1, user: 7, status: ParticipantDeclined},
		{name: "player answers for another player", actor: playerOn(1), event: 1, user: 22, status: ParticipantSignedUp, want: ErrGroupForbidden},
		{name: "player of another team", actor: playerOn(2), event: 1, user: 7, status: ParticipantSignedUp, want: ErrEventNotFound},
		{name: "member without teams", actor: actorWith(memberWith(GroupRolePlayer)), event: 1, user: 7, status: ParticipantSignedUp, want: ErrEventNotFound},
		{name: "outsider", actor: actorWith(GroupAccessGrants{}), event: 1, user: 7, status: ParticipantSignedUp, want: ErrGroupNotFound},
		{name: "parent signs up a child profile", actor: childParentActor(1), event: 1, user: 20, status: ParticipantSignedUp},
		{name: "parent signs up a child account", actor: childAccountParent(), event: 1, user: 23, status: ParticipantSignedUp},
		{name: "parent answers for another child", actor: childParentActor(1), event: 1, user: 22, status: ParticipantSignedUp, want: ErrGroupForbidden},
		{name: "parent without the player role answers for self", actor: childParentActor(1), event: 1, user: 7, status: ParticipantSignedUp, want: ErrGroupForbidden},
		{name: "coach of the team", actor: coachOf(1), event: 1, user: 22, status: ParticipantSignedUp, audited: AuditActionParticipantUpdated},
		{name: "coach of another team", actor: coachOf(2), event: 1, user: 22, status: ParticipantSignedUp, want: ErrEventNotFound},
		{name: "extra coach", actor: extraCoachActor(), prepare: extra, event: 1, user: 22, status: ParticipantDeclined, audited: AuditActionParticipantUpdated},
		{name: "staff nominates", actor: adminActor(), event: 2, user: 22, status: ParticipantNominated, audited: AuditActionParticipantNominated},
		{name: "site administrator nominates", actor: siteAdminActor(), event: 2, user: 20, status: ParticipantNominated, audited: AuditActionParticipantNominated},
		{name: "staff picks a player of another team", actor: adminActor(), event: 1, user: 21, status: ParticipantSignedUp, want: ErrParticipantNotEligible},
		{name: "staff picks a member without the player role", actor: adminActor(), event: 1, user: 30, status: ParticipantSignedUp, want: ErrParticipantNotEligible},
		{name: "unknown status", actor: playerOn(1), event: 1, user: 7, status: "maybe", want: ErrInvalidParticipantStatus},
		{name: "player removes own answer", actor: playerOn(1), event: 1, user: 7, status: "", want: ErrInvalidParticipantStatus},
		{name: "missing event", actor: adminActor(), event: 9, user: 7, status: ParticipantSignedUp, want: ErrEventNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := newParticipationSetup(t)
			if test.prepare != nil {
				test.prepare(setup)
			}
			written, err := setup.service.Respond(context.Background(), test.actor, test.event, test.user, test.status)
			if !errors.Is(err, test.want) {
				t.Fatalf("Respond() error = %v, want %v", err, test.want)
			}
			if test.want != nil {
				if len(setup.repo.rows) != 0 || len(setup.auditor.records) != 0 {
					t.Fatalf("refused change wrote %v, audited %v", setup.repo.rows, setup.auditor.records)
				}
				return
			}
			if written == nil || written.Status != test.status || written.UpdatedBy == nil || *written.UpdatedBy != 7 || written.UpdatedAt != eventNow.Unix() {
				t.Fatalf("written = %+v", written)
			}
			if test.audited == "" {
				if len(setup.auditor.records) != 0 {
					t.Fatalf("self-service answer audited: %+v", setup.auditor.records)
				}
				return
			}
			if len(setup.auditor.records) != 1 || setup.auditor.records[0].Action != test.audited || setup.auditor.records[0].TargetID != "5" {
				t.Fatalf("audit = %+v, want %s", setup.auditor.records, test.audited)
			}
			if detail := setup.auditor.records[0].Detail; !strings.Contains(detail, "member ") || !strings.Contains(detail, "none to "+test.status) {
				t.Fatalf("audit detail = %q", detail)
			}
		})
	}
}

func TestParticipationNominatedTransitions(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	player := playerOn(1)
	if _, err := setup.service.Respond(ctx, player, 2, 7, ParticipantSignedUp); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("self sign-up to a nominated event error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, player, 2, 7, ParticipantNominated); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("self nomination error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, player, 2, 7, ParticipantDeclined); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("declining without a nomination error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 2, 7, ParticipantSignedUp); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("coach sign-up in a nominated event error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 2, 7, ParticipantNominated); err != nil {
		t.Fatalf("coach nominates error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, player, 2, 7, ParticipantDeclined); err != nil {
		t.Fatalf("nominated player opts out error = %v", err)
	}
	if setup.repo.rows[7].Status != ParticipantDeclined {
		t.Fatalf("row = %+v", setup.repo.rows[7])
	}
	if _, err := setup.service.Respond(ctx, player, 2, 7, ParticipantNominated); err != nil {
		t.Fatalf("re-accept error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 22, ParticipantNominated); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("nomination in an open event error = %v", err)
	}
	// The parent of a nominated child opts the child out.
	if _, err := setup.service.Respond(ctx, adminActor(), 2, 20, ParticipantNominated); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.service.Respond(ctx, childParentActor(1), 2, 20, ParticipantDeclined); err != nil {
		t.Fatalf("parent opts the child out error = %v", err)
	}
	// Staff remove a nomination: the row goes and the removal is audited.
	if _, err := setup.service.Respond(ctx, adminActor(), 2, 22, ParticipantNominated); err != nil {
		t.Fatal(err)
	}
	setup.auditor.records = nil
	if written, err := setup.service.Respond(ctx, adminActor(), 2, 22, ""); err != nil || written != nil {
		t.Fatalf("remove = %+v, %v", written, err)
	}
	if _, ok := setup.repo.rows[22]; ok {
		t.Fatal("removed row kept")
	}
	if len(setup.auditor.records) != 1 || setup.auditor.records[0].Action != AuditActionParticipantRemoved || !strings.Contains(setup.auditor.records[0].Detail, "nominated to none") {
		t.Fatalf("audit = %+v", setup.auditor.records)
	}
	// Removing nothing changes nothing and is not audited.
	setup.auditor.records = nil
	if _, err := setup.service.Respond(ctx, adminActor(), 2, 22, ""); err != nil || len(setup.auditor.records) != 0 {
		t.Fatalf("remove nothing = %v, %+v", err, setup.auditor.records)
	}
}

func TestParticipationLimit(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	one := 1
	for _, id := range []int64{1, 2} {
		event := setup.events.events[id]
		event.MaxParticipants = &one
		setup.events.events[id] = event
	}
	if _, err := setup.service.Respond(ctx, childParentActor(1), 1, 20, ParticipantSignedUp); err != nil {
		t.Fatalf("first sign-up error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantSignedUp); !errors.Is(err, ErrEventFull) {
		t.Fatalf("sign-up at the limit error = %v", err)
	}
	// Declining takes no place, so it works at the limit.
	if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantDeclined); err != nil {
		t.Fatalf("decline at the limit error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantSignedUp); !errors.Is(err, ErrEventFull) {
		t.Fatalf("declined player signing up at the limit error = %v", err)
	}
	// Staff and coaches may go over the limit.
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 7, ParticipantSignedUp); err != nil {
		t.Fatalf("coach over the limit error = %v", err)
	}
	// Opting out frees a place for someone else.
	if _, err := setup.service.Respond(ctx, childParentActor(1), 1, 20, ParticipantDeclined); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 7, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantSignedUp); err != nil {
		t.Fatalf("sign-up after a place freed error = %v", err)
	}
	// Nominations count too: a nominated player who opted out cannot
	// re-accept a full event.
	if _, err := setup.service.Respond(ctx, adminActor(), 2, 7, ParticipantNominated); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.service.Respond(ctx, playerOn(1), 2, 7, ParticipantDeclined); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.service.Respond(ctx, adminActor(), 2, 22, ParticipantNominated); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.service.Respond(ctx, playerOn(1), 2, 7, ParticipantNominated); !errors.Is(err, ErrEventFull) {
		t.Fatalf("re-accept a full event error = %v", err)
	}
}

func TestParticipationConcurrentLastPlace(t *testing.T) {
	setup := newParticipationSetup(t)
	one := 1
	event := setup.events.events[1]
	event.MaxParticipants = &one
	setup.events.events[1] = event
	var (
		wait    sync.WaitGroup
		results = make([]error, 2)
	)
	actors := []struct {
		actor GroupActor
		user  int64
	}{{playerOn(1), 7}, {childParentActor(1), 20}}
	for index, item := range actors {
		wait.Go(func() {
			_, results[index] = setup.service.Respond(context.Background(), item.actor, 1, item.user, ParticipantSignedUp)
		})
	}
	wait.Wait()
	wins, full := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrEventFull):
			full++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if wins != 1 || full != 1 || len(setup.repo.rows) != 1 {
		t.Fatalf("wins %d full %d rows %v", wins, full, setup.repo.rows)
	}
}

func TestParticipationDeadlineStartAndCancel(t *testing.T) {
	ctx := context.Background()
	t.Run("deadline", func(t *testing.T) {
		setup := newParticipationSetup(t)
		deadline := eventNow.Add(-time.Hour)
		event := setup.events.events[1]
		event.SignupDeadline = &deadline
		setup.events.events[1] = event
		if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantSignedUp); !errors.Is(err, ErrSignupDeadlinePassed) {
			t.Fatalf("player after the deadline error = %v", err)
		}
		if _, err := setup.service.Respond(ctx, childParentActor(1), 1, 20, ParticipantDeclined); !errors.Is(err, ErrSignupDeadlinePassed) {
			t.Fatalf("parent after the deadline error = %v", err)
		}
		if _, err := setup.service.Respond(ctx, coachOf(1), 1, 7, ParticipantSignedUp); err != nil {
			t.Fatalf("coach after the deadline error = %v", err)
		}
		if _, err := setup.service.Respond(ctx, adminActor(), 1, 20, ParticipantDeclined); err != nil {
			t.Fatalf("staff after the deadline error = %v", err)
		}
		// Exactly at the deadline it is closed.
		*setup.now = deadline
		if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantDeclined); !errors.Is(err, ErrSignupDeadlinePassed) {
			t.Fatalf("player at the deadline error = %v", err)
		}
		*setup.now = deadline.Add(-time.Second)
		if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantDeclined); err != nil {
			t.Fatalf("player before the deadline error = %v", err)
		}
	})
	t.Run("start", func(t *testing.T) {
		setup := newParticipationSetup(t)
		*setup.now = setup.events.events[1].StartsAt
		for _, actor := range []GroupActor{playerOn(1), coachOf(1), adminActor()} {
			if _, err := setup.service.Respond(ctx, actor, 1, 7, ParticipantSignedUp); !errors.Is(err, ErrParticipationStarted) {
				t.Fatalf("change at the start error = %v", err)
			}
		}
		*setup.now = setup.events.events[1].StartsAt.Add(-time.Minute)
		if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantSignedUp); err != nil {
			t.Fatalf("change before the start error = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		setup := newParticipationSetup(t)
		cancelled := int64(1)
		event := setup.events.events[1]
		event.CancelledAt = &cancelled
		setup.events.events[1] = event
		for _, actor := range []GroupActor{playerOn(1), coachOf(1), adminActor()} {
			if _, err := setup.service.Respond(ctx, actor, 1, 7, ParticipantSignedUp); !errors.Is(err, ErrParticipationCancelled) {
				t.Fatalf("change on a cancelled event error = %v", err)
			}
		}
		*setup.now = event.EndsAt.Add(time.Hour)
		if err := setup.service.RecordAttendance(ctx, coachOf(1), 1, []AttendanceMark{{UserID: 7, Attended: true}}); !errors.Is(err, ErrParticipationCancelled) {
			t.Fatalf("attendance on a cancelled event error = %v", err)
		}
	})
}

func TestParticipationAttendance(t *testing.T) {
	ctx := context.Background()
	setup := newParticipationSetup(t)
	for _, user := range []int64{7, 22} {
		if _, err := setup.service.Respond(ctx, coachOf(1), 1, user, ParticipantSignedUp); err != nil {
			t.Fatal(err)
		}
	}
	marks := []AttendanceMark{{UserID: 7, Attended: true}, {UserID: 22, Attended: false}, {UserID: 21, Attended: true}}
	if err := setup.service.RecordAttendance(ctx, coachOf(1), 1, marks); !errors.Is(err, ErrAttendanceTooEarly) {
		t.Fatalf("attendance before the end error = %v", err)
	}
	*setup.now = setup.events.events[1].EndsAt
	setup.auditor.records = nil
	for name, actor := range map[string]GroupActor{"player": playerOn(1), "parent": childParentActor(1)} {
		if err := setup.service.RecordAttendance(ctx, actor, 1, marks); !errors.Is(err, ErrGroupForbidden) {
			t.Fatalf("%s attendance error = %v", name, err)
		}
	}
	if err := setup.service.RecordAttendance(ctx, coachOf(2), 1, marks); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("coach of another team error = %v", err)
	}
	for name, bad := range map[string][]AttendanceMark{
		"empty":     nil,
		"duplicate": {{UserID: 7, Attended: true}, {UserID: 7}},
		"invalid":   {{UserID: 0, Attended: true}},
	} {
		if err := setup.service.RecordAttendance(ctx, coachOf(1), 1, bad); !errors.Is(err, ErrInvalidAttendance) {
			t.Fatalf("%s marks error = %v", name, err)
		}
	}
	if err := setup.service.RecordAttendance(ctx, coachOf(1), 1, []AttendanceMark{{UserID: 30, Attended: true}}); !errors.Is(err, ErrParticipantNotEligible) {
		t.Fatalf("walk-in without the player role error = %v", err)
	}
	// The coach confirms: 7 came, 22 was a no-show, 21 (another team) walked in.
	if err := setup.service.RecordAttendance(ctx, coachOf(1), 1, marks); err != nil {
		t.Fatalf("RecordAttendance() error = %v", err)
	}
	rows := setup.repo.rows
	if !attendedIs(rows[7], true) || !attendedIs(rows[22], false) || !attendedIs(rows[21], true) || rows[21].Status != "" || rows[22].Status != ParticipantSignedUp {
		t.Fatalf("rows = %+v", rows)
	}
	if len(setup.auditor.records) != 1 || setup.auditor.records[0].Action != AuditActionAttendanceRecorded ||
		!strings.Contains(setup.auditor.records[0].Detail, "attended 7,21; absent 22") {
		t.Fatalf("audit = %+v", setup.auditor.records)
	}
	// A walk-in marked absent again goes away; an extra coach and staff
	// record attendance too.
	event := setup.events.events[1]
	event.Coaches = []EventCoach{{UserID: 7}}
	setup.events.events[1] = event
	if err := setup.service.RecordAttendance(ctx, extraCoachActor(), 1, []AttendanceMark{{UserID: 21, Attended: false}}); err != nil {
		t.Fatalf("extra coach error = %v", err)
	}
	if _, ok := setup.repo.rows[21]; ok {
		t.Fatal("absent walk-in kept")
	}
	if err := setup.service.RecordAttendance(ctx, adminActor(), 1, []AttendanceMark{{UserID: 22, Attended: true}}); err != nil {
		t.Fatalf("staff error = %v", err)
	}
	// Sign-up stays closed after the event.
	if _, err := setup.service.Respond(ctx, adminActor(), 1, 20, ParticipantSignedUp); !errors.Is(err, ErrParticipationStarted) {
		t.Fatalf("sign-up after the event error = %v", err)
	}
}

func attendedIs(row Participant, want bool) bool {
	return row.Attended != nil && *row.Attended == want
}

func TestParticipationRoster(t *testing.T) {
	ctx := context.Background()
	setup := newParticipationSetup(t)
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 22, ParticipantSignedUp); err != nil {
		t.Fatal(err)
	}
	detail, err := setup.events.Detail(ctx, playerOn(1).Access, 1)
	if err != nil {
		t.Fatal(err)
	}
	roster, err := setup.service.Roster(ctx, playerOn(1).Access, detail)
	if err != nil {
		t.Fatalf("Roster() error = %v", err)
	}
	if roster.Phase != ParticipationOpen || roster.CanManage || roster.ShowEmails || roster.AttendanceOpen || roster.Taken != 1 {
		t.Fatalf("player roster = %+v", roster)
	}
	var ids []int64
	for _, entry := range roster.Entries {
		ids = append(ids, entry.Player.UserID)
		if entry.Player.Email != nil {
			t.Fatalf("player sees email of %d", entry.Player.UserID)
		}
		switch entry.Player.UserID {
		case 7:
			if !entry.Self || !slices.Equal(entry.Choices, []string{ParticipantSignedUp, ParticipantDeclined}) || entry.CanRemove {
				t.Fatalf("own entry = %+v", entry)
			}
		default:
			if len(entry.Choices) != 0 || entry.CanRemove {
				t.Fatalf("player may change %d: %+v", entry.Player.UserID, entry)
			}
		}
	}
	if !slices.Equal(ids, []int64{7, 20, 22, 23}) {
		t.Fatalf("entries = %v, want team 1 players only", ids)
	}

	parent, err := setup.service.Roster(ctx, childParentActor(1).Access, detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range parent.Entries {
		if (entry.Player.UserID == 20) != (entry.Child && len(entry.Choices) == 2) {
			t.Fatalf("parent entry = %+v", entry)
		}
	}

	coach := coachOf(1).Access
	coachDetail, _ := setup.events.Detail(ctx, coach, 1)
	managed, err := setup.service.Roster(ctx, coach, coachDetail)
	if err != nil {
		t.Fatal(err)
	}
	if !managed.CanManage || !managed.ShowEmails || managed.Entries[0].Player.Email == nil {
		t.Fatalf("coach roster = %+v", managed)
	}
	for _, entry := range managed.Entries {
		if entry.Player.UserID == 22 && (!entry.CanRemove || !slices.Equal(entry.Choices, []string{ParticipantDeclined, ParticipantLeftOut})) {
			t.Fatalf("coach entry = %+v", entry)
		}
	}

	// After the event the coach gets the attendance form with walk-ins.
	*setup.now = detail.Event.EndsAt.Add(time.Minute)
	ended, err := setup.service.Roster(ctx, coach, coachDetail)
	if err != nil {
		t.Fatal(err)
	}
	if ended.Phase != ParticipationEnded || !ended.AttendanceOpen || len(ended.WalkIns) != 1 || ended.WalkIns[0].UserID != 21 {
		t.Fatalf("ended roster = %+v", ended)
	}
	for _, entry := range ended.Entries {
		if len(entry.Choices) != 0 || entry.CanRemove {
			t.Fatalf("answers change after the event: %+v", entry)
		}
	}
	playerEnded, _ := setup.service.Roster(ctx, playerOn(1).Access, detail)
	if playerEnded.AttendanceOpen || len(playerEnded.WalkIns) != 0 {
		t.Fatalf("player gets attendance: %+v", playerEnded)
	}

	// Invisible events and outsiders.
	if _, err := setup.service.Roster(ctx, playerOn(2).Access, detail); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("other team roster error = %v", err)
	}
	if _, err := setup.service.Roster(ctx, actorWith(GroupAccessGrants{}).Access, detail); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider roster error = %v", err)
	}
}

func TestParticipationPhases(t *testing.T) {
	deadline := at("2026-11-01T12:00")
	event := participationEventOf(storedEvent(1, 1))
	event.SignupDeadline = &deadline
	for _, test := range []struct {
		now  time.Time
		want string
	}{
		{at("2026-10-30T12:00"), ParticipationOpen},
		{deadline, ParticipationClosed},
		{event.StartsAt, ParticipationStarted},
		{event.EndsAt, ParticipationEnded},
	} {
		if got := participationPhase(event, test.now); got != test.want {
			t.Errorf("phase at %v = %s, want %s", test.now, got, test.want)
		}
	}
	event.Cancelled = true
	if got := participationPhase(event, at("2026-10-30T12:00")); got != ParticipationCancelled {
		t.Errorf("cancelled phase = %s", got)
	}
}

func TestParticipationConfirmedAttendance(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	setup.repo.records = []AttendanceRecord{
		{EventID: 1, TeamIDs: []int64{1}, UserID: 7, StartsAt: at("2026-10-05T17:00")},
		{EventID: 3, TeamIDs: []int64{2}, UserID: 21, StartsAt: at("2026-10-06T17:00")},
		{EventID: 4, TeamIDs: []int64{1, 2}, UserID: 22, StartsAt: at("2026-10-07T17:00")},
		{EventID: 5, TeamIDs: []int64{1}, UserID: 7, StartsAt: at("2026-11-07T17:00")},
	}
	from, to := at("2026-10-01T00:00"), at("2026-11-01T00:00")
	all, err := setup.service.ConfirmedAttendance(ctx, adminActor().Access, from, to)
	if err != nil || len(all) != 3 {
		t.Fatalf("staff records = %+v, %v", all, err)
	}
	mine, err := setup.service.ConfirmedAttendance(ctx, playerOn(2).Access, from, to)
	if err != nil || len(mine) != 2 || mine[0].EventID != 3 || mine[1].EventID != 4 {
		t.Fatalf("team 2 records = %+v, %v", mine, err)
	}
	if _, err := setup.service.ConfirmedAttendance(ctx, actorWith(GroupAccessGrants{}).Access, from, to); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider error = %v", err)
	}
	if _, err := setup.service.ConfirmedAttendance(ctx, adminActor().Access, to, from); !errors.Is(err, ErrInvalidAttendance) {
		t.Fatalf("reversed range error = %v", err)
	}
}

func TestGroupAccessParticipationQuestions(t *testing.T) {
	event := storedEvent(1, 1)
	withExtra := event
	withExtra.Coaches = []EventCoach{{UserID: 7}}
	tests := []struct {
		name         string
		access       *GroupAccess
		event        Event
		teamCoaches  []int64
		self, child  bool
		manage, nomi bool
	}{
		{name: "admin", access: adminActor().Access, event: event, manage: true, nomi: true},
		{name: "site administrator", access: siteAdminActor().Access, event: event, manage: true, nomi: true},
		{name: "coach of the team", access: coachOf(1).Access, event: event, manage: true, nomi: true},
		{name: "coach of another team", access: coachOf(2).Access, event: event},
		{name: "extra coach", access: extraCoachActor().Access, event: withExtra, manage: true, nomi: true},
		{name: "season coach of the event", access: actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach, GroupRoleParent}, ChildTeamIDs: []int64{1}, ChildUserIDs: []int64{20}}).Access, event: event, teamCoaches: []int64{7}, child: true, manage: true, nomi: true},
		{name: "season coach without the coach role", access: childParentActor(1).Access, event: event, teamCoaches: []int64{7}, child: true},
		{name: "player of the team", access: playerOn(1).Access, event: event, self: true},
		{name: "player of another team", access: playerOn(2).Access, event: event},
		{name: "parent of a child on the team", access: childParentActor(1).Access, event: event, child: true},
		{name: "outsider", access: actorWith(GroupAccessGrants{}).Access, event: event},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := test.access
			if got := access.CanRespondFor(test.event, 7); got != test.self {
				t.Errorf("CanRespondFor(self) = %v", got)
			}
			if got := access.CanRespondFor(test.event, 20); got != test.child {
				t.Errorf("CanRespondFor(child) = %v", got)
			}
			if got := access.CanManageParticipants(test.event, test.teamCoaches); got != test.manage {
				t.Errorf("CanManageParticipants() = %v", got)
			}
			if got := access.CanRecordAttendance(test.event, test.teamCoaches); got != test.manage {
				t.Errorf("CanRecordAttendance() = %v", got)
			}
			if access.CanNominate(test.event, test.teamCoaches) {
				t.Error("CanNominate() true for an open event")
			}
			nominatedEvent := test.event
			nominatedEvent.SignupMode = EventSignupNominated
			if got := access.CanNominate(nominatedEvent, test.teamCoaches); got != test.nomi {
				t.Errorf("CanNominate(nominated) = %v", got)
			}
		})
	}
	if adminActor().Access.CanRespondFor(event, 0) {
		t.Fatal("answer for no one allowed")
	}
}

func TestEventDeleteRefusedWithParticipants(t *testing.T) {
	events := newEventStub(storedEvent(1, 1))
	events.deleteErr = ErrEventHasParticipants
	setup := newTestEventService(t, events)
	if err := setup.service.Delete(context.Background(), adminActor(), 1, 4); !errors.Is(err, ErrEventHasParticipants) {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(setup.auditor.records) != 0 {
		t.Fatalf("refused delete audited: %+v", setup.auditor.records)
	}
}
