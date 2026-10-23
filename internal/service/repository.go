package service

import (
	"context"
	"errors"
)

var (
	// ErrOptimisticLockConflict identifies a write based on a stale revision.
	ErrOptimisticLockConflict = errors.New("service: optimistic lock conflict")
	// ErrRecordNotFound identifies a stale write whose record no longer exists.
	ErrRecordNotFound = errors.New("service: record not found")
)

// ClassifyStaleWrite translates a revision-checked write failure into the
// conflict or not-found outcome. Missing records are reported as not-found even
// when the repository cannot distinguish them from a concurrent change, so the
// caller never shows a misleading conflict for a deleted record.
func ClassifyStaleWrite(missing bool) error {
	if missing {
		return ErrRecordNotFound
	}
	return ErrOptimisticLockConflict
}

// IsWriteConflict reports whether err is an optimistic-lock or missing-record
// outcome produced by a revision-checked write.
func IsWriteConflict(err error) bool {
	return errors.Is(err, ErrOptimisticLockConflict) || errors.Is(err, ErrRecordNotFound)
}

// User is the persistence-facing account model. Nullable database columns use
// pointers so repositories cannot silently turn NULL into an application value.
type User struct {
	ID              int64
	Email           *string
	Username        *string
	DisplayName     *string
	IsGuest         bool
	IsAdmin         bool
	Theme           string
	Locale          string
	CreatedAt       int64
	EmailVerifiedAt *int64
	Version         int64
}

// Session is an opaque account session. Repositories must never expose its ID
// through logs, diagnostics, or user-facing responses.
type Session struct {
	ID        string
	UserID    int64
	CreatedAt int64
	ExpiresAt int64
	RevokedAt *int64
	// SSOSessionID and SSOIDToken bind a client-mode session to the Hydra
	// login session that created it. They are written at creation only, are
	// never credentials for this app, and must never be logged or rendered.
	SSOSessionID string
	SSOIDToken   string
}

// GuestClaimCode is the active transfer credential for one guest account.
// Code is returned only to the explicit transfer-code flow.
type GuestClaimCode struct {
	UserID     int64
	Code       string
	CreatedAt  int64
	LastUsedAt *int64
	RevokedAt  *int64
}

// UserRepository persists account records. Update and Delete require the
// revision read by the caller so stale writes can be reported as conflicts.
type UserRepository interface {
	CreateUser(context.Context, User) (User, error)
	FindUserByID(context.Context, int64) (User, error)
	FindUserByEmail(context.Context, string) (User, error)
	FindUserByUsername(context.Context, string) (User, error)
	UpdateUser(context.Context, User, int64) (User, error)
	DeleteUser(context.Context, int64, int64) error
}

// SessionRepository persists and revokes account sessions.
type SessionRepository interface {
	CreateSession(context.Context, Session) error
	FindSession(context.Context, string) (Session, error)
	RevokeSession(context.Context, string, int64) error
	RevokeUserSessions(context.Context, int64) error
}

// GuestClaimCodeRepository persists one active transfer code per guest.
type GuestClaimCodeRepository interface {
	FindActiveGuestClaimCode(context.Context, int64) (GuestClaimCode, error)
	CreateGuestClaimCode(context.Context, GuestClaimCode) (GuestClaimCode, error)
}

// GuestClaimCodeRedeemer reads and records use of transfer codes. It remains a
// separate port so issuing and redeeming adapters can evolve independently.
type GuestClaimCodeRedeemer interface {
	FindGuestClaimCodeByCode(context.Context, string) (GuestClaimCode, error)
	MarkGuestClaimCodeUsed(context.Context, string, int64) error
}

// GuestClaimCodeRevoker invalidates the active transfer credential during
// guest upgrade. Missing active codes are a successful no-op for adapters.
type GuestClaimCodeRevoker interface {
	RevokeGuestClaimCode(context.Context, int64, int64) error
}

// GuestCleanupRepository removes only guests that are old and have no owned
// application data. The store must enforce both predicates in one operation.
type GuestCleanupRepository interface {
	DeleteEmptyAbandonedGuests(context.Context, int64) (int, error)
}
