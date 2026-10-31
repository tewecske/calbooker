package httpadapter

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func childFixture() service.ChildEntry {
	name := "Anna"
	parent := "Parent Pat"
	return service.ChildEntry{
		Person:    service.PersonRef{UserID: 20, Kind: service.UserKindProfile, DisplayName: &name},
		Profile:   &service.ChildProfile{UserID: 20, GroupID: 3, DisplayName: name, BirthYear: 2016, Version: 2},
		Guardians: []service.PersonRef{{UserID: 7, DisplayName: &parent}},
		Teams:     []service.TeamRef{{ID: 4, Name: "Lions"}},
	}
}

func TestChildrenPageForParent(t *testing.T) {
	parent := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, ChildUserIDs: []int64{20}})
	dependencies, _, _ := groupTestDependencies(t, parent)
	profiles := &profilesUseCaseStub{overview: service.ChildrenOverview{Children: []service.ChildEntry{childFixture()}}}
	dependencies.Profiles = profiles
	page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/children", "", false)
	body := page.Body.String()
	if page.Code != http.StatusOK {
		t.Fatalf("children page = %d", page.Code)
	}
	for _, want := range []string{"Your children", "Anna", "2016", "Lions", "Parent Pat", `id="child-new-name"`, "/en/groups/lions/children/20/delete"} {
		if !strings.Contains(body, want) {
			t.Errorf("children page missing %q", want)
		}
	}
	// Parents do not pick guardians or teams and cannot unlink.
	for _, unwanted := range []string{`id="child-new-guardian"`, `id="child-new-team"`, `id="guardian-parent"`, "/en/groups/lions/guardians/remove"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("parent page shows staff control %q", unwanted)
		}
	}

	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children", "display_name=Ben&birth_year=2017", true)
	if created.Code != http.StatusOK || profiles.lastInput != (service.ProfileInput{DisplayName: "Ben", BirthYear: 2017}) {
		t.Fatalf("create = %d %+v", created.Code, profiles.lastInput)
	}
	profiles.writeErr = service.ErrInvalidProfileBirthYear
	invalid := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children", "display_name=Ben&birth_year=x", false)
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "between 1900 and 2200") || !strings.Contains(invalid.Body.String(), `value="Ben"`) {
		t.Fatalf("invalid create = %d, want 422 keeping the name", invalid.Code)
	}
}

func TestChildrenPageForStaff(t *testing.T) {
	parentName := "Parent Pat"
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleManager))
	profiles := &profilesUseCaseStub{overview: service.ChildrenOverview{
		Children: []service.ChildEntry{childFixture()},
		Members:  []service.GroupMember{{UserID: 7, Roles: []string{service.GroupRoleParent}, DisplayName: &parentName}, {UserID: 20, Roles: []string{service.GroupRolePlayer}, Kind: service.UserKindProfile}},
		Teams:    []service.TeamSummary{{Team: service.Team{ID: 4, Slug: "t4", Name: "Lions", BirthYearFrom: 2016, BirthYearTo: 2016}}},
	}}
	dependencies.Profiles = profiles
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/children", "", false).Body.String()
	for _, want := range []string{"Children and guardians", `id="child-new-guardian"`, `id="child-new-team"`, `id="guardian-parent"`, "/en/groups/lions/guardians/remove", `<option value="4">Lions (2016)</option>`} {
		if !strings.Contains(body, want) {
			t.Errorf("staff page missing %q", want)
		}
	}

	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children", "display_name=Ben&birth_year=2017&guardian_id=7&team_id=4", true); response.Code != http.StatusOK {
		t.Fatalf("staff create = %d", response.Code)
	}
	if profiles.lastInput != (service.ProfileInput{DisplayName: "Ben", BirthYear: 2017, GuardianUserID: 7, TeamID: 4}) {
		t.Fatalf("staff input = %+v", profiles.lastInput)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/guardians/add", "parent_id=7&child_id=20", true); response.Code != http.StatusOK || profiles.lastParent != 7 || profiles.lastChild != 20 {
		t.Fatalf("link = %d (%d->%d)", response.Code, profiles.lastParent, profiles.lastChild)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/guardians/remove", "parent_id=7&child_id=20", true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Parent unlinked.") {
		t.Fatalf("unlink = %d", response.Code)
	}
	writes := profiles.writes
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/guardians/add", "parent_id=x&child_id=20", true); response.Code != http.StatusUnprocessableEntity || profiles.writes != writes {
		t.Fatalf("malformed link = %d, want 422 without a write", response.Code)
	}
	profiles.writeErr = service.ErrGuardianNeedsParentRole
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/guardians/add", "parent_id=8&child_id=20", false); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "parent role first") {
		t.Fatalf("link without role = %d", response.Code)
	}
}

func TestChildProfileUpdateAndDelete(t *testing.T) {
	parent := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleParent}, ChildUserIDs: []int64{20}})
	dependencies, _, _ := groupTestDependencies(t, parent)
	profiles := &profilesUseCaseStub{}
	dependencies.Profiles = profiles
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children/20", "version=2&display_name=Anna+B&birth_year=2016", true); response.Code != http.StatusOK || profiles.lastVersion != 2 || profiles.lastInput.DisplayName != "Anna B" {
		t.Fatalf("update = %d %+v", response.Code, profiles.lastInput)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children/20/delete", "version=3", true); response.Code != http.StatusOK || profiles.lastChild != 20 {
		t.Fatalf("delete = %d", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children/20/delete", "", false); response.Code != http.StatusConflict {
		t.Fatalf("delete without version = %d, want 409", response.Code)
	}
	profiles.writeErr = service.ErrProfileNotFound
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children/21/delete", "version=0", false); response.Code != http.StatusNotFound {
		t.Fatalf("delete of another child = %d, want 404", response.Code)
	}
	profiles.writeErr = service.ErrOptimisticLockConflict
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children/20", "version=1&display_name=A&birth_year=2016", false); response.Code != http.StatusConflict {
		t.Fatalf("stale update = %d, want 409", response.Code)
	}
}

func TestChildrenPageForbiddenForPlayers(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRolePlayer))
	profiles := &profilesUseCaseStub{writeErr: service.ErrGroupForbidden}
	dependencies.Profiles = profiles
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/children", "", false); response.Code != http.StatusForbidden || profiles.calls != 0 {
		t.Fatalf("player children page = %d (calls %d), want 403", response.Code, profiles.calls)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/children", "display_name=A&birth_year=2016", true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("player HTMX create = %d, want 422", response.Code)
	}
}

func TestMembersPageMarksProfilesAndHidesTheirRemoval(t *testing.T) {
	name := "Anna"
	dependencies, groups, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	groups.members = []service.GroupMember{{UserID: 20, Kind: service.UserKindProfile, DisplayName: &name, Roles: []string{service.GroupRolePlayer}}}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/members", "", false).Body.String()
	if !strings.Contains(body, "Child profile") || strings.Contains(body, "/en/groups/lions/members/20/remove\"") || strings.Contains(body, `<option value="parent">`) {
		t.Fatal("profiles must be marked, offered only the player role, and not removable as members")
	}
}

func TestTeamRosterShowsLinkedParents(t *testing.T) {
	child, parent := "Anna", "Parent Pat"
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleManager))
	teams.detail = service.TeamDetail{
		Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", BirthYearFrom: 2016, BirthYearTo: 2016},
		Roster: []service.TeamRosterEntry{
			{UserID: 20, Role: service.GroupRolePlayer, DisplayName: &child},
			{UserID: 7, Role: service.GroupRoleParent, DisplayName: &parent, ChildUserIDs: []int64{20}, Linked: true},
		},
	}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false).Body.String()
	if !strings.Contains(body, `data-roster-linked="true"`) || !strings.Contains(body, "Parent Pat") {
		t.Fatal("linked parent missing from the roster")
	}
	if strings.Contains(body, `value="7:parent"`) {
		t.Fatal("a linked parent must not be removable from the roster directly")
	}
}

type profilesUseCaseStub struct {
	overview    service.ChildrenOverview
	writeErr    error
	calls       int
	writes      int
	lastInput   service.ProfileInput
	lastVersion int64
	lastParent  int64
	lastChild   int64
	lastTeam    int64
	joined      []int64
	left        []int64
}

func (s *profilesUseCaseStub) write() error {
	s.calls++
	s.writes++
	return s.writeErr
}

func (s *profilesUseCaseStub) Children(context.Context, *service.GroupAccess) (service.ChildrenOverview, error) {
	s.calls++
	return s.overview, nil
}

func (s *profilesUseCaseStub) CreateProfile(_ context.Context, _ service.GroupActor, input service.ProfileInput) (service.ChildProfile, error) {
	s.lastInput = input
	return service.ChildProfile{UserID: 21}, s.write()
}

func (s *profilesUseCaseStub) UpdateProfile(_ context.Context, _ service.GroupActor, userID, version int64, input service.ProfileInput) (service.ChildProfile, error) {
	s.lastChild, s.lastVersion, s.lastInput = userID, version, input
	return service.ChildProfile{UserID: userID}, s.write()
}

func (s *profilesUseCaseStub) DeleteProfile(_ context.Context, _ service.GroupActor, userID, version int64) error {
	s.lastChild, s.lastVersion = userID, version
	return s.write()
}

func (s *profilesUseCaseStub) AddGuardian(_ context.Context, _ service.GroupActor, parentID, childID int64) error {
	s.lastParent, s.lastChild = parentID, childID
	return s.write()
}

func (s *profilesUseCaseStub) RemoveGuardian(_ context.Context, _ service.GroupActor, parentID, childID int64) error {
	s.lastParent, s.lastChild = parentID, childID
	return s.write()
}

func (s *profilesUseCaseStub) AddChildToTeam(_ context.Context, _ service.GroupActor, teamID int64, input service.ProfileInput) (service.ChildProfile, error) {
	s.lastTeam, s.lastInput = teamID, input
	return service.ChildProfile{UserID: 21}, s.write()
}

func (s *profilesUseCaseStub) JoinChildToTeam(_ context.Context, _ service.GroupActor, teamID, childID int64) error {
	s.lastTeam, s.lastChild = teamID, childID
	s.joined = append(s.joined, childID)
	return s.write()
}

func (s *profilesUseCaseStub) RemoveChildFromTeam(_ context.Context, _ service.GroupActor, teamID, childID int64) error {
	s.lastTeam, s.lastChild = teamID, childID
	s.left = append(s.left, childID)
	return s.write()
}
