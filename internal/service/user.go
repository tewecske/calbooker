package service

import (
	"errors"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tewecske/calbooker/internal/locale"
)

const (
	defaultUserTheme     = "light"
	defaultUserLocale    = string(locale.Default)
	maxDisplayNameLength = 255
	maxEmailLength       = 255
	maxUsernameLength    = 32
)

var (
	// ErrUserNotFound identifies an identity lookup without a matching account.
	ErrUserNotFound = errors.New("service: user not found")
	// ErrDuplicateEmail identifies an email already attached to an account.
	ErrDuplicateEmail = errors.New("service: duplicate email")
	// ErrDuplicateUsername identifies a username already attached to an account.
	ErrDuplicateUsername = errors.New("service: duplicate username")
	// ErrInvalidDisplayName identifies a display name outside storage limits.
	ErrInvalidDisplayName = errors.New("service: invalid display name")
	// ErrInvalidCreatedUser identifies persistence that did not return its new account.
	ErrInvalidCreatedUser = errors.New("service: invalid created user")
	// ErrInvalidEmail identifies an email value that cannot be used as an account identity.
	ErrInvalidEmail = errors.New("service: invalid email")
	// ErrInvalidUsername identifies a username value that cannot be used as an account identity.
	ErrInvalidUsername = errors.New("service: invalid username")
	// ErrGuestUpgradeRequiresGuest identifies an account that cannot be upgraded.
	ErrGuestUpgradeRequiresGuest = errors.New("service: account is not guest")
	// ErrInvalidUpgradedUser identifies persistence that did not return the
	// converted account.
	ErrInvalidUpgradedUser = errors.New("service: invalid upgraded user")
)

// NormalizeEmail trims and lowercases an optional email identity, then checks
// that its normalized form is a single usable email address.
func NormalizeEmail(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return "", nil
	}
	if utf8.RuneCountInString(normalized) > maxEmailLength {
		return "", ErrInvalidEmail
	}

	address, err := mail.ParseAddress(normalized)
	if err != nil || address.Address != normalized {
		return "", ErrInvalidEmail
	}
	return normalized, nil
}

// NormalizeUsername trims and lowercases an optional username identity. Usernames
// cannot contain whitespace or '@', which keeps them distinct from email input.
func NormalizeUsername(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return "", nil
	}
	if utf8.RuneCountInString(normalized) > maxUsernameLength {
		return "", ErrInvalidUsername
	}
	for _, character := range normalized {
		if character == '@' || unicode.IsSpace(character) {
			return "", ErrInvalidUsername
		}
	}
	return normalized, nil
}

func normalizeDisplayName(raw string) (string, error) {
	displayName := strings.TrimSpace(raw)
	if utf8.RuneCountInString(displayName) > maxDisplayNameLength {
		return "", ErrInvalidDisplayName
	}
	return displayName, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
