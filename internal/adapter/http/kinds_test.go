package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

// Event kinds of the default set, as group 3 has them in these tests.
var (
	testEventKindIDs   = map[string]int64{service.EventKindTraining: 1, service.EventKindMatch: 2, service.EventKindChampionship: 3, service.EventKindOther: 4}
	testEventKindNames = map[string]string{service.EventKindTraining: "Training", service.EventKindMatch: "Match", service.EventKindChampionship: "Championship", service.EventKindOther: "Other"}
)

func defaultEventKindFixtures() []service.Kind {
	hungarian := map[string]string{service.EventKindTraining: "Edzés", service.EventKindMatch: "Mérkőzés", service.EventKindChampionship: "Bajnokság", service.EventKindOther: "Egyéb"}
	kinds := make([]service.Kind, 0, 4)
	for position, code := range []string{service.EventKindTraining, service.EventKindMatch, service.EventKindChampionship, service.EventKindOther} {
		kinds = append(kinds, service.Kind{ID: testEventKindIDs[code], GroupID: 3, SystemCode: code, NameEN: testEventKindNames[code], NameHU: hungarian[code], Position: position, Version: 1})
	}
	return kinds
}

// kindsUseCaseStub records kind calls and answers from fixed lists.
type kindsUseCaseStub struct {
	events      []service.Kind
	resources   []service.Kind
	writeErr    error
	calls       int
	writes      int
	lastScope   service.KindScope
	lastID      int64
	lastVersion int64
	lastInput   service.KindInput
	lastOffset  int
	lastDeleted bool
	lastArchive bool
}

func newKindsStub() *kindsUseCaseStub {
	return &kindsUseCaseStub{
		events: defaultEventKindFixtures(),
		resources: []service.Kind{
			{ID: 11, GroupID: 3, SystemCode: "field", NameEN: "Field", NameHU: "Füves pálya", Version: 1},
			{ID: 12, GroupID: 3, NameEN: "Beach", Uses: 2, Version: 4},
			{ID: 13, GroupID: 3, NameEN: "Old", Version: 1, ArchivedAt: new(int64(7))},
		},
	}
}

func (s *kindsUseCaseStub) list(scope service.KindScope) []service.Kind {
	if scope == service.KindScopeResource {
		return s.resources
	}
	return s.events
}

func (s *kindsUseCaseStub) All(_ context.Context, _ *service.GroupAccess, scope service.KindScope) ([]service.Kind, error) {
	s.calls++
	return s.list(scope), nil
}

func (s *kindsUseCaseStub) Active(_ context.Context, _ *service.GroupAccess, scope service.KindScope) ([]service.Kind, error) {
	s.calls++
	var active []service.Kind
	for _, kind := range s.list(scope) {
		if !kind.Archived() {
			active = append(active, kind)
		}
	}
	return active, nil
}

func (s *kindsUseCaseStub) Find(_ context.Context, _ *service.GroupAccess, scope service.KindScope, kindID int64) (service.Kind, error) {
	for _, kind := range s.list(scope) {
		if kind.ID == kindID {
			return kind, nil
		}
	}
	return service.Kind{}, service.ErrKindNotFound
}

func (s *kindsUseCaseStub) write(scope service.KindScope, id, version int64) error {
	s.calls++
	s.writes++
	s.lastScope, s.lastID, s.lastVersion = scope, id, version
	return s.writeErr
}

func (s *kindsUseCaseStub) Create(_ context.Context, _ service.GroupActor, scope service.KindScope, input service.KindInput) (service.Kind, error) {
	s.lastInput = input
	return service.Kind{ID: 99, GroupID: 3, NameEN: input.NameEN}, s.write(scope, 0, 0)
}

func (s *kindsUseCaseStub) Rename(_ context.Context, _ service.GroupActor, scope service.KindScope, id, version int64, input service.KindInput) (service.Kind, error) {
	s.lastInput = input
	return service.Kind{ID: id, GroupID: 3, NameEN: input.NameEN}, s.write(scope, id, version)
}

func (s *kindsUseCaseStub) SetArchived(_ context.Context, _ service.GroupActor, scope service.KindScope, id, version int64, archived bool) (service.Kind, error) {
	s.lastArchive = archived
	return service.Kind{ID: id, GroupID: 3}, s.write(scope, id, version)
}

func (s *kindsUseCaseStub) Move(_ context.Context, _ service.GroupActor, scope service.KindScope, id int64, offset int) error {
	s.lastOffset = offset
	return s.write(scope, id, 0)
}

func (s *kindsUseCaseStub) Delete(_ context.Context, _ service.GroupActor, scope service.KindScope, id, version int64) error {
	s.lastDeleted = true
	return s.write(scope, id, version)
}

func kindsDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *kindsUseCaseStub) {
	t.Helper()
	dependencies, _, _ := groupTestDependencies(t, access)
	kinds := newKindsStub()
	dependencies.Kinds = kinds
	return dependencies, kinds
}

func TestKindsPageByRole(t *testing.T) {
	tests := []struct {
		name       string
		access     *service.GroupAccess
		wantStatus int
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantStatus: http.StatusOK},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), wantStatus: http.StatusOK},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantStatus: http.StatusOK},
		{name: "coach is denied", access: groupMemberAs(service.GroupRoleCoach), wantStatus: http.StatusForbidden},
		{name: "player is denied", access: groupMemberAs(service.GroupRolePlayer), wantStatus: http.StatusForbidden},
		{name: "parent is denied", access: groupMemberAs(service.GroupRoleParent), wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, kinds := kindsDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/kinds", "", false)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantStatus != http.StatusOK {
				if kinds.calls != 0 || strings.Contains(body, "Championship") {
					t.Fatal("denied account reached the kinds")
				}
				return
			}
			for _, want := range []string{
				`id="kinds-events-heading"`, `id="kinds-resources-heading"`, "Championship", "Beach", `data-kind-code="training"`,
				`data-testid="kind-protected-badge"`, `data-testid="kind-archived-badge"`, `data-section="settings"`,
				`action="/en/groups/lions/kinds/events"`, `action="/en/groups/lions/kinds/resources/12/move"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("kinds page missing %q", want)
				}
			}
			// Training is protected: no archive or delete; Beach is in use: no delete.
			if strings.Contains(body, `kinds/events/1/archive`) || strings.Contains(body, `kinds/events/1/delete`) {
				t.Error("the training kind offers archive or delete")
			}
			if strings.Contains(body, `kinds/resources/12/delete`) || !strings.Contains(body, `kinds/resources/11/delete`) {
				t.Error("delete is offered for a kind in use or missing for a free one")
			}
		})
	}
}

func TestKindsOutsiderGetsNotFound(t *testing.T) {
	dependencies, kinds := kindsDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, request := range []struct{ method, target string }{
		{http.MethodGet, "/en/groups/lions/kinds"},
		{http.MethodPost, "/en/groups/lions/kinds/events"},
		{http.MethodPost, "/en/groups/lions/kinds/events/1"},
		{http.MethodPost, "/en/groups/lions/kinds/events/1/archive"},
		{http.MethodPost, "/en/groups/lions/kinds/events/1/move"},
		{http.MethodPost, "/en/groups/lions/kinds/events/1/delete"},
	} {
		if response := serveGroups(t, dependencies, request.method, request.target, "name_en=X&version=1", false); response.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", request.method, request.target, response.Code)
		}
	}
	if kinds.writes != 0 {
		t.Fatal("outsider requests reached the kind use cases")
	}
}

func TestKindsWrites(t *testing.T) {
	dependencies, kinds := kindsDependencies(t, groupMemberAs(service.GroupRoleManager))
	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/events", "name_en=+Camp+&name_hu=T%C3%A1bor", true)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "Kind added.") || kinds.lastScope != service.KindScopeEvent ||
		kinds.lastInput != (service.KindInput{NameEN: " Camp ", NameHU: "Tábor"}) {
		t.Fatalf("create = %d scope %q input %+v", created.Code, kinds.lastScope, kinds.lastInput)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/resources/12", "version=4&name_en=Sand&name_hu=", true)
	if kinds.lastScope != service.KindScopeResource || kinds.lastID != 12 || kinds.lastVersion != 4 || kinds.lastInput.NameEN != "Sand" {
		t.Fatalf("rename = scope %q id %d version %d input %+v", kinds.lastScope, kinds.lastID, kinds.lastVersion, kinds.lastInput)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/events/2/archive", "version=1&archived=true", true)
	if kinds.lastID != 2 || !kinds.lastArchive || kinds.lastVersion != 1 {
		t.Fatalf("archive = id %d archived %v version %d", kinds.lastID, kinds.lastArchive, kinds.lastVersion)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/events/2/move", "direction=up", true)
	if kinds.lastOffset != -1 {
		t.Fatalf("move up offset = %d", kinds.lastOffset)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/events/2/move", "direction=down", true)
	if kinds.lastOffset != 1 {
		t.Fatalf("move down offset = %d", kinds.lastOffset)
	}
	serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/events/4/delete", "version=1", true)
	if !kinds.lastDeleted || kinds.lastID != 4 {
		t.Fatalf("delete = %v id %d", kinds.lastDeleted, kinds.lastID)
	}
}

func TestKindsWriteFailures(t *testing.T) {
	tests := []struct {
		err    error
		status int
		text   string
	}{
		{service.ErrKindInUse, http.StatusUnprocessableEntity, "This kind is in use."},
		{service.ErrKindProtected, http.StatusUnprocessableEntity, "This kind is required"},
		{service.ErrKindNameTaken, http.StatusUnprocessableEntity, "already has this name"},
		{service.ErrInvalidKindName, http.StatusUnprocessableEntity, "Enter an English name"},
		{service.ErrOptimisticLockConflict, http.StatusConflict, ""},
		{service.ErrKindNotFound, http.StatusNotFound, ""},
		{service.ErrGroupForbidden, http.StatusForbidden, ""},
	}
	for _, test := range tests {
		dependencies, kinds := kindsDependencies(t, groupMemberAs(service.GroupRoleAdmin))
		kinds.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/events/4/delete", "version=1", false)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
			t.Errorf("error %v = %d, want %d with %q", test.err, response.Code, test.status, test.text)
		}
	}
	dependencies, kinds := kindsDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	for _, target := range []string{
		"/en/groups/lions/kinds/people", "/en/groups/lions/kinds/events/x", "/en/groups/lions/kinds/events/0/delete",
		"/en/groups/lions/kinds/people/1/archive",
	} {
		if response := serveGroups(t, dependencies, http.MethodPost, target, "version=1&name_en=X", false); response.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", target, response.Code)
		}
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/kinds/events/4/delete", "version=bad", false); response.Code != http.StatusConflict {
		t.Errorf("a bad version = %d, want a conflict", response.Code)
	}
	if kinds.writes != 0 {
		t.Fatal("malformed requests reached the kind use cases")
	}
}

func TestKindsHungarianNamesAndSettingsLink(t *testing.T) {
	dependencies, _ := kindsDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	body := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/lions/kinds", "", false).Body.String()
	for _, want := range []string{"Esemény típusok", "Erőforrás típusok", "Kötelező"} {
		if !strings.Contains(body, want) {
			t.Errorf("Hungarian kinds page missing %q", want)
		}
	}
	settings := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/settings", "", false).Body.String()
	if !strings.Contains(settings, `href="/en/groups/lions/kinds"`) {
		t.Error("settings page does not link to the kinds page")
	}
}
