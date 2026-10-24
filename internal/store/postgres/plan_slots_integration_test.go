//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestPlanSlotRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	seasons := mustSeasonRepository(t, harness)
	slots, err := NewPlanSlotRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewPlanSlotRepository() error = %v", err)
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
	court, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Court", Kind: service.ResourceKindCourt, CreatedAt: 100})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	field, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Field", Kind: service.ResourceKindField, CreatedAt: 100})
	if err != nil {
		t.Fatalf("CreateResource(field) error = %v", err)
	}
	foreignResource, err := resources.CreateResource(ctx, service.Resource{GroupID: other.ID, Name: "Foreign", Kind: service.ResourceKindCourt, CreatedAt: 100})
	if err != nil {
		t.Fatalf("CreateResource(foreign) error = %v", err)
	}
	season, err := seasons.CreateSeason(ctx, twoPartSeason(group.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason() error = %v", err)
	}
	foreignSeason, err := seasons.CreateSeason(ctx, twoPartSeason(other.ID, "2026/27", "2026-08-01", "2026-12-20", "2027-01-04", "2027-06-30"))
	if err != nil {
		t.Fatalf("CreateSeason(foreign) error = %v", err)
	}
	autumn, spring := season.Parts[0], season.Parts[1]

	maxTeams, minAge := 2, 8
	created, err := slots.CreatePlanSlot(ctx, service.PlanSlot{
		GroupID: group.ID, SeasonPartID: spring.ID, ResourceID: court.ID, Week: service.SeasonWeekB,
		Weekday: 2, StartMinute: 1020, EndMinute: 1110, MinAge: &minAge, MaxTeams: &maxTeams, CreatedAt: 101,
	})
	if err != nil {
		t.Fatalf("CreatePlanSlot() error = %v", err)
	}
	if created.ID <= 0 || created.Version != 0 || created.Week != service.SeasonWeekB || created.MinAge == nil || *created.MinAge != 8 || created.MaxAge != nil || *created.MaxTeams != 2 {
		t.Fatalf("created = %+v", created)
	}
	early, err := slots.CreatePlanSlot(ctx, service.PlanSlot{GroupID: group.ID, SeasonPartID: spring.ID, ResourceID: field.ID, Week: service.SeasonWeekAll, Weekday: 1, StartMinute: 0, EndMinute: 1440, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreatePlanSlot(whole day) error = %v", err)
	}

	// Constraints refuse what validation refuses, and references never
	// cross groups.
	valid := service.PlanSlot{GroupID: group.ID, SeasonPartID: autumn.ID, ResourceID: court.ID, Week: service.SeasonWeekAll, Weekday: 1, StartMinute: 600, EndMinute: 660, CreatedAt: 101}
	tooMany, reversedMin, reversedMax := 100, 12, 10
	for name, mutate := range map[string]func(*service.PlanSlot){
		"week C":        func(s *service.PlanSlot) { s.Week = "C" },
		"weekday 8":     func(s *service.PlanSlot) { s.Weekday = 8 },
		"empty range":   func(s *service.PlanSlot) { s.EndMinute = s.StartMinute },
		"past midnight": func(s *service.PlanSlot) { s.EndMinute = 1441 },
		"team limit":    func(s *service.PlanSlot) { s.MaxTeams = &tooMany },
		"ages reversed": func(s *service.PlanSlot) { s.MinAge, s.MaxAge = &reversedMin, &reversedMax },
	} {
		bad := valid
		mutate(&bad)
		if _, err := slots.CreatePlanSlot(ctx, bad); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	crossResource := valid
	crossResource.ResourceID = foreignResource.ID
	if _, err := slots.CreatePlanSlot(ctx, crossResource); !errors.Is(err, service.ErrResourceNotFound) {
		t.Errorf("foreign resource error = %v", err)
	}
	crossPart := valid
	crossPart.SeasonPartID = foreignSeason.Parts[0].ID
	if _, err := slots.CreatePlanSlot(ctx, crossPart); !errors.Is(err, service.ErrSeasonNotFound) {
		t.Errorf("foreign part error = %v", err)
	}

	list, err := slots.ListPlanSlots(ctx, group.ID, spring.ID)
	if err != nil || len(list) != 2 || list[0].ID != early.ID || list[1].ID != created.ID {
		t.Fatalf("ListPlanSlots() = %+v, %v", list, err)
	}
	if list, err := slots.ListPlanSlots(ctx, other.ID, spring.ID); err != nil || len(list) != 0 {
		t.Fatalf("ListPlanSlots(other group) = %+v, %v", list, err)
	}
	if _, err := slots.FindPlanSlot(ctx, other.ID, created.ID); !errors.Is(err, service.ErrPlanSlotNotFound) {
		t.Fatalf("FindPlanSlot(other group) error = %v", err)
	}
	found, err := slots.FindPlanSlot(ctx, group.ID, created.ID)
	if err != nil || found.ResourceID != court.ID {
		t.Fatalf("FindPlanSlot() = %+v, %v", found, err)
	}

	// Revision-checked update: stale is a conflict, missing is not-found.
	found.Weekday, found.ResourceID, found.MaxTeams = 4, field.ID, nil
	updated, err := slots.UpdatePlanSlot(ctx, found, 0)
	if err != nil || updated.Version != 1 || updated.Weekday != 4 || updated.ResourceID != field.ID || updated.MaxTeams != nil || updated.MinAge == nil {
		t.Fatalf("UpdatePlanSlot() = %+v, %v", updated, err)
	}
	if _, err := slots.UpdatePlanSlot(ctx, found, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("UpdatePlanSlot(stale) error = %v", err)
	}
	foreign := updated
	foreign.GroupID = other.ID
	if _, err := slots.UpdatePlanSlot(ctx, foreign, 1); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("UpdatePlanSlot(other group) error = %v", err)
	}
	if err := slots.DeletePlanSlot(ctx, group.ID, early.ID, 1); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("DeletePlanSlot(stale) error = %v", err)
	}
	if err := slots.DeletePlanSlot(ctx, other.ID, early.ID, 0); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("DeletePlanSlot(other group) error = %v", err)
	}
	if err := slots.DeletePlanSlot(ctx, group.ID, early.ID, 0); err != nil {
		t.Fatalf("DeletePlanSlot() error = %v", err)
	}
	if err := slots.DeletePlanSlot(ctx, group.ID, early.ID, 0); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("DeletePlanSlot(gone) error = %v", err)
	}

	// Replace swaps a part's slots in one transaction and rolls back on a bad
	// slot.
	if _, err := slots.CreatePlanSlot(ctx, valid); err != nil {
		t.Fatalf("CreatePlanSlot(autumn) error = %v", err)
	}
	copies := []service.PlanSlot{
		{ResourceID: court.ID, Week: service.SeasonWeekAll, Weekday: 3, StartMinute: 600, EndMinute: 690, CreatedAt: 102},
		{ResourceID: field.ID, Week: service.SeasonWeekAll, Weekday: 5, StartMinute: 960, EndMinute: 1080, CreatedAt: 102},
	}
	removed, err := slots.ReplacePlanSlots(ctx, group.ID, autumn.ID, copies)
	if err != nil || removed != 1 {
		t.Fatalf("ReplacePlanSlots() = %d, %v", removed, err)
	}
	if list, _ := slots.ListPlanSlots(ctx, group.ID, autumn.ID); len(list) != 2 || list[0].Weekday != 3 || list[0].SeasonPartID != autumn.ID {
		t.Fatalf("after replace = %+v", list)
	}
	bad := append(copies, service.PlanSlot{ResourceID: foreignResource.ID, Week: service.SeasonWeekAll, Weekday: 1, StartMinute: 1, EndMinute: 2, CreatedAt: 102})
	if _, err := slots.ReplacePlanSlots(ctx, group.ID, autumn.ID, bad); !errors.Is(err, service.ErrResourceNotFound) {
		t.Fatalf("ReplacePlanSlots(foreign resource) error = %v", err)
	}
	if list, _ := slots.ListPlanSlots(ctx, group.ID, autumn.ID); len(list) != 2 {
		t.Fatalf("a failed replace changed the part: %+v", list)
	}
	if _, err := slots.ReplacePlanSlots(ctx, other.ID, autumn.ID, nil); !errors.Is(err, service.ErrSeasonNotFound) {
		t.Fatalf("ReplacePlanSlots(other group) error = %v", err)
	}

	// Dropping part 2 removes its plan; deleting the group removes the rest
	// even though resources are referenced.
	season.Parts = season.Parts[:1]
	if _, err := seasons.UpdateSeason(ctx, season, season.Version); err != nil {
		t.Fatalf("UpdateSeason(drop part 2) error = %v", err)
	}
	if _, err := slots.FindPlanSlot(ctx, group.ID, created.ID); !errors.Is(err, service.ErrPlanSlotNotFound) {
		t.Fatalf("slot of a dropped part error = %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, group.ID); err != nil {
		t.Fatalf("delete group with plan slots: %v", err)
	}
	var left int
	if err := harness.DB.QueryRowContext(ctx, `SELECT count(*) FROM plan_slots WHERE group_id = $1`, group.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("slots left after group deletion = %d, %v", left, err)
	}
}
