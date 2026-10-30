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

func mustEventRepository(t *testing.T, harness *postgres.Harness) *EventRepository {
	t.Helper()
	repository, err := NewEventRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewEventRepository() error = %v", err)
	}
	return repository
}

func eventInstant(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		panic(err)
	}
	return parsed
}

func integrationEvent(groupID int64, title, starts, ends string, teamIDs ...int64) service.Event {
	location := "City stadium"
	event := service.Event{
		GroupID: groupID, KindCode: service.EventKindTraining, Title: title, StartsAt: eventInstant(starts), EndsAt: eventInstant(ends),
		Location: &location, SignupMode: service.EventSignupOpen, CreatedAt: 200,
	}
	for _, id := range teamIDs {
		event.Teams = append(event.Teams, service.EventTeam{ID: id})
	}
	return event
}

func eventIDs(events []service.Event) []int64 {
	ids := make([]int64, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

func TestEventRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	events := mustEventRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	coachID := insertIntegrationUser(t, harness, "coach@example.test")
	playerID := insertIntegrationUser(t, harness, "player@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", TimeZone: "Europe/Budapest", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: coachID, Roles: []string{service.GroupRoleCoach}, CreatedAt: 101}); err != nil {
		t.Fatal(err)
	}
	if _, err := memberships.CreateMembership(ctx, service.GroupMembership{GroupID: group.ID, UserID: playerID, Roles: []string{service.GroupRolePlayer}, CreatedAt: 101}); err != nil {
		t.Fatal(err)
	}
	lions, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatal(err)
	}
	tigers, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Tigers", BirthYearFrom: 2017, BirthYearTo: 2017, CreatedAt: 102})
	if err != nil {
		t.Fatal(err)
	}
	foreignTeam, err := teams.CreateTeam(ctx, service.Team{GroupID: other.ID, Name: "Foreign", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatal(err)
	}
	court, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Court 1", KindID: kindID(t, harness, service.KindScopeResource, group.ID, "court"), CreatedAt: 103})
	if err != nil {
		t.Fatal(err)
	}
	foreignCourt, err := resources.CreateResource(ctx, service.Resource{GroupID: other.ID, Name: "Court 1", KindID: kindID(t, harness, service.KindScopeResource, other.ID, "court"), CreatedAt: 103})
	if err != nil {
		t.Fatal(err)
	}
	seedKinds(t, harness, group.ID, other.ID)
	if zone, err := events.GroupTimeZone(ctx, group.ID); err != nil || zone != "Europe/Budapest" {
		t.Fatalf("GroupTimeZone() = %q, %v", zone, err)
	}
	if _, err := events.GroupTimeZone(ctx, 999999); !errors.Is(err, service.ErrGroupNotFound) {
		t.Fatalf("GroupTimeZone(missing) error = %v", err)
	}

	// A full event: two teams, a resource, an extra coach, limit, deadline.
	match := integrationEvent(group.ID, "Cup", "2026-11-07T08:00:00Z", "2026-11-07T15:00:00Z", tigers.ID, lions.ID)
	match.KindID, match.KindCode = kindID(t, harness, service.KindScopeEvent, group.ID, service.EventKindChampionship), ""
	match.SignupMode = service.EventSignupNominated
	match.Location, match.ResourceID = nil, &court.ID
	limit := 18
	deadline := eventInstant("2026-11-06T19:00:00Z")
	match.MaxParticipants, match.SignupDeadline = &limit, &deadline
	match.Coaches = []service.EventCoach{{UserID: coachID}}
	created, err := events.CreateEvent(ctx, match)
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if created.ID <= 0 || created.Version != 0 || !created.StartsAt.Equal(match.StartsAt) || !created.EndsAt.Equal(match.EndsAt) ||
		created.ResourceName == nil || *created.ResourceName != "Court 1" || created.Location != nil || *created.MaxParticipants != 18 ||
		!created.SignupDeadline.Equal(deadline) || created.KindCode != service.EventKindChampionship || created.KindID != match.KindID || created.KindNameEN != "Championship" || created.KindNameHU != "Bajnokság" || created.Cancelled() || created.Generated() {
		t.Fatalf("created = %+v", created)
	}
	if len(created.Teams) != 2 || created.Teams[0].Name != "Lions" || created.Teams[1].Name != "Tigers" {
		t.Fatalf("teams = %+v, want both by name", created.Teams)
	}
	if !slices.Equal(created.CoachUserIDs(), []int64{coachID}) {
		t.Fatalf("coaches = %+v", created.Coaches)
	}

	// Composite foreign keys keep every reference inside the group, and
	// checks repeat the validation.
	foreign := integrationEvent(group.ID, "Bad", "2026-11-08T08:00:00Z", "2026-11-08T09:00:00Z", foreignTeam.ID)
	if _, err := events.CreateEvent(ctx, foreign); !errors.Is(err, service.ErrTeamNotFound) {
		t.Fatalf("foreign team error = %v", err)
	}
	foreign = integrationEvent(group.ID, "Bad", "2026-11-08T08:00:00Z", "2026-11-08T09:00:00Z", lions.ID)
	foreign.Location, foreign.ResourceID = nil, &foreignCourt.ID
	if _, err := events.CreateEvent(ctx, foreign); !errors.Is(err, service.ErrResourceNotFound) {
		t.Fatalf("foreign resource error = %v", err)
	}
	foreign = integrationEvent(group.ID, "Bad", "2026-11-08T08:00:00Z", "2026-11-08T09:00:00Z", lions.ID)
	foreign.Coaches = []service.EventCoach{{UserID: playerID}}
	if _, err := events.CreateEvent(ctx, foreign); !errors.Is(err, service.ErrEventCoachRoleMissing) {
		t.Fatalf("coach without the role error = %v", err)
	}
	foreign.Coaches = []service.EventCoach{{UserID: coachID}}
	foreign.GroupID = other.ID
	foreign.Teams = nil
	if _, err := events.CreateEvent(ctx, foreign); !errors.Is(err, service.ErrEventCoachRoleMissing) {
		t.Fatalf("coach of another group error = %v", err)
	}
	for name, mutate := range map[string]func(*service.Event){
		"both resource and location": func(e *service.Event) { e.ResourceID = &court.ID },
		"neither":                    func(e *service.Event) { e.Location = nil },
		"reversed times":             func(e *service.Event) { e.StartsAt, e.EndsAt = e.EndsAt, e.StartsAt },
		"deadline after the start":   func(e *service.Event) { late := e.EndsAt; e.SignupDeadline = &late },
		"limit out of range":         func(e *service.Event) { big := 1000; e.MaxParticipants = &big },
		"unknown kind":               func(e *service.Event) { e.KindID, e.KindCode = 0, "party" },
		"kind of another group":      func(e *service.Event) { e.KindID, e.KindCode = kindID(t, harness, service.KindScopeEvent, other.ID, service.EventKindMatch), "" },
		"week not a Monday": func(e *service.Event) {
			tuesday := service.ParseSeasonDate("2026-11-03")
			e.GeneratedForWeek = &tuesday
		},
	} {
		bad := integrationEvent(group.ID, "Bad", "2026-11-08T08:00:00Z", "2026-11-08T09:00:00Z", lions.ID)
		mutate(&bad)
		if _, err := events.CreateEvent(ctx, bad); err == nil {
			t.Errorf("%s was stored", name)
		}
	}

	// A generated event keeps its plan columns.
	generated := integrationEvent(group.ID, "Practice", "2026-11-02T16:00:00Z", "2026-11-02T17:30:00Z", lions.ID)
	planSeason, err := seasons.CreateSeason(ctx, service.Season{GroupID: group.ID, Name: "2024/25", StartsOn: seasonDate("2024-08-01"), EndsOn: seasonDate("2025-06-30"), CreatedAt: 100,
		Parts: []service.SeasonPart{{Position: 1, Name: "Whole", StartsOn: seasonDate("2024-08-01"), EndsOn: seasonDate("2025-06-30")}}})
	if err != nil {
		t.Fatal(err)
	}
	var plan int64
	if err := harness.DB.QueryRowContext(ctx, `
		INSERT INTO plan_trainings (group_id, season_part_id, team_id, resource_id, week, weekday, start_minute, end_minute, created_at)
		VALUES ($1, $2, $3, $4, 'all', 1, 960, 1050, 100) RETURNING id
	`, group.ID, planSeason.Parts[0].ID, lions.ID, court.ID).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	monday := service.ParseSeasonDate("2026-11-02")
	generated.PlanTrainingID, generated.GeneratedForWeek = &plan, &monday
	practice, err := events.CreateEvent(ctx, generated)
	if err != nil || practice.PlanTrainingID == nil || *practice.PlanTrainingID != plan || practice.GeneratedForWeek == nil || service.FormatSeasonDate(*practice.GeneratedForWeek) != "2026-11-02" {
		t.Fatalf("generated = %+v, %v", practice, err)
	}
	tigersOnly, err := events.CreateEvent(ctx, integrationEvent(group.ID, "Tigers practice", "2026-11-02T16:30:00Z", "2026-11-02T18:00:00Z", tigers.ID))
	if err != nil {
		t.Fatal(err)
	}
	foreignEvent, err := events.CreateEvent(ctx, integrationEvent(other.ID, "Elsewhere", "2026-11-02T16:00:00Z", "2026-11-02T17:00:00Z", foreignTeam.ID))
	if err != nil {
		t.Fatal(err)
	}

	// Reads are scoped to the group.
	if _, err := events.FindEvent(ctx, other.ID, created.ID); !errors.Is(err, service.ErrEventNotFound) {
		t.Fatalf("FindEvent(other group) error = %v", err)
	}
	all := service.EventScope{All: true}
	list, err := events.ListEvents(ctx, group.ID, service.EventQuery{}, all)
	if err != nil || !slices.Equal(eventIDs(list), []int64{practice.ID, tigersOnly.ID, created.ID}) {
		t.Fatalf("ListEvents(all) = %v, %v", eventIDs(list), err)
	}
	if slices.Contains(eventIDs(list), foreignEvent.ID) {
		t.Fatal("another group's event was listed")
	}
	for name, test := range map[string]struct {
		query service.EventQuery
		scope service.EventScope
		want  []int64
	}{
		"window overlaps":        {service.EventQuery{From: eventInstant("2026-11-02T17:00:00Z"), To: eventInstant("2026-11-02T17:30:00Z")}, all, []int64{practice.ID, tigersOnly.ID}},
		"touching is no overlap": {service.EventQuery{From: eventInstant("2026-11-02T18:00:00Z"), To: eventInstant("2026-11-07T08:00:00Z")}, all, []int64{}},
		"from only":              {service.EventQuery{From: eventInstant("2026-11-03T00:00:00Z")}, all, []int64{created.ID}},
		"team filter":            {service.EventQuery{TeamID: lions.ID}, all, []int64{practice.ID, created.ID}},
		"exclude one":            {service.EventQuery{ExcludeID: practice.ID}, all, []int64{tigersOnly.ID, created.ID}},
		"descending with limit":  {service.EventQuery{Descending: true, Limit: 2}, all, []int64{created.ID, tigersOnly.ID}},
		"scope by team":          {service.EventQuery{}, service.EventScope{TeamIDs: []int64{tigers.ID}}, []int64{tigersOnly.ID, created.ID}},
		"scope by extra coach":   {service.EventQuery{}, service.EventScope{UserID: coachID}, []int64{created.ID}},
		"empty scope":            {service.EventQuery{}, service.EventScope{UserID: playerID}, []int64{}},
		"foreign team in scope":  {service.EventQuery{}, service.EventScope{TeamIDs: []int64{foreignTeam.ID}}, []int64{}},
	} {
		got, err := events.ListEvents(ctx, group.ID, test.query, test.scope)
		if err != nil || !slices.Equal(eventIDs(got), test.want) {
			t.Errorf("%s: ListEvents() = %v, %v; want %v", name, eventIDs(got), err, test.want)
		}
	}

	// Revision-checked update replaces the teams and coaches.
	created.Title = "Autumn cup"
	created.Teams = []service.EventTeam{{ID: lions.ID}}
	created.Coaches = nil
	cancelledAt, editedAt := int64(300), int64(301)
	created.CancelledAt, created.EditedAt = &cancelledAt, &editedAt
	updated, err := events.UpdateEvent(ctx, created, 0)
	if err != nil || updated.Version != 1 || updated.Title != "Autumn cup" || !slices.Equal(updated.TeamIDs(), []int64{lions.ID}) || len(updated.Coaches) != 0 || !updated.Cancelled() || *updated.EditedAt != 301 {
		t.Fatalf("UpdateEvent() = %+v, %v", updated, err)
	}
	if active, _ := events.ListEvents(ctx, group.ID, service.EventQuery{ActiveOnly: true}, all); slices.Contains(eventIDs(active), created.ID) {
		t.Fatal("cancelled event listed as active")
	}
	if _, err := events.UpdateEvent(ctx, created, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	created.GroupID = other.ID
	if _, err := events.UpdateEvent(ctx, created, 1); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("cross-group update error = %v", err)
	}
	if err := events.DeleteEvent(ctx, group.ID, updated.ID, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := events.DeleteEvent(ctx, other.ID, updated.ID, 1); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("cross-group delete error = %v", err)
	}
	if err := events.DeleteEvent(ctx, group.ID, updated.ID, 1); err != nil {
		t.Fatalf("DeleteEvent() error = %v", err)
	}
	if err := events.DeleteEvent(ctx, group.ID, updated.ID, 1); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("repeated delete error = %v", err)
	}

	// Season coaches of the season containing a day.
	season, err := seasons.CreateSeason(ctx, service.Season{GroupID: group.ID, Name: "2026/27", StartsOn: seasonDate("2026-08-01"), EndsOn: seasonDate("2027-06-30"), CreatedAt: 100,
		Parts: []service.SeasonPart{{Position: 1, Name: "Whole", StartsOn: seasonDate("2026-08-01"), EndsOn: seasonDate("2027-06-30")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := seasons.AssignSeasonCoach(ctx, service.CoachAssignment{SeasonID: season.ID, TeamID: tigers.ID, CoachUserID: coachID, Head: true}, group.ID, 100); err != nil {
		t.Fatal(err)
	}
	coaches, err := events.TeamCoachesOn(ctx, group.ID, seasonDate("2026-11-02"))
	if err != nil || len(coaches) != 1 || coaches[0].TeamID != tigers.ID || coaches[0].UserID != coachID {
		t.Fatalf("TeamCoachesOn() = %+v, %v", coaches, err)
	}
	if coaches, _ := events.TeamCoachesOn(ctx, group.ID, seasonDate("2027-07-01")); len(coaches) != 0 {
		t.Fatalf("TeamCoachesOn(no season) = %+v", coaches)
	}
	if coaches, _ := events.TeamCoachesOn(ctx, other.ID, seasonDate("2026-11-02")); len(coaches) != 0 {
		t.Fatalf("TeamCoachesOn(other group) = %+v", coaches)
	}

	// Losing the coach role removes the extra coach entries.
	withCoach := integrationEvent(group.ID, "Coached", "2026-11-09T08:00:00Z", "2026-11-09T09:00:00Z", lions.ID)
	withCoach.Coaches = []service.EventCoach{{UserID: coachID}}
	coached, err := events.CreateEvent(ctx, withCoach)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM group_member_roles WHERE group_id = $1 AND user_id = $2 AND role = 'coach'`, group.ID, coachID); err != nil {
		t.Fatal(err)
	}
	if reloaded, err := events.FindEvent(ctx, group.ID, coached.ID); err != nil || len(reloaded.Coaches) != 0 {
		t.Fatalf("after role removal = %+v, %v", reloaded.Coaches, err)
	}

	// Deleting the group removes its events.
	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, group.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	var left int
	if err := harness.DB.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE group_id = $1`, group.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("events left after group deletion = %d, %v", left, err)
	}
}
