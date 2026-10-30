//go:build integration

package postgres

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
	"github.com/tewecske/calbooker/tests/postgres"
)

func mustKindRepository(t *testing.T, harness *postgres.Harness) *KindRepository {
	t.Helper()
	repository, err := NewKindRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewKindRepository() error = %v", err)
	}
	return repository
}

// seedKinds gives groups their default kinds, as the group service does.
func seedKinds(t *testing.T, harness *postgres.Harness, groupIDs ...int64) {
	t.Helper()
	for _, groupID := range groupIDs {
		if err := SeedDefaultKinds(context.Background(), harness.DB, groupID, 100); err != nil {
			t.Fatalf("SeedDefaultKinds(%d) error = %v", groupID, err)
		}
	}
}

// kindID returns the ID of the group's seeded kind with a system code,
// seeding the defaults first (seeding is idempotent).
func kindID(t *testing.T, harness *postgres.Harness, scope service.KindScope, groupID int64, code string) int64 {
	t.Helper()
	seedKinds(t, harness, groupID)
	table, err := tableFor(scope)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := harness.DB.QueryRowContext(context.Background(), `SELECT id FROM `+table.table+` WHERE group_id = $1 AND system_code = $2`, groupID, code).Scan(&id); err != nil {
		t.Fatalf("kind %s/%s of group %d: %v", scope, code, groupID, err)
	}
	return id
}

func kindNames(kinds []service.Kind) []string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, kind.NameEN)
	}
	return names
}

func TestKindRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	kinds := mustKindRepository(t, harness)
	events := mustEventRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatal(err)
	}

	// Seeding gives both lists, once, with the old labels.
	seedKinds(t, harness, group.ID, other.ID)
	seedKinds(t, harness, group.ID)
	eventKinds, err := kinds.ListKinds(ctx, service.KindScopeEvent, group.ID)
	if err != nil || !slices.Equal(kindNames(eventKinds), []string{"Training", "Match", "Championship", "Other"}) {
		t.Fatalf("event kinds = %v, %v", kindNames(eventKinds), err)
	}
	if eventKinds[0].SystemCode != service.EventKindTraining || eventKinds[0].NameHU != "Edzés" || eventKinds[0].Position != 0 || eventKinds[3].Position != 3 {
		t.Fatalf("event kinds = %+v", eventKinds)
	}
	resourceKinds, err := kinds.ListKinds(ctx, service.KindScopeResource, group.ID)
	if err != nil || len(resourceKinds) != 6 || resourceKinds[2].SystemCode != "hall" || resourceKinds[2].NameHU != "Sportcsarnok" {
		t.Fatalf("resource kinds = %+v, %v", resourceKinds, err)
	}

	// Create appends, names are unique among active kinds of a group only.
	camp, err := kinds.CreateKind(ctx, service.KindScopeEvent, service.Kind{GroupID: group.ID, NameEN: "Camp", NameHU: "Tábor", CreatedAt: 110})
	if err != nil || camp.Position != 4 || camp.SystemCode != "" || camp.Version != 0 || camp.Uses != 0 {
		t.Fatalf("CreateKind() = %+v, %v", camp, err)
	}
	if _, err := kinds.CreateKind(ctx, service.KindScopeEvent, service.Kind{GroupID: group.ID, NameEN: "CAMP", CreatedAt: 110}); !errors.Is(err, service.ErrKindNameTaken) {
		t.Fatalf("duplicate name error = %v", err)
	}
	if _, err := kinds.CreateKind(ctx, service.KindScopeEvent, service.Kind{GroupID: other.ID, NameEN: "Camp", CreatedAt: 110}); err != nil {
		t.Fatalf("the same name in another group error = %v", err)
	}
	if _, err := kinds.CreateKind(ctx, service.KindScopeResource, service.Kind{GroupID: group.ID, NameEN: "Camp", CreatedAt: 110}); err != nil {
		t.Fatalf("the same name in the other list error = %v", err)
	}

	// Another group's kind is invisible.
	if _, err := kinds.FindKind(ctx, service.KindScopeEvent, other.ID, camp.ID); !errors.Is(err, service.ErrKindNotFound) {
		t.Fatalf("FindKind(other group) error = %v", err)
	}
	if _, err := kinds.FindKind(ctx, service.KindScope("bogus"), group.ID, camp.ID); !errors.Is(err, service.ErrInvalidKindScope) {
		t.Fatalf("FindKind(bogus scope) error = %v", err)
	}

	// Update is revision checked; a vanished kind is not-found.
	renamed := camp
	renamed.NameEN = "Summer camp"
	updated, err := kinds.UpdateKind(ctx, service.KindScopeEvent, renamed, camp.Version)
	if err != nil || updated.NameEN != "Summer camp" || updated.Version != 1 {
		t.Fatalf("UpdateKind() = %+v, %v", updated, err)
	}
	if _, err := kinds.UpdateKind(ctx, service.KindScopeEvent, renamed, camp.Version); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale UpdateKind() error = %v, want a conflict", err)
	}
	if _, err := kinds.UpdateKind(ctx, service.KindScopeEvent, service.Kind{ID: 999999, GroupID: group.ID, NameEN: "Ghost"}, 0); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("UpdateKind(vanished) error = %v, want not found", err)
	}
	// Archiving frees the name; restoring into a taken name is refused.
	archivedAt := int64(120)
	archived := updated
	archived.ArchivedAt = &archivedAt
	archived, err = kinds.UpdateKind(ctx, service.KindScopeEvent, archived, updated.Version)
	if err != nil || !archived.Archived() {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	second, err := kinds.CreateKind(ctx, service.KindScopeEvent, service.Kind{GroupID: group.ID, NameEN: "Summer camp", CreatedAt: 130})
	if err != nil {
		t.Fatalf("a name freed by archiving error = %v", err)
	}
	restored := archived
	restored.ArchivedAt = nil
	if _, err := kinds.UpdateKind(ctx, service.KindScopeEvent, restored, archived.Version); !errors.Is(err, service.ErrKindNameTaken) {
		t.Fatalf("restore into a taken name error = %v", err)
	}

	// Move swaps neighbours and renumbers; the ends stay put.
	if err := kinds.MoveKind(ctx, service.KindScopeEvent, group.ID, eventKinds[0].ID, -1); err != nil {
		t.Fatalf("MoveKind(first up) error = %v", err)
	}
	if err := kinds.MoveKind(ctx, service.KindScopeEvent, group.ID, eventKinds[0].ID, 1); err != nil {
		t.Fatalf("MoveKind(first down) error = %v", err)
	}
	moved, _ := kinds.ListKinds(ctx, service.KindScopeEvent, group.ID)
	if !slices.Equal(kindNames(moved), []string{"Match", "Training", "Championship", "Other", "Summer camp", "Summer camp"}) {
		t.Fatalf("after moving = %v", kindNames(moved))
	}
	for i, kind := range moved {
		if kind.Position != i {
			t.Fatalf("positions not renumbered: %+v", moved)
		}
	}
	if err := kinds.MoveKind(ctx, service.KindScopeEvent, other.ID, eventKinds[0].ID, 1); !errors.Is(err, service.ErrKindNotFound) {
		t.Fatalf("MoveKind(another group's kind) error = %v", err)
	}

	// A kind in use cannot be deleted; an unused one can, at its revision.
	training := kindID(t, harness, service.KindScopeEvent, group.ID, service.EventKindTraining)
	if _, err := events.CreateEvent(ctx, service.Event{GroupID: group.ID, KindID: training, Title: "Practice", StartsAt: eventInstant("2026-11-02T16:00:00Z"), EndsAt: eventInstant("2026-11-02T17:00:00Z"),
		Location: new("Park"), SignupMode: service.EventSignupOpen, CreatedAt: 200}); err != nil {
		t.Fatal(err)
	}
	field := kindID(t, harness, service.KindScopeResource, group.ID, "field")
	if _, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Pitch", KindID: field, CreatedAt: 200}); err != nil {
		t.Fatal(err)
	}
	if err := kinds.DeleteKind(ctx, service.KindScopeEvent, group.ID, training, 0); !errors.Is(err, service.ErrKindInUse) {
		t.Fatalf("DeleteKind(in use event kind) error = %v, want in use", err)
	}
	if err := kinds.DeleteKind(ctx, service.KindScopeResource, group.ID, field, 0); !errors.Is(err, service.ErrKindInUse) {
		t.Fatalf("DeleteKind(in use resource kind) error = %v, want in use", err)
	}
	inUse, err := kinds.FindKind(ctx, service.KindScopeEvent, group.ID, training)
	if err != nil || inUse.Uses != 1 {
		t.Fatalf("Uses = %d, %v, want 1", inUse.Uses, err)
	}
	if err := kinds.DeleteKind(ctx, service.KindScopeEvent, group.ID, second.ID, 5); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale DeleteKind() error = %v, want a conflict", err)
	}
	if err := kinds.DeleteKind(ctx, service.KindScopeEvent, other.ID, second.ID, second.Version); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("DeleteKind(another group's kind) error = %v, want not found", err)
	}
	if err := kinds.DeleteKind(ctx, service.KindScopeEvent, group.ID, second.ID, second.Version); err != nil {
		t.Fatalf("DeleteKind() error = %v", err)
	}
	if err := kinds.DeleteKind(ctx, service.KindScopeEvent, group.ID, second.ID, second.Version); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("DeleteKind(again) error = %v, want not found", err)
	}

	// Kinds never cross groups: events and resources refuse a kind of
	// another group, and an event kind is not a resource kind.
	foreignKind := kindID(t, harness, service.KindScopeEvent, other.ID, service.EventKindTraining)
	if _, err := events.CreateEvent(ctx, service.Event{GroupID: group.ID, KindID: foreignKind, Title: "Bad", StartsAt: eventInstant("2026-11-03T16:00:00Z"), EndsAt: eventInstant("2026-11-03T17:00:00Z"),
		Location: new("Park"), SignupMode: service.EventSignupOpen, CreatedAt: 200}); !errors.Is(err, service.ErrInvalidEventKind) {
		t.Fatalf("CreateEvent(another group's kind) error = %v, want invalid kind", err)
	}
	foreignResourceKind := kindID(t, harness, service.KindScopeResource, other.ID, "field")
	if _, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Bad", KindID: foreignResourceKind, CreatedAt: 200}); !errors.Is(err, service.ErrInvalidResourceKind) {
		t.Fatalf("CreateResource(another group's kind) error = %v, want invalid kind", err)
	}
	pitch, err := resources.ListResources(ctx, group.ID, false)
	if err != nil || len(pitch) != 1 || pitch[0].KindCode != "field" || pitch[0].KindNameEN != "Field" || pitch[0].KindNameHU != "Füves pálya" {
		t.Fatalf("resources = %+v, %v, want the kind names on reads", pitch, err)
	}
}

func TestKindMigrationMapsExistingRows(t *testing.T) {
	ctx := context.Background()
	harness := postgres.New(t)
	// Install migrations up to 000013, add rows with the old fixed codes,
	// then install the rest.
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".up.sql") && name < "000014" {
			data, err := fs.ReadFile(migrations.FS, name)
			if err != nil {
				t.Fatal(err)
			}
			before[name] = &fstest.MapFile{Data: data}
		}
	}
	apply := func(source fs.FS) {
		t.Helper()
		runner, err := store.NewMigrator(harness.DB, source)
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Apply(ctx); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	}
	apply(before)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	var groupA, groupB, courtID, matchID, otherID int64
	for _, group := range []struct {
		slug string
		id   *int64
	}{{"club-a", &groupA}, {"club-b", &groupB}} {
		if err := harness.DB.QueryRowContext(ctx, `INSERT INTO groups (name, slug, name_norm, created_by, created_at) VALUES ($1, $1, $1, $2, 100) RETURNING id`, group.slug, adminID).Scan(group.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := harness.DB.QueryRowContext(ctx, `INSERT INTO resources (group_id, name, kind, created_at) VALUES ($1, 'Court', 'court', 100) RETURNING id`, groupA).Scan(&courtID); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		group int64
		kind  string
		id    *int64
	}{{groupA, "match", &matchID}, {groupB, "other", &otherID}} {
		if err := harness.DB.QueryRowContext(ctx, `INSERT INTO events (group_id, kind, title, starts_at, ends_at, location, created_at)
			VALUES ($1, $2, 'Old', '2026-11-02T16:00:00Z', '2026-11-02T17:00:00Z', 'Park', 100) RETURNING id`, event.group, event.kind).Scan(event.id); err != nil {
			t.Fatal(err)
		}
	}
	apply(migrations.FS)

	check := func(query string, id int64, wantGroup int64, wantCode, wantEN, wantHU string) {
		t.Helper()
		var group int64
		var code, en, hu string
		if err := harness.DB.QueryRowContext(ctx, query, id).Scan(&group, &code, &en, &hu); err != nil {
			t.Fatal(err)
		}
		if group != wantGroup || code != wantCode || en != wantEN || hu != wantHU {
			t.Errorf("row %d mapped to group %d %q %q %q, want group %d %q %q %q", id, group, code, en, hu, wantGroup, wantCode, wantEN, wantHU)
		}
	}
	check(`SELECT k.group_id, k.system_code, k.name_en, k.name_hu FROM resources r JOIN resource_kinds k ON k.id = r.kind_id AND k.group_id = r.group_id WHERE r.id = $1`, courtID, groupA, "court", "Court", "Pálya")
	check(`SELECT k.group_id, k.system_code, k.name_en, k.name_hu FROM events e JOIN event_kinds k ON k.id = e.kind_id AND k.group_id = e.group_id WHERE e.id = $1`, matchID, groupA, "match", "Match", "Mérkőzés")
	check(`SELECT k.group_id, k.system_code, k.name_en, k.name_hu FROM events e JOIN event_kinds k ON k.id = e.kind_id AND k.group_id = e.group_id WHERE e.id = $1`, otherID, groupB, "other", "Other", "Egyéb")
	for _, group := range []int64{groupA, groupB} {
		var events, resources int
		if err := harness.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM event_kinds WHERE group_id = $1), (SELECT count(*) FROM resource_kinds WHERE group_id = $1)`, group).Scan(&events, &resources); err != nil {
			t.Fatal(err)
		}
		if events != 4 || resources != 6 {
			t.Errorf("group %d has %d event kinds and %d resource kinds, want the defaults (4, 6)", group, events, resources)
		}
	}
}
