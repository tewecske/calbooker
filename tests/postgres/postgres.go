// Package postgres provides isolated PostgreSQL schemas for integration tests.
package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const databaseURLEnv = "CALBOOKER_DATABASE_URL"

var (
	// ErrEmptyURL identifies a missing integration database URL.
	ErrEmptyURL = errors.New("postgres test harness: empty database url")
	// ErrInvalidURL identifies an unsupported integration database URL.
	ErrInvalidURL = errors.New("postgres test harness: invalid database url")
)

// Harness owns one isolated PostgreSQL schema.
type Harness struct {
	DB     *sql.DB
	Schema string

	adminDB *sql.DB
}

// New opens an isolated schema using CALBOOKER_DATABASE_URL and registers cleanup.
// Tests without a configured database are skipped.
func New(t testing.TB) *Harness {
	t.Helper()
	harness, err := Open(context.Background(), os.Getenv(databaseURLEnv))
	if errors.Is(err, ErrEmptyURL) {
		t.Skip("CALBOOKER_DATABASE_URL is not configured")
	}
	if err != nil {
		t.Fatalf("open PostgreSQL test harness: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := harness.Close(cleanupContext); err != nil {
			t.Errorf("close PostgreSQL test harness: %v", err)
		}
	})
	return harness
}

// Open creates a random schema and returns a database handle scoped to it.
func Open(ctx context.Context, databaseURL string) (*Harness, error) {
	if ctx == nil {
		return nil, errors.New("postgres test harness: nil context")
	}
	if strings.TrimSpace(databaseURL) == "" {
		return nil, ErrEmptyURL
	}

	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.User == nil {
		return nil, ErrInvalidURL
	}

	schema, err := randomSchema()
	if err != nil {
		return nil, err
	}
	adminDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open admin database: %w", err)
	}
	adminDB.SetMaxOpenConns(1)
	adminDB.SetMaxIdleConns(1)
	if err := adminDB.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping admin database: %w", err), adminDB.Close())
	}
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+quoteIdentifier(schema)); err != nil {
		return nil, errors.Join(fmt.Errorf("create test schema: %w", err), adminDB.Close())
	}

	isolatedURL, err := scopedURL(*parsed, schema)
	if err != nil {
		return nil, errors.Join(err, dropSchema(ctx, adminDB, schema), adminDB.Close())
	}
	database, err := sql.Open("pgx", isolatedURL)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open isolated database: %w", err), dropSchema(ctx, adminDB, schema), adminDB.Close())
	}
	database.SetMaxOpenConns(5)
	database.SetMaxIdleConns(5)
	if err := database.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping isolated database: %w", err), database.Close(), dropSchema(ctx, adminDB, schema), adminDB.Close())
	}

	return &Harness{DB: database, Schema: schema, adminDB: adminDB}, nil
}

// Close drops the isolated schema and closes both database handles.
func (h *Harness) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("postgres test harness: nil context")
	}

	var closeErr error
	if h.DB != nil {
		closeErr = errors.Join(closeErr, h.DB.Close())
	}
	if h.adminDB != nil {
		closeErr = errors.Join(closeErr, dropSchema(ctx, h.adminDB, h.Schema), h.adminDB.Close())
	}
	h.DB = nil
	h.adminDB = nil
	return closeErr
}

func randomSchema() (string, error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate test schema: %w", err)
	}
	return "calbooker_test_" + hex.EncodeToString(suffix[:]), nil
}

func scopedURL(databaseURL url.URL, schema string) (string, error) {
	query := databaseURL.Query()
	query.Set("options", "-c search_path="+schema+",public")
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String(), nil
}

func dropSchema(ctx context.Context, db *sql.DB, schema string) error {
	if db == nil || schema == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, "DROP SCHEMA "+quoteIdentifier(schema)+" CASCADE"); err != nil {
		return fmt.Errorf("drop test schema: %w", err)
	}
	return nil
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
