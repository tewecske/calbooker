package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGuestSessionServiceCreate(t *testing.T) {
	users := &guestUserRepositoryStub{}
	sessionRepository := &guestSessionRepositoryStub{}
	sessions, err := NewSessionService(sessionRepository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	service, err := NewGuestSessionService(users, sessions)
	if err != nil {
		t.Fatalf("NewGuestSessionService() error = %v, want nil", err)
	}

	result, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if result.User.ID != result.Session.UserID || result.User.ID != 42 {
		t.Fatalf("guest/session IDs = %d/%d, want persisted guest ID", result.User.ID, result.Session.UserID)
	}
	if !result.User.IsGuest || result.User.Email != nil || result.User.EmailVerifiedAt != nil {
		t.Fatalf("guest account = %+v, want guest without credentials", result.User)
	}
	if result.User.Username == nil || !strings.HasPrefix(*result.User.Username, "guest-") {
		t.Fatalf("guest username = %v, want random guest- prefix", result.User.Username)
	}
	if sessionRepository.created.UserID != result.User.ID {
		t.Fatalf("persisted session user id = %d, want %d", sessionRepository.created.UserID, result.User.ID)
	}
}

func TestGuestSessionServiceCreateWithTheme(t *testing.T) {
	users := &guestUserRepositoryStub{}
	sessionRepository := &guestSessionRepositoryStub{}
	sessions, err := NewSessionService(sessionRepository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	service, err := NewGuestSessionService(users, sessions)
	if err != nil {
		t.Fatalf("NewGuestSessionService() error = %v, want nil", err)
	}

	result, err := service.CreateWithTheme(context.Background(), " DARK ")
	if err != nil {
		t.Fatalf("CreateWithTheme() error = %v, want nil", err)
	}
	if result.User.Theme != "dark" || users.created.Theme != "dark" {
		t.Fatalf("guest theme = %q/%q, want dark", result.User.Theme, users.created.Theme)
	}
}

func TestGuestSessionServiceCreateWithThemeRejectsInvalidTheme(t *testing.T) {
	users := &guestUserRepositoryStub{}
	sessions, err := NewSessionService(&guestSessionRepositoryStub{}, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	service, err := NewGuestSessionService(users, sessions)
	if err != nil {
		t.Fatalf("NewGuestSessionService() error = %v, want nil", err)
	}

	if _, err := service.CreateWithTheme(context.Background(), "blue"); !errors.Is(err, ErrInvalidTheme) {
		t.Fatalf("CreateWithTheme() error = %v, want %v", err, ErrInvalidTheme)
	}
	if users.createCalls != 0 {
		t.Fatalf("CreateUser() calls = %d, want 0 for invalid theme", users.createCalls)
	}
}

func TestNewGuestSessionServiceRejectsInvalidInputs(t *testing.T) {
	sessions, err := NewSessionService(&guestSessionRepositoryStub{}, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	if _, err := NewGuestSessionService(nil, sessions); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("NewGuestSessionService(nil users) error = %v, want %v", err, ErrNilUserRepository)
	}
	if _, err := NewGuestSessionService(&guestUserRepositoryStub{}, nil); !errors.Is(err, ErrNilGuestSessionDependency) {
		t.Fatalf("NewGuestSessionService(nil sessions) error = %v, want %v", err, ErrNilGuestSessionDependency)
	}
}

func TestGuestSessionServicePropagatesFailures(t *testing.T) {
	createErr := errors.New("create guest failed")
	users := &guestUserRepositoryStub{createErr: createErr}
	sessions, err := NewSessionService(&guestSessionRepositoryStub{}, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	service, err := NewGuestSessionService(users, sessions)
	if err != nil {
		t.Fatalf("NewGuestSessionService() error = %v, want nil", err)
	}
	if _, err := service.Create(context.Background()); !errors.Is(err, createErr) {
		t.Fatalf("Create() error = %v, want errors.Is(_, %v)", err, createErr)
	}

	sessionErr := errors.New("create session failed")
	sessions, err = NewSessionService(&guestSessionRepositoryStub{createErr: sessionErr}, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() second error = %v, want nil", err)
	}
	service, err = NewGuestSessionService(&guestUserRepositoryStub{}, sessions)
	if err != nil {
		t.Fatalf("NewGuestSessionService() second error = %v, want nil", err)
	}
	if _, err := service.Create(context.Background()); !errors.Is(err, sessionErr) {
		t.Fatalf("Create() session error = %v, want errors.Is(_, %v)", err, sessionErr)
	}
}

type guestSessionRepositoryStub struct {
	created   Session
	createErr error
}

func (r *guestSessionRepositoryStub) CreateSession(_ context.Context, session Session) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = session
	return nil
}

func (r *guestSessionRepositoryStub) FindSession(context.Context, string) (Session, error) {
	return Session{}, ErrSessionNotFound
}

func (r *guestSessionRepositoryStub) RevokeSession(context.Context, string, int64) error {
	return nil
}

func (r *guestSessionRepositoryStub) RevokeUserSessions(context.Context, int64) error {
	return nil
}
