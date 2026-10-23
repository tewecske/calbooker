package httpadapter

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/locale"
)

func TestPageRendererRenderState(t *testing.T) {
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v, want nil", err)
	}

	response := httptest.NewRecorder()
	err = renderer.RenderState(response, httptest.NewRequest(http.MethodGet, "/en/missing", nil), locale.English, PageStateConflict)
	if err != nil {
		t.Fatalf("RenderState() error = %v, want nil", err)
	}
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Change conflict") {
		t.Errorf("full state response = %d, %q", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/en/missing", nil)
	request.Header.Set("HX-Request", "true")
	response = httptest.NewRecorder()
	err = renderer.RenderState(response, request, locale.English, PageStateSessionExpired)
	if err != nil {
		t.Fatalf("RenderState() HTMX error = %v, want nil", err)
	}
	if strings.Contains(response.Body.String(), "<!doctype html>") || !strings.Contains(response.Body.String(), "Session expired") {
		t.Errorf("HTMX state response = %q", response.Body.String())
	}
}

func TestPageRendererRenderStateRejectsInvalidState(t *testing.T) {
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v, want nil", err)
	}
	err = renderer.RenderState(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/en/", nil),
		locale.Code("fr"),
		PageStateNotFound,
	)
	if !errors.Is(err, ErrInvalidPageState) {
		t.Fatalf("RenderState() error = %v, want errors.Is(_, %v)", err, ErrInvalidPageState)
	}
}
