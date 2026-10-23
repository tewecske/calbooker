//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestSessionRepositoryRoundTripsExpiredAndRevokedState(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTokenUser(t, harness.DB, "session@example.test")
	session := service.Session{ID: "session-round-trip", UserID: userID, CreatedAt: 100, ExpiresAt: 200}
	if err := repository.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	found, err := repository.FindSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("FindSession() error = %v", err)
	}
	if found.ID != session.ID || found.UserID != userID || found.RevokedAt != nil {
		t.Fatalf("found session metadata does not match expected values: user %d/revoked %v", found.UserID, found.RevokedAt)
	}
	if _, err := repository.FindSession(context.Background(), "missing-session"); !errors.Is(err, service.ErrSessionNotFound) {
		t.Fatalf("missing session error = %v, want %v", err, service.ErrSessionNotFound)
	}

	if err := repository.RevokeSession(context.Background(), session.ID, 150); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if err := repository.RevokeSession(context.Background(), session.ID, 175); err != nil {
		t.Fatalf("repeated RevokeSession() error = %v", err)
	}
	revoked, err := repository.FindSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == nil || *revoked.RevokedAt != 150 {
		t.Fatalf("revoked timestamp = %v, want 150", revoked.RevokedAt)
	}

	expired := service.Session{ID: "session-expired", UserID: userID, CreatedAt: 100, ExpiresAt: 101}
	if err := repository.CreateSession(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	storedExpired, err := repository.FindSession(context.Background(), expired.ID)
	if err != nil || storedExpired.ExpiresAt != 101 {
		t.Fatalf("expired session lookup = expires %d/error %v", storedExpired.ExpiresAt, err)
	}
	if err := repository.CreateSession(context.Background(), session); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate session error = %v, want %v", err, ErrConflict)
	}
	var storedID string
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT id FROM sessions WHERE user_id = $1", userID).Scan(&storedID); err != nil {
		t.Fatal(err)
	}
	if storedID == session.ID || len(storedID) != 64 {
		t.Fatalf("stored session identifier was not digested")
	}
}

func TestSessionRepositoryMapsForeignKeyAndInvalidInputs(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSession(context.Background(), service.Session{ID: "orphan-session", UserID: 999999, CreatedAt: 100, ExpiresAt: 200}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("orphan session error = %v, want %v", err, ErrInvalidData)
	}
	if err := repository.CreateSession(context.Background(), service.Session{ID: "", UserID: 1, CreatedAt: 100, ExpiresAt: 200}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("empty session ID error = %v, want %v", err, ErrInvalidData)
	}
	if err := repository.RevokeSession(context.Background(), "missing-session", 100); !errors.Is(err, service.ErrSessionNotFound) {
		t.Fatalf("missing revoke error = %v, want %v", err, service.ErrSessionNotFound)
	}
}

func TestSessionRepositoryRevokesAllSessionsConcurrently(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTokenUser(t, harness.DB, "session-revoke@example.test")
	for _, id := range []string{"revoke-one", "revoke-two", "revoke-three"} {
		if err := repository.CreateSession(context.Background(), service.Session{ID: id, UserID: userID, CreatedAt: 100, ExpiresAt: 200}); err != nil {
			t.Fatal(err)
		}
	}

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, timestamp := range []int64{150, 151} {
		wait.Add(1)
		go func(revokedAt int64) {
			defer wait.Done()
			results <- repository.RevokeUserSessions(context.Background(), userID)
		}(timestamp)
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("RevokeUserSessions() error = %v", err)
		}
	}
	var active int
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL", userID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active sessions after concurrent revoke = %d, want 0", active)
	}
}

func TestSessionRepositoryConcurrentSingleRevocationKeepsOneTimestamp(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTokenUser(t, harness.DB, "session-single-revoke@example.test")
	session := service.Session{ID: "single-revoke", UserID: userID, CreatedAt: 100, ExpiresAt: 200}
	if err := repository.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, timestamp := range []int64{150, 151} {
		wait.Add(1)
		go func(revokedAt int64) {
			defer wait.Done()
			results <- repository.RevokeSession(context.Background(), session.ID, revokedAt)
		}(timestamp)
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("RevokeSession() error = %v", err)
		}
	}
	stored, err := repository.FindSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RevokedAt == nil || (*stored.RevokedAt != 150 && *stored.RevokedAt != 151) {
		t.Fatalf("stored revocation timestamp = %v, want 150 or 151", stored.RevokedAt)
	}
}

func TestSessionRepositoryDeletionCascadesAndPropagatesCancellation(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewSessionRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTokenUser(t, harness.DB, "session-delete@example.test")
	session := service.Session{ID: "delete-session-repository", UserID: userID, CreatedAt: 100, ExpiresAt: 200}
	if err := repository.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.DB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM sessions WHERE user_id = $1", userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("session count after account deletion = %d, want 0", count)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.FindSession(ctx, session.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled FindSession() error = %v, want %v", err, context.Canceled)
	}
}
