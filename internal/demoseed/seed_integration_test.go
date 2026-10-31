//go:build integration

package demoseed

import (
	"context"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func fakeSubjects() map[string]string {
	subjects := map[string]string{}
	for i, a := range Accounts() {
		subjects[a.Email] = string(rune('0'+i/100)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
	}
	return subjects
}

func TestSeedFillsAndIsRepeatable(t *testing.T) {
	harness := postgres.New(t)
	ctx := context.Background()
	runner, err := store.NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	counts := func() map[string]int {
		out := map[string]int{}
		for _, table := range []string{"users", "oauth_identities", "groups", "teams", "guardians", "season_parts", "plan_slots",
			"coach_availability", "season_team_coaches", "coach_qualifications", "events", "event_participants", "plan_trainings"} {
			var n int
			if err := harness.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			out[table] = n
		}
		return out
	}

	subjects := fakeSubjects()
	if err := Seed(ctx, harness.DB, subjects, now); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	first := counts()
	if err := Seed(ctx, harness.DB, subjects, now); err != nil {
		t.Fatalf("second Seed() error = %v", err)
	}
	second := counts()
	for table, n := range first {
		if second[table] != n {
			t.Errorf("%s: %d then %d", table, n, second[table])
		}
	}
	// users and guardians include the profile child "BVSC Kid 1" (no identity).
	want := map[string]int{"users": 78, "oauth_identities": 77, "groups": 3, "teams": 5, "guardians": 31, "season_parts": 6,
		"season_team_coaches": 9, "plan_trainings": 0}
	for table, n := range want {
		if first[table] != n {
			t.Errorf("%s = %d, want %d", table, first[table], n)
		}
	}
	if first["events"] != 5*len(EventSpecs()) {
		t.Errorf("events = %d", first["events"])
	}

	var storedParents int
	if err := harness.DB.QueryRowContext(ctx, `SELECT count(*) FROM team_members WHERE role = 'parent'`).Scan(&storedParents); err != nil || storedParents != 0 {
		t.Errorf("stored parent team rows = %d, %v; parents belong to teams through guardians only", storedParents, err)
	}
	var admins int
	if err := harness.DB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_admin AND email = $1`, SiteAdminEmail).Scan(&admins); err != nil || admins != 1 {
		t.Errorf("site admins = %d, %v", admins, err)
	}
	var subject string
	if err := harness.DB.QueryRowContext(ctx, `
		SELECT i.subject FROM oauth_identities i JOIN users u ON u.id = i.user_id
		WHERE u.email = 'bvsc-coach-1@example.com' AND i.provider = 'sso'`).Scan(&subject); err != nil || subject != subjects["bvsc-coach-1@example.com"] {
		t.Errorf("coach subject = %q, %v", subject, err)
	}
	// Migration history survives the wipe.
	var ledger int
	if err := harness.DB.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&ledger); err != nil || ledger == 0 {
		t.Errorf("schema_migrations rows = %d, %v", ledger, err)
	}
}

func TestSeedWithoutSiteAdminAccount(t *testing.T) {
	harness := postgres.New(t)
	ctx := context.Background()
	runner, err := store.NewMigrator(harness.DB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	subjects := fakeSubjects()
	delete(subjects, SiteAdminEmail)
	if err := Seed(ctx, harness.DB, subjects, time.Now()); err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	delete(subjects, "bvsc@example.com")
	if err := Seed(ctx, harness.DB, subjects, time.Now()); err == nil {
		t.Fatal("Seed() accepted a missing group account")
	}
}
