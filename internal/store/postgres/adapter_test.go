package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name string
		db   *sql.DB
		want error
	}{
		{name: "nil database", want: ErrNilDatabase},
		{name: "database", db: &sql.DB{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := New(test.db)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("New() error = %v, want %v", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if got, err := adapter.DB(); err != nil || got != test.db {
				t.Fatalf("DB() = %p, %v, want %p, nil", got, err, test.db)
			}
		})
	}
}

func TestAdapterDBRejectsNilReceiver(t *testing.T) {
	var adapter *Adapter
	if _, err := adapter.DB(); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("DB() error = %v, want %v", err, ErrNilDatabase)
	}
}

func TestValidateContext(t *testing.T) {
	if err := ValidateContext(nil); !errors.Is(err, ErrNilContext) {
		t.Fatalf("ValidateContext(nil) = %v, want %v", err, ErrNilContext)
	}
	if err := ValidateContext(context.Background()); err != nil {
		t.Fatalf("ValidateContext(context.Background()) = %v", err)
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "nil", want: nil},
		{name: "not found", err: sql.ErrNoRows, want: ErrNotFound},
		{name: "wrapped not found", err: fmt.Errorf("query: %w", sql.ErrNoRows), want: ErrNotFound},
		{name: "canceled", err: context.Canceled, want: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "unique", err: &pgconn.PgError{Code: "23505"}, want: ErrConflict},
		{name: "not null", err: &pgconn.PgError{Code: "23502"}, want: ErrInvalidData},
		{name: "foreign key", err: &pgconn.PgError{Code: "23503"}, want: ErrInvalidData},
		{name: "check", err: &pgconn.PgError{Code: "23514"}, want: ErrInvalidData},
		{name: "length", err: &pgconn.PgError{Code: "22001"}, want: ErrInvalidData},
		{name: "numeric", err: &pgconn.PgError{Code: "22003"}, want: ErrInvalidData},
		{name: "syntax", err: &pgconn.PgError{Code: "22P02"}, want: ErrInvalidData},
		{name: "driver failure", err: &pgconn.PgError{Code: "08006"}, want: ErrDatabase},
		{name: "unknown", err: errors.New("driver failure"), want: ErrDatabase},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("ClassifyError() = %v, want %v", got, test.want)
			}
			if test.err != nil && got != context.Canceled && got != context.DeadlineExceeded && got.Error() == test.err.Error() {
				t.Fatalf("ClassifyError() leaked driver error %q", got)
			}
		})
	}
}

func TestNullableScans(t *testing.T) {
	text := sql.NullString{String: "value", Valid: true}
	if got := nullableString(text); got == nil || *got != "value" {
		t.Fatalf("nullableString(valid) = %v, want value", got)
	}
	if got := nullableString(sql.NullString{}); got != nil {
		t.Fatalf("nullableString(NULL) = %v, want nil", got)
	}

	integer := sql.NullInt64{Int64: 42, Valid: true}
	if got := nullableInt64(integer); got == nil || *got != 42 {
		t.Fatalf("nullableInt64(valid) = %v, want 42", got)
	}
	if got := nullableInt64(sql.NullInt64{}); got != nil {
		t.Fatalf("nullableInt64(NULL) = %v, want nil", got)
	}
}
