package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

func TestSignedInHomeListsGroupsAndShortcuts(t *testing.T) {
	for _, path := range []string{"/en/", "/en/home"} {
		t.Run(path, func(t *testing.T) {
			dependencies := newHTTPTestDependencies(t)
			dependencies.Groups = &groupsUseCaseStub{summaries: []service.GroupSummary{
				{Group: service.Group{ID: 3, Slug: "lions", Name: "Lions Club"}, MemberCount: 2},
			}}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(&http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})
			response := httptest.NewRecorder()
			NewRouterWithServices(dependencies).ServeHTTP(response, request)

			body := response.Body.String()
			for _, want := range []string{
				`href="/en/groups/lions"`, "Lions Club",
				`href="/en/groups#groups-create-heading"`, `href="/en/groups#groups-join-heading"`,
				`id="mobile-menu"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("signed-in home missing %q", want)
				}
			}
			if strings.Contains(body, `class="btn btn-ghost" href="/en/admin"`) {
				t.Error("non-admin home links to admin")
			}
			if strings.Contains(body, "application foundation") {
				t.Error("home still shows the foundation placeholder")
			}
		})
	}
}

func TestAnonymousHomeShowsIntroWithoutGroupLinks(t *testing.T) {
	response := httptest.NewRecorder()
	NewRouterWithServices(newHTTPTestDependencies(t)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/", nil))

	body := response.Body.String()
	if !strings.Contains(body, "Sign in to get started") || !strings.Contains(body, `href="/en/sign-in"`) {
		t.Error("anonymous home missing intro or sign-in link")
	}
	for _, unwanted := range []string{"groups-create-heading", "groups-join-heading", `id="home-groups"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("anonymous home contains %q", unwanted)
		}
	}
}
