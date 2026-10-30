//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

func TestGuestClaimRepositoryLifecycleAndUniformRevocation(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertGuest(t, harness.DB, 100)
	claim := service.GuestClaimCode{UserID: userID, Code: "ABCD2345", CreatedAt: 100}
	created, err := repository.CreateGuestClaimCode(context.Background(), claim)
	if err != nil {
		t.Fatalf("CreateGuestClaimCode() error = %v", err)
	}
	if created.UserID != userID || created.Code != claim.Code || created.RevokedAt != nil {
		t.Fatalf("created claim metadata does not match expected values: user %d/revoked %v", created.UserID, created.RevokedAt)
	}

	active, err := repository.FindActiveGuestClaimCode(context.Background(), userID)
	if err != nil {
		t.Fatalf("FindActiveGuestClaimCode() error = %v", err)
	}
	if active.Code != claim.Code {
		t.Fatalf("active claim code does not match issued code")
	}
	byCode, err := repository.FindGuestClaimCodeByCode(context.Background(), claim.Code)
	if err != nil || byCode.UserID != userID {
		t.Fatalf("FindGuestClaimCodeByCode() = user %d, error %v", byCode.UserID, err)
	}
	if err := repository.MarkGuestClaimCodeUsed(context.Background(), claim.Code, 150); err != nil {
		t.Fatalf("MarkGuestClaimCodeUsed() error = %v", err)
	}
	if err := repository.MarkGuestClaimCodeUsed(context.Background(), claim.Code, 140); err != nil {
		t.Fatalf("older MarkGuestClaimCodeUsed() error = %v", err)
	}
	used, err := repository.FindGuestClaimCodeByCode(context.Background(), claim.Code)
	if err != nil {
		t.Fatal(err)
	}
	if used.LastUsedAt == nil || *used.LastUsedAt != 150 {
		t.Fatalf("last-used timestamp = %v, want 150", used.LastUsedAt)
	}

	if err := repository.RevokeGuestClaimCode(context.Background(), userID, 175); err != nil {
		t.Fatalf("RevokeGuestClaimCode() error = %v", err)
	}
	if err := repository.RevokeGuestClaimCode(context.Background(), userID, 176); err != nil {
		t.Fatalf("repeated RevokeGuestClaimCode() error = %v", err)
	}
	if _, err := repository.FindActiveGuestClaimCode(context.Background(), userID); !errors.Is(err, service.ErrGuestClaimCodeNotFound) {
		t.Fatalf("revoked active lookup error = %v, want %v", err, service.ErrGuestClaimCodeNotFound)
	}
	if _, err := repository.FindGuestClaimCodeByCode(context.Background(), claim.Code); !errors.Is(err, service.ErrGuestClaimCodeNotFound) {
		t.Fatalf("revoked code lookup error = %v, want %v", err, service.ErrGuestClaimCodeNotFound)
	}
	if err := repository.MarkGuestClaimCodeUsed(context.Background(), claim.Code, 180); !errors.Is(err, service.ErrGuestClaimCodeNotFound) {
		t.Fatalf("revoked code mark-used error = %v, want %v", err, service.ErrGuestClaimCodeNotFound)
	}

	newClaim := claim
	newClaim.Code = "EFGH5678"
	if _, err := repository.CreateGuestClaimCode(context.Background(), newClaim); err != nil {
		t.Fatalf("create replacement claim error = %v", err)
	}
	if _, err := repository.CreateGuestClaimCode(context.Background(), service.GuestClaimCode{UserID: userID, Code: "JKLM6789", CreatedAt: 101}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active claim error = %v, want %v", err, ErrConflict)
	}
	if _, err := repository.FindGuestClaimCodeByCode(context.Background(), "unknown-code"); !errors.Is(err, service.ErrGuestClaimCodeNotFound) {
		t.Fatalf("unknown code error = %v, want %v", err, service.ErrGuestClaimCodeNotFound)
	}
}

func TestGuestClaimRepositoryConcurrentActiveCodeCreationHasOneWinner(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertGuest(t, harness.DB, 100)

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for index, code := range []string{"QRST2345", "VWXYZ678"} {
		wait.Add(1)
		go func(index int, code string) {
			defer wait.Done()
			_, createErr := repository.CreateGuestClaimCode(context.Background(), service.GuestClaimCode{UserID: userID, Code: code, CreatedAt: int64(100 + index)})
			results <- createErr
		}(index, code)
	}
	wait.Wait()
	close(results)

	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent claim creation error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent claim results = successes %d, conflicts %d, want 1/1", successes, conflicts)
	}
}

func TestGuestCleanupDeletesOnlyOldEmptyGuests(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	oldEmpty := insertGuest(t, harness.DB, 100)
	boundary := insertGuest(t, harness.DB, 200)
	recent := insertGuest(t, harness.DB, 300)
	memberGuest := insertGuest(t, harness.DB, 100)
	groupCreator := insertGuest(t, harness.DB, 100)
	if _, err := harness.DB.ExecContext(context.Background(), "INSERT INTO guest_claim_codes (user_id, code, created_at) VALUES ($1, 'cleanup-code', 100)", oldEmpty); err != nil {
		t.Fatalf("insert old guest claim: %v", err)
	}
	if _, err := harness.DB.ExecContext(context.Background(), "INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES ('cleanup-session', $1, 100, 200)", oldEmpty); err != nil {
		t.Fatalf("insert old guest session: %v", err)
	}
	if _, err := harness.DB.ExecContext(context.Background(), "INSERT INTO groups (name, slug, name_norm, created_by, created_at) VALUES ('Member Group', 'member-group', 'member group', $1, 100)", memberGuest); err != nil {
		t.Fatal(err)
	}
	var groupID int64
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT id FROM groups WHERE name_norm = 'member group'").Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.DB.ExecContext(context.Background(), "INSERT INTO group_members (group_id, user_id, created_at) VALUES ($1, $2, 100)", groupID, memberGuest); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.DB.ExecContext(context.Background(), "INSERT INTO groups (name, slug, name_norm, created_by, created_at) VALUES ('Creator Group', 'creator-group', 'creator group', $1, 100)", groupCreator); err != nil {
		t.Fatal(err)
	}

	deleted, err := repository.DeleteEmptyAbandonedGuests(context.Background(), 200)
	if err != nil {
		t.Fatalf("DeleteEmptyAbandonedGuests() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted guests = %d, want old empty and old group creator", deleted)
	}
	for _, userID := range []int64{oldEmpty, groupCreator} {
		var count int
		if err := harness.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM users WHERE id = $1", userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("deleted guest %d remains", userID)
		}
	}
	for _, userID := range []int64{boundary, recent, memberGuest} {
		var count int
		if err := harness.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM users WHERE id = $1", userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("protected guest %d count = %d, want 1", userID, count)
		}
	}
	var creator sql.NullInt64
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT created_by FROM groups WHERE name_norm = 'creator group'").Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if creator.Valid {
		t.Fatalf("shared group creator = %d, want NULL", creator.Int64)
	}
}

func TestGuestCleanupWaitsForConcurrentMembershipInsert(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	guestID := insertGuest(t, harness.DB, 100)
	var groupID int64
	if err := harness.DB.QueryRowContext(context.Background(), "INSERT INTO groups (name, slug, name_norm, created_at) VALUES ('Race Group', 'race-group', 'race group', 100) RETURNING id").Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	tx, err := harness.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), "INSERT INTO group_members (group_id, user_id, created_at) VALUES ($1, $2, 100)", groupID, guestID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	var transactionPID int
	if err := tx.QueryRowContext(context.Background(), "SELECT pg_backend_pid()").Scan(&transactionPID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}

	done := make(chan struct{})
	var deleted int
	var cleanupErr error
	go func() {
		deleted, cleanupErr = repository.DeleteEmptyAbandonedGuests(context.Background(), 200)
		close(done)
	}()
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	lockObserved := false
	for !lockObserved {
		select {
		case <-done:
			_ = tx.Rollback()
			t.Fatal("cleanup completed before concurrent membership committed")
		case <-deadline.C:
			_ = tx.Rollback()
			t.Fatal("cleanup lock was not observed")
		case <-ticker.C:
			if err := harness.DB.QueryRowContext(context.Background(), `
				SELECT EXISTS (
					SELECT 1
					FROM pg_locks AS lock
					JOIN pg_class AS relation ON relation.oid = lock.relation
					JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
					WHERE relation.relname = 'users'
					  AND namespace.nspname = $2
					  AND lock.mode = 'RowShareLock'
					  AND lock.granted
					  AND lock.pid <> $1
				)
			`, transactionPID, harness.Schema).Scan(&lockObserved); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not finish after membership commit")
	}
	if cleanupErr != nil {
		t.Fatalf("cleanup error = %v", cleanupErr)
	}
	if deleted != 0 {
		t.Fatalf("deleted guests = %d, want 0", deleted)
	}
}

func TestGuestClaimRepositoryPropagatesCancellation(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	repository, err := NewGuestClaimCodeRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.FindGuestClaimCodeByCode(ctx, "cancelled-code"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled guest lookup error = %v, want %v", err, context.Canceled)
	}
}

func insertGuest(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, createdAt int64) int64 {
	t.Helper()
	var userID int64
	if err := db.QueryRowContext(context.Background(), "INSERT INTO users (kind, theme, locale, created_at) VALUES ('guest', 'light', 'en', $1) RETURNING id", createdAt).Scan(&userID); err != nil {
		t.Fatalf("insert guest: %v", err)
	}
	return userID
}
