package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var ErrEmptyDatabaseURL = errors.New("store: empty database url")

// Open opens and verifies a PostgreSQL database connection.
func Open(ctx context.Context, databaseURL string) (*sql.DB, error) {
	if ctx == nil {
		return nil, errors.New("store: nil database context")
	}
	if strings.TrimSpace(databaseURL) == "" {
		return nil, ErrEmptyDatabaseURL
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		closeErr := db.Close()
		return nil, errors.Join(fmt.Errorf("ping database: %w", err), closeErr)
	}
	return db, nil
}
