package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// kindRepositoryStub keeps kinds of both scopes in memory, scoped by group.
type kindRepositoryStub struct {
	kinds     map[KindScope]map[int64]Kind
	nextID    int64
	moved     []int
	deleteErr error
	seeded    []int64
	seedErr   error
}

func newKindStub(kinds ...Kind) *kindRepositoryStub {
	stub := &kindRepositoryStub{kinds: map[KindScope]map[int64]Kind{KindScopeEvent: {}, KindScopeResource: {}}, nextID: 100}
	for _, kind := range kinds {
		stub.kinds[KindScopeEvent][kind.ID] = kind
	}
	return stub
}

func (r *kindRepositoryStub) ListKinds(_ context.Context, scope KindScope, groupID int64) ([]Kind, error) {
	var list []Kind
	for _, kind := range r.kinds[scope] {
		if kind.GroupID == groupID {
			list = append(list, kind)
		}
	}
	slices.SortFunc(list, func(a, b Kind) int { return int(a.ID - b.ID) })
	return list, nil
}

func (r *kindRepositoryStub) FindKind(_ context.Context, scope KindScope, groupID, kindID int64) (Kind, error) {
	kind, ok := r.kinds[scope][kindID]
	if !ok || kind.GroupID != groupID {
		return Kind{}, ErrKindNotFound
	}
	return kind, nil
}

func (r *kindRepositoryStub) CreateKind(_ context.Context, scope KindScope, kind Kind) (Kind, error) {
	for _, other := range r.kinds[scope] {
		if other.GroupID == kind.GroupID && !other.Archived() && strings.EqualFold(other.NameEN, kind.NameEN) {
			return Kind{}, ErrKindNameTaken
		}
	}
	r.nextID++
	kind.ID = r.nextID
	r.kinds[scope][kind.ID] = kind
	return kind, nil
}

func (r *kindRepositoryStub) UpdateKind(_ context.Context, scope KindScope, kind Kind, expectedVersion int64) (Kind, error) {
	current, ok := r.kinds[scope][kind.ID]
	if !ok {
		return Kind{}, ErrRecordNotFound
	}
	if current.Version != expectedVersion {
		return Kind{}, ErrOptimisticLockConflict
	}
	kind.Version++
	r.kinds[scope][kind.ID] = kind
	return kind, nil
}

func (r *kindRepositoryStub) MoveKind(_ context.Context, _ KindScope, _, _ int64, offset int) error {
	r.moved = append(r.moved, offset)
	return nil
}

func (r *kindRepositoryStub) DeleteKind(_ context.Context, scope KindScope, _, kindID, _ int64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.kinds[scope], kindID)
	return nil
}

func (r *kindRepositoryStub) SeedDefaultKinds(_ context.Context, groupID, _ int64) error {
	r.seeded = append(r.seeded, groupID)
	return r.seedErr
}

func newTestKindService(t *testing.T, repo *kindRepositoryStub) (*KindService, *auditRecorderStub) {
	t.Helper()
	svc, err := NewKindService(repo, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewKindService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	svc.SetAuditor(auditor)
	return svc, auditor
}

func TestNewKindServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewKindService(nil, usersWith()); !errors.Is(err, ErrNilKindRepository) {
		t.Fatalf("nil repository error = %v", err)
	}
	if _, err := NewKindService(newKindStub(), nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
}

func TestKindServiceReadIsForEveryViewerOfTheGroup(t *testing.T) {
	repo := newKindStub(
		Kind{ID: 1, GroupID: 5, SystemCode: EventKindTraining, NameEN: "Training", NameHU: "Edzés"},
		Kind{ID: 2, GroupID: 5, NameEN: "Camp", ArchivedAt: new(int64(9))},
		Kind{ID: 3, GroupID: 6, NameEN: "Foreign"},
	)
	svc, _ := newTestKindService(t, repo)
	player := playerOn(1).Access
	all, err := svc.All(context.Background(), player, KindScopeEvent)
	if err != nil || len(all) != 2 {
		t.Fatalf("All() = %+v, %v, want the group's two kinds", all, err)
	}
	active, err := svc.Active(context.Background(), player, KindScopeEvent)
	if err != nil || len(active) != 1 || active[0].ID != 1 {
		t.Fatalf("Active() = %+v, %v, want only the unarchived kind", active, err)
	}
	if _, err := svc.Find(context.Background(), player, KindScopeEvent, 3); !errors.Is(err, ErrKindNotFound) {
		t.Fatalf("Find() of another group's kind error = %v, want not found", err)
	}
	if _, err := svc.Usable(context.Background(), player, KindScopeEvent, 3); !errors.Is(err, ErrKindNotFound) {
		t.Fatalf("Usable() of another group's kind error = %v, want not found", err)
	}
	if _, err := svc.Usable(context.Background(), player, KindScopeEvent, 2); !errors.Is(err, ErrKindArchived) {
		t.Fatalf("Usable() of an archived kind error = %v, want archived", err)
	}
	outsider := accessFor(GroupAccessGrants{})
	if _, err := svc.All(context.Background(), outsider, KindScopeEvent); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider error = %v, want not found", err)
	}
	if _, err := svc.All(context.Background(), player, KindScope("other")); !errors.Is(err, ErrInvalidKindScope) {
		t.Fatalf("unknown scope error = %v", err)
	}
}

func TestKindServiceManagePermissions(t *testing.T) {
	tests := []struct {
		name    string
		actor   GroupActor
		wantErr error
	}{
		{name: "admin", actor: adminActor()},
		{name: "manager", actor: actorWith(memberWith(GroupRoleManager))},
		{name: "site administrator", actor: siteAdminActor()},
		{name: "coach", actor: actorWith(memberWith(GroupRoleCoach)), wantErr: ErrGroupForbidden},
		{name: "player", actor: playerOn(1), wantErr: ErrGroupForbidden},
		{name: "parent", actor: parentActor(), wantErr: ErrGroupForbidden},
		{name: "outsider", actor: actorWith(GroupAccessGrants{}), wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newKindStub(
				Kind{ID: 1, GroupID: 5, NameEN: "Training", Version: 2},
				Kind{ID: 2, GroupID: 5, NameEN: "Match", Version: 2},
				Kind{ID: 3, GroupID: 5, NameEN: "Other", Version: 2},
			)
			svc, auditor := newTestKindService(t, repo)
			ctx := context.Background()
			_, createErr := svc.Create(ctx, test.actor, KindScopeEvent, KindInput{NameEN: "Camp"})
			_, renameErr := svc.Rename(ctx, test.actor, KindScopeEvent, 1, 2, KindInput{NameEN: "Practice"})
			_, archiveErr := svc.SetArchived(ctx, test.actor, KindScopeEvent, 2, 2, true)
			moveErr := svc.Move(ctx, test.actor, KindScopeEvent, 1, 1)
			deleteErr := svc.Delete(ctx, test.actor, KindScopeEvent, 3, 2)
			for name, err := range map[string]error{"create": createErr, "rename": renameErr, "archive": archiveErr, "move": moveErr, "delete": deleteErr} {
				if !errors.Is(err, test.wantErr) {
					t.Errorf("%s error = %v, want %v", name, err, test.wantErr)
				}
			}
			if test.wantErr != nil && (len(auditor.records) != 0 || len(repo.moved) != 0) {
				t.Fatal("refused kind changes were audited or applied")
			}
			if test.wantErr == nil && len(auditor.records) != 5 {
				t.Fatalf("audit records = %d, want one per change", len(auditor.records))
			}
		})
	}
}

func TestKindServiceCreateAndRename(t *testing.T) {
	repo := newKindStub(Kind{ID: 1, GroupID: 5, NameEN: "Training", Version: 2})
	svc, auditor := newTestKindService(t, repo)
	ctx := context.Background()
	created, err := svc.Create(ctx, adminActor(), KindScopeEvent, KindInput{NameEN: "  Camp ", NameHU: " Tábor "})
	if err != nil || created.GroupID != 5 || created.NameEN != "Camp" || created.NameHU != "Tábor" || created.CreatedAt <= 0 || created.SystemCode != "" {
		t.Fatalf("Create() = %+v, %v, want a trimmed kind of group 5 without system code", created, err)
	}
	record := auditor.records[0]
	if record.Action != "group.event_kind.created" || record.TargetType != "group" || record.TargetID != "5" || !strings.Contains(record.Detail, "Camp") {
		t.Fatalf("audit record = %+v", record)
	}
	for name, input := range map[string]KindInput{
		"empty":        {NameEN: " "},
		"long english": {NameEN: strings.Repeat("a", MaxKindNameLength+1)},
		"long hungary": {NameEN: "a", NameHU: strings.Repeat("a", MaxKindNameLength+1)},
		"control":      {NameEN: "a\nb"},
	} {
		if _, err := svc.Create(ctx, adminActor(), KindScopeEvent, input); !errors.Is(err, ErrInvalidKindName) {
			t.Errorf("%s: Create() error = %v, want invalid name", name, err)
		}
	}
	if _, err := svc.Create(ctx, adminActor(), KindScopeEvent, KindInput{NameEN: "camp"}); !errors.Is(err, ErrKindNameTaken) {
		t.Fatalf("duplicate name error = %v", err)
	}
	renamed, err := svc.Rename(ctx, adminActor(), KindScopeEvent, 1, 2, KindInput{NameEN: "Practice"})
	if err != nil || renamed.NameEN != "Practice" || renamed.Version != 3 {
		t.Fatalf("Rename() = %+v, %v", renamed, err)
	}
	if _, err := svc.Rename(ctx, adminActor(), KindScopeEvent, 1, 1, KindInput{NameEN: "Old"}); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("stale rename error = %v, want conflict", err)
	}
	if _, err := svc.Rename(ctx, adminActor(), KindScopeEvent, 404, 0, KindInput{NameEN: "Old"}); !errors.Is(err, ErrKindNotFound) {
		t.Fatalf("missing kind error = %v, want not found", err)
	}
	foreign := actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleAdmin}})
	foreign.Access = NewGroupAccess(6, 9, GroupAccessGrants{Member: true, Roles: []string{GroupRoleAdmin}})
	if _, err := svc.Rename(ctx, foreign, KindScopeEvent, 1, 2, KindInput{NameEN: "Hijack"}); !errors.Is(err, ErrKindNotFound) {
		t.Fatalf("another group's admin error = %v, want not found", err)
	}
}

func TestKindServiceVanishedKindIsNotFound(t *testing.T) {
	repo := newKindStub(Kind{ID: 1, GroupID: 5, NameEN: "Training"})
	svc, _ := newTestKindService(t, repo)
	delete(repo.kinds[KindScopeEvent], 1)
	repo.kinds[KindScopeEvent][2] = Kind{ID: 2, GroupID: 5, NameEN: "Camp"}
	stale := &vanishingKindRepository{kindRepositoryStub: repo}
	svc.kinds = stale
	if _, err := svc.Rename(context.Background(), adminActor(), KindScopeEvent, 2, 0, KindInput{NameEN: "Camp 2"}); !errors.Is(err, ErrKindNotFound) {
		t.Fatalf("vanished kind error = %v, want not found, never a conflict", err)
	}
}

// vanishingKindRepository finds a kind but reports it gone on write.
type vanishingKindRepository struct{ *kindRepositoryStub }

func (r *vanishingKindRepository) UpdateKind(context.Context, KindScope, Kind, int64) (Kind, error) {
	return Kind{}, ErrRecordNotFound
}

func TestKindServiceArchiveAndProtectedTraining(t *testing.T) {
	repo := newKindStub(
		Kind{ID: 1, GroupID: 5, SystemCode: EventKindTraining, NameEN: "Training"},
		Kind{ID: 2, GroupID: 5, SystemCode: EventKindMatch, NameEN: "Match"},
	)
	repo.kinds[KindScopeResource] = map[int64]Kind{7: {ID: 7, GroupID: 5, SystemCode: EventKindTraining, NameEN: "Training-like resource"}}
	svc, _ := newTestKindService(t, repo)
	ctx := context.Background()
	if _, err := svc.SetArchived(ctx, adminActor(), KindScopeEvent, 1, 0, true); !errors.Is(err, ErrKindProtected) {
		t.Fatalf("archiving training error = %v, want protected", err)
	}
	if err := svc.Delete(ctx, adminActor(), KindScopeEvent, 1, 0); !errors.Is(err, ErrKindProtected) {
		t.Fatalf("deleting training error = %v, want protected", err)
	}
	if _, err := svc.SetArchived(ctx, adminActor(), KindScopeResource, 7, 0, true); err != nil {
		t.Fatalf("a resource kind with the same code is not protected: %v", err)
	}
	archived, err := svc.SetArchived(ctx, adminActor(), KindScopeEvent, 2, 0, true)
	if err != nil || !archived.Archived() {
		t.Fatalf("archive match = %+v, %v", archived, err)
	}
	restored, err := svc.SetArchived(ctx, adminActor(), KindScopeEvent, 2, 1, false)
	if err != nil || restored.Archived() {
		t.Fatalf("restore match = %+v, %v", restored, err)
	}
	if again, err := svc.SetArchived(ctx, adminActor(), KindScopeEvent, 2, restored.Version, false); err != nil || again.Version != restored.Version {
		t.Fatalf("restoring an active kind changed it: %+v, %v", again, err)
	}
}

func TestKindServiceDelete(t *testing.T) {
	repo := newKindStub(Kind{ID: 2, GroupID: 5, SystemCode: EventKindMatch, NameEN: "Match", Uses: 3})
	svc, auditor := newTestKindService(t, repo)
	ctx := context.Background()
	repo.deleteErr = ErrKindInUse
	if err := svc.Delete(ctx, adminActor(), KindScopeEvent, 2, 0); !errors.Is(err, ErrKindInUse) {
		t.Fatalf("deleting a used kind error = %v, want in use", err)
	}
	if len(auditor.records) != 0 {
		t.Fatal("refused delete was audited")
	}
	repo.deleteErr = ErrOptimisticLockConflict
	if err := svc.Delete(ctx, adminActor(), KindScopeEvent, 2, 0); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("stale delete error = %v, want conflict", err)
	}
	repo.deleteErr = ErrRecordNotFound
	if err := svc.Delete(ctx, adminActor(), KindScopeEvent, 2, 0); !errors.Is(err, ErrKindNotFound) {
		t.Fatalf("vanished delete error = %v, want not found", err)
	}
	repo.deleteErr = nil
	if err := svc.Delete(ctx, adminActor(), KindScopeEvent, 2, 0); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != "group.event_kind.deleted" {
		t.Fatalf("audit records = %+v", auditor.records)
	}
}

func TestKindServiceMoveAndLimit(t *testing.T) {
	repo := newKindStub(Kind{ID: 1, GroupID: 5, NameEN: "Training"})
	svc, _ := newTestKindService(t, repo)
	ctx := context.Background()
	if err := svc.Move(ctx, adminActor(), KindScopeEvent, 1, -1); err != nil || !slices.Equal(repo.moved, []int{-1}) {
		t.Fatalf("Move() = %v, moved %v", err, repo.moved)
	}
	if err := svc.Move(ctx, adminActor(), KindScopeEvent, 1, 2); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("Move() by two places error = %v", err)
	}
	for i := 1; i < MaxKindsPerScope; i++ {
		if _, err := svc.Create(ctx, adminActor(), KindScopeEvent, KindInput{NameEN: "Kind " + strconv.Itoa(i)}); err != nil {
			t.Fatalf("Create() #%d error = %v", i, err)
		}
	}
	if _, err := svc.Create(ctx, adminActor(), KindScopeEvent, KindInput{NameEN: "One too many"}); !errors.Is(err, ErrTooManyKinds) {
		t.Fatalf("Create() past the limit error = %v, want too many", err)
	}
}

func TestKindNameFallsBackToEnglish(t *testing.T) {
	kind := Kind{NameEN: "Camp", NameHU: "Tábor"}
	if kind.Name("hu") != "Tábor" || kind.Name("en") != "Camp" {
		t.Fatalf("Name() = %q, %q", kind.Name("hu"), kind.Name("en"))
	}
	if (Kind{NameEN: "Camp"}).Name("hu") != "Camp" {
		t.Fatal("an empty Hungarian name must fall back to English")
	}
}

func TestDefaultKindsAndSeeding(t *testing.T) {
	events := DefaultEventKinds()
	if len(events) != 4 || events[0].Code != EventKindTraining || events[0].NameHU != "Edzés" {
		t.Fatalf("DefaultEventKinds() = %+v", events)
	}
	if resources := DefaultResourceKinds(); len(resources) != 6 || resources[0].Code != "field" {
		t.Fatalf("DefaultResourceKinds() = %+v", resources)
	}
	if DefaultKinds(KindScopeResource)[2].Code != "hall" || DefaultKinds(KindScopeEvent)[1].Code != EventKindMatch {
		t.Fatal("DefaultKinds() mixes up the scopes")
	}
	repo := newKindStub()
	svc, _ := newTestKindService(t, repo)
	if err := svc.SeedDefaults(context.Background(), 5); err != nil || !slices.Equal(repo.seeded, []int64{5}) {
		t.Fatalf("SeedDefaults() = %v, seeded %v", err, repo.seeded)
	}
	if err := svc.SeedDefaults(context.Background(), 0); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("SeedDefaults() of no group error = %v", err)
	}
	repo.seedErr = errors.New("database down")
	if err := svc.SeedDefaults(context.Background(), 5); err == nil {
		t.Fatal("SeedDefaults() hid a repository failure")
	}
}

func TestGroupServiceCreateSeedsDefaultKinds(t *testing.T) {
	user := User{ID: 7, Email: strPtr("owner@example.test")}
	kinds := newKindStub()
	service, _ := newTestGroupService(t, &groupRepositoryStub{}, newMembershipStub(), usersWith(user))
	kindService, _ := newTestKindService(t, kinds)
	service.SetKinds(kindService)
	summary, err := service.Create(context.Background(), user.ID, "192.0.2.1", "Example", "")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !slices.Equal(kinds.seeded, []int64{summary.Group.ID}) {
		t.Fatalf("seeded groups = %v, want the new group %d", kinds.seeded, summary.Group.ID)
	}
	kinds.seedErr = errors.New("database down")
	if _, err := service.Create(context.Background(), user.ID, "192.0.2.1", "Other", ""); err == nil {
		t.Fatal("Create() hid a failed kind seeding")
	}
}

// kindSourceStub answers Usable from a fixed map.
type kindSourceStub map[int64]error

func (s kindSourceStub) Usable(_ context.Context, _ *GroupAccess, _ KindScope, kindID int64) (Kind, error) {
	if err, ok := s[kindID]; ok {
		return Kind{}, err
	}
	return Kind{ID: kindID}, ErrKindNotFound
}

func TestEventAndResourceKindChecks(t *testing.T) {
	source := kindSourceStub{3: nil, 4: ErrKindArchived}
	events := newTestEventService(t, newEventStub())
	events.service.kinds = source
	if _, _, err := events.service.Create(context.Background(), adminActor(), validEventInput(1)); err != nil {
		t.Fatalf("Create() with a usable kind error = %v", err)
	}
	archived := validEventInput(1)
	archived.KindID = 4
	if _, _, err := events.service.Create(context.Background(), adminActor(), archived); !errors.Is(err, ErrEventKindArchived) {
		t.Fatalf("Create() with an archived kind error = %v, want archived", err)
	}
	foreign := validEventInput(1)
	foreign.KindID = 99
	if _, _, err := events.service.Create(context.Background(), adminActor(), foreign); !errors.Is(err, ErrInvalidEventKind) {
		t.Fatalf("Create() with another group's kind error = %v, want invalid kind", err)
	}
	if err := events.service.checkKind(context.Background(), adminActor().Access, 4, 4); err != nil {
		t.Fatalf("keeping the event's own archived kind error = %v", err)
	}

	resources, _ := newTestResourceService(t, newResourceStub())
	resources.SetKinds(source)
	if _, err := resources.Create(context.Background(), adminActor(), ResourceInput{Name: "Field", KindID: 3}); err != nil {
		t.Fatalf("resource Create() with a usable kind error = %v", err)
	}
	if _, err := resources.Create(context.Background(), adminActor(), ResourceInput{Name: "Pool", KindID: 4}); !errors.Is(err, ErrResourceKindArchived) {
		t.Fatalf("resource Create() with an archived kind error = %v, want archived", err)
	}
	if _, err := resources.Create(context.Background(), adminActor(), ResourceInput{Name: "Hall", KindID: 99}); !errors.Is(err, ErrInvalidResourceKind) {
		t.Fatalf("resource Create() with another group's kind error = %v, want invalid kind", err)
	}
}
