package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestGroupsListRequiresAuthentication(t *testing.T) {
	handler, _ := newGroupsTestHandler(t, settingsStubDependencies(t))
	request := httptest.NewRequest(http.MethodGet, "/en/groups", nil)
	response := httptest.NewRecorder()
	handler.list(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
}

func TestGroupsListRendersMemberships(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Groups = &groupsUseCaseStub{summaries: []service.GroupSummary{{Group: service.Group{ID: 3, Name: "Team"}, MemberCount: 2, Role: service.GroupRoleAdmin}}}
	handler, _ := newGroupsTestHandler(t, dependencies)

	response := httptest.NewRecorder()
	handler.list(response, authenticatedRequest(http.MethodGet, "/en/groups", 7, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "Team") {
		t.Errorf("list missing group name")
	}
}

func TestGroupsCreateRejectsInvalidName(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Groups = &groupsUseCaseStub{createErr: service.ErrInvalidGroupName}
	handler, _ := newGroupsTestHandler(t, dependencies)

	response := httptest.NewRecorder()
	handler.create(response, authenticatedRequest(http.MethodPost, "/en/groups", 7, "name="))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(response.Body.String(), "64") {
		t.Errorf("body missing invalid-name message")
	}
}

func TestGroupsCreateRedirectsToDetail(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Groups = &groupsUseCaseStub{summary: service.GroupSummary{Group: service.Group{ID: 3, Name: "Team"}, Role: service.GroupRoleAdmin}}
	handler, _ := newGroupsTestHandler(t, dependencies)

	response := httptest.NewRecorder()
	handler.create(response, authenticatedRequest(http.MethodPost, "/en/groups", 7, "name=Team"))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if location := response.Header().Get("Location"); location != "/en/groups/3" {
		t.Fatalf("redirect = %q, want /en/groups/3", location)
	}
}

func TestGroupDetailVisibility(t *testing.T) {
	tests := []struct {
		name       string
		detail     service.GroupDetail
		detailErr  error
		wantStatus int
		wantCode   string
		rejectCode string
		// wantInBody must appear in the rendered page.
		wantInBody string
	}{
		{
			name: "administrator sees invite code and controls",
			detail: service.GroupDetail{Group: service.Group{ID: 3, Name: "Team", Version: 1}, IsMember: true, IsAdmin: true, ViewerRole: service.GroupRoleAdmin,
				InviteCode: "ABCDEFGHJKLM", Members: []service.GroupMember{{UserID: 7, Role: service.GroupRoleAdmin, Version: 0}, {UserID: 8, Role: service.GroupRoleMember, Version: 4}}},
			wantStatus: http.StatusOK,
			wantCode:   "ABCDEFGHJKLM",
			// Roster forms carry each membership's own revision for the
			// stale-write check.
			wantInBody: `<input type="hidden" name="version" value="4">`,
		},
		{
			name: "member cannot see invite code",
			detail: service.GroupDetail{Group: service.Group{ID: 3, Name: "Team", Version: 1}, IsMember: true, ViewerRole: service.GroupRoleMember,
				InviteCode: "", Members: []service.GroupMember{{UserID: 7, Role: service.GroupRoleMember, Version: 0}}},
			wantStatus: http.StatusOK,
			rejectCode: "ABCDEFGHJKLM",
		},
		{
			name:       "non-member gets not found",
			detailErr:  service.ErrGroupNotFound,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := settingsStubDependencies(t)
			dependencies.Groups = &groupsUseCaseStub{detail: test.detail, detailErr: test.detailErr}
			handler, _ := newGroupsTestHandler(t, dependencies)
			response := httptest.NewRecorder()
			handler.detail(response, authenticatedRequest(http.MethodGet, "/en/groups/3", 7, ""))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantCode != "" && !strings.Contains(response.Body.String(), test.wantCode) {
				t.Errorf("body missing invite code")
			}
			if test.rejectCode != "" && strings.Contains(response.Body.String(), test.rejectCode) {
				t.Errorf("body unexpectedly exposes invite code")
			}
			if test.wantInBody != "" && !strings.Contains(response.Body.String(), test.wantInBody) {
				t.Errorf("body missing %s", test.wantInBody)
			}
		})
	}
}

func TestGroupsListLabelsAllGroupsAndNonMembersForAdmin(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, IsAdmin: true, Version: 1}}
	dependencies.Groups = &groupsUseCaseStub{summaries: []service.GroupSummary{
		{Group: service.Group{ID: 3, Name: "Team"}, MemberCount: 2, Role: service.GroupRoleAdmin},
		{Group: service.Group{ID: 4, Name: "Other"}, MemberCount: 4},
	}}
	handler, _ := newGroupsTestHandler(t, dependencies)

	response := httptest.NewRecorder()
	handler.list(response, authenticatedRequest(http.MethodGet, "/en/groups", 7, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "All groups") {
		t.Errorf("admin list missing all-groups heading")
	}
	if !strings.Contains(body, "Other") {
		t.Errorf("admin list missing non-member group")
	}
	if !strings.Contains(body, "Not a member") {
		t.Errorf("admin list missing non-member role label")
	}
}

func TestGroupDetailHidesLeaveForNonMemberAdmin(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, IsAdmin: true, Version: 1}}
	dependencies.Groups = &groupsUseCaseStub{detail: service.GroupDetail{
		Group:      service.Group{ID: 3, Name: "Team", Version: 1},
		IsAdmin:    true,
		IsMember:   false,
		InviteCode: "ABCDEFGHJKLM",
		Members:    []service.GroupMember{{UserID: 8, Role: service.GroupRoleAdmin, Version: 0}},
	}}
	handler, _ := newGroupsTestHandler(t, dependencies)

	response := httptest.NewRecorder()
	handler.detail(response, authenticatedRequest(http.MethodGet, "/en/groups/3", 7, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "ABCDEFGHJKLM") {
		t.Errorf("non-member admin detail missing invite code")
	}
	if !strings.Contains(body, "Group name") {
		t.Errorf("non-member admin detail missing rename control")
	}
	if strings.Contains(body, "Leave group") {
		t.Errorf("non-member admin detail unexpectedly offers leave")
	}
}

func TestGroupsJoinRateLimited(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.JoinGroup = &rateLimitedJoinStub{err: service.RateLimitError{RetryAfter: 30}}
	handler, _ := newGroupsTestHandler(t, dependencies)
	response := httptest.NewRecorder()
	handler.join(response, authenticatedRequest(http.MethodPost, "/en/groups/join", 7, "code=ABCDEFGHJKLM"))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
}

func TestGroupsJoinInvalidCode(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.JoinGroup = &rateLimitedJoinStub{err: service.ErrGroupInviteNotFound}
	handler, _ := newGroupsTestHandler(t, dependencies)
	response := httptest.NewRecorder()
	handler.join(response, authenticatedRequest(http.MethodPost, "/en/groups/join", 7, "code=UNKNOWN"))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if strings.Contains(response.Body.String(), "UNKNOWN") {
		t.Errorf("body echoes the submitted invite code")
	}
}

func TestGroupsRenameConflict(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Groups = &groupsUseCaseStub{
		detail:    service.GroupDetail{Group: service.Group{ID: 3, Name: "Team", Version: 2}, IsMember: true, IsAdmin: true},
		renameErr: service.ErrOptimisticLockConflict,
	}
	handler, _ := newGroupsTestHandler(t, dependencies)
	response := httptest.NewRecorder()
	handler.rename(response, authenticatedRequest(http.MethodPost, "/en/groups/3/rename", 7, "name=New&version=1"))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
	if !strings.Contains(response.Body.String(), "changed elsewhere") {
		t.Errorf("body missing conflict message")
	}
}

func TestGroupsChangeRoleForbidden(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Groups = &groupsUseCaseStub{
		detail:  service.GroupDetail{Group: service.Group{ID: 3, Name: "Team"}, IsMember: true, IsAdmin: true},
		roleErr: service.ErrNotGroupAdmin,
	}
	handler, _ := newGroupsTestHandler(t, dependencies)
	request := authenticatedRequest(http.MethodPost, "/en/groups/3/members/8/role", 7, "role=admin&version=0")
	response := httptest.NewRecorder()
	handler.changeRole(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestGroupsRosterWritesRequireARevision(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "role without version", path: "/en/groups/3/members/8/role", body: "role=admin"},
		{name: "role with malformed version", path: "/en/groups/3/members/8/role", body: "role=admin&version=latest"},
		{name: "remove without version", path: "/en/groups/3/members/8/remove", body: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies := settingsStubDependencies(t)
			// The stub would succeed, so only the missing revision can refuse.
			dependencies.Groups = &groupsUseCaseStub{detail: service.GroupDetail{Group: service.Group{ID: 3, Name: "Team"}, IsMember: true, IsAdmin: true}}
			handler, _ := newGroupsTestHandler(t, dependencies)
			response := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodPost, test.path, 7, test.body)
			request.SetPathValue("memberID", "8")
			if strings.HasSuffix(test.path, "/role") {
				handler.changeRole(response, request)
			} else {
				handler.removeMember(response, request)
			}
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "changed elsewhere") {
				t.Fatalf("status = %d, want 422 with the reload message", response.Code)
			}
		})
	}
}

func TestGroupsLeaveRedirectsToList(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Groups = &groupsUseCaseStub{}
	handler, _ := newGroupsTestHandler(t, dependencies)
	request := authenticatedRequest(http.MethodPost, "/en/groups/3/leave", 7, "")
	response := httptest.NewRecorder()
	handler.leave(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if location := response.Header().Get("Location"); location != "/en/groups" {
		t.Fatalf("redirect = %q, want /en/groups", location)
	}
}

func newGroupsTestHandler(t *testing.T, dependencies Dependencies) (*groupHandler, *PageRenderer) {
	t.Helper()
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v", err)
	}
	return newGroupHandler(renderer, dependencies), renderer
}

type groupsUseCaseStub struct {
	summaries []service.GroupSummary
	summary   service.GroupSummary
	detail    service.GroupDetail
	createErr error
	listErr   error
	detailErr error
	renameErr error
	roleErr   error
	removeErr error
	leaveErr  error
}

func (s *groupsUseCaseStub) Create(context.Context, int64, string) (service.GroupSummary, error) {
	return s.summary, s.createErr
}

func (s *groupsUseCaseStub) List(context.Context, int64) ([]service.GroupSummary, error) {
	return s.summaries, s.listErr
}

func (s *groupsUseCaseStub) Detail(context.Context, int64, int64) (service.GroupDetail, error) {
	return s.detail, s.detailErr
}

func (s *groupsUseCaseStub) Join(context.Context, int64, string) (service.GroupMembership, error) {
	return service.GroupMembership{}, nil
}

func (s *groupsUseCaseStub) Rename(context.Context, int64, int64, int64, string) (service.Group, error) {
	if s.renameErr != nil {
		return service.Group{}, s.renameErr
	}
	return s.detail.Group, nil
}

func (s *groupsUseCaseStub) ChangeRole(context.Context, int64, int64, int64, int64, string) (service.GroupMembership, error) {
	return service.GroupMembership{}, s.roleErr
}

func (s *groupsUseCaseStub) RemoveMember(context.Context, int64, int64, int64, int64) error {
	return s.removeErr
}

func (s *groupsUseCaseStub) Leave(context.Context, int64, int64) error { return s.leaveErr }

type rateLimitedJoinStub struct {
	membership service.GroupMembership
	err        error
}

func (s *rateLimitedJoinStub) Join(context.Context, int64, string) (service.GroupMembership, error) {
	if s.err != nil {
		return service.GroupMembership{}, s.err
	}
	return s.membership, nil
}
