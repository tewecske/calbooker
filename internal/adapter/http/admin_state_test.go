package httpadapter

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestParseAdminListStateBoundsInvalidValues(t *testing.T) {
	request := httptest.NewRequest("GET", "/en/admin/users?page=-9&size=100000&sort=password_hash&dir=sideways&admin=maybe&q=%20", nil)
	state := parseAdminListState(request)
	if state.Query.Page != 1 {
		t.Errorf("page = %d, want 1", state.Query.Page)
	}
	if state.Query.Size != service.MaxAdminPageSize {
		t.Errorf("size = %d, want %d", state.Query.Size, service.MaxAdminPageSize)
	}
	if state.Query.Sort != service.AdminSortCreatedAt {
		t.Errorf("sort = %q, want %q", state.Query.Sort, service.AdminSortCreatedAt)
	}
	if state.Query.Direction != service.AdminDirectionDesc {
		t.Errorf("direction = %q, want %q", state.Query.Direction, service.AdminDirectionDesc)
	}
	if state.Query.Admin != service.AdminFilterAll {
		t.Errorf("admin = %q, want empty", state.Query.Admin)
	}
	if state.Query.Search != "" {
		t.Errorf("search = %q, want empty", state.Query.Search)
	}
}

func TestAdminListStateRoundTripsThroughURL(t *testing.T) {
	request := httptest.NewRequest("GET", "/en/admin/users?q=alice&admin=yes&confirmed=no&sort=email&dir=asc&page=2&size=25", nil)
	state := parseAdminListState(request)
	rendered := state.URL("/en/admin/users")
	parsed, err := url.Parse(rendered)
	if err != nil {
		t.Fatalf("parse rendered URL: %v", err)
	}
	roundTrip := parseAdminListState(httptest.NewRequest("GET", parsed.String(), nil))
	if roundTrip.Query != state.Query {
		t.Fatalf("round trip = %+v, want %+v", roundTrip.Query, state.Query)
	}
}

func TestAdminListStateOmitsDefaults(t *testing.T) {
	state := adminListState{Query: service.AdminUserQuery{}.Normalize()}
	rendered := state.URL("/en/admin/users")
	if rendered != "/en/admin/users?page=1&size=20" {
		t.Fatalf("URL = %q, want only page and size", rendered)
	}
}

func TestAdminListStateWithFilterResetsPage(t *testing.T) {
	state := adminListState{Query: service.AdminUserQuery{Page: 4, Size: 10}.Normalize()}
	filtered := state.WithFilter(adminParamAdmin, service.AdminFilterYes)
	if filtered.Query.Page != 1 {
		t.Fatalf("page = %d, want 1", filtered.Query.Page)
	}
	if filtered.Query.Admin != service.AdminFilterYes {
		t.Fatalf("admin = %q, want yes", filtered.Query.Admin)
	}
	cleared := filtered.WithFilter(adminParamAdmin, "")
	if cleared.Query.Admin != service.AdminFilterAll {
		t.Fatalf("admin = %q, want empty", cleared.Query.Admin)
	}
}

func TestAdminListStateWithSortTogglesDirection(t *testing.T) {
	state := adminListState{Query: service.AdminUserQuery{}.Normalize()}
	email := state.WithSort(service.AdminSortEmail)
	if email.Query.Sort != service.AdminSortEmail || email.Query.Direction != service.AdminDirectionAsc {
		t.Fatalf("email sort = %+v, want email asc", email.Query)
	}
	toggled := email.WithSort(service.AdminSortEmail)
	if toggled.Query.Direction != service.AdminDirectionDesc {
		t.Fatalf("toggled direction = %q, want desc", toggled.Query.Direction)
	}
	created := toggled.WithSort(service.AdminSortCreatedAt)
	if created.Query.Direction != service.AdminDirectionDesc {
		t.Fatalf("created_at direction = %q, want desc default", created.Query.Direction)
	}
}

func TestAdminListStateWithPageKeepsPageWhenUnbounded(t *testing.T) {
	state := adminListState{Query: service.AdminUserQuery{Size: 10}.Normalize()}
	next := state.WithPage(2)
	if next.Query.Page != 2 || next.Query.Size != 10 {
		t.Fatalf("state = %+v, want page 2 size 10", next.Query)
	}
}
