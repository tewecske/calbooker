package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/locale"
)

// SSOProviderName is the identity provider name of the single sign-on issuer
// in oauth_identities and OAuth flow state.
const SSOProviderName = "sso"

var (
	// ErrSSOUnavailable identifies an issuer that cannot be reached.
	ErrSSOUnavailable = errors.New("service: single sign-on unavailable")
	// ErrNilSSORelyingParty identifies missing OpenID Connect client access.
	ErrNilSSORelyingParty = errors.New("service: nil single sign-on relying party")
	// ErrSSOSignInFailed identifies a callback that cannot sign anyone in.
	ErrSSOSignInFailed = errors.New("service: single sign-on sign-in failed")
	// ErrSSOLogoutTokenInvalid identifies a back-channel logout token that
	// failed verification.
	ErrSSOLogoutTokenInvalid = errors.New("service: invalid single sign-on logout token")
	// ErrSSOGuestIdentityTaken identifies a guest upgrade whose issuer identity
	// already has an account here; accounts are never merged.
	ErrSSOGuestIdentityTaken = errors.New("service: single sign-on identity already has an account")
)

// SSOCallbackResult is the session a callback created. Upgraded reports that
// the callback converted the signed-in guest instead of signing someone in.
type SSOCallbackResult struct {
	Session  Session
	Upgraded bool
}

// SSOGuestUpgradeRepository converts a guest and links its issuer identity in
// one transaction, revoking the guest's transfer code, so a failure can never
// leave a converted account without a way to sign in.
type SSOGuestUpgradeRepository interface {
	UpgradeGuestWithIdentity(ctx context.Context, guest User, expectedVersion int64, identity OAuthIdentity, revokedAt int64) (User, error)
}

// SSOIdentity is the verified identity a client app receives from the issuer.
// IDToken is kept only as the sign-out hint and is never logged.
type SSOIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Locale        string
	SessionID     string
	IDToken       string
}

// SSORelyingParty is the OpenID Connect client port of a client app.
type SSORelyingParty interface {
	AuthorizationURL(ctx context.Context, state, codeChallenge string) (string, error)
	// Exchange redeems a code and returns the identity from a verified ID token.
	Exchange(ctx context.Context, code, codeVerifier string) (SSOIdentity, error)
	// VerifyLogoutToken verifies a back-channel logout token and returns its sid.
	VerifyLogoutToken(ctx context.Context, token string) (string, error)
	// EndSessionURL builds the issuer's logout URL for an ID token hint.
	EndSessionURL(ctx context.Context, idTokenHint, postLogoutRedirect string) (string, error)
}

// SSOClientSessionRepository finds and revokes sessions by their Hydra binding.
type SSOClientSessionRepository interface {
	RevokeSSOSessions(ctx context.Context, sid string, revokedAt int64) (int, error)
	FindSSOIDToken(ctx context.Context, sessionID string) (string, error)
}

// SSOClientDependencies are the ports of client-mode sign-in.
type SSOClientDependencies struct {
	RelyingParty  SSORelyingParty
	States        OAuthStateRepository
	Identities    OAuthIdentityRepository
	Users         UserRepository
	Sessions      *SessionService
	SSOSessions   SSOClientSessionRepository
	GuestUpgrades SSOGuestUpgradeRepository
	StateLifetime time.Duration
	// PostLogoutURL is the registered page the issuer returns to after
	// sign-out.
	PostLogoutURL string
}

// SSOClientService signs visitors in to a client app through the issuer,
// upgrades guests into issuer accounts, and ends sessions when the issuer says
// so. The issuer owns credentials; the app keeps a local account per issuer
// subject for its own data.
type SSOClientService struct {
	relyingParty  SSORelyingParty
	states        OAuthStateRepository
	identities    OAuthIdentityRepository
	users         UserRepository
	sessions      *SessionService
	ssoSessions   SSOClientSessionRepository
	guestUpgrades SSOGuestUpgradeRepository
	stateLife     time.Duration
	postLogoutURL string
	now           func() time.Time
}

// NewSSOClientService constructs client-mode sign-in.
func NewSSOClientService(deps SSOClientDependencies) (*SSOClientService, error) {
	if deps.RelyingParty == nil {
		return nil, ErrNilSSORelyingParty
	}
	if deps.States == nil || deps.Identities == nil || deps.Users == nil || deps.Sessions == nil || deps.SSOSessions == nil || deps.GuestUpgrades == nil || deps.StateLifetime < time.Second || !safeOAuthURL(deps.PostLogoutURL) {
		return nil, ErrOAuthProviderUnavailable
	}
	return &SSOClientService{
		relyingParty:  deps.RelyingParty,
		states:        deps.States,
		identities:    deps.Identities,
		users:         deps.Users,
		sessions:      deps.Sessions,
		ssoSessions:   deps.SSOSessions,
		guestUpgrades: deps.GuestUpgrades,
		stateLife:     deps.StateLifetime,
		postLogoutURL: deps.PostLogoutURL,
		now:           time.Now,
	}, nil
}

// Start creates a one-time state with a PKCE verifier and returns the issuer's
// authorization URL and the state, which the caller binds to the browser.
func (s *SSOClientService) Start(ctx context.Context) (string, string, error) {
	return s.start(ctx, oauthActionSignIn, 0)
}

// StartUpgrade begins the issuer flow that turns the signed-in guest into an
// issuer account. The state is bound to the guest, so only that guest's
// browser can complete it.
func (s *SSOClientService) StartUpgrade(ctx context.Context, guestID int64) (string, string, error) {
	if guestID <= 0 {
		return "", "", ErrInvalidUserID
	}
	guest, err := s.users.FindUserByID(ctx, guestID)
	if err != nil || guest.ID != guestID || !guest.IsGuest {
		return "", "", ErrGuestUpgradeRequiresGuest
	}
	return s.start(ctx, oauthActionLink, guestID)
}

func (s *SSOClientService) start(ctx context.Context, action string, userID int64) (string, string, error) {
	state, err := newOAuthState()
	if err != nil {
		return "", "", err
	}
	verifier, challenge, err := newPKCEVerifier()
	if err != nil {
		return "", "", err
	}
	createdAt := s.now().Unix()
	authorizationURL, err := s.relyingParty.AuthorizationURL(ctx, state, challenge)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrSSOUnavailable, err)
	}
	if !safeOAuthURL(authorizationURL) {
		return "", "", ErrSSOSignInFailed
	}
	if err := s.states.CreateOAuthState(ctx, OAuthState{
		State:        state,
		Provider:     SSOProviderName,
		Action:       action,
		UserID:       userID,
		CodeVerifier: verifier,
		CreatedAt:    createdAt,
		ExpiresAt:    createdAt + int64(s.stateLife/time.Second),
	}); err != nil {
		return "", "", fmt.Errorf("store sign-in state: %w", err)
	}
	return authorizationURL, state, nil
}

// Callback consumes the state, redeems the code, and either signs the issuer
// subject in to its local account or, for an upgrade state, converts the guest
// that started the flow. actorUserID is the account signed in to the browser
// presenting the callback, or zero; an upgrade completes only for that guest.
func (s *SSOClientService) Callback(ctx context.Context, actorUserID int64, code, rawState string) (SSOCallbackResult, error) {
	if strings.TrimSpace(code) == "" || len(code) > maxOAuthCodeLength {
		return SSOCallbackResult{}, ErrSSOSignInFailed
	}
	state, err := normalizeOAuthState(rawState)
	if err != nil {
		return SSOCallbackResult{}, ErrOAuthStateInvalid
	}
	now := s.now().Unix()
	stored, err := s.states.ConsumeOAuthState(ctx, state, now)
	if err != nil || stored.Provider != SSOProviderName || stored.ExpiresAt <= now || stored.CodeVerifier == "" {
		return SSOCallbackResult{}, ErrOAuthStateInvalid
	}
	switch stored.Action {
	case oauthActionSignIn:
		if stored.UserID != 0 {
			return SSOCallbackResult{}, ErrOAuthStateInvalid
		}
	case oauthActionLink:
		if stored.UserID <= 0 || stored.UserID != actorUserID {
			return SSOCallbackResult{}, ErrOAuthStateInvalid
		}
	default:
		return SSOCallbackResult{}, ErrOAuthStateInvalid
	}
	identity, err := s.relyingParty.Exchange(ctx, code, stored.CodeVerifier)
	if err != nil {
		return SSOCallbackResult{}, fmt.Errorf("%w: %w", ErrSSOSignInFailed, err)
	}
	if strings.TrimSpace(identity.Subject) == "" || strings.TrimSpace(identity.SessionID) == "" || identity.IDToken == "" {
		return SSOCallbackResult{}, ErrSSOSignInFailed
	}
	if stored.Action == oauthActionLink {
		return s.upgradeGuest(ctx, stored.UserID, identity, now)
	}
	user, err := s.account(ctx, identity, now)
	if err != nil {
		return SSOCallbackResult{}, err
	}
	session, err := s.sessions.CreateSSO(ctx, user.ID, identity.SessionID, identity.IDToken)
	if err != nil {
		return SSOCallbackResult{}, fmt.Errorf("create session: %w", err)
	}
	return SSOCallbackResult{Session: session}, nil
}

// upgradeGuest turns the guest into the account of the issuer identity,
// keeping its ID and therefore all of its data. Every guest session, including
// ones opened elsewhere with a transfer code, ends; this browser continues on
// a new session bound to the issuer.
func (s *SSOClientService) upgradeGuest(ctx context.Context, guestID int64, identity SSOIdentity, now int64) (SSOCallbackResult, error) {
	guest, err := s.users.FindUserByID(ctx, guestID)
	if err != nil || guest.ID != guestID || !guest.IsGuest {
		return SSOCallbackResult{}, ErrGuestUpgradeRequiresGuest
	}
	if _, err := s.identities.FindOAuthIdentity(ctx, SSOProviderName, identity.Subject); err == nil {
		return SSOCallbackResult{}, ErrSSOGuestIdentityTaken
	} else if !errors.Is(err, ErrOAuthIdentityNotFound) {
		return SSOCallbackResult{}, fmt.Errorf("find identity: %w", err)
	}
	email, err := NormalizeEmail(identity.Email)
	if err != nil {
		email = ""
	}
	upgraded := guest
	upgraded.IsGuest = false
	if email != "" {
		if existing, err := s.users.FindUserByEmail(ctx, email); err == nil && existing.ID > 0 {
			return SSOCallbackResult{}, ErrOAuthEmailConflict
		} else if err != nil && !errors.Is(err, ErrUserNotFound) {
			return SSOCallbackResult{}, fmt.Errorf("find account by email: %w", err)
		}
		upgraded.Email = &email
		if identity.EmailVerified {
			upgraded.EmailVerifiedAt = &now
		}
	}
	if name, err := normalizeDisplayName(identity.Name); err == nil && name != "" && upgraded.DisplayName == nil {
		upgraded.DisplayName = &name
	}
	saved, err := s.guestUpgrades.UpgradeGuestWithIdentity(ctx, upgraded, guest.Version,
		OAuthIdentity{UserID: guestID, Provider: SSOProviderName, Subject: identity.Subject, Email: email, CreatedAt: now}, now)
	switch {
	case errors.Is(err, ErrDuplicateEmail):
		return SSOCallbackResult{}, ErrOAuthEmailConflict
	case errors.Is(err, ErrOAuthIdentityConflict):
		return SSOCallbackResult{}, ErrSSOGuestIdentityTaken
	case err != nil:
		return SSOCallbackResult{}, fmt.Errorf("upgrade guest: %w", err)
	}
	if saved.ID != guestID || saved.IsGuest {
		return SSOCallbackResult{}, ErrInvalidUpgradedUser
	}
	if err := s.sessions.RevokeUser(ctx, guestID); err != nil {
		return SSOCallbackResult{}, fmt.Errorf("revoke guest sessions: %w", err)
	}
	session, err := s.sessions.CreateSSO(ctx, guestID, identity.SessionID, identity.IDToken)
	if err != nil {
		return SSOCallbackResult{}, fmt.Errorf("create session: %w", err)
	}
	return SSOCallbackResult{Session: session, Upgraded: true}, nil
}

// BackChannelLogout verifies the issuer's logout token and revokes every local
// session bound to its sid. Repeated delivery is harmless.
func (s *SSOClientService) BackChannelLogout(ctx context.Context, logoutToken string) error {
	sid, err := s.relyingParty.VerifyLogoutToken(ctx, logoutToken)
	if err != nil || strings.TrimSpace(sid) == "" {
		return ErrSSOLogoutTokenInvalid
	}
	if _, err := s.ssoSessions.RevokeSSOSessions(ctx, sid, s.now().Unix()); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

// SignOutURL returns the issuer logout URL for a local session, or "" when the
// session has no issuer binding. Call it before revoking the local session.
func (s *SSOClientService) SignOutURL(ctx context.Context, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", nil
	}
	idToken, err := s.ssoSessions.FindSSOIDToken(ctx, sessionID)
	if err != nil || idToken == "" {
		return "", nil
	}
	return s.relyingParty.EndSessionURL(ctx, idToken, s.postLogoutURL)
}

// account returns the local account for the issuer subject, creating it on
// first sign-in and refreshing issuer-owned profile data afterwards.
func (s *SSOClientService) account(ctx context.Context, identity SSOIdentity, now int64) (User, error) {
	email, err := NormalizeEmail(identity.Email)
	if err != nil {
		email = ""
	}
	existing, err := s.identities.FindOAuthIdentity(ctx, SSOProviderName, identity.Subject)
	if err == nil {
		user, err := s.users.FindUserByID(ctx, existing.UserID)
		if err != nil || user.ID != existing.UserID {
			return User{}, ErrSSOSignInFailed
		}
		return s.refresh(ctx, user, identity, email, now), nil
	}
	if !errors.Is(err, ErrOAuthIdentityNotFound) {
		return User{}, fmt.Errorf("find identity: %w", err)
	}

	var user User
	if email != "" {
		found, err := s.users.FindUserByEmail(ctx, email)
		switch {
		case err == nil && found.ID > 0:
			// A local account from before this app used single sign-on is
			// linked only when both sides vouch for the address.
			if !identity.EmailVerified || found.EmailVerifiedAt == nil || found.IsGuest {
				return User{}, ErrOAuthEmailConflict
			}
			user = found
		case err != nil && !errors.Is(err, ErrUserNotFound):
			return User{}, fmt.Errorf("find account by email: %w", err)
		}
	}
	if user.ID == 0 {
		created, err := s.users.CreateUser(ctx, newSSOUser(identity, email, now))
		if err != nil {
			return User{}, fmt.Errorf("create account: %w", err)
		}
		user = created
	}
	if err := s.identities.CreateOAuthIdentity(ctx, OAuthIdentity{UserID: user.ID, Provider: SSOProviderName, Subject: identity.Subject, Email: email, CreatedAt: now}); err != nil {
		return User{}, fmt.Errorf("link identity: %w", err)
	}
	return user, nil
}

func newSSOUser(identity SSOIdentity, email string, now int64) User {
	user := User{Theme: defaultUserTheme, Locale: defaultUserLocale, CreatedAt: now}
	if email != "" {
		user.Email = &email
		if identity.EmailVerified {
			user.EmailVerifiedAt = &now
		}
	}
	if name, err := normalizeDisplayName(identity.Name); err == nil && name != "" {
		user.DisplayName = &name
	}
	if locale.Supported(locale.Code(identity.Locale)) {
		user.Locale = identity.Locale
	}
	return user
}

// refresh copies issuer-owned email and name onto the local account. A
// failure (for example an email another local account holds) keeps the old
// values: it must not block sign-in.
func (s *SSOClientService) refresh(ctx context.Context, user User, identity SSOIdentity, email string, now int64) User {
	updated := user
	changed := false
	if email != "" && (user.Email == nil || *user.Email != email) {
		updated.Email = &email
		updated.EmailVerifiedAt = nil
		changed = true
	}
	if email != "" && identity.EmailVerified && updated.EmailVerifiedAt == nil {
		updated.EmailVerifiedAt = &now
		changed = true
	}
	if name, err := normalizeDisplayName(identity.Name); err == nil && name != "" && (user.DisplayName == nil || *user.DisplayName != name) {
		updated.DisplayName = &name
		changed = true
	}
	if !changed {
		return user
	}
	saved, err := s.users.UpdateUser(ctx, updated, user.Version)
	if err != nil {
		return user
	}
	return saved
}
