package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAdminUserQueryNormalizeBoundsValues(t *testing.T) {
	tests := []struct {
		name  string
		input AdminUserQuery
		want  AdminUserQuery
	}{
		{
			name:  "defaults",
			input: AdminUserQuery{},
			want:  AdminUserQuery{Sort: AdminSortCreatedAt, Direction: AdminDirectionDesc, Page: 1, Size: DefaultAdminPageSize},
		},
		{
			name:  "explicit values preserved",
			input: AdminUserQuery{Search: " alice ", Admin: "yes", Guest: "no", Confirmed: "yes", Sort: "email", Direction: "asc", Page: 3, Size: 25},
			want:  AdminUserQuery{Search: "alice", Admin: "yes", Guest: "no", Confirmed: "yes", Sort: "email", Direction: "asc", Page: 3, Size: 25},
		},
		{
			name:  "negative page and oversized size are bounded",
			input: AdminUserQuery{Page: -4, Size: 10_000},
			want:  AdminUserQuery{Sort: AdminSortCreatedAt, Direction: AdminDirectionDesc, Page: 1, Size: MaxAdminPageSize},
		},
		{
			name:  "unknown tri-state filters reset to all",
			input: AdminUserQuery{Admin: "maybe", Guest: "1", Confirmed: "true"},
			want:  AdminUserQuery{Sort: AdminSortCreatedAt, Direction: AdminDirectionDesc, Page: 1, Size: DefaultAdminPageSize},
		},
		{
			name:  "unsupported sort falls back to created_at",
			input: AdminUserQuery{Sort: "password_hash", Direction: "sideways"},
			want:  AdminUserQuery{Sort: AdminSortCreatedAt, Direction: AdminDirectionDesc, Page: 1, Size: DefaultAdminPageSize},
		},
		{
			name:  "id sort defaults to descending",
			input: AdminUserQuery{Sort: "id"},
			want:  AdminUserQuery{Sort: AdminSortID, Direction: AdminDirectionDesc, Page: 1, Size: DefaultAdminPageSize},
		},
		{
			name:  "text sort defaults to ascending",
			input: AdminUserQuery{Sort: "username"},
			want:  AdminUserQuery{Sort: AdminSortUsername, Direction: AdminDirectionAsc, Page: 1, Size: DefaultAdminPageSize},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.input.Normalize()
			if got != test.want {
				t.Fatalf("Normalize() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestAdminListQueriesBoundHugePageNumbers(t *testing.T) {
	// (page-1)*size on this page overflows int64 and used to reach PostgreSQL
	// as a negative OFFSET, failing the page with a 500.
	const huge = 92233720368547758
	users := AdminUserQuery{Page: huge, Size: MaxAdminPageSize}.Normalize()
	audit := AuditQuery{Page: huge, Size: MaxAuditPageSize}.Normalize()
	for name, got := range map[string][2]int{"accounts": {users.Page, users.Offset()}, "audit": {audit.Page, audit.Offset()}} {
		if got[0] != MaxAdminPage || got[1] != (MaxAdminPage-1)*100 {
			t.Errorf("%s page/offset = %d/%d, want %d/%d", name, got[0], got[1], MaxAdminPage, (MaxAdminPage-1)*100)
		}
	}
	if offset := (AdminUserQuery{Page: huge, Size: MaxAdminPageSize}).Offset(); offset < 0 {
		t.Errorf("unnormalized offset = %d, want non-negative", offset)
	}
}

func TestAdminUserQueryNormalizeBoundsSearchLength(t *testing.T) {
	long := make([]rune, maxAdminSearchLength+50)
	for index := range long {
		long[index] = 'a'
	}
	got := (AdminUserQuery{Search: string(long)}).Normalize()
	if len([]rune(got.Search)) != maxAdminSearchLength {
		t.Fatalf("search length = %d, want %d", len([]rune(got.Search)), maxAdminSearchLength)
	}
}

func TestAdminUserQueryOffset(t *testing.T) {
	if got := (AdminUserQuery{Page: 1, Size: 20}).Offset(); got != 0 {
		t.Fatalf("Offset(page 1) = %d, want 0", got)
	}
	if got := (AdminUserQuery{Page: 3, Size: 20}).Offset(); got != 40 {
		t.Fatalf("Offset(page 3) = %d, want 40", got)
	}
	if got := (AdminUserQuery{Page: 0, Size: 20}).Offset(); got != 0 {
		t.Fatalf("Offset(page 0) = %d, want 0", got)
	}
}

func TestAdminSearchPatternEscapesWildcards(t *testing.T) {
	got := AdminSearchPattern(`50%_off\deal`)
	want := `%50\%\_off\\deal%`
	if got != want {
		t.Fatalf("AdminSearchPattern() = %q, want %q", got, want)
	}
}

func TestAdminAccountServiceListNormalizesAndCountsPages(t *testing.T) {
	repository := &adminUserRepositoryStub{total: 45}
	service, err := NewAdminAccountService(&userRepositoryStub{}, repository, nil, nil)
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	page, err := service.List(context.Background(), AdminUserQuery{Size: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repository.received.Size != 20 || repository.received.Page != 1 {
		t.Fatalf("repository query = %+v, want normalized size 20 page 1", repository.received)
	}
	if page.Total != 45 || page.Pages != 3 {
		t.Fatalf("page = %+v, want total 45 pages 3", page)
	}
}

func TestAdminAccountServiceListRejectsMissingContext(t *testing.T) {
	service, err := NewAdminAccountService(&userRepositoryStub{}, &adminUserRepositoryStub{}, nil, nil)
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	//nolint:staticcheck // intentional nil context
	if _, err := service.List(nil, AdminUserQuery{}); err == nil {
		t.Fatal("List(nil) error = nil, want failure")
	}
}

func TestNewAdminAccountServiceRejectsMissingPorts(t *testing.T) {
	if _, err := NewAdminAccountService(nil, &adminUserRepositoryStub{}, nil, nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("error = %v, want %v", err, ErrNilUserRepository)
	}
	if _, err := NewAdminAccountService(&userRepositoryStub{}, nil, nil, nil); !errors.Is(err, ErrNilAdminUserRepository) {
		t.Fatalf("error = %v, want %v", err, ErrNilAdminUserRepository)
	}
}

func TestAdminAccountServiceUpdateChangesRoleAtomically(t *testing.T) {
	email := "member@example.test"
	users := &userRepositoryStub{created: User{ID: 9, Email: &email}}
	auditor := &auditRecorderStub{}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, auditor, &sessionRevokerStub{})
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	if _, err := service.Update(context.Background(), AdminActionContext{ActorID: 1, Origin: "198.51.100.4"}, 9, AdminAccountUpdateInput{
		IsAdmin: true,
		Version: 3,
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if users.updateVersion != 3 {
		t.Fatalf("update version = %d, want 3", users.updateVersion)
	}
	if users.lastUpdated.Email == nil || *users.lastUpdated.Email != email {
		t.Fatalf("updated email = %v, want the issuer-owned email kept", users.lastUpdated.Email)
	}
	if !users.lastUpdated.IsAdmin {
		t.Fatal("updated account is not an administrator")
	}
	var actions []string
	for _, record := range auditor.records {
		actions = append(actions, record.Action)
	}
	// Granting administrator rights must be findable on its own, not hidden
	// inside an ordinary account edit.
	if strings.Join(actions, ",") != AuditActionAccountUpdated+","+AuditActionAdminGranted {
		t.Fatalf("audit actions = %v, want updated, admin granted", actions)
	}
}

func TestAdminAccountServiceUpdateClassifiesWriteFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "conflict", err: ErrOptimisticLockConflict, want: ErrOptimisticLockConflict},
		{name: "missing", err: ErrUserNotFound, want: ErrRecordNotFound},
		{name: "duplicate", err: ErrDuplicateEmail, want: ErrDuplicateEmail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users := &userRepositoryStub{created: User{ID: 9}, updateErr: test.err}
			service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, nil, nil)
			if err != nil {
				t.Fatalf("NewAdminAccountService() error = %v", err)
			}
			_, err = service.Update(context.Background(), AdminActionContext{ActorID: 1}, 9, AdminAccountUpdateInput{})
			if !errors.Is(err, test.want) {
				t.Fatalf("Update() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAdminAccountServiceUpdateRejectsInvalidInput(t *testing.T) {
	service, err := NewAdminAccountService(&userRepositoryStub{created: User{ID: 9}}, &adminUserRepositoryStub{}, nil, &sessionRevokerStub{})
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	cases := []struct {
		name  string
		actor AdminActionContext
		id    int64
		input AdminAccountUpdateInput
		want  error
	}{
		{name: "missing actor", id: 9, input: AdminAccountUpdateInput{}, want: ErrInvalidAdminQuery},
		{name: "missing target", actor: AdminActionContext{ActorID: 1}, input: AdminAccountUpdateInput{}, want: ErrInvalidAdminQuery},
		{name: "negative version", actor: AdminActionContext{ActorID: 1}, id: 9, input: AdminAccountUpdateInput{Version: -1}, want: ErrInvalidAdminQuery},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Update(context.Background(), test.actor, test.id, test.input); !errors.Is(err, test.want) {
				t.Fatalf("Update() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAdminAccountServiceFindMapsMissingAccount(t *testing.T) {
	users := &userRepositoryStub{findErr: ErrUserNotFound}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, nil, nil)
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	if _, err := service.Find(context.Background(), 9); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("Find() error = %v, want %v", err, ErrRecordNotFound)
	}
}

func TestAdminAccountServiceDeleteRemovesAccountAndRecordsAudit(t *testing.T) {
	users := &userRepositoryStub{created: User{ID: 9, Email: strPtr("gone@example.test")}}
	auditor := &auditRecorderStub{}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, auditor, nil)
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	if err := service.Delete(context.Background(), AdminActionContext{ActorID: 1}, 9, 5); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if users.deletedID != 9 || users.deletedVersion != 5 {
		t.Fatalf("delete = id %d version %d, want 9/5", users.deletedID, users.deletedVersion)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionAccountDeleted {
		t.Fatalf("audit records = %+v, want one account-deleted record", auditor.records)
	}
}

// deleteOrderRevoker records whether the account still existed when its
// sessions were revoked: deletion cascades away the SSO session records.
type deleteOrderRevoker struct {
	users           *userRepositoryStub
	revokedUserID   int64
	revokedBeforeDB bool
	err             error
}

func (r *deleteOrderRevoker) RevokeUser(_ context.Context, userID int64) error {
	r.revokedUserID = userID
	r.revokedBeforeDB = r.users.deleteCalls == 0
	return r.err
}

func TestAdminAccountServiceDeleteRevokesSessionsBeforeDeleting(t *testing.T) {
	users := &userRepositoryStub{created: User{ID: 9}}
	revoker := &deleteOrderRevoker{users: users}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, nil, revoker)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), AdminActionContext{ActorID: 1}, 9, 0); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if revoker.revokedUserID != 9 || !revoker.revokedBeforeDB {
		t.Fatalf("revoked user %d before delete = %t, want 9 revoked first", revoker.revokedUserID, revoker.revokedBeforeDB)
	}

	failing := &userRepositoryStub{created: User{ID: 9}}
	service, err = NewAdminAccountService(failing, &adminUserRepositoryStub{}, nil, &deleteOrderRevoker{users: failing, err: ErrSSOUnavailable})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), AdminActionContext{ActorID: 1}, 9, 0); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatalf("Delete() error = %v, want the revocation failure", err)
	}
	if failing.deleteCalls != 0 {
		t.Fatal("account deleted although its sessions could not be revoked")
	}
}

func TestAdminAccountServiceDeleteRefusesSelf(t *testing.T) {
	users := &userRepositoryStub{created: User{ID: 9}}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, nil, nil)
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	if err := service.Delete(context.Background(), AdminActionContext{ActorID: 9}, 9, 0); !errors.Is(err, ErrAdminSelfAction) {
		t.Fatalf("Delete(own) error = %v, want %v", err, ErrAdminSelfAction)
	}
	if users.deleteCalls != 0 {
		t.Fatal("DeleteUser called for a self-delete")
	}
}

func TestAdminAccountServiceDeleteClassifiesFailures(t *testing.T) {
	tests := []struct {
		name    string
		findErr error
		err     error
		want    error
	}{
		{name: "missing on find", findErr: ErrUserNotFound, want: ErrRecordNotFound},
		{name: "missing on delete", err: ErrUserNotFound, want: ErrRecordNotFound},
		{name: "stale revision", err: ErrOptimisticLockConflict, want: ErrOptimisticLockConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users := &userRepositoryStub{created: User{ID: 9}, findErr: test.findErr, deleteErr: test.err}
			service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, nil, nil)
			if err != nil {
				t.Fatalf("NewAdminAccountService() error = %v", err)
			}
			if err := service.Delete(context.Background(), AdminActionContext{ActorID: 1}, 9, 0); !errors.Is(err, test.want) {
				t.Fatalf("Delete() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAdminAccountServiceUpdateRefusesSelfDemotion(t *testing.T) {
	users := &userRepositoryStub{created: User{ID: 9}}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, nil, nil)
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	_, err = service.Update(context.Background(), AdminActionContext{ActorID: 9}, 9, AdminAccountUpdateInput{IsAdmin: false})
	if !errors.Is(err, ErrAdminSelfAction) {
		t.Fatalf("Update(self demotion) error = %v, want %v", err, ErrAdminSelfAction)
	}
	if users.updateVersion != 0 {
		t.Fatal("UpdateUser called for a refused self-demotion")
	}
}

type adminUserRepositoryStub struct {
	received AdminUserQuery
	total    int
	err      error
}

func (s *adminUserRepositoryStub) ListUsers(_ context.Context, query AdminUserQuery) (AdminUserPage, error) {
	s.received = query
	if s.err != nil {
		return AdminUserPage{}, s.err
	}
	return AdminUserPage{Total: s.total, Page: query.Page, Size: query.Size}, nil
}

type userRepositoryStub struct {
	created        User
	createErr      error
	createCalls    int
	lastCreated    User
	findErr        error
	updateErr      error
	updateVersion  int64
	lastUpdated    User
	deleteErr      error
	deleteCalls    int
	deletedID      int64
	deletedVersion int64
}

func (s *userRepositoryStub) CreateUser(_ context.Context, user User) (User, error) {
	s.createCalls++
	s.lastCreated = user
	if s.createErr != nil {
		return User{}, s.createErr
	}
	if s.created.ID == 0 {
		s.created = user
		s.created.ID = 9
	}
	return s.created, nil
}

func (s *userRepositoryStub) FindUserByID(_ context.Context, id int64) (User, error) {
	if s.findErr != nil {
		return User{}, s.findErr
	}
	if s.created.ID != 0 {
		current := s.created
		current.ID = id
		return current, nil
	}
	return User{ID: id, Email: strPtr("actor@example.test")}, nil
}
func (s *userRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	return User{}, nil
}
func (s *userRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	return User{}, nil
}
func (s *userRepositoryStub) UpdateUser(_ context.Context, user User, expectedVersion int64) (User, error) {
	s.updateVersion = expectedVersion
	s.lastUpdated = user
	if s.updateErr != nil {
		return User{}, s.updateErr
	}
	return user, nil
}
func (s *userRepositoryStub) DeleteUser(_ context.Context, id, expectedVersion int64) error {
	s.deleteCalls++
	s.deletedID = id
	s.deletedVersion = expectedVersion
	return s.deleteErr
}

type auditRecorderStub struct {
	records []AuditRecord
	err     error
}

func (s *auditRecorderStub) Record(_ context.Context, record AuditRecord) error {
	s.records = append(s.records, record)
	return s.err
}

func TestAdminAccountServiceRevokeSessionsEndsAndAudits(t *testing.T) {
	users := &userRepositoryStub{created: User{ID: 9, Email: strPtr("revoke@example.test")}}
	auditor := &auditRecorderStub{}
	revoker := &sessionRevokerStub{}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, auditor, revoker)
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	if err := service.RevokeSessions(context.Background(), AdminActionContext{ActorID: 1}, 9); err != nil {
		t.Fatalf("RevokeSessions() error = %v", err)
	}
	if revoker.userID != 9 {
		t.Fatalf("revoked user = %d, want 9", revoker.userID)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionSessionsRevoked {
		t.Fatalf("audit records = %+v, want one sessions-revoked record", auditor.records)
	}
}

func TestAdminAccountServiceRevokeSessionsMapsMissingAccount(t *testing.T) {
	users := &userRepositoryStub{findErr: ErrUserNotFound}
	service, err := NewAdminAccountService(users, &adminUserRepositoryStub{}, nil, &sessionRevokerStub{})
	if err != nil {
		t.Fatalf("NewAdminAccountService() error = %v", err)
	}
	if err := service.RevokeSessions(context.Background(), AdminActionContext{ActorID: 1}, 9); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("RevokeSessions() error = %v, want %v", err, ErrRecordNotFound)
	}
}

type sessionRevokerStub struct {
	userID int64
	err    error
}

func (s *sessionRevokerStub) RevokeUser(_ context.Context, userID int64) error {
	s.userID = userID
	return s.err
}
