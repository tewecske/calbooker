package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

func TestThemeUpdate(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		principal      middleware.Principal
		withPrincipal  bool
		body           string
		updater        ThemeUpdater
		expectedStatus int
	}{
		{name: "missing principal", method: http.MethodPost, body: "theme=dark", updater: &themeUpdaterStub{}, expectedStatus: http.StatusUnauthorized},
		{name: "invalid principal id", method: http.MethodPost, principal: middleware.Principal{ID: "not-a-number"}, withPrincipal: true, body: "theme=dark", updater: &themeUpdaterStub{}, expectedStatus: http.StatusInternalServerError},
		{name: "invalid theme", method: http.MethodPost, principal: middleware.Principal{ID: "7"}, withPrincipal: true, body: "theme=blue", updater: &themeUpdaterStub{err: service.ErrInvalidTheme}, expectedStatus: http.StatusUnprocessableEntity},
		{name: "valid update", method: http.MethodPost, principal: middleware.Principal{ID: "7"}, withPrincipal: true, body: "theme=dark", updater: &themeUpdaterStub{}, expectedStatus: http.StatusNoContent},
		{name: "wrong method", method: http.MethodGet, principal: middleware.Principal{ID: "7"}, withPrincipal: true, updater: &themeUpdaterStub{}, expectedStatus: http.StatusMethodNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/en/account/theme", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			handler := ThemeUpdate(test.updater)
			if test.withPrincipal {
				handler = middleware.RequireAuth(themeAuthenticator{principal: test.principal})(handler)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.expectedStatus {
				t.Errorf("status = %d, want %d", response.Code, test.expectedStatus)
			}
		})
	}
}

type themeUpdaterStub struct {
	err error
}

func (u *themeUpdaterStub) UpdateTheme(context.Context, int64, string) error { return u.err }

type themeAuthenticator struct {
	principal middleware.Principal
}

func (a themeAuthenticator) Authenticate(context.Context, *http.Request) (middleware.Principal, error) {
	return a.principal, nil
}
