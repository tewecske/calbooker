package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type profileRepositoryStub struct {
	profiles    map[int64]ChildProfile
	children    []ChildEntry
	createErr   error
	writeErr    error
	guardianErr error
	created     ChildProfile
	guardianID  int64
	teamID      int64
	links       [][2]int64
	unlinks     [][2]int64
	deleted     []int64
	updateVer   int64
}

func newProfileStub(profiles ...ChildProfile) *profileRepositoryStub {
	stub := &profileRepositoryStub{profiles: make(map[int64]ChildProfile)}
	for _, profile := range profiles {
		stub.profiles[profile.UserID] = profile
	}
	return stub
}

func (r *profileRepositoryStub) CreateProfile(_ context.Context, profile ChildProfile, guardianID, teamID int64) (ChildProfile, error) {
	if r.createErr != nil {
		return ChildProfile{}, r.createErr
	}
	profile.UserID = 40
	r.created, r.guardianID, r.teamID = profile, guardianID, teamID
	return profile, nil
}

func (r *profileRepositoryStub) FindProfile(_ context.Context, groupID, userID int64) (ChildProfile, error) {
	profile, ok := r.profiles[userID]
	if !ok || profile.GroupID != groupID {
		return ChildProfile{}, ErrProfileNotFound
	}
	return profile, nil
}

func (r *profileRepositoryStub) UpdateProfile(_ context.Context, profile ChildProfile, expectedVersion int64) (ChildProfile, error) {
	r.updateVer = expectedVersion
	if r.writeErr != nil {
		return ChildProfile{}, r.writeErr
	}
	profile.Version++
	return profile, nil
}

func (r *profileRepositoryStub) DeleteProfile(_ context.Context, _, userID, _ int64) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.deleted = append(r.deleted, userID)
	return nil
}

func (r *profileRepositoryStub) ListChildren(context.Context, int64) ([]ChildEntry, error) {
	return r.children, nil
}

func (r *profileRepositoryStub) AddGuardian(_ context.Context, _, parentID, childID int64, _ *int64, _ int64) error {
	if r.guardianErr != nil {
		return r.guardianErr
	}
	r.links = append(r.links, [2]int64{parentID, childID})
	return nil
}

func (r *profileRepositoryStub) RemoveGuardian(_ context.Context, _, parentID, childID int64) error {
	if r.guardianErr != nil {
		return r.guardianErr
	}
	r.unlinks = append(r.unlinks, [2]int64{parentID, childID})
	return nil
}

func newTestProfileService(t *testing.T, profiles *profileRepositoryStub) (*ProfileService, *auditRecorderStub) {
	t.Helper()
	service, err := NewProfileService(profiles, newMembershipStub(), newTeamStub(Team{ID: 4, GroupID: 5, Name: "Lions"}), usersWith(User{ID: 7}))
	if err != nil {
		t.Fatalf("NewProfileService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	return service, auditor
}

func parentOf(children ...int64) GroupActor {
	return actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildUserIDs: children})
}

func TestProfileServiceCreate(t *testing.T) {
	tests := []struct {
		name         string
		actor        GroupActor
		input        ProfileInput
		repoErr      error
		wantErr      error
		wantGuardian int64
		wantTeam     int64
	}{
		{name: "parent's profile is linked to the parent", actor: parentOf(), input: ProfileInput{DisplayName: " Anna ", BirthYear: 2016}, wantGuardian: 7},
		{name: "parent may not pick a team", actor: parentOf(), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016, TeamID: 4}, wantErr: ErrGroupForbidden},
		{name: "parent may not link another parent", actor: parentOf(), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016, GuardianUserID: 8}, wantErr: ErrGroupForbidden},
		{name: "staff picks a guardian and team", actor: actorWith(memberWith(GroupRoleManager)), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016, GuardianUserID: 8, TeamID: 4}, wantGuardian: 8, wantTeam: 4},
		{name: "staff may leave the guardian out", actor: adminActor(), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}},
		{name: "site administrator creates", actor: actorWith(GroupAccessGrants{SiteAdmin: true}), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}},
		{name: "unknown team", actor: adminActor(), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016, TeamID: 99}, wantErr: ErrTeamNotFound},
		{name: "guardian without the parent role", actor: adminActor(), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016, GuardianUserID: 8}, repoErr: ErrGuardianNeedsParentRole, wantErr: ErrGuardianNeedsParentRole},
		{name: "empty name", actor: parentOf(), input: ProfileInput{DisplayName: "  ", BirthYear: 2016}, wantErr: ErrInvalidProfileName},
		{name: "long name", actor: parentOf(), input: ProfileInput{DisplayName: strings.Repeat("a", maxProfileNameLength+1), BirthYear: 2016}, wantErr: ErrInvalidProfileName},
		{name: "birth year too early", actor: parentOf(), input: ProfileInput{DisplayName: "Anna", BirthYear: 1899}, wantErr: ErrInvalidProfileBirthYear},
		{name: "player may not create", actor: actorWith(memberWith(GroupRolePlayer)), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}, wantErr: ErrGroupForbidden},
		{name: "coach may not create", actor: coachOf(4), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}, wantErr: ErrGroupForbidden},
		{name: "outsider gets not found", actor: actorWith(GroupAccessGrants{}), input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}, wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profiles := newProfileStub()
			profiles.createErr = test.repoErr
			service, auditor := newTestProfileService(t, profiles)
			created, err := service.CreateProfile(context.Background(), test.actor, test.input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("CreateProfile() error = %v, want %v", err, test.wantErr)
				}
				if len(auditor.records) != 0 {
					t.Fatal("refused profile audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateProfile() error = %v", err)
			}
			if created.DisplayName != "Anna" || created.GroupID != 5 || profiles.guardianID != test.wantGuardian || profiles.teamID != test.wantTeam {
				t.Fatalf("created %+v guardian %d team %d", created, profiles.guardianID, profiles.teamID)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionProfileCreated || strings.Contains(auditor.records[0].Detail, "Anna") {
				t.Fatalf("audit = %+v, want one record without the child's name", auditor.records)
			}
		})
	}
}

func TestProfileServiceUpdateAndDelete(t *testing.T) {
	profile := ChildProfile{UserID: 20, GroupID: 5, DisplayName: "Anna", BirthYear: 2016, Version: 2}
	for _, test := range []struct {
		name    string
		actor   GroupActor
		wantErr error
	}{
		{name: "guardian", actor: parentOf(20)},
		{name: "staff", actor: actorWith(memberWith(GroupRoleManager))},
		{name: "another parent", actor: parentOf(21), wantErr: ErrProfileNotFound},
		{name: "player", actor: actorWith(memberWith(GroupRolePlayer)), wantErr: ErrProfileNotFound},
		{name: "outsider", actor: actorWith(GroupAccessGrants{}), wantErr: ErrGroupNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			profiles := newProfileStub(profile)
			service, auditor := newTestProfileService(t, profiles)
			updated, err := service.UpdateProfile(context.Background(), test.actor, 20, 2, ProfileInput{DisplayName: "Anna B", BirthYear: 2015})
			deleteErr := service.DeleteProfile(context.Background(), test.actor, 20, 3)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || !errors.Is(deleteErr, test.wantErr) {
					t.Fatalf("update/delete errors = %v / %v, want %v", err, deleteErr, test.wantErr)
				}
				return
			}
			if err != nil || deleteErr != nil {
				t.Fatalf("update/delete errors = %v / %v", err, deleteErr)
			}
			if updated.DisplayName != "Anna B" || updated.BirthYear != 2015 || profiles.updateVer != 2 || len(profiles.deleted) != 1 {
				t.Fatalf("updated = %+v, deleted %v", updated, profiles.deleted)
			}
			if len(auditor.records) != 2 || auditor.records[0].Action != AuditActionProfileUpdated || auditor.records[1].Action != AuditActionProfileDeleted {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}

	profiles := newProfileStub(profile)
	service, _ := newTestProfileService(t, profiles)
	if _, err := service.UpdateProfile(context.Background(), parentOf(20), 20, 2, ProfileInput{DisplayName: "", BirthYear: 2015}); !errors.Is(err, ErrInvalidProfileName) {
		t.Fatalf("invalid update error = %v", err)
	}
	profiles.writeErr = ErrOptimisticLockConflict
	if _, err := service.UpdateProfile(context.Background(), parentOf(20), 20, 1, ProfileInput{DisplayName: "A", BirthYear: 2015}); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}
	profiles.writeErr = ErrRecordNotFound
	if err := service.DeleteProfile(context.Background(), parentOf(20), 20, 2); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("vanished delete error = %v, want not found (never a conflict)", err)
	}
	if err := service.DeleteProfile(context.Background(), parentOf(20), 20, -1); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("negative version error = %v", err)
	}
}

func TestProfileServiceGuardians(t *testing.T) {
	profiles := newProfileStub()
	service, auditor := newTestProfileService(t, profiles)
	staff := actorWith(memberWith(GroupRoleManager))
	if err := service.AddGuardian(context.Background(), staff, 8, 20); err != nil {
		t.Fatalf("AddGuardian() error = %v", err)
	}
	if err := service.RemoveGuardian(context.Background(), staff, 8, 20); err != nil {
		t.Fatalf("RemoveGuardian() error = %v", err)
	}
	if len(profiles.links) != 1 || len(profiles.unlinks) != 1 || len(auditor.records) != 2 || auditor.records[0].Action != AuditActionGuardianAdded {
		t.Fatalf("links %v unlinks %v audit %+v", profiles.links, profiles.unlinks, auditor.records)
	}
	if err := service.AddGuardian(context.Background(), staff, 8, 8); !errors.Is(err, ErrInvalidGuardian) {
		t.Fatalf("self link error = %v", err)
	}
	for _, actor := range []GroupActor{parentOf(20), coachOf(4), actorWith(memberWith(GroupRolePlayer))} {
		if err := service.AddGuardian(context.Background(), actor, 7, 21); !errors.Is(err, ErrGroupForbidden) {
			t.Fatalf("non-staff AddGuardian() error = %v, want forbidden", err)
		}
		if err := service.RemoveGuardian(context.Background(), actor, 7, 20); !errors.Is(err, ErrGroupForbidden) {
			t.Fatalf("non-staff RemoveGuardian() error = %v, want forbidden", err)
		}
	}
	for _, repoErr := range []error{ErrGuardianNeedsParentRole, ErrMembershipNotFound} {
		profiles.guardianErr = repoErr
		if err := service.AddGuardian(context.Background(), staff, 8, 20); !errors.Is(err, repoErr) {
			t.Fatalf("AddGuardian() error = %v, want %v", err, repoErr)
		}
	}
	profiles.guardianErr = ErrGuardianNotFound
	if err := service.RemoveGuardian(context.Background(), staff, 8, 20); !errors.Is(err, ErrGuardianNotFound) {
		t.Fatalf("RemoveGuardian() error = %v", err)
	}
}

func TestProfileServiceChildrenVisibility(t *testing.T) {
	profiles := newProfileStub()
	profiles.children = []ChildEntry{{Person: PersonRef{UserID: 20}}, {Person: PersonRef{UserID: 21}}}
	service, _ := newTestProfileService(t, profiles)

	staff, err := service.Children(context.Background(), actorWith(memberWith(GroupRoleAdmin)).Access)
	if err != nil || len(staff.Children) != 2 || staff.Teams == nil {
		t.Fatalf("staff Children() = %+v, %v", staff, err)
	}
	parent, err := service.Children(context.Background(), parentOf(21).Access)
	if err != nil || len(parent.Children) != 1 || parent.Children[0].Person.UserID != 21 || parent.Members != nil || parent.Teams != nil {
		t.Fatalf("parent Children() = %+v, %v; want only their child and no member list", parent, err)
	}
	// A guardian who lost the parent role still sees linked children
	// (the store removes links with the role, so this is a stale grant).
	linked, err := service.Children(context.Background(), actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, ChildUserIDs: []int64{20}}).Access)
	if err != nil || len(linked.Children) != 1 {
		t.Fatalf("linked Children() = %+v, %v", linked, err)
	}
	if _, err := service.Children(context.Background(), actorWith(memberWith(GroupRolePlayer)).Access); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("player Children() error = %v, want forbidden", err)
	}
	if _, err := service.Children(context.Background(), nil); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Children() error = %v, want not found", err)
	}
}

func TestGroupServiceAddRoleKeepsProfilesPlayers(t *testing.T) {
	memberships := newMembershipStub()
	memberships.memberships[20] = GroupMembership{GroupID: 5, UserID: 20, Roles: []string{}}
	service, _ := newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(User{ID: 7}, User{ID: 20, Kind: UserKindProfile}))
	for _, role := range []string{GroupRoleParent, GroupRoleCoach, GroupRoleManager, GroupRoleAdmin} {
		if _, err := service.AddRole(context.Background(), adminActor(), 20, 0, role); !errors.Is(err, ErrRoleNeedsAccount) {
			t.Fatalf("AddRole(profile, %s) error = %v, want ErrRoleNeedsAccount", role, err)
		}
	}
	if _, err := service.AddRole(context.Background(), adminActor(), 20, 0, GroupRolePlayer); err != nil {
		t.Fatalf("AddRole(profile, player) error = %v", err)
	}
}

func TestNewProfileServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewProfileService(nil, newMembershipStub(), newTeamStub(), usersWith()); !errors.Is(err, ErrNilProfileRepository) {
		t.Fatalf("nil profiles error = %v", err)
	}
	if _, err := NewProfileService(newProfileStub(), nil, newTeamStub(), usersWith()); !errors.Is(err, ErrNilMembershipRepository) {
		t.Fatalf("nil memberships error = %v", err)
	}
	if _, err := NewProfileService(newProfileStub(), newMembershipStub(), nil, usersWith()); !errors.Is(err, ErrNilTeamRepository) {
		t.Fatalf("nil teams error = %v", err)
	}
	if _, err := NewProfileService(newProfileStub(), newMembershipStub(), newTeamStub(), nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
}
