package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

type trainingPlanUseCaseStub struct {
	board     service.TrainingBoard
	boardErr  error
	saveErr   error
	changes   service.TrainingChanges
	requests  []service.TrainingBoardRequest
	saved     [][]service.PlanTraining
	revisions []string
}

// Board answers the fixture; a draft replaces its trainings, so the handler
// sees the draft it posted.
func (s *trainingPlanUseCaseStub) Board(_ context.Context, access *service.GroupAccess, request service.TrainingBoardRequest) (service.TrainingBoard, error) {
	if !access.CanViewPlan() {
		return service.TrainingBoard{}, service.ErrGroupForbidden
	}
	s.requests = append(s.requests, request)
	board := s.board
	board.CanEdit = access.CanManagePlan()
	if request.Draft != nil {
		board.Trainings = nil
		for index, item := range request.Draft.Items {
			board.Trainings = append(board.Trainings, service.BoardTraining{Index: index, Training: item.PlanTraining, Reason: item.Reason})
		}
		if request.Draft.Revision != "" {
			board.Revision = request.Draft.Revision
		}
	}
	if request.Suggest != service.TrainingSuggestNone {
		board.Suggested = 1
	}
	return board, s.boardErr
}

func (s *trainingPlanUseCaseStub) Save(_ context.Context, actor service.GroupActor, _ int64, revision string, trainings []service.PlanTraining) (service.TrainingChanges, error) {
	if !actor.Access.CanManagePlan() {
		return service.TrainingChanges{}, service.ErrGroupForbidden
	}
	s.saved, s.revisions = append(s.saved, trainings), append(s.revisions, revision)
	return s.changes, s.saveErr
}

func trainingPlanFixture() service.TrainingBoard {
	anna := "Anna"
	season := service.Season{ID: 2, GroupID: 3, Name: "2026/27", StartsOn: service.ParseSeasonDate("2026-08-01"), EndsOn: service.ParseSeasonDate("2027-06-30")}
	part := service.SeasonPart{ID: 12, GroupID: 3, SeasonID: 2, Position: 1, Name: "Whole", StartsOn: season.StartsOn, EndsOn: season.EndsOn}
	season.Parts = []service.SeasonPart{part}
	lions := service.Team{ID: 4, GroupID: 3, Name: "Lions", BirthYearFrom: 2016, BirthYearTo: 2017}
	tigers := service.Team{ID: 5, GroupID: 3, Name: "Tigers", BirthYearFrom: 2014, BirthYearTo: 2015}
	slot := service.PlanSlot{ID: 8, GroupID: 3, SeasonPartID: 12, ResourceID: 1, Week: service.SeasonWeekAll, Weekday: 2, StartMinute: 960, EndMinute: 1080}
	saved := service.PlanTraining{ID: 20, GroupID: 3, SeasonPartID: 12, TeamID: 4, ResourceID: 1, PlanSlotID: 8, Week: service.SeasonWeekAll, Weekday: 2, StartMinute: 960, EndMinute: 1050}
	return service.TrainingBoard{
		Seasons: []service.Season{season}, Found: true, Season: season, Part: part,
		PreviousSeason: service.Season{ID: 1, Name: "2025/26"}, HasPrevious: true,
		Revision: "rev1", SavedRevision: "rev1",
		Slots:     []service.PlanSlot{slot},
		Resources: []service.Resource{{ID: 1, GroupID: 3, Name: "Field"}},
		Teams: []service.TrainingBoardTeam{
			{Team: lions, Ages: service.AgeRange{Min: 9, Max: 10}, Coaches: []service.TrainingBoardCoach{{UserID: 7, DisplayName: &anna, Head: true}}, Trainings: 1},
			{Team: tigers, Ages: service.AgeRange{Min: 11, Max: 12}},
		},
		Trainings: []service.BoardTraining{{
			Index: 0, Training: saved, Slot: slot, HasSlot: true, Saved: true,
			Warnings: []service.TrainingWarning{{Kind: service.TrainingWarningCoachUnavailable, Index: 0, Other: -1, CoachUserID: 7}},
		}},
		Warnings: []service.TrainingWarning{{Kind: service.TrainingWarningCoachUnavailable, Index: 0, Other: -1, CoachUserID: 7}},
	}
}

func trainingPlanDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *trainingPlanUseCaseStub) {
	t.Helper()
	dependencies, _, _ := groupTestDependencies(t, access)
	board := &trainingPlanUseCaseStub{board: trainingPlanFixture()}
	dependencies.TrainingPlan = board
	return dependencies, board
}

// serveTrainingPlanSwap posts like the board does: HTMX, from the grid or a
// button (the response retargets the board).
func serveTrainingPlanSwap(t *testing.T, dependencies Dependencies, target, form string) *httptest.ResponseRecorder {
	t.Helper()
	handler, _ := newGroupsTestHandler(t, dependencies)
	mux := http.NewServeMux()
	registerGroupRoutes(mux, handler)
	request := authenticatedRequest(http.MethodPost, target, 7, form)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

const trainingDraftPath = "/en/groups/3/training-plan/draft?part=12"

func TestTrainingPlanPageByRole(t *testing.T) {
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
			dependencies, board := trainingPlanDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/training-plan?part=12", "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantStatus != http.StatusOK {
				if len(board.requests) != 0 || strings.Contains(body, "Lions") {
					t.Fatal("denied account reached the board")
				}
				return
			}
			if len(board.requests) != 1 || board.requests[0].PartID != 12 || board.requests[0].Draft != nil {
				t.Fatalf("board requests = %+v", board.requests)
			}
			for _, want := range []string{`id="training-plan-board"`, "Training plan", "Lions", "Anna", `data-section="training-plan"`, "Field", "16:00–17:30"} {
				if !strings.Contains(body, want) {
					t.Errorf("board missing %q", want)
				}
			}
			editing := []string{
				`name="revision" value="rev1"`, `name="t" value="20:4:1:all:2:960:1050:"`, `data-testid="training-plan-suggest"`,
				`formaction="/en/groups/3/training-plan/draft?part=12"`, `data-tp-pool`, `name="add_team"`, `data-move-url=`,
				"Anna is not available at this time.", "Suggestions start from: 2025/26", `aria-label="Remove the training of Lions"`,
			}
			for _, want := range editing {
				if strings.Contains(body, want) != test.wantEdit {
					t.Errorf("editing marker %q shown = %v, want %v", want, !test.wantEdit, test.wantEdit)
				}
			}
			if strings.Contains(body, `data-testid="training-plan-read-only"`) == test.wantEdit {
				t.Error("read-only notice must show exactly when editing is off")
			}
			// Slots are drawn behind the trainings.
			if !strings.Contains(body, "wg-item-bg") {
				t.Error("slot background missing from the grid")
			}
		})
	}
}

func TestTrainingPlanOutsidersAndBadPaths(t *testing.T) {
	dependencies, board := trainingPlanDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, target := range []string{"/en/groups/3/training-plan", "/en/groups/3/training-plan/draft"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if method == http.MethodGet && strings.HasSuffix(target, "/draft") {
				continue
			}
			if response := serveGroups(t, dependencies, method, target, "t=1:4:1:all:2:960:1050:", false); response.Code != http.StatusNotFound {
				t.Errorf("non-member %s %s status = %d, want 404", method, target, response.Code)
			}
		}
	}
	if len(board.requests) != 0 || len(board.saved) != 0 {
		t.Fatal("a non-member reached the board")
	}
	dependencies, board = trainingPlanDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	board.boardErr = service.ErrSeasonNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/training-plan?part=99", "", false); response.Code != http.StatusNotFound {
		t.Errorf("unknown part status = %d", response.Code)
	}
}

func TestTrainingPlanDraftOperations(t *testing.T) {
	state := "revision=rev1&t=20:4:1:all:2:960:1050:"
	dependencies, board := trainingPlanDependencies(t, groupMemberAs(service.GroupRoleManager))

	// Suggest: the answer retargets the board and carries the alert.
	response := serveTrainingPlanSwap(t, dependencies, trainingDraftPath, state+"&suggest=fill")
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("HX-Retarget") != "#training-plan-board" || response.Header().Get("HX-Reswap") != "outerHTML" {
		t.Fatalf("suggest status = %d, headers = %v", response.Code, response.Header())
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) || !strings.Contains(body, `id="training-plan-board"`) || strings.Contains(body, "<html") || !strings.Contains(body, "1 training suggested.") {
		t.Fatal("suggest answer is not the board fragment with alerts")
	}
	request := board.requests[0]
	if request.Suggest != service.TrainingSuggestFill || request.PartID != 12 || request.Draft == nil || request.Draft.Revision != "rev1" || len(request.Draft.Items) != 1 || request.Draft.Items[0].ID != 20 {
		t.Fatalf("request = %+v", request)
	}
	if len(board.saved) != 0 {
		t.Fatal("a draft was saved")
	}

	// Remove drops the posted training.
	board.requests = nil
	serveTrainingPlanSwap(t, dependencies, trainingDraftPath, state+"&t=0:5:1:all:3:960:1050:s&remove=0")
	last := board.requests[len(board.requests)-1]
	if len(last.Draft.Items) != 1 || last.Draft.Items[0].TeamID != 5 || last.Draft.Items[0].Reason != service.TrainingSuggestionSlot {
		t.Fatalf("draft after remove = %+v", last.Draft)
	}

	// A move from the grid changes the training named by its index.
	board.requests = nil
	response = serveTrainingPlanSwap(t, dependencies, trainingDraftPath, state+"&item=0&weekday=3&resource=1&start=1020&end=1110&week=")
	if response.Code != http.StatusOK {
		t.Fatalf("move status = %d: %s", response.Code, response.Body.String())
	}
	moved := board.requests[len(board.requests)-1].Draft.Items[0]
	if moved.ID != 20 || moved.Weekday != 3 || moved.StartMinute != 1020 || moved.EndMinute != 1110 || moved.TeamID != 4 {
		t.Fatalf("moved = %+v, want the same training on Wednesday 17:00-18:30", moved)
	}

	// A team dragged from the pool adds a training at the drop point.
	board.requests = nil
	response = serveTrainingPlanSwap(t, dependencies, trainingDraftPath, state+"&add_team=5&weekday=4&resource=1&start=600&end=660&week=")
	added := board.requests[len(board.requests)-1].Draft.Items
	if response.Code != http.StatusOK || len(added) != 2 || added[1].TeamID != 5 || added[1].ID != 0 || added[1].Weekday != 4 || added[1].Week != service.SeasonWeekAll {
		t.Fatalf("dragged add = %d %+v", response.Code, added)
	}

	// The add form carries add_* fields.
	board.requests = nil
	form := state + "&add=1&add_team=5&add_weekday=5&add_resource=1&add_start=540&add_end=600"
	if response = serveTrainingPlanSwap(t, dependencies, trainingDraftPath, form); response.Code != http.StatusOK {
		t.Fatalf("form add status = %d", response.Code)
	}
	if added = board.requests[len(board.requests)-1].Draft.Items; len(added) != 2 || added[1].Weekday != 5 || added[1].StartMinute != 540 {
		t.Fatalf("form add = %+v", added)
	}

	// Bad input answers 422 with the board and an error.
	for name, bad := range map[string]string{
		"malformed training":  "t=1:4:1:all:2:960",
		"bad week":            "t=1:4:1:C:2:960:1050:",
		"bad reason":          "t=1:4:1:all:2:960:1050:x",
		"remove out of range": state + "&remove=7",
		"move unknown item":   state + "&item=9&weekday=3&resource=1&start=600&end=660&week=",
		"move off the grid":   state + "&item=0&weekday=9&resource=1&start=600&end=660&week=",
		"move unknown column": state + "&item=0&weekday=3&resource=77&start=600&end=660&week=",
		"add without team":    state + "&add=1&add_weekday=3&add_resource=1&add_start=600&add_end=660",
	} {
		if response := serveTrainingPlanSwap(t, dependencies, trainingDraftPath, bad); response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", name, response.Code)
		}
	}

	// Coaches may not draft.
	coach, coachBoard := trainingPlanDependencies(t, groupMemberAs(service.GroupRoleCoach))
	if response := serveTrainingPlanSwap(t, coach, trainingDraftPath, "suggest=fill"); response.Code != http.StatusForbidden {
		t.Fatalf("coach draft status = %d", response.Code)
	}
	if len(coachBoard.requests) != 0 {
		t.Fatal("coach draft reached the board")
	}
}

func TestTrainingPlanSave(t *testing.T) {
	dependencies, board := trainingPlanDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	board.changes = service.TrainingChanges{Added: 1, Moved: 2, Removed: 3}
	form := "revision=rev1&t=20:4:1:all:2:960:1050:&t=0:5:1:all:3:960:1050:s"
	target := "/en/groups/3/training-plan?part=12"
	response := serveTrainingPlanSwap(t, dependencies, target, form)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Training plan saved: 1 added, 2 moved, 3 removed.") {
		t.Fatalf("save status = %d", response.Code)
	}
	if len(board.saved) != 1 || board.revisions[0] != "rev1" || len(board.saved[0]) != 2 || board.saved[0][0].ID != 20 || board.saved[0][1].TeamID != 5 {
		t.Fatalf("saved = %+v (%v)", board.saved, board.revisions)
	}
	if last := board.requests[len(board.requests)-1]; last.Draft != nil {
		t.Fatalf("post-save board request = %+v", last)
	}
	board.changes = service.TrainingChanges{}
	if response = serveTrainingPlanSwap(t, dependencies, target, form); !strings.Contains(response.Body.String(), "Nothing changed.") {
		t.Fatal("an empty save does not say nothing changed")
	}

	plain := serveGroups(t, dependencies, http.MethodPost, target, form, false)
	if plain.Code != http.StatusSeeOther || plain.Header().Get("Location") != "/en/groups/3/training-plan?part=12" {
		t.Fatalf("plain save = %d %q", plain.Code, plain.Header().Get("Location"))
	}
	if response = serveTrainingPlanSwap(t, dependencies, "/en/groups/3/training-plan", form); response.Code != http.StatusNotFound {
		t.Fatalf("save without a part status = %d, want 404", response.Code)
	}

	failures := []struct {
		err        error
		wantStatus int
		wantText   string
	}{
		{service.ErrOptimisticLockConflict, http.StatusUnprocessableEntity, "changed since you opened the board"},
		{service.ErrInvalidTrainingTime, http.StatusUnprocessableEntity, "needs a day"},
		{service.ErrInvalidTrainingWeek, http.StatusUnprocessableEntity, "does not exist in this part"},
		{service.ErrTrainingPlanTooLarge, http.StatusUnprocessableEntity, "too many trainings"},
		{service.ErrTeamNotFound, http.StatusUnprocessableEntity, "no longer exists"},
		{service.ErrResourceNotFound, http.StatusUnprocessableEntity, "no longer exists"},
		{service.ErrSeasonNotFound, http.StatusNotFound, ""},
	}
	for _, failure := range failures {
		board.saveErr = failure.err
		board.requests = nil
		response := serveTrainingPlanSwap(t, dependencies, target, "revision=old&t=20:4:1:all:2:960:1050:")
		if response.Code != failure.wantStatus || !strings.Contains(response.Body.String(), failure.wantText) {
			t.Errorf("%v: status = %d, want %d with %q", failure.err, response.Code, failure.wantStatus, failure.wantText)
		}
		// A refused save keeps the user's draft on the board.
		if failure.wantStatus == http.StatusUnprocessableEntity && (len(board.requests) == 0 || board.requests[0].Draft == nil || board.requests[0].Draft.Revision != "old") {
			t.Errorf("%v: draft not kept: %+v", failure.err, board.requests)
		}
	}
	board.saveErr = service.ErrOptimisticLockConflict
	if plain := serveGroups(t, dependencies, http.MethodPost, target, "revision=old", false); plain.Code != http.StatusConflict {
		t.Fatalf("plain conflict status = %d", plain.Code)
	}

	for _, role := range []string{service.GroupRoleCoach, service.GroupRolePlayer, service.GroupRoleParent} {
		denied, deniedBoard := trainingPlanDependencies(t, groupMemberAs(role))
		if response := serveTrainingPlanSwap(t, denied, target, form); response.Code != http.StatusForbidden || len(deniedBoard.saved) != 0 {
			t.Fatalf("%s save status = %d, saved %d", role, response.Code, len(deniedBoard.saved))
		}
	}
}
