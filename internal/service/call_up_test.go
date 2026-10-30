package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// Call-ups and left-out players (#47) on the setup of participation_test.go:
// event 1 is open and event 2 nominated, both on team 1. Player 21 plays on
// team 2, so a call-up for event 1 crosses teams.

func calledUpPlayer(eventIDs ...int64) GroupActor {
	return actorWith(GroupAccessGrants{
		Member: true, Roles: []string{GroupRolePlayer},
		OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}}, CalledUpEventIDs: eventIDs,
	})
}

func TestParticipationCallUpAcrossTeams(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	setup.repo.overlaps = map[int64][]OverlapEvent{21: {{EventID: 9, Title: "Away game"}}}

	result, err := setup.service.CallUp(ctx, coachOf(1), 1, 21)
	if err != nil {
		t.Fatalf("CallUp() error = %v", err)
	}
	row := setup.repo.rows[21]
	if row.Status != ParticipantNominated || !row.CalledUp || row.UpdatedBy == nil || *row.UpdatedBy != 7 {
		t.Fatalf("stored row = %+v, want called up and nominated", row)
	}
	if result.Participant == nil || !result.Participant.CalledUp {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Overlaps) != 1 || result.Overlaps[0].Title != "Away game" {
		t.Fatalf("overlaps = %+v, want the warning", result.Overlaps)
	}
	if len(setup.auditor.records) != 1 || setup.auditor.records[0].Action != AuditActionParticipantCalledUp {
		t.Fatalf("audit = %+v", setup.auditor.records)
	}

	// Calling the same player up again changes nothing and warns nothing.
	again, err := setup.service.CallUp(ctx, coachOf(1), 1, 21)
	if err != nil || len(again.Overlaps) != 0 || len(setup.auditor.records) != 1 {
		t.Fatalf("second CallUp() = %+v, %v (audit %d)", again, err, len(setup.auditor.records))
	}

	// Staff call players up for nominated events too, with the same warning.
	other := newParticipationSetup(t)
	other.repo.overlaps = map[int64][]OverlapEvent{21: {{EventID: 9, Title: "Away game"}}}
	if res, err := other.service.CallUp(ctx, adminActor(), 2, 21); err != nil || len(res.Overlaps) != 1 {
		t.Fatalf("staff CallUp() = %+v, %v", res, err)
	}
}

func TestParticipationCallUpRules(t *testing.T) {
	cancelled := func(s participationSetup) {
		event := s.events.events[1]
		at := eventNow.Unix()
		event.CancelledAt = &at
		s.events.events[1] = event
	}
	started := func(s participationSetup) {
		*s.now = s.events.events[1].StartsAt.Add(time.Minute)
	}
	extra := func(s participationSetup) {
		event := s.events.events[1]
		event.Coaches = []EventCoach{{UserID: 7}}
		s.events.events[1] = event
	}
	tests := []struct {
		name    string
		actor   GroupActor
		prepare func(participationSetup)
		user    int64
		want    error
	}{
		{name: "coach of the event team", actor: coachOf(1), user: 21},
		{name: "staff", actor: adminActor(), user: 21},
		{name: "site administrator", actor: siteAdminActor(), user: 21},
		{name: "extra coach", actor: extraCoachActor(), prepare: extra, user: 21},
		{name: "coach of the player's own team only", actor: coachOf(2), user: 21, want: ErrEventNotFound},
		{name: "coach of an unrelated team", actor: coachOf(3), user: 21, want: ErrEventNotFound},
		{name: "player", actor: playerOn(1), user: 21, want: ErrGroupForbidden},
		{name: "parent", actor: childParentActor(1), user: 21, want: ErrGroupForbidden},
		{name: "outsider", actor: actorWith(GroupAccessGrants{}), user: 21, want: ErrGroupNotFound},
		{name: "player already on the event's teams", actor: adminActor(), user: 22, want: ErrParticipantNotEligible},
		{name: "member without the player role", actor: adminActor(), user: 30, want: ErrParticipantNotEligible},
		{name: "unknown member", actor: adminActor(), user: 99, want: ErrParticipantNotEligible},
		{name: "cancelled event", actor: adminActor(), prepare: cancelled, user: 21, want: ErrParticipationCancelled},
		{name: "started event", actor: adminActor(), prepare: started, user: 21, want: ErrParticipationStarted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := newParticipationSetup(t)
			if test.prepare != nil {
				test.prepare(setup)
			}
			_, err := setup.service.CallUp(context.Background(), test.actor, 1, test.user)
			if !errors.Is(err, test.want) {
				t.Fatalf("CallUp() error = %v, want %v", err, test.want)
			}
			if test.want != nil && (len(setup.repo.rows) != 0 || len(setup.auditor.records) != 0) {
				t.Fatalf("refused call-up wrote %v, audited %v", setup.repo.rows, setup.auditor.records)
			}
		})
	}
}

func TestParticipationCallUpIgnoresTheLimit(t *testing.T) {
	setup := newParticipationSetup(t)
	event := setup.events.events[1]
	limit := 1
	event.MaxParticipants = &limit
	setup.events.events[1] = event
	ctx := context.Background()
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 22, ParticipantSignedUp); err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
	if _, err := setup.service.CallUp(ctx, coachOf(1), 1, 21); err != nil {
		t.Fatalf("CallUp() over the limit error = %v, want it to pass for coaches", err)
	}
	roster, err := setup.service.Roster(ctx, adminActor().Access, EventDetail{Event: event})
	if err != nil || roster.Taken != 2 {
		t.Fatalf("Roster() taken = %d, %v; the call-up takes a place", roster.Taken, err)
	}
}

func TestParticipationCalledUpPlayerAnswers(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	// Player 7 plays on team 2 and is called up for event 1 (team 1).
	setup.repo.players[0].TeamIDs = []int64{2}
	if _, err := setup.service.CallUp(ctx, adminActor(), 1, 7); err != nil {
		t.Fatalf("CallUp() error = %v", err)
	}
	player := calledUpPlayer(1)

	// Without the call-up the event stays invisible.
	if _, err := setup.service.Respond(ctx, playerOn(2), 1, 7, ParticipantDeclined); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("player without the call-up error = %v, want not found", err)
	}
	// The player opts out and takes the place back, in an open event.
	written, err := setup.service.Respond(ctx, player, 1, 7, ParticipantDeclined)
	if err != nil || written.Status != ParticipantDeclined || !written.CalledUp {
		t.Fatalf("decline = %+v, %v; the row must stay called up", written, err)
	}
	if _, err := setup.service.Respond(ctx, player, 1, 7, ParticipantSignedUp); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("called-up sign-up error = %v, want invalid (the answer is nominated)", err)
	}
	written, err = setup.service.Respond(ctx, player, 1, 7, ParticipantNominated)
	if err != nil || written.Status != ParticipantNominated || !written.CalledUp {
		t.Fatalf("re-accept = %+v, %v", written, err)
	}
	// Self-service answers are not audited; only the call-up was.
	if len(setup.auditor.records) != 1 {
		t.Fatalf("audit = %+v", setup.auditor.records)
	}
	// The player cannot clear the call-up, but a coach can.
	if _, err := setup.service.Respond(ctx, player, 1, 7, ""); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("self clear error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 7, ""); err != nil {
		t.Fatalf("coach withdraws the call-up error = %v", err)
	}
	if _, kept := setup.repo.rows[7]; kept {
		t.Fatal("withdrawn call-up must delete the row")
	}
	if _, err := setup.service.Respond(ctx, player, 1, 7, ParticipantDeclined); !errors.Is(err, ErrParticipantNotEligible) {
		t.Fatalf("answer after the withdrawal error = %v, want not eligible", err)
	}
}

func TestParticipationCalledUpReacceptRespectsTheLimit(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	event := setup.events.events[1]
	limit := 1
	event.MaxParticipants = &limit
	setup.events.events[1] = event
	setup.repo.players[0].TeamIDs = []int64{2}
	if _, err := setup.service.CallUp(ctx, adminActor(), 1, 7); err != nil {
		t.Fatalf("CallUp() error = %v", err)
	}
	player := calledUpPlayer(1)
	if _, err := setup.service.Respond(ctx, player, 1, 7, ParticipantDeclined); err != nil {
		t.Fatalf("decline error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 22, ParticipantSignedUp); err != nil {
		t.Fatalf("fill the event error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, player, 1, 7, ParticipantNominated); !errors.Is(err, ErrEventFull) {
		t.Fatalf("re-accept in a full event error = %v, want %v", err, ErrEventFull)
	}
}

func TestParticipationLeaveOut(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	if _, err := setup.service.Respond(ctx, playerOn(1), 1, 7, ParticipantLeftOut); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("player leaves self out error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 21, ParticipantLeftOut); !errors.Is(err, ErrParticipantNotEligible) {
		t.Fatalf("leaving out a player of another team error = %v, want not eligible", err)
	}
	written, err := setup.service.Respond(ctx, coachOf(1), 1, 22, ParticipantLeftOut)
	if err != nil || written.Status != ParticipantLeftOut || written.CalledUp {
		t.Fatalf("leave out = %+v, %v", written, err)
	}
	if len(setup.auditor.records) != 1 || setup.auditor.records[0].Action != AuditActionParticipantLeftOut {
		t.Fatalf("audit = %+v", setup.auditor.records)
	}
	if written.Counts() {
		t.Fatal("a left-out player must not take a place")
	}
	// The left-out player cannot answer; a coach can take it back.
	guest := actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 1, Role: GroupRolePlayer}}})
	setup.repo.players[0].UserID = 7
	setup.repo.rows[7] = Participant{EventID: 1, UserID: 7, Status: ParticipantLeftOut}
	if _, err := setup.service.Respond(ctx, guest, 1, 7, ParticipantSignedUp); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("left-out player answers error = %v, want invalid", err)
	}
	if _, err := setup.service.Respond(ctx, guest, 1, 7, ParticipantDeclined); !errors.Is(err, ErrInvalidParticipantStatus) {
		t.Fatalf("left-out player declines error = %v, want invalid", err)
	}
	if _, err := setup.service.Respond(ctx, coachOf(1), 1, 7, ""); err != nil {
		t.Fatalf("coach clears the leave-out error = %v", err)
	}
	if _, err := setup.service.Respond(ctx, guest, 1, 7, ParticipantSignedUp); err != nil {
		t.Fatalf("answer after the leave-out was cleared error = %v", err)
	}
}

func TestParticipationRosterCallUps(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	detail := EventDetail{Event: setup.events.events[1]}

	roster, err := setup.service.Roster(ctx, coachOf(1).Access, detail)
	if err != nil {
		t.Fatalf("Roster() error = %v", err)
	}
	if len(roster.CallUps) != 1 || roster.CallUps[0].UserID != 21 {
		t.Fatalf("call-up candidates = %+v, want only the player of team 2", roster.CallUps)
	}
	// Players, parents, and cancelled or started events offer none.
	if roster, err = setup.service.Roster(ctx, playerOn(1).Access, detail); err != nil || len(roster.CallUps) != 0 {
		t.Fatalf("player roster call-ups = %+v, %v", roster.CallUps, err)
	}

	if _, err := setup.service.CallUp(ctx, coachOf(1), 1, 21); err != nil {
		t.Fatalf("CallUp() error = %v", err)
	}
	roster, err = setup.service.Roster(ctx, coachOf(1).Access, detail)
	if err != nil || len(roster.CallUps) != 0 {
		t.Fatalf("after the call-up: candidates %+v, %v", roster.CallUps, err)
	}
	var entry *ParticipationEntry
	for i := range roster.Entries {
		if roster.Entries[i].Player.UserID == 21 {
			entry = &roster.Entries[i]
		}
	}
	if entry == nil || !entry.CalledUp || entry.OnEventTeam || entry.Status != ParticipantNominated {
		t.Fatalf("called-up entry = %+v", entry)
	}
	if !slices.Contains(entry.Choices, ParticipantDeclined) || slices.Contains(entry.Choices, ParticipantSignedUp) || slices.Contains(entry.Choices, ParticipantLeftOut) || !entry.CanRemove {
		t.Fatalf("coach choices = %v remove %v", entry.Choices, entry.CanRemove)
	}

	*setup.now = detail.Event.StartsAt.Add(time.Minute)
	if roster, err = setup.service.Roster(ctx, coachOf(1).Access, detail); err != nil || len(roster.CallUps) != 0 {
		t.Fatalf("started event call-ups = %+v, %v", roster.CallUps, err)
	}
}

func TestCalledUpPlayerAndParentSeeTheEvent(t *testing.T) {
	event := storedEvent(1, 1)
	other := storedEvent(2, 1)
	tests := []struct {
		name   string
		grants GroupAccessGrants
		want   map[int64]bool
	}{
		{name: "player of another team", grants: GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}}}, want: map[int64]bool{1: false, 2: false}},
		{name: "called-up player", grants: GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}}, CalledUpEventIDs: []int64{1}}, want: map[int64]bool{1: true, 2: false}},
		{name: "called-up player without a team", grants: GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, CalledUpEventIDs: []int64{2}}, want: map[int64]bool{1: false, 2: true}},
		{name: "parent of a called-up child", grants: GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildUserIDs: []int64{20}, ChildTeamIDs: []int64{2}, CalledUpEventIDs: []int64{1}}, want: map[int64]bool{1: true, 2: false}},
		{name: "non-member with a stale grant", grants: GroupAccessGrants{CalledUpEventIDs: []int64{1}}, want: map[int64]bool{1: false, 2: false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := accessFor(test.grants)
			for _, candidate := range []Event{event, other} {
				if got := access.CanViewEvent(candidate); got != test.want[candidate.ID] {
					t.Errorf("CanViewEvent(%d) = %v, want %v", candidate.ID, got, test.want[candidate.ID])
				}
			}
			// A call-up shows the event, not the teams.
			if access.CanViewTeam(1) {
				t.Error("a call-up must not reveal the event's team")
			}
		})
	}
}

func TestEventScopeIncludesCallUps(t *testing.T) {
	access := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}}, CalledUpEventIDs: []int64{9, 4}})
	scope := eventScope(access)
	if scope.All || !slices.Equal(scope.TeamIDs, []int64{2}) || !slices.Equal(scope.EventIDs, []int64{9, 4}) || scope.UserID != 7 {
		t.Fatalf("scope = %+v", scope)
	}
	if !eventScope(accessFor(memberWith(GroupRoleManager))).All {
		t.Fatal("staff scope must stay unrestricted")
	}
}

type callUpSourceStub struct {
	ids   []int64
	asked []int64
}

func (s *callUpSourceStub) CalledUpEventIDs(_ context.Context, _ int64, userIDs []int64) ([]int64, error) {
	s.asked = slices.Clone(userIDs)
	return s.ids, nil
}

func TestGroupAccessServiceLoadsCallUpsForPlayersAndParents(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name     string
		roles    []string
		children []int64
		wantAsk  []int64
		wantSeen []int64
	}{
		{name: "player", roles: []string{GroupRolePlayer}, wantAsk: []int64{7}, wantSeen: []int64{1}},
		{name: "parent with children", roles: []string{GroupRoleParent}, children: []int64{20, 23}, wantAsk: []int64{7, 20, 23}, wantSeen: []int64{1}},
		{name: "coach has none", roles: []string{GroupRoleCoach}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewGroupAccessService(&groupAccessRepositoryStub{access: MemberAccess{Roles: test.roles}}, &groupUserRepositoryStub{users: map[int64]User{7: {ID: 7}}})
			if err != nil {
				t.Fatalf("NewGroupAccessService() error = %v", err)
			}
			source := &callUpSourceStub{ids: []int64{1}}
			service.SetCallUps(source)
			if len(test.children) > 0 {
				service.SetChildTeams(&childRelationStub{children: test.children, teamRelationStub: teamRelationStub{ids: []int64{2}}})
			}
			access, err := service.For(ctx, 7, 5)
			if err != nil {
				t.Fatalf("For() error = %v", err)
			}
			if !slices.Equal(source.asked, test.wantAsk) || !slices.Equal(access.CalledUpEventIDs(), test.wantSeen) {
				t.Fatalf("asked %v, saw %v", source.asked, access.CalledUpEventIDs())
			}
		})
	}
}
