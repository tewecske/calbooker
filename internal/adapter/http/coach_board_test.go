package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

type coachBoardUseCaseStub struct {
	board     service.CoachBoard
	boardErr  error
	saveErr   error
	changes   service.CoachAssignmentChanges
	requests  []service.CoachBoardRequest
	saved     [][]service.CoachAssignment
	revisions []string
}

func (s *coachBoardUseCaseStub) Board(_ context.Context, access *service.GroupAccess, request service.CoachBoardRequest) (service.CoachBoard, error) {
	if !access.CanViewSeasons() {
		return service.CoachBoard{}, service.ErrGroupForbidden
	}
	s.requests = append(s.requests, request)
	board := s.board
	board.CanEdit = access.CanManageSeasons()
	if request.Draft != nil && request.Draft.Revision != "" {
		board.Revision = request.Draft.Revision
	}
	if request.Suggest != service.CoachSuggestNone {
		board.Suggested = 2
	}
	return board, s.boardErr
}

func (s *coachBoardUseCaseStub) Save(_ context.Context, actor service.GroupActor, _ int64, revision string, assignments []service.CoachAssignment) (service.CoachAssignmentChanges, error) {
	if !actor.Access.CanManageSeasons() {
		return service.CoachAssignmentChanges{}, service.ErrGroupForbidden
	}
	s.saved, s.revisions = append(s.saved, assignments), append(s.revisions, revision)
	return s.changes, s.saveErr
}

func coachBoardFixture() service.CoachBoard {
	anna, bela := "Anna", "Bela"
	season := service.Season{ID: 2, GroupID: 3, Name: "2026/27", StartsOn: service.ParseSeasonDate("2026-08-01"), EndsOn: service.ParseSeasonDate("2027-06-30")}
	lions := service.Team{ID: 4, GroupID: 3, Name: "Lions", BirthYearFrom: 2016, BirthYearTo: 2017}
	tigers := service.Team{ID: 5, GroupID: 3, Name: "Tigers", BirthYearFrom: 2014, BirthYearTo: 2015}
	mismatch := service.CoachBoardWarning{Kind: service.CoachWarningQualificationMismatch, TeamID: 5, CoachUserID: 7, TeamAges: service.AgeRange{Min: 11, Max: 12}, Qualification: service.AgeRange{Min: 6, Max: 10}}
	return service.CoachBoard{
		Season: season, PreviousSeason: service.Season{ID: 1, Name: "2025/26"}, HasPrevious: true,
		Revision: "rev1", SavedRevision: "rev1",
		Teams: []service.CoachBoardTeam{
			{Team: lions, Ages: service.AgeRange{Min: 9, Max: 10}, Assignments: []service.CoachBoardAssignment{
				{CoachUserID: 8, DisplayName: &bela, Head: true, Saved: true, Suggestion: &service.CoachSuggestion{Reason: service.CoachSuggestionPreviousSeason}},
			}},
			{Team: tigers, Ages: service.AgeRange{Min: 11, Max: 12}, Assignments: []service.CoachBoardAssignment{
				{CoachUserID: 7, DisplayName: &anna, Suggestion: &service.CoachSuggestion{Reason: service.CoachSuggestionQualification, Qualification: service.AgeRange{Min: 6, Max: 10}, Load: 1}, Warnings: []service.CoachBoardWarning{mismatch}},
			}},
		},
		Coaches: []service.CoachBoardCoach{
			{CoachCandidate: service.CoachCandidate{UserID: 7, DisplayName: &anna, Qualification: &service.CoachQualification{MinAge: 6, MaxAge: 10}, HasAvailability: true}, Teams: 1},
			{CoachCandidate: service.CoachCandidate{UserID: 8, DisplayName: &bela}, Teams: 1},
		},
		Warnings: []service.CoachBoardWarning{mismatch},
	}
}

func coachBoardDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *coachBoardUseCaseStub) {
	t.Helper()
	dependencies, _, _ := groupTestDependencies(t, access)
	board := &coachBoardUseCaseStub{board: coachBoardFixture()}
	dependencies.CoachBoard = board
	return dependencies, board
}

// serveCoachBoardSwap posts like the board does: HTMX targeting #coach-board.
func serveCoachBoardSwap(t *testing.T, dependencies Dependencies, target, form string) *httptest.ResponseRecorder {
	t.Helper()
	handler, _ := newGroupsTestHandler(t, dependencies)
	mux := http.NewServeMux()
	registerGroupRoutes(mux, handler)
	request := authenticatedRequest(http.MethodPost, target, 7, form)
	request.Header.Set("HX-Request", "true")
	request.Header.Set("HX-Target", "coach-board")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestCoachBoardPageByRole(t *testing.T) {
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
			dependencies, board := coachBoardDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons/2/coach-board", "", false)
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
			if len(board.requests) != 1 || board.requests[0].SeasonID != 2 || board.requests[0].Draft != nil {
				t.Fatalf("board requests = %+v", board.requests)
			}
			for _, want := range []string{`id="coach-board"`, "Coach board: 2026/27", "Lions", "Bela", `href="/en/groups/3/seasons/2"`} {
				if !strings.Contains(body, want) {
					t.Errorf("board missing %q", want)
				}
			}
			editing := []string{
				`data-coach-board`, `name="revision" value="rev1"`, `name="a" value="4:8"`, `name="head" value="4:8" checked`,
				`data-testid="coach-board-suggest"`, `formaction="/en/groups/3/seasons/2/coach-board/draft"`,
				"Coached this team last season", "Qualified for ages 6–10, 1 team so far", `name="suggested" value="5:7:q:1:6:10"`,
				"Qualified for ages 6–10, the team is 11–12", "Tigers: Anna – Qualified for ages 6–10", `data-cb-pool`,
				"No qualification", "No availability", `aria-label="Remove Anna from Tigers"`, `<option value="5:7">Anna</option>`,
				"Previous season: 2025/26", "Ages 9–10",
			}
			for _, want := range editing {
				if strings.Contains(body, want) != test.wantEdit {
					t.Errorf("editing marker %q shown = %v, want %v", want, !test.wantEdit, test.wantEdit)
				}
			}
			if strings.Contains(body, `data-testid="coach-board-read-only"`) == test.wantEdit {
				t.Error("read-only notice must show exactly when editing is off")
			}
		})
	}
}

func TestCoachBoardOutsidersAndBadPaths(t *testing.T) {
	dependencies, board := coachBoardDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if response := serveGroups(t, dependencies, method, "/en/groups/3/seasons/2/coach-board", "a=4:7", false); response.Code != http.StatusNotFound {
			t.Errorf("non-member %s status = %d, want 404", method, response.Code)
		}
	}
	if len(board.requests) != 0 || len(board.saved) != 0 {
		t.Fatal("a non-member reached the board")
	}

	dependencies, board = coachBoardDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons/x/coach-board", "", false); response.Code != http.StatusNotFound {
		t.Errorf("bad season id status = %d", response.Code)
	}
	board.boardErr = service.ErrSeasonNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons/9/coach-board", "", false); response.Code != http.StatusNotFound {
		t.Errorf("unknown season status = %d", response.Code)
	}
}

func TestCoachBoardDraftAndSuggest(t *testing.T) {
	dependencies, board := coachBoardDependencies(t, groupMemberAs(service.GroupRoleManager))
	form := url.Values{
		"revision":  {"rev1"},
		"a":         {"4:8", "5:7", "5:8"},
		"head":      {"4:8"},
		"add":       {"", "6:7"},
		"remove":    {"5:8"},
		"suggested": {"4:8:p:0", "5:7:q:1:6:10"},
		"suggest":   {"fill"},
	}
	response := serveCoachBoardSwap(t, dependencies, "/en/groups/3/seasons/2/coach-board/draft", form.Encode())
	if response.Code != http.StatusOK {
		t.Fatalf("draft status = %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, `hx-swap-oob="true"`) || !strings.Contains(body, `id="coach-board"`) || strings.Contains(body, "<html") || !strings.Contains(body, "2 suggestions added") {
		t.Fatal("draft answer is not the board fragment with alerts")
	}
	if len(board.saved) != 0 {
		t.Fatal("a draft was saved")
	}
	request := board.requests[0]
	if request.Suggest != service.CoachSuggestFill || request.Draft == nil || request.Draft.Revision != "rev1" {
		t.Fatalf("request = %+v", request)
	}
	want := []service.CoachAssignment{{TeamID: 4, CoachUserID: 8, Head: true}, {TeamID: 5, CoachUserID: 7}, {TeamID: 6, CoachUserID: 7}}
	if len(request.Draft.Assignments) != len(want) {
		t.Fatalf("draft = %+v, want %+v", request.Draft.Assignments, want)
	}
	for index := range want {
		if request.Draft.Assignments[index] != want[index] {
			t.Fatalf("draft = %+v, want %+v", request.Draft.Assignments, want)
		}
	}
	if len(request.Draft.Suggestions) != 2 || request.Draft.Suggestions[1].Suggestion.Qualification != (service.AgeRange{Min: 6, Max: 10}) || request.Draft.Suggestions[0].Suggestion.Reason != service.CoachSuggestionPreviousSeason {
		t.Fatalf("suggestions = %+v", request.Draft.Suggestions)
	}

	// Malformed values answer 422 with the saved board.
	for _, bad := range []string{"a=4", "a=x:7", "head=4:-1", "suggested=4:7:z:1", "suggested=4:7:q:1:6", "remove=4"} {
		if response := serveCoachBoardSwap(t, dependencies, "/en/groups/3/seasons/2/coach-board/draft", bad); response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s status = %d, want 422", bad, response.Code)
		}
	}

	// Coaches may not draft.
	coach, coachBoard := coachBoardDependencies(t, groupMemberAs(service.GroupRoleCoach))
	if response := serveCoachBoardSwap(t, coach, "/en/groups/3/seasons/2/coach-board/draft", "suggest=fill"); response.Code != http.StatusForbidden {
		t.Fatalf("coach draft status = %d", response.Code)
	}
	if len(coachBoard.requests) != 0 {
		t.Fatal("coach draft reached the board")
	}
}

func TestCoachBoardSave(t *testing.T) {
	dependencies, board := coachBoardDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	board.changes = service.CoachAssignmentChanges{Added: 1, Removed: 2, HeadChanged: 1}
	form := "revision=rev1&a=4:8&a=5:7&head=5:7"
	response := serveCoachBoardSwap(t, dependencies, "/en/groups/3/seasons/2/coach-board", form)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Coach assignments saved: 1 added, 2 removed, 1 head coach changes.") {
		t.Fatalf("save status = %d", response.Code)
	}
	if len(board.saved) != 1 || board.revisions[0] != "rev1" || len(board.saved[0]) != 2 || !board.saved[0][1].Head || board.saved[0][0].Head {
		t.Fatalf("saved = %+v (%v)", board.saved, board.revisions)
	}
	// After a save the board shows the saved state again.
	if last := board.requests[len(board.requests)-1]; last.Draft != nil {
		t.Fatalf("post-save board request = %+v", last)
	}

	// Without HTMX a save redirects back to the board.
	plain := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons/2/coach-board", form, false)
	if plain.Code != http.StatusSeeOther || plain.Header().Get("Location") != "/en/groups/3/seasons/2/coach-board" {
		t.Fatalf("plain save = %d %q", plain.Code, plain.Header().Get("Location"))
	}

	failures := []struct {
		err        error
		wantStatus int
		wantText   string
	}{
		{service.ErrOptimisticLockConflict, http.StatusUnprocessableEntity, "changed since you opened the board"},
		{service.ErrCoachBoardTwoHeads, http.StatusUnprocessableEntity, "only one head coach"},
		{service.ErrSeasonCoachRoleMissing, http.StatusUnprocessableEntity, "no longer has the coach role"},
		{service.ErrTeamNotFound, http.StatusUnprocessableEntity, "no longer exists"},
		{service.ErrSeasonNotFound, http.StatusNotFound, ""},
		{service.ErrRecordNotFound, http.StatusNotFound, ""},
	}
	for _, failure := range failures {
		board.saveErr = failure.err
		board.requests = nil
		response := serveCoachBoardSwap(t, dependencies, "/en/groups/3/seasons/2/coach-board", "revision=old&a=4:8")
		if response.Code != failure.wantStatus || !strings.Contains(response.Body.String(), failure.wantText) {
			t.Errorf("%v: status = %d, want %d with %q", failure.err, response.Code, failure.wantStatus, failure.wantText)
		}
		// A refused save keeps the user's draft on the board.
		if failure.wantStatus == http.StatusUnprocessableEntity && (len(board.requests) == 0 || board.requests[0].Draft == nil || board.requests[0].Draft.Revision != "old") {
			t.Errorf("%v: draft not kept: %+v", failure.err, board.requests)
		}
	}
	board.saveErr = service.ErrOptimisticLockConflict
	if plain := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/seasons/2/coach-board", "revision=old", false); plain.Code != http.StatusConflict {
		t.Fatalf("plain conflict status = %d", plain.Code)
	}

	coach, coachBoard := coachBoardDependencies(t, groupMemberAs(service.GroupRoleCoach))
	if response := serveCoachBoardSwap(t, coach, "/en/groups/3/seasons/2/coach-board", form); response.Code != http.StatusForbidden || len(coachBoard.saved) != 0 {
		t.Fatalf("coach save status = %d, saved %d", response.Code, len(coachBoard.saved))
	}
	player, _ := coachBoardDependencies(t, groupMemberAs(service.GroupRolePlayer))
	if response := serveCoachBoardSwap(t, player, "/en/groups/3/seasons/2/coach-board", form); response.Code != http.StatusForbidden {
		t.Fatalf("player save status = %d", response.Code)
	}
}

func TestSeasonPageLinksCoachBoard(t *testing.T) {
	dependencies, _, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleCoach))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/seasons/1", "", false).Body.String()
	if !strings.Contains(body, `href="/en/groups/3/seasons/1/coach-board"`) {
		t.Fatal("season page does not link the coach board")
	}
}
