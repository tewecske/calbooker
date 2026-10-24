//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/tests/postgres"
)

func mustResourceRepository(t *testing.T, harness *postgres.Harness) *ResourceRepository {
	t.Helper()
	repository, err := NewResourceRepository(harness.DB)
	if err != nil {
		t.Fatalf("NewResourceRepository() error = %v", err)
	}
	return repository
}

func TestResourceRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")

	group, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin() error = %v", err)
	}

	description := "North end"
	limit := 2
	created, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Main field", Kind: service.ResourceKindField, Description: &description, MaxConcurrentTeams: &limit, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	if created.ID <= 0 || created.Version != 0 || created.Description == nil || *created.Description != description || created.MaxConcurrentTeams == nil || *created.MaxConcurrentTeams != 2 {
		t.Fatalf("created = %+v", created)
	}
	pool, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Pool", Kind: service.ResourceKindPool, CreatedAt: 102})
	if err != nil || pool.MaxConcurrentTeams != nil || pool.Description != nil {
		t.Fatalf("CreateResource(no limit) = %+v, %v", pool, err)
	}

	// Constraints refuse what validation would refuse.
	for _, bad := range []service.Resource{
		{GroupID: group.ID, Name: "Bad kind", Kind: "stadium", CreatedAt: 103},
		{GroupID: group.ID, Name: " ", Kind: service.ResourceKindRoom, CreatedAt: 103},
		{GroupID: group.ID, Name: "Zero", Kind: service.ResourceKindRoom, MaxConcurrentTeams: new(int), CreatedAt: 103},
	} {
		if _, err := resources.CreateResource(ctx, bad); err == nil {
			t.Errorf("CreateResource(%+v) stored an invalid resource", bad)
		}
	}

	// Names are unique among active resources, ignoring case.
	if _, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "MAIN FIELD", Kind: service.ResourceKindCourt, CreatedAt: 104}); !errors.Is(err, service.ErrResourceNameTaken) {
		t.Fatalf("duplicate name error = %v, want ErrResourceNameTaken", err)
	}

	created.Name = "Main pitch"
	created.MaxConcurrentTeams = nil
	updated, err := resources.UpdateResource(ctx, created, 0)
	if err != nil || updated.Name != "Main pitch" || updated.Version != 1 || updated.MaxConcurrentTeams != nil {
		t.Fatalf("UpdateResource() = %+v, %v", updated, err)
	}
	if _, err := resources.UpdateResource(ctx, created, 0); !errors.Is(err, service.ErrOptimisticLockConflict) {
		t.Fatalf("stale UpdateResource() error = %v, want ErrOptimisticLockConflict", err)
	}
	renamed := updated
	renamed.Name = "pool"
	if _, err := resources.UpdateResource(ctx, renamed, updated.Version); !errors.Is(err, service.ErrResourceNameTaken) {
		t.Fatalf("rename onto an active name error = %v, want ErrResourceNameTaken", err)
	}

	// Archiving frees the name; restoring fails while another resource holds it.
	archivedAt := int64(200)
	updated.ArchivedAt = &archivedAt
	archived, err := resources.UpdateResource(ctx, updated, updated.Version)
	if err != nil || !archived.Archived() {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	if found, err := resources.FindResource(ctx, group.ID, archived.ID); err != nil || !found.Archived() {
		t.Fatalf("FindResource(archived) = %+v, %v; archived resources stay loadable", found, err)
	}
	successor, err := resources.CreateResource(ctx, service.Resource{GroupID: group.ID, Name: "Main Pitch", Kind: service.ResourceKindField, CreatedAt: 201})
	if err != nil {
		t.Fatalf("CreateResource(reused name) error = %v", err)
	}
	archived.ArchivedAt = nil
	if _, err := resources.UpdateResource(ctx, archived, archived.Version); !errors.Is(err, service.ErrResourceNameTaken) {
		t.Fatalf("restore over a taken name error = %v, want ErrResourceNameTaken", err)
	}

	all, err := resources.ListResources(ctx, group.ID, true)
	if err != nil || len(all) != 3 || all[0].ID != successor.ID || all[1].ID != pool.ID || all[2].ID != archived.ID {
		t.Fatalf("ListResources(all) = %+v, %v; want active by name, then archived", all, err)
	}
	active, err := resources.ListResources(ctx, group.ID, false)
	if err != nil || len(active) != 2 {
		t.Fatalf("ListResources(active) = %+v, %v", active, err)
	}
	for _, resource := range active {
		if resource.Archived() {
			t.Fatalf("active list holds archived resource %+v", resource)
		}
	}
}

func TestResourceRepositoryGroupScoping(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")

	groupA, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "A", NameNorm: "a", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(A) error = %v", err)
	}
	groupB, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "B", NameNorm: "b", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(B) error = %v", err)
	}
	resourceA, err := resources.CreateResource(ctx, service.Resource{GroupID: groupA.ID, Name: "Main field", Kind: service.ResourceKindField, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateResource(A) error = %v", err)
	}
	// The same name in another group is fine.
	resourceB, err := resources.CreateResource(ctx, service.Resource{GroupID: groupB.ID, Name: "Main field", Kind: service.ResourceKindField, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateResource(B, same name) error = %v", err)
	}

	if _, err := resources.FindResource(ctx, groupB.ID, resourceA.ID); !errors.Is(err, service.ErrResourceNotFound) {
		t.Fatalf("FindResource(A's resource through B) error = %v, want ErrResourceNotFound", err)
	}
	for _, includeArchived := range []bool{true, false} {
		list, err := resources.ListResources(ctx, groupB.ID, includeArchived)
		if err != nil || len(list) != 1 || list[0].ID != resourceB.ID {
			t.Fatalf("ListResources(B, %v) = %+v, %v; want only B's resource", includeArchived, list, err)
		}
	}
	foreign := resourceA
	foreign.GroupID = groupB.ID
	foreign.Name = "Hijacked"
	if _, err := resources.UpdateResource(ctx, foreign, resourceA.Version); !errors.Is(err, service.ErrRecordNotFound) {
		t.Fatalf("UpdateResource(A's resource through B) error = %v, want ErrRecordNotFound", err)
	}
	if found, err := resources.FindResource(ctx, groupA.ID, resourceA.ID); err != nil || found.Name != "Main field" || found.Version != 0 {
		t.Fatalf("A's resource changed through B: %+v, %v", found, err)
	}

	// Deleting a group removes its resources only.
	if _, err := harness.DB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, groupA.ID); err != nil {
		t.Fatalf("delete group A: %v", err)
	}
	if _, err := resources.FindResource(ctx, groupA.ID, resourceA.ID); !errors.Is(err, service.ErrResourceNotFound) {
		t.Fatalf("resource of a deleted group error = %v, want ErrResourceNotFound", err)
	}
	if _, err := resources.FindResource(ctx, groupB.ID, resourceB.ID); err != nil {
		t.Fatalf("B's resource after deleting A: %v", err)
	}
}

// TestResourceCompositeReference proves that a later table referencing
// (group_id, resource_id) cannot point at another group's resource.
func TestResourceCompositeReference(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	resources := mustResourceRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")
	groupA, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "A", NameNorm: "a", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(A) error = %v", err)
	}
	groupB, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "B", NameNorm: "b", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatalf("CreateGroupWithAdmin(B) error = %v", err)
	}
	resourceA, err := resources.CreateResource(ctx, service.Resource{GroupID: groupA.ID, Name: "Main", Kind: service.ResourceKindField, CreatedAt: 101})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `
		CREATE TABLE resource_refs (
			group_id BIGINT NOT NULL,
			resource_id BIGINT NOT NULL,
			FOREIGN KEY (group_id, resource_id) REFERENCES resources (group_id, id)
		)`); err != nil {
		t.Fatalf("create referencing table: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `INSERT INTO resource_refs VALUES ($1, $2)`, groupA.ID, resourceA.ID); err != nil {
		t.Fatalf("same-group reference refused: %v", err)
	}
	if _, err := harness.DB.ExecContext(ctx, `INSERT INTO resource_refs VALUES ($1, $2)`, groupB.ID, resourceA.ID); err == nil {
		t.Fatal("cross-group reference was accepted")
	}
}
