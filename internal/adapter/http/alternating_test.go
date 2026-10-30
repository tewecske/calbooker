package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func (s *seasonsUseCaseStub) SetPartAlternating(_ context.Context, actor service.GroupActor, seasonID, partID, version int64, alternating bool) (service.Season, error) {
	if !actor.Access.CanManageSeasons() {
		return service.Season{}, service.ErrGroupForbidden
	}
	s.lastID, s.lastVersion, s.lastPart, s.lastAlternating = seasonID, version, partID, alternating
	return s.found, s.write()
}

func TestPlanPageShowsAlternatingSwitchToStaffOnly(t *testing.T) {
	tests := []struct {
		name   string
		access *service.GroupAccess
		want   bool
	}{
		{"manager", groupMemberAs(service.GroupRoleManager), true},
		{"coach", groupMemberAs(service.GroupRoleCoach), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _ := planDependencies(t, test.access)
			body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/plan?part=22", "", false).Body.String()
			if got := strings.Contains(body, `data-testid="plan-alternating-form"`); got != test.want {
				t.Fatalf("switch shown = %v, want %v", got, test.want)
			}
			if test.want && !strings.Contains(body, "/en/groups/lions/seasons/2/parts/22/alternating") {
				t.Error("switch does not post to the part route")
			}
		})
	}
}

func TestSetPartAlternatingFromPlanPage(t *testing.T) {
	dependencies, plan := planDependencies(t, groupMemberAs(service.GroupRoleManager))
	seasons := &seasonsUseCaseStub{found: plan.board.Season}
	dependencies.Seasons = seasons
	target := "/en/groups/lions/seasons/2/parts/22/alternating"

	saved := serveGroups(t, dependencies, http.MethodPost, target, "version=4&alternating=false&return=plan", true)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "A/B weeks saved.") {
		t.Fatalf("save = %d", saved.Code)
	}
	if seasons.lastID != 2 || seasons.lastPart != 22 || seasons.lastVersion != 4 || seasons.lastAlternating {
		t.Fatalf("service got season %d part %d version %d alternating %v", seasons.lastID, seasons.lastPart, seasons.lastVersion, seasons.lastAlternating)
	}
	if on := serveGroups(t, dependencies, http.MethodPost, target, "version=5&alternating=true", true); on.Code != http.StatusOK || !seasons.lastAlternating {
		t.Fatalf("switch on = %d (alternating %v)", on.Code, seasons.lastAlternating)
	}

	writes := seasons.writes
	for _, version := range []string{"", "x", "-1"} {
		if response := serveGroups(t, dependencies, http.MethodPost, target, "version="+version+"&alternating=true", false); response.Code != http.StatusConflict {
			t.Errorf("version %q = %d, want 409", version, response.Code)
		}
	}
	if seasons.writes != writes {
		t.Fatal("malformed versions reached the use case")
	}

	for _, test := range []struct {
		err    error
		status int
		text   string
	}{
		{service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
		{service.ErrSeasonNotFound, http.StatusNotFound, ""},
	} {
		seasons.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, target, "version=5&alternating=true", false)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
			t.Errorf("error %v = %d, want %d with %q", test.err, response.Code, test.status, test.text)
		}
	}
}

func TestSetPartAlternatingRefusesNonStaff(t *testing.T) {
	target := "/en/groups/lions/seasons/2/parts/22/alternating"
	for name, access := range map[string]*service.GroupAccess{
		"coach":  groupMemberAs(service.GroupRoleCoach),
		"player": groupMemberAs(service.GroupRolePlayer),
	} {
		t.Run(name, func(t *testing.T) {
			dependencies, plan := planDependencies(t, access)
			seasons := &seasonsUseCaseStub{found: plan.board.Season}
			dependencies.Seasons = seasons
			response := serveGroups(t, dependencies, http.MethodPost, target, "version=4&alternating=false&return=plan", false)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.Code)
			}
			if seasons.writes != 0 {
				t.Fatal("a non-staff change was written")
			}
		})
	}
}

func TestSetPartAlternatingOutsiderGetsNotFound(t *testing.T) {
	dependencies, _ := planDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	seasons := &seasonsUseCaseStub{}
	dependencies.Seasons = seasons
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/seasons/2/parts/22/alternating", "version=4&alternating=false", false)
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
		t.Fatalf("status = %d, want 404 without the group name", response.Code)
	}
	if seasons.calls != 0 {
		t.Fatal("outsider reached the season use case")
	}
}

func TestSetPartAlternatingFromTrainingPlan(t *testing.T) {
	dependencies, board := trainingPlanDependencies(t, groupMemberAs(service.GroupRoleManager))
	seasons := &seasonsUseCaseStub{found: board.board.Season}
	dependencies.Seasons = seasons
	page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/training-plan?part=12", "", false).Body.String()
	if !strings.Contains(page, `data-testid="training-plan-alternating-form"`) || !strings.Contains(page, "/en/groups/lions/seasons/2/parts/12/alternating") {
		t.Fatal("training plan board lacks the A/B switch")
	}
	response := serveTrainingPlanSwap(t, dependencies, "/en/groups/lions/seasons/2/parts/12/alternating", "version=3&alternating=true&return=training-plan")
	if response.Code != http.StatusOK || response.Header().Get("HX-Retarget") != "#training-plan-board" || !strings.Contains(response.Body.String(), "A/B weeks saved.") {
		t.Fatalf("save = %d retarget %q", response.Code, response.Header().Get("HX-Retarget"))
	}
	if seasons.lastPart != 12 || seasons.lastVersion != 3 || !seasons.lastAlternating {
		t.Fatalf("service got part %d version %d alternating %v", seasons.lastPart, seasons.lastVersion, seasons.lastAlternating)
	}

	coach, _ := trainingPlanDependencies(t, groupMemberAs(service.GroupRoleCoach))
	coachSeasons := &seasonsUseCaseStub{found: board.board.Season}
	coach.Seasons = coachSeasons
	if response := serveGroups(t, coach, http.MethodPost, "/en/groups/lions/seasons/2/parts/12/alternating", "version=3&alternating=true&return=training-plan", false); response.Code != http.StatusForbidden || coachSeasons.writes != 0 {
		t.Fatalf("coach change = %d (%d writes), want 403", response.Code, coachSeasons.writes)
	}
}

func TestSeasonFormShowsAlternatingReadOnlyForStoredParts(t *testing.T) {
	dependencies, _, _ := seasonDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/seasons/1", "", false).Body.String()
	if !strings.Contains(body, `-part2-alternating-readonly"`) || !strings.Contains(body, "/en/groups/lions/plan?part=12") {
		t.Fatal("stored part lacks the read-only A/B value with a plan link")
	}
	if strings.Contains(body, `-part2-alternating" class="checkbox"`) {
		t.Error("stored part still has the A/B checkbox")
	}
}
