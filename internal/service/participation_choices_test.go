package service

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestParticipationCalendarChoices(t *testing.T) {
	ctx := context.Background()
	setup := newParticipationSetup(t)
	parent := childAccountParent()
	event := setup.events.events[1]
	choicesFor := func(t *testing.T, actor GroupActor, events ...Event) map[int64]CalendarChoice {
		t.Helper()
		choices, err := setup.service.CalendarChoices(ctx, actor.Access, events)
		if err != nil {
			t.Fatalf("CalendarChoices() error = %v", err)
		}
		byUser := make(map[int64]CalendarChoice, len(choices))
		for _, choice := range choices {
			byUser[choice.UserID] = choice
		}
		return byUser
	}

	t.Run("parent answers for the child", func(t *testing.T) {
		got := choicesFor(t, parent, event)
		child, ok := got[23]
		if !ok || child.Self || !slices.Equal(child.Choices, []string{ParticipantSignedUp, ParticipantDeclined}) {
			t.Fatalf("child choice = %+v (ok %v)", child, ok)
		}
		if _, ok := got[20]; ok {
			t.Error("another family's child has choices")
		}
	})
	t.Run("the current answer is not a choice", func(t *testing.T) {
		setup.repo.rows[23] = Participant{EventID: 1, UserID: 23, Status: ParticipantSignedUp}
		defer delete(setup.repo.rows, 23)
		child := choicesFor(t, parent, event)[23]
		if child.Current != ParticipantSignedUp || !slices.Equal(child.Choices, []string{ParticipantDeclined}) {
			t.Errorf("child choice = %+v", child)
		}
	})
	t.Run("nothing for staff without a player", func(t *testing.T) {
		if got := choicesFor(t, adminActor(), event); len(got) != 0 {
			t.Errorf("admin choices = %+v", got)
		}
	})
	t.Run("nothing for cancelled, started, or past deadline events", func(t *testing.T) {
		cancelled := event
		stamp := eventNow.Unix()
		cancelled.CancelledAt = &stamp
		started := event
		started.StartsAt, started.EndsAt = eventNow.Add(-time.Hour), eventNow.Add(time.Hour)
		late := event
		deadline := eventNow.Add(-time.Minute)
		late.SignupDeadline = &deadline
		if got := choicesFor(t, parent, cancelled, started, late); len(got) != 0 {
			t.Errorf("choices = %+v", got)
		}
	})
	t.Run("outsider", func(t *testing.T) {
		if _, err := setup.service.CalendarChoices(ctx, actorWith(GroupAccessGrants{}).Access, []Event{event}); err == nil {
			t.Error("outsider error = nil")
		}
	})
}
