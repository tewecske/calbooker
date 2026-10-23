package service

import (
	"context"
	"errors"
	"testing"
)

func TestLocaleSettingsServiceUpdate(t *testing.T) {
	users := &settingsUserRepositoryStub{user: User{ID: 7, Locale: "en", Version: 1}}
	service, err := NewLocaleSettingsService(users)
	if err != nil {
		t.Fatalf("NewLocaleSettingsService() error = %v", err)
	}
	updated, err := service.Update(context.Background(), 7, "  HU ")
	if err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}
	if updated.Locale != "hu" {
		t.Fatalf("updated locale = %q, want %q", updated.Locale, "hu")
	}
	if users.updateCalls != 1 {
		t.Fatalf("UpdateUser() calls = %d, want 1", users.updateCalls)
	}
}

func TestLocaleSettingsServiceRejectsUnsupportedLanguage(t *testing.T) {
	users := &settingsUserRepositoryStub{user: User{ID: 7, Locale: "en", Version: 1}}
	service, err := NewLocaleSettingsService(users)
	if err != nil {
		t.Fatalf("NewLocaleSettingsService() error = %v", err)
	}
	for _, code := range []string{"", "de", "fr", "../en", "en-US"} {
		if _, err := service.Update(context.Background(), 7, code); !errors.Is(err, ErrInvalidLocale) {
			t.Fatalf("Update(%q) error = %v, want ErrInvalidLocale", code, err)
		}
	}
	if users.updateCalls != 0 {
		t.Fatalf("UpdateUser() calls = %d, want 0", users.updateCalls)
	}
}

func TestLocaleSettingsServiceUnchangedSkipsWrite(t *testing.T) {
	users := &settingsUserRepositoryStub{user: User{ID: 7, Locale: "hu", Version: 1}}
	service, err := NewLocaleSettingsService(users)
	if err != nil {
		t.Fatalf("NewLocaleSettingsService() error = %v", err)
	}
	if _, err := service.Update(context.Background(), 7, "hu"); err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}
	if users.updateCalls != 0 {
		t.Fatalf("UpdateUser() calls = %d, want 0 for unchanged locale", users.updateCalls)
	}
}

type settingsUserRepositoryStub struct {
	user        User
	lookupUser  User
	findErr     error
	updateErr   error
	updateCalls int
}

func (r *settingsUserRepositoryStub) CreateUser(context.Context, User) (User, error) {
	return User{}, nil
}

func (r *settingsUserRepositoryStub) FindUserByID(context.Context, int64) (User, error) {
	if r.findErr != nil {
		return User{}, r.findErr
	}
	return r.user, nil
}

func (r *settingsUserRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *settingsUserRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	if r.lookupUser.ID <= 0 {
		return User{}, ErrUserNotFound
	}
	return r.lookupUser, nil
}

func (r *settingsUserRepositoryStub) UpdateUser(_ context.Context, user User, _ int64) (User, error) {
	r.updateCalls++
	if r.updateErr != nil {
		return User{}, r.updateErr
	}
	return user, nil
}

func (r *settingsUserRepositoryStub) DeleteUser(context.Context, int64, int64) error {
	return ErrUserNotFound
}

func strPtr(value string) *string { return &value }
