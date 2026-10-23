package service

import (
	"context"
	"errors"
	"testing"
)

// noopAdminUserRepository satisfies the administrator list port; the
// optimistic-lock outcomes below never exercise the list query.
type noopAdminUserRepository struct{}

func (noopAdminUserRepository) ListUsers(context.Context, AdminUserQuery) (AdminUserPage, error) {
	return AdminUserPage{}, nil
}

// lockOutcome asserts that err matches want and never matches its opposite
// sentinel, so a stale revision and a vanished record stay distinguishable.
func assertLockOutcome(t *testing.T, name string, err, want, forbidden error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s error = %v, want errors.Is(_, %v)", name, err, want)
	}
	if errors.Is(err, forbidden) {
		t.Fatalf("%s error = %v must not match %v", name, err, forbidden)
	}
}

func TestOptimisticLockOutcomesAcrossEditableResources(t *testing.T) {
	conflict := ErrOptimisticLockConflict
	missing := ErrRecordNotFound

	tests := []struct {
		name  string
		run   func(writeErr error) error
		blank error
	}{
		{
			name: "locale settings",
			run: func(writeErr error) error {
				users := &settingsUserRepositoryStub{
					user:      User{ID: 7, Locale: "en", Version: 4},
					updateErr: writeErr,
				}
				service, err := NewLocaleSettingsService(users)
				if err != nil {
					t.Fatalf("NewLocaleSettingsService() error = %v", err)
				}
				_, err = service.Update(context.Background(), 7, "hu")
				return err
			},
		},
		{
			name: "administrator account edit",
			run: func(writeErr error) error {
				users := &userRepositoryStub{updateErr: writeErr}
				service, err := NewAdminAccountService(users, noopAdminUserRepository{}, nil, nil)
				if err != nil {
					t.Fatalf("NewAdminAccountService() error = %v", err)
				}
				_, err = service.Update(context.Background(), AdminActionContext{ActorID: 1}, 9, AdminAccountUpdateInput{
					Version: 1,
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name+": stale revision is a conflict", func(t *testing.T) {
			assertLockOutcome(t, test.name, test.run(conflict), conflict, missing)
			if !IsWriteConflict(test.run(conflict)) {
				t.Fatalf("%s stale revision must be a write conflict", test.name)
			}
		})
		t.Run(test.name+": vanished record is not-found", func(t *testing.T) {
			assertLockOutcome(t, test.name, test.run(ErrUserNotFound), missing, conflict)
			if !IsWriteConflict(test.run(ErrUserNotFound)) {
				t.Fatalf("%s vanished record must be classified as not-found", test.name)
			}
		})
	}
}

func TestOptimisticLockOutcomesForAdministratorDelete(t *testing.T) {
	conflict := ErrOptimisticLockConflict
	missing := ErrRecordNotFound
	run := func(deleteErr error) error {
		users := &userRepositoryStub{deleteErr: deleteErr}
		service, err := NewAdminAccountService(users, noopAdminUserRepository{}, nil, nil)
		if err != nil {
			t.Fatalf("NewAdminAccountService() error = %v", err)
		}
		return service.Delete(context.Background(), AdminActionContext{ActorID: 1}, 9, 3)
	}

	assertLockOutcome(t, "administrator delete", run(conflict), conflict, missing)
	assertLockOutcome(t, "administrator delete", run(ErrUserNotFound), missing, conflict)
}

func TestOptimisticLockOutcomesForGroupEditors(t *testing.T) {
	conflict := ErrOptimisticLockConflict

	tests := []struct {
		name    string
		run     func(writeErr error) error
		missing error
	}{
		{
			name: "group rename",
			run: func(writeErr error) error {
				groups := &groupRepositoryStub{
					group:     Group{ID: 5, Name: "Old", NameNorm: "old", Version: 1},
					updateErr: writeErr,
				}
				service, err := NewGroupService(groups, &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin}, &groupUserRepositoryStub{user: User{ID: 7}})
				if err != nil {
					t.Fatalf("NewGroupService() error = %v", err)
				}
				_, err = service.Rename(context.Background(), 7, 5, 1, "Renamed")
				return err
			},
			missing: ErrGroupNotFound,
		},
		{
			name: "group invite rotation",
			run: func(writeErr error) error {
				groups := &groupRepositoryStub{
					group:     Group{ID: 5, InviteCode: "OLDCODE12345", Version: 1},
					updateErr: writeErr,
				}
				service, err := NewGroupService(groups, &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin}, &groupUserRepositoryStub{user: User{ID: 7}})
				if err != nil {
					t.Fatalf("NewGroupService() error = %v", err)
				}
				_, err = service.RotateInvite(context.Background(), 7, 5)
				return err
			},
			missing: ErrGroupNotFound,
		},
		{
			name: "membership role change",
			run: func(writeErr error) error {
				memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin, updateErr: writeErr}
				service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, Version: 1}}, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
				if err != nil {
					t.Fatalf("NewGroupService() error = %v", err)
				}
				_, err = service.ChangeRole(context.Background(), 7, 5, 8, 1, GroupRoleMember)
				return err
			},
			missing: ErrMembershipNotFound,
		},
		{
			name: "membership removal",
			run: func(writeErr error) error {
				memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin, deleteErr: writeErr}
				service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, Version: 1}}, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
				if err != nil {
					t.Fatalf("NewGroupService() error = %v", err)
				}
				return service.RemoveMember(context.Background(), 7, 5, 8, 1)
			},
			missing: ErrMembershipNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name+": stale revision is a conflict", func(t *testing.T) {
			assertLockOutcome(t, test.name, test.run(conflict), conflict, test.missing)
		})
		t.Run(test.name+": vanished record is not-found", func(t *testing.T) {
			assertLockOutcome(t, test.name, test.run(test.missing), test.missing, conflict)
		})
	}
}

func TestWriteConflictPredicateMatchesOnlyConflictSentinels(t *testing.T) {
	if IsWriteConflict(ErrGroupNotFound) || IsWriteConflict(ErrMembershipNotFound) {
		t.Fatal("resource-specific not-found errors must not be reported as write conflicts")
	}
	if !IsWriteConflict(ErrOptimisticLockConflict) || !IsWriteConflict(ErrRecordNotFound) {
		t.Fatal("write conflict sentinels must report a conflict")
	}
	for _, err := range []error{ErrInvalidSettings, ErrNotGroupAdmin, nil} {
		if IsWriteConflict(err) {
			t.Errorf("IsWriteConflict(%v) = true, want false", err)
		}
	}
}
