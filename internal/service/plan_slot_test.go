package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type planSlotRepositoryStub struct {
	slots      map[int64]PlanSlot
	nextID     int64
	createErr  error
	updateErr  error
	deleteErr  error
	replaceErr error
	replaced   []PlanSlot
}

func newPlanSlotStub(slots ...PlanSlot) *planSlotRepositoryStub {
	stub := &planSlotRepositoryStub{slots: map[int64]PlanSlot{}, nextID: 100}
	for _, slot := range slots {
		stub.slots[slot.ID] = slot
	}
	return stub
}

func (r *planSlotRepositoryStub) ListPlanSlots(_ context.Context, groupID, partID int64) ([]PlanSlot, error) {
	var list []PlanSlot
	for _, slot := range r.slots {
		if slot.GroupID == groupID && slot.SeasonPartID == partID {
			list = append(list, slot)
		}
	}
	slices.SortFunc(list, func(a, b PlanSlot) int { return int(a.ID - b.ID) })
	return list, nil
}

func (r *planSlotRepositoryStub) FindPlanSlot(_ context.Context, groupID, slotID int64) (PlanSlot, error) {
	slot, ok := r.slots[slotID]
	if !ok || slot.GroupID != groupID {
		return PlanSlot{}, ErrPlanSlotNotFound
	}
	return slot, nil
}

func (r *planSlotRepositoryStub) CreatePlanSlot(_ context.Context, slot PlanSlot) (PlanSlot, error) {
	if r.createErr != nil {
		return PlanSlot{}, r.createErr
	}
	r.nextID++
	slot.ID = r.nextID
	r.slots[slot.ID] = slot
	return slot, nil
}

func (r *planSlotRepositoryStub) UpdatePlanSlot(_ context.Context, slot PlanSlot, expectedVersion int64) (PlanSlot, error) {
	if r.updateErr != nil {
		return PlanSlot{}, r.updateErr
	}
	current, ok := r.slots[slot.ID]
	if !ok {
		return PlanSlot{}, ClassifyStaleWrite(true)
	}
	if current.Version != expectedVersion {
		return PlanSlot{}, ClassifyStaleWrite(false)
	}
	slot.Version = expectedVersion + 1
	r.slots[slot.ID] = slot
	return slot, nil
}

func (r *planSlotRepositoryStub) DeletePlanSlot(_ context.Context, groupID, slotID, expectedVersion int64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	current, ok := r.slots[slotID]
	if !ok || current.GroupID != groupID {
		return ClassifyStaleWrite(true)
	}
	if current.Version != expectedVersion {
		return ClassifyStaleWrite(false)
	}
	delete(r.slots, slotID)
	return nil
}

func (r *planSlotRepositoryStub) ReplacePlanSlots(_ context.Context, groupID, partID int64, slots []PlanSlot) (int, error) {
	if r.replaceErr != nil {
		return 0, r.replaceErr
	}
	removed := 0
	for id, slot := range r.slots {
		if slot.GroupID == groupID && slot.SeasonPartID == partID {
			delete(r.slots, id)
			removed++
		}
	}
	r.replaced = slices.Clone(slots)
	for _, slot := range slots {
		r.nextID++
		slot.ID, slot.GroupID, slot.SeasonPartID = r.nextID, groupID, partID
		r.slots[slot.ID] = slot
	}
	return removed, nil
}

type planSeasonsStub struct {
	seasons []Season
	today   time.Time
}

func (s *planSeasonsStub) List(_ context.Context, access *GroupAccess) ([]Season, error) {
	if !access.CanViewSeasons() {
		return nil, ErrGroupForbidden
	}
	return s.seasons, nil
}

func (s *planSeasonsStub) FindPart(_ context.Context, access *GroupAccess, partID int64) (Season, SeasonPart, error) {
	if !access.CanView() {
		return Season{}, SeasonPart{}, ErrGroupNotFound
	}
	for _, season := range s.seasons {
		if part, ok := season.Part(partID); ok {
			return season, part, nil
		}
	}
	return Season{}, SeasonPart{}, ErrSeasonNotFound
}

func (s *planSeasonsStub) Today(context.Context, *GroupAccess) (time.Time, error) {
	return s.today, nil
}

type planResourcesStub struct {
	resources []Resource
}

func (s *planResourcesStub) List(context.Context, *GroupAccess) ([]Resource, error) {
	return s.resources, nil
}

func (s *planResourcesStub) Find(_ context.Context, _ *GroupAccess, resourceID int64) (Resource, error) {
	for _, resource := range s.resources {
		if resource.ID == resourceID {
			return resource, nil
		}
	}
	return Resource{}, ErrResourceNotFound
}

// planFixture: group 5 has last season (parts 11 autumn, 12 spring with A/B
// weeks) and this season (parts 21 autumn, 22 spring with A/B weeks); courts
// 1 (limit 2) and 2 (no limit) are active, 3 is archived.
func planFixture() (*planSeasonsStub, *planResourcesStub) {
	part := func(id, seasonID int64, position int, name, starts, ends string, alternating bool) SeasonPart {
		return SeasonPart{ID: id, GroupID: 5, SeasonID: seasonID, Position: position, Name: name, StartsOn: ParseSeasonDate(starts), EndsOn: ParseSeasonDate(ends), AlternatingWeeks: alternating}
	}
	seasons := []Season{
		{ID: 2, GroupID: 5, Name: "2026/27", StartsOn: ParseSeasonDate("2026-08-01"), EndsOn: ParseSeasonDate("2027-06-30"), Parts: []SeasonPart{
			part(21, 2, 1, "Autumn", "2026-08-01", "2026-12-20", false),
			part(22, 2, 2, "Spring", "2027-01-04", "2027-06-30", true),
		}},
		{ID: 1, GroupID: 5, Name: "2025/26", StartsOn: ParseSeasonDate("2025-08-01"), EndsOn: ParseSeasonDate("2026-06-30"), Parts: []SeasonPart{
			part(11, 1, 1, "Autumn", "2025-08-01", "2025-12-20", false),
			part(12, 1, 2, "Spring", "2026-01-05", "2026-06-30", true),
		}},
	}
	archivedAt := int64(1)
	resources := []Resource{
		{ID: 1, GroupID: 5, Name: "Court 1", Kind: ResourceKindCourt, MaxConcurrentTeams: intPtr(2)},
		{ID: 2, GroupID: 5, Name: "Court 2", Kind: ResourceKindCourt},
		{ID: 3, GroupID: 5, Name: "Old hall", Kind: ResourceKindHall, ArchivedAt: &archivedAt},
	}
	return &planSeasonsStub{seasons: seasons, today: ParseSeasonDate("2026-10-24")}, &planResourcesStub{resources: resources}
}

func newTestPlanSlotService(t *testing.T, slots *planSlotRepositoryStub) (*PlanSlotService, *auditRecorderStub) {
	t.Helper()
	seasons, resources := planFixture()
	service, err := NewPlanSlotService(slots, seasons, resources, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewPlanSlotService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	return service, auditor
}

func slotOn(id, partID, resourceID int64, week string, weekday, start, end int) PlanSlot {
	return PlanSlot{ID: id, GroupID: 5, SeasonPartID: partID, ResourceID: resourceID, Week: week, Weekday: weekday, StartMinute: start, EndMinute: end}
}

func TestNewPlanSlotServiceRejectsNilDependencies(t *testing.T) {
	seasons, resources := planFixture()
	users := usersWith()
	for name, build := range map[string]func() (*PlanSlotService, error){
		"repository": func() (*PlanSlotService, error) { return NewPlanSlotService(nil, seasons, resources, users) },
		"seasons":    func() (*PlanSlotService, error) { return NewPlanSlotService(newPlanSlotStub(), nil, resources, users) },
		"resources":  func() (*PlanSlotService, error) { return NewPlanSlotService(newPlanSlotStub(), seasons, nil, users) },
		"users": func() (*PlanSlotService, error) {
			return NewPlanSlotService(newPlanSlotStub(), seasons, resources, nil)
		},
	} {
		if _, err := build(); err == nil {
			t.Errorf("nil %s accepted", name)
		}
	}
	var missing *PlanSlotService
	if _, err := missing.Slots(context.Background(), adminActor().Access, 21); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("nil service error = %v", err)
	}
}

func TestGroupAccessPlanQuestions(t *testing.T) {
	for _, test := range []struct {
		name         string
		access       *GroupAccess
		manage, view bool
	}{
		{"admin", accessFor(memberWith(GroupRoleAdmin)), true, true},
		{"manager", accessFor(memberWith(GroupRoleManager)), true, true},
		{"site administrator", accessFor(GroupAccessGrants{SiteAdmin: true}), true, true},
		{"coach", accessFor(memberWith(GroupRoleCoach)), false, true},
		{"player", accessFor(memberWith(GroupRolePlayer)), false, false},
		{"parent", accessFor(memberWith(GroupRoleParent)), false, false},
		{"outsider", accessFor(GroupAccessGrants{}), false, false},
	} {
		if test.access.CanManagePlan() != test.manage || test.access.CanViewPlan() != test.view {
			t.Errorf("%s: manage %v view %v, want %v %v", test.name, test.access.CanManagePlan(), test.access.CanViewPlan(), test.manage, test.view)
		}
	}
}

func TestPlanSlotServiceViewByRole(t *testing.T) {
	slots := newPlanSlotStub(slotOn(1, 21, 1, SeasonWeekAll, 1, 1020, 1110), slotOn(2, 11, 1, SeasonWeekAll, 2, 600, 660))
	service, _ := newTestPlanSlotService(t, slots)
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		actor   GroupActor
		wantErr error
	}{
		{"admin", adminActor(), nil},
		{"manager", actorWith(memberWith(GroupRoleManager)), nil},
		{"site administrator", siteAdminActor(), nil},
		{"coach reads", coachOf(1), nil},
		{"player is forbidden", playerOn(1), ErrGroupForbidden},
		{"parent is forbidden", parentActor(), ErrGroupForbidden},
		{"outsider is not found", actorWith(GroupAccessGrants{}), ErrGroupNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			list, err := service.Slots(ctx, test.actor.Access, 21)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Slots() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && (len(list) != 1 || list[0].ID != 1) {
				t.Fatalf("Slots() = %+v", list)
			}
			if _, err := service.Slot(ctx, test.actor.Access, 2); !errors.Is(err, test.wantErr) {
				t.Fatalf("Slot() error = %v, want %v", err, test.wantErr)
			}
			if _, err := service.Board(ctx, test.actor.Access, 21); !errors.Is(err, test.wantErr) {
				t.Fatalf("Board() error = %v, want %v", err, test.wantErr)
			}
		})
	}
	if _, err := service.Slots(ctx, adminActor().Access, 99); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("Slots(unknown part) error = %v", err)
	}
	if _, err := service.Slot(ctx, adminActor().Access, 99); !errors.Is(err, ErrPlanSlotNotFound) {
		t.Fatalf("Slot(unknown) error = %v", err)
	}
	if _, err := service.Slot(ctx, adminActor().Access, 0); !errors.Is(err, ErrPlanSlotNotFound) {
		t.Fatalf("Slot(0) error = %v", err)
	}
}

func TestPlanSlotServiceBoard(t *testing.T) {
	slots := newPlanSlotStub(
		slotOn(1, 21, 1, SeasonWeekAll, 1, 1020, 1110),
		slotOn(2, 21, 1, SeasonWeekAll, 1, 1080, 1170),
		slotOn(3, 21, 1, SeasonWeekAll, 1, 1080, 1140),
		slotOn(4, 11, 1, SeasonWeekAll, 2, 600, 660),
	)
	service, _ := newTestPlanSlotService(t, slots)
	board, err := service.Board(context.Background(), coachOf().Access, 0)
	if err != nil {
		t.Fatalf("Board() error = %v", err)
	}
	if !board.Found || board.Part.ID != 21 || board.Season.ID != 2 || len(board.Slots) != 3 || len(board.Resources) != 3 || len(board.Seasons) != 2 {
		t.Fatalf("Board() = %+v", board)
	}
	if len(board.Warnings) != 1 || board.Warnings[0].Teams != 3 || board.Warnings[0].StartMinute != 1080 || board.Warnings[0].EndMinute != 1110 {
		t.Fatalf("warnings = %+v", board.Warnings)
	}
	// Part 1: last season's part 1, and the part before (last season's part 2).
	if len(board.CopySources) != 2 || board.CopySources[0].Kind != PlanCopyPreviousSeason || board.CopySources[0].Part.ID != 11 || board.CopySources[0].SlotCount != 1 ||
		board.CopySources[1].Kind != PlanCopyPreviousPart || board.CopySources[1].Part.ID != 12 {
		t.Fatalf("copy sources = %+v", board.CopySources)
	}

	spring, err := service.Board(context.Background(), adminActor().Access, 22)
	if err != nil {
		t.Fatalf("Board(22) error = %v", err)
	}
	if len(spring.CopySources) != 2 || spring.CopySources[0].Part.ID != 12 || spring.CopySources[1].Kind != PlanCopyPreviousPart || spring.CopySources[1].Part.ID != 21 {
		t.Fatalf("spring copy sources = %+v", spring.CopySources)
	}
	if _, err := service.Board(context.Background(), adminActor().Access, 99); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("Board(unknown) error = %v", err)
	}

	empty, err := NewPlanSlotService(newPlanSlotStub(), &planSeasonsStub{}, &planResourcesStub{}, usersWith())
	if err != nil {
		t.Fatal(err)
	}
	if board, err := empty.Board(context.Background(), adminActor().Access, 0); err != nil || board.Found {
		t.Fatalf("Board(no seasons) = %+v, %v", board, err)
	}
}

func TestDefaultPlanPart(t *testing.T) {
	seasons, _ := planFixture()
	for _, test := range []struct {
		today string
		want  int64
	}{
		{"2026-10-24", 21}, // inside autumn
		{"2026-12-28", 22}, // winter break: the season's next part
		{"2027-07-15", 22}, // after every season: the latest part
		{"2025-01-01", 11}, // before every season: the next part to start
	} {
		_, part, ok := defaultPlanPart(seasons.seasons, ParseSeasonDate(test.today))
		if !ok || part.ID != test.want {
			t.Errorf("defaultPlanPart(%s) = %d, %v, want %d", test.today, part.ID, ok, test.want)
		}
	}
	if _, _, ok := defaultPlanPart(nil, ParseSeasonDate("2026-01-01")); ok {
		t.Error("defaultPlanPart(no seasons) found a part")
	}
}

func TestPlanSlotServiceCreate(t *testing.T) {
	valid := PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 1020, EndMinute: 1110, MinAge: 8, MaxAge: 10, MaxTeams: 2}
	tests := []struct {
		name    string
		actor   GroupActor
		partID  int64
		input   PlanSlotInput
		wantErr error
	}{
		{name: "admin creates", actor: adminActor(), partID: 21, input: valid},
		{name: "manager creates without limits", actor: actorWith(memberWith(GroupRoleManager)), partID: 21, input: PlanSlotInput{ResourceID: 2, Weekday: 7, StartMinute: 0, EndMinute: 1440}},
		{name: "site administrator creates", actor: siteAdminActor(), partID: 21, input: valid},
		{name: "week B in an alternating part", actor: adminActor(), partID: 22, input: PlanSlotInput{ResourceID: 1, Week: SeasonWeekB, Weekday: 3, StartMinute: 600, EndMinute: 660}},
		{name: "coach is forbidden", actor: coachOf(1), partID: 21, input: valid, wantErr: ErrGroupForbidden},
		{name: "player is forbidden", actor: playerOn(1), partID: 21, input: valid, wantErr: ErrGroupForbidden},
		{name: "parent is forbidden", actor: parentActor(), partID: 21, input: valid, wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), partID: 21, input: valid, wantErr: ErrGroupNotFound},
		{name: "unknown part", actor: adminActor(), partID: 99, input: valid, wantErr: ErrSeasonNotFound},
		{name: "week A without alternating weeks", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Week: SeasonWeekA, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrInvalidPlanSlotWeek},
		{name: "unknown week", actor: adminActor(), partID: 22, input: PlanSlotInput{ResourceID: 1, Week: "C", Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrInvalidPlanSlotWeek},
		{name: "weekday 0", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 0, StartMinute: 600, EndMinute: 660}, wantErr: ErrInvalidPlanSlotTime},
		{name: "weekday 8", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 8, StartMinute: 600, EndMinute: 660}, wantErr: ErrInvalidPlanSlotTime},
		{name: "end before start", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 660, EndMinute: 600}, wantErr: ErrInvalidPlanSlotTime},
		{name: "past midnight", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 1400, EndMinute: 1441}, wantErr: ErrInvalidPlanSlotTime},
		{name: "negative start", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: -1, EndMinute: 60}, wantErr: ErrInvalidPlanSlotTime},
		{name: "ages reversed", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660, MinAge: 12, MaxAge: 10}, wantErr: ErrInvalidPlanSlotAges},
		{name: "age too high", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660, MaxAge: 100}, wantErr: ErrInvalidPlanSlotAges},
		{name: "unparseable age", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660, MinAge: -1}, wantErr: ErrInvalidPlanSlotAges},
		{name: "team limit too high", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660, MaxTeams: 100}, wantErr: ErrInvalidPlanSlotTeams},
		{name: "unknown resource", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 9, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrResourceNotFound},
		{name: "no resource", actor: adminActor(), partID: 21, input: PlanSlotInput{Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrResourceNotFound},
		{name: "archived resource", actor: adminActor(), partID: 21, input: PlanSlotInput{ResourceID: 3, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrResourceArchived},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			slots := newPlanSlotStub()
			service, auditor := newTestPlanSlotService(t, slots)
			created, err := service.Create(context.Background(), test.actor, test.partID, test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if len(slots.slots) != 0 || len(auditor.records) != 0 {
					t.Fatal("a refused slot was stored or audited")
				}
				return
			}
			if created.ID == 0 || created.GroupID != 5 || created.SeasonPartID != test.partID || created.ResourceID != test.input.ResourceID {
				t.Fatalf("created = %+v", created)
			}
			wantWeek := test.input.Week
			if wantWeek == "" {
				wantWeek = SeasonWeekAll
			}
			if created.Week != wantWeek {
				t.Fatalf("week = %q, want %q", created.Week, wantWeek)
			}
			if (test.input.MaxTeams == 0) != (created.MaxTeams == nil) || (test.input.MinAge == 0) != (created.MinAge == nil) {
				t.Fatalf("optional values = %+v", created)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionPlanSlotCreated || auditor.records[0].TargetType != "group" || auditor.records[0].TargetID != "5" ||
				auditor.records[0].ActorEmail != "staff@example.test" || auditor.records[0].IP != "192.0.2.1" || !strings.Contains(auditor.records[0].Detail, "part ") {
				t.Fatalf("audit = %+v", auditor.records)
			}
			if test.actor.Access.IsSiteAdmin() && !strings.HasSuffix(auditor.records[0].Detail, "(site administrator)") {
				t.Fatalf("site administrator audit detail = %q", auditor.records[0].Detail)
			}
		})
	}
}

func TestPlanSlotServiceCreateLimitsAndErrors(t *testing.T) {
	slots := newPlanSlotStub()
	for id := int64(1); id <= MaxPlanSlotsPerPart; id++ {
		slots.slots[id] = slotOn(id, 21, 2, SeasonWeekAll, 1, 600, 660)
	}
	service, _ := newTestPlanSlotService(t, slots)
	input := PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660}
	if _, err := service.Create(context.Background(), adminActor(), 21, input); !errors.Is(err, ErrPlanFull) {
		t.Fatalf("Create(full) error = %v", err)
	}
	failing := newPlanSlotStub()
	failing.createErr = errors.New("boom")
	service, _ = newTestPlanSlotService(t, failing)
	if _, err := service.Create(context.Background(), adminActor(), 21, input); err == nil || errors.Is(err, ErrPlanFull) {
		t.Fatalf("Create(store failure) error = %v", err)
	}
	failing.createErr = ErrResourceNotFound
	if _, err := service.Create(context.Background(), adminActor(), 21, input); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("Create(foreign key) error = %v", err)
	}
}

func TestPlanSlotServiceUpdate(t *testing.T) {
	stored := slotOn(1, 22, 1, SeasonWeekA, 1, 1020, 1110)
	stored.Version = 3
	onArchived := slotOn(2, 21, 3, SeasonWeekAll, 2, 600, 660)
	tests := []struct {
		name    string
		actor   GroupActor
		slotID  int64
		version int64
		input   PlanSlotInput
		wantErr error
	}{
		{name: "admin moves to both weeks", actor: adminActor(), slotID: 1, version: 3, input: PlanSlotInput{ResourceID: 2, Week: SeasonWeekAll, Weekday: 2, StartMinute: 600, EndMinute: 690, MaxTeams: 1}},
		{name: "slot stays on its archived resource", actor: adminActor(), slotID: 2, version: 0, input: PlanSlotInput{ResourceID: 3, Weekday: 3, StartMinute: 600, EndMinute: 660}},
		{name: "moving onto an archived resource", actor: adminActor(), slotID: 1, version: 3, input: PlanSlotInput{ResourceID: 3, Week: SeasonWeekA, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrResourceArchived},
		{name: "stale revision is a conflict", actor: adminActor(), slotID: 1, version: 2, input: PlanSlotInput{ResourceID: 1, Week: SeasonWeekA, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrOptimisticLockConflict},
		{name: "missing slot is not found", actor: adminActor(), slotID: 9, version: 0, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrPlanSlotNotFound},
		{name: "invalid input", actor: adminActor(), slotID: 1, version: 3, input: PlanSlotInput{ResourceID: 1, Week: SeasonWeekA, Weekday: 1, StartMinute: 600, EndMinute: 600}, wantErr: ErrInvalidPlanSlotTime},
		{name: "negative revision", actor: adminActor(), slotID: 1, version: -1, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrInvalidGroup},
		{name: "coach is forbidden", actor: coachOf(1), slotID: 1, version: 3, input: PlanSlotInput{ResourceID: 1, Week: SeasonWeekA, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrGroupForbidden},
		{name: "player is forbidden", actor: playerOn(1), slotID: 1, version: 3, input: PlanSlotInput{ResourceID: 1, Week: SeasonWeekA, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), slotID: 1, version: 3, input: PlanSlotInput{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 660}, wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			slots := newPlanSlotStub(stored, onArchived)
			service, auditor := newTestPlanSlotService(t, slots)
			updated, err := service.Update(context.Background(), test.actor, test.slotID, test.version, test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if len(auditor.records) != 0 {
					t.Fatal("a refused update was audited")
				}
				return
			}
			if updated.Version != test.version+1 || updated.Weekday != test.input.Weekday || updated.ResourceID != test.input.ResourceID || updated.GroupID != 5 {
				t.Fatalf("updated = %+v", updated)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionPlanSlotUpdated {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}
}

func TestPlanSlotServiceMoveKeepsDetails(t *testing.T) {
	inB := slotOn(1, 22, 1, SeasonWeekB, 1, 1020, 1110)
	inB.MinAge, inB.MaxAge, inB.MaxTeams = intPtr(8), intPtr(10), intPtr(2)
	// A stale A/B slot left in a part that no longer alternates.
	stale := slotOn(2, 21, 1, SeasonWeekA, 1, 600, 660)
	slots := newPlanSlotStub(inB, stale)
	service, _ := newTestPlanSlotService(t, slots)
	moved, err := service.Move(context.Background(), adminActor(), 1, 0, PlanSlotMove{ResourceID: 2, Weekday: 4, StartMinute: 960, EndMinute: 1080})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if moved.Week != SeasonWeekB || moved.Weekday != 4 || moved.ResourceID != 2 || moved.StartMinute != 960 || moved.EndMinute != 1080 ||
		*moved.MinAge != 8 || *moved.MaxAge != 10 || *moved.MaxTeams != 2 {
		t.Fatalf("moved = %+v", moved)
	}
	normalized, err := service.Move(context.Background(), adminActor(), 2, 0, PlanSlotMove{ResourceID: 1, Weekday: 1, StartMinute: 600, EndMinute: 720})
	if err != nil || normalized.Week != SeasonWeekAll {
		t.Fatalf("Move(stale week) = %+v, %v", normalized, err)
	}
	if _, err := service.Move(context.Background(), adminActor(), 1, 0, PlanSlotMove{ResourceID: 2, Weekday: 4, StartMinute: 960, EndMinute: 1080}); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("Move(stale) error = %v", err)
	}
	if _, err := service.Move(context.Background(), coachOf(), 1, 1, PlanSlotMove{ResourceID: 2, Weekday: 4, StartMinute: 960, EndMinute: 1080}); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("Move(coach) error = %v", err)
	}
}

func TestPlanSlotServiceDelete(t *testing.T) {
	stored := slotOn(1, 21, 1, SeasonWeekAll, 1, 600, 660)
	stored.Version = 2
	slots := newPlanSlotStub(stored)
	service, auditor := newTestPlanSlotService(t, slots)
	ctx := context.Background()
	if err := service.Delete(ctx, coachOf(), 1, 2); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("Delete(coach) error = %v", err)
	}
	if err := service.Delete(ctx, adminActor(), 1, 1); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("Delete(stale) error = %v", err)
	}
	if err := service.Delete(ctx, adminActor(), 1, 2); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(slots.slots) != 0 || len(auditor.records) != 1 || auditor.records[0].Action != AuditActionPlanSlotDeleted {
		t.Fatalf("after delete: slots %v, audit %+v", slots.slots, auditor.records)
	}
	if err := service.Delete(ctx, adminActor(), 1, 2); !errors.Is(err, ErrPlanSlotNotFound) {
		t.Fatalf("Delete(gone) error = %v", err)
	}
	// A slot deleted between the lookup and the write is not-found, never a
	// conflict.
	slots.slots[1] = stored
	slots.deleteErr = ErrRecordNotFound
	if err := service.Delete(ctx, adminActor(), 1, 2); !errors.Is(err, ErrPlanSlotNotFound) || IsWriteConflict(err) {
		t.Fatalf("Delete(vanished) error = %v", err)
	}
}

func TestPlanSlotServiceCopy(t *testing.T) {
	withDetails := slotOn(1, 12, 1, SeasonWeekA, 1, 1020, 1110)
	withDetails.MinAge, withDetails.MaxAge, withDetails.MaxTeams = intPtr(8), intPtr(10), intPtr(2)
	source := []PlanSlot{
		withDetails,
		slotOn(2, 12, 2, SeasonWeekB, 3, 600, 690),
		slotOn(3, 12, 2, SeasonWeekAll, 5, 960, 1080),
		slotOn(4, 12, 3, SeasonWeekAll, 6, 600, 660), // archived resource
		slotOn(5, 12, 2, SeasonWeekA, 3, 600, 690),   // same as 2 but week A
	}
	ctx := context.Background()

	t.Run("into an alternating part keeps weeks and details", func(t *testing.T) {
		slots := newPlanSlotStub(source...)
		service, auditor := newTestPlanSlotService(t, slots)
		result, err := service.Copy(ctx, adminActor(), 22, 12, false)
		if err != nil {
			t.Fatalf("Copy() error = %v", err)
		}
		if result != (PlanCopyResult{Copied: 4, SkippedArchived: 1}) {
			t.Fatalf("result = %+v", result)
		}
		copied, _ := service.Slots(ctx, adminActor().Access, 22)
		weeks := map[string]int{}
		for _, slot := range copied {
			weeks[slot.Week]++
			if slot.SeasonPartID != 22 || slot.GroupID != 5 || slot.ResourceID == 3 {
				t.Fatalf("copy = %+v", slot)
			}
		}
		if weeks[SeasonWeekA] != 2 || weeks[SeasonWeekB] != 1 || weeks[SeasonWeekAll] != 1 {
			t.Fatalf("weeks = %v", weeks)
		}
		first := slots.replaced[0]
		if first.Weekday != 1 || first.StartMinute != 1020 || first.EndMinute != 1110 || first.ResourceID != 1 || *first.MinAge != 8 || *first.MaxAge != 10 || *first.MaxTeams != 2 {
			t.Fatalf("first copy = %+v", first)
		}
		// The copy's values are its own, not shared with the source.
		*slots.replaced[0].MaxTeams = 5
		if *withDetails.MaxTeams != 2 {
			t.Fatal("copy shares the source's team limit")
		}
		if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionPlanCopied || !strings.Contains(auditor.records[0].Detail, "part 22 from part 12") {
			t.Fatalf("audit = %+v", auditor.records)
		}
		// Copying again needs confirmation, then redoes the copy.
		if _, err := service.Copy(ctx, adminActor(), 22, 12, false); !errors.Is(err, ErrPlanCopyNotConfirmed) {
			t.Fatalf("Copy(unconfirmed) error = %v", err)
		}
		again, err := service.Copy(ctx, adminActor(), 22, 12, true)
		if err != nil || again.Replaced != 4 || again.Copied != 4 {
			t.Fatalf("Copy(again) = %+v, %v", again, err)
		}
		if copied, _ := service.Slots(ctx, adminActor().Access, 22); len(copied) != 4 {
			t.Fatalf("after redo %d slots, want 4", len(copied))
		}
	})

	t.Run("into a part without alternating weeks maps to every week", func(t *testing.T) {
		slots := newPlanSlotStub(source...)
		service, _ := newTestPlanSlotService(t, slots)
		result, err := service.Copy(ctx, adminActor(), 21, 12, false)
		if err != nil {
			t.Fatalf("Copy() error = %v", err)
		}
		if result != (PlanCopyResult{Copied: 3, SkippedArchived: 1, Merged: 1}) {
			t.Fatalf("result = %+v", result)
		}
		for _, slot := range slots.replaced {
			if slot.Week != SeasonWeekAll {
				t.Fatalf("copy week = %q", slot.Week)
			}
		}
	})

	for _, test := range []struct {
		name           string
		actor          GroupActor
		target, source int64
		wantErr        error
	}{
		{"coach is forbidden", coachOf(1), 22, 12, ErrGroupForbidden},
		{"player is forbidden", playerOn(1), 22, 12, ErrGroupForbidden},
		{"outsider is not found", actorWith(GroupAccessGrants{}), 22, 12, ErrGroupNotFound},
		{"from itself", adminActor(), 22, 22, ErrInvalidPlanCopy},
		{"unknown source", adminActor(), 22, 99, ErrSeasonNotFound},
		{"unknown target", adminActor(), 99, 12, ErrSeasonNotFound},
		{"no source", adminActor(), 22, 0, ErrSeasonNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			slots := newPlanSlotStub(source...)
			service, auditor := newTestPlanSlotService(t, slots)
			if _, err := service.Copy(ctx, test.actor, test.target, test.source, true); !errors.Is(err, test.wantErr) {
				t.Fatalf("Copy() error = %v, want %v", err, test.wantErr)
			}
			if slots.replaced != nil || len(auditor.records) != 0 {
				t.Fatal("a refused copy changed or audited something")
			}
		})
	}
}

func TestPlanWarnings(t *testing.T) {
	plain := SeasonPart{ID: 21}
	alternating := SeasonPart{ID: 22, AlternatingWeeks: true}
	_, resources := planFixture()
	withTeams := func(slot PlanSlot, teams int) PlanSlot {
		slot.MaxTeams = intPtr(teams)
		return slot
	}
	tests := []struct {
		name  string
		part  SeasonPart
		slots []PlanSlot
		want  []PlanWarning
	}{
		{name: "within the limit", part: plain, slots: []PlanSlot{
			slotOn(1, 21, 1, SeasonWeekAll, 1, 600, 720), slotOn(2, 21, 1, SeasonWeekAll, 1, 660, 780),
		}},
		{name: "touching slots never overlap", part: plain, slots: []PlanSlot{
			withTeams(slotOn(1, 21, 1, SeasonWeekAll, 1, 600, 660), 2), withTeams(slotOn(2, 21, 1, SeasonWeekAll, 1, 660, 720), 2),
		}},
		{name: "one slot above the limit", part: plain, slots: []PlanSlot{withTeams(slotOn(1, 21, 1, SeasonWeekAll, 2, 600, 660), 3)},
			want: []PlanWarning{{ResourceID: 1, Week: SeasonWeekAll, Weekday: 2, StartMinute: 600, EndMinute: 660, Teams: 3, Limit: 2, SlotIDs: []int64{1}}}},
		{name: "three overlapping slots merge into one stretch", part: plain, slots: []PlanSlot{
			slotOn(1, 21, 1, SeasonWeekAll, 1, 600, 720), slotOn(2, 21, 1, SeasonWeekAll, 1, 630, 690), slotOn(3, 21, 1, SeasonWeekAll, 1, 660, 750),
			withTeams(slotOn(4, 21, 1, SeasonWeekAll, 1, 690, 720), 1),
		}, want: []PlanWarning{{ResourceID: 1, Week: SeasonWeekAll, Weekday: 1, StartMinute: 660, EndMinute: 720, Teams: 3, Limit: 2, SlotIDs: []int64{1, 2, 3, 4}}}},
		{name: "resources without a limit never warn", part: plain, slots: []PlanSlot{
			withTeams(slotOn(1, 21, 2, SeasonWeekAll, 1, 600, 720), 50), withTeams(slotOn(2, 21, 2, SeasonWeekAll, 1, 600, 720), 50),
		}},
		{name: "other days and resources do not add up", part: plain, slots: []PlanSlot{
			withTeams(slotOn(1, 21, 1, SeasonWeekAll, 1, 600, 720), 2), withTeams(slotOn(2, 21, 1, SeasonWeekAll, 2, 600, 720), 2), withTeams(slotOn(3, 21, 2, SeasonWeekAll, 1, 600, 720), 2),
		}},
		{name: "A and B weeks are checked apart", part: alternating, slots: []PlanSlot{
			withTeams(slotOn(1, 22, 1, SeasonWeekA, 1, 600, 720), 2), withTeams(slotOn(2, 22, 1, SeasonWeekB, 1, 600, 720), 2),
		}},
		{name: "every-week slots count in week B", part: alternating, slots: []PlanSlot{
			withTeams(slotOn(1, 22, 1, SeasonWeekAll, 1, 600, 720), 2), slotOn(2, 22, 1, SeasonWeekB, 1, 660, 690),
		}, want: []PlanWarning{{ResourceID: 1, Week: SeasonWeekB, Weekday: 1, StartMinute: 660, EndMinute: 690, Teams: 3, Limit: 2, SlotIDs: []int64{1, 2}}}},
		{name: "a warning of both weeks is reported once", part: alternating, slots: []PlanSlot{
			withTeams(slotOn(1, 22, 1, SeasonWeekAll, 1, 600, 720), 2), slotOn(2, 22, 1, SeasonWeekAll, 1, 660, 690),
		}, want: []PlanWarning{{ResourceID: 1, Week: SeasonWeekAll, Weekday: 1, StartMinute: 660, EndMinute: 690, Teams: 3, Limit: 2, SlotIDs: []int64{1, 2}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := PlanWarnings(test.part, test.slots, resources.resources)
			if len(got) != len(test.want) {
				t.Fatalf("PlanWarnings() = %+v, want %+v", got, test.want)
			}
			for index := range got {
				if !sameWarning(got[index], test.want[index]) || got[index].Week != test.want[index].Week || got[index].Limit != test.want[index].Limit {
					t.Fatalf("warning %d = %+v, want %+v", index, got[index], test.want[index])
				}
			}
		})
	}
}
