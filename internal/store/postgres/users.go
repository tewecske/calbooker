package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

// UserRepository persists service users in PostgreSQL.
type UserRepository struct {
	adapter *Adapter
}

// NewUserRepository constructs a PostgreSQL user repository over db.
func NewUserRepository(db *sql.DB) (*UserRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &UserRepository{adapter: adapter}, nil
}

var _ service.UserRepository = (*UserRepository)(nil)

// CreateUser inserts one account and returns database-generated fields.
func (r *UserRepository) CreateUser(ctx context.Context, user service.User) (service.User, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.User{}, err
	}
	if user.Theme == "" {
		user.Theme = "light"
	}
	if user.Locale == "" {
		user.Locale = "en"
	}
	if user.Kind == "" {
		user.Kind = service.UserKindAccount
	}
	created, err := scanUser(db.QueryRowContext(ctx, `
		INSERT INTO users (
			email, username, display_name, kind, is_admin,
			theme, locale, created_at, email_verified_at, version
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, email, username, display_name, kind,
			is_admin, theme, locale, created_at, email_verified_at, version
	`, user.Email, user.Username, user.DisplayName, string(user.Kind),
		user.IsAdmin, user.Theme, user.Locale, user.CreatedAt, user.EmailVerifiedAt, user.Version))
	if err != nil {
		return service.User{}, mapUserError(err)
	}
	return created, nil
}

// FindUserByID returns one account by its generated identifier.
func (r *UserRepository) FindUserByID(ctx context.Context, id int64) (service.User, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.User{}, err
	}
	if id <= 0 {
		return service.User{}, service.ErrUserNotFound
	}
	return r.find(ctx, db.QueryRowContext(ctx, userSelect+" WHERE id = $1", id))
}

// FindUserByEmail returns an account by normalized email.
func (r *UserRepository) FindUserByEmail(ctx context.Context, email string) (service.User, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.User{}, err
	}
	normalized, err := service.NormalizeEmail(email)
	if err != nil || normalized == "" {
		return service.User{}, service.ErrUserNotFound
	}
	return r.find(ctx, db.QueryRowContext(ctx, userSelect+" WHERE email = $1", normalized))
}

// FindUserByUsername returns an account by normalized username.
func (r *UserRepository) FindUserByUsername(ctx context.Context, username string) (service.User, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.User{}, err
	}
	normalized, err := service.NormalizeUsername(username)
	if err != nil || normalized == "" {
		return service.User{}, service.ErrUserNotFound
	}
	return r.find(ctx, db.QueryRowContext(ctx, userSelect+" WHERE username = $1", normalized))
}

// UpdateUser replaces editable account fields when expectedVersion is current.
func (r *UserRepository) UpdateUser(ctx context.Context, user service.User, expectedVersion int64) (service.User, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.User{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.User{}, mapUserError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if !user.IsAdmin || !user.IsFullUser() {
		if err := keepAnotherAdministrator(ctx, tx, user.ID); err != nil {
			return service.User{}, err
		}
	}
	updated, scanErr := scanUser(tx.QueryRowContext(ctx, `
		UPDATE users
		SET email = $1, username = $2, display_name = $3,
			kind = $4, is_admin = $5, theme = $6, locale = $7,
			email_verified_at = $8, version = version + 1
		WHERE id = $9 AND version = $10
		RETURNING id, email, username, display_name, kind,
			is_admin, theme, locale, created_at, email_verified_at, version
	`, user.Email, user.Username, user.DisplayName, string(user.Kind),
		user.IsAdmin, user.Theme, user.Locale, user.EmailVerifiedAt,
		user.ID, expectedVersion))
	if scanErr == nil {
		if err := tx.Commit(); err != nil {
			return service.User{}, mapUserError(err)
		}
		return updated, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return service.User{}, mapUserError(scanErr)
	}
	_ = tx.Rollback()
	return service.User{}, r.classifyMissingWrite(ctx, db, user.ID)
}

// DeleteUser removes an account when expectedVersion is current.
func (r *UserRepository) DeleteUser(ctx context.Context, id, expectedVersion int64) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return mapUserError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := keepAnotherAdministrator(ctx, tx, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = $1 AND version = $2", id, expectedVersion)
	if err != nil {
		return mapUserError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mapUserError(err)
	}
	if rows == 1 {
		return mapUserError(tx.Commit())
	}
	_ = tx.Rollback()
	return r.classifyMissingWrite(ctx, db, id)
}

// keepAnotherAdministrator runs before a write that removes administrator
// rights from id (demotion or deletion). When id is currently an
// administrator it serializes such writes with a transaction-scoped advisory
// lock and refuses the write if no other administrator would remain, so two
// administrators demoting each other at once cannot leave none. Writes to
// accounts that are not administrators take no lock.
func keepAnotherAdministrator(ctx context.Context, tx *sql.Tx, id int64) error {
	var isAdmin bool
	err := tx.QueryRowContext(ctx, "SELECT is_admin AND kind = 'account' FROM users WHERE id = $1", id).Scan(&isAdmin)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !isAdmin) {
		return nil
	}
	if err != nil {
		return mapUserError(err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('calbooker:administrators', 0))"); err != nil {
		return mapUserError(err)
	}
	// Re-read under the lock: a concurrent writer may have changed either the
	// target or the other administrators.
	var others int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM users WHERE is_admin AND kind = 'account' AND id <> $1
	`, id).Scan(&others); err != nil {
		return mapUserError(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT is_admin AND kind = 'account' FROM users WHERE id = $1", id).Scan(&isAdmin); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return mapUserError(err)
	}
	if isAdmin && others == 0 {
		return service.ErrLastAdministrator
	}
	return nil
}

const userSelect = `
	SELECT id, email, username, display_name, kind,
		is_admin, theme, locale, created_at, email_verified_at, version
	FROM users`

type rowScanner interface {
	Scan(...any) error
}

func (r *UserRepository) find(ctx context.Context, row rowScanner) (service.User, error) {
	user, err := scanUser(row)
	if err != nil {
		return service.User{}, mapUserError(err)
	}
	return user, nil
}

func (r *UserRepository) classifyMissingWrite(ctx context.Context, db *sql.DB, id int64) error {
	if id <= 0 {
		return service.ErrUserNotFound
	}
	var exists bool
	err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", id).Scan(&exists)
	if err != nil {
		return mapUserError(err)
	}
	if !exists {
		return service.ErrUserNotFound
	}
	return service.ErrOptimisticLockConflict
}

func (r *UserRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}

func scanUser(row rowScanner) (service.User, error) {
	var (
		user            service.User
		email, username sql.NullString
		displayName     sql.NullString
		emailVerifiedAt sql.NullInt64
		kind            string
	)
	if err := row.Scan(
		&user.ID, &email, &username, &displayName,
		&kind, &user.IsAdmin, &user.Theme, &user.Locale,
		&user.CreatedAt, &emailVerifiedAt, &user.Version,
	); err != nil {
		return service.User{}, err
	}
	user.Kind = service.UserKind(kind)
	if user.ID <= 0 || user.Version < 0 || !user.Kind.Valid() {
		return service.User{}, ErrInvalidData
	}
	user.Email = nullableString(email)
	user.Username = nullableString(username)
	user.DisplayName = nullableString(displayName)
	user.EmailVerifiedAt = nullableInt64(emailVerifiedAt)
	return user, nil
}

func mapUserError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return service.ErrUserNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "users_email_unique_idx":
			return service.ErrDuplicateEmail
		case "users_username_unique_idx":
			return service.ErrDuplicateUsername
		}
	}
	return ClassifyError(err)
}

var _ service.SSOGuestUpgradeRepository = (*UserRepository)(nil)

// UpgradeGuestWithIdentity converts a guest in place and links its single
// sign-on identity in one transaction, revoking the guest's transfer code.
// It applies only while the account is still a guest at expectedVersion, so
// either everything happens or nothing does.
func (r *UserRepository) UpgradeGuestWithIdentity(ctx context.Context, guest service.User, expectedVersion int64, identity service.OAuthIdentity, revokedAt int64) (service.User, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.User{}, err
	}
	identity.Provider = canonicalProvider(identity.Provider)
	if guest.ID <= 0 || !guest.IsFullUser() || identity.UserID != guest.ID || identity.Provider == "" || identity.Subject == "" || len(identity.Subject) > 255 {
		return service.User{}, ErrInvalidData
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.User{}, mapUserError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE guest_claim_codes SET revoked_at = $2
		WHERE user_id = $1 AND revoked_at IS NULL
	`, guest.ID, revokedAt); err != nil {
		return service.User{}, mapGuestClaimError(err)
	}
	updated, err := scanUser(tx.QueryRowContext(ctx, `
		UPDATE users
		SET email = $1, display_name = $2, kind = 'account', email_verified_at = $3, version = version + 1
		WHERE id = $4 AND version = $5 AND kind = 'guest'
		RETURNING id, email, username, display_name, kind,
			is_admin, theme, locale, created_at, email_verified_at, version
	`, guest.Email, guest.DisplayName, guest.EmailVerifiedAt, guest.ID, expectedVersion))
	if errors.Is(err, sql.ErrNoRows) {
		return service.User{}, service.ErrGuestUpgradeRequiresGuest
	}
	if err != nil {
		return service.User{}, mapUserError(err)
	}
	var email any
	if identity.Email != "" {
		email = identity.Email
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_identities (user_id, provider, subject, email, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, guest.ID, identity.Provider, identity.Subject, email, identity.CreatedAt); err != nil {
		return service.User{}, mapOAuthIdentityError(err)
	}
	if err := tx.Commit(); err != nil {
		return service.User{}, mapUserError(err)
	}
	return updated, nil
}
