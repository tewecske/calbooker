//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func setCalledUp(status string, calledUp bool, by int64) service.ParticipantDecision {
	return func(state service.ParticipationState) (*service.Participant, error) {
		row := service.Participant{Status: status, CalledUp: calledUp, UpdatedBy: &by, UpdatedAt: 300}
		if state.Current != nil {
			row.Attended = state.Current.Attended
		}
		return &row, nil
	}
}

func TestCallUpRowsAndVisibility(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	repo := club.participants
	groupID, eventID := club.groupID, club.event.ID

	// The guest plays on Tigers; the Lions event is not theirs.
	scope := service.EventScope{TeamIDs: []int64{club.tigers}, UserID: club.guestID}
	listed := func(scope service.EventScope) []int64 {
		t.Helper()
		events, err := club.events.ListEvents(ctx, groupID, service.EventQuery{}, scope)
		if err != nil {
			t.Fatal(err)
		}
		return eventIDs(events)
	}
	if ids := listed(scope); slices.Contains(ids, eventID) {
		t.Fatalf("before the call-up the guest sees %v", ids)
	}

	row, err := repo.ChangeParticipant(ctx, groupID, eventID, club.guestID, setCalledUp(service.ParticipantNominated, true, club.adminID))
	if err != nil || row == nil || !row.CalledUp || row.Status != service.ParticipantNominated {
		t.Fatalf("call-up = %+v, %v", row, err)
	}
	rows, err := repo.ListParticipants(ctx, groupID, eventID)
	if err != nil || len(rows) != 1 || !rows[0].CalledUp {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	ids, err := repo.CalledUpEventIDs(ctx, groupID, []int64{club.guestID, club.playerID})
	if err != nil || !slices.Equal(ids, []int64{eventID}) {
		t.Fatalf("CalledUpEventIDs() = %v, %v", ids, err)
	}
	if ids, err := repo.CalledUpEventIDs(ctx, groupID, []int64{club.playerID}); err != nil || len(ids) != 0 {
		t.Fatalf("someone else's call-ups = %v, %v", ids, err)
	}
	if ids, err := repo.CalledUpEventIDs(ctx, club.otherGroupID, []int64{club.guestID}); err != nil || len(ids) != 0 {
		t.Fatalf("other group's call-ups = %v, %v", ids, err)
	}
	if ids, err := repo.CalledUpEventIDs(ctx, groupID, nil); err != nil || len(ids) != 0 {
		t.Fatalf("no users = %v, %v", ids, err)
	}

	// The scope with the call-up sees exactly that event, and not another
	// Lions event.
	other, err := club.events.CreateEvent(ctx, integrationEvent(groupID, "Other Lions", "2026-11-05T16:00:00Z", "2026-11-05T17:00:00Z", club.lions))
	if err != nil {
		t.Fatal(err)
	}
	scope.EventIDs = ids
	if got := listed(scope); !slices.Contains(got, eventID) || slices.Contains(got, other.ID) {
		t.Fatalf("with the call-up the guest sees %v", got)
	}

	// A declined call-up stays a call-up (the player can take the place back).
	if row, err := repo.ChangeParticipant(ctx, groupID, eventID, club.guestID, setCalledUp(service.ParticipantDeclined, true, club.guestID)); err != nil || !row.CalledUp {
		t.Fatalf("declined call-up = %+v, %v", row, err)
	}
	if ids, err := repo.CalledUpEventIDs(ctx, groupID, []int64{club.guestID}); err != nil || len(ids) != 1 {
		t.Fatalf("declined call-up ids = %v, %v", ids, err)
	}

	// Only nominated or declined rows can be called up; left_out is a status.
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.guestID, setCalledUp(service.ParticipantSignedUp, true, club.adminID)); !errors.Is(err, service.ErrInvalidParticipantStatus) {
		t.Fatalf("called-up sign-up error = %v, want %v", err, service.ErrInvalidParticipantStatus)
	}
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.childID, setCalledUp(service.ParticipantLeftOut, false, club.adminID)); err != nil {
		t.Fatalf("left out error = %v", err)
	}
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.childID, setCalledUp(service.ParticipantLeftOut, true, club.adminID)); !errors.Is(err, service.ErrInvalidParticipantStatus) {
		t.Fatalf("called-up left_out error = %v, want %v", err, service.ErrInvalidParticipantStatus)
	}
	// A left-out player takes no place.
	taken := 0
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.playerID, func(state service.ParticipationState) (*service.Participant, error) {
		taken = state.Taken
		return nil, nil
	}); err != nil || taken != 0 {
		t.Fatalf("taken = %d, %v; declined and left-out rows take no place", taken, err)
	}

	// Leaving the group removes the call-up with the membership.
	if _, err := club.harness.DB.ExecContext(ctx, `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, club.guestID); err != nil {
		t.Fatal(err)
	}
	if ids, err := repo.CalledUpEventIDs(ctx, groupID, []int64{club.guestID}); err != nil || len(ids) != 0 {
		t.Fatalf("call-ups after leaving = %v, %v", ids, err)
	}
}

func TestListOverlaps(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	repo := club.participants
	groupID := club.groupID
	create := func(title, starts, ends string, teams ...int64) service.Event {
		event, err := club.events.CreateEvent(ctx, integrationEvent(groupID, title, starts, ends, teams...))
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	// The fixture event is Lions 16:00-17:30 on 2026-11-02. The guest plays
	// on Tigers.
	tigersAtSameTime := create("Tigers practice", "2026-11-02T16:30:00Z", "2026-11-02T18:00:00Z", club.tigers)
	lionsAtSameTime := create("Lions match", "2026-11-02T17:00:00Z", "2026-11-02T18:00:00Z", club.lions)
	touching := create("Right after", "2026-11-02T17:30:00Z", "2026-11-02T18:30:00Z", club.tigers)
	cancelled := create("Cancelled", "2026-11-02T16:00:00Z", "2026-11-02T17:00:00Z", club.tigers)
	declined := create("Declined", "2026-11-02T16:00:00Z", "2026-11-02T17:00:00Z", club.tigers)
	leftOut := create("Left out", "2026-11-02T16:00:00Z", "2026-11-02T17:00:00Z", club.tigers)
	cancelledAt := int64(500)
	cancelled.CancelledAt = &cancelledAt
	if _, err := club.events.UpdateEvent(ctx, cancelled, cancelled.Version); err != nil {
		t.Fatal(err)
	}
	answer := func(eventID int64, status string, calledUp bool) {
		t.Helper()
		if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.guestID, setCalledUp(status, calledUp, club.adminID)); err != nil {
			t.Fatal(err)
		}
	}
	answer(declined.ID, service.ParticipantDeclined, false)
	answer(leftOut.ID, service.ParticipantLeftOut, false)
	// The guest signed up for a Lions match: an event of another team counts
	// when the guest is coming.
	answer(lionsAtSameTime.ID, service.ParticipantSignedUp, false)

	overlaps, err := repo.ListOverlaps(ctx, groupID, club.guestID, club.event.StartsAt, club.event.EndsAt, club.event.ID)
	if err != nil {
		t.Fatal(err)
	}
	titles := make([]string, 0, len(overlaps))
	for _, overlap := range overlaps {
		titles = append(titles, overlap.Title)
	}
	if !slices.Equal(titles, []string{"Tigers practice", "Lions match"}) {
		t.Fatalf("overlaps = %v, want the team event and the signed-up event", titles)
	}
	_ = tigersAtSameTime
	_ = touching

	// Someone with no team and no answers has none; the event itself and
	// other groups are never reported.
	if overlaps, err := repo.ListOverlaps(ctx, groupID, club.parentID, club.event.StartsAt, club.event.EndsAt, club.event.ID); err != nil || len(overlaps) != 0 {
		t.Fatalf("parent overlaps = %+v, %v", overlaps, err)
	}
	if overlaps, err := repo.ListOverlaps(ctx, club.otherGroupID, club.guestID, club.event.StartsAt, club.event.EndsAt, 0); err != nil || len(overlaps) != 0 {
		t.Fatalf("other group overlaps = %+v, %v", overlaps, err)
	}
	if overlaps, err := repo.ListOverlaps(ctx, groupID, club.playerID, club.event.StartsAt, club.event.EndsAt, club.event.ID); err != nil || len(overlaps) != 1 || overlaps[0].Title != "Lions match" {
		t.Fatalf("player overlaps = %+v, %v; the Lions match is on the player's team", overlaps, err)
	}
}

func TestStatisticsCountCallUpsAndLeftOut(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	repo, err := NewStatisticsRepository(club.harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	groupID := club.groupID
	later, err := club.events.CreateEvent(ctx, integrationEvent(groupID, "Lions later", "2026-11-04T16:00:00Z", "2026-11-04T17:00:00Z", club.lions))
	if err != nil {
		t.Fatal(err)
	}
	third, err := club.events.CreateEvent(ctx, integrationEvent(groupID, "Lions third", "2026-11-06T16:00:00Z", "2026-11-06T17:00:00Z", club.lions))
	if err != nil {
		t.Fatal(err)
	}
	row := func(eventID, userID int64, status string, calledUp bool) {
		t.Helper()
		if _, err := club.participants.ChangeParticipant(ctx, groupID, eventID, userID, setCalledUp(status, calledUp, club.adminID)); err != nil {
			t.Fatal(err)
		}
	}
	attend := func(eventID, userID int64) {
		t.Helper()
		if err := club.participants.RecordAttendance(ctx, groupID, eventID, []service.AttendanceMark{{UserID: userID, Attended: true}}, club.adminID, 400, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The Tigers guest is called up for two Lions events, attends one and
	// declines the other; a third Lions event has no row.
	row(club.event.ID, club.guestID, service.ParticipantNominated, true)
	attend(club.event.ID, club.guestID)
	row(later.ID, club.guestID, service.ParticipantDeclined, true)
	// The Lions player is left out of one event (a coach's decision) and the
	// child attends the fixture event.
	row(later.ID, club.playerID, service.ParticipantLeftOut, false)
	row(third.ID, club.playerID, service.ParticipantLeftOut, false)
	attend(third.ID, club.playerID)

	filter := service.StatisticsFilter{
		GroupID: groupID, AllTeams: true,
		From: eventInstant("2026-10-31T23:00:00Z"), To: eventInstant("2026-11-30T23:00:00Z"), Until: eventInstant("2026-12-01T00:00:00Z"),
	}
	players, err := repo.PlayerStatistics(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	byUser := make(map[int64]service.PlayerStatistics)
	for _, player := range players {
		byUser[player.UserID] = player
	}
	// Guest: no Tigers events; the called-up fixture event counts and was
	// attended, the declined call-up does not count.
	if p := byUser[club.guestID]; p.Events != 1 || p.Attended != 1 || p.SignedUp != 1 || p.Teams != "Tigers" {
		t.Fatalf("called-up guest = %+v", p)
	}
	// Player: fixture, later, third = 3 Lions events, minus the one left out
	// and not attended; the left-out event that was attended still counts.
	if p := byUser[club.playerID]; p.Events != 2 || p.Attended != 1 {
		t.Fatalf("left-out player = %+v", p)
	}
	if p := byUser[club.childID]; p.Events != 3 {
		t.Fatalf("child = %+v", p)
	}

	// Team figures: the left-out player is not an opportunity, and a
	// called-up guest never counts for the Lions.
	teams, err := repo.TeamStatistics(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range teams {
		switch team.TeamID {
		case club.lions:
			// 3 events x 2 players, minus one left out and not attended.
			if team.Events != 3 || team.Opportunities != 5 || team.Attended != 1 {
				t.Fatalf("lions = %+v", team)
			}
		case club.tigers:
			if team.Attended != 0 || team.Opportunities != 0 {
				t.Fatalf("tigers = %+v", team)
			}
		}
	}

	// A called-up player without any team still appears (and only for the
	// people who may see them).
	if _, err := club.harness.DB.ExecContext(ctx, `DELETE FROM team_members WHERE group_id = $1 AND user_id = $2`, groupID, club.guestID); err != nil {
		t.Fatal(err)
	}
	scoped := filter
	scoped.AllTeams, scoped.UserIDs = false, []int64{club.guestID}
	if players, err = repo.PlayerStatistics(ctx, scoped); err != nil || len(players) != 1 || players[0].Events != 1 || players[0].Attended != 1 || players[0].Teams != "" {
		t.Fatalf("teamless called-up player = %+v, %v", players, err)
	}
	coach := filter
	coach.AllTeams, coach.TeamIDs = false, []int64{club.tigers}
	if players, err = repo.PlayerStatistics(ctx, coach); err != nil || len(players) != 0 {
		t.Fatalf("a Tigers coach sees %+v", players)
	}
}

func TestPlayersAndParentsInSeveralTeams(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	profiles := mustProfileRepository(t, club.harness)
	groupID := club.groupID
	// The player joins Tigers as well; the parent gets a second child there.
	if _, err := club.harness.DB.ExecContext(ctx, `INSERT INTO team_members (group_id, team_id, user_id, role, created_at) VALUES ($1, $2, $3, 'player', 1)`, groupID, club.tigers, club.playerID); err != nil {
		t.Fatal(err)
	}
	second, err := profiles.CreateProfile(ctx, service.ChildProfile{GroupID: groupID, DisplayName: "Bela", BirthYear: 2017, CreatedAt: 105}, club.parentID, club.tigers)
	if err != nil {
		t.Fatal(err)
	}

	// Rosters: the player is on both; the parent shows on both teams through
	// the children.
	teams := mustTeamRepository(t, club.harness)
	for _, teamID := range []int64{club.lions, club.tigers} {
		roster, err := teams.ListRoster(ctx, groupID, teamID)
		if err != nil {
			t.Fatal(err)
		}
		players, parents := 0, 0
		for _, entry := range roster {
			switch {
			case entry.UserID == club.playerID && entry.Role == service.GroupRolePlayer:
				players++
			case entry.UserID == club.parentID && entry.Role == service.GroupRoleParent && entry.Linked:
				parents++
			}
		}
		if players != 1 || parents != 1 {
			t.Fatalf("team %d roster = %+v", teamID, roster)
		}
	}

	// The parent's children and their teams, and the player's teams on the
	// event list.
	children, err := profiles.ChildUserIDs(ctx, groupID, club.parentID)
	if err != nil || !slices.Equal(children, []int64{club.childID, second.UserID}) {
		t.Fatalf("ChildUserIDs() = %v, %v", children, err)
	}
	childTeams, err := profiles.TeamIDs(ctx, groupID, club.parentID)
	if err != nil || !slices.Equal(childTeams, []int64{club.lions, club.tigers}) {
		t.Fatalf("children's TeamIDs() = %v, %v", childTeams, err)
	}
	players, err := club.participants.ListEventPlayers(ctx, groupID, club.event.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, player := range players {
		if player.UserID == club.playerID && !slices.Equal(player.TeamIDs, []int64{club.lions, club.tigers}) {
			t.Fatalf("player's teams = %v, want both", player.TeamIDs)
		}
	}
	access := service.NewGroupAccess(groupID, club.parentID, service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, ChildUserIDs: children, ChildTeamIDs: childTeams})
	tigersEvent, err := club.events.CreateEvent(ctx, integrationEvent(groupID, "Tigers practice", "2026-11-03T16:00:00Z", "2026-11-03T17:00:00Z", club.tigers))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := club.events.ListEvents(ctx, groupID, service.EventQuery{}, service.EventScope{TeamIDs: access.VisibleTeamIDs(), UserID: club.parentID})
	if err != nil {
		t.Fatal(err)
	}
	if got := eventIDs(listed); !slices.Contains(got, club.event.ID) || !slices.Contains(got, tigersEvent.ID) {
		t.Fatalf("the parent sees %v, want both teams' events", got)
	}

	// Statistics: the player counts a shared event once and both teams'
	// events; a parent sees both children.
	repo, err := NewStatisticsRepository(club.harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := club.events.CreateEvent(ctx, integrationEvent(groupID, "Shared", "2026-11-04T16:00:00Z", "2026-11-04T17:00:00Z", club.lions, club.tigers))
	if err != nil {
		t.Fatal(err)
	}
	_ = shared
	filter := service.StatisticsFilter{
		GroupID: groupID, UserIDs: append([]int64{club.playerID}, children...),
		From: eventInstant("2026-10-31T23:00:00Z"), To: eventInstant("2026-11-30T23:00:00Z"), Until: eventInstant("2026-12-01T00:00:00Z"),
	}
	stats, err := repo.PlayerStatistics(ctx, filter)
	if err != nil || len(stats) != 3 {
		t.Fatalf("family statistics = %+v, %v", stats, err)
	}
	for _, p := range stats {
		switch p.UserID {
		case club.playerID:
			// fixture (Lions), tigers practice, shared once = 3.
			if p.Events != 3 || p.Teams != "Lions, Tigers" {
				t.Fatalf("two-team player = %+v", p)
			}
		case club.childID:
			if p.Events != 2 {
				t.Fatalf("Lions child = %+v", p)
			}
		case second.UserID:
			if p.Events != 2 {
				t.Fatalf("Tigers child = %+v", p)
			}
		}
	}
}
