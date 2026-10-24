package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/locale"
)

// TestAreaRoutesRegistered checks that every area's register function adds
// its routes for each supported language, so a route never silently falls
// through to the localized not-found page.
func TestAreaRoutesRegistered(t *testing.T) {
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v", err)
	}
	mux := http.NewServeMux()
	dependencies := Dependencies{}
	auth := newAuthHandler(renderer, dependencies)
	registerCoreRoutes(mux, renderer, auth)
	registerAboutRoutes(mux, renderer, auth)
	registerAuthRoutes(mux, auth)
	registerGuestRoutes(mux, auth)
	registerSettingsRoutes(mux, newSettingsHandler(renderer, dependencies))
	registerGroupRoutes(mux, newGroupHandler(renderer, dependencies))
	registerAdminRoutes(mux, newAdminHandler(renderer, dependencies))

	localizedRoutes := []struct {
		method string
		route  string
	}{
		{http.MethodGet, "/home"},
		{http.MethodGet, "/about"},
		{http.MethodPost, "/sign-out"},
		{http.MethodGet, "/sign-in"},
		{http.MethodGet, "/oauth/sso/callback"},
		{http.MethodPost, "/guest/upgrade"},
		{http.MethodPost, "/guest/write"},
		{http.MethodGet, "/guest/transfer"},
		{http.MethodGet, "/account/settings"},
		{http.MethodPost, "/account/theme"},
		{http.MethodGet, "/groups"},
		{http.MethodPost, "/groups/7/members/9/roles/add"},
		{http.MethodGet, "/groups/7/settings"},
		{http.MethodGet, "/groups/7/teams/4"},
		{http.MethodPost, "/groups/7/teams/4/roster/remove"},
		{http.MethodGet, "/groups/7/resources"},
		{http.MethodPost, "/groups/7/resources/4/archive"},
		{http.MethodGet, "/groups/7/seasons"},
		{http.MethodPost, "/groups/7/seasons/4/delete"},
		{http.MethodPost, "/groups/7/teams/4/coaches/add"},
		{http.MethodGet, "/admin/users/3/edit"},
		{http.MethodPost, "/admin/ratelimits/clear"},
	}
	for _, language := range locale.Codes() {
		for _, test := range localizedRoutes {
			path := "/" + string(language) + test.route
			t.Run(test.method+" "+path, func(t *testing.T) {
				_, pattern := mux.Handler(httptest.NewRequest(test.method, path, nil))
				want := test.method + " /" + string(language)
				if !strings.HasPrefix(pattern, want) {
					t.Errorf("pattern = %q, want an area route starting with %q", pattern, want)
				}
			})
		}
	}

	for _, test := range []struct{ method, path, want string }{
		{http.MethodPost, ssoBackChannelLogoutPath, "POST " + ssoBackChannelLogoutPath},
		{http.MethodGet, "/healthz", "GET /healthz"},
		{http.MethodGet, "/en/unknown", "GET /{language}/{path...}"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			if _, pattern := mux.Handler(httptest.NewRequest(test.method, test.path, nil)); pattern != test.want {
				t.Errorf("pattern = %q, want %q", pattern, test.want)
			}
		})
	}
}

func TestLocalizedPrefixes(t *testing.T) {
	prefixes := localizedPrefixes()
	codes := locale.Codes()
	if len(prefixes) != len(codes) {
		t.Fatalf("localizedPrefixes() = %v, want one per language %v", prefixes, codes)
	}
	for index, code := range codes {
		if prefixes[index] != "/"+string(code) {
			t.Errorf("prefix %d = %q, want %q", index, prefixes[index], "/"+string(code))
		}
	}
}
