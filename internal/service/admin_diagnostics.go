package service

import (
	"context"
	"errors"
)

var (
	// ErrNilAdminDiagnosticsRepository identifies missing diagnostics persistence.
	ErrNilAdminDiagnosticsRepository = errors.New("service: nil admin diagnostics repository")
)

// AdminSessionRepository reads active sessions for an account. It never
// returns credential-bearing identifiers to a diagnostic caller.
type AdminSessionRepository interface {
	ListActiveSessions(context.Context, int64) ([]Session, error)
	CountActiveSessions(context.Context, int64) (int, error)
}

// AdminUserDetail is the safe account diagnostics aggregate. It contains no
// session identifiers, tokens, or provider subjects.
type AdminUserDetail struct {
	User       User
	Confirmed  bool
	Sessions   []Session
	Identities []OAuthIdentity
}

// AdminDiagnosticsService aggregates safe account diagnostics for the
// administrator area.
type AdminDiagnosticsService struct {
	users      UserRepository
	sessions   AdminSessionRepository
	identities OAuthIdentityRepository
}

// NewAdminDiagnosticsService constructs diagnostics with explicit account,
// session, and identity ports.
func NewAdminDiagnosticsService(users UserRepository, sessions AdminSessionRepository, identities OAuthIdentityRepository) (*AdminDiagnosticsService, error) {
	if users == nil || sessions == nil || identities == nil {
		return nil, ErrNilAdminDiagnosticsRepository
	}
	return &AdminDiagnosticsService{users: users, sessions: sessions, identities: identities}, nil
}

// Detail returns the safe diagnostics for one account. A vanished account is
// reported as not-found so the caller can render a not-found result.
func (s *AdminDiagnosticsService) Detail(ctx context.Context, userID int64) (AdminUserDetail, error) {
	if s == nil || s.users == nil || s.sessions == nil || s.identities == nil {
		return AdminUserDetail{}, ErrNilAdminDiagnosticsRepository
	}
	if ctx == nil {
		return AdminUserDetail{}, errors.New("service: nil admin diagnostics context")
	}
	if userID <= 0 {
		return AdminUserDetail{}, ErrInvalidUserID
	}
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return AdminUserDetail{}, ErrRecordNotFound
		}
		return AdminUserDetail{}, err
	}
	sessions, err := s.sessions.ListActiveSessions(ctx, userID)
	if err != nil {
		return AdminUserDetail{}, err
	}
	identities, err := s.identities.ListOAuthIdentities(ctx, userID)
	if err != nil {
		return AdminUserDetail{}, err
	}
	return AdminUserDetail{
		User:       user,
		Confirmed:  user.EmailVerifiedAt != nil,
		Sessions:   sessions,
		Identities: identities,
	}, nil
}
