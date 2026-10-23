package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAboutPageRendersCredits(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/about", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, fragment := range []string{
		"<!doctype html>",
		`id="page-heading"`,
		"About Calbooker",
		`href="https://github.com/tailwindlabs/heroicons"`,
		"MIT License",
		"Copyright (c) Tailwind Labs, Inc.",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("about page missing %q", fragment)
		}
	}
}

func TestAboutPageIsLocalized(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/hu/about", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, fragment := range []string{`lang="hu"`, "A Calbooker névjegy", "Ikonok"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("localized about page missing %q", fragment)
		}
	}
}

func TestAboutPageRendersHTMXFragment(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/en/about", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if strings.Contains(body, "<!doctype html>") {
		t.Error("HTMX response contains full document")
	}
	for _, fragment := range []string{`id="page-content"`, `id="page-heading"`, "About Calbooker"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("about fragment missing %q", fragment)
		}
	}
}

func TestHomePageLinksToAbout(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/", nil))

	if body := response.Body.String(); !strings.Contains(body, `href="/en/about"`) {
		t.Errorf("home page missing about navigation link")
	}
}
