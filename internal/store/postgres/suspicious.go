package postgres

import (
	"context"
	"database/sql"

	"github.com/tewecske/calbooker/internal/service"
)

// SuspiciousAccountRepository finds accounts exceeding usage thresholds.
type SuspiciousAccountRepository struct {
	adapter *Adapter
}

// NewSuspiciousAccountRepository constructs a PostgreSQL threshold repository.
func NewSuspiciousAccountRepository(db *sql.DB) (*SuspiciousAccountRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &SuspiciousAccountRepository{adapter: adapter}, nil
}

var _ service.SuspiciousAccountRepository = (*SuspiciousAccountRepository)(nil)

// QueryAccountUsage returns accounts meeting either threshold within the
// window, ordered by request count.
func (r *SuspiciousAccountRepository) QueryAccountUsage(ctx context.Context, query service.AccountUsageQuery) ([]service.AccountUsage, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if query.Since <= 0 || query.Until <= query.Since || query.ActionThreshold <= 0 || query.OriginThreshold <= 0 {
		return nil, ErrInvalidData
	}
	limit := query.Limit
	if limit <= 0 || limit > service.MaxSuspiciousAccounts {
		limit = service.MaxSuspiciousAccounts
	}
	rows, err := db.QueryContext(ctx, `
		SELECT user_id, COUNT(*) AS requests, COUNT(DISTINCT ip) AS origins
		FROM usage_events
		WHERE created_at >= $1 AND created_at < $2 AND user_id IS NOT NULL
		GROUP BY user_id
		HAVING COUNT(*) >= $3 OR COUNT(DISTINCT ip) >= $4
		ORDER BY COUNT(*) DESC, user_id ASC
		LIMIT $5
	`, query.Since, query.Until, query.ActionThreshold, query.OriginThreshold, limit)
	if err != nil {
		return nil, wrapOperation("query suspicious accounts", err)
	}
	defer func() { _ = rows.Close() }()
	results := make([]service.AccountUsage, 0)
	for rows.Next() {
		var usage service.AccountUsage
		if err := rows.Scan(&usage.UserID, &usage.Requests, &usage.DistinctOrigins); err != nil {
			return nil, wrapOperation("scan suspicious account", err)
		}
		if usage.UserID <= 0 {
			return nil, ErrInvalidData
		}
		results = append(results, usage)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapOperation("iterate suspicious accounts", err)
	}
	return results, nil
}

func (r *SuspiciousAccountRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}
