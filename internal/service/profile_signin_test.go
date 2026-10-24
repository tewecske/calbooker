package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Child profiles never sign in: no guest transfer code is issued for one,
// none redeems into a session, and no guest upgrade or invite join accepts
// one. (The session store and the session authenticator refuse them too;
// see the postgres and HTTP tests.)

func TestProfileGetsNoGuestTransferCode(t *testing.T) {
	users := &guestClaimUserRepositoryStub{user: User{ID: 42, Kind: UserKindProfile}}
	service, err := NewGuestClaimService(users, &guestClaimCodeRepositoryStub{})
	if err != nil {
		t.Fatalf("NewGuestClaimService() error = %v", err)
	}
	if _, err := service.Issue(context.Background(), 42); !errors.Is(err, ErrNotGuest) {
		t.Fatalf("Issue(profile) error = %v, want ErrNotGuest", err)
	}
}

func TestProfileTransferCodeNeverRedeemsIntoASession(t *testing.T) {
	users := &guestRedemptionUserRepositoryStub{user: User{ID: 42, Kind: UserKindProfile}}
	codes := &guestRedemptionCodeRepositoryStub{claim: GuestClaimCode{UserID: 42, Code: "ABCDEFGH23", CreatedAt: 1}}
	sessionRepository := &guestRedemptionSessionRepositoryStub{}
	sessions, err := NewSessionService(sessionRepository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v", err)
	}
	service, err := NewGuestRedemptionService(users, codes, sessions)
	if err != nil {
		t.Fatalf("NewGuestRedemptionService() error = %v", err)
	}
	if _, err := service.Redeem(context.Background(), "ABCDEFGH23"); !errors.Is(err, ErrInvalidGuestClaimCode) {
		t.Fatalf("Redeem(profile) error = %v, want ErrInvalidGuestClaimCode", err)
	}
	if sessionRepository.createCalls != 0 {
		t.Fatal("a profile got a session")
	}
}

func TestProfileIsNeverUpgraded(t *testing.T) {
	f := newSSOClientFixture(t)
	f.users.byID = User{ID: 5, Kind: UserKindProfile}
	if _, _, err := f.service.StartUpgrade(context.Background(), 5); !errors.Is(err, ErrGuestUpgradeRequiresGuest) {
		t.Fatalf("StartUpgrade(profile) error = %v, want ErrGuestUpgradeRequiresGuest", err)
	}

	f = upgradeFixture(t)
	f.users.byID.Kind = UserKindProfile
	if _, err := f.service.Callback(context.Background(), 5, "code", testSSOState); !errors.Is(err, ErrGuestUpgradeRequiresGuest) {
		t.Fatalf("Callback(profile) error = %v, want ErrGuestUpgradeRequiresGuest", err)
	}
	if f.sessions.created.UserID != 0 || f.upgrades.calls != 0 {
		t.Fatal("a profile was upgraded or got a session")
	}
}
