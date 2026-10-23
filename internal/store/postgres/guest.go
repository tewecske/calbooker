package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// GuestClaimCodeRepository persists active guest transfer credentials.
type GuestClaimCodeRepository struct {
	adapter *Adapter
}

// NewGuestClaimCodeRepository constructs guest claim-code storage.
func NewGuestClaimCodeRepository(db *sql.DB) (*GuestClaimCodeRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &GuestClaimCodeRepository{adapter: adapter}, nil
}

var (
	_ service.GuestClaimCodeRepository = (*GuestClaimCodeRepository)(nil)
	_ service.GuestClaimCodeRedeemer   = (*GuestClaimCodeRepository)(nil)
	_ service.GuestClaimCodeRevoker    = (*GuestClaimCodeRepository)(nil)
	_ service.GuestCleanupRepository   = (*GuestClaimCodeRepository)(nil)
)

// FindActiveGuestClaimCode returns the active transfer credential for userID.
func (r *GuestClaimCodeRepository) FindActiveGuestClaimCode(ctx context.Context, userID int64) (service.GuestClaimCode, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GuestClaimCode{}, err
	}
	if userID <= 0 {
		return service.GuestClaimCode{}, service.ErrGuestClaimCodeNotFound
	}
	code, err := scanGuestClaimCode(db.QueryRowContext(ctx, `
		SELECT user_id, code, created_at, last_used_at, revoked_at
		FROM guest_claim_codes
		WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY id DESC
		LIMIT 1
	`, userID))
	if err != nil {
		return service.GuestClaimCode{}, mapGuestClaimError(err)
	}
	return code, nil
}

// CreateGuestClaimCode creates one active transfer credential.
func (r *GuestClaimCodeRepository) CreateGuestClaimCode(ctx context.Context, code service.GuestClaimCode) (service.GuestClaimCode, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GuestClaimCode{}, err
	}
	if code.UserID <= 0 || code.Code == "" || len(code.Code) > 64 {
		return service.GuestClaimCode{}, ErrInvalidData
	}
	created, err := scanGuestClaimCode(db.QueryRowContext(ctx, `
		INSERT INTO guest_claim_codes (user_id, code, created_at, last_used_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING user_id, code, created_at, last_used_at, revoked_at
	`, code.UserID, code.Code, code.CreatedAt, code.LastUsedAt, code.RevokedAt))
	if err != nil {
		return service.GuestClaimCode{}, mapGuestClaimError(err)
	}
	return created, nil
}

// FindGuestClaimCodeByCode returns an active transfer credential. Revoked codes
// deliberately follow the same not-found path as unknown codes.
func (r *GuestClaimCodeRepository) FindGuestClaimCodeByCode(ctx context.Context, code string) (service.GuestClaimCode, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.GuestClaimCode{}, err
	}
	if code == "" {
		return service.GuestClaimCode{}, service.ErrGuestClaimCodeNotFound
	}
	claim, err := scanGuestClaimCode(db.QueryRowContext(ctx, `
		SELECT user_id, code, created_at, last_used_at, revoked_at
		FROM guest_claim_codes
		WHERE code = $1 AND revoked_at IS NULL
	`, code))
	if err != nil {
		return service.GuestClaimCode{}, mapGuestClaimError(err)
	}
	return claim, nil
}

// MarkGuestClaimCodeUsed records the latest successful redemption while
// leaving the code active for subsequent devices.
func (r *GuestClaimCodeRepository) MarkGuestClaimCodeUsed(ctx context.Context, code string, usedAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if code == "" {
		return service.ErrGuestClaimCodeNotFound
	}
	result, err := db.ExecContext(ctx, `
		UPDATE guest_claim_codes
		SET last_used_at = GREATEST(COALESCE(last_used_at, $2), $2)
		WHERE code = $1 AND revoked_at IS NULL
	`, code, usedAt)
	if err != nil {
		return mapGuestClaimError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapGuestClaimError(err)
	}
	if rows == 0 {
		return service.ErrGuestClaimCodeNotFound
	}
	return nil
}

// RevokeGuestClaimCode invalidates the active code. Missing codes are a
// successful no-op so guest upgrade remains idempotent.
func (r *GuestClaimCodeRepository) RevokeGuestClaimCode(ctx context.Context, userID, revokedAt int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if userID <= 0 {
		return nil
	}
	_, err = db.ExecContext(ctx, `
		UPDATE guest_claim_codes
		SET revoked_at = $2
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID, revokedAt)
	return mapGuestClaimError(err)
}

// DeleteEmptyAbandonedGuests deletes old guests that own no group membership.
// Candidate users are locked before membership checks so a concurrent
// membership insert cannot be cascaded away by cleanup. Credentials and
// sessions are dependent rows and are intentionally removed by the users
// foreign-key cascade rather than treated as application data.
func (r *GuestClaimCodeRepository) DeleteEmptyAbandonedGuests(ctx context.Context, cutoff int64) (int, error) {
	db, err := r.database(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, mapGuestClaimError(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT id
		FROM users
		WHERE is_guest = TRUE AND created_at < $1
		FOR UPDATE
	`, cutoff)
	if err != nil {
		return 0, mapGuestClaimError(err)
	}
	var candidates []int64
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			_ = rows.Close()
			return 0, mapGuestClaimError(err)
		}
		candidates = append(candidates, userID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, mapGuestClaimError(err)
	}
	if err := rows.Close(); err != nil {
		return 0, mapGuestClaimError(err)
	}

	deleted := 0
	for _, userID := range candidates {
		var hasMembership bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM group_members WHERE user_id = $1)
		`, userID).Scan(&hasMembership); err != nil {
			return 0, mapGuestClaimError(err)
		}
		if hasMembership {
			continue
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = $1", userID)
		if err != nil {
			return 0, mapGuestClaimError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, mapGuestClaimError(err)
		}
		deleted += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, mapGuestClaimError(err)
	}
	return deleted, nil
}

func (r *GuestClaimCodeRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanGuestClaimCode(row rowScanner) (service.GuestClaimCode, error) {
	var (
		code                  service.GuestClaimCode
		lastUsedAt, revokedAt sql.NullInt64
	)
	if err := row.Scan(&code.UserID, &code.Code, &code.CreatedAt, &lastUsedAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.GuestClaimCode{}, service.ErrGuestClaimCodeNotFound
		}
		return service.GuestClaimCode{}, err
	}
	if code.UserID <= 0 || code.Code == "" || len(code.Code) > 64 {
		return service.GuestClaimCode{}, ErrInvalidData
	}
	code.LastUsedAt = nullableInt64(lastUsedAt)
	code.RevokedAt = nullableInt64(revokedAt)
	return code, nil
}

func mapGuestClaimError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, service.ErrGuestClaimCodeNotFound) || errors.Is(err, sql.ErrNoRows) {
		return service.ErrGuestClaimCodeNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return ClassifyError(err)
}
