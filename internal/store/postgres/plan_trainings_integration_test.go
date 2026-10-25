//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestPlanTrainingRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	teams := mustTeamRepository(t, harness)
	slots, err := NewPlanSlotRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewPlanSlotRepository() error = %v", err)
	}
	trainings, err := NewPlanTrainingRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewPlanTrainingRepository() error = %v", err)
	}
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(other) error = %v", err)
	}
	field, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Field", Kind: service.ResourceKindField, CreatedAt: 100})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	foreignField, err := resources.CreateResource(ctx, service.Resource{GroupID: other.ID, Name: "Foreign", Kind: service.ResourceKindField, CreatedAt: 100})
	if err != nil {
		t.Fatalf("CreateResource(foreign) error = %v", err)
	}
	lions, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Lions", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	tigers, err := teams.CreateTeam(ctx, service.Team{GroupID: group.ID, Name: "Tigers", BirthYearFrom: 2017, BirthYearTo: 2017, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam(tigers) error = %v", err)
	}
	foreignTeam, err := teams.CreateTeam(ctx, service.Team{GroupID: other.ID, Name: "Foreign", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 102})
	if err != nil {
		t.Fatalf("CreateTeam(foreign) error = %v", err)
	}
	season, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason() error = %v", err)
	}
	autumn, spring := season.Parts[0], season.Parts[1]
	slot, err := slots.CreatePlanSlot(ctx, service.PlanSlot{
		GroupID: group.ID, SeasonPartID: autumn.ID, ResourceID: field.ID, Week: service.SeasonWeekAll,
		Weekday: 2, StartMinute: 960, EndMinute: 1080, CreatedAt: 101,
	})
	if err != nil {
		t.Fatalf("CreatePlanSlot() error = %v", err)
	}

	replace := func(partID int64, desired []service.PlanTraining) (service.TrainingChanges, error) {
		return trainings.ReplacePlanTrainings(ctx, group.ID, partID, 200, func([]service.PlanTraining) ([]service.PlanTraining, error) {
			return desired, nil
		})
	}
	tue := service.PlanTraining{TeamID: lions.ID, ResourceID: field.ID, PlanSlotID: slot.ID, Week: service.SeasonWeekAll, Weekday: 2, StartMinute: 960, EndMinute: 1050}
	thu := service.PlanTraining{TeamID: tigers.ID, ResourceID: field.ID, Week: service.SeasonWeekAll, Weekday: 4, StartMinute: 960, EndMinute: 1050}

	changes, err := replace(autumn.ID, []service.PlanTraining{tue, thu})
	if err != nil || changes != (service.TrainingChanges{Added: 2}) {
		t.Fatalf("insert = %+v, %v; want 2 added", changes, err)
	}
	saved, err := trainings.ListPlanTrainings(ctx, group.ID, autumn.ID)
	if err != nil || len(saved) != 2 || saved[0].Weekday != 2 || saved[0].PlanSlotID != slot.ID || saved[1].PlanSlotID != 0 {
		t.Fatalf("list = %+v, %v; want Tuesday in the slot, then Thursday without", saved, err)
	}
	if other, _ := trainings.ListPlanTrainings(ctx, group.ID, spring.ID); len(other) != 0 {
		t.Fatalf("spring plan = %+v, want empty", other)
	}
	if foreign, _ := trainings.ListPlanTrainings(ctx, other.ID, autumn.ID); len(foreign) != 0 {
		t.Fatalf("other group sees %+v", foreign)
	}

	// The edit sees the current set; its error is returned and nothing written.
	stop := errors.New("stop")
	if _, err := trainings.ReplacePlanTrainings(ctx, group.ID, autumn.ID, 200, func(current []service.PlanTraining) ([]service.PlanTraining, error) {
		if len(current) != 2 {
			t.Errorf("edit saw %d trainings, want 2", len(current))
		}
		return nil, stop
	}); !errors.Is(err, stop) {
		t.Fatalf("edit error = %v, want it returned", err)
	}

	// A move keeps the training's ID; a swap of two keys passes the deferred
	// unique constraint.
	moved := saved[0]
	moved.StartMinute, moved.EndMinute = 1000, 1090
	if changes, err = replace(autumn.ID, []service.PlanTraining{moved, saved[1]}); err != nil || changes != (service.TrainingChanges{Moved: 1}) {
		t.Fatalf("move = %+v, %v", changes, err)
	}
	again, _ := trainings.ListPlanTrainings(ctx, group.ID, autumn.ID)
	if again[0].ID != saved[0].ID || again[0].StartMinute != 1000 {
		t.Fatalf("after move = %+v, want the same ID at 16:40", again)
	}
	first, second := again[0], again[1]
	first.TeamID, second.TeamID = second.TeamID, first.TeamID
	first.Weekday, second.Weekday = again[1].Weekday, again[0].Weekday
	first.StartMinute, first.EndMinute, second.StartMinute, second.EndMinute = again[1].StartMinute, again[1].EndMinute, again[0].StartMinute, again[0].EndMinute
	if _, err = replace(autumn.ID, []service.PlanTraining{first, second}); err != nil {
		t.Fatalf("swap error = %v", err)
	}

	// Foreign references are refused by the composite foreign keys.
	if _, err = replace(autumn.ID, []service.PlanTraining{{TeamID: foreignTeam.ID, ResourceID: field.ID, Week: service.SeasonWeekAll, Weekday: 1, StartMinute: 600, EndMinute: 660}}); !errors.Is(err, service.ErrTeamNotFound) {
		t.Errorf("foreign team error = %v, want ErrTeamNotFound", err)
	}
	if _, err = replace(autumn.ID, []service.PlanTraining{{TeamID: lions.ID, ResourceID: foreignField.ID, Week: service.SeasonWeekAll, Weekday: 1, StartMinute: 600, EndMinute: 660}}); !errors.Is(err, service.ErrResourceNotFound) {
		t.Errorf("foreign resource error = %v, want ErrResourceNotFound", err)
	}
	if _, err = replace(autumn.ID, []service.PlanTraining{{TeamID: lions.ID, ResourceID: field.ID, Week: "C", Weekday: 1, StartMinute: 600, EndMinute: 660}}); err == nil {
		t.Error("an invalid week was accepted")
	}
	if _, err = trainings.ReplacePlanTrainings(ctx, other.ID, autumn.ID, 200, func([]service.PlanTraining) ([]service.PlanTraining, error) { return nil, nil }); !errors.Is(err, service.ErrSeasonNotFound) {
		t.Errorf("foreign part error = %v, want ErrSeasonNotFound", err)
	}
	if stored, _ := trainings.ListPlanTrainings(ctx, group.ID, autumn.ID); len(stored) != 2 {
		t.Fatalf("failed saves changed the plan: %+v", stored)
	}

	// An event generated from a training keeps living when the training goes;
	// only its link is cleared. A deleted slot leaves the training in place.
	var eventID int64
	if err := harness.DB.QueryRowContext(ctx, `
		INSERT INTO events (group_id, kind, title, starts_at, ends_at, resource_id, plan_training_id, created_at)
		VALUES ($1, 'training', 'Lions', now(), now() + interval '1 hour', $2, $3, 300) RETURNING id
	`, group.ID, field.ID, again[0].ID).Scan(&eventID); err != nil {
		t.Fatalf("insert event error = %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `INSERT INTO events (group_id, kind, title, starts_at, ends_at, resource_id, plan_training_id, created_at)
		VALUES ($1, 'training', 'Bad', now(), now() + interval '1 hour', $2, 999999, 300)`, group.ID, field.ID); err == nil {
		t.Error("an event pointing to a missing training was accepted")
	}
	slotted, _ := trainings.ListPlanTrainings(ctx, group.ID, autumn.ID)
	var slottedID int64
	for _, training := range slotted {
		if training.PlanSlotID == slot.ID {
			slottedID = training.ID
		}
	}
	if slottedID == 0 {
		t.Fatal("no training kept its slot")
	}
	if err := slots.DeletePlanSlot(ctx, group.ID, slot.ID, slot.Version); err != nil {
		t.Fatalf("DeletePlanSlot() error = %v", err)
	}
	after, _ := trainings.ListPlanTrainings(ctx, group.ID, autumn.ID)
	if len(after) != 2 {
		t.Fatalf("deleting the slot left %d trainings, want 2", len(after))
	}
	for _, training := range after {
		if training.PlanSlotID != 0 {
			t.Errorf("training %d still references the deleted slot", training.ID)
		}
	}
	if changes, err = replace(autumn.ID, nil); err != nil || changes != (service.TrainingChanges{Removed: 2}) {
		t.Fatalf("clear = %+v, %v", changes, err)
	}
	var linked *int64
	if err := harness.DB.QueryRowContext(ctx, `SELECT plan_training_id FROM events WHERE id = $1`, eventID).Scan(&linked); err != nil || linked != nil {
		t.Fatalf("event after training delete: link %v, error %v; want the event kept with a cleared link", linked, err)
	}

	// Removing a team or a part removes its trainings.
	if _, err = replace(spring.ID, []service.PlanTraining{{TeamID: lions.ID, ResourceID: field.ID, Week: service.SeasonWeekB, Weekday: 3, StartMinute: 600, EndMinute: 660}, {TeamID: tigers.ID, ResourceID: field.ID, Week: service.SeasonWeekB, Weekday: 3, StartMinute: 600, EndMinute: 660}}); err != nil {
		t.Fatalf("spring insert error = %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM teams WHERE group_id = $1 AND id = $2`, group.ID, tigers.ID); err != nil {
		t.Fatalf("delete team error = %v", err)
	}
	if left, _ := trainings.ListPlanTrainings(ctx, group.ID, spring.ID); len(left) != 1 || left[0].TeamID != lions.ID {
		t.Fatalf("after team delete = %+v, want Lions only", left)
	}
	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM seasons WHERE group_id = $1 AND id = $2`, group.ID, season.ID); err != nil {
		t.Fatalf("delete season error = %v", err)
	}
	if left, _ := trainings.ListPlanTrainings(ctx, group.ID, spring.ID); len(left) != 0 {
		t.Fatalf("after season delete = %+v, want none", left)
	}
}
