package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// OAuthStateRepository persists short-lived OAuth callback state.
type OAuthStateRepository struct {
	adapter *Adapter
}

// NewOAuthStateRepository constructs OAuth callback-state storage.
func NewOAuthStateRepository(db *sql.DB) (*OAuthStateRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &OAuthStateRepository{adapter: adapter}, nil
}

var _ service.OAuthStateRepository = (*OAuthStateRepository)(nil)

// CreateOAuthState stores one anonymous or user-bound callback state.
func (r *OAuthStateRepository) CreateOAuthState(ctx context.Context, state service.OAuthState) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	state.Provider = canonicalProvider(state.Provider)
	if state.UserID < 0 || state.State == "" || state.Provider == "" || state.Action == "" {
		return ErrInvalidData
	}
	if len(state.CodeVerifier) > 128 {
		return ErrInvalidData
	}
	var userID any
	if state.UserID > 0 {
		userID = state.UserID
	}
	var codeVerifier any
	if state.CodeVerifier != "" {
		codeVerifier = state.CodeVerifier
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, action, user_id, code_verifier, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, sessionDigest(state.State), state.Provider, state.Action, userID, codeVerifier, state.CreatedAt, state.ExpiresAt)
	return mapOAuthStateError(err)
}

// ConsumeOAuthState atomically consumes an active callback state.
func (r *OAuthStateRepository) ConsumeOAuthState(ctx context.Context, state string, now int64) (service.OAuthState, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.OAuthState{}, err
	}
	consumed, err := scanOAuthState(db.QueryRowContext(ctx, `
		UPDATE oauth_states
		SET consumed_at = $2
		WHERE state = $1 AND consumed_at IS NULL AND expires_at > $2
		RETURNING state, provider, action, user_id, code_verifier, created_at, expires_at
	`, sessionDigest(state), now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.OAuthState{}, service.ErrOAuthStateInvalid
		}
		return service.OAuthState{}, mapOAuthStateError(err)
	}
	consumed.State = state
	return consumed, nil
}

// OAuthIdentityRepository persists stable provider identities.
type OAuthIdentityRepository struct {
	adapter *Adapter
}

// NewOAuthIdentityRepository constructs external-identity storage.
func NewOAuthIdentityRepository(db *sql.DB) (*OAuthIdentityRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &OAuthIdentityRepository{adapter: adapter}, nil
}

var _ service.OAuthIdentityRepository = (*OAuthIdentityRepository)(nil)

// FindOAuthIdentity resolves one provider subject without exposing it in an
// error when no identity exists.
func (r *OAuthIdentityRepository) FindOAuthIdentity(ctx context.Context, provider, subject string) (service.OAuthIdentity, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.OAuthIdentity{}, err
	}
	provider = canonicalProvider(provider)
	if provider == "" || subject == "" {
		return service.OAuthIdentity{}, service.ErrOAuthIdentityNotFound
	}
	identity, err := scanOAuthIdentity(db.QueryRowContext(ctx, `
		SELECT id, user_id, provider, subject, email, created_at
		FROM oauth_identities
		WHERE provider = $1 AND subject = $2
	`, provider, subject))
	if err != nil {
		return service.OAuthIdentity{}, mapOAuthIdentityError(err)
	}
	return identity, nil
}

// CreateOAuthIdentity attaches one provider subject to an account.
func (r *OAuthIdentityRepository) CreateOAuthIdentity(ctx context.Context, identity service.OAuthIdentity) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	identity.Provider = canonicalProvider(identity.Provider)
	if identity.UserID <= 0 || identity.Provider == "" || identity.Subject == "" {
		return ErrInvalidData
	}
	if len(identity.Provider) > 32 || len(identity.Subject) > 255 || len(identity.Email) > 255 {
		return ErrInvalidData
	}
	var email any
	if identity.Email != "" {
		email = identity.Email
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO oauth_identities (user_id, provider, subject, email, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, identity.UserID, identity.Provider, identity.Subject, email, identity.CreatedAt)
	return mapOAuthIdentityError(err)
}

// ListOAuthIdentities returns all identities for one account in stable order.
func (r *OAuthIdentityRepository) ListOAuthIdentities(ctx context.Context, userID int64) ([]service.OAuthIdentity, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, service.ErrOAuthIdentityNotFound
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, user_id, provider, subject, email, created_at
		FROM oauth_identities
		WHERE user_id = $1
		ORDER BY created_at, id
	`, userID)
	if err != nil {
		return nil, mapOAuthIdentityError(err)
	}
	defer func() { _ = rows.Close() }()

	identities := make([]service.OAuthIdentity, 0)
	for rows.Next() {
		identity, scanErr := scanOAuthIdentity(rows)
		if scanErr != nil {
			return nil, mapOAuthIdentityError(scanErr)
		}
		identities = append(identities, identity)
	}
	if err := rows.Err(); err != nil {
		return nil, mapOAuthIdentityError(err)
	}
	return identities, nil
}

func (r *OAuthStateRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func (r *OAuthIdentityRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanOAuthState(row rowScanner) (service.OAuthState, error) {
	var (
		state        service.OAuthState
		userID       sql.NullInt64
		codeVerifier sql.NullString
	)
	if err := row.Scan(&state.State, &state.Provider, &state.Action, &userID, &codeVerifier, &state.CreatedAt, &state.ExpiresAt); err != nil {
		return service.OAuthState{}, err
	}
	state.CodeVerifier = codeVerifier.String
	if state.State == "" || len(state.State) > 64 || state.Provider == "" || len(state.Provider) > 32 || state.Action == "" || state.ExpiresAt <= state.CreatedAt {
		return service.OAuthState{}, ErrInvalidData
	}
	if userID.Valid {
		if userID.Int64 <= 0 {
			return service.OAuthState{}, ErrInvalidData
		}
		state.UserID = userID.Int64
	}
	return state, nil
}

func scanOAuthIdentity(row rowScanner) (service.OAuthIdentity, error) {
	var (
		identity service.OAuthIdentity
		email    sql.NullString
	)
	if err := row.Scan(&identity.ID, &identity.UserID, &identity.Provider, &identity.Subject, &email, &identity.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.OAuthIdentity{}, service.ErrOAuthIdentityNotFound
		}
		return service.OAuthIdentity{}, err
	}
	if identity.ID <= 0 || identity.UserID <= 0 || identity.Provider == "" || len(identity.Provider) > 32 || identity.Subject == "" || len(identity.Subject) > 255 {
		return service.OAuthIdentity{}, ErrInvalidData
	}
	if email.Valid {
		identity.Email = email.String
	}
	return identity, nil
}

func canonicalProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func mapOAuthStateError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, service.ErrOAuthStateInvalid) || errors.Is(err, sql.ErrNoRows) {
		return service.ErrOAuthStateInvalid
	}
	return ClassifyError(err)
}

func mapOAuthIdentityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, service.ErrOAuthIdentityNotFound) || errors.Is(err, sql.ErrNoRows) {
		return service.ErrOAuthIdentityNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "oauth_identities_provider_subject_unique" {
		return service.ErrOAuthIdentityConflict
	}
	return ClassifyError(err)
}
