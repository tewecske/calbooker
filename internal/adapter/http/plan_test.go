package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

type planUseCaseStub struct {
	board     service.PlanBoard
	boardErr  error
	slot      service.PlanSlot
	slotErr   error
	writeErr  error
	copyRes   service.PlanCopyResult
	boardPart []int64
	created   []service.PlanSlotInput
	createdIn []int64
	moved     []service.PlanSlotMove
	movedSlot [][2]int64
	updated   []service.PlanSlotInput
	deleted   []int64
	copies    [][2]int64
	replace   []bool
}

func (s *planUseCaseStub) Board(_ context.Context, access *service.GroupAccess, partID int64) (service.PlanBoard, error) {
	s.boardPart = append(s.boardPart, partID)
	if !access.CanViewPlan() {
		return service.PlanBoard{}, service.ErrGroupForbidden
	}
	return s.board, s.boardErr
}

func (s *planUseCaseStub) Slot(context.Context, *service.GroupAccess, int64) (service.PlanSlot, error) {
	return s.slot, s.slotErr
}

func (s *planUseCaseStub) Create(_ context.Context, actor service.GroupActor, partID int64, input service.PlanSlotInput) (service.PlanSlot, error) {
	if !actor.Access.CanManagePlan() {
		return service.PlanSlot{}, service.ErrGroupForbidden
	}
	s.created, s.createdIn = append(s.created, input), append(s.createdIn, partID)
	return service.PlanSlot{ID: 50}, s.writeErr
}

func (s *planUseCaseStub) Update(_ context.Context, actor service.GroupActor, _, _ int64, input service.PlanSlotInput) (service.PlanSlot, error) {
	if !actor.Access.CanManagePlan() {
		return service.PlanSlot{}, service.ErrGroupForbidden
	}
	s.updated = append(s.updated, input)
	if s.writeErr != nil {
		return service.PlanSlot{}, s.writeErr
	}
	slot := s.slot
	slot.Week = input.Week
	return slot, nil
}

func (s *planUseCaseStub) Move(_ context.Context, actor service.GroupActor, slotID, version int64, move service.PlanSlotMove) (service.PlanSlot, error) {
	if !actor.Access.CanManagePlan() {
		return service.PlanSlot{}, service.ErrGroupForbidden
	}
	s.moved, s.movedSlot = append(s.moved, move), append(s.movedSlot, [2]int64{slotID, version})
	return s.slot, s.writeErr
}

func (s *planUseCaseStub) Delete(_ context.Context, actor service.GroupActor, slotID, _ int64) error {
	if !actor.Access.CanManagePlan() {
		return service.ErrGroupForbidden
	}
	s.deleted = append(s.deleted, slotID)
	return s.writeErr
}

func (s *planUseCaseStub) Copy(_ context.Context, actor service.GroupActor, target, source int64, replace bool) (service.PlanCopyResult, error) {
	if !actor.Access.CanManagePlan() {
		return service.PlanCopyResult{}, service.ErrGroupForbidden
	}
	s.copies, s.replace = append(s.copies, [2]int64{target, source}), append(s.replace, replace)
	return s.copyRes, s.writeErr
}

func planBoardFixture() service.PlanBoard {
	limit := 1
	teams := 2
	archivedAt := int64(1)
	autumn := service.SeasonPart{ID: 21, GroupID: 3, SeasonID: 2, Position: 1, Name: "Autumn", StartsOn: service.ParseSeasonDate("2026-08-01"), EndsOn: service.ParseSeasonDate("2026-12-20")}
	spring := service.SeasonPart{ID: 22, GroupID: 3, SeasonID: 2, Position: 2, Name: "Spring", StartsOn: service.ParseSeasonDate("2027-01-04"), EndsOn: service.ParseSeasonDate("2027-06-30"), AlternatingWeeks: true}
	season := service.Season{ID: 2, GroupID: 3, Name: "2026/27", Parts: []service.SeasonPart{autumn, spring}}
	last := service.Season{ID: 1, GroupID: 3, Name: "2025/26", Parts: []service.SeasonPart{{ID: 12, Position: 2, Name: "Spring"}}}
	slotA := service.PlanSlot{ID: 7, GroupID: 3, SeasonPartID: 22, ResourceID: 4, Week: service.SeasonWeekA, Weekday: 1, StartMinute: 17 * 60, EndMinute: 18*60 + 30, MaxTeams: &teams, Version: 2}
	slotB := service.PlanSlot{ID: 8, GroupID: 3, SeasonPartID: 22, ResourceID: 4, Week: service.SeasonWeekB, Weekday: 3, StartMinute: 10 * 60, EndMinute: 11 * 60}
	slotAll := service.PlanSlot{ID: 9, GroupID: 3, SeasonPartID: 22, ResourceID: 5, Week: service.SeasonWeekAll, Weekday: 5, StartMinute: 16 * 60, EndMinute: 17 * 60}
	return service.PlanBoard{
		Seasons:   []service.Season{season, last},
		Found:     true,
		Season:    season,
		Part:      spring,
		Slots:     []service.PlanSlot{slotA, slotB, slotAll},
		Resources: []service.Resource{{ID: 4, GroupID: 3, Name: "Court 1", MaxConcurrentTeams: &limit}, {ID: 5, GroupID: 3, Name: "Old hall", ArchivedAt: &archivedAt}},
		Warnings: []service.PlanWarning{
			{ResourceID: 4, Week: service.SeasonWeekA, Weekday: 1, StartMinute: 17 * 60, EndMinute: 18*60 + 30, Teams: 2, Limit: 1, SlotIDs: []int64{7}},
		},
		CopySources: []service.PlanCopySource{{Kind: service.PlanCopyPreviousSeason, Season: last, Part: last.Parts[0], SlotCount: 4}},
	}
}

func planDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *planUseCaseStub) {
	t.Helper()
	dependencies, _, _ := groupTestDependencies(t, access)
	plan := &planUseCaseStub{board: planBoardFixture(), slot: planBoardFixture().Slots[0]}
	dependencies.Plan = plan
	return dependencies, plan
}

func TestPlanPageByRole(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantStatus int
		wantEdit   bool
	}{
		{name: "admin edits", access: groupMemberAs(service.GroupRoleAdmin), wantStatus: http.StatusOK, wantEdit: true},
		{name: "manager edits", access: groupMemberAs(service.GroupRoleManager), wantStatus: http.StatusOK, wantEdit: true},
		{name: "site administrator edits", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantStatus: http.StatusOK, wantEdit: true},
		{name: "coach reads", access: groupMemberAs(service.GroupRoleCoach), wantStatus: http.StatusOK},
		{name: "player is denied", access: groupMemberAs(service.GroupRolePlayer), wantStatus: http.StatusForbidden},
		{name: "parent is denied", access: groupMemberAs(service.GroupRoleParent), wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, plan := planDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/plan?part=22&week=A", "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantStatus != http.StatusOK {
				if len(plan.boardPart) != 0 || strings.Contains(body, "Court 1") {
					t.Fatal("denied account reached the plan")
				}
				return
			}
			for _, want := range []string{
				`id="plan-heading"`, `data-section="plan"`, `id="plan-grid"`, "Court 1", "Old hall (archived)",
				`data-wg-item="7-2"`, `data-wg-item="9-0"`, "Week A only", "Up to 2 teams", "Every week",
				`data-testid="plan-warnings"`, "slots allow 2 teams at once", `data-slot-warning="true"`,
				`href="/en/groups/3/plan?part=22&amp;week=B"`, `<option value="22" selected>2026/27: Spring</option>`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("plan page missing %q", want)
				}
			}
			// Week B's slot is not in week A.
			if strings.Contains(body, `data-wg-item="8-0"`) {
				t.Error("week A shows a week B slot")
			}
			if strings.Contains(body, "data-move-url") != test.wantEdit || strings.Contains(body, `id="plan-copy-source"`) != test.wantEdit {
				t.Errorf("editing controls shown = %v, want %v", !test.wantEdit, test.wantEdit)
			}
			if strings.Contains(body, `data-testid="plan-read-only"`) == test.wantEdit {
				t.Error("read-only notice must show exactly when editing is off")
			}
			// Archived resources' slots never move.
			if strings.Contains(body, `data-wg-item="9-0" data-weekday="5" data-start="960" data-end="1020" data-resource="5" data-wg-draggable`) {
				t.Error("a slot on an archived resource is draggable")
			}
			if len(plan.boardPart) != 1 || plan.boardPart[0] != 22 {
				t.Fatalf("Board parts = %v", plan.boardPart)
			}
		})
	}
}

func TestPlanPageWeekBAndDefaults(t *testing.T) {
	dependencies, plan := planDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/plan?part=22&week=B", "", false).Body.String()
	if !strings.Contains(body, `data-wg-item="8-0"`) || strings.Contains(body, `data-wg-item="7-2"`) || !strings.Contains(body, `data-week="B"`) {
		t.Fatal("week B view is wrong")
	}
	serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/plan?part=bad", "", false)
	if plan.boardPart[len(plan.boardPart)-1] != 0 {
		t.Fatalf("a malformed part must fall back to the default, got %v", plan.boardPart)
	}

	plan.board = service.PlanBoard{}
	empty := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/plan", "", false)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `data-testid="plan-no-seasons"`) {
		t.Fatalf("no-season plan: status %d", empty.Code)
	}

	plan.board, plan.boardErr = planBoardFixture(), service.ErrSeasonNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/plan?part=99", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("unknown part status = %d", response.Code)
	}
}

func TestPlanNavigationFollowsAccess(t *testing.T) {
	for _, test := range []struct {
		access *service.GroupAccess
		want   bool
	}{
		{groupMemberAs(service.GroupRoleAdmin), true},
		{groupMemberAs(service.GroupRoleCoach), true},
		{groupMemberAs(service.GroupRolePlayer), false},
		{groupMemberAs(service.GroupRoleParent), false},
	} {
		dependencies, _ := planDependencies(t, test.access)
		body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false).Body.String()
		if strings.Contains(body, `data-section="plan"`) != test.want {
			t.Errorf("roles %v: plan tab shown = %v, want %v", test.access.Roles(), !test.want, test.want)
		}
	}
}

func TestPlanRoutesOutsiderGetsNotFound(t *testing.T) {
	for _, test := range []struct{ method, target string }{
		{http.MethodGet, "/en/groups/3/plan"},
		{http.MethodPost, "/en/groups/3/plan/slots/create?part=22&week=A"},
		{http.MethodPost, "/en/groups/3/plan/slots/move?part=22&week=A"},
		{http.MethodPost, "/en/groups/3/plan/copy?part=22"},
		{http.MethodGet, "/en/groups/3/plan/slots/7"},
		{http.MethodPost, "/en/groups/3/plan/slots/7"},
		{http.MethodPost, "/en/groups/3/plan/slots/7/delete"},
	} {
		dependencies, plan := planDependencies(t, nil)
		dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
		response := serveGroups(t, dependencies, test.method, test.target, "version=1&weekday=1&start=600&end=660&resource=4&week=A&source_part_id=12", false)
		if response.Code != http.StatusNotFound || len(plan.boardPart)+len(plan.created)+len(plan.moved)+len(plan.copies)+len(plan.deleted) != 0 {
			t.Errorf("%s %s: status %d, plan touched", test.method, test.target, response.Code)
		}
	}
}

func TestPlanGridCreateAndMove(t *testing.T) {
	dependencies, plan := planDependencies(t, groupMemberAs(service.GroupRoleManager))
	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/create?part=22&week=B", "weekday=2&resource=4&start=1022&end=1080&week=B", true)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d", created.Code)
	}
	body := created.Body.String()
	if !strings.Contains(body, `id="plan-grid"`) || !strings.Contains(body, `id="plan-details" hx-swap-oob="true"`) || !strings.Contains(body, "Plan saved.") || strings.Contains(body, `id="page-content"`) {
		t.Fatal("create answer is not the grid swap")
	}
	if len(plan.created) != 1 || plan.createdIn[0] != 22 || plan.created[0] != (service.PlanSlotInput{ResourceID: 4, Week: service.SeasonWeekB, Weekday: 2, StartMinute: 1020, EndMinute: 1080}) {
		t.Fatalf("created = %+v in %v", plan.created, plan.createdIn)
	}

	moved := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/move?part=22&week=A", "item=7-2&weekday=4&resource=4&start=960&end=1050&week=A", false)
	if moved.Code != http.StatusSeeOther || moved.Header().Get("Location") != "/en/groups/3/plan?part=22&week=A" {
		t.Fatalf("plain move: status %d location %q", moved.Code, moved.Header().Get("Location"))
	}
	if len(plan.moved) != 1 || plan.movedSlot[0] != [2]int64{7, 2} || plan.moved[0] != (service.PlanSlotMove{ResourceID: 4, Weekday: 4, StartMinute: 960, EndMinute: 1050}) {
		t.Fatalf("moved = %+v %v", plan.moved, plan.movedSlot)
	}

	for name, form := range map[string]string{
		"wrong week":          "item=7-2&weekday=4&resource=4&start=960&end=1050&week=B",
		"resource not shown":  "item=7-2&weekday=4&resource=99&start=960&end=1050&week=A",
		"malformed item":      "item=7&weekday=4&resource=4&start=960&end=1050&week=A",
		"end before start":    "item=7-2&weekday=4&resource=4&start=960&end=900&week=A",
		"weekday out of grid": "item=7-2&weekday=9&resource=4&start=960&end=1050&week=A",
	} {
		before := len(plan.moved)
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/move?part=22&week=A", form, true)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "does not fit the plan") || len(plan.moved) != before {
			t.Errorf("%s: status %d, moved %d", name, response.Code, len(plan.moved)-before)
		}
	}

	for _, test := range []struct {
		err     error
		message string
	}{
		{service.ErrOptimisticLockConflict, "changed"},
		{service.ErrPlanSlotNotFound, "no longer exists"},
		{service.ErrResourceArchived, "archived"},
	} {
		plan.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/move?part=22&week=A", "item=7-2&weekday=4&resource=4&start=960&end=1050&week=A", true)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.message) || !strings.Contains(response.Body.String(), `id="plan-grid"`) {
			t.Errorf("%v: status %d", test.err, response.Code)
		}
	}
	plan.writeErr = errors.New("boom")
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/move?part=22&week=A", "item=7-2&weekday=4&resource=4&start=960&end=1050&week=A", true); response.Code != http.StatusInternalServerError {
		t.Errorf("unexpected error status = %d", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/create", "weekday=2&resource=4&start=1020&end=1080", true); response.Code != http.StatusNotFound {
		t.Errorf("create without part status = %d", response.Code)
	}
}

func TestPlanGridChangesForbiddenForCoach(t *testing.T) {
	dependencies, plan := planDependencies(t, groupMemberAs(service.GroupRoleCoach))
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/create?part=22&week=A", "weekday=2&resource=4&start=1020&end=1080&week=A", false); response.Code != http.StatusForbidden {
		t.Fatalf("coach create status = %d", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/move?part=22&week=A", "item=7-2&weekday=4&resource=4&start=960&end=1050&week=A", true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("coach HTMX move status = %d", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/copy?part=22", "source_part_id=12", false); response.Code != http.StatusForbidden {
		t.Fatalf("coach copy status = %d", response.Code)
	}
	if len(plan.created)+len(plan.moved)+len(plan.copies) != 0 {
		t.Fatal("coach changed the plan")
	}
	player, playerPlan := planDependencies(t, groupMemberAs(service.GroupRolePlayer))
	if response := serveGroups(t, player, http.MethodPost, "/en/groups/3/plan/slots/create?part=22&week=A", "weekday=2&resource=4&start=1020&end=1080&week=A", false); response.Code != http.StatusForbidden || len(playerPlan.boardPart) != 0 {
		t.Fatalf("player create status = %d", response.Code)
	}
}

func TestPlanCopy(t *testing.T) {
	dependencies, plan := planDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	plan.copyRes = service.PlanCopyResult{Copied: 3, SkippedArchived: 1, Merged: 2}
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/copy?part=22&week=A", "source_part_id=12&replace=true", true)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Copied 3 slots.") || !strings.Contains(body, "1 slot on an archived resource was not copied.") || !strings.Contains(body, "2 duplicate slots were copied once") {
		t.Fatalf("copy: status %d", response.Code)
	}
	if len(plan.copies) != 1 || plan.copies[0] != [2]int64{22, 12} || !plan.replace[0] {
		t.Fatalf("copies = %v replace %v", plan.copies, plan.replace)
	}
	if !strings.Contains(body, `hx-confirm="Replace the 3 slots of this part"`) || !strings.Contains(body, `id="plan-copy-replace"`) {
		t.Error("a part with slots must ask before replacing them")
	}

	plan.writeErr = service.ErrPlanCopyNotConfirmed
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/copy?part=22", "source_part_id=12", true); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Confirm that the copy replaces") {
		t.Fatalf("unconfirmed copy status = %d", response.Code)
	}
	if plan.replace[len(plan.replace)-1] {
		t.Fatal("a missing confirmation was read as replace")
	}
	plan.writeErr = service.ErrSeasonNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/copy?part=22", "source_part_id=x", false); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "Choose another season part") {
		t.Fatalf("malformed source status = %d", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/copy?part=22", "source_part_id=99", false); response.Code != http.StatusNotFound {
		t.Fatalf("unknown source status = %d", response.Code)
	}
}

func TestPlanSlotPage(t *testing.T) {
	admin, _ := planDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	response := serveGroups(t, admin, http.MethodGet, "/en/groups/3/plan/slots/7", "", false)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `id="plan-slot-heading"`) || !strings.Contains(body, `id="plan-slot-week"`) ||
		!strings.Contains(body, `value="17:00"`) || !strings.Contains(body, `value="18:30"`) || !strings.Contains(body, `<option value="A" selected>Week A only</option>`) ||
		!strings.Contains(body, `href="/en/groups/3/plan?part=22&amp;week=A"`) || !strings.Contains(body, `data-section="plan"`) {
		t.Fatalf("admin slot page: status %d", response.Code)
	}
	// Archived resources are not offered, except the slot's own.
	if strings.Contains(body, `>Old hall (archived)</option>`) {
		t.Error("the form offers an archived resource")
	}
	coach, _ := planDependencies(t, groupMemberAs(service.GroupRoleCoach))
	coachBody := serveGroups(t, coach, http.MethodGet, "/en/groups/3/plan/slots/7", "", false).Body.String()
	if !strings.Contains(coachBody, `data-testid="plan-slot-details"`) || strings.Contains(coachBody, `id="plan-slot-start"`) {
		t.Error("coach must see the slot read only")
	}
	missing, plan := planDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	plan.slotErr = service.ErrPlanSlotNotFound
	if response := serveGroups(t, missing, http.MethodGet, "/en/groups/3/plan/slots/9", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("missing slot status = %d", response.Code)
	}
	if response := serveGroups(t, missing, http.MethodGet, "/en/groups/3/plan/slots/x", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("malformed slot status = %d", response.Code)
	}
	player, _ := planDependencies(t, groupMemberAs(service.GroupRoleParent))
	if response := serveGroups(t, player, http.MethodGet, "/en/groups/3/plan/slots/7", "", false); response.Code != http.StatusForbidden {
		t.Fatalf("parent slot page status = %d", response.Code)
	}
}

func TestPlanSlotUpdateAndDelete(t *testing.T) {
	dependencies, plan := planDependencies(t, groupMemberAs(service.GroupRoleManager))
	form := "version=2&resource_id=4&week=all&weekday=2&start=09:30&end=00:00&min_age=8&max_age=&max_teams=3"
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/7", form, true)
	if response.Code != http.StatusOK || response.Header().Get("HX-Redirect") != "/en/groups/3/plan?part=22" {
		t.Fatalf("update: status %d redirect %q", response.Code, response.Header().Get("HX-Redirect"))
	}
	if plan.updated[0] != (service.PlanSlotInput{ResourceID: 4, Week: "all", Weekday: 2, StartMinute: 570, EndMinute: 1440, MinAge: 8, MaxTeams: 3}) {
		t.Fatalf("updated = %+v", plan.updated[0])
	}

	plan.writeErr = service.ErrInvalidPlanSlotAges
	invalid := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/7", "version=2&resource_id=4&week=A&weekday=2&start=9:30&end=10:00&min_age=x&max_teams=", true)
	body := invalid.Body.String()
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(body, `id="plan-slot-ages-error"`) || !strings.Contains(body, `value="x"`) {
		t.Fatalf("invalid update: status %d", invalid.Code)
	}
	if plan.updated[1].MinAge != -1 || plan.updated[1].StartMinute != 570 {
		t.Fatalf("malformed values must reach validation as invalid: %+v", plan.updated[1])
	}
	plan.writeErr = service.ErrOptimisticLockConflict
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/7", form, false); response.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d", response.Code)
	}
	plan.writeErr = service.ErrPlanSlotNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/7", form, false); response.Code != http.StatusNotFound {
		t.Fatalf("vanished update status = %d", response.Code)
	}
	before := len(plan.updated)
	plan.writeErr = nil
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/7", "resource_id=4&weekday=1&start=10:00&end=11:00", false); response.Code != http.StatusConflict || len(plan.updated) != before {
		t.Fatalf("update without revision status = %d", response.Code)
	}

	deleted := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/plan/slots/7/delete", "version=2", false)
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/en/groups/3/plan?part=22&week=A" || len(plan.deleted) != 1 {
		t.Fatalf("delete: status %d location %q", deleted.Code, deleted.Header().Get("Location"))
	}

	coach, coachPlan := planDependencies(t, groupMemberAs(service.GroupRoleCoach))
	if response := serveGroups(t, coach, http.MethodPost, "/en/groups/3/plan/slots/7", form, false); response.Code != http.StatusForbidden || len(coachPlan.updated) != 0 {
		t.Fatalf("coach update status = %d", response.Code)
	}
	if response := serveGroups(t, coach, http.MethodPost, "/en/groups/3/plan/slots/7/delete", "version=2", false); response.Code != http.StatusForbidden || len(coachPlan.deleted) != 0 {
		t.Fatalf("coach delete status = %d", response.Code)
	}
}

func TestParsePlanTime(t *testing.T) {
	for raw, want := range map[string]int{"00:00": 0, "9:05": 545, "17:30": 1050, "24:00": 1440, "24:01": -1, "7": -1, "ab:cd": -1, "": -1, "10:5": -1, "-1:00": -1} {
		if got := parsePlanTime(raw); got != want {
			t.Errorf("parsePlanTime(%q) = %d, want %d", raw, got, want)
		}
	}
}
