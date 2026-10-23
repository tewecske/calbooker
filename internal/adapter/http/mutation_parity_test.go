package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

// mutationRequest builds an authenticated mutation request in either full-page
// or HTMX form.
func mutationRequest(method, target, form string, htmx bool, configure func(*http.Request)) *http.Request {
	request := authenticatedRequest(method, target, 7, form)
	if configure != nil {
		configure(request)
	}
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	return request
}

// runBothModes executes the same handler for a normal browser request and an
// in-page HTMX request so their behavior can be compared directly.
func runBothModes(handler http.HandlerFunc, build func(htmx bool) *http.Request) (full, htmx *httptest.ResponseRecorder) {
	full = httptest.NewRecorder()
	handler(full, build(false))
	htmx = httptest.NewRecorder()
	handler(htmx, build(true))
	return full, htmx
}

// assertMutationParity verifies status parity, document shape, and that the
// same message is reachable for full-page and HTMX responses.
func assertMutationParity(t *testing.T, name string, full, htmx *httptest.ResponseRecorder, message string) {
	t.Helper()
	if full.Code != htmx.Code {
		t.Fatalf("%s status: full = %d, HTMX = %d, want equal", name, full.Code, htmx.Code)
	}
	fullBody, htmxBody := full.Body.String(), htmx.Body.String()
	if !strings.Contains(fullBody, "<!doctype html>") {
		t.Errorf("%s full response is not a complete document", name)
	}
	if strings.Contains(htmxBody, "<!doctype html>") {
		t.Errorf("%s HTMX response contains a complete document", name)
	}
	if !strings.Contains(htmxBody, `id="page-content"`) {
		t.Errorf("%s HTMX response missing page content swap target", name)
	}
	if message != "" {
		if !strings.Contains(fullBody, message) {
			t.Errorf("%s full response missing %q", name, message)
		}
		if !strings.Contains(htmxBody, message) {
			t.Errorf("%s HTMX response missing %q", name, message)
		}
	}
}

func TestSettingsMutationAuthenticationParity(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.ThemeUpdater = settingsThemeUpdater{}
	handler, _ := newSettingsTestHandler(t, dependencies)

	tests := []struct {
		name   string
		target string
		handle http.HandlerFunc
	}{
		{name: "locale", target: "/en/account/settings/locale", handle: handler.updateLocale},
		{name: "theme", target: "/en/account/theme", handle: handler.updateTheme},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			full := httptest.NewRecorder()
			test.handle(full, httptest.NewRequest(http.MethodPost, test.target, nil))
			htmx := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, test.target, nil)
			request.Header.Set("HX-Request", "true")
			test.handle(htmx, request)

			if full.Code != http.StatusSeeOther || full.Header().Get("Location") != "/en/sign-in" {
				t.Fatalf("full unauthenticated = %d %q, want 303 /en/sign-in", full.Code, full.Header().Get("Location"))
			}
			if htmx.Code != http.StatusOK || htmx.Header().Get("HX-Redirect") != "/en/sign-in" {
				t.Fatalf("HTMX unauthenticated = %d %q, want 200 HX-Redirect /en/sign-in", htmx.Code, htmx.Header().Get("HX-Redirect"))
			}
		})
	}
}

func TestGroupMutationParity(t *testing.T) {
	t.Run("create redirect", func(t *testing.T) {
		dependencies := settingsStubDependencies(t)
		dependencies.Groups = &groupsUseCaseStub{summary: service.GroupSummary{Group: service.Group{ID: 3, Name: "Team"}, Role: service.GroupRoleAdmin}}
		handler, _ := newGroupsTestHandler(t, dependencies)

		full, htmx := runBothModes(handler.create, func(htmx bool) *http.Request {
			return mutationRequest(http.MethodPost, "/en/groups", "name=Team", htmx, nil)
		})
		if full.Code != http.StatusSeeOther || full.Header().Get("Location") != "/en/groups/3" {
			t.Fatalf("full create = %d %q, want 303 /en/groups/3", full.Code, full.Header().Get("Location"))
		}
		if htmx.Code != http.StatusOK || htmx.Header().Get("HX-Redirect") != "/en/groups/3" {
			t.Fatalf("HTMX create = %d %q, want 200 HX-Redirect /en/groups/3", htmx.Code, htmx.Header().Get("HX-Redirect"))
		}
		if htmx.Header().Get("Location") != "" {
			t.Fatalf("HTMX create also set Location = %q", htmx.Header().Get("Location"))
		}
	})

	t.Run("create validation", func(t *testing.T) {
		dependencies := settingsStubDependencies(t)
		dependencies.Groups = &groupsUseCaseStub{createErr: service.ErrInvalidGroupName}
		handler, _ := newGroupsTestHandler(t, dependencies)

		full, htmx := runBothModes(handler.create, func(htmx bool) *http.Request {
			return mutationRequest(http.MethodPost, "/en/groups", "name=", htmx, nil)
		})
		if full.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d", full.Code, http.StatusUnprocessableEntity)
		}
		assertMutationParity(t, "create validation", full, htmx, "64")
	})

	t.Run("rename conflict", func(t *testing.T) {
		dependencies := settingsStubDependencies(t)
		dependencies.Groups = &groupsUseCaseStub{
			detail:    service.GroupDetail{Group: service.Group{ID: 3, Name: "Team", Version: 2}, IsMember: true, IsAdmin: true},
			renameErr: service.ErrOptimisticLockConflict,
		}
		handler, _ := newGroupsTestHandler(t, dependencies)

		full, htmx := runBothModes(handler.rename, func(htmx bool) *http.Request {
			return mutationRequest(http.MethodPost, "/en/groups/3/rename", "name=New&version=1", htmx, nil)
		})
		if full.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d", full.Code, http.StatusConflict)
		}
		assertMutationParity(t, "rename conflict", full, htmx, "changed elsewhere")
	})

	t.Run("not-found", func(t *testing.T) {
		dependencies := settingsStubDependencies(t)
		dependencies.Groups = &groupsUseCaseStub{detailErr: service.ErrGroupNotFound, leaveErr: service.ErrGroupNotFound}
		handler, _ := newGroupsTestHandler(t, dependencies)

		full, htmx := runBothModes(handler.leave, func(htmx bool) *http.Request {
			return mutationRequest(http.MethodPost, "/en/groups/3/leave", "", htmx, nil)
		})
		if full.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", full.Code, http.StatusNotFound)
		}
		assertMutationParity(t, "leave not-found", full, htmx, "")
	})

	t.Run("leave redirect", func(t *testing.T) {
		dependencies := settingsStubDependencies(t)
		dependencies.Groups = &groupsUseCaseStub{}
		handler, _ := newGroupsTestHandler(t, dependencies)

		full, htmx := runBothModes(handler.leave, func(htmx bool) *http.Request {
			return mutationRequest(http.MethodPost, "/en/groups/3/leave", "", htmx, nil)
		})
		if full.Code != http.StatusSeeOther || full.Header().Get("Location") != "/en/groups" {
			t.Fatalf("full leave = %d %q, want 303 /en/groups", full.Code, full.Header().Get("Location"))
		}
		if htmx.Code != http.StatusOK || htmx.Header().Get("HX-Redirect") != "/en/groups" {
			t.Fatalf("HTMX leave = %d %q, want 200 HX-Redirect /en/groups", htmx.Code, htmx.Header().Get("HX-Redirect"))
		}
	})
}

func TestAdminEditMutationParity(t *testing.T) {
	withID := func(request *http.Request) { request.SetPathValue("id", "9") }

	tests := []struct {
		name    string
		stub    *adminAccountEditorStub
		admin   bool
		status  int
		message string
	}{
		{name: "conflict", stub: &adminAccountEditorStub{found: service.User{ID: 9, Version: 1}, updateErr: service.ErrOptimisticLockConflict}, admin: true, status: http.StatusConflict, message: "changed elsewhere"},
		{name: "not-found", stub: &adminAccountEditorStub{found: service.User{ID: 9, Version: 1}, updateErr: service.ErrRecordNotFound}, admin: true, status: http.StatusNotFound, message: ""},
		{name: "forbidden", stub: &adminAccountEditorStub{}, admin: false, status: http.StatusForbidden, message: "Access denied"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := adminStubDependencies(t, test.admin)
			dependencies.AdminAccountEditor = test.stub
			handler := newAdminTestHandler(t, dependencies)

			full, htmx := runBothModes(handler.editAccount, func(htmx bool) *http.Request {
				return mutationRequest(http.MethodPost, "/en/admin/users/9/edit", "email=a@example.test&version=1", htmx, withID)
			})
			if full.Code != test.status {
				t.Fatalf("status = %d, want %d", full.Code, test.status)
			}
			assertMutationParity(t, test.name, full, htmx, test.message)
		})
	}
}

func TestCSRFProtectionParity(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		name := "full page"
		if htmx {
			name = "htmx fragment"
		}
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/en/account/settings/profile", strings.NewReader("username=bob"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			NewHandler().ServeHTTP(response, request)

			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
			}
			if body := response.Body.String(); body != "csrf validation failed\n" {
				t.Fatalf("body = %q, want csrf validation failed", body)
			}
		})
	}
}
