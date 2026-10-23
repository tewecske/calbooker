package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

type httpSSOClientStub struct {
	startTarget    string
	startState     string
	callbackErr    error
	callbackCode   string
	callbackState  string
	callbackCalls  int
	logoutToken    string
	logoutErr      error
	signOutURL     string
	signOutCalls   int
	signOutAfter   *httpSignOutStub // set to check ordering against local sign-out
	signOutFirst   bool
	upgradeGuestID int64
	upgradeErr     error
	callbackActor  int64
}

func (s *httpSSOClientStub) Start(context.Context) (string, string, error) {
	return s.startTarget, s.startState, nil
}

func (s *httpSSOClientStub) StartUpgrade(_ context.Context, guestID int64) (string, string, error) {
	s.upgradeGuestID = guestID
	return s.startTarget, s.startState, s.upgradeErr
}

func (s *httpSSOClientStub) Callback(_ context.Context, actor int64, code, state string) (service.SSOCallbackResult, error) {
	s.callbackCalls++
	s.callbackActor = actor
	s.callbackCode, s.callbackState = code, state
	return service.SSOCallbackResult{Session: service.Session{ID: "sso-session"}}, s.callbackErr
}

func (s *httpSSOClientStub) BackChannelLogout(_ context.Context, token string) error {
	s.logoutToken = token
	return s.logoutErr
}

func (s *httpSSOClientStub) SignOutURL(context.Context, string) (string, error) {
	s.signOutCalls++
	if s.signOutAfter != nil {
		s.signOutFirst = !s.signOutAfter.called
	}
	return s.signOutURL, nil
}

func clientModeDependencies(t *testing.T, client *httpSSOClientStub) Dependencies {
	t.Helper()
	dependencies := newHTTPTestDependencies(t)
	dependencies.SSOClient = client
	dependencies.SSOAccountURL = "https://accounts.example.test/en/account/settings"
	return dependencies
}

func TestClientModeRemovesLocalCredentialRoutes(t *testing.T) {
	handler := NewRouterWithServices(clientModeDependencies(t, &httpSSOClientStub{}))
	for _, path := range []string{"/en/sign-up", "/en/forgot-password", "/en/reset-password", "/en/resend-confirmation", "/en/confirm-email", "/confirm-email"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d in client mode", path, response.Code, http.StatusNotFound)
		}
	}
	csrf := csrfCookie(t, handler)
	for _, path := range []string{"/en/sign-in", "/en/account/settings/password", "/en/account/settings/profile"} {
		response := csrfRequest(t, handler, http.MethodPost, path, url.Values{"_csrf": {csrf.Value}, "identifier": {"a"}, "password": {"b"}}, csrf)
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want it unavailable in client mode", path, response.Code)
		}
	}
}

func TestClientModeSignInPageOffersOnlySingleSignOn(t *testing.T) {
	handler := NewRouterWithServices(clientModeDependencies(t, &httpSSOClientStub{}))
	page := serveContract(t, handler, newContractRequest(http.MethodGet, "/en/sign-in?sso=error"))
	page.assertStatus(t, http.StatusOK).
		assertBodyContains(t, `href="/en/oauth/sso"`).
		assertBodyAbsent(t, `type="password"`).
		assertBodyAbsent(t, `/en/sign-up`).
		assertCatalogMessage(t, "en", "sso.error.generic")
}

func TestClientModeStartBindsStateToTheBrowser(t *testing.T) {
	client := &httpSSOClientStub{startTarget: "http://127.0.0.1:4444/oauth2/auth?x=1", startState: strings.Repeat("ab", 32)}
	handler := NewRouterWithServices(clientModeDependencies(t, client))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/oauth/sso", nil))
	if response.Code != http.StatusFound || response.Header().Get("Location") != client.startTarget {
		t.Fatalf("start = %d %q", response.Code, response.Header().Get("Location"))
	}
	if cookie := responseCookie(response, oauthStateCookieName); cookie == nil || cookie.Value != client.startState || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie = %+v", cookie)
	}
}

func TestClientModeCallback(t *testing.T) {
	state := strings.Repeat("ab", 32)
	tests := []struct {
		name        string
		query       string
		cookie      string
		callbackErr error
		wantCalls   int
		wantTarget  string
	}{
		{name: "success", query: "code=c&state=" + state, cookie: state, wantCalls: 1, wantTarget: "/en/home"},
		{name: "state from another browser", query: "code=c&state=" + state, cookie: strings.Repeat("cd", 32), wantTarget: "/en/sign-in?sso=error"},
		{name: "issuer reported an error", query: "error=access_denied&state=" + state, cookie: state, wantTarget: "/en/sign-in?sso=error"},
		{name: "callback failure", query: "code=c&state=" + state, cookie: state, callbackErr: service.ErrSSOSignInFailed, wantCalls: 1, wantTarget: "/en/sign-in?sso=error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &httpSSOClientStub{callbackErr: test.callbackErr}
			handler := NewRouterWithServices(clientModeDependencies(t, client))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, oauthCallbackRequest("/en/oauth/sso/callback?"+test.query, test.cookie))
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.wantTarget {
				t.Fatalf("callback = %d %q, want %q", response.Code, response.Header().Get("Location"), test.wantTarget)
			}
			if client.callbackCalls != test.wantCalls {
				t.Fatalf("Callback calls = %d, want %d", client.callbackCalls, test.wantCalls)
			}
			session := responseCookie(response, middleware.DefaultSessionCookieName)
			if test.wantTarget == "/en/home" {
				if session == nil || session.Value != "sso-session" || client.callbackCode != "c" || client.callbackState != state {
					t.Fatalf("session cookie = %+v, callback(%q, %q)", session, client.callbackCode, client.callbackState)
				}
			} else if session != nil && session.MaxAge >= 0 {
				t.Fatalf("a failed callback set a session cookie: %+v", session)
			}
		})
	}
}

func TestBackChannelLogoutNeedsNoBrowserCSRFButAValidToken(t *testing.T) {
	for _, logoutErr := range []error{nil, service.ErrSSOLogoutTokenInvalid} {
		client := &httpSSOClientStub{logoutErr: logoutErr}
		handler := NewRouterWithServices(clientModeDependencies(t, client))
		request := httptest.NewRequest(http.MethodPost, "/oauth/sso/backchannel-logout", strings.NewReader("logout_token=signed.logout.token"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		want := http.StatusOK
		if errors.Is(logoutErr, service.ErrSSOLogoutTokenInvalid) {
			want = http.StatusBadRequest
		}
		if response.Code != want || client.logoutToken != "signed.logout.token" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("back-channel (err %v) = %d token %q cache %q, want %d", logoutErr, response.Code, client.logoutToken, response.Header().Get("Cache-Control"), want)
		}
	}

	// Without a configured issuer the endpoint fails closed.
	handler := NewRouterWithServices(newHTTPTestDependencies(t))
	request := httptest.NewRequest(http.MethodPost, "/oauth/sso/backchannel-logout", strings.NewReader("logout_token=x"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("back-channel without an issuer = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestClientModeSignOutEndsTheIssuerSession(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	signOut := dependencies.SessionAdmin.(*httpSignOutStub)
	client := &httpSSOClientStub{signOutURL: "http://127.0.0.1:4444/oauth2/sessions/logout?id_token_hint=h", signOutAfter: signOut}
	dependencies.SSOClient = client
	handler := NewRouterWithServices(dependencies)
	csrf := csrfCookie(t, handler)
	response := csrfRequest(t, handler, http.MethodPost, "/en/sign-out", url.Values{"_csrf": {csrf.Value}}, csrf, &http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != client.signOutURL {
		t.Fatalf("sign-out = %d %q, want the issuer logout URL", response.Code, response.Header().Get("Location"))
	}
	if !signOut.called || !client.signOutFirst {
		t.Fatalf("local revoked %t, issuer URL read first %t; want the hint read before revoking", signOut.called, client.signOutFirst)
	}
}

func TestClientModeSettingsHideCredentialControls(t *testing.T) {
	dependencies := clientModeDependencies(t, &httpSSOClientStub{})
	handler := NewRouterWithServices(dependencies)
	request := httptest.NewRequest(http.MethodGet, "/en/account/settings", nil)
	request.AddCookie(&http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("settings status = %d", response.Code)
	}
	for _, want := range []string{`id="sso-account-heading"`, "user@example.test", dependencies.SSOAccountURL, `id="preferences-heading"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings missing %q", want)
		}
	}
	for _, absent := range []string{`id="password-heading"`, `id="profile-heading"`, `id="identities-heading"`} {
		if strings.Contains(body, absent) {
			t.Errorf("settings still shows %q in client mode", absent)
		}
	}
}

func guestClientModeDependencies(t *testing.T, client *httpSSOClientStub) Dependencies {
	t.Helper()
	dependencies := clientModeDependencies(t, client)
	dependencies.Users = httpUserStub{user: service.User{ID: 42, IsGuest: true, Theme: "light"}}
	return dependencies
}

func TestClientModeGuestUpgradeOffersSingleSignOn(t *testing.T) {
	handler := NewRouterWithServices(guestClientModeDependencies(t, &httpSSOClientStub{}))
	request := newContractRequest(http.MethodGet, "/en/guest/upgrade?sso=taken").withCookie(&http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})
	serveContract(t, handler, request).
		assertStatus(t, http.StatusOK).
		assertBodyContains(t, `data-testid="guest-upgrade-sso"`).
		assertBodyContains(t, `action="/en/guest/upgrade"`).
		assertBodyAbsent(t, `type="password"`).
		assertCatalogMessage(t, "en", "guest.error.sso_taken")
}

func TestClientModeGuestUpgradeStartsOnlyForAGuest(t *testing.T) {
	session := &http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"}
	client := &httpSSOClientStub{startTarget: "http://127.0.0.1:4444/oauth2/auth?u=1", startState: strings.Repeat("ab", 32)}
	handler := NewRouterWithServices(guestClientModeDependencies(t, client))
	csrf := csrfCookie(t, handler)
	response := csrfRequest(t, handler, http.MethodPost, "/en/guest/upgrade", url.Values{"_csrf": {csrf.Value}}, csrf, session)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != client.startTarget || client.upgradeGuestID != 42 {
		t.Fatalf("upgrade start = %d %q for guest %d", response.Code, response.Header().Get("Location"), client.upgradeGuestID)
	}
	if cookie := responseCookie(response, oauthStateCookieName); cookie == nil || cookie.Value != client.startState {
		t.Fatalf("state cookie = %+v", cookie)
	}

	// A full account, or a visitor without an account, cannot start one.
	cases := []struct {
		dependencies Dependencies
		cookies      []*http.Cookie
	}{
		{dependencies: clientModeDependencies(t, &httpSSOClientStub{}), cookies: []*http.Cookie{session}},
		{dependencies: guestClientModeDependencies(t, &httpSSOClientStub{})},
	}
	for _, c := range cases {
		stub := c.dependencies.SSOClient.(*httpSSOClientStub)
		handler := NewRouterWithServices(c.dependencies)
		csrf := csrfCookie(t, handler)
		response := csrfRequest(t, handler, http.MethodPost, "/en/guest/upgrade", url.Values{"_csrf": {csrf.Value}}, append([]*http.Cookie{csrf}, c.cookies...)...)
		if response.Code == http.StatusSeeOther || stub.upgradeGuestID != 0 {
			t.Fatalf("non-guest upgrade start = %d, StartUpgrade(%d)", response.Code, stub.upgradeGuestID)
		}
	}
}

func TestClientModeGuestCallbackFailuresReturnToTheUpgradePage(t *testing.T) {
	state := strings.Repeat("ab", 32)
	tests := []struct {
		err  error
		want string
	}{
		{err: service.ErrSSOGuestIdentityTaken, want: "/en/guest/upgrade?sso=taken"},
		{err: service.ErrOAuthEmailConflict, want: "/en/guest/upgrade?sso=conflict"},
		{err: service.ErrSSOSignInFailed, want: "/en/guest/upgrade?sso=error"},
	}
	for _, test := range tests {
		client := &httpSSOClientStub{callbackErr: test.err}
		handler := NewRouterWithServices(guestClientModeDependencies(t, client))
		request := oauthCallbackRequest("/en/oauth/sso/callback?code=c&state="+state, state)
		request.AddCookie(&http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.want {
			t.Fatalf("callback with %v = %d %q, want %q", test.err, response.Code, response.Header().Get("Location"), test.want)
		}
		if client.callbackActor != 42 {
			t.Fatalf("Callback actor = %d, want the signed-in guest 42", client.callbackActor)
		}
	}
}

func responseCookie(response *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func oauthCallbackRequest(target, stateCookie string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if stateCookie != "" {
		request.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: stateCookie})
	}
	return request
}
