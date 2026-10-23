package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

const (
	guestClaimCodeLength = 10
	guestClaimAlphabet   = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

var (
	// ErrGuestClaimCodeNotFound identifies a guest without an active code.
	ErrGuestClaimCodeNotFound = errors.New("service: guest claim code not found")
	// ErrNotGuest identifies an account that cannot own a guest transfer code.
	ErrNotGuest = errors.New("service: account is not guest")
	// ErrInvalidGuestClaimCode identifies malformed claim-code persistence.
	ErrInvalidGuestClaimCode = errors.New("service: invalid guest claim code")
	// ErrGuestClaimCodeGeneration identifies failure to create a transfer code.
	ErrGuestClaimCodeGeneration = errors.New("service: guest claim code generation failed")
	// ErrNilGuestClaimRepository identifies missing claim-code persistence.
	ErrNilGuestClaimRepository = errors.New("service: nil guest claim repository")
)

// GuestClaimService issues one active transfer code for a guest account.
type GuestClaimService struct {
	users UserRepository
	codes GuestClaimCodeRepository
	mu    sync.Mutex
}

// NewGuestClaimService constructs idempotent guest transfer-code issuance.
func NewGuestClaimService(users UserRepository, codes GuestClaimCodeRepository) (*GuestClaimService, error) {
	if users == nil {
		return nil, ErrNilUserRepository
	}
	if codes == nil {
		return nil, ErrNilGuestClaimRepository
	}
	return &GuestClaimService{users: users, codes: codes}, nil
}

// Issue returns the existing active code or creates one when none exists.
// Calls for the same service are serialized so repeated requests cannot create
// replacement codes between the lookup and insert.
func (s *GuestClaimService) Issue(ctx context.Context, userID int64) (GuestClaimCode, error) {
	if s == nil || s.users == nil || s.codes == nil {
		return GuestClaimCode{}, ErrNilGuestClaimRepository
	}
	if ctx == nil {
		return GuestClaimCode{}, errors.New("service: nil guest claim context")
	}
	if userID <= 0 {
		return GuestClaimCode{}, ErrInvalidUserID
	}
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return GuestClaimCode{}, fmt.Errorf("find guest for claim code: %w", err)
	}
	if user.ID != userID || !user.IsGuest {
		return GuestClaimCode{}, ErrNotGuest
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	active, err := s.codes.FindActiveGuestClaimCode(ctx, userID)
	if err == nil {
		if err := validateGuestClaimCode(active, userID); err != nil {
			return GuestClaimCode{}, err
		}
		return active, nil
	}
	if !errors.Is(err, ErrGuestClaimCodeNotFound) {
		return GuestClaimCode{}, fmt.Errorf("find active guest claim code: %w", err)
	}

	code, err := newGuestClaimCode()
	if err != nil {
		return GuestClaimCode{}, err
	}
	created, err := s.codes.CreateGuestClaimCode(ctx, GuestClaimCode{
		UserID:    userID,
		Code:      code,
		CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		// A second application instance may win the unique active-code insert.
		// Re-read before returning an error so issuance stays idempotent there too.
		active, lookupErr := s.codes.FindActiveGuestClaimCode(ctx, userID)
		if lookupErr == nil {
			if validateErr := validateGuestClaimCode(active, userID); validateErr != nil {
				return GuestClaimCode{}, validateErr
			}
			return active, nil
		}
		return GuestClaimCode{}, fmt.Errorf("create guest claim code: %w", err)
	}
	if err := validateGuestClaimCode(created, userID); err != nil {
		return GuestClaimCode{}, err
	}
	return created, nil
}

func newGuestClaimCode() (string, error) {
	code := make([]byte, guestClaimCodeLength)
	max := big.NewInt(int64(len(guestClaimAlphabet)))
	for index := range code {
		value, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("%w: random source unavailable", ErrGuestClaimCodeGeneration)
		}
		code[index] = guestClaimAlphabet[value.Int64()]
	}
	return string(code), nil
}

func validateGuestClaimCode(code GuestClaimCode, userID int64) error {
	if code.UserID != userID || len(code.Code) != guestClaimCodeLength || code.CreatedAt <= 0 || code.RevokedAt != nil {
		return ErrInvalidGuestClaimCode
	}
	for index := range code.Code {
		if !containsGuestClaimCharacter(code.Code[index]) {
			return ErrInvalidGuestClaimCode
		}
	}
	return nil
}

func containsGuestClaimCharacter(target byte) bool {
	for index := 0; index < len(guestClaimAlphabet); index++ {
		if guestClaimAlphabet[index] == target {
			return true
		}
	}
	return false
}
