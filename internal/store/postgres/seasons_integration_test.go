//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/tests/postgres"
)

func mustSeasonRepository(t *testing.T, harness *postgres.Harness) *SeasonRepository {
	t.Helper()
	repository, err := NewSeasonRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewSeasonRepository() error = %v", err)
	}
	return repository
}

func seasonDate(raw string) time.Time {
	return service.ParseSeasonDate(raw)
}

func twoPartSeason(groupID int64, name, starts, autumnEnds, springStarts, ends string) service.Season {
	return service.Season{
		GroupID: groupID, Name: name, StartsOn: seasonDate(starts), EndsOn: seasonDate(ends), CreatedAt: 100,
		Parts: []service.SeasonPart{
			{Position: 1, Name: "Autumn", StartsOn: seasonDate(starts), EndsOn: seasonDate(autumnEnds)},
			{Position: 2, Name: "Spring", StartsOn: seasonDate(springStarts), EndsOn: seasonDate(ends), AlternatingWeeks: true},
		},
	}
}

func TestSeasonRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", TimeZone: "Pacific/Auckland", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(other) error = %v", err)
	}
	if zone, err := seasons.GroupTimeZone(ctx, group.ID); err != nil || zone == "" {
		t.Fatalf("GroupTimeZone() = %q, %v", zone, err)
	}
	if _, err := seasons.GroupTimeZone(ctx, 999999); !errors.Is(err, service.ErrGroupNotFound) {
		t.Fatalf("GroupTimeZone(missing) error = %v", err)
	}

	created, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason() error = %v", err)
	}
	if created.ID <= 0 || created.Version != 0 || len(created.Parts) != 2 || created.Parts[0].Position != 1 || !created.Parts[1].AlternatingWeeks ||
		service.FormatSeasonDate(created.StartsOn) != "2026-08-01" || service.FormatSeasonDate(created.Parts[1].StartsOn) != "2027-01-04" || created.Parts[0].GroupID != group.ID {
		t.Fatalf("created = %+v", created)
	}

	// Constraints refuse what validation refuses.
	for name, bad := range map[string]service.Season{
		"overlapping season":   twoPartSeason(group.ID, "Overlap", "2027-06-30", "2027-09-01", "2027-10-01", "2028-06-30"),
		"duplicate name":       twoPartSeason(group.ID, "2026/27", "2030-08-01", "2030-12-20", "2031-01-04", "2031-06-30"),
		"reversed dates":       {GroupID: group.ID, Name: "Reversed", StartsOn: seasonDate("2032-06-30"), EndsOn: seasonDate("2032-01-01"), CreatedAt: 100},
		"overlapping parts":    twoPartSeason(group.ID, "Parts", "2033-08-01", "2034-01-10", "2034-01-04", "2034-06-30"),
		"part of a third kind": {GroupID: group.ID, Name: "Third", StartsOn: seasonDate("2035-01-01"), EndsOn: seasonDate("2035-12-31"), CreatedAt: 100, Parts: []service.SeasonPart{{Position: 3, Name: "X", StartsOn: seasonDate("2035-01-01"), EndsOn: seasonDate("2035-02-01")}}},
	} {
		if _, err := seasons.CreateSeason(ctx, bad); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	if _, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "Overlap", "2027-06-30", "2027-09-01", "2027-10-01", "2028-06-30")); !errors.Is(err, service.ErrSeasonOverlap) {
		t.Fatalf("overlap error = %v, want ErrSeasonOverlap", err)
	}
	if _, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2030-08-01", "2030-12-20", "2031-01-04", "2031-06-30")); !errors.Is(err, service.ErrSeasonNameTaken) {
		t.Fatalf("duplicate name error = %v, want ErrSeasonNameTaken", err)
	}
	if _, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "Parts", "2033-08-01", "2034-01-10", "2034-01-04", "2034-06-30")); !errors.Is(err, service.ErrSeasonPartsOverlap) {
		t.Fatalf("part overlap error = %v, want ErrSeasonPartsOverlap", err)
	}
	if list, _ := seasons.ListSeasons(ctx, group.ID); len(list) != 1 {
		t.Fatalf("refused seasons left rows behind: %+v", list)
	}

	// Another group may use the same dates and name.
	foreignSeason, err := seasons.CreateSeason(ctx, twoPartSeason(other.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason(other group) error = %v", err)
	}
	next, err := seasons.CreateSeason(ctx, service.Season{GroupID: group.ID, Name: "2027/28", StartsOn: seasonDate("2027-07-01"), EndsOn: seasonDate("2028-06-30"), CreatedAt: 101,
		Parts: []service.SeasonPart{{Position: 1, Name: "Whole", StartsOn: seasonDate("2027-07-01"), EndsOn: seasonDate("2028-06-30")}}})
	if err != nil {
		t.Fatalf("CreateSeason(adjacent) error = %v", err)
	}

	// Every read is scoped to the group.
	if _, err := seasons.FindSeason(ctx, other.ID, created.ID); !errors.Is(err, service.ErrSeasonNotFound) {
		t.Fatalf("FindSeason(other group) error = %v", err)
	}
	if _, err := seasons.FindSeasonByPart(ctx, other.ID, created.Parts[1].ID); !errors.Is(err, service.ErrSeasonNotFound) {
		t.Fatalf("FindSeasonByPart(other group) error = %v", err)
	}
	if found, err := seasons.FindSeasonByPart(ctx, group.ID, created.Parts[1].ID); err != nil || found.ID != created.ID {
		t.Fatalf("FindSeasonByPart() = %+v, %v", found, err)
	}
	list, err := seasons.ListSeasons(ctx, group.ID)
	if err != nil || len(list) != 2 || list[0].ID != next.ID || list[1].ID != created.ID || len(list[1].Parts) != 2 || len(list[0].Parts) != 1 {
		t.Fatalf("ListSeasons() = %+v, %v; want latest first with parts", list, err)
	}
	for raw, want := range map[string]int64{"2026-07-31": 0, "2026-08-01": created.ID, "2027-06-30": created.ID, "2027-07-01": next.ID, "2028-07-01": 0} {
		found, err := seasons.SeasonOn(ctx, group.ID, seasonDate(raw))
		if want == 0 {
			if !errors.Is(err, service.ErrSeasonNotFound) {
				t.Errorf("SeasonOn(%s) = %+v, %v; want none", raw, found, err)
			}
			continue
		}
		if err != nil || found.ID != want {
			t.Errorf("SeasonOn(%s) = %d, %v; want %d", raw, found.ID, err, want)
		}
	}

	// Update moves both parts in one transaction (part 1 now ends after
	// part 2 used to start) and drops nothing when both stay.
	moved := created
	moved.Name = "Season 2026/27"
	moved.Parts = []service.SeasonPart{
		{Position: 1, Name: "Autumn", StartsOn: seasonDate("2026-08-01"), EndsOn: seasonDate("2027-01-31")},
		{Position: 2, Name: "Spring", StartsOn: seasonDate("2027-02-01"), EndsOn: seasonDate("2027-06-30")},
	}
	updated, err := seasons.UpdateSeason(ctx, moved, 0)
	if err != nil || updated.Version != 1 || updated.Name != "Season 2026/27" || len(updated.Parts) != 2 || updated.Parts[0].ID != created.Parts[0].ID || updated.Parts[1].ID != created.Parts[1].ID || updated.Parts[1].AlternatingWeeks {
		t.Fatalf("UpdateSeason() = %+v, %v; want parts kept by position", updated, err)
	}
	if _, err := seasons.UpdateSeason(ctx, moved, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale UpdateSeason() error = %v", err)
	}
	foreign := updated
	foreign.GroupID = other.ID
	if _, err := seasons.UpdateSeason(ctx, foreign, updated.Version); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("UpdateSeason(other group) error = %v, want ErrRecordNotFound", err)
	}
	overlapping := updated
	overlapping.EndsOn = seasonDate("2027-07-01")
	if _, err := seasons.UpdateSeason(ctx, overlapping, updated.Version); !errors.Is(err, service.ErrSeasonOverlap) {
		t.Fatalf("UpdateSeason(overlap) error = %v", err)
	}
	onePart := updated
	onePart.Parts = updated.Parts[:1]
	single, err := seasons.UpdateSeason(ctx, onePart, updated.Version)
	if err != nil || len(single.Parts) != 1 || single.Version != 2 {
		t.Fatalf("UpdateSeason(one part) = %+v, %v; want part 2 deleted", single, err)
	}

	if err := seasons.DeleteSeason(ctx, group.ID, single.ID, 1); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale DeleteSeason() error = %v", err)
	}
	if err := seasons.DeleteSeason(ctx, other.ID, single.ID, 2); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("DeleteSeason(other group) error = %v", err)
	}
	if err := seasons.DeleteSeason(ctx, group.ID, single.ID, 2); err != nil {
		t.Fatalf("DeleteSeason() error = %v", err)
	}
	if err := seasons.DeleteSeason(ctx, group.ID, single.ID, 2); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("DeleteSeason(deleted) error = %v, want ErrRecordNotFound", err)
	}
	var parts int
	if err := harness.DB.QueryRowContext(ctx, `SELECT count(*) FROM season_parts WHERE season_id = $1`, single.ID).Scan(&parts); err != nil || parts != 0 {
		t.Fatalf("parts after delete = %d, %v", parts, err)
	}
	if _, err := seasons.FindSeason(ctx, other.ID, foreignSeason.ID); err != nil {
		t.Fatalf("other group's season was touched: %v", err)
	}
}

func TestSeasonCoachesFollowCoachRole(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	coachID := insertIntegrationUser(t, harness, "coach@example.test")
	secondID := insertIntegrationUser(t, harness, "second@example.test")
	playerID := insertIntegrationGuest(t, harness)

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(other) error = %v", err)
	}
	for userID, roles := range map[int64][]string{coachID: {service.GroupRoleCoach}, secondID: {service.GroupRoleCoach}, playerID: {service.GroupRolePlayer}} {
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
	current, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason() error = %v", err)
	}
	upcoming, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2027/28", "2027-08-01", "2027-12-20", "2028-01-04", "2028-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason(upcoming) error = %v", err)
	}
	foreignSeason, err := seasons.CreateSeason(ctx, twoPartSeason(other.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason(other group) error = %v", err)
	}

	assign := func(seasonID, teamID, userID int64, head bool) error {
		return seasons.AssignSeasonCoach(ctx, service.CoachAssignment{SeasonID: seasonID, TeamID: teamID, CoachUserID: userID, Head: head}, group.ID, 103)
	}
	// Foreign keys keep assignments inside the group and to coaches.
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"player as coach":      {assign(current.ID, lions.ID, playerID, false), service.ErrSeasonCoachRoleMissing},
		"admin without coach":  {assign(current.ID, lions.ID, adminID, false), service.ErrSeasonCoachRoleMissing},
		"other group's season": {assign(foreignSeason.ID, lions.ID, coachID, false), service.ErrSeasonNotFound},
		"other group's team":   {assign(current.ID, foreignTeam.ID, coachID, false), service.ErrTeamNotFound},
		"unknown season":       {assign(999999, lions.ID, coachID, false), service.ErrSeasonNotFound},
	} {
		if !errors.Is(test.err, test.want) {
			t.Errorf("%s error = %v, want %v", name, test.err, test.want)
		}
	}

	if err := assign(current.ID, lions.ID, coachID, true); err != nil {
		t.Fatalf("AssignSeasonCoach() error = %v", err)
	}
	if err := assign(current.ID, tigers.ID, coachID, false); err != nil {
		t.Fatalf("AssignSeasonCoach(second team) error = %v", err)
	}
	if err := assign(upcoming.ID, tigers.ID, secondID, false); err != nil {
		t.Fatalf("AssignSeasonCoach(upcoming) error = %v", err)
	}
	// Marking a second head coach unmarks the first; re-assigning is an update.
	if err := assign(current.ID, lions.ID, secondID, true); err != nil {
		t.Fatalf("AssignSeasonCoach(new head) error = %v", err)
	}
	if err := assign(current.ID, lions.ID, secondID, true); err != nil {
		t.Fatalf("AssignSeasonCoach(repeat) error = %v", err)
	}
	lionsCoaches, err := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{SeasonID: current.ID, TeamID: lions.ID})
	if err != nil || len(lionsCoaches) != 2 || lionsCoaches[0].CoachUserID != secondID || !lionsCoaches[0].IsHead || lionsCoaches[1].IsHead || lionsCoaches[0].TeamName != "Lions" || lionsCoaches[0].SeasonName != "2026/27" || lionsCoaches[0].Email == nil {
		t.Fatalf("ListSeasonCoaches(lions) = %+v, %v; want one head coach first", lionsCoaches, err)
	}
	if all, err := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{}); err != nil || len(all) != 4 || all[0].SeasonID != upcoming.ID {
		t.Fatalf("ListSeasonCoaches(all) = %+v, %v; want latest season first", all, err)
	}
	if foreign, err := seasons.ListSeasonCoaches(ctx, other.ID, service.SeasonCoachFilter{}); err != nil || len(foreign) != 0 {
		t.Fatalf("ListSeasonCoaches(other group) = %+v, %v", foreign, err)
	}
	if teamCoaches, err := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{TeamID: tigers.ID}); err != nil || len(teamCoaches) != 2 {
		t.Fatalf("ListSeasonCoaches(tigers) = %+v, %v; want both seasons", teamCoaches, err)
	}

	// Coached teams follow the season containing the day.
	for raw, want := range map[string][]int64{
		"2026-10-24": {lions.ID, tigers.ID},
		"2027-07-15": {},
		"2027-09-01": {},
	} {
		ids, err := seasons.CoachedTeamIDs(ctx, group.ID, coachID, seasonDate(raw))
		if err != nil || !slices.Equal(ids, want) {
			t.Errorf("CoachedTeamIDs(coach, %s) = %v, %v; want %v", raw, ids, err, want)
		}
	}
	if ids, err := seasons.CoachedTeamIDs(ctx, group.ID, secondID, seasonDate("2027-09-01")); err != nil || !slices.Equal(ids, []int64{tigers.ID}) {
		t.Fatalf("CoachedTeamIDs(second, upcoming) = %v, %v", ids, err)
	}
	if ids, err := seasons.CoachedTeamIDs(ctx, other.ID, coachID, seasonDate("2026-10-24")); err != nil || len(ids) != 0 {
		t.Fatalf("CoachedTeamIDs(other group) = %v, %v", ids, err)
	}

	if err := seasons.RemoveSeasonCoach(ctx, group.ID, current.ID, tigers.ID, coachID); err != nil {
		t.Fatalf("RemoveSeasonCoach() error = %v", err)
	}
	if err := seasons.RemoveSeasonCoach(ctx, group.ID, current.ID, tigers.ID, coachID); !errors.Is(err, service.ErrSeasonCoachNotFound) {
		t.Fatalf("RemoveSeasonCoach(missing) error = %v", err)
	}
	if err := seasons.RemoveSeasonCoach(ctx, other.ID, current.ID, lions.ID, coachID); !errors.Is(err, service.ErrSeasonCoachNotFound) {
		t.Fatalf("RemoveSeasonCoach(other group) error = %v", err)
	}

	// Removing the coach role removes the member's assignments.
	membership, err := memberships.FindMembership(ctx, group.ID, coachID)
	if err != nil {
		t.Fatalf("FindMembership() error = %v", err)
	}
	if _, err := memberships.RemoveMemberRole(ctx, group.ID, coachID, service.GroupRoleCoach, membership.Version); err != nil {
		t.Fatalf("RemoveMemberRole() error = %v", err)
	}
	if ids, err := seasons.CoachedTeamIDs(ctx, group.ID, coachID, seasonDate("2026-10-24")); err != nil || len(ids) != 0 {
		t.Fatalf("CoachedTeamIDs after role removal = %v, %v; want none", ids, err)
	}

	// Deleting a season or a team removes its assignments.
	if err := seasons.DeleteSeason(ctx, group.ID, upcoming.ID, 0); err != nil {
		t.Fatalf("DeleteSeason() error = %v", err)
	}
	if all, _ := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{}); len(all) != 1 || all[0].CoachUserID != secondID || all[0].TeamID != lions.ID {
		t.Fatalf("assignments after season delete = %+v", all)
	}
	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM teams WHERE id = $1`, lions.ID); err != nil {
		t.Fatalf("delete team: %v", err)
	}
	if all, _ := seasons.ListSeasonCoaches(ctx, group.ID, service.SeasonCoachFilter{}); len(all) != 0 {
		t.Fatalf("assignments after team delete = %+v", all)
	}
}
