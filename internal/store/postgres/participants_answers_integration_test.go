//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestParticipantRepositoryListAnswers(t *testing.T) {
	ctx := context.Background()
	club := newParticipantClub(t)
	repo := club.participants
	groupID, eventID := club.groupID, club.event.ID
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.playerID, setStatus(service.ParticipantSignedUp, club.playerID)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ChangeParticipant(ctx, groupID, eventID, club.childID, setStatus(service.ParticipantDeclined, club.parentID)); err != nil {
		t.Fatal(err)
	}
	// A walk-in has attendance but no answer.
	if err := repo.RecordAttendance(ctx, groupID, eventID, []service.AttendanceMark{{UserID: club.guestID, Attended: true}}, club.adminID, 400, nil); err != nil {
		t.Fatal(err)
	}
	from, to := eventInstant("2026-11-01T00:00:00Z"), eventInstant("2026-12-01T00:00:00Z")
	answers, err := repo.ListAnswers(ctx, groupID, []int64{club.playerID, club.childID, club.guestID}, from, to)
	if err != nil || len(answers) != 2 {
		t.Fatalf("answers = %+v, %v", answers, err)
	}
	statuses := map[int64]string{}
	for _, answer := range answers {
		statuses[answer.UserID] = answer.Status
		if answer.EventID != eventID {
			t.Fatalf("answer = %+v", answer)
		}
	}
	if statuses[club.playerID] != service.ParticipantSignedUp || statuses[club.childID] != service.ParticipantDeclined {
		t.Fatalf("statuses = %v", statuses)
	}
	// Only the asked members, only the range, only the group.
	if answers, err := repo.ListAnswers(ctx, groupID, []int64{club.playerID}, from, to); err != nil || len(answers) != 1 {
		t.Fatalf("one member answers = %+v, %v", answers, err)
	}
	if answers, _ := repo.ListAnswers(ctx, groupID, []int64{club.playerID}, to, to.AddDate(0, 1, 0)); len(answers) != 0 {
		t.Fatalf("out of range answers = %+v", answers)
	}
	if answers, _ := repo.ListAnswers(ctx, club.otherGroupID, []int64{club.playerID}, from, to); len(answers) != 0 {
		t.Fatalf("other group answers = %+v", answers)
	}
	if answers, err := repo.ListAnswers(ctx, groupID, nil, from, to); err != nil || len(answers) != 0 {
		t.Fatalf("no members answers = %+v, %v", answers, err)
	}
}
