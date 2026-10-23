//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

func TestPostgresRepositoryContractsExerciseGuestServices(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	users, err := NewUserRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	sessionsRepository, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := service.NewSessionService(sessionsRepository, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Real guest account, claim-code, session, single sign-on upgrade, and
	// revocation stores.
	guestSession, err := service.NewGuestSessionService(users, sessions)
	if err != nil {
		t.Fatal(err)
	}
	guestResult, err := guestSession.CreateWithTheme(context.Background(), "dark")
	if err != nil {
		t.Fatalf("guest CreateWithTheme() error = %v", err)
	}
	claims, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	claimService, err := service.NewGuestClaimService(users, claims)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := claimService.Issue(context.Background(), guestResult.User.ID)
	if err != nil {
		t.Fatalf("guest claim Issue() error = %v", err)
	}
	redemption, err := service.NewGuestRedemptionService(users, claims, sessions)
	if err != nil {
		t.Fatal(err)
	}
	secondDevice, err := redemption.Redeem(context.Background(), claim.Code)
	if err != nil {
		t.Fatalf("guest claim Redeem() error = %v", err)
	}
	if secondDevice.User.ID != guestResult.User.ID || secondDevice.User.Theme != "dark" {
		t.Fatal("guest redemption did not preserve account identity and theme")
	}

	guest, err := users.FindUserByID(context.Background(), guestResult.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	email := "upgraded@example.test"
	upgradedGuest := guest
	upgradedGuest.Kind = service.UserKindAccount
	upgradedGuest.Email = &email
	upgraded, err := users.UpgradeGuestWithIdentity(context.Background(), upgradedGuest, guest.Version,
		service.OAuthIdentity{UserID: guest.ID, Provider: service.SSOProviderName, Subject: "issuer-subject", Email: email, CreatedAt: 200}, 200)
	if err != nil {
		t.Fatalf("UpgradeGuestWithIdentity() error = %v", err)
	}
	if upgraded.ID != guest.ID || upgraded.IsGuest() || upgraded.Email == nil || *upgraded.Email != email {
		t.Fatalf("upgraded account = %+v, want the same account with the issuer email", upgraded)
	}
	identities, err := NewOAuthIdentityRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	if identity, err := identities.FindOAuthIdentity(context.Background(), service.SSOProviderName, "issuer-subject"); err != nil || identity.UserID != guest.ID {
		t.Fatalf("linked identity = %+v, %v; want the upgraded account", identity, err)
	}
	if _, err := claims.FindGuestClaimCodeByCode(context.Background(), claim.Code); !errors.Is(err, service.ErrGuestClaimCodeNotFound) {
		t.Fatalf("upgraded guest claim lookup error = %v, want not found", err)
	}
	if err := sessions.RevokeUser(context.Background(), guest.ID); err != nil {
		t.Fatal(err)
	}
	for name, sessionID := range map[string]string{"original guest": guestResult.Session.ID, "transfer-code device": secondDevice.Session.ID} {
		if _, err := sessions.Lookup(context.Background(), sessionID); !errors.Is(err, service.ErrSessionRevoked) {
			t.Fatalf("%s session after upgrade error = %v, want revoked", name, err)
		}
	}
}

func TestPostgresRepositoryContractsDoNotExposeBearerValuesInErrors(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	sessions, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTokenUser(t, harness.DB, "secret-contract@example.test")
	secretSessionID := "session-secret-contract-value"
	if err := sessions.CreateSession(context.Background(), service.Session{ID: secretSessionID, UserID: userID, CreatedAt: 100, ExpiresAt: 200}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.RevokeSession(context.Background(), "unknown-session-secret", 100); !errors.Is(err, service.ErrSessionNotFound) {
		t.Fatal(err)
	} else if strings.Contains(err.Error(), "unknown-session-secret") {
		t.Fatal("session error exposed bearer value")
	}

	claims, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	secretCode := "SECRET2345"
	if _, err := claims.FindGuestClaimCodeByCode(context.Background(), secretCode); !errors.Is(err, service.ErrGuestClaimCodeNotFound) {
		t.Fatal(err)
	} else if strings.Contains(err.Error(), secretCode) {
		t.Fatal("claim error exposed bearer value")
	}
}

func insertTokenUser(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, email string) int64 {
	t.Helper()
	var userID int64
	if err := db.QueryRowContext(context.Background(), "INSERT INTO users (email, theme, locale, created_at) VALUES ($1, 'light', 'en', 100) RETURNING id", email).Scan(&userID); err != nil {
		t.Fatalf("insert token user: %v", err)
	}
	return userID
}
