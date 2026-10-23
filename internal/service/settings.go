package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
)

var (
	// ErrInvalidSettings identifies missing settings wiring.
	ErrInvalidSettings = errors.New("service: invalid settings")
	// ErrInvalidLocale identifies a language code outside the supported catalog.
	ErrInvalidLocale = errors.New("service: invalid locale")
)

// LocaleSettingsService persists an account language preference. It returns the
// canonical route so callers can navigate to the equivalent localized page.
type LocaleSettingsService struct {
	users UserRepository
}

// NewLocaleSettingsService constructs locale settings with its account port.
func NewLocaleSettingsService(users UserRepository) (*LocaleSettingsService, error) {
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &LocaleSettingsService{users: users}, nil
}

// Update validates and persists the account language preference. Unsupported
// languages are rejected before any write.
func (s *LocaleSettingsService) Update(ctx context.Context, userID int64, rawLocale string) (User, error) {
	if s == nil || s.users == nil {
		return User{}, ErrInvalidSettings
	}
	if ctx == nil {
		return User{}, errors.New("service: nil locale settings context")
	}
	if userID <= 0 {
		return User{}, ErrInvalidUserID
	}
	code := strings.ToLower(strings.TrimSpace(rawLocale))
	if !validLocale(code) {
		return User{}, ErrInvalidLocale
	}
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return User{}, fmt.Errorf("find locale settings user: %w", err)
	}
	if user.ID != userID {
		return User{}, ErrUserNotFound
	}
	if user.Locale == code {
		return user, nil
	}
	user.Locale = code
	updated, err := s.users.UpdateUser(ctx, user, user.Version)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrRecordNotFound
		}
		if errors.Is(err, ErrOptimisticLockConflict) {
			return User{}, ErrOptimisticLockConflict
		}
		return User{}, fmt.Errorf("update locale settings: %w", err)
	}
	return updated, nil
}

func validLocale(code string) bool {
	return locale.Supported(locale.Code(code))
}
