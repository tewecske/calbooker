//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestOAuthStateRepositoryRoundTripsNullableBindingAndConsumesOnce(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	stateRepository, err := NewOAuthStateRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTokenUser(t, harness.DB, "oauth-state@example.test")
	anonymous := service.OAuthState{State: "anonymous-oauth-state", Provider: "Google", Action: "sign-in", CodeVerifier: "pkce-verifier", CreatedAt: 100, ExpiresAt: 200}
	linked := service.OAuthState{State: "linked-oauth-state", Provider: "google", Action: "link", UserID: userID, CreatedAt: 100, ExpiresAt: 200}
	if err := stateRepository.CreateOAuthState(context.Background(), anonymous); err != nil {
		t.Fatalf("CreateOAuthState(anonymous) error = %v", err)
	}
	if err := stateRepository.CreateOAuthState(context.Background(), linked); err != nil {
		t.Fatalf("CreateOAuthState(linked) error = %v", err)
	}

	consumed, err := stateRepository.ConsumeOAuthState(context.Background(), anonymous.State, 150)
	if err != nil {
		t.Fatalf("ConsumeOAuthState(anonymous) error = %v", err)
	}
	if consumed.UserID != 0 || consumed.Provider != "google" || consumed.Action != "sign-in" || consumed.CodeVerifier != "pkce-verifier" {
		t.Fatalf("anonymous state binding = user %d/provider %q/action %q/verifier %q", consumed.UserID, consumed.Provider, consumed.Action, consumed.CodeVerifier)
	}
	if consumed.State != anonymous.State {
		t.Fatalf("consumed state = %q, want the caller's raw state back", consumed.State)
	}
	var storedState string
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT state FROM oauth_states WHERE user_id IS NULL").Scan(&storedState); err != nil || storedState != sessionDigest(anonymous.State) {
		t.Fatalf("stored state = %q (%v), want only its digest", storedState, err)
	}
	if _, err := stateRepository.ConsumeOAuthState(context.Background(), anonymous.State, 151); !errors.Is(err, service.ErrOAuthStateInvalid) {
		t.Fatalf("reused state error = %v, want %v", err, service.ErrOAuthStateInvalid)
	}
	linkedConsumed, err := stateRepository.ConsumeOAuthState(context.Background(), linked.State, 150)
	if err != nil {
		t.Fatalf("ConsumeOAuthState(linked) error = %v", err)
	}
	if linkedConsumed.UserID != userID || linkedConsumed.Provider != "google" || linkedConsumed.Action != "link" {
		t.Fatalf("linked state binding = user %d/provider %q/action %q", linkedConsumed.UserID, linkedConsumed.Provider, linkedConsumed.Action)
	}

	expired := service.OAuthState{State: "expired-oauth-state", Provider: "google", Action: "sign-in", CreatedAt: 100, ExpiresAt: 200}
	if err := stateRepository.CreateOAuthState(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	if _, err := stateRepository.ConsumeOAuthState(context.Background(), expired.State, expired.ExpiresAt); !errors.Is(err, service.ErrOAuthStateInvalid) {
		t.Fatalf("expired state error = %v, want %v", err, service.ErrOAuthStateInvalid)
	}
}

func TestOAuthStateConsumptionAllowsOneConcurrentWinner(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	stateRepository, err := NewOAuthStateRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	state := service.OAuthState{State: "concurrent-oauth-state", Provider: "google", Action: "sign-in", CreatedAt: 100, ExpiresAt: 200}
	if err := stateRepository.CreateOAuthState(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for timestamp := int64(150); timestamp <= 151; timestamp++ {
		wait.Add(1)
		go func(now int64) {
			defer wait.Done()
			_, consumeErr := stateRepository.ConsumeOAuthState(context.Background(), state.State, now)
			results <- consumeErr
		}(timestamp)
	}
	wait.Wait()
	close(results)

	var successes, invalid int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, service.ErrOAuthStateInvalid):
			invalid++
		default:
			t.Fatalf("concurrent state consume error = %v", err)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent state results = successes %d, invalid %d, want 1/1", successes, invalid)
	}
}

func TestOAuthIdentityRepositoryFindListAndConflict(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	identityRepository, err := NewOAuthIdentityRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	firstUserID := insertTokenUser(t, harness.DB, "oauth-first@example.test")
	first := service.OAuthIdentity{UserID: firstUserID, Provider: "Google", Subject: "stable-subject", CreatedAt: 100}
	if err := identityRepository.CreateOAuthIdentity(context.Background(), first); err != nil {
		t.Fatalf("CreateOAuthIdentity() error = %v", err)
	}
	if err := identityRepository.CreateOAuthIdentity(context.Background(), first); !errors.Is(err, service.ErrOAuthIdentityConflict) {
		t.Fatalf("duplicate identity error = %v, want %v", err, service.ErrOAuthIdentityConflict)
	}

	found, err := identityRepository.FindOAuthIdentity(context.Background(), " google ", "stable-subject")
	if err != nil {
		t.Fatalf("FindOAuthIdentity() error = %v", err)
	}
	if found.Provider != "google" || found.Email != "" || found.Subject != "stable-subject" {
		t.Fatalf("found identity provider/email do not match expected values: %q/%q", found.Provider, found.Email)
	}
	if _, err := identityRepository.FindOAuthIdentity(context.Background(), "google", "missing-subject"); !errors.Is(err, service.ErrOAuthIdentityNotFound) {
		t.Fatalf("missing identity error = %v, want %v", err, service.ErrOAuthIdentityNotFound)
	}
	identities, err := identityRepository.ListOAuthIdentities(context.Background(), firstUserID)
	if err != nil {
		t.Fatalf("ListOAuthIdentities() error = %v", err)
	}
	if len(identities) != 1 || identities[0].UserID != firstUserID {
		t.Fatalf("listed identities = %d, want one owned identity", len(identities))
	}
}

func TestOAuthIdentityDeletionCascadesWithUserDeletion(t *testing.T) {
	harness := newUserRepositoryHarness(t)
	identityRepository, err := NewOAuthIdentityRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	userID := insertTokenUser(t, harness.DB, "oauth-cascade@example.test")
	identity := service.OAuthIdentity{UserID: userID, Provider: "google", Subject: "cascade-subject", CreatedAt: 100}
	if err := identityRepository.CreateOAuthIdentity(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.DB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM oauth_identities WHERE user_id = $1", userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("identity count after user deletion = %d, want 0", count)
	}
}
