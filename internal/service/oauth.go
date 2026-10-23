package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"time"
)

const (
	// DefaultOAuthStateLifetime is used by application wiring when no
	// deployment-specific state lifetime is configured.
	DefaultOAuthStateLifetime = 10 * time.Minute
	oauthStateBytes           = 32
	maxOAuthStateLength       = oauthStateBytes * 2
	maxOAuthCodeLength        = 2048
	oauthActionSignIn         = "sign-in"
	oauthActionLink           = "link"
)

var (
	// ErrOAuthProviderUnavailable identifies an absent or unusable provider.
	ErrOAuthProviderUnavailable = errors.New("service: oauth provider unavailable")
	// ErrOAuthStateInvalid identifies missing, expired, or mismatched callback state.
	ErrOAuthStateInvalid = errors.New("service: invalid oauth state")
	// ErrOAuthEmailConflict refuses account merging by reported email.
	ErrOAuthEmailConflict = errors.New("service: oauth email conflict")
	// ErrOAuthIdentityNotFound identifies an unlinked provider subject.
	ErrOAuthIdentityNotFound = errors.New("service: oauth identity not found")
	// ErrOAuthIdentityConflict identifies a concurrent identity attachment.
	ErrOAuthIdentityConflict = errors.New("service: oauth identity conflict")
)

// OAuthState is one short-lived, one-time callback state record.
// CodeVerifier is the PKCE secret that redeems this flow's authorization code.
type OAuthState struct {
	State        string
	Provider     string
	Action       string
	UserID       int64
	CodeVerifier string
	CreatedAt    int64
	ExpiresAt    int64
}

// OAuthStateRepository persists and atomically consumes callback state.
type OAuthStateRepository interface {
	CreateOAuthState(context.Context, OAuthState) error
	ConsumeOAuthState(context.Context, string, int64) (OAuthState, error)
}

// OAuthIdentity is a stable external identity attached to an account.
type OAuthIdentity struct {
	ID        int64
	UserID    int64
	Provider  string
	Subject   string
	Email     string
	CreatedAt int64
}

// OAuthIdentityRepository resolves and creates external identities.
type OAuthIdentityRepository interface {
	FindOAuthIdentity(context.Context, string, string) (OAuthIdentity, error)
	CreateOAuthIdentity(context.Context, OAuthIdentity) error
	ListOAuthIdentities(context.Context, int64) ([]OAuthIdentity, error)
}

func normalizeOAuthState(raw string) (string, error) {
	if len(raw) != maxOAuthStateLength {
		return "", ErrOAuthStateInvalid
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return "", ErrOAuthStateInvalid
	}
	return raw, nil
}

func newOAuthState() (string, error) {
	value := make([]byte, oauthStateBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate oauth state: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// newPKCEVerifier returns an RFC 7636 code verifier and its S256 challenge.
func newPKCEVerifier() (string, string, error) {
	value := make([]byte, oauthStateBytes)
	if _, err := rand.Read(value); err != nil {
		return "", "", fmt.Errorf("generate oauth code verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(value)
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func safeOAuthURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil
}
