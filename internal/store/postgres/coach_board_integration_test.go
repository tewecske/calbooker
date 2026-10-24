//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestReplaceSeasonCoaches(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	annaID := insertIntegrationUser(t, harness, "anna@example.test")
	belaID := insertIntegrationUser(t, harness, "bela@example.test")
	playerID := insertIntegrationGuest(t, harness)

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(other) error = %v", err)
	}
	for userID, roles := range map[int64][]string{annaID: {service.GroupRoleCoach}, belaID: {service.GroupRoleCoach}, playerID: {service.GroupRolePlayer}} {
		if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: userID, Roles: roles, CreatedAt: 101}); err != nil {
			t.Fatalf("CreateMembership(%d) error = %v", userID, err)
		}
	}
	lions, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	tigers, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Tigers", BirthYearFrom: 2017, BirthYearTo: 2017, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam(second) error = %v", err)
	}
	foreignTeam, err := teams.CreateTeam(ctx, service.Team{GroupID: other.ID, Name: "Foreign", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam(other group) error = %v", err)
	}
	season, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason() error = %v", err)
	}
	if err := seasons.AssignSeasonCoach(ctx, service.CoachAssignment{SeasonID: season.ID, TeamID: lions.ID, CoachUserID: annaID, Head: true}, group.ID, 103); err != nil {
		t.Fatalf("AssignSeasonCoach() error = %v", err)
	}
	if err := seasons.AssignSeasonCoach(ctx, service.CoachAssignment{SeasonID: season.ID, TeamID: tigers.ID, CoachUserID: annaID}, group.ID, 103); err != nil {
		t.Fatalf("AssignSeasonCoach(tigers) error = %v", err)
	}

	replace := func(groupID, seasonID int64, desired []service.CoachAssignment) (service.CoachAssignmentChanges, []service.CoachAssignment, error) {
		var seen []service.CoachAssignment
		changes, err := seasons.ReplaceSeasonCoaches(ctx, groupID, seasonID, 104, func(current []service.CoachAssignment) ([]service.CoachAssignment, error) {
			seen = current
			return desired, nil
		})
		return changes, seen, err
	}

	// Swap the head coach of Lions (Bela new head, Anna stays), drop Tigers.
	changes, seen, err := replace(group.ID, season.ID, []service.CoachAssignment{
		{SeasonID: season.ID, TeamID: lions.ID, CoachUserID: annaID},
		{SeasonID: season.ID, TeamID: lions.ID, CoachUserID: belaID, Head: true},
	})
	if err != nil {
		t.Fatalf("ReplaceSeasonCoaches() error = %v", err)
	}
	if len(seen) != 2 || !seen[0].Head || seen[0].TeamID != lions.ID {
		t.Fatalf("edit saw %+v", seen)
	}
	if changes != (service.CoachAssignmentChanges{Added: 1, Removed: 1, HeadChanged: 1}) {
		t.Fatalf("changes = %+v", changes)
	}
	stored, err := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{SeasonID: season.ID})
	if err != nil || len(stored) != 2 || stored[0].CoachUserID != belaID || !stored[0].IsHead || stored[1].IsHead || stored[1].TeamID != lions.ID {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	// An error from edit writes nothing.
	boom := errors.New("stale")
	if _, err := seasons.ReplaceSeasonCoaches(ctx, group.ID, season.ID, 104, func([]service.CoachAssignment) ([]service.CoachAssignment, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("edit error = %v", err)
	}
	if after, _ := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{SeasonID: season.ID}); len(after) != 2 {
		t.Fatalf("failed edit changed assignments: %+v", after)
	}

	// Foreign keys and group scoping; nothing is written on failure.
	for name, test := range map[string]struct {
		groupID, seasonID int64
		desired           []service.CoachAssignment
		want              error
	}{
		"player as coach":    {group.ID, season.ID, []service.CoachAssignment{{TeamID: lions.ID, CoachUserID: playerID}}, service.ErrSeasonCoachRoleMissing},
		"other group's team": {group.ID, season.ID, []service.CoachAssignment{{TeamID: foreignTeam.ID, CoachUserID: annaID}}, service.ErrTeamNotFound},
		"season of another":  {other.ID, season.ID, nil, service.ErrSeasonNotFound},
		"unknown season":     {group.ID, 999999, nil, service.ErrSeasonNotFound},
	} {
		if _, _, err := replace(test.groupID, test.seasonID, test.desired); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
	if after, _ := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{SeasonID: season.ID}); len(after) != 2 {
		t.Fatalf("failed replaces changed assignments: %+v", after)
	}

	// Removing everything empties the season.
	if changes, _, err := replace(group.ID, season.ID, nil); err != nil || changes.Removed != 2 {
		t.Fatalf("clear = %+v, %v", changes, err)
	}
	// Removing a coach from a season that is gone reports the assignment
	// as not found.
	if err := seasons.RemoveSeasonCoach(ctx, group.ID, 999999, lions.ID, annaID); !errors.Is(err, service.ErrSeasonCoachNotFound) {
		t.Fatalf("RemoveSeasonCoach(unknown season) error = %v", err)
	}
}
