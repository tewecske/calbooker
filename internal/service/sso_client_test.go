package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// testSSOState has the shape of a real state: 32 random bytes in hex.
var testSSOState = strings.Repeat("ab", 32)

type relyingPartyStub struct {
	identity    SSOIdentity
	exchangeErr error
	code        string
	verifier    string
	logoutSID   string
	logoutErr   error
	endHint     string
	endRedirect string
}

func (s *relyingPartyStub) AuthorizationURL(_ context.Context, state, challenge string) (string, error) {
	return "https://id.example.test/oauth2/auth?state=" + state + "&code_challenge=" + challenge, nil
}

func (s *relyingPartyStub) Exchange(_ context.Context, code, verifier string) (SSOIdentity, error) {
	s.code, s.verifier = code, verifier
	return s.identity, s.exchangeErr
}

func (s *relyingPartyStub) VerifyLogoutToken(context.Context, string) (string, error) {
	return s.logoutSID, s.logoutErr
}

func (s *relyingPartyStub) EndSessionURL(_ context.Context, hint, redirect string) (string, error) {
	s.endHint, s.endRedirect = hint, redirect
	return "https://id.example.test/oauth2/sessions/logout?hint", nil
}

type ssoClientSessionsStub struct {
	revokedSID string
	revokedAt  int64
	idToken    string
}

func (s *ssoClientSessionsStub) RevokeSSOSessions(_ context.Context, sid string, at int64) (int, error) {
	s.revokedSID, s.revokedAt = sid, at
	return 1, nil
}

func (s *ssoClientSessionsStub) FindSSOIDToken(context.Context, string) (string, error) {
	return s.idToken, nil
}

type ssoClientFixture struct {
	service    *SSOClientService
	rp         *relyingPartyStub
	states     *oauthStateRepositoryStub
	identities *oauthIdentityRepositoryStub
	users      *oauthUserRepositoryStub
	sessions   *sessionRepositoryStub
	ssoStore   *ssoClientSessionsStub
	upgrades   *guestUpgradeRepositoryStub
}

func newSSOClientFixture(t *testing.T) *ssoClientFixture {
	t.Helper()
	f := &ssoClientFixture{
		rp: &relyingPartyStub{identity: SSOIdentity{Subject: "42", Email: " Ada@Example.test ", EmailVerified: true, Name: "Ada", Locale: "hu", SessionID: "sid-1", IDToken: "id.token.value"}},
		states: &oauthStateRepositoryStub{consumed: OAuthState{
			State: testSSOState, Provider: SSOProviderName, Action: oauthActionSignIn, CodeVerifier: "pkce-verifier", ExpiresAt: 2_000,
		}},
		identities: &oauthIdentityRepositoryStub{findErr: ErrOAuthIdentityNotFound},
		users:      &oauthUserRepositoryStub{},
		sessions:   &sessionRepositoryStub{},
		ssoStore:   &ssoClientSessionsStub{},
		upgrades:   &guestUpgradeRepositoryStub{},
	}
	sessionService, err := NewSessionService(f.sessions, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = NewSSOClientService(SSOClientDependencies{
		RelyingParty: f.rp, States: f.states, Identities: f.identities, Users: f.users,
		Sessions: sessionService, SSOSessions: f.ssoStore, GuestUpgrades: f.upgrades,
		StateLifetime: time.Minute, PostLogoutURL: "https://app.example.test/en/sign-in",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service.now = func() time.Time { return time.Unix(1_000, 0) }
	return f
}

func TestSSOClientStartStoresStateWithPKCE(t *testing.T) {
	f := newSSOClientFixture(t)
	target, state, err := f.service.Start(context.Background())
	if err != nil || state == "" || target == "" {
		t.Fatalf("Start() = %q, %q, %v", target, state, err)
	}
	created := f.states.created
	if created.State != state || created.Provider != SSOProviderName || created.Action != oauthActionSignIn || created.CodeVerifier == "" || created.ExpiresAt != 1_060 {
		t.Fatalf("stored state = %+v", created)
	}
}

func TestSSOClientCallbackCreatesAccountAndBoundSession(t *testing.T) {
	f := newSSOClientFixture(t)
	result, err := f.service.Callback(context.Background(), 0, "the-code", testSSOState)
	session := result.Session
	if err != nil || result.Upgraded {
		t.Fatalf("Callback() error = %v", err)
	}
	if f.rp.code != "the-code" || f.rp.verifier != "pkce-verifier" {
		t.Fatalf("exchange used code %q verifier %q, want the stored PKCE verifier", f.rp.code, f.rp.verifier)
	}
	created := f.users.created
	if created.Email == nil || *created.Email != "ada@example.test" || created.EmailVerifiedAt == nil || created.DisplayName == nil || *created.DisplayName != "Ada" || created.Locale != "hu" {
		t.Fatalf("created account = %+v", created)
	}
	if f.identities.created != (OAuthIdentity{UserID: 99, Provider: SSOProviderName, Subject: "42", Email: "ada@example.test", CreatedAt: 1_000}) {
		t.Fatalf("identity = %+v", f.identities.created)
	}
	stored := f.sessions.created
	if stored.UserID != 99 || stored.SSOSessionID != "sid-1" || stored.SSOIDToken != "id.token.value" {
		t.Fatalf("stored session = user %d sid %q token %q", stored.UserID, stored.SSOSessionID, stored.SSOIDToken)
	}
	if session.ID != stored.ID || session.SSOIDToken != "" {
		t.Fatal("returned session must match the stored one and never carry the ID token")
	}
}

func TestSSOClientCallbackKeepsUnverifiedEmailUnverified(t *testing.T) {
	f := newSSOClientFixture(t)
	f.rp.identity.EmailVerified = false
	f.rp.identity.Locale = "xx"
	if _, err := f.service.Callback(context.Background(), 0, "code", testSSOState); err != nil {
		t.Fatal(err)
	}
	if f.users.created.EmailVerifiedAt != nil || f.users.created.Locale != defaultUserLocale {
		t.Fatalf("created = verified %v locale %q, want unverified with the default locale", f.users.created.EmailVerifiedAt, f.users.created.Locale)
	}
}

func TestSSOClientCallbackRefreshesAnExistingAccount(t *testing.T) {
	f := newSSOClientFixture(t)
	old := "old@example.test"
	f.identities = &oauthIdentityRepositoryStub{found: OAuthIdentity{UserID: 7, Provider: SSOProviderName, Subject: "42"}}
	f.service.identities = f.identities
	f.users.byID = User{ID: 7, Email: &old, Version: 3}
	if _, err := f.service.Callback(context.Background(), 0, "code", testSSOState); err != nil {
		t.Fatal(err)
	}
	if f.users.createCalls != 0 || f.identities.createCalls != 0 {
		t.Fatal("an existing identity created a new account or link")
	}
	if f.users.updated.Email == nil || *f.users.updated.Email != "ada@example.test" || f.users.updated.EmailVerifiedAt == nil || *f.users.updated.DisplayName != "Ada" {
		t.Fatalf("refreshed account = %+v", f.users.updated)
	}
	if f.sessions.created.UserID != 7 {
		t.Fatalf("session for user %d, want 7", f.sessions.created.UserID)
	}

	// A failed refresh (for example a taken email) must not block sign-in.
	f.users.updateErr = ErrDuplicateEmail
	if _, err := f.service.Callback(context.Background(), 0, "code", testSSOState); err != nil {
		t.Fatalf("Callback() with a failing refresh error = %v", err)
	}
}

func TestSSOClientCallbackLinksOrRefusesAnEmailOwner(t *testing.T) {
	verifiedAt := int64(10)
	tests := []struct {
		name          string
		localVerified bool
		claimVerified bool
		wantErr       error
	}{
		{name: "both verified link", localVerified: true, claimVerified: true},
		{name: "unverified claim", localVerified: true, claimVerified: false, wantErr: ErrOAuthEmailConflict},
		{name: "unverified local account", localVerified: false, claimVerified: true, wantErr: ErrOAuthEmailConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newSSOClientFixture(t)
			f.rp.identity.EmailVerified = test.claimVerified
			email := "ada@example.test"
			local := User{ID: 5, Email: &email}
			if test.localVerified {
				local.EmailVerifiedAt = &verifiedAt
			}
			f.users.byEmail = local
			_, err := f.service.Callback(context.Background(), 0, "code", testSSOState)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Callback() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if f.identities.createCalls != 0 || f.sessions.created.UserID != 0 {
					t.Fatal("a refused link still created an identity or a session")
				}
				return
			}
			if f.users.createCalls != 0 || f.identities.created.UserID != 5 || f.sessions.created.UserID != 5 {
				t.Fatalf("link = created %d accounts, identity user %d, session user %d; want the existing account 5", f.users.createCalls, f.identities.created.UserID, f.sessions.created.UserID)
			}
		})
	}
}

func TestSSOClientCallbackRejectsBadStateOrIdentity(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*ssoClientFixture)
		state string
	}{
		{name: "malformed state", state: "x"},
		{name: "unknown state", edit: func(f *ssoClientFixture) { f.states.consumeErr = ErrOAuthStateInvalid }},
		{name: "state of another provider", edit: func(f *ssoClientFixture) { f.states.consumed.Provider = "google" }},
		{name: "link state", edit: func(f *ssoClientFixture) { f.states.consumed.Action = oauthActionLink; f.states.consumed.UserID = 3 }},
		{name: "expired state", edit: func(f *ssoClientFixture) { f.states.consumed.ExpiresAt = 1_000 }},
		{name: "failed exchange", edit: func(f *ssoClientFixture) { f.rp.exchangeErr = errors.New("bad token") }},
		{name: "identity without sid", edit: func(f *ssoClientFixture) { f.rp.identity.SessionID = "" }},
		{name: "identity without subject", edit: func(f *ssoClientFixture) { f.rp.identity.Subject = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newSSOClientFixture(t)
			if test.edit != nil {
				test.edit(f)
			}
			state := test.state
			if state == "" {
				state = testSSOState
			}
			if _, err := f.service.Callback(context.Background(), 0, "code", state); err == nil {
				t.Fatal("Callback() succeeded, want failure")
			}
			if f.sessions.created.UserID != 0 {
				t.Fatal("a rejected callback created a session")
			}
		})
	}
}

func TestSSOClientBackChannelLogout(t *testing.T) {
	f := newSSOClientFixture(t)
	f.rp.logoutSID = "sid-9"
	if err := f.service.BackChannelLogout(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
	if f.ssoStore.revokedSID != "sid-9" || f.ssoStore.revokedAt != 1_000 {
		t.Fatalf("revoked %q at %d", f.ssoStore.revokedSID, f.ssoStore.revokedAt)
	}

	f = newSSOClientFixture(t)
	f.rp.logoutErr = errors.New("bad signature")
	if err := f.service.BackChannelLogout(context.Background(), "token"); !errors.Is(err, ErrSSOLogoutTokenInvalid) || f.ssoStore.revokedSID != "" {
		t.Fatalf("invalid token error = %v revoked %q, want rejection without revocation", err, f.ssoStore.revokedSID)
	}
}

func TestSSOClientSignOutURL(t *testing.T) {
	f := newSSOClientFixture(t)
	if target, err := f.service.SignOutURL(context.Background(), "session"); err != nil || target != "" {
		t.Fatalf("SignOutURL(no binding) = %q, %v; want empty", target, err)
	}
	f.ssoStore.idToken = "stored.id.token"
	target, err := f.service.SignOutURL(context.Background(), "session")
	if err != nil || target == "" || f.rp.endHint != "stored.id.token" || f.rp.endRedirect != "https://app.example.test/en/sign-in" {
		t.Fatalf("SignOutURL() = %q, %v (hint %q, redirect %q)", target, err, f.rp.endHint, f.rp.endRedirect)
	}
}

type guestUpgradeRepositoryStub struct {
	calls           int
	guest           User
	expectedVersion int64
	identity        OAuthIdentity
	revokedAt       int64
	err             error
}

func (s *guestUpgradeRepositoryStub) UpgradeGuestWithIdentity(_ context.Context, guest User, expectedVersion int64, identity OAuthIdentity, revokedAt int64) (User, error) {
	s.calls++
	s.guest, s.expectedVersion, s.identity, s.revokedAt = guest, expectedVersion, identity, revokedAt
	if s.err != nil {
		return User{}, s.err
	}
	saved := guest
	saved.Version = expectedVersion + 1
	return saved, nil
}

// upgradeFixture is a fixture whose state was started by guest 5.
func upgradeFixture(t *testing.T) *ssoClientFixture {
	t.Helper()
	f := newSSOClientFixture(t)
	f.states.consumed.Action = oauthActionLink
	f.states.consumed.UserID = 5
	f.users.byID = User{ID: 5, IsGuest: true, Theme: "dark", Locale: "hu", Version: 2}
	return f
}

func TestSSOClientStartUpgradeRequiresAGuest(t *testing.T) {
	f := newSSOClientFixture(t)
	f.users.byID = User{ID: 5, IsGuest: true}
	if _, _, err := f.service.StartUpgrade(context.Background(), 5); err != nil {
		t.Fatalf("StartUpgrade(guest) error = %v", err)
	}
	if f.states.created.Action != oauthActionLink || f.states.created.UserID != 5 || f.states.created.Provider != SSOProviderName {
		t.Fatalf("upgrade state = %+v, want a link state bound to guest 5", f.states.created)
	}

	f = newSSOClientFixture(t)
	f.users.byID = User{ID: 5}
	if _, _, err := f.service.StartUpgrade(context.Background(), 5); !errors.Is(err, ErrGuestUpgradeRequiresGuest) || f.states.created.State != "" {
		t.Fatalf("StartUpgrade(non-guest) error = %v state %+v, want refusal without state", err, f.states.created)
	}
}

func TestSSOClientCallbackUpgradesTheGuestThatStartedIt(t *testing.T) {
	f := upgradeFixture(t)
	result, err := f.service.Callback(context.Background(), 5, "code", testSSOState)
	if err != nil || !result.Upgraded {
		t.Fatalf("Callback() = %+v, %v; want an upgrade", result, err)
	}
	if f.upgrades.calls != 1 || f.users.createCalls != 0 {
		t.Fatalf("upgrade calls %d, accounts created %d; want the guest converted in place", f.upgrades.calls, f.users.createCalls)
	}
	guest := f.upgrades.guest
	if guest.ID != 5 || guest.IsGuest || guest.Email == nil || *guest.Email != "ada@example.test" || guest.EmailVerifiedAt == nil || guest.DisplayName == nil || *guest.DisplayName != "Ada" {
		t.Fatalf("converted guest = %+v", guest)
	}
	// The guest's own preferences survive the upgrade.
	if guest.Theme != "dark" || guest.Locale != "hu" || f.upgrades.expectedVersion != 2 {
		t.Fatalf("converted guest theme %q locale %q version %d, want the guest's dark/hu at version 2", guest.Theme, guest.Locale, f.upgrades.expectedVersion)
	}
	if f.upgrades.identity != (OAuthIdentity{UserID: 5, Provider: SSOProviderName, Subject: "42", Email: "ada@example.test", CreatedAt: 1_000}) || f.upgrades.revokedAt != 1_000 {
		t.Fatalf("identity %+v revokedAt %d", f.upgrades.identity, f.upgrades.revokedAt)
	}
	if f.sessions.revokedUserID != 5 {
		t.Fatal("the guest's other sessions were not revoked")
	}
	if f.sessions.created.UserID != 5 || f.sessions.created.SSOSessionID != "sid-1" || result.Session.ID != f.sessions.created.ID {
		t.Fatalf("new session = user %d sid %q, want guest 5 bound to sid-1", f.sessions.created.UserID, f.sessions.created.SSOSessionID)
	}
}

func TestSSOClientUpgradeRefusals(t *testing.T) {
	tests := []struct {
		name     string
		actor    int64
		edit     func(*ssoClientFixture)
		want     error
		exchange bool
	}{
		{name: "another browser account", actor: 6, want: ErrOAuthStateInvalid},
		{name: "no signed-in account", actor: 0, want: ErrOAuthStateInvalid},
		{name: "account is no longer a guest", actor: 5, exchange: true, want: ErrGuestUpgradeRequiresGuest, edit: func(f *ssoClientFixture) { f.users.byID.IsGuest = false }},
		{name: "identity already has an account", actor: 5, exchange: true, want: ErrSSOGuestIdentityTaken, edit: func(f *ssoClientFixture) {
			f.identities.findErr = nil
			f.identities.found = OAuthIdentity{UserID: 9, Provider: SSOProviderName, Subject: "42"}
		}},
		{name: "email belongs to another account", actor: 5, exchange: true, want: ErrOAuthEmailConflict, edit: func(f *ssoClientFixture) {
			email := "ada@example.test"
			f.users.byEmail = User{ID: 9, Email: &email}
		}},
		{name: "identity linked concurrently", actor: 5, exchange: true, want: ErrSSOGuestIdentityTaken, edit: func(f *ssoClientFixture) { f.upgrades.err = ErrOAuthIdentityConflict }},
		{name: "email taken concurrently", actor: 5, exchange: true, want: ErrOAuthEmailConflict, edit: func(f *ssoClientFixture) { f.upgrades.err = ErrDuplicateEmail }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := upgradeFixture(t)
			if test.edit != nil {
				test.edit(f)
			}
			_, err := f.service.Callback(context.Background(), test.actor, "code", testSSOState)
			if !errors.Is(err, test.want) {
				t.Fatalf("Callback() error = %v, want %v", err, test.want)
			}
			if (f.rp.code != "") != test.exchange {
				t.Fatalf("code exchanged = %t, want %t", f.rp.code != "", test.exchange)
			}
			if f.sessions.created.UserID != 0 || f.sessions.revokedUserID != 0 {
				t.Fatal("a refused upgrade touched sessions")
			}
		})
	}
}
