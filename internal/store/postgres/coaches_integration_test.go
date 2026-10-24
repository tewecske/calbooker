//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestCoachRepositoryQualificationsAndAvailability(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	coaches, err := NewCoachRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewCoachRepository() error = %v", err)
	}
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	coachID := insertIntegrationUser(t, harness, "coach@example.test")
	playerID := insertIntegrationUser(t, harness, "player@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(other) error = %v", err)
	}
	membership, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: coachID, Roles: []string{service.GroupRoleCoach}, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateMembership(coach) error = %v", err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: playerID, Roles: []string{service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatalf("CreateMembership(player) error = %v", err)
	}
	season, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason() error = %v", err)
	}
	foreign, err := seasons.CreateSeason(ctx, twoPartSeason(other.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason(other) error = %v", err)
	}
	autumn, spring := season.Parts[0].ID, season.Parts[1].ID

	// Qualifications: only coaches, ages checked, scoped to the group.
	if err := coaches.SetQualification(ctx, service.CoachQualification{GroupID: group.ID, CoachUserID: coachID, MinAge: 9, MaxAge: 12}, 200); err != nil {
		t.Fatalf("SetQualification() error = %v", err)
	}
	if err := coaches.SetQualification(ctx, service.CoachQualification{GroupID: group.ID, CoachUserID: coachID, MinAge: 10, MaxAge: 14}, 201); err != nil {
		t.Fatalf("SetQualification(replace) error = %v", err)
	}
	if err := coaches.SetQualification(ctx, service.CoachQualification{GroupID: group.ID, CoachUserID: playerID, MinAge: 9, MaxAge: 12}, 200); !errors.Is(err, service.ErrCoachNotFound) {
		t.Fatalf("player qualification error = %v", err)
	}
	if err := coaches.SetQualification(ctx, service.CoachQualification{GroupID: other.ID, CoachUserID: coachID, MinAge: 9, MaxAge: 12}, 200); !errors.Is(err, service.ErrCoachNotFound) {
		t.Fatalf("other group qualification error = %v", err)
	}
	if err := coaches.SetQualification(ctx, service.CoachQualification{GroupID: group.ID, CoachUserID: coachID, MinAge: 12, MaxAge: 9}, 200); !errors.Is(err, service.ErrInvalidQualification) {
		t.Fatalf("reversed ages error = %v", err)
	}
	list, err := coaches.ListQualifications(ctx, group.ID)
	if err != nil || len(list) != 1 || list[0].MinAge != 10 || list[0].MaxAge != 14 || list[0].CoachUserID != coachID {
		t.Fatalf("ListQualifications() = %+v, %v", list, err)
	}
	if list, _ := coaches.ListQualifications(ctx, other.ID); len(list) != 0 {
		t.Fatalf("other group qualifications = %+v", list)
	}

	// Availability: replace through EditAvailability.
	windows := []service.AvailabilityWindow{
		{Week: service.SeasonWeekAll, Weekday: 2, StartMinute: 960, EndMinute: 1080},
		{Week: service.SeasonWeekAll, Weekday: 4, StartMinute: 1020, EndMinute: 1140},
	}
	if err := coaches.EditAvailability(ctx, group.ID, autumn, coachID, 300, func(current []service.AvailabilityWindow) ([]service.AvailabilityWindow, error) {
		if len(current) != 0 {
			t.Errorf("current = %+v, want none", current)
		}
		return windows, nil
	}); err != nil {
		t.Fatalf("EditAvailability() error = %v", err)
	}
	stored, err := coaches.ListAvailability(ctx, group.ID, autumn, coachID)
	if err != nil || len(stored) != 2 || stored[0].Weekday != 2 || stored[1].StartMinute != 1020 || stored[0].GroupID != group.ID || stored[0].SeasonPartID != autumn || stored[0].ID <= 0 {
		t.Fatalf("ListAvailability() = %+v, %v", stored, err)
	}
	if all, _ := coaches.ListAvailability(ctx, group.ID, autumn, 0); len(all) != 2 {
		t.Fatalf("every coach's windows = %+v", all)
	}
	if foreignList, _ := coaches.ListAvailability(ctx, other.ID, autumn, 0); len(foreignList) != 0 {
		t.Fatalf("other group sees the windows: %+v", foreignList)
	}

	// An error from edit writes nothing.
	errStop := errors.New("stop")
	if err := coaches.EditAvailability(ctx, group.ID, autumn, coachID, 301, func([]service.AvailabilityWindow) ([]service.AvailabilityWindow, error) {
		return nil, errStop
	}); !errors.Is(err, errStop) {
		t.Fatalf("edit error = %v", err)
	}
	if stored, _ := coaches.ListAvailability(ctx, group.ID, autumn, coachID); len(stored) != 2 {
		t.Fatalf("failed edit changed windows: %+v", stored)
	}

	// Group scoping and references.
	keep := func(current []service.AvailabilityWindow) ([]service.AvailabilityWindow, error) { return current, nil }
	if err := coaches.EditAvailability(ctx, group.ID, foreign.Parts[0].ID, coachID, 302, keep); !errors.Is(err, service.ErrSeasonNotFound) {
		t.Fatalf("other group's part error = %v", err)
	}
	if err := coaches.EditAvailability(ctx, group.ID, autumn, playerID, 302, keep); !errors.Is(err, service.ErrCoachNotFound) {
		t.Fatalf("player availability error = %v", err)
	}
	if err := coaches.EditAvailability(ctx, other.ID, autumn, coachID, 302, keep); !errors.Is(err, service.ErrCoachNotFound) {
		t.Fatalf("other group coach error = %v", err)
	}
	if err := coaches.EditAvailability(ctx, group.ID, spring, coachID, 302, func([]service.AvailabilityWindow) ([]service.AvailabilityWindow, error) {
		return []service.AvailabilityWindow{{Week: "C", Weekday: 1, StartMinute: 0, EndMinute: 60}}, nil
	}); !errors.Is(err, service.ErrAvailabilityWeek) {
		t.Fatalf("invalid week error = %v", err)
	}
	if err := coaches.EditAvailability(ctx, group.ID, spring, coachID, 302, func([]service.AvailabilityWindow) ([]service.AvailabilityWindow, error) {
		return []service.AvailabilityWindow{{Week: service.SeasonWeekA, Weekday: 1, StartMinute: 600, EndMinute: 1450}}, nil
	}); !errors.Is(err, service.ErrInvalidAvailability) {
		t.Fatalf("invalid minutes error = %v", err)
	}

	// Concurrent edits of one coach serialize: no change is lost.
	var wait sync.WaitGroup
	for index := range 8 {
		wait.Go(func() {
			if err := coaches.EditAvailability(ctx, group.ID, spring, coachID, 303, func(current []service.AvailabilityWindow) ([]service.AvailabilityWindow, error) {
				return append(current, service.AvailabilityWindow{Week: service.SeasonWeekB, Weekday: 1 + index%7, StartMinute: index * 60, EndMinute: index*60 + 30}), nil
			}); err != nil {
				t.Errorf("concurrent edit error = %v", err)
			}
		})
	}
	wait.Wait()
	if stored, _ := coaches.ListAvailability(ctx, group.ID, spring, coachID); len(stored) != 8 {
		t.Fatalf("concurrent edits kept %d windows, want 8", len(stored))
	}

	// Removing the coach role removes the qualification and the windows;
	// deleting a season part removes its windows.
	if _, err := seasons.UpdateSeason(ctx, service.Season{ID: season.ID, GroupID: group.ID, Name: season.Name, StartsOn: season.StartsOn, EndsOn: season.EndsOn, Parts: season.Parts[:1]}, season.Version); err != nil {
		t.Fatalf("UpdateSeason(drop part 2) error = %v", err)
	}
	if stored, _ := coaches.ListAvailability(ctx, group.ID, spring, coachID); len(stored) != 0 {
		t.Fatalf("deleted part kept windows: %+v", stored)
	}
	if _, err := memberships.RemoveMemberRole(ctx, group.ID, coachID, service.GroupRoleCoach, membership.Version); err != nil {
		t.Fatalf("RemoveMemberRole() error = %v", err)
	}
	if stored, _ := coaches.ListAvailability(ctx, group.ID, autumn, coachID); len(stored) != 0 {
		t.Fatalf("former coach kept windows: %+v", stored)
	}
	if list, _ := coaches.ListQualifications(ctx, group.ID); len(list) != 0 {
		t.Fatalf("former coach kept the qualification: %+v", list)
	}
	if err := coaches.ClearQualification(ctx, group.ID, coachID); err != nil {
		t.Fatalf("ClearQualification(nothing) error = %v", err)
	}
}
