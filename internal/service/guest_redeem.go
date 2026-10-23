package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GuestRedemptionService signs another device into a guest account with a
// valid transfer code. Codes remain active after use and can create multiple
// sessions until explicitly revoked by a later guest workflow.
type GuestRedemptionService struct {
	users    UserRepository
	codes    GuestClaimCodeRedeemer
	sessions *SessionService
}

// NewGuestRedemptionService constructs guest transfer-code redemption.
func NewGuestRedemptionService(
	users UserRepository,
	codes GuestClaimCodeRedeemer,
	sessions *SessionService,
) (*GuestRedemptionService, error) {
	if users == nil {
		return nil, ErrNilUserRepository
	}
	if codes == nil {
		return nil, ErrNilGuestClaimRepository
	}
	if sessions == nil {
		return nil, ErrNilGuestSessionDependency
	}
	return &GuestRedemptionService{users: users, codes: codes, sessions: sessions}, nil
}

// Redeem validates one transfer code and creates a new session for its guest.
// Invalid, unknown, expired-by-revocation, and mistyped codes share one
// sentinel so callers cannot distinguish code state.
func (s *GuestRedemptionService) Redeem(ctx context.Context, rawCode string) (GuestSessionResult, error) {
	if s == nil || s.users == nil || s.codes == nil || s.sessions == nil {
		return GuestSessionResult{}, ErrNilGuestSessionDependency
	}
	if ctx == nil {
		return GuestSessionResult{}, errors.New("service: nil guest redemption context")
	}
	code, err := normalizeGuestClaimCode(rawCode)
	if err != nil {
		return GuestSessionResult{}, err
	}
	claim, err := s.codes.FindGuestClaimCodeByCode(ctx, code)
	if err != nil {
		if errors.Is(err, ErrGuestClaimCodeNotFound) {
			return GuestSessionResult{}, ErrInvalidGuestClaimCode
		}
		return GuestSessionResult{}, fmt.Errorf("find guest claim code: %w", err)
	}
	if claim.Code != code || claim.UserID <= 0 || claim.RevokedAt != nil {
		return GuestSessionResult{}, ErrInvalidGuestClaimCode
	}
	user, err := s.users.FindUserByID(ctx, claim.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return GuestSessionResult{}, ErrInvalidGuestClaimCode
		}
		return GuestSessionResult{}, fmt.Errorf("find guest for claim redemption: %w", err)
	}
	if user.ID != claim.UserID || !user.IsGuest() || user.Email != nil {
		return GuestSessionResult{}, ErrInvalidGuestClaimCode
	}

	session, err := s.sessions.Create(ctx, user.ID)
	if err != nil {
		return GuestSessionResult{}, fmt.Errorf("create redeemed guest session: %w", err)
	}
	if err := s.codes.MarkGuestClaimCodeUsed(ctx, code, time.Now().Unix()); err != nil {
		if revokeErr := s.sessions.Revoke(ctx, session.ID); revokeErr != nil {
			return GuestSessionResult{}, errors.Join(
				fmt.Errorf("mark guest claim code used: %w", err),
				fmt.Errorf("revoke redeemed guest session: %w", revokeErr),
			)
		}
		return GuestSessionResult{}, fmt.Errorf("mark guest claim code used: %w", err)
	}
	return GuestSessionResult{User: user, Session: session}, nil
}

func normalizeGuestClaimCode(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if len(code) != guestClaimCodeLength {
		return "", ErrInvalidGuestClaimCode
	}
	for index := range code {
		if !containsGuestClaimCharacter(code[index]) {
			return "", ErrInvalidGuestClaimCode
		}
	}
	return code, nil
}
