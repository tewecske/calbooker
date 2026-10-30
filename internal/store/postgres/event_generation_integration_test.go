//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

func TestEventGenerationRepository(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	events := mustEventRepository(t, harness)
	trainings, err := NewPlanTrainingRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewPlanTrainingRepository() error = %v", err)
	}
	generation, err := NewEventGenerationRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewEventGenerationRepository() error = %v", err)
	}
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	field, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Field", KindID: kindID(t, harness, service.KindScopeResource, group.ID, "field"), CreatedAt: 100})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	lions, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	tigers, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Tigers", BirthYearFrom: 2017, BirthYearTo: 2017, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam(tigers) error = %v", err)
	}
	season, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason() error = %v", err)
	}
	spring := season.Parts[1]
	lionsTraining := service.PlanTraining{TeamID: lions.ID, ResourceID: field.ID, Week: service.SeasonWeekAll, Weekday: 2, StartMinute: 960, EndMinute: 1050}
	tigersTraining := service.PlanTraining{TeamID: tigers.ID, ResourceID: field.ID, Week: service.SeasonWeekB, Weekday: 3, StartMinute: 960, EndMinute: 1050}
	if _, err := trainings.ReplacePlanTrainings(ctx, group.ID, spring.ID, 200, func([]service.PlanTraining) ([]service.PlanTraining, error) {
		return []service.PlanTraining{lionsTraining, tigersTraining}, nil
	}); err != nil {
		t.Fatalf("ReplacePlanTrainings() error = %v", err)
	}

	teamByID := map[int64]service.Team{lions.ID: lions, tigers.ID: tigers}
	resourceByID := map[int64]service.Resource{field.ID: field}
	run := func(apply bool, weeks ...string) service.GenerationOutcome {
		t.Helper()
		var monday []time.Time
		for _, week := range weeks {
			monday = append(monday, seasonDate(week))
		}
		var outcome service.GenerationOutcome
		err := generation.Generate(ctx, group.ID, spring.ID, monday, apply, func(state service.GenerationState) (service.GenerationPlan, error) {
			var err error
			outcome, err = service.PlanGeneration(service.GenerationInput{
				GroupID: group.ID, Part: spring, Weeks: monday, State: state, Teams: teamByID, Resources: resourceByID, CreatedAt: 300,
			})
			return outcome.Plan, err
		})
		if err != nil {
			t.Fatalf("Generate(apply=%v, %v) error = %v", apply, weeks, err)
		}
		return outcome
	}
	listAll := func() []service.Event {
		t.Helper()
		list, err := events.ListEvents(ctx, group.ID, service.EventQuery{}, service.EventScope{All: true})
		if err != nil {
			t.Fatalf("ListEvents() error = %v", err)
		}
		return list
	}

	// A preview locks and writes nothing.
	if preview := run(false, "2027-01-04", "2027-01-11"); preview.Counts.Create != 3 || len(listAll()) != 0 {
		t.Fatalf("preview = %+v, events %d, want 3 planned and none written", preview.Counts, len(listAll()))
	}
	first := run(true, "2027-01-04", "2027-01-11")
	list := listAll()
	// Week A (Jan 4): Lions Tuesday; week B (Jan 11): Lions Tuesday and Tigers Wednesday.
	if first.Counts.Create != 3 || len(list) != 3 {
		t.Fatalf("first run = %+v, events %d, want 3", first.Counts, len(list))
	}
	for _, event := range list {
		if event.KindCode != service.EventKindTraining || event.SignupMode != service.EventSignupOpen || event.PlanTrainingID == nil || event.GeneratedForWeek == nil ||
			event.ResourceID == nil || *event.ResourceID != field.ID || len(event.Teams) != 1 || event.EditedAt != nil {
			t.Fatalf("event = %+v, want an open generated training with one team and a resource", event)
		}
	}
	if !list[0].StartsAt.Equal(time.Date(2027, 1, 5, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("first start = %v, want 16:00 Budapest on 2027-01-05", list[0].StartsAt)
	}

	// Generating again changes nothing.
	again := run(true, "2027-01-04", "2027-01-11")
	if again.Counts.Unchanged != 3 || again.Counts.Changed() || len(listAll()) != 3 {
		t.Fatalf("second run = %+v, want 3 unchanged", again.Counts)
	}

	// The unique index refuses a second event for the same training and week.
	duplicate := list[0]
	duplicate.ID = 0
	if _, err := events.CreateEvent(ctx, duplicate); err == nil {
		t.Fatal("CreateEvent(duplicate generated event) error = nil, want the unique index to refuse it")
	}

	// An edit by hand and an answer protect events; the rest follow the plan.
	week4 := list[0]
	edited := week4
	edited.Title = "Lions (moved)"
	edited.EditedAt = int64Pointer(400)
	if _, err := events.UpdateEvent(ctx, edited, week4.Version); err != nil {
		t.Fatalf("UpdateEvent() error = %v", err)
	}
	answered := list[1]
	if _, err := harness.DB.ExecContext(ctx, `INSERT INTO event_participants (group_id, event_id, user_id, status, updated_at) VALUES ($1, $2, $3, 'signed_up', 1)`,
		group.ID, answered.ID, adminID); err != nil {
		t.Fatalf("insert participant error = %v", err)
	}
	// Move Lions to Thursday.
	var current []service.PlanTraining
	if current, err = trainings.ListPlanTrainings(ctx, group.ID, spring.ID); err != nil {
		t.Fatalf("ListPlanTrainings() error = %v", err)
	}
	moved := make([]service.PlanTraining, 0, len(current))
	for _, training := range current {
		if training.TeamID == lions.ID {
			training.Weekday = 4
		}
		moved = append(moved, training)
	}
	if _, err := trainings.ReplacePlanTrainings(ctx, group.ID, spring.ID, 201, func([]service.PlanTraining) ([]service.PlanTraining, error) { return moved, nil }); err != nil {
		t.Fatalf("move training error = %v", err)
	}
	regen := run(true, "2027-01-04", "2027-01-11")
	if regen.Counts.Keep != 2 || regen.Counts.Rebuild != 0 || regen.Counts.Unchanged != 1 {
		t.Fatalf("regenerate = %+v, want the edited and the answered Lions events kept and Tigers unchanged", regen.Counts)
	}
	for _, event := range listAll() {
		if event.ID == week4.ID && event.Title != "Lions (moved)" {
			t.Fatalf("edited event = %+v, want it untouched", event)
		}
	}
	// A new week follows the moved plan.
	if next := run(true, "2027-01-18"); next.Counts.Create != 1 {
		t.Fatalf("week 3 = %+v, want 1 created", next.Counts)
	}

	// Deleting a training clears the link; its untouched event is removed on
	// the next run, the protected ones stay.
	if _, err := trainings.ReplacePlanTrainings(ctx, group.ID, spring.ID, 202, func(rows []service.PlanTraining) ([]service.PlanTraining, error) { return nil, nil }); err != nil {
		t.Fatalf("delete trainings error = %v", err)
	}
	for _, event := range listAll() {
		if event.PlanTrainingID != nil {
			t.Fatalf("event %d still links training %d after the training was deleted", event.ID, *event.PlanTrainingID)
		}
	}
	removed := run(true, "2027-01-04", "2027-01-11", "2027-01-18")
	if removed.Counts.Remove != 2 || removed.Counts.Keep != 2 {
		t.Fatalf("after deleting the plan = %+v, want 2 removed and 2 kept", removed.Counts)
	}
	left := listAll()
	if len(left) != 2 {
		t.Fatalf("events left = %d, want the edited and the answered one", len(left))
	}

	// A failing plan writes nothing; a missing part is not-found.
	boom := errors.New("boom")
	if err := generation.Generate(ctx, group.ID, spring.ID, []time.Time{seasonDate("2027-01-04")}, true, func(service.GenerationState) (service.GenerationPlan, error) {
		return service.GenerationPlan{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Generate(failing plan) error = %v, want the plan's error", err)
	}
	if err := generation.Generate(ctx, group.ID, spring.ID+999, []time.Time{seasonDate("2027-01-04")}, true, func(service.GenerationState) (service.GenerationPlan, error) {
		return service.GenerationPlan{}, nil
	}); !errors.Is(err, service.ErrSeasonNotFound) {
		t.Fatalf("Generate(missing part) error = %v, want ErrSeasonNotFound", err)
	}
	// A plan that deletes an event with answers is refused.
	if err := generation.Generate(ctx, group.ID, spring.ID, []time.Time{answered.GeneratedForWeek.Truncate(24 * time.Hour)}, true, func(service.GenerationState) (service.GenerationPlan, error) {
		return service.GenerationPlan{Delete: []int64{answered.ID}}, nil
	}); !errors.Is(err, service.ErrEventHasParticipants) {
		t.Fatalf("Generate(delete answered) error = %v, want ErrEventHasParticipants", err)
	}
	if len(listAll()) != 2 {
		t.Fatal("a refused plan changed events")
	}
}

func int64Pointer(value int64) *int64 { return &value }
