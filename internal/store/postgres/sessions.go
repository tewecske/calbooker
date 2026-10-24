package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// SessionRepository persists opaque account sessions.
type SessionRepository struct {
	adapter *Adapter
}

// NewSessionRepository constructs session storage over db.
func NewSessionRepository(db *sql.DB) (*SessionRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &SessionRepository{adapter: adapter}, nil
}

var _ service.SessionRepository = (*SessionRepository)(nil)
var _ service.AdminSessionRepository = (*SessionRepository)(nil)

// ListActiveSessions returns sessions that are neither revoked nor expired,
// newest first. The stored digest is never a usable credential.
func (r *SessionRepository) ListActiveSessions(ctx context.Context, userID int64) ([]service.Session, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, service.ErrInvalidSessionUserID
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, user_id, created_at, expires_at, revoked_at
		FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at DESC, id DESC
	`, userID, time.Now().Unix())
	if err != nil {
		return nil, mapSessionError(err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]service.Session, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, mapSessionError(err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, mapSessionError(err)
	}
	return sessions, nil
}

// CountActiveSessions returns how many sessions can currently authenticate.
func (r *SessionRepository) CountActiveSessions(ctx context.Context, userID int64) (int, error) {
	db, err := r.database(ctx)
	if err != nil {
		return 0, err
	}
	if userID <= 0 {
		return 0, service.ErrInvalidSessionUserID
	}
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2
	`, userID, time.Now().Unix()).Scan(&count); err != nil {
		return 0, mapSessionError(err)
	}
	return count, nil
}

// CreateSession stores one opaque session credential.
func (r *SessionRepository) CreateSession(ctx context.Context, session service.Session) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if session.ID == "" || len(session.ID) > 64 || session.UserID <= 0 {
		return ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapSessionError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockUserSessions(ctx, tx, session.UserID); err != nil {
		return mapSessionError(err)
	}
	// Child profiles never sign in, whatever path asked for the session.
	var isProfile bool
	if err := tx.QueryRowContext(ctx, `SELECT kind = 'profile' FROM users WHERE id = $1`, session.UserID).Scan(&isProfile); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return mapSessionError(err)
	}
	if isProfile {
		return service.ErrProfileCannotSignIn
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at, revoked_at, sso_sid, sso_id_token)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''))
	`, sessionDigest(session.ID), session.UserID, session.CreatedAt, session.ExpiresAt, session.RevokedAt, session.SSOSessionID, session.SSOIDToken)
	if err != nil {
		return mapSessionError(err)
	}
	if err := tx.Commit(); err != nil {
		return mapSessionError(err)
	}
	return mapSessionError(err)
}

// FindSession returns stored state, including expired and revoked rows. The
// service layer owns authentication policy for those states.
func (r *SessionRepository) FindSession(ctx context.Context, id string) (service.Session, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.Session{}, err
	}
	if id == "" || len(id) > 64 {
		return service.Session{}, service.ErrSessionNotFound
	}
	session, err := scanSession(db.QueryRowContext(ctx, `
		SELECT id, user_id, created_at, expires_at, revoked_at
		FROM sessions
		WHERE id = $1
	`, sessionDigest(id)))
	if err != nil {
		return service.Session{}, mapSessionError(err)
	}
	session.ID = id
	return session, nil
}

var _ service.SSOClientSessionRepository = (*SessionRepository)(nil)

// RevokeSSOSessions revokes every active session bound to a Hydra session ID
// and returns how many it revoked. An unknown sid revokes nothing.
func (r *SessionRepository) RevokeSSOSessions(ctx context.Context, sid string, revokedAt int64) (int, error) {
	db, err := r.database(ctx)
	if err != nil {
		return 0, err
	}
	if sid == "" || len(sid) > 128 {
		return 0, ErrInvalidData
	}
	result, err := db.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = $2
		WHERE sso_sid = $1 AND revoked_at IS NULL
	`, sid, revokedAt)
	if err != nil {
		return 0, mapSessionError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, mapSessionError(err)
	}
	return int(rows), nil
}

// FindSSOIDToken returns the ID token hint stored with a session, or "" when
// the session has none. It is read only to build the issuer's logout URL.
func (r *SessionRepository) FindSSOIDToken(ctx context.Context, id string) (string, error) {
	db, err := r.database(ctx)
	if err != nil {
		return "", err
	}
	if id == "" || len(id) > 64 {
		return "", service.ErrSessionNotFound
	}
	var idToken sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT sso_id_token FROM sessions WHERE id = $1`, sessionDigest(id)).Scan(&idToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", service.ErrSessionNotFound
		}
		return "", mapSessionError(err)
	}
	return idToken.String, nil
}

// RevokeSession records the first revocation time. Repeated revocation is an
// idempotent success while an absent session remains distinguishable.
func (r *SessionRepository) RevokeSession(ctx context.Context, id string, revokedAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if id == "" || len(id) > 64 {
		return service.ErrSessionNotFound
	}
	result, err := db.ExecContext(ctx, `
		UPDATE sessions
		SET revoked_at = COALESCE(revoked_at, $2)
		WHERE id = $1
	`, sessionDigest(id), revokedAt)
	if err != nil {
		return mapSessionError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapSessionError(err)
	}
	if rows == 0 {
		return service.ErrSessionNotFound
	}
	return nil
}

// RevokeUserSessions revokes every session for userID in one atomic update.
// Accounts without sessions are a successful no-op.
func (r *SessionRepository) RevokeUserSessions(ctx context.Context, userID int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if userID <= 0 {
		return service.ErrInvalidSessionUserID
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapSessionError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockUserSessions(ctx, tx, userID); err != nil {
		return mapSessionError(err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE sessions
		SET revoked_at = COALESCE(revoked_at, $2)
		WHERE user_id = $1
	`, userID, time.Now().Unix())
	if err != nil {
		return mapSessionError(err)
	}
	return mapSessionError(tx.Commit())
}

func (r *SessionRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanSession(row rowScanner) (service.Session, error) {
	var (
		session   service.Session
		revokedAt sql.NullInt64
	)
	if err := row.Scan(&session.ID, &session.UserID, &session.CreatedAt, &session.ExpiresAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.Session{}, service.ErrSessionNotFound
		}
		return service.Session{}, err
	}
	if session.ID == "" || len(session.ID) > 64 || session.UserID <= 0 || session.ExpiresAt <= session.CreatedAt {
		return service.Session{}, ErrInvalidData
	}
	session.RevokedAt = nullableInt64(revokedAt)
	return session, nil
}

func lockUserSessions(ctx context.Context, tx *sql.Tx, userID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('calbooker:sessions:' || $1::bigint::text, 0))
	`, userID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := rows.Scan(new(any)); err != nil {
			return err
		}
	}
	return rows.Err()
}

func sessionDigest(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])
}

func mapSessionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, service.ErrSessionNotFound) || errors.Is(err, sql.ErrNoRows) {
		return service.ErrSessionNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return ClassifyError(err)
}
