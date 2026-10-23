package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
)

func TestNewHandler(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{name: "health check", method: http.MethodGet, path: "/healthz", expectedStatus: http.StatusOK, expectedBody: "ok\n"},
		{name: "state change without csrf", method: http.MethodPost, path: "/healthz", expectedStatus: http.StatusForbidden, expectedBody: "csrf validation failed\n"},
		{name: "unknown route", method: http.MethodGet, path: "/missing", expectedStatus: http.StatusNotFound, expectedBody: "404 page not found\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()

			NewHandler().ServeHTTP(response, request)

			if response.Code != test.expectedStatus {
				t.Errorf("status = %d, want %d", response.Code, test.expectedStatus)
			}
			if response.Body.String() != test.expectedBody {
				t.Errorf("body = %q, want %q", response.Body.String(), test.expectedBody)
			}
		})
	}
}

func TestNewHandlerAddsRequestID(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	requestID := response.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("X-Request-ID header is empty")
	}
	if len(requestID) != 32 {
		t.Errorf("request id length = %d, want 32", len(requestID))
	}
}

func TestAuthenticatedPublicPagesReflectSessionHeader(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	handler := NewRouterWithServices(dependencies)
	session := &http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"}

	for _, path := range []string{"/en/", "/en/about", "/en/home"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(session)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			body := response.Body.String()
			if strings.Contains(body, `href="/en/sign-in"`) {
				t.Error("authenticated page advertised sign-in navigation")
			}
			if strings.Contains(body, `href="/en/sign-up"`) {
				t.Error("authenticated page advertised sign-up navigation")
			}
			if !strings.Contains(body, `href="/en/groups"`) {
				t.Error("authenticated page missing groups navigation")
			}
			if !strings.Contains(body, "user@example.test") {
				t.Error("authenticated page missing account menu label")
			}
		})
	}
}

func TestNewHandlerRendersHomeLayout(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Errorf("content type = %q, want text/html; charset=utf-8", contentType)
	}
	body := response.Body.String()
	for _, fragment := range []string{
		"<!doctype html>",
		`<link rel="stylesheet" href="/static/app.css">`,
		`<html lang="en" data-theme="light" data-account-theme=""`,
		"<title>Calbooker</title>",
		`<h1 id="page-heading"`,
		"Welcome to Calbooker",
		`aria-label="Primary navigation"`,
		`id="alerts"`,
		`id="theme-toggle"`,
		`<script src="/static/app.js"></script>`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("home body missing %q", fragment)
		}
	}
	if !strings.Contains(body, `src="/static/htmx.min.js"`) {
		t.Error("home body missing local HTMX runtime")
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "<style") || strings.Contains(body, " style=") {
		t.Error("home body has inline script or style, which the Content-Security-Policy blocks")
	}
}

func TestNewHandlerServesGeneratedStylesheet(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/css; charset=utf-8" {
		t.Errorf("content type = %q, want text/css; charset=utf-8", contentType)
	}
	if response.Body.Len() == 0 {
		t.Fatal("stylesheet response is empty")
	}
}

func TestNewHandlerServesPinnedHTMXRuntime(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.Len() == 0 || !strings.Contains(response.Body.String(), "htmx") {
		t.Fatal("HTMX runtime response is empty or invalid")
	}
}

func TestNewHandlerDoesNotServeAssetSource(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/src/app.css", nil))

	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestNewHandlerRendersHTMXFragment(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/en/", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()

	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Header().Get("Vary") != "HX-Request" {
		t.Errorf("vary = %q, want HX-Request", response.Header().Get("Vary"))
	}
	body := response.Body.String()
	if strings.Contains(body, "<!doctype html>") {
		t.Error("HTMX response contains full document")
	}
	for _, fragment := range []string{`<section id="page-content" aria-labelledby="page-heading">`, `<h1 id="page-heading"`, "Welcome to Calbooker"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("HTMX response missing %q", fragment)
		}
	}
}

func TestNewHandlerRegistersLocalizedAuthenticationRoutes(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/hu/sign-in", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, fragment := range []string{"<!doctype html>", `lang="hu"`, `href="/hu/oauth/sso"`, `data-testid="sso-sign-in"`} {
		if !strings.Contains(body, fragment) {
			t.Errorf("sign-in page missing %q", fragment)
		}
	}
}

func TestNewHandlerRejectsAuthenticationMutationWithoutCSRF(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/en/sign-in", strings.NewReader("identifier=user%40example.test&password=secret-password"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestNewHandlerRedirectsToDefaultLocale(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusFound)
	}
	if location := response.Header().Get("Location"); location != "/en/" {
		t.Errorf("location = %q, want /en/", location)
	}
}

func TestNewHandlerRedirectsUsingLanguagePreferences(t *testing.T) {
	tests := []struct {
		name           string
		acceptLanguage string
		cookie         string
		wantLocation   string
	}{
		{name: "browser language", acceptLanguage: "hu-HU, en;q=0.8", wantLocation: "/hu/"},
		{name: "browser preference wins", acceptLanguage: "en", cookie: "hu", wantLocation: "/hu/"},
		{name: "invalid preference defaults", acceptLanguage: "fr", cookie: "de", wantLocation: "/en/"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Accept-Language", test.acceptLanguage)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: localeCookieName, Value: test.cookie})
			}
			response := httptest.NewRecorder()
			NewHandler().ServeHTTP(response, request)

			if location := response.Header().Get("Location"); location != test.wantLocation {
				t.Errorf("location = %q, want %q", location, test.wantLocation)
			}
		})
	}
}

func TestNewHandlerCanonicalizesLocalePath(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/hu", nil))

	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusFound)
	}
	if location := response.Header().Get("Location"); location != "/hu/" {
		t.Errorf("location = %q, want /hu/", location)
	}
}

func TestNewHandlerRejectsUnsupportedLocaleDeepLink(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fr/", nil))

	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestNewHandlerRendersLocalizedNotFound(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/hu/missing", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	body := response.Body.String()
	for _, fragment := range []string{"<!doctype html>", "Az oldal nem található", "A kért oldal nem található."} {
		if !strings.Contains(body, fragment) {
			t.Errorf("localized not-found body missing %q", fragment)
		}
	}
}
