package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestSessionRepositoryConstructor(t *testing.T) {
	if _, err := NewSessionRepository(nil); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("NewSessionRepository(nil) error = %v, want %v", err, ErrNilDatabase)
	}
	if repository, err := NewSessionRepository(&sql.DB{}); err != nil || repository == nil {
		t.Fatalf("NewSessionRepository() = %v, %v", repository, err)
	}
}

func TestSessionRepositoryRejectsNilContext(t *testing.T) {
	repository, err := NewSessionRepository(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FindSession(nil, "session"); !errors.Is(err, ErrNilContext) {
		t.Fatalf("FindSession(nil) = %v, want %v", err, ErrNilContext)
	}
}

func TestMapSessionError(t *testing.T) {
	if err := mapSessionError(sql.ErrNoRows); !errors.Is(err, service.ErrSessionNotFound) {
		t.Fatalf("missing session error = %v, want %v", err, service.ErrSessionNotFound)
	}
	if err := mapSessionError(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want %v", err, context.Canceled)
	}
}
