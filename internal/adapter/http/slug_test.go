package httpadapter

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestGroupSlugResolution(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions", "", false); response.Code != http.StatusOK {
		t.Fatalf("known slug status = %d, want 200", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/lions/members", "", false); response.Code != http.StatusOK {
		t.Fatalf("known slug below a section status = %d, want 200", response.Code)
	}
	for _, slug := range []string{"tigers", "Lions", "new", "a-b"} {
		response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/"+slug, "", false)
		if response.Code != http.StatusNotFound {
			t.Errorf("slug %q status = %d, want 404", slug, response.Code)
		}
		if strings.Contains(response.Body.String(), "Lions Club") {
			t.Errorf("not-found page for %q leaks the group name", slug)
		}
	}
}

func TestGroupSlugOfAGroupTheAccountMayNotSeeIsNotFound(t *testing.T) {
	dependencies, groups, teams := groupTestDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	for _, target := range []string{"/en/groups/lions", "/en/groups/lions/teams/t4"} {
		response := serveGroups(t, dependencies, http.MethodGet, target, "", false)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Lions Club") {
			t.Fatalf("%s status = %d, want 404 without the group name", target, response.Code)
		}
	}
	if groups.calls != 0 || teams.calls != 0 {
		t.Fatalf("use cases called %d/%d times for an outsider", groups.calls, teams.calls)
	}
}

func TestOldNumericGroupURLsRedirectPermanently(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	tests := []struct{ from, to string }{
		{"/en/groups/3", "/en/groups/lions"},
		{"/hu/groups/3/members", "/hu/groups/lions/members"},
		{"/en/groups/3/plan?part=2&week=A", "/en/groups/lions/plan?part=2&week=A"},
		{"/en/groups/3/events/5", "/en/groups/lions/events/5"},
		{"/en/groups/3/seasons/2/coach-board", "/en/groups/lions/seasons/2/coach-board"},
	}
	for _, test := range tests {
		response := serveGroups(t, dependencies, http.MethodGet, test.from, "", false)
		if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != test.to {
			t.Errorf("%s = %d %q, want 301 %s", test.from, response.Code, response.Header().Get("Location"), test.to)
		}
	}
}

func TestOldNumericURLsDoNotRedirectWrites(t *testing.T) {
	dependencies, groups, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	for _, target := range []string{"/en/groups/3/settings", "/en/groups/3/teams", "/en/groups/lions/teams/4/archive", "/en/groups/3/teams/t4/archive"} {
		response := serveGroups(t, dependencies, http.MethodPost, target, "version=1&name=X&archived=true", false)
		if response.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", target, response.Code)
		}
	}
	if groups.writes != 0 || teams.writes != 0 {
		t.Fatalf("writes = %d/%d through old URLs, want none", groups.writes, teams.writes)
	}
}

func TestOldNumericGroupURLDoesNotRevealTheSlugToOutsiders(t *testing.T) {
	dependencies, _, _ := groupTestDependencies(t, nil)
	dependencies.GroupAccess = &groupAccessStub{err: service.ErrGroupNotFound}
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/3", "", false)
	if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
		t.Fatalf("outsider status = %d, Location %q, want 404 without a redirect", response.Code, response.Header().Get("Location"))
	}
}

func TestTeamSlugResolutionAndOldNumericRedirect(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "U10"}}
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false); response.Code != http.StatusOK {
		t.Fatalf("known team slug status = %d, want 200", response.Code)
	}
	if response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/unknown", "", false); response.Code != http.StatusNotFound {
		t.Fatalf("unknown team slug status = %d, want 404", response.Code)
	}
	response := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/4", "", false)
	if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != "/en/groups/lions/teams/t4" {
		t.Fatalf("numeric team = %d %q, want 301 /en/groups/lions/teams/t4", response.Code, response.Header().Get("Location"))
	}
	// Both parts numeric: the group redirects first, then the team.
	response = serveGroups(t, dependencies, http.MethodGet, "/en/groups/3/teams/4", "", false)
	if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != "/en/groups/lions/teams/4" {
		t.Fatalf("numeric group and team = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestGroupSettingsSlugEdits(t *testing.T) {
	dependencies, groups, _ := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	form := "version=2&name=Lions+Club&time_zone=Europe%2FBudapest&slug="

	page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/settings", "", false).Body.String()
	if !strings.Contains(page, `id="group-settings-slug"`) || !strings.Contains(page, `value="lions"`) {
		t.Fatal("settings page has no slug field with the current slug")
	}

	// A new slug moves the page: the response sends the browser there.
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/settings", form+"lions-fc", false)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en/groups/lions-fc/settings" || groups.lastSlug != "lions-fc" {
		t.Fatalf("slug change = %d %q (slug %q), want 303 to the new settings URL", response.Code, response.Header().Get("Location"), groups.lastSlug)
	}
	htmx := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/settings", form+"lions-fc", true)
	if htmx.Header().Get("HX-Redirect") != "/en/groups/lions-fc/settings" {
		t.Fatalf("HTMX slug change HX-Redirect = %q", htmx.Header().Get("HX-Redirect"))
	}

	// Saving without a slug change stays on the page with the success alert.
	response = serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/settings", form+"lions", false)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Group settings saved.") {
		t.Fatalf("unchanged slug = %d, want 200 with the saved alert", response.Code)
	}

	for _, test := range []struct {
		err  error
		want string
	}{
		{service.ErrSlugTaken, "Another group already uses that link name."},
		{service.ErrInvalidSlug, "Use lowercase letters, digits, and single hyphens"},
	} {
		groups.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/settings", form+"x", false)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.want) {
			t.Errorf("%v = %d, want 422 with %q", test.err, response.Code, test.want)
		}
	}
}

func TestTeamSlugEdits(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "U10", BirthYearFrom: 2015, BirthYearTo: 2016, Version: 6}}
	form := "version=6&name=U10&birth_year_from=2015&birth_year_to=2016&slug="

	page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false).Body.String()
	if !strings.Contains(page, `id="team-edit-slug"`) {
		t.Fatal("team page has no slug field")
	}
	newTeam := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams", "", false).Body.String()
	if strings.Contains(newTeam, `id="team-new-slug"`) {
		t.Fatal("create form asks for a slug; it is derived from the name")
	}

	teams.detail.Team.Slug = "u10"
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4", form+"u10", false)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en/groups/lions/teams/u10" || teams.lastInput.Slug != "u10" {
		t.Fatalf("slug change = %d %q (input slug %q), want 303 to the new team URL", response.Code, response.Header().Get("Location"), teams.lastInput.Slug)
	}

	for _, test := range []struct {
		err  error
		want string
	}{
		{service.ErrSlugTaken, "Another team in this group already uses that link name."},
		{service.ErrInvalidSlug, "Use lowercase letters, digits, and single hyphens"},
	} {
		teams.writeErr = test.err
		response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4", form+"x", false)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.want) {
			t.Errorf("%v = %d, want 422 with %q", test.err, response.Code, test.want)
		}
	}
}

func TestGroupPagesLinkBySlug(t *testing.T) {
	dependencies, _, teams := groupTestDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	teams.summaries = []service.TeamSummary{{Team: service.Team{ID: 4, Slug: "u10", GroupID: 3, Name: "U10", BirthYearFrom: 2015, BirthYearTo: 2016}}}
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams", "", false).Body.String()
	for _, want := range []string{`href="/en/groups/lions/teams/u10"`, `href="/en/groups/lions/members"`} {
		if !strings.Contains(body, want) {
			t.Errorf("team list missing %q", want)
		}
	}
	for _, numeric := range []string{"/groups/3", "/teams/4"} {
		if strings.Contains(body, numeric) {
			t.Errorf("team list still links %q", numeric)
		}
	}
}
