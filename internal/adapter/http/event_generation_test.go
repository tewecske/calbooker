package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

type eventGenerationStub struct {
	options     service.GenerationOptions
	preview     service.GenerationPreview
	err         error
	previews    []service.GenerationRequest
	generated   []service.GenerationRequest
	optionParts []int64
}

func (s *eventGenerationStub) Options(_ context.Context, access *service.GroupAccess, partID int64) (service.GenerationOptions, error) {
	if !access.CanManagePlan() {
		return service.GenerationOptions{}, service.ErrGroupForbidden
	}
	s.optionParts = append(s.optionParts, partID)
	return s.options, nil
}

func (s *eventGenerationStub) Preview(_ context.Context, access *service.GroupAccess, request service.GenerationRequest) (service.GenerationPreview, error) {
	if !access.CanManagePlan() {
		return service.GenerationPreview{}, service.ErrGroupForbidden
	}
	s.previews = append(s.previews, request)
	return s.preview, s.err
}

func (s *eventGenerationStub) Generate(_ context.Context, actor service.GroupActor, request service.GenerationRequest) (service.GenerationPreview, error) {
	if !actor.Access.CanManagePlan() {
		return service.GenerationPreview{}, service.ErrGroupForbidden
	}
	s.generated = append(s.generated, request)
	return s.preview, s.err
}

func eventGenerationFixture() (service.GenerationOptions, service.GenerationPreview) {
	board := trainingPlanFixture()
	week := service.ParseSeasonDate("2026-10-26")
	options := service.GenerationOptions{
		Seasons: board.Seasons, Found: true, Season: board.Season, Part: board.Part,
		Weeks: service.GenerationWeeks(board.Part), DefaultWeek: week,
	}
	starts := time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
	preview := service.GenerationPreview{
		Season: board.Season, Part: board.Part, TimeZone: "Europe/Budapest", Weeks: []time.Time{week},
		Items: []service.GenerationItem{
			{Action: service.GenerationCreate, Week: week, TeamName: "Lions", ResourceName: "Field", StartsAt: starts, EndsAt: starts.Add(90 * time.Minute)},
			{Action: service.GenerationKeep, Reason: service.GenerationReasonEdited, Week: week, EventID: 77, TeamName: "Tigers", ResourceName: "Field", StartsAt: starts.Add(24 * time.Hour), EndsAt: starts.Add(26 * time.Hour)},
		},
		Counts: service.GenerationCounts{Create: 1, Keep: 1},
	}
	return options, preview
}

func eventGenerationDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *eventGenerationStub) {
	t.Helper()
	dependencies, _, _ := groupTestDependencies(t, access)
	options, preview := eventGenerationFixture()
	stub := &eventGenerationStub{options: options, preview: preview}
	dependencies.EventGeneration = stub
	return dependencies, stub
}

const eventGenerationPath = "/en/groups/lions/event-generation"

func TestEventGenerationPageByRole(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantStatus int
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantStatus: http.StatusOK},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), wantStatus: http.StatusOK},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantStatus: http.StatusOK},
		{name: "coach", access: groupMemberAs(service.GroupRoleCoach), wantStatus: http.StatusForbidden},
		{name: "player", access: groupMemberAs(service.GroupRolePlayer), wantStatus: http.StatusForbidden},
		{name: "parent", access: groupMemberAs(service.GroupRoleParent), wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, stub := eventGenerationDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, eventGenerationPath+"?part=12&week=2026-10-26&weeks=2", "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantStatus != http.StatusOK {
				if len(stub.previews) != 0 || strings.Contains(body, "Lions") {
					t.Fatal("denied account reached the preview")
				}
				return
			}
			if len(stub.previews) != 1 || stub.previews[0].PartID != 12 || stub.previews[0].Weeks != 2 || !stub.previews[0].Week.Equal(service.ParseSeasonDate("2026-10-26")) {
				t.Fatalf("preview requests = %+v", stub.previews)
			}
			for _, want := range []string{
				"Generate events from the plan", `data-testid="event-generation-result"`, `data-action="create"`, "Will be created", "Lions",
				"Tuesday 2026-10-27 16:00–17:30", "Europe/Budapest", `data-testid="event-generation-kept-item"`, `href="/en/groups/lions/events/77"`,
				"edited or cancelled by hand", `action="/en/groups/lions/event-generation"`, `name="week" value="2026-10-26"`, `name="weeks" value="2"`,
				`name="part" value="12"`, `data-section="training-plan"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("page missing %q", want)
				}
			}
			if strings.Contains(body, "Will be created: ") && !strings.Contains(body, `data-testid="event-generation-count-create">1<`) {
				t.Error("summary count missing")
			}
		})
	}
}

func TestEventGenerationPageWithoutWeekOnlyOffersTheChoice(t *testing.T) {
	dependencies, stub := eventGenerationDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	response := serveGroups(t, dependencies, http.MethodGet, eventGenerationPath, "", false)
	body := response.Body.String()
	if response.Code != http.StatusOK || len(stub.previews) != 0 || strings.Contains(body, `data-testid="event-generation-result"`) {
		t.Fatalf("status = %d, previews = %d, want the form only", response.Code, len(stub.previews))
	}
	for _, want := range []string{`data-testid="event-generation-form"`, `<option value="2026-10-26" selected>`} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q", want)
		}
	}
}

func TestEventGenerationConfirm(t *testing.T) {
	dependencies, stub := eventGenerationDependencies(t, groupMemberAs(service.GroupRoleManager))
	response := serveGroups(t, dependencies, http.MethodPost, eventGenerationPath, "part=12&week=2026-10-26&weeks=2", false)
	body := response.Body.String()
	if response.Code != http.StatusOK || len(stub.generated) != 1 || stub.generated[0].PartID != 12 || stub.generated[0].Weeks != 2 {
		t.Fatalf("status = %d, generated = %+v", response.Code, stub.generated)
	}
	for _, want := range []string{"Events generated: 1 created, 0 rebuilt, 0 removed, 1 kept.", `data-done="true"`, "Created", `data-testid="event-generation-kept"`} {
		if !strings.Contains(body, want) {
			t.Errorf("result missing %q", want)
		}
	}
	if strings.Contains(body, `data-testid="event-generation-confirm"`) {
		t.Error("the result offers to generate again")
	}
	stub.preview.Counts = service.GenerationCounts{Unchanged: 2}
	response = serveGroups(t, dependencies, http.MethodPost, eventGenerationPath, "part=12&week=2026-10-26", false)
	if !strings.Contains(response.Body.String(), "Nothing changed") {
		t.Error("a repeat run does not say nothing changed")
	}
}

func TestEventGenerationRefusals(t *testing.T) {
	dependencies, stub := eventGenerationDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	for _, test := range []struct {
		err        error
		wantStatus int
	}{
		{service.ErrInvalidGenerationWeek, http.StatusUnprocessableEntity},
		{service.ErrSeasonNotFound, http.StatusNotFound},
		{service.ErrGroupNotFound, http.StatusNotFound},
		{service.ErrGroupForbidden, http.StatusForbidden},
	} {
		stub.err = test.err
		if response := serveGroups(t, dependencies, http.MethodPost, eventGenerationPath, "part=12&week=2026-10-27", false); response.Code != test.wantStatus {
			t.Errorf("confirm with %v status = %d, want %d", test.err, response.Code, test.wantStatus)
		}
		if response := serveGroups(t, dependencies, http.MethodGet, eventGenerationPath+"?part=12&week=2026-10-27", "", false); response.Code != test.wantStatus {
			t.Errorf("preview with %v status = %d, want %d", test.err, response.Code, test.wantStatus)
		}
	}
	stub.err = nil
	if response := serveGroups(t, dependencies, http.MethodPost, eventGenerationPath, "week=2026-10-26", false); response.Code != http.StatusNotFound {
		t.Errorf("confirm without a part status = %d, want 404", response.Code)
	}

	outsider, outsiderStub := eventGenerationDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	outsider.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if response := serveGroups(t, outsider, method, eventGenerationPath+"?part=12&week=2026-10-26", "part=12&week=2026-10-26", false); response.Code != http.StatusNotFound {
			t.Errorf("non-member %s status = %d, want 404", method, response.Code)
		}
	}
	if len(outsiderStub.previews)+len(outsiderStub.generated) != 0 {
		t.Fatal("a non-member reached event generation")
	}
	for _, role := range []string{service.GroupRoleCoach, service.GroupRolePlayer, service.GroupRoleParent} {
		denied, deniedStub := eventGenerationDependencies(t, groupMemberAs(role))
		if response := serveGroups(t, denied, http.MethodPost, eventGenerationPath, "part=12&week=2026-10-26", false); response.Code != http.StatusForbidden || len(deniedStub.generated) != 0 {
			t.Errorf("%s confirm status = %d, generated %d", role, response.Code, len(deniedStub.generated))
		}
	}
}

func TestTrainingPlanLinksToEventGeneration(t *testing.T) {
	for role, wantLink := range map[string]bool{service.GroupRoleAdmin: true, service.GroupRoleCoach: false} {
		dependencies, _ := trainingPlanDependencies(t, groupMemberAs(role))
		response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/training-plan?part=12", "", false)
		body := response.Body.String()
		if strings.Contains(body, `data-testid="training-plan-generate-link"`) != wantLink {
			t.Errorf("%s: generate link shown = %v, want %v", role, !wantLink, wantLink)
		}
		if wantLink && !strings.Contains(body, `href="/en/groups/lions/event-generation?part=12"`) {
			t.Error("generate link does not point at the part")
		}
	}
}
