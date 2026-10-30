package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

var errGroupCreateBoom = errors.New("boom")

func adminActor() GroupActor {
	return GroupActor{Access: accessFor(memberWith(GroupRoleAdmin)), Origin: "192.0.2.1"}
}

func actorWith(grants GroupAccessGrants) GroupActor {
	return GroupActor{Access: accessFor(grants), Origin: "192.0.2.1"}
}

func newTestGroupService(t *testing.T, groups *groupRepositoryStub, memberships *groupMembershipRepositoryStub, users *groupUserRepositoryStub) (*GroupService, *auditRecorderStub) {
	t.Helper()
	service, err := NewGroupService(groups, memberships, users)
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	return service, auditor
}

func TestGroupServiceCreate(t *testing.T) {
	tests := []struct {
		name      string
		user      User
		groupName string
		timeZone  string
		createErr error
		wantErr   error
		wantZone  string
	}{
		{name: "creates group with creator as administrator", user: User{ID: 7, Email: strPtr("owner@example.test")}, groupName: "  Example Group  ", wantZone: DefaultGroupTimeZone},
		{name: "uses the chosen time zone", user: User{ID: 7}, groupName: "Example", timeZone: " Europe/Vienna ", wantZone: "Europe/Vienna"},
		{name: "rejects an unknown time zone", user: User{ID: 7}, groupName: "Example", timeZone: "Mars/Olympus", wantErr: ErrInvalidTimeZone},
		{name: "rejects guest creator", user: User{ID: 7, Kind: UserKindGuest}, groupName: "Example", wantErr: ErrGroupRequiresAccount},
		{name: "rejects profile creator", user: User{ID: 7, Kind: UserKindProfile}, groupName: "Example", wantErr: ErrGroupRequiresAccount},
		{name: "rejects empty name", user: User{ID: 7}, groupName: "   ", wantErr: ErrInvalidGroupName},
		{name: "rejects oversized name", user: User{ID: 7}, groupName: strings.Repeat("a", maxGroupNameLength+1), wantErr: ErrInvalidGroupName},
		{name: "propagates non-collision failure", user: User{ID: 7}, groupName: "Example", createErr: errGroupCreateBoom, wantErr: errGroupCreateBoom},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			memberships := newMembershipStub()
			memberships.createErr = test.createErr
			service, auditor := newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(test.user))
			summary, err := service.Create(context.Background(), test.user.ID, "192.0.2.1", test.groupName, test.timeZone)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Create() error = %v, want errors.Is(_, %v)", err, test.wantErr)
				}
				if len(auditor.records) != 0 {
					t.Fatalf("failed create audited %d records", len(auditor.records))
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v, want nil", err)
			}
			if !slices.Equal(summary.Roles, []string{GroupRoleAdmin}) || summary.Group.TimeZone != test.wantZone {
				t.Fatalf("summary = %+v, want admin in %s", summary, test.wantZone)
			}
			if summary.Group.CreatedBy == nil || *summary.Group.CreatedBy != test.user.ID || memberships.adminUserID != test.user.ID {
				t.Fatalf("creator = %v / %d, want %d", summary.Group.CreatedBy, memberships.adminUserID, test.user.ID)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionGroupCreated || auditor.records[0].IP != "192.0.2.1" {
				t.Fatalf("audit = %+v, want one group.created record with origin", auditor.records)
			}
		})
	}
}

func TestGroupServiceListIncludesEveryGroupForSiteAdministrator(t *testing.T) {
	memberGroups := []GroupSummary{{Group: Group{ID: 1}, MemberCount: 1, Roles: []string{GroupRolePlayer, GroupRoleAdmin}}}
	allGroups := []GroupSummary{memberGroups[0], {Group: Group{ID: 2}, MemberCount: 3}}
	groups := &groupRepositoryStub{summaries: memberGroups, allSummaries: allGroups}

	administrator, _ := newTestGroupService(t, groups, newMembershipStub(), usersWith(User{ID: 7, IsAdmin: true}))
	summaries, err := administrator.List(context.Background(), 7)
	if err != nil || len(summaries) != 2 || len(summaries[1].Roles) != 0 {
		t.Fatalf("site administrator List() = %+v, %v; want every group without invented roles", summaries, err)
	}
	if !slices.Equal(summaries[0].Roles, []string{GroupRoleAdmin, GroupRolePlayer}) {
		t.Fatalf("roles = %v, want display order", summaries[0].Roles)
	}

	ordinary, _ := newTestGroupService(t, groups, newMembershipStub(), usersWith(User{ID: 7}))
	if summaries, err := ordinary.List(context.Background(), 7); err != nil || len(summaries) != 1 {
		t.Fatalf("ordinary List() = %+v, %v; want only memberships", summaries, err)
	}
	if _, err := ordinary.List(context.Background(), 0); !errors.Is(err, ErrInvalidUserID) {
		t.Fatalf("List(0) error = %v", err)
	}
}

func TestGroupServiceOverviewForMembers(t *testing.T) {
	groups := &groupRepositoryStub{group: Group{ID: 5, Name: "Club", TimeZone: "Europe/Budapest"}}
	service, _ := newTestGroupService(t, groups, newMembershipStub(), usersWith(User{ID: 7}))
	for _, grants := range []GroupAccessGrants{memberWith(GroupRoleAdmin), memberWith(GroupRolePlayer)} {
		overview, err := service.Overview(context.Background(), accessFor(grants))
		if err != nil || overview.Group.ID != 5 {
			t.Fatalf("Overview() = %+v, %v", overview, err)
		}
	}
	if _, err := service.Overview(context.Background(), accessFor(GroupAccessGrants{})); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Overview() error = %v, want ErrGroupNotFound", err)
	}
	if _, err := service.Group(context.Background(), nil); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Group() error = %v, want ErrGroupNotFound", err)
	}
}

func TestGroupServiceUpdateSettings(t *testing.T) {
	tests := []struct {
		name     string
		actor    GroupActor
		version  int64
		groupNm  string
		slug     string
		zone     string
		writeErr error
		wantErr  error
		wantSlug string
	}{
		{name: "admin saves name and time zone", actor: adminActor(), version: 1, groupNm: "Renamed", zone: "Europe/Vienna"},
		{name: "manager saves", actor: actorWith(memberWith(GroupRoleManager)), version: 1, groupNm: "Renamed", zone: "UTC"},
		{name: "coach is forbidden", actor: actorWith(memberWith(GroupRoleCoach)), version: 1, groupNm: "Renamed", zone: "UTC", wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), version: 1, groupNm: "Renamed", zone: "UTC", wantErr: ErrGroupNotFound},
		{name: "invalid time zone", actor: adminActor(), version: 1, groupNm: "Renamed", zone: "Local", wantErr: ErrInvalidTimeZone},
		{name: "invalid name", actor: adminActor(), version: 1, groupNm: " ", zone: "UTC", wantErr: ErrInvalidGroupName},
		{name: "negative revision", actor: adminActor(), version: -1, groupNm: "Renamed", zone: "UTC", wantErr: ErrInvalidGroup},
		{name: "stale revision", actor: adminActor(), version: 1, groupNm: "Renamed", zone: "UTC", writeErr: ErrOptimisticLockConflict, wantErr: ErrOptimisticLockConflict},
		{name: "new slug is saved", actor: adminActor(), version: 1, groupNm: "Renamed", slug: " New-Slug ", zone: "UTC", wantSlug: "new-slug"},
		{name: "empty slug keeps the current one", actor: adminActor(), version: 1, groupNm: "Renamed", zone: "UTC", wantSlug: "old"},
		{name: "reserved slug", actor: adminActor(), version: 1, groupNm: "Renamed", slug: "settings", zone: "UTC", wantErr: ErrInvalidSlug},
		{name: "numeric slug", actor: adminActor(), version: 1, groupNm: "Renamed", slug: "123", zone: "UTC", wantErr: ErrInvalidSlug},
		{name: "slug of another group", actor: adminActor(), version: 1, groupNm: "Renamed", slug: "lions", zone: "UTC", wantErr: ErrSlugTaken},
		{name: "vanished group", actor: adminActor(), version: 1, groupNm: "Renamed", zone: "UTC", writeErr: ErrGroupNotFound, wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			groups := &groupRepositoryStub{group: Group{ID: 5, Name: "Old", Slug: "old", NameNorm: "old", TimeZone: "Europe/Budapest", Version: 1}, updateErr: test.writeErr, takenSlugs: map[string]bool{"lions": true}}
			service, auditor := newTestGroupService(t, groups, newMembershipStub(), usersWith(User{ID: 7}))
			updated, err := service.UpdateSettings(context.Background(), test.actor, test.version, test.groupNm, test.slug, test.zone)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("UpdateSettings() error = %v, want %v", err, test.wantErr)
				}
				if len(auditor.records) != 0 {
					t.Fatal("failed update was audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateSettings() error = %v", err)
			}
			if updated.Name != test.groupNm || updated.TimeZone != test.zone || groups.expectedVersion != test.version {
				t.Fatalf("updated = %+v (expected version %d)", updated, groups.expectedVersion)
			}
			if test.wantSlug != "" && updated.Slug != test.wantSlug {
				t.Fatalf("slug = %q, want %q", updated.Slug, test.wantSlug)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionGroupSettings || auditor.records[0].TargetID != "5" {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}
}

func TestNormalizeTimeZone(t *testing.T) {
	for _, valid := range []string{"Europe/Budapest", " UTC ", "America/New_York"} {
		if _, err := NormalizeTimeZone(valid); err != nil {
			t.Errorf("NormalizeTimeZone(%q) error = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "Local", "Europe/Atlantis", "../etc/passwd", strings.Repeat("a", 65)} {
		if _, err := NormalizeTimeZone(invalid); !errors.Is(err, ErrInvalidTimeZone) {
			t.Errorf("NormalizeTimeZone(%q) error = %v, want ErrInvalidTimeZone", invalid, err)
		}
	}
	for _, zone := range CommonTimeZones() {
		if _, err := NormalizeTimeZone(zone); err != nil {
			t.Errorf("offered time zone %q is invalid: %v", zone, err)
		}
	}
}

func TestGroupServiceMembersVisibility(t *testing.T) {
	email := func(value string) *string { return &value }
	members := []GroupMember{
		{UserID: 7, Roles: []string{GroupRolePlayer}, Email: email("self@example.test"), TeamIDs: []int64{1}},
		{UserID: 8, Roles: []string{GroupRolePlayer}, Email: email("teammate@example.test"), TeamIDs: []int64{1, 2}},
		{UserID: 9, Roles: []string{GroupRoleParent}, Email: email("other@example.test"), TeamIDs: []int64{3}},
		{UserID: 10, Roles: []string{GroupRoleAdmin}, Email: email("admin@example.test")},
	}
	tests := []struct {
		name       string
		grants     GroupAccessGrants
		wantIDs    []int64
		wantEmails []int64
	}{
		{name: "admin sees everyone with emails", grants: memberWith(GroupRoleAdmin), wantIDs: []int64{7, 8, 9, 10}, wantEmails: []int64{7, 8, 9, 10}},
		{name: "manager sees everyone with emails", grants: memberWith(GroupRoleManager), wantIDs: []int64{7, 8, 9, 10}, wantEmails: []int64{7, 8, 9, 10}},
		{name: "player sees self and teammates by name", grants: GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 1, Role: GroupRolePlayer}}}, wantIDs: []int64{7, 8}, wantEmails: []int64{7}},
		{name: "coach sees coached team with emails", grants: GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{3}}, wantIDs: []int64{7, 9}, wantEmails: []int64{7, 9}},
		{name: "member without teams sees only self", grants: memberWith(), wantIDs: []int64{7}, wantEmails: []int64{7}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			memberships := newMembershipStub()
			memberships.members = slices.Clone(members)
			service, _ := newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(User{ID: 7}))
			got, err := service.Members(context.Background(), accessFor(test.grants))
			if err != nil {
				t.Fatalf("Members() error = %v", err)
			}
			var ids, emails []int64
			for _, member := range got {
				ids = append(ids, member.UserID)
				if member.Email != nil {
					emails = append(emails, member.UserID)
				}
			}
			if !slices.Equal(ids, test.wantIDs) || !slices.Equal(emails, test.wantEmails) {
				t.Fatalf("members %v emails %v, want %v emails %v", ids, emails, test.wantIDs, test.wantEmails)
			}
		})
	}
	service, _ := newTestGroupService(t, &groupRepositoryStub{}, newMembershipStub(), usersWith(User{ID: 7}))
	if _, err := service.Members(context.Background(), accessFor(GroupAccessGrants{})); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Members() error = %v, want ErrGroupNotFound", err)
	}
}

func TestGroupServiceAddRole(t *testing.T) {
	tests := []struct {
		name       string
		actor      GroupActor
		target     User
		targetRole []string
		role       string
		writeErr   error
		wantErr    error
		wantWrite  bool
	}{
		{name: "admin adds coach to a full user", actor: adminActor(), target: User{ID: 8}, targetRole: []string{GroupRolePlayer}, role: GroupRoleCoach, wantWrite: true},
		{name: "manager adds parent to a guest", actor: actorWith(memberWith(GroupRoleManager)), target: User{ID: 8, Kind: UserKindGuest}, targetRole: []string{GroupRolePlayer}, role: GroupRoleParent, wantWrite: true},
		{name: "guest cannot become coach", actor: adminActor(), target: User{ID: 8, Kind: UserKindGuest}, targetRole: []string{GroupRolePlayer}, role: GroupRoleCoach, wantErr: ErrRoleNeedsAccount},
		{name: "profile cannot become manager", actor: adminActor(), target: User{ID: 8, Kind: UserKindProfile}, role: GroupRoleManager, wantErr: ErrRoleNeedsAccount},
		{name: "manager cannot grant admin", actor: actorWith(memberWith(GroupRoleManager)), target: User{ID: 8}, role: GroupRoleAdmin, wantErr: ErrGroupForbidden},
		{name: "coach cannot grant roles", actor: actorWith(memberWith(GroupRoleCoach)), target: User{ID: 8}, role: GroupRolePlayer, wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), target: User{ID: 8}, role: GroupRolePlayer, wantErr: ErrGroupNotFound},
		{name: "unknown role", actor: adminActor(), target: User{ID: 8}, role: "member", wantErr: ErrInvalidGroupRole},
		{name: "unknown role from a player stays forbidden", actor: actorWith(memberWith(GroupRolePlayer)), target: User{ID: 8}, role: "member", wantErr: ErrGroupForbidden},
		{name: "held role is unchanged", actor: adminActor(), target: User{ID: 8}, targetRole: []string{GroupRolePlayer}, role: GroupRolePlayer},
		{name: "stale revision", actor: adminActor(), target: User{ID: 8}, role: GroupRolePlayer, writeErr: ErrOptimisticLockConflict, wantErr: ErrOptimisticLockConflict, wantWrite: true},
		{name: "vanished member", actor: adminActor(), target: User{ID: 8}, role: GroupRolePlayer, writeErr: ErrMembershipNotFound, wantErr: ErrMembershipNotFound, wantWrite: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			memberships := newMembershipStub()
			memberships.memberships[8] = GroupMembership{GroupID: 5, UserID: 8, Roles: test.targetRole, Version: 3}
			memberships.addErr = test.writeErr
			service, auditor := newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(User{ID: 7, Email: strPtr("admin@example.test")}, test.target))
			_, err := service.AddRole(context.Background(), test.actor, 8, 3, test.role)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("AddRole() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && err != nil {
				t.Fatalf("AddRole() error = %v", err)
			}
			if (memberships.writes > 0) != test.wantWrite {
				t.Fatalf("writes = %d, want write %v", memberships.writes, test.wantWrite)
			}
			audited := len(auditor.records) == 1 && auditor.records[0].Action == AuditActionGroupRoleAdded && auditor.records[0].ActorEmail == "admin@example.test"
			if audited != (test.wantErr == nil && test.wantWrite) {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}
}

func TestGroupServiceRemoveRole(t *testing.T) {
	tests := []struct {
		name     string
		actor    GroupActor
		held     []string
		role     string
		writeErr error
		wantErr  error
	}{
		{name: "admin removes admin", actor: adminActor(), held: []string{GroupRoleAdmin}, role: GroupRoleAdmin},
		{name: "last admin is kept", actor: adminActor(), held: []string{GroupRoleAdmin}, role: GroupRoleAdmin, writeErr: ErrLastGroupAdmin, wantErr: ErrLastGroupAdmin},
		{name: "manager cannot remove admin", actor: actorWith(memberWith(GroupRoleManager)), held: []string{GroupRoleAdmin}, role: GroupRoleAdmin, wantErr: ErrGroupForbidden},
		{name: "manager removes coach", actor: actorWith(memberWith(GroupRoleManager)), held: []string{GroupRoleCoach}, role: GroupRoleCoach},
		{name: "player is forbidden", actor: actorWith(memberWith(GroupRolePlayer)), held: []string{GroupRoleCoach}, role: GroupRoleCoach, wantErr: ErrGroupForbidden},
		{name: "unknown role", actor: adminActor(), role: "owner", wantErr: ErrInvalidGroupRole},
		{name: "stale revision", actor: adminActor(), held: []string{GroupRolePlayer}, role: GroupRolePlayer, writeErr: ErrOptimisticLockConflict, wantErr: ErrOptimisticLockConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			memberships := newMembershipStub()
			memberships.memberships[8] = GroupMembership{GroupID: 5, UserID: 8, Roles: test.held, Version: 3}
			memberships.removeErr = test.writeErr
			service, auditor := newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(User{ID: 7}))
			_, err := service.RemoveRole(context.Background(), test.actor, 8, 3, test.role)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("RemoveRole() error = %v, want %v", err, test.wantErr)
				}
				if len(auditor.records) != 0 {
					t.Fatal("failed removal was audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("RemoveRole() error = %v", err)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionGroupRoleRemoved {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}
}

func TestGroupServiceRemoveMemberAndLeave(t *testing.T) {
	memberships := newMembershipStub()
	memberships.memberships[7] = GroupMembership{GroupID: 5, UserID: 7, Roles: []string{GroupRoleManager}, Version: 1}
	memberships.memberships[8] = GroupMembership{GroupID: 5, UserID: 8, Roles: []string{GroupRolePlayer}}
	memberships.memberships[9] = GroupMembership{GroupID: 5, UserID: 9, Roles: []string{GroupRoleAdmin}}
	memberships.memberships[20] = GroupMembership{GroupID: 5, UserID: 20, Roles: []string{GroupRolePlayer}}
	service, auditor := newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(User{ID: 7}, User{ID: 8}, User{ID: 20, Kind: UserKindProfile}))
	manager := actorWith(memberWith(GroupRoleManager))

	// Child profiles are deleted through the profile service, never removed
	// as members (that would orphan the profile).
	if err := service.RemoveMember(context.Background(), manager, 20, 0); !errors.Is(err, ErrProfileMembership) {
		t.Fatalf("RemoveMember(profile) error = %v, want ErrProfileMembership", err)
	}

	if err := service.RemoveMember(context.Background(), manager, 7, 0); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("RemoveMember(self) error = %v, want ErrInvalidGroup", err)
	}
	if err := service.RemoveMember(context.Background(), manager, 9, 0); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("manager removing admin error = %v, want ErrGroupForbidden", err)
	}
	if err := service.RemoveMember(context.Background(), actorWith(memberWith(GroupRoleCoach)), 8, 0); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("coach RemoveMember() error = %v, want ErrGroupForbidden", err)
	}
	if err := service.RemoveMember(context.Background(), manager, 8, 0); err != nil {
		t.Fatalf("RemoveMember() error = %v", err)
	}
	if err := service.RemoveMember(context.Background(), manager, 42, 0); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("RemoveMember(missing) error = %v, want ErrMembershipNotFound", err)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionGroupRemoved {
		t.Fatalf("audit = %+v, want one removal", auditor.records)
	}

	if err := service.Leave(context.Background(), manager.Access); err != nil {
		t.Fatalf("Leave() error = %v", err)
	}
	if memberships.deleteCount != 2 || memberships.deletedVersion != 1 {
		t.Fatalf("deletes = %d (version %d), want leave at the member's revision", memberships.deleteCount, memberships.deletedVersion)
	}
	memberships.deleteErr = ErrLastGroupAdmin
	if err := service.Leave(context.Background(), adminActor().Access); !errors.Is(err, ErrLastGroupAdmin) {
		t.Fatalf("last admin Leave() error = %v, want ErrLastGroupAdmin", err)
	}
	if err := service.Leave(context.Background(), actorWith(GroupAccessGrants{SiteAdmin: true}).Access); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("site administrator Leave() error = %v, want ErrMembershipNotFound", err)
	}
}

func TestGroupServiceSiteAdministratorActionsAreMarked(t *testing.T) {
	groups := &groupRepositoryStub{group: Group{ID: 5, Version: 1}}
	service, auditor := newTestGroupService(t, groups, newMembershipStub(), usersWith(User{ID: 7, IsAdmin: true}))
	if _, err := service.UpdateSettings(context.Background(), actorWith(GroupAccessGrants{SiteAdmin: true}), 1, "Club", "", "UTC"); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if len(auditor.records) != 1 || !strings.Contains(auditor.records[0].Detail, "site administrator") {
		t.Fatalf("audit = %+v, want site administrator marker", auditor.records)
	}
}

func TestNormalizeGroupName(t *testing.T) {
	if _, err := normalizeGroupName("valid name"); err != nil {
		t.Fatalf("normalizeGroupName(valid) error = %v", err)
	}
	if _, err := normalizeGroupName("bad\nname"); !errors.Is(err, ErrInvalidGroupName) {
		t.Fatalf("normalizeGroupName(control) error = %v, want ErrInvalidGroupName", err)
	}
}

func TestNewGroupServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewGroupService(nil, newMembershipStub(), usersWith()); !errors.Is(err, ErrNilGroupRepository) {
		t.Fatalf("nil groups error = %v", err)
	}
	if _, err := NewGroupService(&groupRepositoryStub{}, nil, usersWith()); !errors.Is(err, ErrNilMembershipRepository) {
		t.Fatalf("nil memberships error = %v", err)
	}
	if _, err := NewGroupService(&groupRepositoryStub{}, newMembershipStub(), nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
}

type groupUserRepositoryStub struct {
	users map[int64]User
}

func usersWith(users ...User) *groupUserRepositoryStub {
	stub := &groupUserRepositoryStub{users: make(map[int64]User)}
	for _, user := range users {
		stub.users[user.ID] = user
	}
	return stub
}

func (r *groupUserRepositoryStub) CreateUser(context.Context, User) (User, error) {
	return User{}, nil
}

func (r *groupUserRepositoryStub) FindUserByID(_ context.Context, id int64) (User, error) {
	user, ok := r.users[id]
	if !ok || id <= 0 {
		return User{}, ErrUserNotFound
	}
	return user, nil
}

func (r *groupUserRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *groupUserRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *groupUserRepositoryStub) UpdateUser(context.Context, User, int64) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *groupUserRepositoryStub) DeleteUser(context.Context, int64, int64) error { return nil }

type groupRepositoryStub struct {
	group           Group
	summaries       []GroupSummary
	allSummaries    []GroupSummary
	findErr         error
	updateErr       error
	expectedVersion int64
	takenSlugs      map[string]bool
}

func (r *groupRepositoryStub) FindGroupByID(context.Context, int64) (Group, error) {
	if r.findErr != nil {
		return Group{}, r.findErr
	}
	return r.group, nil
}

func (r *groupRepositoryStub) FindGroupBySlug(_ context.Context, slug string) (Group, error) {
	if r.findErr != nil {
		return Group{}, r.findErr
	}
	if r.group.Slug != slug {
		return Group{}, ErrGroupNotFound
	}
	return r.group, nil
}

func (r *groupRepositoryStub) GroupSlugTaken(_ context.Context, slug string, exceptID int64) (bool, error) {
	return r.takenSlugs[slug] || (r.group.Slug == slug && r.group.ID != exceptID), nil
}

func (r *groupRepositoryStub) ListGroupsForUser(context.Context, int64) ([]GroupSummary, error) {
	return r.summaries, nil
}

func (r *groupRepositoryStub) ListAllGroupsForUser(context.Context, int64) ([]GroupSummary, error) {
	if r.allSummaries != nil {
		return r.allSummaries, nil
	}
	return r.summaries, nil
}

func (r *groupRepositoryStub) UpdateGroup(_ context.Context, group Group, expectedVersion int64) (Group, error) {
	r.expectedVersion = expectedVersion
	if r.updateErr != nil {
		return Group{}, r.updateErr
	}
	group.Version++
	return group, nil
}

type groupMembershipRepositoryStub struct {
	memberships    map[int64]GroupMembership
	members        []GroupMember
	createCalls    int
	createErr      error
	createErrs     []error
	addErr         error
	removeErr      error
	deleteErr      error
	adminUserID    int64
	writes         int
	deleteCount    int
	deletedVersion int64
}

func newMembershipStub() *groupMembershipRepositoryStub {
	return &groupMembershipRepositoryStub{memberships: make(map[int64]GroupMembership)}
}

func (r *groupMembershipRepositoryStub) CreateGroupWithAdmin(_ context.Context, group Group, userID int64) (Group, GroupMembership, error) {
	r.createCalls++
	r.adminUserID = userID
	if len(r.createErrs) > 0 {
		err := r.createErrs[0]
		r.createErrs = r.createErrs[1:]
		return Group{}, GroupMembership{}, err
	}
	if r.createErr != nil {
		return Group{}, GroupMembership{}, r.createErr
	}
	group.ID = int64(r.createCalls)
	return group, GroupMembership{GroupID: group.ID, UserID: userID, Roles: []string{GroupRoleAdmin}}, nil
}

func (r *groupMembershipRepositoryStub) CreateMembership(_ context.Context, membership GroupMembership) (GroupMembership, error) {
	r.createCalls++
	if r.createErr != nil {
		return GroupMembership{}, r.createErr
	}
	r.memberships[membership.UserID] = membership
	return membership, nil
}

func (r *groupMembershipRepositoryStub) FindMembership(_ context.Context, _, userID int64) (GroupMembership, error) {
	membership, ok := r.memberships[userID]
	if !ok {
		return GroupMembership{}, ErrMembershipNotFound
	}
	return membership, nil
}

func (r *groupMembershipRepositoryStub) ListMembers(context.Context, int64) ([]GroupMember, error) {
	return r.members, nil
}

func (r *groupMembershipRepositoryStub) CountMembers(context.Context, int64) (int, error) {
	return len(r.members), nil
}

func (r *groupMembershipRepositoryStub) AddMemberRole(_ context.Context, _, userID int64, role string, _ int64) (GroupMembership, error) {
	r.writes++
	if r.addErr != nil {
		return GroupMembership{}, r.addErr
	}
	membership := r.memberships[userID]
	membership.Roles = append(membership.Roles, role)
	membership.Version++
	r.memberships[userID] = membership
	return membership, nil
}

func (r *groupMembershipRepositoryStub) RemoveMemberRole(_ context.Context, _, userID int64, role string, _ int64) (GroupMembership, error) {
	r.writes++
	if r.removeErr != nil {
		return GroupMembership{}, r.removeErr
	}
	membership := r.memberships[userID]
	membership.Roles = slices.DeleteFunc(slices.Clone(membership.Roles), func(held string) bool { return held == role })
	membership.Version++
	r.memberships[userID] = membership
	return membership, nil
}

func (r *groupMembershipRepositoryStub) DeleteMembership(_ context.Context, _, _, expectedVersion int64) error {
	r.deleteCount++
	r.deletedVersion = expectedVersion
	return r.deleteErr
}
