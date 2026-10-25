package service

import (
	"context"
	"errors"
	"testing"
)

func TestParticipationCalendarAnswers(t *testing.T) {
	setup := newParticipationSetup(t)
	ctx := context.Background()
	parent := childAccountParent()
	own := parent.Access.UserID()
	setup.repo.rows = map[int64]Participant{
		own: {EventID: 1, UserID: own, Status: ParticipantSignedUp},
		23:  {EventID: 2, UserID: 23, Status: ParticipantDeclined},
		21:  {EventID: 1, UserID: 21, Status: ParticipantSignedUp},
	}
	from, to := at("2026-10-01T00:00"), at("2026-11-01T00:00")
	answers, err := setup.service.CalendarAnswers(ctx, parent.Access, from, to)
	if err != nil {
		t.Fatalf("CalendarAnswers() error = %v", err)
	}
	got := map[int64]bool{}
	for _, answer := range answers {
		got[answer.UserID] = answer.Self
	}
	if len(got) != 2 || !got[own] || got[23] {
		t.Errorf("answers = %+v, want own (self) and child 23 only", answers)
	}
	if _, err := setup.service.CalendarAnswers(ctx, actorWith(GroupAccessGrants{}).Access, from, to); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("outsider error = %v", err)
	}
	if _, err := setup.service.CalendarAnswers(ctx, parent.Access, to, from); !errors.Is(err, ErrInvalidAttendance) {
		t.Errorf("reversed range error = %v", err)
	}
}
