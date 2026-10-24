//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestStatisticsRepositoryAggregation(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	repo, err := NewStatisticsRepository(club.harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	groupID := club.groupID
	// The fixture event is Lions on 2026-11-02 16:00Z. Boundaries below are
	// the November of Europe/Budapest (UTC+1 after the clocks went back on
	// 2026-10-25): [2026-10-31T23:00Z, 2026-11-30T23:00Z).
	create := func(title, starts, ends string, teams ...int64) service.Event {
		event, err := club.events.CreateEvent(ctx, integrationEvent(groupID, title, starts, ends, teams...))
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	first := create("Local midnight plus", "2026-10-31T23:30:00Z", "2026-11-01T00:30:00Z", club.lions)  // in
	before := create("Local october", "2026-10-31T22:30:00Z", "2026-10-31T23:30:00Z", club.lions)       // out (Oct 31 23:30 local)
	last := create("Local november end", "2026-11-30T22:30:00Z", "2026-11-30T23:30:00Z", club.lions)    // in
	after := create("Local december", "2026-11-30T23:30:00Z", "2026-12-01T00:30:00Z", club.lions)       // out
	shared := create("Shared", "2026-11-10T16:00:00Z", "2026-11-10T17:00:00Z", club.lions, club.tigers) // both teams
	tigersOnly := create("Tigers", "2026-11-11T16:00:00Z", "2026-11-11T17:00:00Z", club.tigers)         // tigers
	cancelled := create("Cancelled", "2026-11-12T16:00:00Z", "2026-11-12T17:00:00Z", club.lions)        //
	future := create("Not held yet", "2026-11-20T16:00:00Z", "2026-11-20T17:00:00Z", club.lions)        // after Until
	cancelledAt := int64(500)
	cancelled.CancelledAt = &cancelledAt
	if _, err := club.events.UpdateEvent(ctx, cancelled, cancelled.Version); err != nil {
		t.Fatal(err)
	}
	_ = before
	_ = after

	answer := func(eventID, userID int64, status string) {
		t.Helper()
		if _, err := club.participants.ChangeParticipant(ctx, groupID, eventID, userID, setStatus(status, club.adminID)); err != nil {
			t.Fatal(err)
		}
	}
	attend := func(eventID int64, marks ...service.AttendanceMark) {
		t.Helper()
		if err := club.participants.RecordAttendance(ctx, groupID, eventID, marks, club.adminID, 400, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Fixture event (Nov 2): player and child sign up, the player attends.
	answer(club.event.ID, club.playerID, service.ParticipantSignedUp)
	answer(club.event.ID, club.childID, service.ParticipantNominated)
	attend(club.event.ID, service.AttendanceMark{UserID: club.playerID, Attended: true}, service.AttendanceMark{UserID: club.childID})
	// First and last events of the month: player attends without answering.
	attend(first.ID, service.AttendanceMark{UserID: club.playerID, Attended: true})
	answer(last.ID, club.childID, service.ParticipantDeclined)
	// Outside the month: attended, but not counted.
	attend(before.ID, service.AttendanceMark{UserID: club.playerID, Attended: true})
	attend(after.ID, service.AttendanceMark{UserID: club.playerID, Attended: true})
	// Shared event: the guest (Tigers) attends, the player (Lions) too.
	answer(shared.ID, club.guestID, service.ParticipantSignedUp)
	attend(shared.ID, service.AttendanceMark{UserID: club.guestID, Attended: true}, service.AttendanceMark{UserID: club.playerID, Attended: true})
	// A walk-in from Tigers at a Lions-only event is not the guest's figure.
	attend(first.ID, service.AttendanceMark{UserID: club.guestID, Attended: true})
	// Cancelled and future events count for nothing.
	attend(cancelled.ID, service.AttendanceMark{UserID: club.playerID, Attended: true})
	_ = tigersOnly
	_ = future

	filter := service.StatisticsFilter{
		GroupID: groupID, AllTeams: true,
		From:  eventInstant("2026-10-31T23:00:00Z"),
		To:    eventInstant("2026-11-30T23:00:00Z"),
		Until: eventInstant("2026-11-15T00:00:00Z"),
	}
	// Until excludes "last" (Nov 30) and "future" (Nov 20) as not held yet.
	teams, err := repo.TeamStatistics(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]service.TeamStatistics)
	for _, team := range teams {
		byName[team.Name] = team
	}
	// Lions: held events fixture, first, shared = 3; players player + child = 2.
	lions := byName["Lions"]
	if len(teams) != 2 || lions.TeamID != club.lions || lions.Players != 2 || lions.Events != 3 || lions.Opportunities != 6 || lions.SignedUp != 2 || lions.Attended != 3 {
		t.Fatalf("lions = %+v (all %+v)", lions, teams)
	}
	// Tigers: held events shared and tigersOnly = 2; one player (guest).
	tigers := byName["Tigers"]
	if tigers.Players != 1 || tigers.Events != 2 || tigers.Opportunities != 2 || tigers.SignedUp != 1 || tigers.Attended != 1 {
		t.Fatalf("tigers = %+v", tigers)
	}
	if rate, ok := lions.Rate(); !ok || rate != 50 {
		t.Fatalf("lions rate = %d, %v", rate, ok)
	}

	// Team scope narrows the rows.
	narrow := filter
	narrow.AllTeams, narrow.TeamIDs = false, []int64{club.tigers}
	if teams, err = repo.TeamStatistics(ctx, narrow); err != nil || len(teams) != 1 || teams[0].TeamID != club.tigers {
		t.Fatalf("scoped teams = %+v, %v", teams, err)
	}
	narrow.TeamIDs = nil
	if teams, err = repo.TeamStatistics(ctx, narrow); err != nil || len(teams) != 0 {
		t.Fatalf("empty scope teams = %+v, %v", teams, err)
	}
	// Another group sees none of it.
	other := filter
	other.GroupID = club.otherGroupID
	if teams, err = repo.TeamStatistics(ctx, other); err != nil || len(teams) != 0 {
		t.Fatalf("other group teams = %+v, %v", teams, err)
	}
	if players, err := repo.PlayerStatistics(ctx, other); err != nil || len(players) != 0 {
		t.Fatalf("other group players = %+v, %v", players, err)
	}

	players, err := repo.PlayerStatistics(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	byUser := make(map[int64]service.PlayerStatistics)
	for _, player := range players {
		byUser[player.UserID] = player
	}
	if len(players) != 3 {
		t.Fatalf("players = %+v", players)
	}
	if p := byUser[club.playerID]; p.Events != 3 || p.SignedUp != 1 || p.Attended != 3 || p.Teams != "Lions" {
		t.Fatalf("player = %+v", p)
	}
	if p := byUser[club.childID]; p.Events != 3 || p.SignedUp != 1 || p.Attended != 0 {
		t.Fatalf("child = %+v", p)
	}
	if p := byUser[club.guestID]; p.Events != 2 || p.SignedUp != 1 || p.Attended != 1 || p.Teams != "Tigers" {
		t.Fatalf("guest = %+v", p)
	}

	// Player scope: own and children's rows, then a team's players.
	own := filter
	own.AllTeams, own.UserIDs = false, []int64{club.playerID, club.parentID}
	if players, err = repo.PlayerStatistics(ctx, own); err != nil || len(players) != 1 || players[0].UserID != club.playerID {
		t.Fatalf("own players = %+v, %v", players, err)
	}
	coach := filter
	coach.AllTeams, coach.TeamIDs = false, []int64{club.tigers}
	if players, err = repo.PlayerStatistics(ctx, coach); err != nil || len(players) != 1 || players[0].UserID != club.guestID {
		t.Fatalf("coach players = %+v, %v", players, err)
	}
	none := filter
	none.AllTeams = false
	if players, err = repo.PlayerStatistics(ctx, none); err != nil || len(players) != 0 {
		t.Fatalf("no scope players = %+v, %v", players, err)
	}

	// A wider Until adds the later Lions events as absences; a player on two
	// teams of one event counts it once.
	if _, err := club.harness.DB.ExecContext(ctx, `INSERT INTO team_members (group_id, team_id, user_id, role, created_at) VALUES ($1, $2, $3, 'player', 1)`, groupID, club.tigers, club.playerID); err != nil {
		t.Fatal(err)
	} else {
		wide := filter
		wide.Until = eventInstant("2026-12-31T00:00:00Z")
		wide.AllTeams, wide.UserIDs = false, []int64{club.playerID}
		players, err := repo.PlayerStatistics(ctx, wide)
		if err != nil || len(players) != 1 {
			t.Fatalf("two-team player = %+v, %v", players, err)
		}
		// fixture, first, last, shared (once), tigersOnly, future = 6.
		if p := players[0]; p.Events != 6 || p.Teams != "Lions, Tigers" {
			t.Fatalf("two-team player = %+v", p)
		}
	}
}
