package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/locale"
)

func TestContractRequestEncodesForm(t *testing.T) {
	values := url.Values{
		"identifier": {"user name&role=admin"},
		"password":   {"p@ss word=1"},
		"empty":      {""},
	}
	request := newContractRequest(http.MethodPost, "/en/sign-in").withForm(values).build()

	if got := request.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q, want application/x-www-form-urlencoded", got)
	}
	if err := request.ParseForm(); err != nil {
		t.Fatalf("ParseForm() error = %v, want nil", err)
	}
	for key, want := range values {
		if got := request.PostForm.Get(key); got != want[0] {
			t.Errorf("PostForm[%q] = %q, want %q", key, got, want[0])
		}
	}
	if request.URL.Path != "/en/sign-in" {
		t.Errorf("path = %q, want /en/sign-in", request.URL.Path)
	}
}

func TestContractHelpersClassifyRealRoutes(t *testing.T) {
	handler := NewHandler()

	t.Run("full document", func(t *testing.T) {
		serveContract(t, handler, newContractRequest(http.MethodGet, "/en/")).
			assertStatus(t, http.StatusOK).
			assertFullDocument(t).
			assertBodyContains(t, "Welcome to Calbooker")
	})

	t.Run("accessible fragment", func(t *testing.T) {
		serveContract(t, handler, newContractRequest(http.MethodGet, "/en/").withHTMX()).
			assertStatus(t, http.StatusOK).
			assertFragment(t)
	})

	t.Run("browser redirect", func(t *testing.T) {
		serveContract(t, handler, newContractRequest(http.MethodGet, "/")).
			assertFound(t, "/en/")
	})

	t.Run("localized not found uses stable message ids", func(t *testing.T) {
		serveContract(t, handler, newContractRequest(http.MethodGet, "/hu/missing")).
			assertStatus(t, http.StatusNotFound).
			assertFullDocument(t).
			assertCatalogMessage(t, locale.Code("hu"), "errors.not_found.heading").
			assertCatalogMessage(t, locale.Code("hu"), "errors.not_found.message")
	})

	t.Run("plain text health response", func(t *testing.T) {
		response := serveContract(t, handler, newContractRequest(http.MethodGet, "/healthz")).
			assertStatus(t, http.StatusOK)
		if err := checkEncoding(response.recorder, "text/plain; charset=utf-8"); err != nil {
			t.Fatalf("%v", err)
		}
		if response.body() != "ok\n" {
			t.Fatalf("body = %q, want ok", response.body())
		}
	})
}

func TestContractHelpersDetectMismatches(t *testing.T) {
	full := httptest.NewRecorder()
	full.Header().Set("Content-Type", "text/html; charset=utf-8")
	full.WriteHeader(http.StatusOK)
	_, _ = full.WriteString("<!doctype html><html><body><h1 id=\"page-heading\">Hi</h1></body></html>")

	fragment := httptest.NewRecorder()
	fragment.Header().Set("Content-Type", "text/html; charset=utf-8")
	fragment.Header().Set("Vary", "HX-Request")
	fragment.WriteHeader(http.StatusOK)
	_, _ = fragment.WriteString(`<div id="alerts" hx-swap-oob="true"></div><section><h1 id="page-heading">Hi</h1></section>`)

	empty := httptest.NewRecorder()
	empty.WriteHeader(http.StatusNoContent)

	nonEmpty := httptest.NewRecorder()
	nonEmpty.WriteHeader(http.StatusOK)
	_, _ = nonEmpty.WriteString("payload")

	redirect := httptest.NewRecorder()
	redirect.Header().Set("Location", "/en/")
	redirect.WriteHeader(http.StatusSeeOther)

	if err := checkStatus(full, http.StatusNotFound); err == nil {
		t.Error("checkStatus(status mismatch) = nil, want error")
	}
	if err := checkEncoding(full, "text/plain"); err == nil {
		t.Error("checkEncoding(mismatch) = nil, want error")
	}
	if err := checkFullDocument(fragment); err == nil {
		t.Error("checkFullDocument(fragment) = nil, want error")
	}
	if err := checkFragment(full); err == nil {
		t.Error("checkFragment(full document) = nil, want error")
	}
	if err := checkEmptyBody(nonEmpty); err == nil {
		t.Error("checkEmptyBody(non-empty) = nil, want error")
	}
	if err := checkEmptyBody(empty); err != nil {
		t.Errorf("checkEmptyBody(empty) = %v, want nil", err)
	}
	if err := checkRedirect(redirect, http.StatusSeeOther, "/hu/"); err == nil {
		t.Error("checkRedirect(wrong location) = nil, want error")
	}
	if err := checkRedirect(redirect, http.StatusFound, "/en/"); err == nil {
		t.Error("checkRedirect(wrong status) = nil, want error")
	}
	if err := checkRedirect(redirect, http.StatusSeeOther, "/en/"); err != nil {
		t.Errorf("checkRedirect(valid) = %v, want nil", err)
	}

	catalogs := contractCatalogs(t)
	if err := checkCatalogMessage(catalogs, locale.Code("en"), "errors.not_found.heading", "<p>nothing</p>"); err == nil {
		t.Error("checkCatalogMessage(missing) = nil, want error")
	}
	if err := checkCatalogMessage(nil, locale.Code("en"), "errors.not_found.heading", "body"); err == nil {
		t.Error("checkCatalogMessage(nil catalogs) = nil, want error")
	}
	if err := checkCatalogMessage(catalogs, locale.Code("en"), "errors.not_found.heading", "errors.not_found.heading"); err == nil {
		t.Error("checkCatalogMessage(leaked identifier) = nil, want error")
	}
}

func TestContractHTMXRedirectAndEmptyResponses(t *testing.T) {
	htmxRedirect := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("HX-Redirect", "/en/sign-in")
		writer.WriteHeader(http.StatusOK)
	})
	serveContract(t, htmxRedirect, newContractRequest(http.MethodPost, "/en/settings")).
		assertHTMXRedirect(t, "/en/sign-in")

	noContent := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	response := serveContract(t, noContent, newContractRequest(http.MethodPost, "/en/settings"))
	if err := checkStatus(response.recorder, http.StatusNoContent); err != nil {
		t.Fatalf("%v", err)
	}
	response.assertEmpty(t)

	broken := httptest.NewRecorder()
	broken.Header().Set("HX-Redirect", "/en/sign-in")
	broken.Header().Set("Location", "/en/sign-in")
	broken.WriteHeader(http.StatusOK)
	if err := checkHTMXRedirect(broken, "/en/sign-in"); err == nil {
		t.Error("checkHTMXRedirect(dual redirect) = nil, want error")
	}
}

func TestContractRequestHelpersCompose(t *testing.T) {
	request := newContractRequest(http.MethodPost, "/en/account/settings/profile").
		withFormValue("display_name", "Ada Lovelace").
		withFormValue("display_name", "Grace Hopper").
		withHeader("Accept-Language", "hu").
		withCookie(&http.Cookie{Name: "calbooker_locale", Value: "hu"}).
		withHTMX()

	built := request.build()
	if got := built.PostFormValue("display_name"); got != "Grace Hopper" {
		t.Errorf("display_name = %q, want Grace Hopper", got)
	}
	if got := built.Header.Get("Accept-Language"); got != "hu" {
		t.Errorf("Accept-Language = %q, want hu", got)
	}
	if got := built.Header.Get("HX-Request"); got != "true" {
		t.Errorf("HX-Request = %q, want true", got)
	}
	cookie, err := built.Cookie("calbooker_locale")
	if err != nil || cookie.Value != "hu" {
		t.Errorf("locale cookie = %v/%v, want hu", cookie, err)
	}
	if !strings.Contains(built.URL.Path, "/en/") {
		t.Errorf("path = %q, want localized path", built.URL.Path)
	}
}
