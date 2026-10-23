package service

import (
	"context"
	"errors"
	"testing"
)

func TestAdminDiagnosticsServiceDetailAggregatesSafeData(t *testing.T) {
	email := "target@example.test"
	verified := int64(120)
	users := &userRepositoryStub{created: User{ID: 9, Email: &email, IsAdmin: true, EmailVerifiedAt: &verified, Version: 2}}
	sessions := &adminSessionRepositoryStub{sessions: []Session{{ID: "digest", UserID: 9, CreatedAt: 1, ExpiresAt: 2}}, count: 1}
	identities := &oauthIdentityRepositoryStub{listed: []OAuthIdentity{{ID: 4, UserID: 9, Provider: "example", Email: email}}}
	diagnostics, err := NewAdminDiagnosticsService(users, sessions, identities)
	if err != nil {
		t.Fatalf("NewAdminDiagnosticsService() error = %v", err)
	}
	detail, err := diagnostics.Detail(context.Background(), 9)
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if !detail.Confirmed {
		t.Fatalf("detail = %+v, want confirmed account", detail)
	}
	if len(detail.Sessions) != 1 || len(detail.Identities) != 1 {
		t.Fatalf("detail aggregates = %+v, want one of each", detail)
	}
}

func TestAdminDiagnosticsServiceDetailMapsMissingAccount(t *testing.T) {
	users := &userRepositoryStub{findErr: ErrUserNotFound}
	diagnostics, err := NewAdminDiagnosticsService(users, &adminSessionRepositoryStub{}, &oauthIdentityRepositoryStub{})
	if err != nil {
		t.Fatalf("NewAdminDiagnosticsService() error = %v", err)
	}
	if _, err := diagnostics.Detail(context.Background(), 9); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("Detail() error = %v, want %v", err, ErrRecordNotFound)
	}
}

func TestNewAdminDiagnosticsServiceRejectsMissingPorts(t *testing.T) {
	if _, err := NewAdminDiagnosticsService(nil, &adminSessionRepositoryStub{}, &oauthIdentityRepositoryStub{}); !errors.Is(err, ErrNilAdminDiagnosticsRepository) {
		t.Fatalf("error = %v, want %v", err, ErrNilAdminDiagnosticsRepository)
	}
}

type adminSessionRepositoryStub struct {
	sessions []Session
	count    int
	err      error
}

func (s *adminSessionRepositoryStub) ListActiveSessions(context.Context, int64) ([]Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.sessions, nil
}

func (s *adminSessionRepositoryStub) CountActiveSessions(context.Context, int64) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.count, nil
}
