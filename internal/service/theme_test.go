package service

import (
	"context"
	"errors"
	"testing"
)

var (
	errThemeLookup   = errors.New("lookup failed")
	errThemeConflict = errors.New("conflict")
)

func TestThemeServiceUpdateTheme(t *testing.T) {
	tests := []struct {
		name           string
		currentTheme   string
		requestedTheme string
		findErr        error
		updateErr      error
		wantErr        error
		wantUpdates    int
	}{
		{name: "updates theme", currentTheme: "light", requestedTheme: "dark", wantUpdates: 1},
		{name: "same theme is idempotent", currentTheme: "dark", requestedTheme: "dark", wantUpdates: 0},
		{name: "invalid theme", currentTheme: "light", requestedTheme: "blue", wantErr: ErrInvalidTheme},
		{name: "repository lookup failure", currentTheme: "light", requestedTheme: "dark", findErr: errThemeLookup, wantErr: errThemeLookup},
		{name: "repository update failure", currentTheme: "light", requestedTheme: "dark", updateErr: errThemeConflict, wantErr: errThemeConflict, wantUpdates: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &themeRepositoryStub{
				user:      User{ID: 1, Theme: test.currentTheme, Version: 4},
				findErr:   test.findErr,
				updateErr: test.updateErr,
			}
			service, err := NewThemeService(repository)
			if err != nil {
				t.Fatalf("NewThemeService() error = %v, want nil", err)
			}
			err = service.UpdateTheme(context.Background(), 1, test.requestedTheme)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("UpdateTheme() error = %v, want errors.Is(_, %v)", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatalf("UpdateTheme() error = %v, want nil", err)
			}
			if repository.updateCalls != test.wantUpdates {
				t.Errorf("update calls = %d, want %d", repository.updateCalls, test.wantUpdates)
			}
		})
	}
}

func TestNewThemeServiceRejectsInvalidInputs(t *testing.T) {
	if _, err := NewThemeService(nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("NewThemeService(nil) error = %v, want %v", err, ErrNilUserRepository)
	}
	service, err := NewThemeService(&themeRepositoryStub{})
	if err != nil {
		t.Fatalf("NewThemeService() error = %v, want nil", err)
	}
	if err := service.UpdateTheme(nil, 1, "dark"); err == nil {
		t.Error("UpdateTheme(nil context) error = nil, want error")
	}
	if err := service.UpdateTheme(context.Background(), 0, "dark"); !errors.Is(err, ErrInvalidUserID) {
		t.Errorf("UpdateTheme(invalid id) error = %v, want %v", err, ErrInvalidUserID)
	}
}

type themeRepositoryStub struct {
	user        User
	findErr     error
	updateErr   error
	updateCalls int
}

func (r *themeRepositoryStub) CreateUser(context.Context, User) (User, error) { return User{}, nil }

func (r *themeRepositoryStub) FindUserByID(context.Context, int64) (User, error) {
	if r.findErr != nil {
		return User{}, r.findErr
	}
	return r.user, nil
}

func (r *themeRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	return User{}, nil
}

func (r *themeRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	return User{}, nil
}

func (r *themeRepositoryStub) UpdateUser(_ context.Context, user User, _ int64) (User, error) {
	r.updateCalls++
	if r.updateErr != nil {
		return User{}, r.updateErr
	}
	r.user = user
	return user, nil
}

func (r *themeRepositoryStub) DeleteUser(context.Context, int64, int64) error { return nil }

func (r *themeRepositoryStub) CreateSession(context.Context, Session) error { return nil }

func (r *themeRepositoryStub) FindSession(context.Context, string) (Session, error) {
	return Session{}, nil
}

func (r *themeRepositoryStub) RevokeSession(context.Context, string, int64) error { return nil }

func (r *themeRepositoryStub) RevokeUserSessions(context.Context, int64) error { return nil }
