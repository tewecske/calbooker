package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

func TestSettingsPageRequiresAuthentication(t *testing.T) {
	handler, _ := newSettingsTestHandler(t, settingsStubDependencies(t))

	request := httptest.NewRequest(http.MethodGet, "/en/account/settings", nil)
	response := httptest.NewRecorder()
	handler.page(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if location := response.Header().Get("Location"); location != "/en/sign-in" {
		t.Fatalf("redirect = %q, want /en/sign-in", location)
	}
}

func TestSettingsPageRendersAccountData(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, Email: strPtr("alice@example.test"), DisplayName: strPtr("Alice"), Locale: "en", Theme: "dark", Version: 1}}
	dependencies.SSOAccountURL = "http://localhost:8080/en/account/settings"
	handler, _ := newSettingsTestHandler(t, dependencies)

	request := authenticatedRequest(http.MethodGet, "/en/account/settings", 7, "")
	response := httptest.NewRecorder()
	handler.page(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, want := range []string{"alice@example.test", `href="http://localhost:8080/en/account/settings"`, `action="/en/account/settings/locale"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
	// The provider owns credentials and profile data; none of their forms
	// belong here.
	for _, unwanted := range []string{`name="password"`, `name="new_password"`, `/account/settings/profile`, `/account/settings/identity`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("settings page renders provider-owned control %q", unwanted)
		}
	}
}

func TestSettingsLocaleUpdateRedirectsToSelectedLanguage(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, Locale: "en", Version: 1}}
	dependencies.LocaleSettings = settingsLocaleUpdater{}
	handler, _ := newSettingsTestHandler(t, dependencies)

	request := authenticatedRequest(http.MethodPost, "/en/account/settings/locale", 7, "locale=hu")
	response := httptest.NewRecorder()
	handler.updateLocale(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if location := response.Header().Get("Location"); location != "/hu/account/settings" {
		t.Fatalf("redirect = %q, want /hu/account/settings", location)
	}
}

func TestSettingsLocaleUpdateRejectsUnsupported(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, Locale: "en", Version: 1}}
	dependencies.LocaleSettings = settingsLocaleUpdater{err: service.ErrInvalidLocale}
	handler, _ := newSettingsTestHandler(t, dependencies)

	request := authenticatedRequest(http.MethodPost, "/en/account/settings/locale", 7, "locale=de")
	response := httptest.NewRecorder()
	handler.updateLocale(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(response.Body.String(), "available languages") {
		t.Errorf("body missing invalid locale message")
	}
}

func TestSettingsThemeUpdate(t *testing.T) {
	tests := []struct {
		name           string
		updater        AccountThemeUpdater
		body           string
		expectedStatus int
	}{
		{name: "success", updater: settingsThemeUpdater{}, body: "theme=dark", expectedStatus: http.StatusNoContent},
		{name: "invalid theme", updater: settingsThemeUpdater{err: service.ErrInvalidTheme}, body: "theme=blue", expectedStatus: http.StatusUnprocessableEntity},
		{name: "missing dependency", updater: nil, body: "theme=dark", expectedStatus: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := settingsStubDependencies(t)
			dependencies.Users = testUserFinder{user: service.User{ID: 7, Version: 1}}
			if test.updater != nil {
				dependencies.ThemeUpdater = test.updater
			}
			handler, _ := newSettingsTestHandler(t, dependencies)
			request := authenticatedRequest(http.MethodPost, "/en/account/theme", 7, test.body)
			response := httptest.NewRecorder()
			handler.updateTheme(response, request)
			if response.Code != test.expectedStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.expectedStatus)
			}
		})
	}
}

func TestSettingsFragmentResponseForHTMX(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, Username: strPtr("alice"), Locale: "en", Version: 1}}
	handler, _ := newSettingsTestHandler(t, dependencies)

	request := authenticatedRequest(http.MethodGet, "/en/account/settings", 7, "")
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.page(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if strings.Contains(body, "<!doctype html>") {
		t.Errorf("HTMX request returned a full document")
	}
	if !strings.Contains(body, `id="page-content"`) {
		t.Errorf("HTMX fragment missing page-content region")
	}
}

// TestSettingsFragmentReplacesHeadingRegion guards against the HTMX fragment
// introducing a second page heading: the full page and the fragment must each
// expose exactly one #page-content swap target and one #page-heading, with the
// heading nested inside the swap target so an outerHTML swap stays idempotent.
func TestSettingsFragmentReplacesHeadingRegion(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, Username: strPtr("alice"), Locale: "en", Version: 1}}
	handler, _ := newSettingsTestHandler(t, dependencies)

	full := httptest.NewRecorder()
	handler.page(full, authenticatedRequest(http.MethodGet, "/en/account/settings", 7, ""))

	fragment := httptest.NewRecorder()
	request := authenticatedRequest(http.MethodGet, "/en/account/settings", 7, "")
	request.Header.Set("HX-Request", "true")
	handler.page(fragment, request)

	responses := map[string]string{"full page": full.Body.String(), "fragment": fragment.Body.String()}
	for name, body := range responses {
		if got := strings.Count(body, `id="page-content"`); got != 1 {
			t.Errorf("%s has %d #page-content swap targets, want 1", name, got)
		}
		if got := strings.Count(body, `id="page-heading"`); got != 1 {
			t.Errorf("%s has %d #page-heading elements, want 1", name, got)
		}
		content := strings.Index(body, `id="page-content"`)
		heading := strings.Index(body, `id="page-heading"`)
		if content < 0 || heading < content {
			t.Errorf("%s renders the page heading outside the #page-content swap target", name)
		}
	}
}

func TestSettingsHTMXUnauthenticatedRedirect(t *testing.T) {
	handler, _ := newSettingsTestHandler(t, settingsStubDependencies(t))
	request := httptest.NewRequest(http.MethodGet, "/en/account/settings", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	handler.page(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("HX-Redirect"); got != "/en/sign-in" {
		t.Fatalf("HX-Redirect = %q, want /en/sign-in", got)
	}
}

func newSettingsTestHandler(t *testing.T, dependencies Dependencies) (*settingsHandler, *PageRenderer) {
	t.Helper()
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v", err)
	}
	return newSettingsHandler(renderer, dependencies), renderer
}

func settingsStubDependencies(t *testing.T) Dependencies {
	t.Helper()
	return Dependencies{
		Users:         testUserFinder{user: service.User{ID: 7, Version: 1}},
		Sessions:      testSessionLookup{session: service.Session{ID: "opaque-session", UserID: 7, CreatedAt: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}},
		SessionCookie: testSessionCookie(t),
	}
}

func authenticatedRequest(method, target string, _ int64, form string) *http.Request {
	body := strings.NewReader(form)
	request := httptest.NewRequest(method, target, body)
	if form != "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	segments := strings.Split(strings.Trim(strings.SplitN(target, "?", 2)[0], "/"), "/")
	if len(segments) >= 3 && segments[1] == "groups" {
		request.SetPathValue("group", segments[2])
		if len(segments) >= 5 && segments[3] == "teams" {
			request.SetPathValue("team", segments[4])
		}
	}
	if len(segments) >= 5 && segments[3] == "members" {
		request.SetPathValue("memberID", segments[4])
	}
	request.AddCookie(&http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})
	return request
}

type settingsLocaleUpdater struct{ err error }

func (u settingsLocaleUpdater) Update(context.Context, int64, string) (service.User, error) {
	if u.err != nil {
		return service.User{}, u.err
	}
	return service.User{ID: 7}, nil
}

type settingsThemeUpdater struct{ err error }

func (u settingsThemeUpdater) UpdateTheme(context.Context, int64, string) error { return u.err }

var errSettingsUnexpected = errors.New("unexpected")

func strPtr(value string) *string { return &value }
