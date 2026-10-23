package postgres

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tewecske/calbooker/internal/service"
)

func TestOAuthRepositoryConstructors(t *testing.T) {
	constructors := []struct {
		name string
		new  func(*sql.DB) (any, error)
	}{
		{name: "state", new: func(db *sql.DB) (any, error) { return NewOAuthStateRepository(db) }},
		{name: "identity", new: func(db *sql.DB) (any, error) { return NewOAuthIdentityRepository(db) }},
	}
	for _, constructor := range constructors {
		t.Run(constructor.name, func(t *testing.T) {
			if _, err := constructor.new(nil); !errors.Is(err, ErrNilDatabase) {
				t.Fatalf("constructor(nil) error = %v, want %v", err, ErrNilDatabase)
			}
		})
	}
}

func TestOAuthRepositoriesRejectNilContext(t *testing.T) {
	state, err := NewOAuthStateRepository(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.ConsumeOAuthState(nil, "state", 1); !errors.Is(err, ErrNilContext) {
		t.Fatalf("state Consume(nil) = %v, want %v", err, ErrNilContext)
	}
	identity, err := NewOAuthIdentityRepository(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.FindOAuthIdentity(nil, "google", "subject"); !errors.Is(err, ErrNilContext) {
		t.Fatalf("identity Find(nil) = %v, want %v", err, ErrNilContext)
	}
}

func TestMapOAuthIdentityError(t *testing.T) {
	err := mapOAuthIdentityError(&pgconn.PgError{Code: "23505", ConstraintName: "oauth_identities_provider_subject_unique"})
	if !errors.Is(err, service.ErrOAuthIdentityConflict) {
		t.Fatalf("duplicate identity error = %v, want %v", err, service.ErrOAuthIdentityConflict)
	}
	if err := mapOAuthIdentityError(sql.ErrNoRows); !errors.Is(err, service.ErrOAuthIdentityNotFound) {
		t.Fatalf("missing identity error = %v, want %v", err, service.ErrOAuthIdentityNotFound)
	}
}
