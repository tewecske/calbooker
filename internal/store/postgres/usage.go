package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

// UsageEventRepository stores normalized request events for operations.
type UsageEventRepository struct {
	adapter *Adapter
}

// NewUsageEventRepository constructs a PostgreSQL usage-event repository.
func NewUsageEventRepository(db *sql.DB) (*UsageEventRepository, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	return &UsageEventRepository{adapter: adapter}, nil
}

var _ service.UsageEventRepository = (*UsageEventRepository)(nil)

// RecordUsageEvent inserts one normalized request event.
func (r *UsageEventRepository) RecordUsageEvent(ctx context.Context, event service.UsageEvent) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	if event.Method == "" || event.Route == "" || event.Status < 100 || event.Status > 599 {
		return ErrInvalidData
	}
	createdAt := event.CreatedAt
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO usage_events (created_at, method, route, status, request_id, trace_id, user_id, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, createdAt, event.Method, event.Route, event.Status, nullableUsageRequestID(event.RequestID), nullableUsageRequestID(event.TraceID), event.UserID, event.IP); err != nil {
		return wrapOperation("record usage event", err)
	}
	return nil
}

func nullableUsageRequestID(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (r *UsageEventRepository) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.adapter == nil {
		return nil, ErrNilDatabase
	}
	return r.adapter.DB()
}
