package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidTheme identifies a value outside supported visual themes.
	ErrInvalidTheme = errors.New("service: invalid theme")
	// ErrInvalidUserID identifies an account identifier that cannot be updated.
	ErrInvalidUserID = errors.New("service: invalid user id")
	// ErrNilUserRepository identifies missing persistence for theme updates.
	ErrNilUserRepository = errors.New("service: nil user repository")
)

// ThemeService updates account-owned visual theme preferences.
type ThemeService struct {
	users UserRepository
}

// NewThemeService constructs a theme use case with its consuming repository.
func NewThemeService(users UserRepository) (*ThemeService, error) {
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &ThemeService{users: users}, nil
}

// UpdateTheme validates and persists one account's theme using its current
// optimistic-lock revision.
func (s *ThemeService) UpdateTheme(ctx context.Context, userID int64, theme string) error {
	if s == nil || s.users == nil {
		return ErrNilUserRepository
	}
	if ctx == nil {
		return errors.New("service: nil theme context")
	}
	if userID <= 0 {
		return ErrInvalidUserID
	}
	theme, err := normalizeTheme(theme)
	if err != nil {
		return err
	}
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("find user for theme update: %w", err)
	}
	if user.Theme == theme {
		return nil
	}
	user.Theme = theme
	if _, err := s.users.UpdateUser(ctx, user, user.Version); err != nil {
		return fmt.Errorf("update user theme: %w", err)
	}
	return nil
}

func normalizeTheme(raw string) (string, error) {
	theme := strings.ToLower(strings.TrimSpace(raw))
	if theme != "light" && theme != "dark" {
		return "", ErrInvalidTheme
	}
	return theme, nil
}
