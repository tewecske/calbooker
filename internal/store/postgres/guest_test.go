package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestGuestClaimRepositoryConstructor(t *testing.T) {
	if _, err := NewGuestClaimCodeRepository(nil); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("NewGuestClaimCodeRepository(nil) error = %v, want %v", err, ErrNilDatabase)
	}
	if repository, err := NewGuestClaimCodeRepository(&sql.DB{}); err != nil || repository == nil {
		t.Fatalf("NewGuestClaimCodeRepository() = %v, %v", repository, err)
	}
}

func TestMapGuestClaimError(t *testing.T) {
	if err := mapGuestClaimError(sql.ErrNoRows); !errors.Is(err, service.ErrGuestClaimCodeNotFound) {
		t.Fatalf("missing code error = %v, want service sentinel", err)
	}
	if err := mapGuestClaimError(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context canceled", err)
	}
}
