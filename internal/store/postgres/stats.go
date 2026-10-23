package postgres

import (
	"context"
	"database/sql"

	"github.com/tewecske/calbooker/internal/service"
)

// DatastoreStatsRepository returns bounded aggregate counts for administrators.
type DatastoreStatsRepository struct {
	adapter *Adapter
}

// NewDatastoreStatsRepository constructs a PostgreSQL health-count repository.
func NewDatastoreStatsRepository(db *sql.DB) (*DatastoreStatsRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &DatastoreStatsRepository{adapter: adapter}, nil
}

var _ service.DatastoreStatsRepository = (*DatastoreStatsRepository)(nil)

// CountAccounts returns account totals by status.
func (r *DatastoreStatsRepository) CountAccounts(ctx context.Context) (service.AccountCounts, error) {
	db, err := r.database(ctx)
	if err != nil {
		return service.AccountCounts{}, err
	}
	var counts service.AccountCounts
	if err := db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE is_guest),
			COUNT(*) FILTER (WHERE is_admin),
			COUNT(*) FILTER (WHERE is_guest = FALSE AND email IS NOT NULL AND email_verified_at IS NULL)
		FROM users
	`).Scan(&counts.Total, &counts.Guests, &counts.Administrators, &counts.Unconfirmed); err != nil {
		return service.AccountCounts{}, wrapOperation("count accounts", err)
	}
	return counts, nil
}

// CountActiveSessions returns unexpired, unrevoked session count.
func (r *DatastoreStatsRepository) CountActiveSessions(ctx context.Context, now int64) (int, error) {
	return r.count(ctx, "count active sessions",
		`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL AND expires_at > $1`, now)
}

// CountExpiredSessions returns sessions waiting for cleanup.
func (r *DatastoreStatsRepository) CountExpiredSessions(ctx context.Context, now int64) (int, error) {
	return r.count(ctx, "count expired sessions",
		`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL OR expires_at <= $1`, now)
}

// CountExpiredOAuthStates returns OAuth states waiting for cleanup.
func (r *DatastoreStatsRepository) CountExpiredOAuthStates(ctx context.Context, now int64) (int, error) {
	return r.count(ctx, "count expired oauth states",
		`SELECT COUNT(*) FROM oauth_states WHERE consumed_at IS NOT NULL OR expires_at <= $1`, now)
}

func (r *DatastoreStatsRepository) count(ctx context.Context, operation, query string, argument int64) (int, error) {
	db, err := r.database(ctx)
	if err != nil {
		return 0, err
	}
	if argument <= 0 {
		return 0, ErrInvalidData
	}
	var count int
	if err := db.QueryRowContext(ctx, query, argument).Scan(&count); err != nil {
		return 0, wrapOperation(operation, err)
	}
	return count, nil
}

func (r *DatastoreStatsRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}
