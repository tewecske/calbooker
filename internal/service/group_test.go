package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var errGroupCreateBoom = errors.New("boom")

func TestGroupServiceCreate(t *testing.T) {
	tests := []struct {
		name      string
		user      User
		groupName string
		createErr error
		wantErr   error
		wantRole  string
	}{
		{name: "creates group with creator as administrator", user: User{ID: 7}, groupName: "  Example Group  ", wantRole: GroupRoleAdmin},
		{name: "rejects guest creator", user: User{ID: 7, IsGuest: true}, groupName: "Example", wantErr: ErrInvalidGroup},
		{name: "rejects empty name", user: User{ID: 7}, groupName: "   ", wantErr: ErrInvalidGroupName},
		{name: "rejects oversized name", user: User{ID: 7}, groupName: strings.Repeat("a", maxGroupNameLength+1), wantErr: ErrInvalidGroupName},
		{name: "propagates non-collision failure", user: User{ID: 7}, groupName: "Example", createErr: errGroupCreateBoom, wantErr: errGroupCreateBoom},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users := &groupUserRepositoryStub{user: test.user}
			memberships := &groupMembershipRepositoryStub{}
			if test.createErr != nil {
				memberships.createErr = test.createErr
			}
			service, err := NewGroupService(&groupRepositoryStub{}, memberships, users)
			if err != nil {
				t.Fatalf("NewGroupService() error = %v", err)
			}
			summary, err := service.Create(context.Background(), test.user.ID, test.groupName)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Create() error = %v, want errors.Is(_, %v)", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v, want nil", err)
			}
			if summary.Role != test.wantRole {
				t.Fatalf("summary role = %q, want %q", summary.Role, test.wantRole)
			}
			if len(summary.Group.InviteCode) != groupInviteCodeLength {
				t.Fatalf("invite code length = %d, want %d", len(summary.Group.InviteCode), groupInviteCodeLength)
			}
			if summary.Group.CreatedBy == nil || *summary.Group.CreatedBy != test.user.ID {
				t.Fatalf("created_by = %v, want %d", summary.Group.CreatedBy, test.user.ID)
			}
			if memberships.adminUserID != test.user.ID || memberships.adminRole != GroupRoleAdmin {
				t.Fatalf("membership = user %d role %q, want admin", memberships.adminUserID, memberships.adminRole)
			}
		})
	}
}

func TestGroupServiceCreateRetriesInviteCollision(t *testing.T) {
	users := &groupUserRepositoryStub{user: User{ID: 7}}
	memberships := &groupMembershipRepositoryStub{collisions: 2}
	service, err := NewGroupService(&groupRepositoryStub{}, memberships, users)
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	if _, err := service.Create(context.Background(), 7, "Example"); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if memberships.createCalls != 3 {
		t.Fatalf("create calls = %d, want 3", memberships.createCalls)
	}
}

func TestGroupServiceList(t *testing.T) {
	groups := &groupRepositoryStub{summaries: []GroupSummary{{Group: Group{ID: 1}, MemberCount: 0, Role: GroupRoleMember}}}
	service, err := NewGroupService(groups, &groupMembershipRepositoryStub{}, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	summaries, err := service.List(context.Background(), 7)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].MemberCount != 1 {
		t.Fatalf("List() = %+v, want member count normalized to 1", summaries)
	}
}

func TestGroupServiceListIncludesEveryGroupForSiteAdministrator(t *testing.T) {
	memberGroups := []GroupSummary{{Group: Group{ID: 1}, MemberCount: 1, Role: GroupRoleMember}}
	allGroups := []GroupSummary{
		{Group: Group{ID: 1}, MemberCount: 1, Role: GroupRoleMember},
		{Group: Group{ID: 2}, MemberCount: 3},
	}

	administrator, err := NewGroupService(
		&groupRepositoryStub{summaries: memberGroups, allSummaries: allGroups},
		&groupMembershipRepositoryStub{},
		&groupUserRepositoryStub{user: User{ID: 7, IsAdmin: true}},
	)
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	summaries, err := administrator.List(context.Background(), 7)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("List() returned %d groups, want every group", len(summaries))
	}
	if summaries[1].Role != "" {
		t.Fatalf("non-member group role = %q, want empty", summaries[1].Role)
	}

	ordinary, err := NewGroupService(
		&groupRepositoryStub{summaries: memberGroups, allSummaries: allGroups},
		&groupMembershipRepositoryStub{},
		&groupUserRepositoryStub{user: User{ID: 7}},
	)
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	memberships, err := ordinary.List(context.Background(), 7)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("ordinary List() returned %d groups, want only memberships", len(memberships))
	}
}

func TestGroupServiceDetailVisibility(t *testing.T) {
	tests := []struct {
		name            string
		role            string
		member          bool
		wantErr         error
		wantInvite      bool
		wantRoster      bool
		wantOtherEmails bool
	}{
		{name: "administrator sees invite code, roster, and emails", role: GroupRoleAdmin, member: true, wantInvite: true, wantRoster: true, wantOtherEmails: true},
		{name: "member sees roster without invite code or others' emails", role: GroupRoleMember, member: true, wantRoster: true},
		{name: "non-member gets not found", member: false, wantErr: ErrGroupNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			members := []GroupMember{{UserID: 7, Role: GroupRoleAdmin, Email: stringPointer("viewer@example.test")}, {UserID: 8, Role: GroupRoleMember, Email: stringPointer("other@example.test")}}
			memberships := &groupMembershipRepositoryStub{member: test.member, role: test.role, members: members}
			service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, InviteCode: "ABCDEFGHJKLM"}}, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
			if err != nil {
				t.Fatalf("NewGroupService() error = %v", err)
			}
			detail, err := service.Detail(context.Background(), 7, 5)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Detail() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detail() error = %v, want nil", err)
			}
			if (detail.InviteCode != "") != test.wantInvite {
				t.Errorf("invite code visibility = %v, want %v", detail.InviteCode != "", test.wantInvite)
			}
			if (len(detail.Members) > 0) != test.wantRoster {
				t.Errorf("roster visibility = %v, want %v", len(detail.Members) > 0, test.wantRoster)
			}
			if len(detail.Members) == 2 {
				if detail.Members[0].Email == nil {
					t.Error("viewer's own email hidden from them")
				}
				if (detail.Members[1].Email != nil) != test.wantOtherEmails {
					t.Errorf("other member email visible = %v, want %v", detail.Members[1].Email != nil, test.wantOtherEmails)
				}
			}
		})
	}
}

func TestGroupServiceDetailAllowsSiteAdministratorWithoutMembership(t *testing.T) {
	members := []GroupMember{{UserID: 8, Role: GroupRoleAdmin}}
	memberships := &groupMembershipRepositoryStub{member: false, members: members}
	users := &groupUserRepositoryStub{user: User{ID: 7, IsAdmin: true}}
	service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, InviteCode: "ABCDEFGHJKLM"}}, memberships, users)
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	detail, err := service.Detail(context.Background(), 7, 5)
	if err != nil {
		t.Fatalf("Detail() error = %v, want nil", err)
	}
	if detail.IsMember {
		t.Errorf("IsMember = true, want false for a non-member administrator")
	}
	if !detail.IsAdmin {
		t.Errorf("IsAdmin = false, want true for a site administrator")
	}
	if detail.InviteCode != "ABCDEFGHJKLM" {
		t.Errorf("InviteCode = %q, want the group invite code", detail.InviteCode)
	}
	if len(detail.Members) != len(members) {
		t.Errorf("roster length = %d, want %d", len(detail.Members), len(members))
	}
	if detail.ViewerRole != "" {
		t.Errorf("ViewerRole = %q, want empty without a membership", detail.ViewerRole)
	}
}

func TestGroupServiceAdminRightsForSiteAdministrator(t *testing.T) {
	tests := []struct {
		name      string
		user      User
		member    bool
		wantErr   error
		wantAudit bool
	}{
		{name: "site administrator without membership is audited", user: User{ID: 7, IsAdmin: true}, wantAudit: true},
		{name: "group administrator is not audited", user: User{ID: 7}, member: true},
		{name: "ordinary non-member is refused", user: User{ID: 7}, wantErr: ErrNotGroupAdmin},
		{name: "guest is never a site administrator", user: User{ID: 7, IsAdmin: true, IsGuest: true}, wantErr: ErrNotGroupAdmin},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			groups := &groupRepositoryStub{group: Group{ID: 5, Version: 1}}
			service, err := NewGroupService(groups, &groupMembershipRepositoryStub{member: test.member, role: GroupRoleAdmin}, &groupUserRepositoryStub{user: test.user})
			if err != nil {
				t.Fatalf("NewGroupService() error = %v", err)
			}
			auditor := &auditRecorderStub{}
			service.SetAuditor(auditor)
			if _, err := service.Rename(context.Background(), 7, 5, 1, "Renamed"); !errors.Is(err, test.wantErr) {
				t.Fatalf("Rename() error = %v, want %v", err, test.wantErr)
			}
			audited := len(auditor.records) == 1 && auditor.records[0].Action == AuditActionGroupRenamed &&
				auditor.records[0].TargetType == "group" && auditor.records[0].TargetID == "5" && auditor.records[0].ActorUserID == 7
			if audited != test.wantAudit || (!test.wantAudit && len(auditor.records) != 0) {
				t.Fatalf("audit records = %+v, want audited %v", auditor.records, test.wantAudit)
			}
		})
	}
}

func TestGroupServiceRenameRequiresAdmin(t *testing.T) {
	memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleMember}
	service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, Version: 2}}, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	if _, err := service.Rename(context.Background(), 7, 5, 2, "New Name"); !errors.Is(err, ErrNotGroupAdmin) {
		t.Fatalf("Rename() error = %v, want ErrNotGroupAdmin", err)
	}
}

func TestGroupServiceRenamePersistsNormalized(t *testing.T) {
	memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin}
	groups := &groupRepositoryStub{group: Group{ID: 5, Version: 2}}
	service, err := NewGroupService(groups, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	updated, err := service.Rename(context.Background(), 7, 5, 2, "  New   Name ")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if updated.Name != "New   Name" || updated.NameNorm != "new   name" {
		t.Fatalf("rename result = %+v, want trimmed name and normalized form", updated)
	}
	if groups.expectedVersion != 2 {
		t.Fatalf("expected version = %d, want 2", groups.expectedVersion)
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

type groupUserRepositoryStub struct {
	user User
}

func (r *groupUserRepositoryStub) CreateUser(context.Context, User) (User, error) {
	return User{}, nil
}

func (r *groupUserRepositoryStub) FindUserByID(context.Context, int64) (User, error) {
	if r.user.ID <= 0 {
		return User{}, ErrUserNotFound
	}
	return r.user, nil
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
	createErr       error
	findErr         error
	findByCodeErr   error
	updateErr       error
	expectedVersion int64
}

func (r *groupRepositoryStub) CreateGroup(_ context.Context, group Group) (Group, error) {
	if r.createErr != nil {
		return Group{}, r.createErr
	}
	group.ID = 1
	return group, nil
}

func (r *groupRepositoryStub) FindGroupByID(context.Context, int64) (Group, error) {
	if r.findErr != nil {
		return Group{}, r.findErr
	}
	return r.group, nil
}

func (r *groupRepositoryStub) FindGroupByInviteCode(context.Context, string) (Group, error) {
	if r.findByCodeErr != nil {
		return Group{}, r.findByCodeErr
	}
	return r.group, nil
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
	member            bool
	role              string
	members           []GroupMember
	collisions        int
	createCalls       int
	createErr         error
	adminUserID       int64
	adminRole         string
	updateErr         error
	deleteErr         error
	deleteCount       int
	createdMembership GroupMembership
}

func (r *groupMembershipRepositoryStub) CreateGroupWithAdmin(_ context.Context, group Group, userID int64, role string) (Group, GroupMembership, error) {
	r.createCalls++
	r.adminUserID = userID
	r.adminRole = role
	if r.createErr != nil {
		return Group{}, GroupMembership{}, r.createErr
	}
	if r.collisions > 0 {
		r.collisions--
		return Group{}, GroupMembership{}, ErrGroupInviteTaken
	}
	group.ID = int64(r.createCalls)
	group.Version = 0
	return group, GroupMembership{GroupID: group.ID, UserID: userID, Role: role}, nil
}

func (r *groupMembershipRepositoryStub) CreateMembership(_ context.Context, membership GroupMembership) (GroupMembership, error) {
	r.createdMembership = membership
	if r.createErr != nil {
		return GroupMembership{}, r.createErr
	}
	return membership, nil
}

func (r *groupMembershipRepositoryStub) FindMembership(context.Context, int64, int64) (GroupMembership, error) {
	if !r.member {
		return GroupMembership{}, ErrMembershipNotFound
	}
	return GroupMembership{GroupID: 5, UserID: 7, Role: r.role}, nil
}

func (r *groupMembershipRepositoryStub) ListMembers(context.Context, int64) ([]GroupMember, error) {
	return r.members, nil
}

func (r *groupMembershipRepositoryStub) CountMembers(context.Context, int64) (int, error) {
	if len(r.members) > 0 {
		return len(r.members), nil
	}
	if r.member {
		return 1, nil
	}
	return 0, nil
}

func (r *groupMembershipRepositoryStub) UpdateMembershipRole(_ context.Context, membership GroupMembership, _ int64) (GroupMembership, error) {
	if r.updateErr != nil {
		return GroupMembership{}, r.updateErr
	}
	return membership, nil
}

func (r *groupMembershipRepositoryStub) DeleteMembership(context.Context, int64, int64, int64) error {
	r.deleteCount++
	return r.deleteErr
}

func TestGroupServiceJoinIsIdempotent(t *testing.T) {
	memberships := &groupMembershipRepositoryStub{}
	groups := &groupRepositoryStub{group: Group{ID: 5, InviteCode: "ABCDEFGHJKLM"}}
	service, err := NewGroupService(groups, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	joined, err := service.Join(context.Background(), 8, " abcdefghjklm ")
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	if joined.Role != GroupRoleMember || joined.GroupID != 5 {
		t.Fatalf("Join() = %+v, want ordinary member of group 5", joined)
	}
	if memberships.createdMembership.Role != GroupRoleMember {
		t.Fatalf("created role = %q, want member", memberships.createdMembership.Role)
	}

	existing := &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin}
	service, err = NewGroupService(groups, existing, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	again, err := service.Join(context.Background(), 7, "ABCDEFGHJKLM")
	if err != nil {
		t.Fatalf("Join() existing error = %v", err)
	}
	if again.Role != GroupRoleAdmin || existing.createCalls != 0 {
		t.Fatalf("idempotent join = %+v calls %d, want existing admin membership", again, existing.createCalls)
	}
}

func TestGroupServiceJoinUnknownCode(t *testing.T) {
	groups := &groupRepositoryStub{findByCodeErr: ErrGroupNotFound}
	service, err := NewGroupService(groups, &groupMembershipRepositoryStub{}, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	if _, err := service.Join(context.Background(), 7, "UNKNOWN00000"); !errors.Is(err, ErrGroupInviteNotFound) {
		t.Fatalf("Join() error = %v, want ErrGroupInviteNotFound", err)
	}
}

func TestGroupServiceRotateInviteRequiresAdmin(t *testing.T) {
	memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleMember}
	service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, Version: 1}}, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	if _, err := service.RotateInvite(context.Background(), 7, 5); !errors.Is(err, ErrNotGroupAdmin) {
		t.Fatalf("RotateInvite() error = %v, want ErrNotGroupAdmin", err)
	}
}

func TestGroupServiceRotateInviteChangesCode(t *testing.T) {
	memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin}
	groups := &groupRepositoryStub{group: Group{ID: 5, InviteCode: "OLDCODE12345", Version: 1}}
	service, err := NewGroupService(groups, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	code, err := service.RotateInvite(context.Background(), 7, 5)
	if err != nil {
		t.Fatalf("RotateInvite() error = %v", err)
	}
	if len(code) != groupInviteCodeLength || code == "OLDCODE12345" {
		t.Fatalf("rotated code = %q, want a fresh invite code", code)
	}
}

func TestGroupServiceChangeRoleGuardsLastAdmin(t *testing.T) {
	memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin, updateErr: ErrLastGroupAdmin}
	service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, Version: 1}}, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	if _, err := service.ChangeRole(context.Background(), 7, 5, 8, 0, GroupRoleMember); !errors.Is(err, ErrLastGroupAdmin) {
		t.Fatalf("ChangeRole() error = %v, want ErrLastGroupAdmin", err)
	}
	if _, err := service.ChangeRole(context.Background(), 7, 5, 8, 0, "owner"); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("ChangeRole(invalid role) error = %v, want ErrInvalidGroup", err)
	}
}

func TestGroupServiceRemoveAndLeave(t *testing.T) {
	memberships := &groupMembershipRepositoryStub{member: true, role: GroupRoleAdmin}
	service, err := NewGroupService(&groupRepositoryStub{group: Group{ID: 5, Version: 1}}, memberships, &groupUserRepositoryStub{user: User{ID: 7}})
	if err != nil {
		t.Fatalf("NewGroupService() error = %v", err)
	}
	if err := service.RemoveMember(context.Background(), 7, 5, 7, 0); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("RemoveMember(self) error = %v, want ErrInvalidGroup", err)
	}
	if err := service.RemoveMember(context.Background(), 7, 5, 8, 0); err != nil {
		t.Fatalf("RemoveMember() error = %v, want nil", err)
	}
	if memberships.deleteCount != 1 {
		t.Fatalf("delete calls = %d, want 1", memberships.deleteCount)
	}
	if err := service.Leave(context.Background(), 7, 5); err != nil {
		t.Fatalf("Leave() error = %v, want nil", err)
	}
	if memberships.deleteCount != 2 {
		t.Fatalf("delete calls after leave = %d, want 2", memberships.deleteCount)
	}
}
