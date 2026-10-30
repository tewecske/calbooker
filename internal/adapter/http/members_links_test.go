package httpadapter

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func membersLinkFixture(t *testing.T, access *service.GroupAccess) (Dependencies, *profilesUseCaseStub) {
	t.Helper()
	dependencies, groups, _ := groupTestDependencies(t, access)
	pat, ana, ben, cat := "Parent Pat", "Player Ana", "Player Ben", "Parent Cat"
	groups.members = []service.GroupMember{
		{UserID: 7, Roles: []string{service.GroupRoleParent}, DisplayName: &pat, Kind: service.UserKindAccount},
		{UserID: 8, Roles: []string{service.GroupRolePlayer}, DisplayName: &ana, Kind: service.UserKindAccount},
		{UserID: 9, Roles: []string{service.GroupRolePlayer}, DisplayName: &ben, Kind: service.UserKindAccount},
		{UserID: 10, Roles: []string{service.GroupRoleParent}, DisplayName: &cat, Kind: service.UserKindAccount},
	}
	profiles := &profilesUseCaseStub{overview: service.ChildrenOverview{Children: []service.ChildEntry{{
		Person:    service.PersonRef{UserID: 8, DisplayName: &ana},
		Guardians: []service.PersonRef{{UserID: 7, DisplayName: &pat}},
	}}}}
	dependencies.Profiles = profiles
	return dependencies, profiles
}

func TestMembersPageLinksParentsToPlayersForStaff(t *testing.T) {
	dependencies, profiles := membersLinkFixture(t, groupMemberAs(service.GroupRoleManager))
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/members", "", false).Body.String()
	for _, want := range []string{
		`data-testid="member-players"`, `data-player="8"`, "Unlink player: Player Ana",
		`data-testid="member-parents"`, `data-parent="7"`,
		`Link to player: Parent Cat`, `<option value="8">Player Ana</option>`, `<option value="9">Player Ben</option>`,
		`name="return" value="members"`, "/en/groups/lions/guardians/add", "/en/groups/lions/guardians/remove",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("members page missing %q", want)
		}
	}
	// Pat is already linked to Ana, so Ana is not offered again.
	pat := body[strings.Index(body, `data-member-id="7"`):strings.Index(body, `data-member-id="8"`)]
	if strings.Contains(pat, `<option value="8">`) || !strings.Contains(pat, `<option value="9">`) {
		t.Error("link select for a linked parent offers the wrong players")
	}

	linked := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/guardians/add", "parent_id=10&child_id=9&return=members", true)
	if linked.Code != http.StatusOK || profiles.lastParent != 10 || profiles.lastChild != 9 {
		t.Fatalf("link = %d (%d->%d)", linked.Code, profiles.lastParent, profiles.lastChild)
	}
	if text := linked.Body.String(); !strings.Contains(text, "Parent linked.") || !strings.Contains(text, `data-testid="member-players"`) || strings.Contains(text, `id="guardian-parent"`) {
		t.Error("link did not re-render the members page")
	}
	unlinked := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/guardians/remove", "parent_id=7&child_id=8&return=members", true)
	if unlinked.Code != http.StatusOK || !strings.Contains(unlinked.Body.String(), "Parent unlinked.") || !strings.Contains(unlinked.Body.String(), `data-testid="member-players"`) {
		t.Fatalf("unlink = %d", unlinked.Code)
	}

	profiles.writeErr = service.ErrGuardianNeedsParentRole
	failed := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/guardians/add", "parent_id=8&child_id=9&return=members", true)
	if failed.Code != http.StatusUnprocessableEntity || !strings.Contains(failed.Body.String(), "parent role first") || !strings.Contains(failed.Body.String(), `data-testid="member-players"`) {
		t.Fatalf("failed link = %d, want 422 on the members page", failed.Code)
	}
}

func TestMembersPageHidesGuardianLinksFromOthers(t *testing.T) {
	for name, access := range map[string]*service.GroupAccess{
		"coach":  groupMemberAs(service.GroupRoleCoach),
		"player": groupMemberAs(service.GroupRolePlayer),
	} {
		t.Run(name, func(t *testing.T) {
			dependencies, profiles := membersLinkFixture(t, access)
			body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/members", "", false).Body.String()
			for _, unwanted := range []string{"member-players", "member-parents", "guardians/add", "guardians/remove"} {
				if strings.Contains(body, unwanted) {
					t.Errorf("%s sees %q", name, unwanted)
				}
			}
			if profiles.calls != 0 {
				t.Error("guardian data loaded for a non-staff member")
			}
		})
	}
}

func TestMembersGuardianLinksOutsiderGetsNotFound(t *testing.T) {
	dependencies, profiles := membersLinkFixture(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, route := range []struct{ method, target string }{
		{http.MethodGet, "/en/groups/lions/members"},
		{http.MethodPost, "/en/groups/lions/guardians/add"},
		{http.MethodPost, "/en/groups/lions/guardians/remove"},
	} {
		response := serveGroups(t, dependencies, route.method, route.target, "parent_id=7&child_id=9&return=members", false)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
			t.Errorf("%s %s = %d, want 404", route.method, route.target, response.Code)
		}
	}
	if profiles.calls != 0 {
		t.Fatal("outsider reached the guardian use cases")
	}
}
