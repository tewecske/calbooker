package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var errSessionLookup = errors.New("lookup failed")

func TestSessionServiceCreate(t *testing.T) {
	repository := &sessionRepositoryStub{}
	service, err := NewSessionService(repository, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}

	session, err := service.Create(context.Background(), 42)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if len(session.ID) != maxSessionID {
		t.Fatalf("session ID length = %d, want %d", len(session.ID), maxSessionID)
	}
	if session.ID != repository.created.ID {
		t.Fatal("Create() did not persist returned session")
	}
	if session.UserID != 42 {
		t.Errorf("session user id = %d, want 42", session.UserID)
	}
	if session.ExpiresAt-session.CreatedAt != int64((24*time.Hour)/time.Second) {
		t.Errorf("session lifetime = %d seconds, want %d", session.ExpiresAt-session.CreatedAt, int64((24*time.Hour)/time.Second))
	}
	if strings.Contains(session.ID, " ") {
		t.Error("session ID contains whitespace")
	}

	second, err := service.Create(context.Background(), 42)
	if err != nil {
		t.Fatalf("Create() second error = %v, want nil", err)
	}
	if session.ID == second.ID {
		t.Fatal("Create() generated duplicate session IDs")
	}
}

func TestSessionServiceLookup(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name      string
		session   Session
		findErr   error
		expected  error
		wantFound bool
	}{
		{name: "active", session: Session{ID: "session-id", UserID: 42, CreatedAt: now - 10, ExpiresAt: now + 10}, wantFound: true},
		{name: "expired", session: Session{ID: "session-id", UserID: 42, CreatedAt: now - 20, ExpiresAt: now}, expected: ErrSessionExpired},
		{name: "revoked", session: Session{ID: "session-id", UserID: 42, CreatedAt: now - 10, ExpiresAt: now + 10, RevokedAt: &now}, expected: ErrSessionRevoked},
		{name: "missing", expected: ErrSessionNotFound},
		{name: "repository failure", findErr: errSessionLookup, expected: errSessionLookup},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &sessionRepositoryStub{session: test.session, findErr: test.findErr}
			service, err := NewSessionService(repository, time.Hour)
			if err != nil {
				t.Fatalf("NewSessionService() error = %v, want nil", err)
			}
			got, err := service.Lookup(context.Background(), "session-id")
			if !errors.Is(err, test.expected) {
				t.Fatalf("Lookup() error = %v, want errors.Is(_, %v)", err, test.expected)
			}
			if test.wantFound && got.ID != test.session.ID {
				t.Errorf("Lookup() ID = %q, want %q", got.ID, test.session.ID)
			}
		})
	}
}

func TestSessionServiceRevoke(t *testing.T) {
	repository := &sessionRepositoryStub{}
	service, err := NewSessionService(repository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}

	if err := service.Revoke(context.Background(), "session-id"); err != nil {
		t.Fatalf("Revoke() error = %v, want nil", err)
	}
	if repository.revokedID != "session-id" || repository.revokedAt <= 0 {
		t.Fatalf("Revoke() recorded ID/time = %q/%d", repository.revokedID, repository.revokedAt)
	}
	if err := service.RevokeUser(context.Background(), 42); err != nil {
		t.Fatalf("RevokeUser() error = %v, want nil", err)
	}
	if repository.revokedUserID != 42 {
		t.Errorf("RevokeUser() user id = %d, want 42", repository.revokedUserID)
	}
}

func TestSessionServiceCreateSSOStoresTheBindingButNeverReturnsTheToken(t *testing.T) {
	repository := &sessionRepositoryStub{}
	service, err := NewSessionService(repository, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSSO(context.Background(), 42, "sid-1", "id.token")
	if err != nil {
		t.Fatal(err)
	}
	if repository.created.SSOSessionID != "sid-1" || repository.created.SSOIDToken != "id.token" {
		t.Fatalf("stored binding = %q/%q", repository.created.SSOSessionID, repository.created.SSOIDToken)
	}
	if session.SSOSessionID != "sid-1" || session.SSOIDToken != "" {
		t.Fatalf("returned session sid %q token %q, want the sid without the token", session.SSOSessionID, session.SSOIDToken)
	}
	for _, binding := range [][2]string{{"", "id.token"}, {"sid-1", ""}} {
		if _, err := service.CreateSSO(context.Background(), 42, binding[0], binding[1]); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("CreateSSO(%q, %q) error = %v, want %v", binding[0], binding[1], err, ErrInvalidSession)
		}
	}
}

func TestNewSessionServiceRejectsInvalidInputs(t *testing.T) {
	if _, err := NewSessionService(nil, time.Hour); !errors.Is(err, ErrNilSessionRepository) {
		t.Fatalf("NewSessionService(nil) error = %v, want %v", err, ErrNilSessionRepository)
	}
	if _, err := NewSessionService(&sessionRepositoryStub{}, 500*time.Millisecond); !errors.Is(err, ErrInvalidSessionLifetime) {
		t.Fatalf("NewSessionService(short lifetime) error = %v, want %v", err, ErrInvalidSessionLifetime)
	}
	service, err := NewSessionService(&sessionRepositoryStub{}, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	if _, err := service.Create(nil, 42); err == nil {
		t.Error("Create(nil context) error = nil, want error")
	}
	if _, err := service.Create(context.Background(), 0); !errors.Is(err, ErrInvalidSessionUserID) {
		t.Errorf("Create(invalid user id) error = %v, want %v", err, ErrInvalidSessionUserID)
	}
	if _, err := service.Lookup(context.Background(), strings.Repeat("x", maxSessionID+1)); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("Lookup(oversized id) error = %v, want %v", err, ErrInvalidSessionID)
	}
	if err := service.RevokeUser(context.Background(), 0); !errors.Is(err, ErrInvalidSessionUserID) {
		t.Errorf("RevokeUser(invalid user id) error = %v, want %v", err, ErrInvalidSessionUserID)
	}
}

type sessionRepositoryStub struct {
	created       Session
	session       Session
	findErr       error
	revokedID     string
	revokedAt     int64
	revokedUserID int64
}

func (r *sessionRepositoryStub) CreateSession(_ context.Context, session Session) error {
	r.created = session
	return nil
}

func (r *sessionRepositoryStub) FindSession(context.Context, string) (Session, error) {
	if r.findErr != nil {
		return Session{}, r.findErr
	}
	return r.session, nil
}

func (r *sessionRepositoryStub) RevokeSession(_ context.Context, id string, revokedAt int64) error {
	r.revokedID = id
	r.revokedAt = revokedAt
	return nil
}

func (r *sessionRepositoryStub) RevokeUserSessions(_ context.Context, userID int64) error {
	r.revokedUserID = userID
	return nil
}
