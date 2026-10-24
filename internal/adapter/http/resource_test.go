package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func resourceFixtures() []service.Resource {
	description := "North end"
	limit := 2
	archivedAt := int64(5)
	return []service.Resource{
		{ID: 4, GroupID: 3, Name: "Main field", Kind: service.ResourceKindField, Description: &description, MaxConcurrentTeams: &limit, Version: 6},
		{ID: 5, GroupID: 3, Name: "Old hall", Kind: service.ResourceKindHall, ArchivedAt: &archivedAt},
	}
}

func resourceDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *resourcesUseCaseStub) {
	t.Helper()
	dependencies, _, _ := groupTestDependencies(t, access)
	resources := &resourcesUseCaseStub{list: resourceFixtures(), found: resourceFixtures()[0]}
	dependencies.Resources = resources
	return dependencies, resources
}

func TestResourcesListByRole(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantStatus int
		wantCreate bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantStatus: http.StatusOK, wantCreate: true},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), wantStatus: http.StatusOK, wantCreate: true},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantStatus: http.StatusOK, wantCreate: true},
		{name: "coach reads", access: groupMemberAs(service.GroupRoleCoach), wantStatus: http.StatusOK},
		{name: "player is denied", access: groupMemberAs(service.GroupRolePlayer), wantStatus: http.StatusForbidden},
		{name: "parent is denied", access: groupMemberAs(service.GroupRoleParent), wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, resources := resourceDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/resources", "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantStatus != http.StatusOK {
				if resources.calls != 0 || strings.Contains(body, "Main field") {
					t.Fatal("denied account reached the resource list")
				}
				return
			}
			for _, want := range []string{`id="group-resources-heading"`, "Main field", "North end", "Field", `data-testid="resource-capacity">2<`, "Archived resources", "Old hall", "Sports hall", `data-section="resources"`, `href="/en/groups/3/resources/4"`} {
				if !strings.Contains(body, want) {
					t.Errorf("resources page missing %q", want)
				}
			}
			if strings.Contains(body, `id="resource-new-name"`) != test.wantCreate {
				t.Errorf("create form shown = %v, want %v", !test.wantCreate, test.wantCreate)
			}
			if strings.Contains(body, `data-testid="resources-read-only"`) == test.wantCreate {
				t.Error("read-only notice must show exactly when the create form is hidden")
			}
		})
	}
}

func TestResourcesNavigationFollowsAccess(t *testing.T) {
	for _, test := range []struct {
		access *service.GroupAccess
		want   bool
	}{
		{groupMemberAs(service.GroupRoleCoach), true},
		{groupMemberAs(service.GroupRoleManager), true},
		{groupMemberAs(service.GroupRolePlayer), false},
	} {
		dependencies, _ := resourceDependencies(t, test.access)
		body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false).Body.String()
		if strings.Contains(body, `data-section="resources"`) != test.want {
			t.Errorf("roles %v: resources tab shown = %v, want %v", test.access.Roles(), !test.want, test.want)
		}
	}
}

func TestResourcesOutsiderGetsNotFound(t *testing.T) {
	dependencies, resources := resourceDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, test := range []struct{ method, target string }{
		{http.MethodGet, "/en/groups/3/resources"},
		{http.MethodPost, "/en/groups/3/resources"},
		{http.MethodGet, "/en/groups/3/resources/4"},
		{http.MethodPost, "/en/groups/3/resources/4"},
		{http.MethodPost, "/en/groups/3/resources/4/archive"},
	} {
		if response := serveGroups(t, dependencies, test.method, test.target, "version=1&name=X&kind=field", false); response.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", test.method, test.target, response.Code)
		}
	}
	if resources.calls != 0 {
		t.Fatal("outsider requests reached the resource use cases")
	}
}

func TestResourceCreate(t *testing.T) {
	dependencies, resources := resourceDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources", "name=Court+1&kind=court&description=Indoor&max_concurrent_teams=3", true)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "Resource added.") || !strings.Contains(created.Body.String(), `id="alerts"`) {
		t.Fatalf("create = %d, want the list with the success alert", created.Code)
	}
	if resources.lastInput != (service.ResourceInput{Name: "Court 1", Kind: service.ResourceKindCourt, Description: "Indoor", MaxConcurrentTeams: 3}) {
		t.Fatalf("input = %+v", resources.lastInput)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources", "name=Pool&kind=pool&max_concurrent_teams=", true)
	if resources.lastInput.MaxConcurrentTeams != 0 {
		t.Fatalf("empty limit = %d, want 0 (no limit)", resources.lastInput.MaxConcurrentTeams)
	}
	for _, raw := range []string{"abc", "0", "-3", "1.5"} {
		serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources", "name=Pool&kind=pool&max_concurrent_teams="+raw, true)
		if resources.lastInput.MaxConcurrentTeams != -1 {
			t.Errorf("limit %q = %d, want -1 so validation refuses it", raw, resources.lastInput.MaxConcurrentTeams)
		}
	}

	for _, test := range []struct {
		err   error
		text  string
		field string
	}{
		{service.ErrInvalidResourceName, "up to 64 characters", `id="resource-new-name-error"`},
		{service.ErrResourceNameTaken, "already has this name", `id="resource-new-name-error"`},
		{service.ErrInvalidResourceKind, "Choose a kind", `id="resource-new-kind-error"`},
		{service.ErrInvalidResourceDescription, "255 characters", `id="resource-new-description-error"`},
		{service.ErrInvalidResourceCapacity, "from 1 to 99", `id="resource-new-max-teams-error"`},
	} {
		resources.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources", "name=Kept+name&kind=pool&max_concurrent_teams=500", true)
		body := response.Body.String()
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, test.text) || !strings.Contains(body, test.field) || !strings.Contains(body, `value="Kept name"`) || !strings.Contains(body, `value="500"`) {
			t.Errorf("error %v = %d, want 422 with %q on the field and the submitted values", test.err, response.Code, test.text)
		}
		if !strings.Contains(body, `<option value="pool" selected>`) {
			t.Errorf("error %v: submitted kind not kept", test.err)
		}
	}
}

func TestResourceCreateForbiddenForCoach(t *testing.T) {
	dependencies, resources := resourceDependencies(t, groupMemberAs(service.GroupRoleCoach))
	resources.writeErr = service.ErrGroupForbidden
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources", "name=X&kind=field", false); response.Code != http.StatusForbidden {
		t.Fatalf("coach create = %d, want 403", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources", "name=X&kind=field", true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("coach HTMX create = %d, want 422 so the alert swaps", response.Code)
	}
	player, playerResources := resourceDependencies(t, groupMemberAs(service.GroupRolePlayer))
	if response := serveGroups(t, player, http.MethodPost, "/en/groups/3/resources", "name=X&kind=field", false); response.Code != http.StatusForbidden || playerResources.writes != 0 {
		t.Fatalf("player create = %d (writes %d), want 403 before the use case", response.Code, playerResources.writes)
	}
}

func TestResourceDetailByRole(t *testing.T) {
	for _, test := range []struct {
		name     string
		access   *service.GroupAccess
		wantEdit bool
	}{
		{name: "manager edits", access: groupMemberAs(service.GroupRoleManager), wantEdit: true},
		{name: "coach reads", access: groupMemberAs(service.GroupRoleCoach)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _ := resourceDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/resources/4", "", false)
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `id="resource-heading"`) || !strings.Contains(body, "North end") || !strings.Contains(body, `data-section="resources" aria-current="page"`) {
				t.Fatalf("status = %d, resource page incomplete", response.Code)
			}
			for _, needle := range []string{`id="resource-edit-name"`, `/en/groups/3/resources/4/archive`, `name="version" value="6"`} {
				if strings.Contains(body, needle) != test.wantEdit {
					t.Errorf("body contains %q = %v, want %v", needle, !test.wantEdit, test.wantEdit)
				}
			}
			if test.wantEdit && (!strings.Contains(body, `<option value="field" selected>`) || !strings.Contains(body, `value="2"`)) {
				t.Error("edit form not filled from the stored resource")
			}
		})
	}
	dependencies, resources := resourceDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	resources.findErr = service.ErrResourceNotFound
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/resources/9", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("missing resource = %d, want 404", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/resources/x", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("malformed id = %d, want 404", response.Code)
	}
	player, _ := resourceDependencies(t, groupMemberAs(service.GroupRolePlayer))
	if response := serveGroups(t, player, http.MethodGet, "/en/groups/3/resources/4", "", false); response.Code != http.StatusForbidden {
		t.Fatalf("player detail = %d, want 403", response.Code)
	}
}

func TestResourceUpdateAndArchive(t *testing.T) {
	dependencies, resources := resourceDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	saved := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources/4", "version=6&name=Main+pitch&kind=field&max_concurrent_teams=4", true)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "Resource saved.") || resources.lastVersion != 6 || resources.lastID != 4 || resources.lastInput.Name != "Main pitch" {
		t.Fatalf("save = %d (version %d, id %d, input %+v)", saved.Code, resources.lastVersion, resources.lastID, resources.lastInput)
	}
	archived := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources/4/archive", "version=7&archived=true", true)
	if archived.Code != http.StatusOK || !strings.Contains(archived.Body.String(), "Resource archived.") || !resources.lastArchived {
		t.Fatalf("archive = %d", archived.Code)
	}
	restored := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/resources/4/archive", "version=8&archived=false", true)
	if restored.Code != http.StatusOK || !strings.Contains(restored.Body.String(), "Resource restored.") || resources.lastArchived {
		t.Fatalf("restore = %d", restored.Code)
	}

	writes := resources.writes
	for _, target := range []string{"/en/groups/3/resources/4", "/en/groups/3/resources/4/archive"} {
		for _, version := range []string{"", "x", "-1"} {
			response := serveGroups(t, dependencies, http.MethodPost, target, "version="+version+"&name=A&kind=field&archived=true", false)
			if response.Code != http.StatusConflict {
				t.Errorf("%s version %q = %d, want 409", target, version, response.Code)
			}
		}
	}
	if resources.writes != writes {
		t.Fatal("malformed versions reached the use case")
	}

	for _, test := range []struct {
		target string
		err    error
		status int
		text   string
	}{
		{"/en/groups/3/resources/4", service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
		{"/en/groups/3/resources/4", service.ErrResourceNotFound, http.StatusNotFound, ""},
		{"/en/groups/3/resources/4", service.ErrInvalidResourceCapacity, http.StatusUnprocessableEntity, "from 1 to 99"},
		{"/en/groups/3/resources/4/archive", service.ErrResourceNameTaken, http.StatusUnprocessableEntity, "already has this name"},
		{"/en/groups/3/resources/4/archive", service.ErrGroupForbidden, http.StatusForbidden, ""},
		{"/en/groups/3/resources/4/archive", service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
	} {
		resources.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, test.target, "version=6&name=A&kind=field&archived=false", false)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
			t.Errorf("%s error %v = %d, want %d with %q", test.target, test.err, response.Code, test.status, test.text)
		}
	}
}

func TestResourcesHungarianLabels(t *testing.T) {
	dependencies, _ := resourceDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/3/resources", "", false).Body.String()
	for _, want := range []string{"Helyszínek", "Füves pálya", "Archivált helyszínek", "Helyszín hozzáadása", `href="/hu/groups/3/resources/4"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Hungarian resources page missing %q", want)
		}
	}
}

type resourcesUseCaseStub struct {
	list         []service.Resource
	found        service.Resource
	findErr      error
	writeErr     error
	calls        int
	writes       int
	lastInput    service.ResourceInput
	lastID       int64
	lastVersion  int64
	lastArchived bool
}

func (s *resourcesUseCaseStub) write() error {
	s.calls++
	s.writes++
	return s.writeErr
}

func (s *resourcesUseCaseStub) List(context.Context, *service.GroupAccess) ([]service.Resource, error) {
	s.calls++
	return s.list, nil
}

func (s *resourcesUseCaseStub) Find(context.Context, *service.GroupAccess, int64) (service.Resource, error) {
	s.calls++
	return s.found, s.findErr
}

func (s *resourcesUseCaseStub) Create(_ context.Context, _ service.GroupActor, input service.ResourceInput) (service.Resource, error) {
	s.lastInput = input
	return service.Resource{ID: 99, GroupID: 3, Name: input.Name, Kind: input.Kind}, s.write()
}

func (s *resourcesUseCaseStub) Update(_ context.Context, _ service.GroupActor, id, version int64, input service.ResourceInput) (service.Resource, error) {
	s.lastID, s.lastVersion, s.lastInput = id, version, input
	return s.found, s.write()
}

func (s *resourcesUseCaseStub) SetArchived(_ context.Context, _ service.GroupActor, id, version int64, archived bool) (service.Resource, error) {
	s.lastID, s.lastVersion, s.lastArchived = id, version, archived
	return s.found, s.write()
}
