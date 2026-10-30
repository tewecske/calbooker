//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/tests/postgres"
)

type participantClub struct {
	harness      *postgres.Harness
	participants *ParticipantRepository
	events       *EventRepository
	groupID      int64
	otherGroupID int64
	adminID      int64
	playerID     int64
	guestID      int64
	childID      int64
	parentID     int64
	strangerID   int64
	lions        int64
	tigers       int64
	event        service.Event
}

func newParticipantClub(t *testing.T) participantClub {
	t.Helper()
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	profiles := mustProfileRepository(t, harness)
	participants, err := NewParticipantRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	club := participantClub{harness: harness, participants: participants, events: mustEventRepository(t, harness)}
	club.adminID = insertIntegrationUser(t, harness, "admin@example.test")
	club.playerID = insertIntegrationUser(t, harness, "player@example.test")
	club.parentID = insertIntegrationUser(t, harness, "parent@example.test")
	club.strangerID = insertIntegrationUser(t, harness, "stranger@example.test")
	club.guestID = insertIntegrationGuest(t, harness)
	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", TimeZone: "Europe/Budapest", CreatedAt: 100}, club.adminID)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", TimeZone: "Europe/Budapest", CreatedAt: 100}, club.strangerID)
	if err != nil {
		t.Fatal(err)
	}
	club.groupID, club.otherGroupID = group.ID, other.ID
	seedKinds(t, harness, group.ID, other.ID)
	for _, member := range []struct {
		id    int64
		roles []string
	}{{club.playerID, []string{service.GroupRolePlayer}}, {club.guestID, []string{service.GroupRolePlayer}}, {club.parentID, []string{service.GroupRoleParent}}} {
		if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: member.id, Roles: member.roles, CreatedAt: 101}); err != nil {
			t.Fatal(err)
		}
	}
	lions, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatal(err)
	}
	tigers, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Tigers", BirthYearFrom: 2017, BirthYearTo: 2017, CreatedAt: 102})
	if err != nil {
		t.Fatal(err)
	}
	club.lions, club.tigers = lions.ID, tigers.ID
	for _, entry := range []struct{ team, user int64 }{{lions.ID, club.playerID}, {tigers.ID, club.guestID}} {
		if err := teams.AddRosterEntry(ctx, group.ID, entry.team, entry.user, service.GroupRolePlayer, 103); err != nil {
			t.Fatal(err)
		}
	}
	child, err := profiles.CreateProfile(ctx, service.ChildProfile{GroupID: group.ID, DisplayName: "Anna", BirthYear: 2016, CreatedAt: 104}, club.parentID, lions.ID)
	if err != nil {
		t.Fatal(err)
	}
	club.childID = child.UserID
	event := integrationEvent(group.ID, "Practice", "2026-11-02T16:00:00Z", "2026-11-02T17:30:00Z", lions.ID)
	if club.event, err = club.events.CreateEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	return club
}

func setStatus(status string, by int64) service.ParticipantDecision {
	return func(state service.ParticipationState) (*service.Participant, error) {
		row := service.Participant{Status: status, UpdatedBy: &by, UpdatedAt: 300}
		if state.Current != nil {
			row.Attended = state.Current.Attended
		}
		return &row, nil
	}
}

func TestParticipantRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	repo := club.participants
	groupID, eventID := club.groupID, club.event.ID

	players, err := repo.ListEventPlayers(ctx, groupID, eventID)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[int64]service.EventPlayer)
	for _, player := range players {
		byID[player.UserID] = player
	}
	if len(players) != 3 || !slices.Equal(byID[club.playerID].TeamIDs, []int64{club.lions}) || !slices.Equal(byID[club.guestID].TeamIDs, []int64{club.tigers}) ||
		!slices.Equal(byID[club.childID].TeamIDs, []int64{club.lions}) || byID[club.childID].Kind != service.UserKindProfile || byID[club.guestID].Kind != service.UserKindGuest ||
		!byID[club.playerID].IsPlayer || byID[club.playerID].Email == nil {
		t.Fatalf("players = %+v", players)
	}
	if _, ok := byID[club.parentID]; ok {
		t.Fatal("a parent without the player role is listed")
	}

	// Insert, update, and the locked state the decision sees.
	if row, err := repo.ChangeParticipant(ctx, groupID, eventID, club.playerID, setStatus(service.ParticipantSignedUp, club.playerID)); err != nil || row == nil || row.EventID != eventID || row.UserID != club.playerID {
		t.Fatalf("insert = %+v, %v", row, err)
	}
	var seen service.ParticipationState
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.childID, func(state service.ParticipationState) (*service.Participant, error) {
		seen = state
		return setStatus(service.ParticipantNominated, club.parentID)(state)
	}); err != nil {
		t.Fatal(err)
	}
	if seen.Current != nil || seen.Taken != 1 || seen.Event.SignupMode != service.EventSignupOpen || !seen.Event.StartsAt.Equal(club.event.StartsAt) || seen.Event.Cancelled {
		t.Fatalf("state = %+v", seen)
	}
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.playerID, func(state service.ParticipationState) (*service.Participant, error) {
		seen = state
		return setStatus(service.ParticipantDeclined, club.adminID)(state)
	}); err != nil {
		t.Fatal(err)
	}
	if seen.Current == nil || seen.Current.Status != service.ParticipantSignedUp || seen.Taken != 2 {
		t.Fatalf("update state = %+v", seen)
	}
	rows, err := repo.ListParticipants(ctx, groupID, eventID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	for _, row := range rows {
		if row.UserID == club.playerID && (row.Status != service.ParticipantDeclined || row.UpdatedBy == nil || *row.UpdatedBy != club.adminID || row.UpdatedAt != 300 || row.Attended != nil) {
			t.Fatalf("player row = %+v", row)
		}
	}
	// A refused decision writes nothing.
	refusal := errors.New("refused")
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.guestID, func(service.ParticipationState) (*service.Participant, error) { return nil, refusal }); !errors.Is(err, refusal) {
		t.Fatalf("refusal error = %v", err)
	}

	// Group scoping: the other group sees no event, no players, no rows.
	if _, err := repo.ChangeParticipant(ctx, club.otherGroupID, eventID, club.strangerID, setStatus(service.ParticipantSignedUp, club.strangerID)); !errors.Is(err, service.ErrEventNotFound) {
		t.Fatalf("other group change error = %v", err)
	}
	if rows, err := repo.ListParticipants(ctx, club.otherGroupID, eventID); err != nil || len(rows) != 0 {
		t.Fatalf("other group rows = %+v, %v", rows, err)
	}
	if players, err := repo.ListEventPlayers(ctx, club.otherGroupID, eventID); err != nil || len(players) != 0 {
		t.Fatalf("other group players = %+v, %v", players, err)
	}
	// A non-member cannot get a row.
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.strangerID, setStatus(service.ParticipantSignedUp, club.adminID)); !errors.Is(err, service.ErrMembershipNotFound) {
		t.Fatalf("non-member error = %v", err)
	}

	// Deleting an event with participants is refused; without, it works.
	if err := club.events.DeleteEvent(ctx, groupID, eventID, club.event.Version); !errors.Is(err, service.ErrEventHasParticipants) {
		t.Fatalf("delete with participants error = %v", err)
	}
	empty, err := club.events.CreateEvent(ctx, integrationEvent(groupID, "Empty", "2026-11-03T16:00:00Z", "2026-11-03T17:00:00Z", club.lions))
	if err != nil {
		t.Fatal(err)
	}
	if err := club.events.DeleteEvent(ctx, groupID, empty.ID, empty.Version); err != nil {
		t.Fatalf("delete without participants error = %v", err)
	}

	// Removing a row.
	if row, err := repo.ChangeParticipant(ctx, groupID, eventID, club.childID, func(service.ParticipationState) (*service.Participant, error) { return nil, nil }); err != nil || row != nil {
		t.Fatalf("delete row = %+v, %v", row, err)
	}

	// Leaving the group removes the member's rows.
	if _, err := club.harness.DB.ExecContext(ctx, `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, club.playerID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := repo.ListParticipants(ctx, groupID, eventID); len(rows) != 0 {
		t.Fatalf("rows after leaving = %+v", rows)
	}
}

func TestParticipantRepositoryConcurrentLastPlace(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	full := errors.New("full")
	takeLastPlace := func(user int64) service.ParticipantDecision {
		return func(state service.ParticipationState) (*service.Participant, error) {
			// Widen the window between reading the count and writing.
			time.Sleep(20 * time.Millisecond)
			if state.Taken >= 1 {
				return nil, full
			}
			return setStatus(service.ParticipantSignedUp, user)(state)
		}
	}
	users := []int64{club.playerID, club.guestID, club.childID}
	results := make([]error, len(users))
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index, user := range users {
		wait.Go(func() {
			<-start
			_, results[index] = club.participants.ChangeParticipant(ctx, club.groupID, club.event.ID, user, takeLastPlace(user))
		})
	}
	close(start)
	wait.Wait()
	wins := 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, full):
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	rows, err := club.participants.ListParticipants(ctx, club.groupID, club.event.ID)
	if err != nil || wins != 1 || len(rows) != 1 {
		t.Fatalf("wins %d rows %+v err %v", wins, rows, err)
	}
}

func TestParticipantRepositoryAttendance(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	repo := club.participants
	groupID, eventID := club.groupID, club.event.ID
	for _, user := range []int64{club.playerID, club.childID} {
		if _, err := repo.ChangeParticipant(ctx, groupID, eventID, user, setStatus(service.ParticipantSignedUp, user)); err != nil {
			t.Fatal(err)
		}
	}
	tooEarly := errors.New("too early")
	marks := []service.AttendanceMark{{UserID: club.playerID, Attended: true}, {UserID: club.childID}, {UserID: club.guestID, Attended: true}}
	if err := repo.RecordAttendance(ctx, groupID, eventID, marks, club.adminID, 400, func(service.ParticipationEvent) error { return tooEarly }); !errors.Is(err, tooEarly) {
		t.Fatalf("refused attendance error = %v", err)
	}
	if err := repo.RecordAttendance(ctx, groupID, eventID, marks, club.adminID, 400, func(event service.ParticipationEvent) error {
		if !event.EndsAt.Equal(club.event.EndsAt) {
			t.Errorf("locked event = %+v", event)
		}
		return nil
	}); err != nil {
		t.Fatalf("RecordAttendance() error = %v", err)
	}
	rows, err := repo.ListParticipants(ctx, groupID, eventID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	for _, row := range rows {
		switch row.UserID {
		case club.playerID:
			if row.Status != service.ParticipantSignedUp || row.Attended == nil || !*row.Attended || *row.UpdatedBy != club.adminID {
				t.Fatalf("attended row = %+v", row)
			}
		case club.childID:
			if row.Attended == nil || *row.Attended {
				t.Fatalf("no-show row = %+v", row)
			}
		case club.guestID:
			if row.Status != "" || row.Attended == nil || !*row.Attended {
				t.Fatalf("walk-in row = %+v", row)
			}
		}
	}
	// The other group cannot record attendance on this event.
	if err := repo.RecordAttendance(ctx, club.otherGroupID, eventID, marks, club.strangerID, 400, nil); !errors.Is(err, service.ErrEventNotFound) {
		t.Fatalf("other group attendance error = %v", err)
	}

	// Confirmed attendance for statistics: in range, not cancelled, scoped.
	from, to := eventInstant("2026-11-01T00:00:00Z"), eventInstant("2026-12-01T00:00:00Z")
	records, err := repo.ListConfirmedAttendance(ctx, groupID, from, to)
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %+v, %v", records, err)
	}
	for _, record := range records {
		if record.EventID != eventID || !slices.Equal(record.TeamIDs, []int64{club.lions}) || !record.StartsAt.Equal(club.event.StartsAt) {
			t.Fatalf("record = %+v", record)
		}
	}
	if records, _ := repo.ListConfirmedAttendance(ctx, groupID, to, to.AddDate(0, 1, 0)); len(records) != 0 {
		t.Fatalf("records out of range = %+v", records)
	}
	if records, _ := repo.ListConfirmedAttendance(ctx, club.otherGroupID, from, to); len(records) != 0 {
		t.Fatalf("other group records = %+v", records)
	}

	// A walk-in marked absent again goes away; the no-show stays absent.
	if err := repo.RecordAttendance(ctx, groupID, eventID, []service.AttendanceMark{{UserID: club.guestID}}, club.adminID, 401, nil); err != nil {
		t.Fatal(err)
	}
	if rows, _ := repo.ListParticipants(ctx, groupID, eventID); len(rows) != 2 {
		t.Fatalf("rows after removing the walk-in = %+v", rows)
	}

	// Cancelled events count for no statistics.
	cancelled := club.event
	at := int64(500)
	cancelled.CancelledAt = &at
	if _, err := club.events.UpdateEvent(ctx, cancelled, cancelled.Version); err != nil {
		t.Fatal(err)
	}
	if records, _ := repo.ListConfirmedAttendance(ctx, groupID, from, to); len(records) != 0 {
		t.Fatalf("cancelled event records = %+v", records)
	}
}
