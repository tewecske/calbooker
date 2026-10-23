package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

func TestUserRepositoryConstructors(t *testing.T) {
	if _, err := NewUserRepository(nil); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("NewUserRepository(nil) error = %v, want %v", err, ErrNilDatabase)
	}
	repository, err := NewUserRepository(&sql.DB{})
	if err != nil {
		t.Fatalf("NewUserRepository() error = %v", err)
	}
	if repository == nil {
		t.Fatal("NewUserRepository() returned nil repository")
	}
}

func TestUserRepositoryRejectsInvalidContextAndReceiver(t *testing.T) {
	repository, err := NewUserRepository(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FindUserByID(nil, 1); !errors.Is(err, ErrNilContext) {
		t.Fatalf("FindUserByID(nil) error = %v, want %v", err, ErrNilContext)
	}
	if _, err := repository.FindUserByEmail(context.Background(), "not-an-email"); !errors.Is(err, service.ErrUserNotFound) {
		t.Fatalf("FindUserByEmail(invalid) error = %v, want %v", err, service.ErrUserNotFound)
	}
	var nilRepository *UserRepository
	if _, err := nilRepository.FindUserByID(context.Background(), 1); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("nil repository error = %v, want %v", err, ErrNilDatabase)
	}
}

func TestMapUserError(t *testing.T) {
	tests := []struct {
		name       string
		constraint string
		want       error
	}{
		{name: "email", constraint: "users_email_unique_idx", want: service.ErrDuplicateEmail},
		{name: "username", constraint: "users_username_unique_idx", want: service.ErrDuplicateUsername},
		{name: "other conflict", constraint: "other_unique_idx", want: ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := mapUserError(&pgconn.PgError{Code: "23505", ConstraintName: test.constraint})
			if !errors.Is(err, test.want) {
				t.Fatalf("mapUserError() = %v, want %v", err, test.want)
			}
		})
	}
}
