package httpadapter

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewPageRendererRender(t *testing.T) {
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v, want nil", err)
	}

	response := httptest.NewRecorder()
	err = renderer.Render(response, "home", PageData{
		Language: "en",
		Title:    "<title>",
		Heading:  "<heading>",
		Alerts: []Alert{
			{Level: "error", Message: "<message>"},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, value := range []string{"&lt;title&gt;", "&lt;heading&gt;", "&lt;message&gt;"} {
		if !strings.Contains(body, value) {
			t.Errorf("rendered body missing escaped value %q", value)
		}
	}
	if strings.Contains(body, "<message>") {
		t.Error("rendered body contains unescaped alert data")
	}
}

func TestPageRendererRenderRejectsInvalidPage(t *testing.T) {
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v, want nil", err)
	}

	tests := []struct {
		name string
		page PageData
		want error
	}{
		{
			name: "missing language",
			page: PageData{Title: "title", Heading: "heading"},
			want: ErrInvalidPage,
		},
		{
			name: "missing title",
			page: PageData{Language: "en", Heading: "heading"},
			want: ErrInvalidPage,
		},
		{
			name: "missing heading",
			page: PageData{Language: "en", Title: "title"},
			want: ErrInvalidPage,
		},
		{
			name: "missing template",
			page: PageData{
				Language: "en",
				Title:    "title",
				Heading:  "heading",
			},
			want: ErrPageNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := "home"
			if test.name == "missing template" {
				name = "missing"
			}
			response := httptest.NewRecorder()
			err := renderer.Render(response, name, test.page)
			if !errors.Is(err, test.want) {
				t.Fatalf("Render() error = %v, want errors.Is(_, %v)", err, test.want)
			}
			if response.Code != http.StatusOK {
				t.Errorf("status = %d, want untouched recorder status %d", response.Code, http.StatusOK)
			}
			if response.Body.Len() != 0 {
				t.Error("invalid render wrote response body")
			}
		})
	}
}

func TestIsHTMX(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "missing header", want: false},
		{name: "true", header: "true", want: true},
		{name: "case and whitespace", header: " TRUE ", want: true},
		{name: "false", header: "false", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("HX-Request", test.header)
			if got := IsHTMX(request); got != test.want {
				t.Errorf("IsHTMX() = %t, want %t", got, test.want)
			}
		})
	}

	if IsHTMX(nil) {
		t.Error("IsHTMX(nil) = true, want false")
	}
}

func TestPageRendererRendersAccountThemeConfiguration(t *testing.T) {
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v, want nil", err)
	}
	response := httptest.NewRecorder()
	err = renderer.RenderRequest(response, httptest.NewRequest(http.MethodGet, "/en/", nil), PageData{
		Language:         "en",
		Title:            "Calbooker",
		Heading:          "Home",
		Kind:             "home",
		Template:         "home",
		FragmentTemplate: "home-fragment",
		Account:          &AccountMenu{Theme: "dark", ThemeURL: "/en/account/theme"},
	})
	if err != nil {
		t.Fatalf("RenderRequest() error = %v, want nil", err)
	}
	for _, fragment := range []string{
		`data-account-theme="dark"`,
		`data-theme-url="/en/account/theme"`,
		`<script src="/static/app.js"></script>`,
	} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Errorf("account theme response missing %q", fragment)
		}
	}
}
