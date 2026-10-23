// Package postgres contains PostgreSQL persistence adapters for service ports.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound identifies an absent persistence record.
	ErrNotFound = errors.New("postgres: record not found")
	// ErrConflict identifies a uniqueness or optimistic-lock conflict.
	ErrConflict = errors.New("postgres: record conflict")
	// ErrInvalidData identifies data rejected by a database constraint.
	ErrInvalidData = errors.New("postgres: invalid data")
	// ErrDatabase identifies an unexpected database failure without exposing
	// driver details through service behavior.
	ErrDatabase = errors.New("postgres: database operation failed")
	// ErrNilDatabase identifies a missing database handle.
	ErrNilDatabase = errors.New("postgres: nil database")
	// ErrNilContext identifies a missing request context.
	ErrNilContext = errors.New("postgres: nil context")
)

// Adapter is the shared PostgreSQL adapter boundary. It owns no connection
// lifecycle; the composition root closes the injected database handle.
type Adapter struct {
	db *sql.DB
}

// New creates an adapter over db. Callers retain ownership of db.
func New(db *sql.DB) (*Adapter, error) {
	if db == nil {
		return nil, ErrNilDatabase
	}
	return &Adapter{db: db}, nil
}

// DB returns the injected database handle for concrete adapters in this
// package. Every caller must use a context-aware database method.
func (a *Adapter) DB() (*sql.DB, error) {
	if a == nil || a.db == nil {
		return nil, ErrNilDatabase
	}
	return a.db, nil
}

// ValidateContext rejects nil contexts before database/sql can panic.
func ValidateContext(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	return nil
}

// ClassifyError maps database and driver errors to safe adapter errors.
// Cancellation and deadline errors remain discoverable with errors.Is.
func ClassifyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, ErrConflict) {
		return ErrConflict
	}
	if errors.Is(err, ErrInvalidData) {
		return ErrInvalidData
	}
	if errors.Is(err, ErrDatabase) {
		return ErrDatabase
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrConflict
		case "23502", "23503", "23514", "22001", "22003", "22P02":
			return ErrInvalidData
		default:
			return ErrDatabase
		}
	}
	return ErrDatabase
}

func wrapOperation(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, ClassifyError(err))
}
