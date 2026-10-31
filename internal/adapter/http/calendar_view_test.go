package httpadapter

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestCalendarViewParameterSelectsViewAndKeepsItInLinks(t *testing.T) {
	dependencies, _ := calendarDependencies(t, playerOfTeam(2))
	groups := dependencies.Groups.(*groupsUseCaseStub)
	groups.summaries = []service.GroupSummary{{Group: service.Group{ID: 3, Slug: "lions", Name: "Lions Club"}}, {Group: service.Group{ID: 9, Slug: "swim-club", Name: "Swim Club"}}}
	get := func(query string) string {
		return serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/calendar?week=2026-10-21"+query, "", false).Body.String()
	}

	body := get("")
	for _, want := range []string{
		`<div class="max-md:hidden" data-testid="calendar-grid-view">`,
		`class="grid gap-3 md:hidden" aria-label="Events by day"`,
		`href="/en/groups/lions/calendar?view=grid&amp;week=2026-10-19"`,
		`href="/en/groups/lions/calendar?view=agenda&amp;week=2026-10-19"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("default view missing %q", want)
		}
	}
	if strings.Contains(body, `name="view"`) || strings.Contains(body, `aria-current="true" data-testid="calendar-view`) {
		t.Error("default view should not pin a view")
	}

	agenda := get("&view=agenda&team=2")
	for _, want := range []string{
		`<div class="hidden" data-testid="calendar-grid-view">`,
		`class="grid gap-3 " aria-label="Events by day"`,
		`<input type="hidden" name="view" value="agenda">`,
		`href="/en/groups/lions/calendar?team=2&amp;view=agenda&amp;week=2026-10-12"`,
		`href="/en/groups/lions/calendar?team=2&amp;view=agenda&amp;week=2026-10-26"`,
		`href="/en/groups/lions/calendar?team=2&amp;view=agenda"`,
		`href="/en/groups/swim-club/calendar?view=agenda&amp;week=2026-10-19"`,
		`aria-current="true" data-testid="calendar-view-agenda"`,
	} {
		if !strings.Contains(agenda, want) {
			t.Errorf("agenda view missing %q", want)
		}
	}

	grid := get("&view=grid")
	for _, want := range []string{
		`<div class="" data-testid="calendar-grid-view">`,
		`class="grid gap-3 hidden" aria-label="Events by day"`,
		`<input type="hidden" name="view" value="grid">`,
		`href="/en/groups/swim-club/calendar?view=grid&amp;week=2026-10-19"`,
	} {
		if !strings.Contains(grid, want) {
			t.Errorf("grid view missing %q", want)
		}
	}

	if other := get("&view=bogus"); strings.Contains(other, "view=bogus") || !strings.Contains(other, `max-md:hidden" data-testid="calendar-grid-view"`) {
		t.Error("unknown view should fall back to the default")
	}
}
