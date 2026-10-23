package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	sessionIDBytes = 32
	maxSessionID   = sessionIDBytes * 2
)

var (
	// ErrNilSessionRepository identifies missing session persistence.
	ErrNilSessionRepository = errors.New("service: nil session repository")
	// ErrInvalidSessionUserID identifies an unusable account identifier.
	ErrInvalidSessionUserID = errors.New("service: invalid session user id")
	// ErrInvalidSessionLifetime identifies a duration that cannot produce a valid stored expiry.
	ErrInvalidSessionLifetime = errors.New("service: invalid session lifetime")
	// ErrInvalidSessionID identifies an empty or oversized session credential.
	ErrInvalidSessionID = errors.New("service: invalid session id")
	// ErrSessionNotFound identifies a repository result without a session.
	ErrSessionNotFound = errors.New("service: session not found")
	// ErrInvalidSession identifies a malformed session record from persistence.
	ErrInvalidSession = errors.New("service: invalid session")
	// ErrSessionExpired identifies a session past its fixed expiry.
	ErrSessionExpired = errors.New("service: session expired")
	// ErrSessionRevoked identifies a session explicitly invalidated by the server.
	ErrSessionRevoked = errors.New("service: session revoked")
	// ErrSessionIDGeneration identifies failure to create an opaque credential.
	ErrSessionIDGeneration = errors.New("service: session id generation failed")
)

// SessionService owns session lifecycle rules at the application boundary.
type SessionService struct {
	sessions SessionRepository
	lifetime time.Duration
}

// NewSessionService constructs session operations with a fixed lifetime.
func NewSessionService(sessions SessionRepository, lifetime time.Duration) (*SessionService, error) {
	if sessions == nil {
		return nil, ErrNilSessionRepository
	}
	if lifetime < time.Second {
		return nil, ErrInvalidSessionLifetime
	}
	return &SessionService{sessions: sessions, lifetime: lifetime}, nil
}

// Create creates and persists one opaque session for userID.
func (s *SessionService) Create(ctx context.Context, userID int64) (Session, error) {
	return s.create(ctx, userID, "", "")
}

// CreateSSO creates a session bound to the Hydra login session sid, keeping
// idToken as the hint Hydra requires for sign-out. The returned session never
// carries the ID token.
func (s *SessionService) CreateSSO(ctx context.Context, userID int64, sid, idToken string) (Session, error) {
	if strings.TrimSpace(sid) == "" || strings.TrimSpace(idToken) == "" {
		return Session{}, ErrInvalidSession
	}
	session, err := s.create(ctx, userID, sid, idToken)
	session.SSOIDToken = ""
	return session, err
}

func (s *SessionService) create(ctx context.Context, userID int64, sid, idToken string) (Session, error) {
	if err := validateSessionService(s, ctx); err != nil {
		return Session{}, err
	}
	if userID <= 0 {
		return Session{}, ErrInvalidSessionUserID
	}

	id, err := newSessionID()
	if err != nil {
		return Session{}, err
	}
	createdAt := time.Now().Unix()
	expiresAt := createdAt + int64(s.lifetime/time.Second)
	if expiresAt <= createdAt {
		return Session{}, ErrInvalidSessionLifetime
	}
	session := Session{
		ID:           id,
		UserID:       userID,
		CreatedAt:    createdAt,
		ExpiresAt:    expiresAt,
		SSOSessionID: sid,
		SSOIDToken:   idToken,
	}
	if err := s.sessions.CreateSession(ctx, session); err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return session, nil
}

// Lookup returns a session only while it remains active. Expired and revoked
// sessions are rejected before they can authenticate an account.
func (s *SessionService) Lookup(ctx context.Context, id string) (Session, error) {
	if err := validateSessionService(s, ctx); err != nil {
		return Session{}, err
	}
	if err := validateSessionID(id); err != nil {
		return Session{}, err
	}

	session, err := s.sessions.FindSession(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("find session: %w", err)
	}
	if session.ID == "" {
		return Session{}, ErrSessionNotFound
	}
	if session.ID != id || session.UserID <= 0 || session.ExpiresAt <= session.CreatedAt {
		return Session{}, ErrInvalidSession
	}
	if session.RevokedAt != nil {
		return Session{}, ErrSessionRevoked
	}
	if time.Now().Unix() >= session.ExpiresAt {
		return Session{}, ErrSessionExpired
	}
	return session, nil
}

// Revoke invalidates one session immediately.
func (s *SessionService) Revoke(ctx context.Context, id string) error {
	if err := validateSessionService(s, ctx); err != nil {
		return err
	}
	if err := validateSessionID(id); err != nil {
		return err
	}
	if err := s.sessions.RevokeSession(ctx, id, time.Now().Unix()); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// SignOut revokes the current session. Missing, malformed, or already absent
// sessions are harmless so repeated sign-out remains idempotent.
func (s *SessionService) SignOut(ctx context.Context, id string) error {
	if err := validateSessionService(s, ctx); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return nil
	}
	if err := s.Revoke(ctx, id); err != nil {
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrInvalidSessionID) {
			return nil
		}
		return err
	}
	return nil
}

// RevokeUser invalidates every session belonging to userID.
func (s *SessionService) RevokeUser(ctx context.Context, userID int64) error {
	if err := validateSessionService(s, ctx); err != nil {
		return err
	}
	if userID <= 0 {
		return ErrInvalidSessionUserID
	}
	if err := s.sessions.RevokeUserSessions(ctx, userID); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

func validateSessionService(s *SessionService, ctx context.Context) error {
	if s == nil || s.sessions == nil {
		return ErrNilSessionRepository
	}
	if ctx == nil {
		return errors.New("service: nil session context")
	}
	return nil
}

func validateSessionID(id string) error {
	if strings.TrimSpace(id) == "" || len(id) > maxSessionID {
		return ErrInvalidSessionID
	}
	return nil
}

func newSessionID() (string, error) {
	value := make([]byte, sessionIDBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("%w: random source unavailable", ErrSessionIDGeneration)
	}
	return hex.EncodeToString(value), nil
}
