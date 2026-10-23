package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

// RateLimitStore keeps fixed-window attempt counters in PostgreSQL so every
// instance of an app shares them. One store serves one limiter (its scope);
// all scopes share the rate_limit_buckets table. Keys are stored as a SHA-256
// hash plus the limiter's redacted hint, never in the clear.
type RateLimitStore struct {
	adapter *Adapter
	scope   string
}

// NewRateLimitStore constructs the shared store for one limiter scope.
func NewRateLimitStore(db *sql.DB, scope string) (*RateLimitStore, error) {
	adapter, err := New(db)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(scope) == "" || len(scope) > 64 {
		return nil, ErrInvalidData
	}
	return &RateLimitStore{adapter: adapter, scope: scope}, nil
}

var _ service.RateLimitStore = (*RateLimitStore)(nil)

func rateLimitKeyHash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// Hit counts one attempt in a single statement, so concurrent attempts from
// several instances never lose an increment. An expired window restarts.
func (s *RateLimitStore) Hit(ctx context.Context, action, key, hint string, window time.Duration, now time.Time) (service.RateLimitHit, error) {
	db, err := s.database(ctx)
	if err != nil {
		return service.RateLimitHit{}, err
	}
	if window < time.Millisecond {
		return service.RateLimitHit{}, ErrInvalidData
	}
	nowMillis, windowMillis := now.UnixMilli(), window.Milliseconds()
	var count int
	var started int64
	err = db.QueryRowContext(ctx, `
		INSERT INTO rate_limit_buckets (scope, action, key_hash, key_hint, hit_count, started_at, expires_at)
		VALUES ($1, $2, $3, $4, 1, $5::bigint, $5::bigint + $6::bigint)
		ON CONFLICT (scope, action, key_hash) DO UPDATE SET
			hit_count  = CASE WHEN rate_limit_buckets.expires_at <= $5::bigint THEN 1 ELSE rate_limit_buckets.hit_count + 1 END,
			started_at = CASE WHEN rate_limit_buckets.expires_at <= $5::bigint THEN $5::bigint ELSE rate_limit_buckets.started_at END,
			expires_at = CASE WHEN rate_limit_buckets.expires_at <= $5::bigint THEN $5::bigint + $6::bigint ELSE rate_limit_buckets.expires_at END
		RETURNING hit_count, started_at
	`, s.scope, action, rateLimitKeyHash(key), hint, nowMillis, windowMillis).Scan(&count, &started)
	if err != nil {
		return service.RateLimitHit{}, wrapOperation("hit rate limit", err)
	}
	return service.RateLimitHit{Count: count, Started: time.UnixMilli(started)}, nil
}

// Get returns the live window for action/key without counting an attempt.
func (s *RateLimitStore) Get(ctx context.Context, action, key string, now time.Time) (service.RateLimitHit, error) {
	db, err := s.database(ctx)
	if err != nil {
		return service.RateLimitHit{}, err
	}
	var count int
	var started int64
	err = db.QueryRowContext(ctx, `
		SELECT hit_count, started_at FROM rate_limit_buckets
		WHERE scope = $1 AND action = $2 AND key_hash = $3 AND expires_at > $4
	`, s.scope, action, rateLimitKeyHash(key), now.UnixMilli()).Scan(&count, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return service.RateLimitHit{}, nil
	}
	if err != nil {
		return service.RateLimitHit{}, wrapOperation("get rate limit", err)
	}
	return service.RateLimitHit{Count: count, Started: time.UnixMilli(started)}, nil
}

// Clear removes one action/key window.
func (s *RateLimitStore) Clear(ctx context.Context, action, key string) error {
	db, err := s.database(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM rate_limit_buckets WHERE scope = $1 AND action = $2 AND key_hash = $3`,
		s.scope, action, rateLimitKeyHash(key))
	return wrapOperation("clear rate limit", err)
}

// List returns every live window of this scope.
func (s *RateLimitStore) List(ctx context.Context, now time.Time) ([]service.RateLimitStoredBucket, error) {
	db, err := s.database(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT action, key_hint, hit_count, started_at FROM rate_limit_buckets
		WHERE scope = $1 AND expires_at > $2
	`, s.scope, now.UnixMilli())
	if err != nil {
		return nil, wrapOperation("list rate limits", err)
	}
	defer func() { _ = rows.Close() }()
	buckets := make([]service.RateLimitStoredBucket, 0)
	for rows.Next() {
		var bucket service.RateLimitStoredBucket
		var started int64
		if err := rows.Scan(&bucket.Action, &bucket.KeyHint, &bucket.Count, &started); err != nil {
			return nil, wrapOperation("scan rate limit", err)
		}
		bucket.Started = time.UnixMilli(started)
		buckets = append(buckets, bucket)
	}
	return buckets, wrapOperation("list rate limits", rows.Err())
}

// ClearAction removes every window of one action in this scope.
func (s *RateLimitStore) ClearAction(ctx context.Context, action string) (int, error) {
	return s.delete(ctx, "clear rate limit action", `DELETE FROM rate_limit_buckets WHERE scope = $1 AND action = $2`, s.scope, action)
}

// ClearAll removes every window in this scope.
func (s *RateLimitStore) ClearAll(ctx context.Context) (int, error) {
	return s.delete(ctx, "clear rate limits", `DELETE FROM rate_limit_buckets WHERE scope = $1`, s.scope)
}

// DeleteExpired removes expired windows of every scope. Expired windows are
// already ignored and restarted by Hit; this only bounds the table.
func (s *RateLimitStore) DeleteExpired(ctx context.Context) (int, error) {
	return s.delete(ctx, "delete expired rate limits", `DELETE FROM rate_limit_buckets WHERE expires_at <= $1`, time.Now().UnixMilli())
}

func (s *RateLimitStore) delete(ctx context.Context, operation, query string, args ...any) (int, error) {
	db, err := s.database(ctx)
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, wrapOperation(operation, err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, wrapOperation(operation, err)
	}
	return int(removed), nil
}

func (s *RateLimitStore) database(ctx context.Context) (*sql.DB, error) {
	if err := ValidateContext(ctx); err != nil {
		return nil, err
	}
	return s.adapter.DB()
}
