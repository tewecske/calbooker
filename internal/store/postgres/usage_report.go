package postgres

import (
	"context"
	"database/sql"
	"strings"

	"github.com/tewecske/calbooker/internal/service"
)

// UsageReportRepository aggregates usage events by normalized route.
type UsageReportRepository struct {
	adapter *Adapter
}

// NewUsageReportRepository constructs a PostgreSQL usage-report repository.
func NewUsageReportRepository(db *sql.DB) (*UsageReportRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &UsageReportRepository{adapter: adapter}, nil
}

var _ service.UsageReportRepository = (*UsageReportRepository)(nil)

// QueryRouteUsage returns route counts within the window. The ordering is
// chosen from a fixed literal so no request value is interpolated.
func (r *UsageReportRepository) QueryRouteUsage(ctx context.Context, query service.RouteUsageQuery) ([]service.RouteUsage, error) {
	db, err := r.database(ctx)
	if err != nil {
		return nil, err
	}
	if query.Since <= 0 || query.Until <= query.Since {
		return nil, ErrInvalidData
	}
	limit := query.Limit
	if limit <= 0 || limit > service.MaxUsageReportLimit {
		limit = service.DefaultUsageReportLimit
	}
	direction := "DESC"
	if query.Ascending {
		direction = "ASC"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT route, COUNT(*) AS requests
		FROM usage_events
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY route
		ORDER BY COUNT(*) `+direction+`, route ASC
		LIMIT $3`,
		query.Since, query.Until, limit)
	if err != nil {
		return nil, wrapOperation("query route usage", err)
	}
	defer func() { _ = rows.Close() }()
	results := make([]service.RouteUsage, 0)
	for rows.Next() {
		var (
			route    sql.NullString
			requests int
		)
		if err := rows.Scan(&route, &requests); err != nil {
			return nil, wrapOperation("scan route usage", err)
		}
		if !route.Valid || strings.TrimSpace(route.String) == "" {
			return nil, ErrInvalidData
		}
		results = append(results, service.RouteUsage{Route: route.String, Requests: requests})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapOperation("iterate route usage", err)
	}
	return results, nil
}

func (r *UsageReportRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}
