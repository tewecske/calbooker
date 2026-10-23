package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/config"
)

// weekGridDemoPost sends a CSRF-protected form post through the full router.
func weekGridDemoPost(t *testing.T, handler http.Handler, target string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	token, err := middleware.NewCSRFToken()
	if err != nil {
		t.Fatalf("NewCSRFToken() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set(middleware.CSRFHeaderName, token)
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: token})
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func weekGridDemoGet(handler http.Handler, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

func developmentRouter() http.Handler {
	return NewRouterWithServices(Dependencies{DevelopmentRoutes: true})
}

func TestWeekGridDemoIsNotServedWithoutDevelopmentRoutes(t *testing.T) {
	// Production wiring: cmd/web passes config.DevelopmentRoutes(), which is
	// false outside the development environment.
	production := config.Config{Environment: config.EnvironmentProduction}
	testEnvironment := config.Config{Environment: config.EnvironmentTest}
	if production.DevelopmentRoutes() || testEnvironment.DevelopmentRoutes() {
		t.Fatal("development routes enabled outside the development environment")
	}
	if !(config.Config{Environment: config.EnvironmentDevelopment}).DevelopmentRoutes() {
		t.Fatal("development routes disabled in development")
	}

	handler := NewRouterWithServices(Dependencies{DevelopmentRoutes: production.DevelopmentRoutes()})
	for _, language := range []string{"en", "hu"} {
		if response := weekGridDemoGet(handler, "/"+language+"/dev/weekgrid"); response.Code != http.StatusNotFound ||
			strings.Contains(response.Body.String(), "data-week-grid") {
			t.Errorf("GET %s demo status = %d, want 404 without the grid", language, response.Code)
		}
		for _, action := range []string{"move", "create", "reset"} {
			form := url.Values{"item": {"u8"}, "weekday": {"2"}, "start": {"600"}, "end": {"690"}, "week": {"A"}}
			response := weekGridDemoPost(t, handler, "/"+language+"/dev/weekgrid/"+action, form, true)
			if response.Code < 400 {
				t.Errorf("POST %s %s status = %d, want an error status", language, action, response.Code)
			}
		}
	}
}

func TestWeekGridDemoPageRendersGrid(t *testing.T) {
	response := weekGridDemoGet(developmentRouter(), "/en/dev/weekgrid")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	for _, fragment := range []string{
		"<!doctype html>",
		`<script src="/static/sortable.min.js" defer></script>`,
		`id="demo-grid"`,
		`data-move-url="/en/dev/weekgrid/move?week=A"`,
		`data-create-url="/en/dev/weekgrid/create?week=A"`,
		`data-wg-item="u8"`,
		`data-wg-item="week-a"`,
		`wg-cols-7`,
		`wg-rows-56`,
		`wg-step-15`,
		`aria-current="page" data-wg-week-tab>Week A`,
		`data-wg-hidden-item="night"`,
		`The coach is not available at this time.`,
		`data-wg-move-form`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("demo page missing %q", fragment)
		}
	}
	if strings.Contains(body, `data-wg-item="week-b"`) {
		t.Error("week A shows a week B item")
	}
	if strings.Contains(body, ` style=`) {
		t.Error("demo page uses a style attribute, which the CSP forbids")
	}
	// The fixed item is shown but cannot be dragged.
	fixed := body[strings.Index(body, `data-wg-item="fixed"`):]
	fixed = fixed[:strings.Index(fixed, ">")]
	if strings.Contains(fixed, "data-wg-draggable") {
		t.Errorf("fixed item is draggable: %s", fixed)
	}
}

func TestWeekGridDemoViews(t *testing.T) {
	handler := developmentRouter()
	tests := []struct {
		target  string
		want    []string
		without []string
	}{
		{target: "/en/dev/weekgrid?week=B", want: []string{`data-wg-item="week-b"`, `aria-current="page" data-wg-week-tab>Week B`}, without: []string{`data-wg-item="week-a"`}},
		{target: "/en/dev/weekgrid?resources=1", want: []string{"wg-cols-14", "wg-cspan-2", `aria-label="Monday, Court 2"`, `name="resource"`}},
		{target: "/en/dev/weekgrid?readonly=1", want: []string{`data-wg-item="u8"`}, without: []string{"data-move-url", "data-create-url", "data-wg-draggable", "data-wg-move-form"}},
		{target: "/en/dev/weekgrid?from=2026-10-22", want: []string{"Oct 19 – Oct 25, 2026", `href="/en/dev/weekgrid?from=2026-10-12"`, `href="/en/dev/weekgrid?from=2026-10-26"`}, without: []string{"data-wg-week-tab"}},
		{target: "/hu/dev/weekgrid", want: []string{`lang="hu"`, "Heti naptár bemutató", "Hétfő", "A hét"}},
	}
	for _, test := range tests {
		t.Run(test.target, func(t *testing.T) {
			response := weekGridDemoGet(handler, test.target)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			body := response.Body.String()
			for _, fragment := range test.want {
				if !strings.Contains(body, fragment) {
					t.Errorf("missing %q", fragment)
				}
			}
			for _, fragment := range test.without {
				if strings.Contains(body, fragment) {
					t.Errorf("unexpected %q", fragment)
				}
			}
		})
	}
}

func TestWeekGridDemoMove(t *testing.T) {
	handler := developmentRouter()
	form := url.Values{"item": {"u8"}, "weekday": {"3"}, "start": {"607"}, "end": {"697"}, "week": {"A"}}
	response := weekGridDemoPost(t, handler, "/en/dev/weekgrid/move?week=A", form, true)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, `id="page-content"`) {
		t.Error("move response is not the grid fragment")
	}
	for _, fragment := range []string{
		`id="alerts"`, `hx-swap-oob="true"`, "Item updated.", `id="demo-grid"`,
		`data-wg-item="u8" data-weekday="3" data-start="600" data-end="690"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("move response missing %q", fragment)
		}
	}

	page := weekGridDemoGet(handler, "/en/dev/weekgrid").Body.String()
	if !strings.Contains(page, `data-wg-item="u8" data-weekday="3" data-start="600"`) {
		t.Error("the move did not persist")
	}
}

func TestWeekGridDemoMoveWithoutHTMXRedirects(t *testing.T) {
	handler := developmentRouter()
	form := url.Values{"item": {"u10"}, "weekday": {"2"}, "start": {"480"}, "end": {"570"}, "week": {"B"}}
	response := weekGridDemoPost(t, handler, "/en/dev/weekgrid/move?week=B&resources=1", url.Values{
		"item": form["item"], "weekday": form["weekday"], "start": form["start"], "end": form["end"], "week": form["week"], "resource": {"court-2"},
	}, false)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en/dev/weekgrid?resources=1&week=B" {
		t.Errorf("status = %d location = %q, want 303 back to the view", response.Code, response.Header().Get("Location"))
	}
}

func TestWeekGridDemoRejectsInvalidChanges(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		form    url.Values
		message string
	}{
		{name: "outside the grid", target: "/en/dev/weekgrid/move?week=A", form: url.Values{"item": {"u8"}, "weekday": {"1"}, "start": {"1300"}, "end": {"1390"}, "week": {"A"}}, message: "That change does not fit the grid."},
		{name: "week mismatch", target: "/en/dev/weekgrid/move?week=A", form: url.Values{"item": {"u8"}, "weekday": {"1"}, "start": {"600"}, "end": {"690"}, "week": {"B"}}, message: "That change does not fit the grid."},
		{name: "fixed item", target: "/en/dev/weekgrid/move?week=A", form: url.Values{"item": {"fixed"}, "weekday": {"1"}, "start": {"600"}, "end": {"690"}, "week": {"A"}}, message: "That item no longer exists or cannot be moved."},
		{name: "unknown item", target: "/en/dev/weekgrid/move?week=A", form: url.Values{"item": {"nope"}, "weekday": {"1"}, "start": {"600"}, "end": {"690"}, "week": {"A"}}, message: "That item no longer exists or cannot be moved."},
		{name: "create without end", target: "/en/dev/weekgrid/create?week=A", form: url.Values{"weekday": {"1"}, "start": {"600"}, "week": {"A"}}, message: "That change does not fit the grid."},
		{name: "localized", target: "/hu/dev/weekgrid/move?week=A", form: url.Values{"item": {"u8"}, "weekday": {"9"}, "start": {"600"}, "end": {"690"}, "week": {"A"}}, message: "Ez a módosítás nem fér el a naptárban."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := developmentRouter()
			response := weekGridDemoPost(t, handler, test.target, test.form, true)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", response.Code)
			}
			body := response.Body.String()
			if !strings.Contains(body, test.message) || !strings.Contains(body, `class="alert alert-error"`) || !strings.Contains(body, `id="demo-grid"`) {
				t.Errorf("rejection missing alert or grid: %s", body)
			}
			if !strings.Contains(body, `data-wg-item="u8" data-weekday="1" data-start="960"`) {
				t.Error("rejected change altered the grid")
			}

			full := weekGridDemoPost(t, handler, test.target, test.form, false)
			if full.Code != http.StatusUnprocessableEntity || !strings.Contains(full.Body.String(), "<!doctype html>") {
				t.Errorf("full-page rejection status = %d, want a 422 document", full.Code)
			}
		})
	}
}

func TestWeekGridDemoCreateAndReset(t *testing.T) {
	handler := developmentRouter()
	form := url.Values{"weekday": {"7"}, "start": {"540"}, "end": {"600"}, "week": {"A"}}
	response := weekGridDemoPost(t, handler, "/en/dev/weekgrid/create?week=A", form, true)
	if response.Code != http.StatusOK {
		t.Fatalf("create status = %d", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, `data-wg-item="new-1" data-weekday="7" data-start="540" data-end="600"`) ||
		!strings.Contains(body, "New item 1") || !strings.Contains(body, "Item added.") {
		t.Errorf("create response missing the new item: %s", body)
	}
	if body := weekGridDemoGet(handler, "/en/dev/weekgrid?week=B").Body.String(); strings.Contains(body, `data-wg-item="new-1"`) {
		t.Error("an item created in week A shows in week B")
	}

	reset := weekGridDemoPost(t, handler, "/en/dev/weekgrid/reset?week=A", url.Values{}, true)
	if reset.Code != http.StatusOK || strings.Contains(reset.Body.String(), `data-wg-item="new-1"`) || !strings.Contains(reset.Body.String(), "Demo reset.") {
		t.Errorf("reset status = %d, body still has the new item or lacks the alert", reset.Code)
	}
}

func TestWeekGridDemoRequiresCSRF(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/en/dev/weekgrid/reset", strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	developmentRouter().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without a CSRF token", response.Code)
	}
}
