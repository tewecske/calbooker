package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// groupAccessAs returns the access of user 7 in group 3.
func groupAccessAs(grants service.GroupAccessGrants) *service.GroupAccess {
	return service.NewGroupAccess(3, 7, grants)
}

func groupMemberAs(roles ...string) *service.GroupAccess {
	return groupAccessAs(service.GroupAccessGrants{Member: true, Roles: roles})
}

// groupTestDependencies wires stubs for user 7 acting in group 3 with access.
func groupTestDependencies(t *testing.T, access *service.GroupAccess) (Dependencies, *groupsUseCaseStub, *teamsUseCaseStub) {
	t.Helper()
	dependencies := settingsStubDependencies(t)
	groups := &groupsUseCaseStub{
		group:    service.Group{ID: 3, Name: "Lions Club", TimeZone: "Europe/Budapest", Version: 2},
		overview: service.GroupOverview{Group: service.Group{ID: 3, Name: "Lions Club", TimeZone: "Europe/Budapest"}, MemberCount: 2},
	}
	teams := &teamsUseCaseStub{}
	dependencies.Groups = groups
	dependencies.Teams = teams
	dependencies.Invites = &invitesUseCaseStub{}
	dependencies.Profiles = &profilesUseCaseStub{}
	dependencies.Resources = &resourcesUseCaseStub{}
	dependencies.Seasons = &seasonsUseCaseStub{}
	dependencies.GroupAccess = &groupAccessStub{access: access}
	return dependencies, groups, teams
}

// serveGroups routes one request through the group and team routes, so path
// values come from the real patterns.
func serveGroups(t *testing.T, dependencies Dependencies, method, target, form string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	handler, _ := newGroupsTestHandler(t, dependencies)
	mux := http.NewServeMux()
	registerGroupRoutes(mux, handler)
	request := authenticatedRequest(method, target, 7, form)
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestGroupsListRequiresAuthentication(t *testing.T) {
	handler, _ := newGroupsTestHandler(t, settingsStubDependencies(t))
	response := httptest.NewRecorder()
	handler.list(response, httptest.NewRequest(http.MethodGet, "/en/groups", nil))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
}

func TestGroupsListRendersRoles(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Groups = &groupsUseCaseStub{summaries: []service.GroupSummary{{Group: service.Group{ID: 3, Name: "Lions Club"}, MemberCount: 2, Roles: []string{service.GroupRoleAdmin, service.GroupRoleCoach}}}}
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups", "", false)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Lions Club") || !strings.Contains(body, "Administrator, Coach") {
		t.Fatalf("status = %d, body missing group or roles", response.Code)
	}
	if !strings.Contains(body, `id="group-new-time-zone"`) || !strings.Contains(body, `<option value="Europe/Budapest" selected>`) {
		t.Error("create form missing the time zone picker with the default selected")
	}
}

func TestGroupsListLabelsAllGroupsAndNonMembersForAdmin(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, IsAdmin: true, Version: 1}}
	dependencies.Groups = &groupsUseCaseStub{summaries: []service.GroupSummary{{Group: service.Group{ID: 4, Name: "Other"}, MemberCount: 4}}}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups", "", false).Body.String()
	for _, want := range []string{"All groups", "Other", "Not a member"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin list missing %q", want)
		}
	}
}

func TestGroupsGuestSeesWhyTheyCannotCreate(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, Kind: service.UserKindGuest, Version: 1}}
	dependencies.Groups = &groupsUseCaseStub{}
	hungarian := serveGroups(t, dependencies, http.MethodGet, "/hu/groups", "", false).Body.String()
	if strings.Contains(hungarian, `id="group-new-name"`) || !strings.Contains(hungarian, "group-create-requires-account") || !strings.Contains(hungarian, "/hu/guest/upgrade") {
		t.Fatal("guest list must replace the create form with the account notice")
	}

	dependencies.Groups = &groupsUseCaseStub{createErr: service.ErrGroupRequiresAccount}
	full := serveGroups(t, dependencies, http.MethodPost, "/en/groups", "name=Club", false)
	if full.Code != http.StatusForbidden || !strings.Contains(full.Body.String(), "Only signed-in accounts") {
		t.Fatalf("guest create status = %d, want 403 with the account message", full.Code)
	}
	if htmx := serveGroups(t, dependencies, http.MethodPost, "/en/groups", "name=Club", true); htmx.Code != http.StatusUnprocessableEntity {
		t.Fatalf("HTMX guest create status = %d, want 422 so the alert swaps", htmx.Code)
	}
}

func TestGroupsCreate(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	groups := &groupsUseCaseStub{summary: service.GroupSummary{Group: service.Group{ID: 3, Name: "Club"}}}
	dependencies.Groups = groups
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups", "name=Club&time_zone=Europe%2FVienna", false)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en/groups/3" {
		t.Fatalf("create = %d %q, want 303 /en/groups/3", response.Code, response.Header().Get("Location"))
	}
	if groups.createdZone != "Europe/Vienna" {
		t.Fatalf("time zone = %q, want Europe/Vienna", groups.createdZone)
	}

	for _, test := range []struct {
		err  error
		want string
	}{
		{service.ErrInvalidGroupName, "64 characters"},
		{service.ErrInvalidTimeZone, "valid time zone"},
	} {
		dependencies.Groups = &groupsUseCaseStub{createErr: test.err}
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups", "name=&time_zone=Nowhere", false)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.want) {
			t.Fatalf("create error %v = %d, want 422 with %q", test.err, response.Code, test.want)
		}
	}
}

// TestGroupRoutesAnswerNotFoundToOutsiders checks that every group-scoped
// route answers 404 to an account that is not a member.
func TestGroupRoutesAnswerNotFoundToOutsiders(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/en/groups/3"},
		{http.MethodGet, "/en/groups/3/invites"},
		{http.MethodPost, "/en/groups/3/invites"},
		{http.MethodPost, "/en/groups/3/invites/5/revoke"},
		{http.MethodGet, "/en/groups/3/children"},
		{http.MethodPost, "/en/groups/3/children"},
		{http.MethodPost, "/en/groups/3/children/9"},
		{http.MethodPost, "/en/groups/3/children/9/delete"},
		{http.MethodPost, "/en/groups/3/guardians/add"},
		{http.MethodPost, "/en/groups/3/guardians/remove"},
		{http.MethodPost, "/en/groups/3/leave"},
		{http.MethodGet, "/en/groups/3/settings"},
		{http.MethodPost, "/en/groups/3/settings"},
		{http.MethodGet, "/en/groups/3/members"},
		{http.MethodPost, "/en/groups/3/members/8/roles/add"},
		{http.MethodPost, "/en/groups/3/members/8/roles/remove"},
		{http.MethodPost, "/en/groups/3/members/8/remove"},
		{http.MethodGet, "/en/groups/3/teams"},
		{http.MethodPost, "/en/groups/3/teams"},
		{http.MethodGet, "/en/groups/3/teams/4"},
		{http.MethodPost, "/en/groups/3/teams/4"},
		{http.MethodPost, "/en/groups/3/teams/4/archive"},
		{http.MethodPost, "/en/groups/3/teams/4/roster/add"},
		{http.MethodPost, "/en/groups/3/teams/4/roster/remove"},
		{http.MethodGet, "/hu/groups/3/members"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			dependencies, groups, teams := groupTestDependencies(t, nil)
			dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
			response := serveGroups(t, dependencies, route.method, route.path, "version=1&role=player&name=X&time_zone=UTC&entry=8:player", false)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", response.Code)
			}
			if strings.Contains(response.Body.String(), "Lions Club") {
				t.Fatal("not-found page leaks the group name")
			}
			if groups.calls != 0 || teams.calls != 0 {
				t.Fatalf("use cases called %d/%d times for an outsider", groups.calls, teams.calls)
			}
		})
	}
	dependencies, _, _ := groupTestDependencies(t, nil)
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/abc", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("malformed group id status = %d, want 404", response.Code)
	}
}

func TestGroupRoutesAnswerForbiddenToInsufficientRoles(t *testing.T) {
	player := groupMemberAs(service.GroupRolePlayer)
	tests := []struct {
		name   string
		method string
		path   string
		form   string
		setup  func(*groupsUseCaseStub, *teamsUseCaseStub)
	}{
		{name: "settings page", method: http.MethodGet, path: "/en/groups/3/settings"},
		{name: "settings update", method: http.MethodPost, path: "/en/groups/3/settings", form: "version=2&name=X&time_zone=UTC", setup: func(g *groupsUseCaseStub, _ *teamsUseCaseStub) { g.writeErr = service.ErrGroupForbidden }},
		{name: "role grant", method: http.MethodPost, path: "/en/groups/3/members/8/roles/add", form: "version=0&role=coach", setup: func(g *groupsUseCaseStub, _ *teamsUseCaseStub) { g.writeErr = service.ErrGroupForbidden }},
		{name: "member removal", method: http.MethodPost, path: "/en/groups/3/members/8/remove", form: "version=0", setup: func(g *groupsUseCaseStub, _ *teamsUseCaseStub) { g.writeErr = service.ErrGroupForbidden }},
		{name: "team creation", method: http.MethodPost, path: "/en/groups/3/teams", form: "name=A&birth_year_from=2015&birth_year_to=2016", setup: func(_ *groupsUseCaseStub, s *teamsUseCaseStub) { s.writeErr = service.ErrGroupForbidden }},
		{name: "roster change", method: http.MethodPost, path: "/en/groups/3/teams/4/roster/add", form: "entry=8:player", setup: func(_ *groupsUseCaseStub, s *teamsUseCaseStub) { s.writeErr = service.ErrGroupForbidden }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, htmx := range []bool{false, true} {
				dependencies, groups, teams := groupTestDependencies(t, player)
				teams.detail = service.TeamDetail{Team: service.Team{ID: 4, GroupID: 3, Name: "U10"}}
				if test.setup != nil {
					test.setup(groups, teams)
				}
				response := serveGroups(t, dependencies, test.method, test.path, test.form, htmx)
				want := http.StatusForbidden
				if htmx && test.method == http.MethodPost {
					want = http.StatusUnprocessableEntity
				}
				if response.Code != want {
					t.Fatalf("htmx=%v status = %d, want %d", htmx, response.Code, want)
				}
			}
		})
	}
}

func TestGroupOverviewByRole(t *testing.T) {
	tests := []struct {
		name         string
		access       *service.GroupAccess
		wantInvite   bool
		wantChildren bool
		wantSettings bool
		wantLeave    bool
	}{
		{name: "admin", access: groupMemberAs(service.GroupRoleAdmin), wantInvite: true, wantChildren: true, wantSettings: true, wantLeave: true},
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), wantInvite: true, wantChildren: true, wantSettings: true, wantLeave: true},
		{name: "coach", access: groupMemberAs(service.GroupRoleCoach), wantInvite: true, wantLeave: true},
		{name: "parent", access: groupMemberAs(service.GroupRoleParent), wantChildren: true, wantLeave: true},
		{name: "player", access: groupMemberAs(service.GroupRolePlayer), wantLeave: true},
		{name: "site administrator", access: groupAccessAs(service.GroupAccessGrants{SiteAdmin: true}), wantInvite: true, wantChildren: true, wantSettings: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, _ := groupTestDependencies(t, test.access)
			response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false)
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `id="group-summary-heading"`) || !strings.Contains(body, "Europe/Budapest") {
				t.Fatalf("status = %d, overview incomplete", response.Code)
			}
			checks := map[string]bool{
				`data-section="invites"`:  test.wantInvite,
				`data-section="children"`: test.wantChildren,
				`data-section="settings"`: test.wantSettings,
				"Leave group":             test.wantLeave,
			}
			for needle, want := range checks {
				if strings.Contains(body, needle) != want {
					t.Errorf("body contains %q = %v, want %v", needle, !want, want)
				}
			}
		})
	}
}

func TestGroupSettings(t *testing.T) {
	dependencies, groups, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleManager))
	page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/settings", "", false)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `<input type="hidden" name="version" value="2">`) {
		t.Fatalf("settings page = %d, want the form with the group revision", page.Code)
	}

	saved := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/settings", "version=2&name=New&time_zone=Europe%2FVienna", true)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "Group settings saved.") || !strings.Contains(saved.Body.String(), `id="alerts"`) {
		t.Fatalf("save = %d, want fragment with success alert", saved.Code)
	}
	if groups.lastVersion != 2 || groups.lastName != "New" || groups.lastZone != "Europe/Vienna" || groups.lastOrigin == "" {
		t.Fatalf("UpdateSettings got version %d name %q zone %q origin %q", groups.lastVersion, groups.lastName, groups.lastZone, groups.lastOrigin)
	}

	groups.writeErr = service.ErrOptimisticLockConflict
	if conflict := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/settings", "version=1&name=New&time_zone=UTC", false); conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "changed elsewhere") {
		t.Fatalf("stale save = %d, want 409 with reload message", conflict.Code)
	}
	groups.writeErr = service.ErrInvalidTimeZone
	invalid := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/settings", "version=2&name=New&time_zone=Mars", false)
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), `<option value="Mars" selected>`) {
		t.Fatalf("invalid zone = %d, want 422 keeping the submitted value", invalid.Code)
	}
}

func TestGroupMembersPage(t *testing.T) {
	email := "player@example.test"
	members := []service.GroupMember{
		{UserID: 7, Roles: []string{service.GroupRoleAdmin}, Kind: service.UserKindAccount, Version: 1},
		{UserID: 8, Roles: []string{service.GroupRolePlayer}, Kind: service.UserKindGuest, Email: &email, Version: 4},
	}
	dependencies, groups, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	groups.members = members
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/members", "", false).Body.String()
	for _, want := range []string{"player@example.test", `/en/groups/3/members/8/roles/add`, `value="4"`, `<option value="parent">Parent</option>`, "Guest", "Member #7"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin members page missing %q", want)
		}
	}
	// A guest is never offered a staff role.
	guestRow := body[strings.Index(body, `data-member-id="8"`):]
	if strings.Contains(guestRow, `<option value="coach">`) {
		t.Error("guest row offers the coach role")
	}
	// The admin's own row cannot remove itself.
	selfRow := body[strings.Index(body, `data-member-id="7"`):strings.Index(body, `data-member-id="8"`)]
	if strings.Contains(selfRow, "/members/7/remove") {
		t.Error("own row offers removal")
	}

	dependencies, groups, _ = groupTestDependencies(t, groupMemberAs(service.GroupRolePlayer))
	groups.members = []service.GroupMember{{UserID: 7, Roles: []string{service.GroupRolePlayer}}}
	body = serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/members", "", false).Body.String()
	if strings.Contains(body, "/roles/add") || !strings.Contains(body, "members of your teams") {
		t.Error("player members page must be read only and explain the limited list")
	}
}

func TestGroupRoleChanges(t *testing.T) {
	dependencies, groups, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	added := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/members/8/roles/add", "version=4&role=parent", true)
	if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), "Role added.") || groups.lastRole != "parent" || groups.lastTarget != 8 || groups.lastVersion != 4 {
		t.Fatalf("add role = %d (role %q target %d version %d)", added.Code, groups.lastRole, groups.lastTarget, groups.lastVersion)
	}
	removed := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/members/8/roles/remove", "version=5&role=parent", true)
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), "Role removed.") {
		t.Fatalf("remove role = %d", removed.Code)
	}

	for _, test := range []struct {
		err    error
		status int
		text   string
	}{
		{service.ErrLastGroupAdmin, http.StatusUnprocessableEntity, "at least one administrator"},
		{service.ErrRoleNeedsAccount, http.StatusUnprocessableEntity, "need a signed-in account"},
		{service.ErrOptimisticLockConflict, http.StatusConflict, "changed elsewhere"},
		{service.ErrMembershipNotFound, http.StatusNotFound, "no longer in this group"},
		{service.ErrInvalidGroupRole, http.StatusUnprocessableEntity, "valid role"},
	} {
		groups.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/members/8/roles/remove", "version=5&role=admin", false)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
			t.Errorf("error %v = %d, want %d with %q", test.err, response.Code, test.status, test.text)
		}
	}
}

func TestGroupWritesRequireARevision(t *testing.T) {
	for _, test := range []struct{ name, path, body string }{
		{"role without version", "/en/groups/3/members/8/roles/add", "role=coach"},
		{"role with malformed version", "/en/groups/3/members/8/roles/remove", "role=coach&version=latest"},
		{"remove without version", "/en/groups/3/members/8/remove", ""},
		{"settings without version", "/en/groups/3/settings", "name=X&time_zone=UTC"},
		{"team without version", "/en/groups/3/teams/4", "name=X&birth_year_from=2015&birth_year_to=2016"},
		{"archive without version", "/en/groups/3/teams/4/archive", "archived=true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The stubs would succeed, so only the missing revision can refuse.
			dependencies, groups, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
			teams.detail = service.TeamDetail{Team: service.Team{ID: 4, GroupID: 3, Name: "U10"}}
			response := serveGroups(t, dependencies, http.MethodPost, test.path, test.body, true)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "changed elsewhere") {
				t.Fatalf("status = %d, want 422 with the reload message", response.Code)
			}
			if groups.writes != 0 || teams.writes != 0 {
				t.Fatal("write reached the use case without a revision")
			}
		})
	}
}

func TestGroupsJoin(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.JoinGroup = &rateLimitedJoinStub{err: service.RateLimitError{RetryAfter: 30}}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/join", "code=ABCDEFGHJKLM", false); response.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited join status = %d, want 429", response.Code)
	}
	dependencies.JoinGroup = &rateLimitedJoinStub{err: service.ErrGroupInviteNotFound}
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/join", "code=UNKNOWN", false)
	if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), "UNKNOWN") {
		t.Fatalf("invalid code status = %d, want 422 without echoing the code", response.Code)
	}
	dependencies.JoinGroup = &rateLimitedJoinStub{redemption: service.InviteRedemption{Membership: service.GroupMembership{GroupID: 3}}}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/join", "code=ABCDEFGHJKLM", false); response.Header().Get("Location") != "/en/groups/3" {
		t.Fatalf("join redirect = %q, want /en/groups/3", response.Header().Get("Location"))
	}
	// A team invite lands on the team page.
	dependencies.JoinGroup = &rateLimitedJoinStub{redemption: service.InviteRedemption{Membership: service.GroupMembership{GroupID: 3}, TeamID: 4}}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/join", "code=ABCDEFGHJKLM", false); response.Header().Get("Location") != "/en/groups/3/teams/4" {
		t.Fatalf("team join redirect = %q, want /en/groups/3/teams/4", response.Header().Get("Location"))
	}
	for _, test := range []struct {
		err  error
		want string
	}{
		{service.ErrInviteExpired, "has expired"},
		{service.ErrInviteUsedUp, "used up"},
		{service.ErrInviteRevoked, "was revoked"},
		{service.ErrRoleNeedsAccount, "Upgrade to a full account"},
	} {
		dependencies.JoinGroup = &rateLimitedJoinStub{err: test.err}
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/join", "code=ABCDEFGHJKLM", false)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.want) {
			t.Fatalf("join error %v = %d, want 422 with %q", test.err, response.Code, test.want)
		}
	}
}

func TestGroupsLeaveRedirectsToList(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRolePlayer))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/leave", "", false)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en/groups" {
		t.Fatalf("leave = %d %q, want 303 /en/groups", response.Code, response.Header().Get("Location"))
	}
	htmx := serveGroups(t, dependencies, http.MethodPost, "/en/groups/3/leave", "", true)
	if htmx.Code != http.StatusOK || htmx.Header().Get("HX-Redirect") != "/en/groups" {
		t.Fatalf("HTMX leave = %d %q, want HX-Redirect /en/groups", htmx.Code, htmx.Header().Get("HX-Redirect"))
	}
}

// TestGroupTemplateLabelsExist checks that every .L label the group and
// team templates use resolves in every language, since a missing map key
// renders silently.
func TestGroupTemplateLabelsExist(t *testing.T) {
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v", err)
	}
	labels := groupLabels(renderer)
	pattern := regexp.MustCompile(`[.$]L\.((?:groups|teams|invites|children|resources|seasons)_[a-z0-9_]+)`)
	for _, name := range []string{"groups.html", "teams.html", "invites.html", "children.html", "resources.html", "seasons.html"} {
		source, err := os.ReadFile(filepath.Join("..", "..", "..", "templates", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			for _, language := range locale.Codes() {
				if labels[language][match[1]] == "" {
					t.Errorf("%s uses %s, missing in %s", name, match[1], language)
				}
			}
		}
	}
}

func newGroupsTestHandler(t *testing.T, dependencies Dependencies) (*groupHandler, *PageRenderer) {
	t.Helper()
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v", err)
	}
	return newGroupHandler(renderer, dependencies), renderer
}

type groupAccessStub struct {
	access *service.GroupAccess
	err    error
}

func (s *groupAccessStub) For(context.Context, int64, int64) (*service.GroupAccess, error) {
	return s.access, s.err
}

type groupsUseCaseStub struct {
	summaries   []service.GroupSummary
	summary     service.GroupSummary
	group       service.Group
	overview    service.GroupOverview
	members     []service.GroupMember
	createErr   error
	writeErr    error
	calls       int
	writes      int
	createdZone string
	lastVersion int64
	lastName    string
	lastZone    string
	lastOrigin  string
	lastRole    string
	lastTarget  int64
}

func (s *groupsUseCaseStub) Create(_ context.Context, _ int64, _, _, timeZone string) (service.GroupSummary, error) {
	s.createdZone = timeZone
	return s.summary, s.createErr
}

func (s *groupsUseCaseStub) List(context.Context, int64) ([]service.GroupSummary, error) {
	return s.summaries, nil
}

func (s *groupsUseCaseStub) Overview(context.Context, *service.GroupAccess) (service.GroupOverview, error) {
	s.calls++
	return s.overview, nil
}

func (s *groupsUseCaseStub) Group(context.Context, *service.GroupAccess) (service.Group, error) {
	s.calls++
	return s.group, nil
}

func (s *groupsUseCaseStub) write() error {
	s.calls++
	s.writes++
	return s.writeErr
}

func (s *groupsUseCaseStub) UpdateSettings(_ context.Context, actor service.GroupActor, version int64, name, timeZone string) (service.Group, error) {
	s.lastVersion, s.lastName, s.lastZone, s.lastOrigin = version, name, timeZone, actor.Origin
	return s.group, s.write()
}

func (s *groupsUseCaseStub) Leave(context.Context, *service.GroupAccess) error {
	return s.write()
}

func (s *groupsUseCaseStub) Members(context.Context, *service.GroupAccess) ([]service.GroupMember, error) {
	s.calls++
	return s.members, nil
}

func (s *groupsUseCaseStub) AddRole(_ context.Context, _ service.GroupActor, target, version int64, role string) (service.GroupMembership, error) {
	s.lastTarget, s.lastVersion, s.lastRole = target, version, role
	return service.GroupMembership{}, s.write()
}

func (s *groupsUseCaseStub) RemoveRole(_ context.Context, _ service.GroupActor, target, version int64, role string) (service.GroupMembership, error) {
	s.lastTarget, s.lastVersion, s.lastRole = target, version, role
	return service.GroupMembership{}, s.write()
}

func (s *groupsUseCaseStub) RemoveMember(_ context.Context, _ service.GroupActor, target, version int64) error {
	s.lastTarget, s.lastVersion = target, version
	return s.write()
}

type rateLimitedJoinStub struct {
	redemption service.InviteRedemption
	err        error
}

func (s *rateLimitedJoinStub) Join(context.Context, int64, string) (service.InviteRedemption, error) {
	if s.err != nil {
		return service.InviteRedemption{}, s.err
	}
	return s.redemption, nil
}
