package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGuestClaimServiceIssueIsIdempotent(t *testing.T) {
	users := &guestClaimUserRepositoryStub{user: User{ID: 42, IsGuest: true}}
	codes := &guestClaimCodeRepositoryStub{}
	service, err := NewGuestClaimService(users, codes)
	if err != nil {
		t.Fatalf("NewGuestClaimService() error = %v, want nil", err)
	}

	first, err := service.Issue(context.Background(), 42)
	if err != nil {
		t.Fatalf("Issue() first error = %v, want nil", err)
	}
	second, err := service.Issue(context.Background(), 42)
	if err != nil {
		t.Fatalf("Issue() second error = %v, want nil", err)
	}
	if first.Code == "" || first.Code != second.Code {
		t.Fatalf("issued codes = %q/%q, want same non-empty code", first.Code, second.Code)
	}
	if len(first.Code) != guestClaimCodeLength || strings.ContainsAny(first.Code, "ILO01") {
		t.Fatalf("issued code = %q, want transcribable alphabet", first.Code)
	}
	if codes.createCalls != 1 {
		t.Fatalf("CreateGuestClaimCode() calls = %d, want 1", codes.createCalls)
	}
}

func TestGuestClaimServiceIssueRejectsInvalidInputs(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	tests := []struct {
		name    string
		user    User
		findErr error
		userID  int64
		wantErr error
	}{
		{name: "invalid user id", userID: 0, wantErr: ErrInvalidUserID},
		{name: "normal account", user: User{ID: 42}, userID: 42, wantErr: ErrNotGuest},
		{name: "user lookup failure", findErr: lookupErr, userID: 42, wantErr: lookupErr},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users := &guestClaimUserRepositoryStub{user: test.user, findErr: test.findErr}
			service, err := NewGuestClaimService(users, &guestClaimCodeRepositoryStub{})
			if err != nil {
				t.Fatalf("NewGuestClaimService() error = %v, want nil", err)
			}
			_, err = service.Issue(context.Background(), test.userID)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Issue() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestGuestClaimServiceIssuePropagatesCodeFailures(t *testing.T) {
	findErr := errors.New("find code failed")
	users := &guestClaimUserRepositoryStub{user: User{ID: 42, IsGuest: true}}
	codes := &guestClaimCodeRepositoryStub{findErr: findErr}
	service, err := NewGuestClaimService(users, codes)
	if err != nil {
		t.Fatalf("NewGuestClaimService() error = %v, want nil", err)
	}
	if _, err := service.Issue(context.Background(), 42); !errors.Is(err, findErr) {
		t.Fatalf("Issue() error = %v, want errors.Is(_, %v)", err, findErr)
	}

	createErr := errors.New("create code failed")
	codes = &guestClaimCodeRepositoryStub{createErr: createErr}
	service, err = NewGuestClaimService(users, codes)
	if err != nil {
		t.Fatalf("NewGuestClaimService() second error = %v, want nil", err)
	}
	if _, err := service.Issue(context.Background(), 42); !errors.Is(err, createErr) {
		t.Fatalf("Issue() create error = %v, want errors.Is(_, %v)", err, createErr)
	}
}

type guestClaimUserRepositoryStub struct {
	user    User
	findErr error
}

func (r *guestClaimUserRepositoryStub) CreateUser(context.Context, User) (User, error) {
	return User{}, nil
}

func (r *guestClaimUserRepositoryStub) FindUserByID(context.Context, int64) (User, error) {
	if r.findErr != nil {
		return User{}, r.findErr
	}
	return r.user, nil
}

func (r *guestClaimUserRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestClaimUserRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestClaimUserRepositoryStub) UpdateUser(context.Context, User, int64) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestClaimUserRepositoryStub) DeleteUser(context.Context, int64, int64) error {
	return ErrUserNotFound
}

type guestClaimCodeRepositoryStub struct {
	active      GuestClaimCode
	findErr     error
	createErr   error
	createCalls int
}

func (r *guestClaimCodeRepositoryStub) FindActiveGuestClaimCode(context.Context, int64) (GuestClaimCode, error) {
	if r.findErr != nil {
		return GuestClaimCode{}, r.findErr
	}
	if r.active.Code == "" {
		return GuestClaimCode{}, ErrGuestClaimCodeNotFound
	}
	return r.active, nil
}

func (r *guestClaimCodeRepositoryStub) CreateGuestClaimCode(_ context.Context, code GuestClaimCode) (GuestClaimCode, error) {
	r.createCalls++
	if r.createErr != nil {
		return GuestClaimCode{}, r.createErr
	}
	r.active = code
	return code, nil
}
