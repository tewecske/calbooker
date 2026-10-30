package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type resourceRepositoryStub struct {
	resources       map[int64]Resource
	createErr       error
	updateErr       error
	listErr         error
	expectedVersion int64
	updated         Resource
	includeArchived []bool
}

func newResourceStub(resources ...Resource) *resourceRepositoryStub {
	stub := &resourceRepositoryStub{resources: make(map[int64]Resource)}
	for _, resource := range resources {
		stub.resources[resource.ID] = resource
	}
	return stub
}

func (r *resourceRepositoryStub) CreateResource(_ context.Context, resource Resource) (Resource, error) {
	if r.createErr != nil {
		return Resource{}, r.createErr
	}
	resource.ID = 99
	r.resources[resource.ID] = resource
	return resource, nil
}

func (r *resourceRepositoryStub) FindResource(_ context.Context, groupID, resourceID int64) (Resource, error) {
	resource, ok := r.resources[resourceID]
	if !ok || resource.GroupID != groupID {
		return Resource{}, ErrResourceNotFound
	}
	return resource, nil
}

func (r *resourceRepositoryStub) ListResources(_ context.Context, groupID int64, includeArchived bool) ([]Resource, error) {
	r.includeArchived = append(r.includeArchived, includeArchived)
	if r.listErr != nil {
		return nil, r.listErr
	}
	list := make([]Resource, 0, len(r.resources))
	for _, resource := range r.resources {
		if resource.GroupID == groupID && (includeArchived || !resource.Archived()) {
			list = append(list, resource)
		}
	}
	return list, nil
}

func (r *resourceRepositoryStub) UpdateResource(_ context.Context, resource Resource, expectedVersion int64) (Resource, error) {
	r.expectedVersion = expectedVersion
	if r.updateErr != nil {
		return Resource{}, r.updateErr
	}
	resource.Version++
	r.updated = resource
	return resource, nil
}

func newTestResourceService(t *testing.T, resources *resourceRepositoryStub) (*ResourceService, *auditRecorderStub) {
	t.Helper()
	service, err := NewResourceService(resources, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewResourceService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	return service, auditor
}

func siteAdminActor() GroupActor {
	return actorWith(GroupAccessGrants{SiteAdmin: true})
}

func parentActor() GroupActor {
	return actorWith(memberWith(GroupRoleParent))
}

func TestNewResourceServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewResourceService(nil, usersWith()); !errors.Is(err, ErrNilResourceRepository) {
		t.Fatalf("nil repository error = %v", err)
	}
	if _, err := NewResourceService(newResourceStub(), nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
	var missing *ResourceService
	if _, err := missing.List(context.Background(), adminActor().Access); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("nil service error = %v", err)
	}
}

func TestResourceServiceCreate(t *testing.T) {
	valid := ResourceInput{Name: " Main field ", KindID: 1, Description: " North end ", MaxConcurrentTeams: 2}
	tests := []struct {
		name    string
		actor   GroupActor
		input   ResourceInput
		wantErr error
	}{
		{name: "admin creates", actor: adminActor(), input: valid},
		{name: "manager creates without limit or description", actor: actorWith(memberWith(GroupRoleManager)), input: ResourceInput{Name: "Pool", KindID: 1}},
		{name: "site administrator creates", actor: siteAdminActor(), input: valid},
		{name: "coach is forbidden", actor: coachOf(1), input: valid, wantErr: ErrGroupForbidden},
		{name: "player is forbidden", actor: playerOn(1), input: valid, wantErr: ErrGroupForbidden},
		{name: "parent is forbidden", actor: parentActor(), input: valid, wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), input: valid, wantErr: ErrGroupNotFound},
		{name: "empty name", actor: adminActor(), input: ResourceInput{Name: " ", KindID: 1}, wantErr: ErrInvalidResourceName},
		{name: "long name", actor: adminActor(), input: ResourceInput{Name: strings.Repeat("a", 65), KindID: 1}, wantErr: ErrInvalidResourceName},
		{name: "control character in name", actor: adminActor(), input: ResourceInput{Name: "A\nB", KindID: 1}, wantErr: ErrInvalidResourceName},
		{name: "unknown kind", actor: adminActor(), input: ResourceInput{Name: "A", KindID: -1}, wantErr: ErrInvalidResourceKind},
		{name: "missing kind", actor: adminActor(), input: ResourceInput{Name: "A"}, wantErr: ErrInvalidResourceKind},
		{name: "long description", actor: adminActor(), input: ResourceInput{Name: "A", KindID: 1, Description: strings.Repeat("d", 256)}, wantErr: ErrInvalidResourceDescription},
		{name: "negative limit", actor: adminActor(), input: ResourceInput{Name: "A", KindID: 1, MaxConcurrentTeams: -1}, wantErr: ErrInvalidResourceCapacity},
		{name: "limit too high", actor: adminActor(), input: ResourceInput{Name: "A", KindID: 1, MaxConcurrentTeams: 100}, wantErr: ErrInvalidResourceCapacity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resources := newResourceStub()
			service, auditor := newTestResourceService(t, resources)
			created, err := service.Create(context.Background(), test.actor, test.input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Create() error = %v, want %v", err, test.wantErr)
				}
				if len(auditor.records) != 0 || len(resources.resources) != 0 {
					t.Fatal("failed create was stored or audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if created.GroupID != 5 || created.Name != strings.TrimSpace(test.input.Name) || created.KindID != test.input.KindID || created.CreatedAt <= 0 {
				t.Fatalf("created = %+v, want trimmed resource in group 5", created)
			}
			if test.input.MaxConcurrentTeams == 0 && created.MaxConcurrentTeams != nil {
				t.Fatal("zero limit must be stored as no limit")
			}
			if test.input.Description == "" && created.Description != nil {
				t.Fatal("empty description must be stored as none")
			}
			if len(auditor.records) != 1 {
				t.Fatalf("audit records = %d, want 1", len(auditor.records))
			}
			record := auditor.records[0]
			if record.Action != AuditActionResourceCreated || record.TargetType != "group" || record.TargetID != "5" || record.IP != "192.0.2.1" || !strings.Contains(record.Detail, "resource 99") {
				t.Fatalf("audit record = %+v", record)
			}
			if test.actor.Access.IsSiteAdmin() != strings.Contains(record.Detail, "(site administrator)") {
				t.Fatalf("site administrator marking wrong in %q", record.Detail)
			}
		})
	}
}

func TestResourceServiceCreateNameTaken(t *testing.T) {
	resources := newResourceStub()
	resources.createErr = ErrResourceNameTaken
	service, auditor := newTestResourceService(t, resources)
	if _, err := service.Create(context.Background(), adminActor(), ResourceInput{Name: "Main", KindID: 1}); !errors.Is(err, ErrResourceNameTaken) {
		t.Fatalf("Create() error = %v, want ErrResourceNameTaken", err)
	}
	resources.createErr = errors.New("database down")
	if _, err := service.Create(context.Background(), adminActor(), ResourceInput{Name: "Main", KindID: 1}); err == nil || errors.Is(err, ErrResourceNameTaken) {
		t.Fatalf("Create() error = %v, want a wrapped storage error", err)
	}
	if len(auditor.records) != 0 {
		t.Fatal("failed creates were audited")
	}
}

func TestResourceServiceListAndActive(t *testing.T) {
	archivedAt := int64(10)
	resources := newResourceStub(
		Resource{ID: 1, GroupID: 5, Name: "Main", KindID: 1},
		Resource{ID: 2, GroupID: 5, Name: "Old hall", KindID: 1, ArchivedAt: &archivedAt},
		Resource{ID: 3, GroupID: 6, Name: "Other club", KindID: 1},
	)
	tests := []struct {
		name    string
		actor   GroupActor
		wantErr error
	}{
		{name: "admin", actor: adminActor()},
		{name: "manager", actor: actorWith(memberWith(GroupRoleManager))},
		{name: "coach reads", actor: coachOf()},
		{name: "site administrator", actor: siteAdminActor()},
		{name: "player is forbidden", actor: playerOn(1), wantErr: ErrGroupForbidden},
		{name: "parent is forbidden", actor: parentActor(), wantErr: ErrGroupForbidden},
		{name: "member without roles is forbidden", actor: actorWith(memberWith()), wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := newTestResourceService(t, resources)
			all, err := service.List(context.Background(), test.actor.Access)
			active, activeErr := service.ActiveResources(context.Background(), test.actor.Access)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || !errors.Is(activeErr, test.wantErr) {
					t.Fatalf("List() error = %v, ActiveResources() error = %v, want %v", err, activeErr, test.wantErr)
				}
				return
			}
			if err != nil || activeErr != nil {
				t.Fatalf("List() error = %v, ActiveResources() error = %v", err, activeErr)
			}
			if len(all) != 2 {
				t.Fatalf("List() = %+v, want both resources of group 5", all)
			}
			if len(active) != 1 || active[0].ID != 1 {
				t.Fatalf("ActiveResources() = %+v, want only the active resource", active)
			}
		})
	}
	resources.listErr = errors.New("database down")
	service, _ := newTestResourceService(t, resources)
	if _, err := service.List(context.Background(), adminActor().Access); err == nil {
		t.Fatal("List() hid a storage error")
	}
}

func TestResourceServiceFindAndUsable(t *testing.T) {
	archivedAt := int64(10)
	resources := newResourceStub(
		Resource{ID: 1, GroupID: 5, Name: "Main", KindID: 1},
		Resource{ID: 2, GroupID: 5, Name: "Old hall", KindID: 1, ArchivedAt: &archivedAt},
		Resource{ID: 3, GroupID: 6, Name: "Other club", KindID: 1},
	)
	service, _ := newTestResourceService(t, resources)
	ctx := context.Background()
	// Any member loads a referenced resource, archived or not.
	for _, actor := range []GroupActor{adminActor(), coachOf(), playerOn(1), parentActor()} {
		if found, err := service.Find(ctx, actor.Access, 2); err != nil || found.ID != 2 {
			t.Fatalf("Find(archived) = %+v, %v", found, err)
		}
	}
	if _, err := service.Find(ctx, adminActor().Access, 3); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("Find(other group) error = %v, want ErrResourceNotFound", err)
	}
	if _, err := service.Find(ctx, adminActor().Access, 0); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("Find(0) error = %v, want ErrResourceNotFound", err)
	}
	if _, err := service.Find(ctx, actorWith(GroupAccessGrants{}).Access, 1); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Find() error = %v, want ErrGroupNotFound", err)
	}
	if usable, err := service.Usable(ctx, coachOf().Access, 1); err != nil || usable.ID != 1 {
		t.Fatalf("Usable(active) = %+v, %v", usable, err)
	}
	if _, err := service.Usable(ctx, adminActor().Access, 2); !errors.Is(err, ErrResourceArchived) {
		t.Fatalf("Usable(archived) error = %v, want ErrResourceArchived", err)
	}
	if _, err := service.Usable(ctx, adminActor().Access, 3); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("Usable(other group) error = %v, want ErrResourceNotFound", err)
	}
}

func TestResourceServiceUpdate(t *testing.T) {
	stored := Resource{ID: 1, GroupID: 5, Name: "Main", KindID: 1, Version: 2}
	other := Resource{ID: 3, GroupID: 6, Name: "Other club", KindID: 1}
	input := ResourceInput{Name: "Main pitch", KindID: 1, MaxConcurrentTeams: 4}
	tests := []struct {
		name       string
		actor      GroupActor
		resourceID int64
		version    int64
		input      ResourceInput
		updateErr  error
		wantErr    error
	}{
		{name: "admin edits", actor: adminActor(), resourceID: 1, version: 2, input: input},
		{name: "manager edits", actor: actorWith(memberWith(GroupRoleManager)), resourceID: 1, version: 2, input: input},
		{name: "coach is forbidden", actor: coachOf(), resourceID: 1, version: 2, input: input, wantErr: ErrGroupForbidden},
		{name: "player is forbidden", actor: playerOn(), resourceID: 1, version: 2, input: input, wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), resourceID: 1, version: 2, input: input, wantErr: ErrGroupNotFound},
		{name: "other group's resource is not found", actor: adminActor(), resourceID: 3, version: 0, input: input, wantErr: ErrResourceNotFound},
		{name: "invalid id", actor: adminActor(), resourceID: 0, version: 2, input: input, wantErr: ErrResourceNotFound},
		{name: "negative version", actor: adminActor(), resourceID: 1, version: -1, input: input, wantErr: ErrInvalidGroup},
		{name: "invalid input", actor: adminActor(), resourceID: 1, version: 2, input: ResourceInput{Name: "A", KindID: -1}, wantErr: ErrInvalidResourceKind},
		{name: "stale revision is a conflict", actor: adminActor(), resourceID: 1, version: 1, input: input, updateErr: ErrOptimisticLockConflict, wantErr: ErrOptimisticLockConflict},
		{name: "vanished resource is not found", actor: adminActor(), resourceID: 1, version: 2, input: input, updateErr: ErrRecordNotFound, wantErr: ErrResourceNotFound},
		{name: "duplicate name", actor: adminActor(), resourceID: 1, version: 2, input: input, updateErr: ErrResourceNameTaken, wantErr: ErrResourceNameTaken},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resources := newResourceStub(stored, other)
			resources.updateErr = test.updateErr
			service, auditor := newTestResourceService(t, resources)
			updated, err := service.Update(context.Background(), test.actor, test.resourceID, test.version, test.input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Update() error = %v, want %v", err, test.wantErr)
				}
				if len(auditor.records) != 0 {
					t.Fatal("failed update was audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if updated.Name != "Main pitch" || updated.MaxConcurrentTeams == nil || *updated.MaxConcurrentTeams != 4 || resources.expectedVersion != 2 {
				t.Fatalf("updated = %+v (expected version %d)", updated, resources.expectedVersion)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionResourceUpdated || !strings.Contains(auditor.records[0].Detail, "max teams 4") {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}
	resources := newResourceStub(stored)
	resources.updateErr = errors.New("database down")
	service, _ := newTestResourceService(t, resources)
	if _, err := service.Update(context.Background(), adminActor(), 1, 2, input); err == nil || errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("storage error = %v, want wrapped", err)
	}
}

func TestResourceServiceSetArchived(t *testing.T) {
	resources := newResourceStub(Resource{ID: 1, GroupID: 5, Name: "Main", KindID: 1, Version: 2})
	service, auditor := newTestResourceService(t, resources)
	ctx := context.Background()

	archived, err := service.SetArchived(ctx, adminActor(), 1, 2, true)
	if err != nil || !archived.Archived() || resources.expectedVersion != 2 {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	resources.resources[1] = archived
	// Archiving again changes nothing and records nothing.
	if again, err := service.SetArchived(ctx, adminActor(), 1, archived.Version, true); err != nil || again.Version != archived.Version {
		t.Fatalf("repeat archive = %+v, %v", again, err)
	}
	restored, err := service.SetArchived(ctx, actorWith(memberWith(GroupRoleManager)), 1, archived.Version, false)
	if err != nil || restored.Archived() {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	if len(auditor.records) != 2 || auditor.records[0].Action != AuditActionResourceArchived || auditor.records[1].Action != AuditActionResourceRestored {
		t.Fatalf("audit = %+v", auditor.records)
	}

	if _, err := service.SetArchived(ctx, coachOf(), 1, 3, true); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("coach archive error = %v, want ErrGroupForbidden", err)
	}
	if _, err := service.SetArchived(ctx, actorWith(GroupAccessGrants{}), 1, 3, true); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider archive error = %v, want ErrGroupNotFound", err)
	}
	resources.resources[1] = archived
	resources.updateErr = ErrResourceNameTaken
	if _, err := service.SetArchived(ctx, adminActor(), 1, archived.Version, false); !errors.Is(err, ErrResourceNameTaken) {
		t.Fatalf("restore over a taken name error = %v, want ErrResourceNameTaken", err)
	}
	resources.updateErr = ErrOptimisticLockConflict
	if _, err := service.SetArchived(ctx, adminActor(), 1, 0, false); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("stale restore error = %v, want conflict", err)
	}
	if len(auditor.records) != 2 {
		t.Fatal("failed archive changes were audited")
	}
}
