package service

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// accessFor builds a member's access in group 5 for user 7.
func accessFor(grants GroupAccessGrants) *GroupAccess {
	return NewGroupAccess(5, 7, grants)
}

func memberWith(roles ...string) GroupAccessGrants {
	return GroupAccessGrants{Member: true, Roles: roles}
}

func TestGroupAccessPerRole(t *testing.T) {
	type answers struct {
		manageGroup, manageAdmin, manageCoach, groupInvite, removeMember bool
		allMembers, createTeam, editTeam, seesAllTeams, rosterTeam1      bool
		viewTeam1, viewTeam2, emailsTeam1                                bool
	}
	ownTeam1 := []TeamRole{{TeamID: 1, Role: GroupRolePlayer}}
	tests := []struct {
		name   string
		grants GroupAccessGrants
		want   answers
	}{
		{
			name:   "admin manages everything",
			grants: memberWith(GroupRoleAdmin),
			want:   answers{true, true, true, true, true, true, true, true, true, true, true, true, true},
		},
		{
			name:   "manager manages everything but the admin role",
			grants: memberWith(GroupRoleManager),
			want:   answers{true, false, true, true, true, true, true, true, true, true, true, true, true},
		},
		{
			name:   "coach without coached teams sees nothing extra",
			grants: memberWith(GroupRoleCoach),
			want:   answers{},
		},
		{
			name:   "coach of team 1 manages its roster and sees its emails",
			grants: GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{1}},
			want:   answers{rosterTeam1: true, viewTeam1: true, emailsTeam1: true},
		},
		{
			name:   "coached teams count only with the coach role",
			grants: GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, CoachedTeamIDs: []int64{1}},
			want:   answers{viewTeam1: true},
		},
		{
			name:   "player sees own team by name only",
			grants: GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: ownTeam1},
			want:   answers{viewTeam1: true},
		},
		{
			name:   "parent sees own and children's teams",
			grants: GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, OwnTeams: []TeamRole{{TeamID: 1, Role: GroupRoleParent}}, ChildTeamIDs: []int64{2}},
			want:   answers{viewTeam1: true, viewTeam2: true},
		},
		{
			name:   "member without roles sees no team",
			grants: memberWith(),
			want:   answers{},
		},
		{
			name:   "site administrator manages without membership",
			grants: GroupAccessGrants{SiteAdmin: true},
			want:   answers{true, true, true, true, true, true, true, true, true, true, true, true, true},
		},
		{
			name:   "player and coach of team 1 combine permissions",
			grants: GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer, GroupRoleCoach}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}}, CoachedTeamIDs: []int64{1}},
			want:   answers{rosterTeam1: true, viewTeam1: true, viewTeam2: true, emailsTeam1: true},
		},
		{
			name:   "manager and parent combine permissions",
			grants: GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent, GroupRoleManager}},
			want:   answers{true, false, true, true, true, true, true, true, true, true, true, true, true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := accessFor(test.grants)
			got := answers{
				manageGroup:  access.CanManageGroup(),
				manageAdmin:  access.CanManageRole(GroupRoleAdmin),
				manageCoach:  access.CanManageRole(GroupRoleCoach),
				groupInvite:  access.CanCreateInvite(GroupRolePlayer, 0),
				removeMember: access.CanRemoveMember(),
				allMembers:   access.CanViewAllMembers(),
				createTeam:   access.CanCreateTeam(),
				editTeam:     access.CanEditTeam(1),
				seesAllTeams: access.SeesAllTeams(),
				rosterTeam1:  access.CanManageRoster(1),
				viewTeam1:    access.CanViewTeam(1),
				viewTeam2:    access.CanViewTeam(2),
				emailsTeam1:  access.CanViewTeamEmails(1),
			}
			if got != test.want {
				t.Fatalf("answers = %+v\nwant      %+v", got, test.want)
			}
			if !access.CanView() {
				t.Fatal("CanView() = false, want true for members and site administrators")
			}
		})
	}
}

func TestGroupAccessOutsiderSeesNothing(t *testing.T) {
	for _, access := range []*GroupAccess{nil, accessFor(GroupAccessGrants{}), NewGroupAccess(0, 7, memberWith(GroupRoleAdmin)), NewGroupAccess(5, 0, memberWith(GroupRoleAdmin))} {
		if access.CanView() || access.IsMember() || access.CanManageGroup() || access.CanViewTeam(1) || access.HasRole(GroupRoleAdmin) || access.Roles() != nil || access.VisibleTeamIDs() != nil {
			t.Fatalf("access %+v grants something to an outsider", access)
		}
	}
}

func TestGroupAccessResources(t *testing.T) {
	tests := []struct {
		name         string
		grants       GroupAccessGrants
		manage, view bool
	}{
		{name: "admin", grants: memberWith(GroupRoleAdmin), manage: true, view: true},
		{name: "manager", grants: memberWith(GroupRoleManager), manage: true, view: true},
		{name: "site administrator", grants: GroupAccessGrants{SiteAdmin: true}, manage: true, view: true},
		{name: "coach reads only", grants: memberWith(GroupRoleCoach), view: true},
		{name: "player", grants: memberWith(GroupRolePlayer)},
		{name: "parent", grants: memberWith(GroupRoleParent)},
		{name: "member without roles", grants: memberWith()},
		{name: "outsider", grants: GroupAccessGrants{Roles: []string{GroupRoleAdmin}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := accessFor(test.grants)
			if access.CanManageResources() != test.manage || access.CanViewResources() != test.view {
				t.Fatalf("manage/view = %v/%v, want %v/%v", access.CanManageResources(), access.CanViewResources(), test.manage, test.view)
			}
		})
	}
	var missing *GroupAccess
	if missing.CanManageResources() || missing.CanViewResources() {
		t.Fatal("nil access grants resources")
	}
}

func TestGroupAccessRolesAndVisibleTeams(t *testing.T) {
	access := accessFor(GroupAccessGrants{
		Member:         true,
		Roles:          []string{GroupRoleParent, "owner", GroupRoleAdmin, GroupRoleParent},
		OwnTeams:       []TeamRole{{TeamID: 3, Role: GroupRolePlayer}, {TeamID: 1, Role: GroupRoleParent}},
		CoachedTeamIDs: []int64{3},
		ChildTeamIDs:   []int64{2, 1},
	})
	if roles := access.Roles(); !slices.Equal(roles, []string{GroupRoleAdmin, GroupRoleParent}) {
		t.Fatalf("Roles() = %v, want ordered known roles without duplicates", roles)
	}
	if teams := access.VisibleTeamIDs(); !slices.Equal(teams, []int64{1, 2, 3}) {
		t.Fatalf("VisibleTeamIDs() = %v, want [1 2 3]", teams)
	}
	if access.CanManageRole("owner") {
		t.Fatal("CanManageRole(unknown) = true, want false")
	}
	if access.GroupID() != 5 || access.UserID() != 7 {
		t.Fatalf("ids = %d/%d, want 5/7", access.GroupID(), access.UserID())
	}
}

func TestGroupRoleHelpers(t *testing.T) {
	if !slices.Equal(GroupRoles(), []string{"admin", "manager", "coach", "player", "parent"}) {
		t.Fatalf("GroupRoles() = %v", GroupRoles())
	}
	for _, role := range []string{GroupRoleAdmin, GroupRoleManager, GroupRoleCoach} {
		if !GroupRoleNeedsAccount(role) {
			t.Errorf("GroupRoleNeedsAccount(%q) = false, want true", role)
		}
	}
	for _, role := range []string{GroupRolePlayer, GroupRoleParent} {
		if GroupRoleNeedsAccount(role) {
			t.Errorf("GroupRoleNeedsAccount(%q) = true, want false", role)
		}
	}
	if ValidGroupRole("member") {
		t.Error("ValidGroupRole(member) = true, want false")
	}
}

type groupAccessRepositoryStub struct {
	access    MemberAccess
	err       error
	exists    bool
	existsErr error
}

func (r *groupAccessRepositoryStub) FindMemberAccess(context.Context, int64, int64) (MemberAccess, error) {
	return r.access, r.err
}

func (r *groupAccessRepositoryStub) GroupExists(context.Context, int64) (bool, error) {
	return r.exists, r.existsErr
}

type teamRelationStub struct {
	ids   []int64
	err   error
	calls int
}

func (s *teamRelationStub) TeamIDs(context.Context, int64, int64) ([]int64, error) {
	s.calls++
	return s.ids, s.err
}

// childRelationStub is a guardian source: children and their teams.
type childRelationStub struct {
	teamRelationStub
	children    []int64
	childrenErr error
}

func (s *childRelationStub) ChildUserIDs(context.Context, int64, int64) ([]int64, error) {
	return s.children, s.childrenErr
}

func TestGroupAccessServiceFor(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name       string
		repository *groupAccessRepositoryStub
		user       User
		wantErr    error
		check      func(*testing.T, *GroupAccess)
	}{
		{
			name:       "member gets roles and own teams",
			repository: &groupAccessRepositoryStub{access: MemberAccess{Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 4, Role: GroupRolePlayer}}}},
			user:       User{ID: 7},
			check: func(t *testing.T, access *GroupAccess) {
				if !access.IsMember() || !access.CanViewTeam(4) || access.CanManageGroup() {
					t.Fatalf("member access = %+v", access)
				}
			},
		},
		{
			name:       "outsider is not found",
			repository: &groupAccessRepositoryStub{err: ErrMembershipNotFound, exists: true},
			user:       User{ID: 7},
			wantErr:    ErrGroupNotFound,
		},
		{
			name:       "site administrator sees an existing group",
			repository: &groupAccessRepositoryStub{err: ErrMembershipNotFound, exists: true},
			user:       User{ID: 7, IsAdmin: true},
			check: func(t *testing.T, access *GroupAccess) {
				if access.IsMember() || !access.IsSiteAdmin() || !access.CanManageGroup() {
					t.Fatalf("site administrator access = %+v", access)
				}
			},
		},
		{
			name:       "site administrator gets not found for a missing group",
			repository: &groupAccessRepositoryStub{err: ErrMembershipNotFound},
			user:       User{ID: 7, IsAdmin: true},
			wantErr:    ErrGroupNotFound,
		},
		{
			name:       "guest flagged as administrator is not a site administrator",
			repository: &groupAccessRepositoryStub{err: ErrMembershipNotFound, exists: true},
			user:       User{ID: 7, IsAdmin: true, Kind: UserKindGuest},
			wantErr:    ErrGroupNotFound,
		},
		{
			name:       "storage failure is returned",
			repository: &groupAccessRepositoryStub{err: errBoom},
			user:       User{ID: 7},
			wantErr:    errBoom,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewGroupAccessService(test.repository, &groupUserRepositoryStub{users: map[int64]User{7: test.user}})
			if err != nil {
				t.Fatalf("NewGroupAccessService() error = %v", err)
			}
			access, err := service.For(context.Background(), 7, 5)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("For() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("For() error = %v", err)
			}
			test.check(t, access)
		})
	}
}

func TestGroupAccessServiceExtensionSources(t *testing.T) {
	repository := &groupAccessRepositoryStub{access: MemberAccess{Roles: []string{GroupRoleCoach, GroupRoleParent}}}
	service, err := NewGroupAccessService(repository, &groupUserRepositoryStub{users: map[int64]User{7: {ID: 7}}})
	if err != nil {
		t.Fatalf("NewGroupAccessService() error = %v", err)
	}
	coached := &teamRelationStub{ids: []int64{8}}
	children := &childRelationStub{teamRelationStub: teamRelationStub{ids: []int64{9}}, children: []int64{30}}
	service.SetCoachedTeams(coached)
	service.SetChildTeams(children)
	access, err := service.For(context.Background(), 7, 5)
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}
	if !access.CoachesTeam(8) || !access.CanManageRoster(8) || !access.CanViewTeam(9) || access.CanManageRoster(9) {
		t.Fatalf("extension grants not applied: %+v", access)
	}
	if !access.IsGuardianOf(30) || access.IsGuardianOf(31) || !slices.Equal(access.ChildUserIDs(), []int64{30}) || access.CanViewTeamEmails(9) {
		t.Fatalf("child grants = %v, want child 30 without team emails", access.ChildUserIDs())
	}
	if !slices.Equal(access.VisibleTeamIDs(), []int64{8, 9}) {
		t.Fatalf("VisibleTeamIDs() = %v, want coached and child teams", access.VisibleTeamIDs())
	}

	// Without children the team source is not asked.
	children.children, children.calls = nil, 0
	if access, err := service.For(context.Background(), 7, 5); err != nil || access.CanViewTeam(9) || children.calls != 0 {
		t.Fatalf("childless For() = %v (team calls %d), want no child teams", err, children.calls)
	}
	children.children = []int64{30}

	children.childrenErr = errors.New("boom")
	if _, err := service.For(context.Background(), 7, 5); err == nil {
		t.Fatal("For() error = nil, want the child source failure")
	}
	children.childrenErr = nil

	// A member without the coach role is never asked for coached teams.
	repository.access.Roles = []string{GroupRolePlayer}
	coached.calls = 0
	if _, err := service.For(context.Background(), 7, 5); err != nil {
		t.Fatalf("For() error = %v", err)
	}
	if coached.calls != 0 {
		t.Fatalf("coached team source calls = %d, want 0 without the coach role", coached.calls)
	}

	children.err = errors.New("boom")
	if _, err := service.For(context.Background(), 7, 5); err == nil {
		t.Fatal("For() error = nil, want the child team source failure")
	}
}

func TestNewGroupAccessServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewGroupAccessService(nil, &groupUserRepositoryStub{}); !errors.Is(err, ErrNilGroupAccessRepository) {
		t.Fatalf("nil repository error = %v", err)
	}
	if _, err := NewGroupAccessService(&groupAccessRepositoryStub{}, nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
}
