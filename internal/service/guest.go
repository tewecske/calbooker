package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const guestUsernameBytes = 8

var (
	// ErrInvalidGuestCreation identifies an unusable guest creation request.
	ErrInvalidGuestCreation = errors.New("service: invalid guest creation")
	// ErrInvalidCreatedGuest identifies persistence that did not return a guest.
	ErrInvalidCreatedGuest = errors.New("service: invalid created guest")
	// ErrGuestUsernameGeneration identifies failure to create a guest username.
	ErrGuestUsernameGeneration = errors.New("service: guest username generation failed")
	// ErrNilGuestSessionDependency identifies missing guest session wiring.
	ErrNilGuestSessionDependency = errors.New("service: nil guest session dependency")
)

// GuestService creates an anonymous account for an ownership-requiring write.
// Callers must invoke Create only after deciding that the operation needs an
// owner; page rendering must not call this use case.
type GuestService struct {
	users UserRepository
}

// NewGuestService constructs guest-account creation with explicit persistence.
func NewGuestService(users UserRepository) (*GuestService, error) {
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &GuestService{users: users}, nil
}

// Create creates a credential-free guest account with safe default
// preferences. Session and username assignment belong to the guest session
// flow so account creation remains usable by ownership write services.
func (s *GuestService) Create(ctx context.Context) (User, error) {
	return s.createWithTheme(ctx, "", defaultUserTheme)
}

func (s *GuestService) createWithTheme(ctx context.Context, username, theme string) (User, error) {
	if s == nil || s.users == nil {
		return User{}, ErrInvalidGuestCreation
	}
	if ctx == nil {
		return User{}, errors.New("service: nil guest context")
	}
	theme, err := normalizeTheme(theme)
	if err != nil {
		return User{}, err
	}
	if username != "" {
		normalized, err := NormalizeUsername(username)
		if err != nil {
			return User{}, errors.Join(ErrInvalidGuestCreation, err)
		}
		username = normalized
	}

	guest := User{
		Kind:      UserKindGuest,
		Theme:     theme,
		Locale:    defaultUserLocale,
		CreatedAt: time.Now().Unix(),
	}
	if username != "" {
		guest.Username = &username
	}
	created, err := s.users.CreateUser(ctx, guest)
	if err != nil {
		return User{}, fmt.Errorf("create guest user: %w", err)
	}
	if created.ID <= 0 || !created.IsGuest() || created.Email != nil || created.EmailVerifiedAt != nil {
		return User{}, ErrInvalidCreatedGuest
	}
	return created, nil
}

// GuestSessionResult contains a newly created guest account and ordinary
// session. The session ID remains an opaque credential for the cookie layer.
type GuestSessionResult struct {
	User    User
	Session Session
}

// GuestSessionService creates guest accounts and authenticates them with the
// same session lifecycle as normal accounts.
type GuestSessionService struct {
	guests   *GuestService
	sessions *SessionService
}

// NewGuestSessionService constructs guest account and session operations.
func NewGuestSessionService(users UserRepository, sessions *SessionService) (*GuestSessionService, error) {
	guests, err := NewGuestService(users)
	if err != nil {
		return nil, err
	}
	if sessions == nil {
		return nil, ErrNilGuestSessionDependency
	}
	return &GuestSessionService{guests: guests, sessions: sessions}, nil
}

// Create creates a guest with a random username and an ordinary session.
func (s *GuestSessionService) Create(ctx context.Context) (GuestSessionResult, error) {
	return s.CreateWithTheme(ctx, defaultUserTheme)
}

// CreateWithTheme creates a guest using the active anonymous browser theme.
func (s *GuestSessionService) CreateWithTheme(ctx context.Context, theme string) (GuestSessionResult, error) {
	if s == nil || s.guests == nil || s.sessions == nil {
		return GuestSessionResult{}, ErrNilGuestSessionDependency
	}
	username, err := newGuestUsername()
	if err != nil {
		return GuestSessionResult{}, err
	}
	user, err := s.guests.createWithTheme(ctx, username, theme)
	if err != nil {
		return GuestSessionResult{}, err
	}
	session, err := s.sessions.Create(ctx, user.ID)
	if err != nil {
		return GuestSessionResult{}, fmt.Errorf("create guest session: %w", err)
	}
	return GuestSessionResult{User: user, Session: session}, nil
}

func newGuestUsername() (string, error) {
	value := make([]byte, guestUsernameBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("%w: random source unavailable", ErrGuestUsernameGeneration)
	}
	return "guest-" + hex.EncodeToString(value), nil
}
