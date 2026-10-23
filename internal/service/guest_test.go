package service

import (
	"context"
	"errors"
	"testing"
)

func TestGuestServiceCreate(t *testing.T) {
	repository := &guestUserRepositoryStub{}
	service, err := NewGuestService(repository)
	if err != nil {
		t.Fatalf("NewGuestService() error = %v, want nil", err)
	}

	guest, err := service.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if guest.ID != 42 || !guest.IsGuest {
		t.Fatalf("created guest = %+v, want persisted guest", guest)
	}
	if guest.Email != nil || guest.EmailVerifiedAt != nil {
		t.Fatalf("created guest contains credentials: %+v", guest)
	}
	if repository.createCalls != 1 {
		t.Fatalf("CreateUser() calls = %d, want 1", repository.createCalls)
	}
	if !repository.created.IsGuest || repository.created.Email != nil {
		t.Fatalf("persisted guest = %+v, want credential-free guest", repository.created)
	}
	if repository.created.Theme != defaultUserTheme || repository.created.Locale != defaultUserLocale {
		t.Fatalf("persisted defaults = %q/%q, want %q/%q", repository.created.Theme, repository.created.Locale, defaultUserTheme, defaultUserLocale)
	}
}

func TestGuestServiceCreateRejectsInvalidInputs(t *testing.T) {
	createErr := errors.New("create failed")
	tests := []struct {
		name        string
		repository  UserRepository
		ctx         context.Context
		wantErr     error
		wantFailure bool
	}{
		{name: "nil repository", repository: nil, ctx: context.Background(), wantErr: ErrNilUserRepository},
		{name: "nil context", repository: &guestUserRepositoryStub{}, ctx: nil, wantFailure: true},
		{name: "create failure", repository: &guestUserRepositoryStub{createErr: createErr}, ctx: context.Background(), wantErr: createErr},
		{name: "invalid persisted guest", repository: &guestUserRepositoryStub{created: User{ID: 42}}, ctx: context.Background(), wantErr: ErrInvalidCreatedGuest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewGuestService(test.repository)
			if test.repository == nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("NewGuestService() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewGuestService() error = %v, want nil", err)
			}
			_, err = service.Create(test.ctx)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Create() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if test.wantErr == nil && test.wantFailure && err == nil {
				t.Fatal("Create() error = nil, want error")
			}
			if test.wantErr == nil && !test.wantFailure && err != nil {
				t.Fatalf("Create() error = %v, want nil", err)
			}
		})
	}
}

type guestUserRepositoryStub struct {
	created     User
	createErr   error
	createCalls int
}

func (r *guestUserRepositoryStub) CreateUser(_ context.Context, user User) (User, error) {
	r.createCalls++
	if r.createErr != nil {
		return User{}, r.createErr
	}
	if r.created.ID == 0 {
		r.created = user
		r.created.ID = 42
	}
	return r.created, nil
}

func (r *guestUserRepositoryStub) FindUserByID(context.Context, int64) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestUserRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestUserRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestUserRepositoryStub) UpdateUser(context.Context, User, int64) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestUserRepositoryStub) DeleteUser(context.Context, int64, int64) error {
	return ErrUserNotFound
}
