package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGuestRedemptionServiceRedeemCreatesMultipleSessions(t *testing.T) {
	users := &guestRedemptionUserRepositoryStub{user: User{ID: 42, IsGuest: true}}
	codes := &guestRedemptionCodeRepositoryStub{claim: GuestClaimCode{
		UserID:    42,
		Code:      "ABCDEFGH23",
		CreatedAt: 1,
	}}
	sessionRepository := &guestRedemptionSessionRepositoryStub{}
	sessions, err := NewSessionService(sessionRepository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	service, err := NewGuestRedemptionService(users, codes, sessions)
	if err != nil {
		t.Fatalf("NewGuestRedemptionService() error = %v, want nil", err)
	}

	first, err := service.Redeem(context.Background(), " abcdefgh23 ")
	if err != nil {
		t.Fatalf("Redeem() first error = %v, want nil", err)
	}
	second, err := service.Redeem(context.Background(), "ABCDEFGH23")
	if err != nil {
		t.Fatalf("Redeem() second error = %v, want nil", err)
	}
	if first.User.ID != 42 || first.Session.UserID != 42 || second.Session.UserID != 42 {
		t.Fatalf("redeemed IDs = %+v/%+v, want guest 42", first, second)
	}
	if first.Session.ID == second.Session.ID {
		t.Fatal("repeated redemption reused session credential")
	}
	if codes.markCalls != 2 || sessionRepository.createCalls != 2 {
		t.Fatalf("mark/session calls = %d/%d, want 2/2", codes.markCalls, sessionRepository.createCalls)
	}
}

func TestGuestRedemptionServiceRejectsUniformlyInvalidCodes(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		claim   GuestClaimCode
		findErr error
	}{
		{name: "malformed", code: "short"},
		{name: "unknown", code: "ABCDEFGH23", findErr: ErrGuestClaimCodeNotFound},
		{name: "revoked", code: "ABCDEFGH23", claim: GuestClaimCode{UserID: 42, Code: "ABCDEFGH23", CreatedAt: 1, RevokedAt: int64Pointer(2)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users := &guestRedemptionUserRepositoryStub{user: User{ID: 42, IsGuest: true}}
			codes := &guestRedemptionCodeRepositoryStub{claim: test.claim, findErr: test.findErr}
			sessions, err := NewSessionService(&guestRedemptionSessionRepositoryStub{}, time.Hour)
			if err != nil {
				t.Fatalf("NewSessionService() error = %v, want nil", err)
			}
			service, err := NewGuestRedemptionService(users, codes, sessions)
			if err != nil {
				t.Fatalf("NewGuestRedemptionService() error = %v, want nil", err)
			}
			_, err = service.Redeem(context.Background(), test.code)
			if !errors.Is(err, ErrInvalidGuestClaimCode) {
				t.Fatalf("Redeem() error = %v, want %v", err, ErrInvalidGuestClaimCode)
			}
		})
	}
}

func TestGuestRedemptionServicePropagatesOperationalFailures(t *testing.T) {
	backendErr := errors.New("backend failed")
	users := &guestRedemptionUserRepositoryStub{user: User{ID: 42, IsGuest: true}}
	codes := &guestRedemptionCodeRepositoryStub{
		claim:   GuestClaimCode{UserID: 42, Code: "ABCDEFGH23", CreatedAt: 1},
		markErr: backendErr,
	}
	sessionRepository := &guestRedemptionSessionRepositoryStub{}
	sessions, err := NewSessionService(sessionRepository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	service, err := NewGuestRedemptionService(users, codes, sessions)
	if err != nil {
		t.Fatalf("NewGuestRedemptionService() error = %v, want nil", err)
	}
	if _, err := service.Redeem(context.Background(), "ABCDEFGH23"); !errors.Is(err, backendErr) {
		t.Fatalf("Redeem() error = %v, want errors.Is(_, %v)", err, backendErr)
	}
	if sessionRepository.revokeCalls != 1 {
		t.Fatalf("revoke calls = %d, want 1", sessionRepository.revokeCalls)
	}
}

func TestGuestRedemptionServiceDoesNotMarkCodeWhenSessionCreationFails(t *testing.T) {
	users := &guestRedemptionUserRepositoryStub{user: User{ID: 42, IsGuest: true}}
	codes := &guestRedemptionCodeRepositoryStub{claim: GuestClaimCode{
		UserID:    42,
		Code:      "ABCDEFGH23",
		CreatedAt: 1,
	}}
	sessionErr := errors.New("session backend failed")
	sessions, err := NewSessionService(&guestRedemptionSessionRepositoryStub{createErr: sessionErr}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewGuestRedemptionService(users, codes, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Redeem(context.Background(), "ABCDEFGH23"); !errors.Is(err, sessionErr) {
		t.Fatalf("Redeem() error = %v, want %v", err, sessionErr)
	}
	if codes.markCalls != 0 {
		t.Fatalf("mark calls = %d, want 0", codes.markCalls)
	}
}

type guestRedemptionUserRepositoryStub struct {
	user User
}

func (r *guestRedemptionUserRepositoryStub) CreateUser(context.Context, User) (User, error) {
	return User{}, nil
}

func (r *guestRedemptionUserRepositoryStub) FindUserByID(context.Context, int64) (User, error) {
	if r.user.ID == 0 {
		return User{}, ErrUserNotFound
	}
	return r.user, nil
}

func (r *guestRedemptionUserRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestRedemptionUserRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestRedemptionUserRepositoryStub) UpdateUser(context.Context, User, int64) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *guestRedemptionUserRepositoryStub) DeleteUser(context.Context, int64, int64) error {
	return ErrUserNotFound
}

type guestRedemptionCodeRepositoryStub struct {
	claim     GuestClaimCode
	findErr   error
	markErr   error
	markCalls int
}

func (r *guestRedemptionCodeRepositoryStub) FindGuestClaimCodeByCode(context.Context, string) (GuestClaimCode, error) {
	if r.findErr != nil {
		return GuestClaimCode{}, r.findErr
	}
	if r.claim.Code == "" {
		return GuestClaimCode{}, ErrGuestClaimCodeNotFound
	}
	return r.claim, nil
}

func (r *guestRedemptionCodeRepositoryStub) MarkGuestClaimCodeUsed(_ context.Context, _ string, usedAt int64) error {
	if r.markErr != nil {
		return r.markErr
	}
	r.markCalls++
	r.claim.LastUsedAt = &usedAt
	return nil
}

type guestRedemptionSessionRepositoryStub struct {
	createCalls int
	createErr   error
	revokeCalls int
}

func (r *guestRedemptionSessionRepositoryStub) CreateSession(context.Context, Session) error {
	r.createCalls++
	return r.createErr
}

func (r *guestRedemptionSessionRepositoryStub) FindSession(context.Context, string) (Session, error) {
	return Session{}, ErrSessionNotFound
}

func (r *guestRedemptionSessionRepositoryStub) RevokeSession(context.Context, string, int64) error {
	r.revokeCalls++
	return nil
}

func (r *guestRedemptionSessionRepositoryStub) RevokeUserSessions(context.Context, int64) error {
	return nil
}
