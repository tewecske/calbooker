//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

// paginationUserCount is a realistic list volume: several pages at the default
// page size with a non-uniform final page.
const paginationUserCount = 257

func seedPaginationUsers(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO users (email, username, display_name, is_admin, kind, theme, locale, created_at, email_verified_at)
		SELECT
			'pagination-' || lpad(series.i::text, 4, '0') || '@example.test',
			'pagination-' || lpad(series.i::text, 4, '0'),
			'Pagination User ' || series.i,
			(series.i % 5 = 0),
			'account',
			'light',
			'en',
			1000 + (series.i % 10),
			CASE WHEN series.i % 3 = 0 THEN 1000 ELSE NULL END
		FROM generate_series(0, $1) AS series(i)
	`, paginationUserCount-1); err != nil {
		t.Fatalf("seed pagination users: %v", err)
	}
}

func scalarCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return count
}

func TestAdminUserRepositoryWalksRealisticVolumeDeterministically(t *testing.T) {
	harness, _ := newAdminUserHarness(t)
	seedPaginationUsers(t, harness.DB)
	ctx := context.Background()

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]bool, paginationUserCount)
	var ordering []struct {
		createdAt int64
		id        int64
	}
	for page := 1; page <= 11; page++ {
		result, err := listed.ListUsers(ctx, service.AdminUserQuery{
			Size: 25, Page: page, Sort: service.AdminSortCreatedAt, Direction: service.AdminDirectionAsc,
		})
		if err != nil {
			t.Fatalf("ListUsers(page %d) error = %v", page, err)
		}
		if result.Total != paginationUserCount {
			t.Fatalf("page %d total = %d, want %d", page, result.Total, paginationUserCount)
		}
		wantRows := 25
		if page == 11 {
			wantRows = paginationUserCount - 10*25
		}
		if len(result.Users) != wantRows {
			t.Fatalf("page %d rows = %d, want %d", page, len(result.Users), wantRows)
		}
		for _, user := range result.Users {
			if user.Email == nil {
				t.Fatalf("page %d returned an account without an email", page)
			}
			if seen[*user.Email] {
				t.Fatalf("account %s appeared on more than one page", *user.Email)
			}
			seen[*user.Email] = true
			ordering = append(ordering, struct {
				createdAt int64
				id        int64
			}{user.CreatedAt, user.ID})
		}
	}
	if len(seen) != paginationUserCount {
		t.Fatalf("walked %d unique accounts, want %d", len(seen), paginationUserCount)
	}
	if !sort.SliceIsSorted(ordering, func(i, j int) bool {
		if ordering[i].createdAt != ordering[j].createdAt {
			return ordering[i].createdAt < ordering[j].createdAt
		}
		return ordering[i].id < ordering[j].id
	}) {
		t.Fatal("created_at pagination is not stably ordered by (created_at, id)")
	}

	beyond, err := listed.ListUsers(ctx, service.AdminUserQuery{Size: 25, Page: 12, Sort: service.AdminSortCreatedAt})
	if err != nil {
		t.Fatalf("ListUsers(beyond last page) error = %v", err)
	}
	if beyond.Total != paginationUserCount || len(beyond.Users) != 0 {
		t.Fatalf("beyond last page = total %d rows %d, want %d/0", beyond.Total, len(beyond.Users), paginationUserCount)
	}
}

func TestAdminUserRepositoryFilteringAtVolume(t *testing.T) {
	harness, _ := newAdminUserHarness(t)
	seedPaginationUsers(t, harness.DB)
	ctx := context.Background()

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		query service.AdminUserQuery
		count string
	}{
		{
			name:  "administrators",
			query: service.AdminUserQuery{Admin: service.AdminFilterYes, Size: 10},
			count: "SELECT count(*) FROM users WHERE is_admin = TRUE",
		},
		{
			name:  "non administrators",
			query: service.AdminUserQuery{Admin: service.AdminFilterNo, Size: 10},
			count: "SELECT count(*) FROM users WHERE is_admin = FALSE",
		},
		{
			name:  "confirmed",
			query: service.AdminUserQuery{Confirmed: service.AdminFilterYes, Size: 10},
			count: "SELECT count(*) FROM users WHERE email_verified_at IS NOT NULL",
		},
		{
			name:  "unconfirmed",
			query: service.AdminUserQuery{Confirmed: service.AdminFilterNo, Size: 10},
			count: "SELECT count(*) FROM users WHERE email_verified_at IS NULL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := scalarCount(t, harness.DB, test.count)
			result, err := listed.ListUsers(ctx, test.query)
			if err != nil {
				t.Fatalf("ListUsers(%s) error = %v", test.name, err)
			}
			if result.Total != expected {
				t.Fatalf("%s total = %d, want %d", test.name, result.Total, expected)
			}
			if len(result.Users) > result.Size {
				t.Fatalf("%s rows = %d, size = %d", test.name, len(result.Users), result.Size)
			}
		})
	}

	found, err := listed.ListUsers(ctx, service.AdminUserQuery{Search: "pagination-0042", Size: 10})
	if err != nil {
		t.Fatalf("ListUsers(search) error = %v", err)
	}
	if found.Total != 1 {
		t.Fatalf("unique-token search total = %d, want 1", found.Total)
	}
	if found.Users[0].Email == nil || *found.Users[0].Email != "pagination-0042@example.test" {
		t.Fatalf("unique-token search matched %v", found.Users[0].Email)
	}
}

func TestAdminUserRepositorySortsNullableColumnsAtVolume(t *testing.T) {
	harness, _ := newAdminUserHarness(t)
	seedPaginationUsers(t, harness.DB)
	ctx := context.Background()

	var orphanID int64
	if err := harness.DB.QueryRowContext(ctx, `
		INSERT INTO users (username, created_at) VALUES ('pagination-orphan', 5000) RETURNING id
	`).Scan(&orphanID); err != nil {
		t.Fatalf("insert account without email: %v", err)
	}

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{service.AdminDirectionAsc, service.AdminDirectionDesc} {
		result, err := listed.ListUsers(ctx, service.AdminUserQuery{
			Size: 100, Page: 3, Sort: service.AdminSortEmail, Direction: direction,
		})
		if err != nil {
			t.Fatalf("ListUsers(email %s) error = %v", direction, err)
		}
		if len(result.Users) == 0 {
			t.Fatalf("email %s returned no rows", direction)
		}
		last := result.Users[len(result.Users)-1]
		if last.Email != nil {
			t.Fatalf("email %s last row email = %v, want NULL last", direction, *last.Email)
		}
		if last.ID != orphanID {
			t.Fatalf("email %s NULL row id = %d, want %d", direction, last.ID, orphanID)
		}
	}
}

func TestAdminUserRepositoryBoundsHostileQueriesAtVolume(t *testing.T) {
	harness, _ := newAdminUserHarness(t)
	seedPaginationUsers(t, harness.DB)
	ctx := context.Background()

	listed, err := NewAdminUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}

	result, err := listed.ListUsers(ctx, service.AdminUserQuery{
		Size: 1_000_000, Page: -9, Sort: "id; DROP TABLE users", Direction: "desc; DELETE",
		Search: strings.Repeat("x", 5000),
	})
	if err != nil {
		t.Fatalf("ListUsers(hostile) error = %v", err)
	}
	if result.Page != 1 {
		t.Fatalf("hostile page = %d, want 1", result.Page)
	}
	if result.Size != service.MaxAdminPageSize {
		t.Fatalf("hostile size = %d, want %d", result.Size, service.MaxAdminPageSize)
	}
	if len(result.Users) > service.MaxAdminPageSize {
		t.Fatalf("hostile rows = %d, want <= %d", len(result.Users), service.MaxAdminPageSize)
	}
	if result.Total != 0 {
		t.Fatalf("hostile oversized search total = %d, want 0", result.Total)
	}

	defaulted, err := listed.ListUsers(ctx, service.AdminUserQuery{})
	if err != nil {
		t.Fatalf("ListUsers(defaults) error = %v", err)
	}
	if defaulted.Page != 1 || defaulted.Size != service.DefaultAdminPageSize {
		t.Fatalf("default page/size = %d/%d, want 1/%d", defaulted.Page, defaulted.Size, service.DefaultAdminPageSize)
	}
	if defaulted.Total != paginationUserCount {
		t.Fatalf("default total = %d, want %d", defaulted.Total, paginationUserCount)
	}
}

func TestDatastorePerformanceIndexesExist(t *testing.T) {
	harness, _ := newAdminUserHarness(t)

	expected := []string{
		"users_email_unique_idx",
		"users_username_unique_idx",
		"sessions_user_id_idx",
		"oauth_identities_user_id_idx",
		"guest_claim_codes_user_id_idx",
		"guest_claim_codes_active_user_idx",
		"groups_name_norm_idx",
		"group_member_roles_role_idx",
		"teams_group_id_idx",
		"team_members_member_idx",
		"group_members_user_id_idx",
		"audit_log_occurred_at_idx",
		"audit_log_actor_user_id_idx",
		"audit_log_target_idx",
		"usage_events_created_at_idx",
		"usage_events_route_created_at_idx",
		"usage_events_user_id_created_at_idx",
		"usage_events_request_id_idx",
		"usage_events_trace_id_idx",
		"oauth_states_user_id_idx",
		"oauth_states_expires_at_idx",
	}

	rows, err := harness.DB.QueryContext(context.Background(),
		"SELECT indexname FROM pg_indexes WHERE schemaname = current_schema()")
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer func() { _ = rows.Close() }()

	present := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}

	for _, name := range expected {
		if !present[name] {
			t.Errorf("expected index %s is missing", name)
		}
	}
}

func TestAdminUserRepositoryOrderingUsesIndexFriendlyPlan(t *testing.T) {
	harness, _ := newAdminUserHarness(t)
	seedPaginationUsers(t, harness.DB)
	ctx := context.Background()
	if _, err := harness.DB.ExecContext(ctx, "ANALYZE users"); err != nil {
		t.Fatalf("analyze users: %v", err)
	}

	// Force the planner away from a sequential scan so the tiny test relation
	// still proves the unique email lookup is served by its index.
	transaction, err := harness.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scan: %v", err)
	}
	var plan string
	if err := transaction.QueryRowContext(ctx,
		"EXPLAIN SELECT id FROM users WHERE email = 'pagination-0042@example.test'").Scan(&plan); err != nil {
		t.Fatalf("explain email lookup: %v", err)
	}
	if !strings.Contains(plan, "users_email_unique_idx") {
		t.Errorf("email lookup plan does not use users_email_unique_idx: %s", plan)
	}
}
