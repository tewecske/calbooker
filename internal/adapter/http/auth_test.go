package httpadapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

type httpUserStub struct {
	user service.User
	err  error
}

func (s httpUserStub) FindUserByID(context.Context, int64) (service.User, error) {
	return s.user, s.err
}

type httpSessionStub struct {
	session service.Session
	err     error
}

func (s httpSessionStub) Lookup(context.Context, string) (service.Session, error) {
	return s.session, s.err
}

type httpSignOutStub struct {
	called    bool
	sessionID string
	err       error
}

type httpGuestCreatorStub struct {
	result    service.GuestSessionResult
	err       error
	lastTheme string
}

func (s *httpGuestCreatorStub) Create(_ context.Context, _ string, theme string) (service.GuestSessionResult, error) {
	s.lastTheme = theme
	return s.result, s.err
}

type httpGuestClaimerStub struct {
	claim service.GuestClaimCode
	err   error
}

func (s httpGuestClaimerStub) Issue(context.Context, int64) (service.GuestClaimCode, error) {
	return s.claim, s.err
}

type httpGuestRedeemerStub struct {
	result service.GuestSessionResult
	err    error
}

func (s httpGuestRedeemerStub) Redeem(context.Context, string, string) (service.GuestSessionResult, error) {
	return s.result, s.err
}

func (s *httpSignOutStub) SignOut(_ context.Context, sessionID string) error {
	s.called = true
	s.sessionID = sessionID
	return s.err
}

func newHTTPTestDependencies(t *testing.T) Dependencies {
	t.Helper()
	cookie, err := middleware.NewSessionCookie(middleware.SessionCookieConfig{Lifetime: time.Hour})
	if err != nil {
		t.Fatalf("NewSessionCookie() error = %v", err)
	}
	return Dependencies{
		Users:         httpUserStub{user: service.User{ID: 42, Email: stringPointer("user@example.test"), Theme: "light"}},
		Sessions:      httpSessionStub{session: service.Session{ID: "opaque-session", UserID: 42, CreatedAt: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}},
		SessionAdmin:  &httpSignOutStub{},
		SessionCookie: cookie,
	}
}

func stringPointer(value string) *string { return &value }

func csrfRequest(t *testing.T, handler http.Handler, method, path string, values url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func csrfCookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/sign-in", nil))
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == middleware.CSRFCookieName {
			return cookie
		}
	}
	t.Fatal("GET did not set CSRF cookie")
	return nil
}

func TestSignOutRevokesAndClearsSession(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	signOut := dependencies.SessionAdmin.(*httpSignOutStub)
	handler := NewRouterWithServices(dependencies)
	csrf := csrfCookie(t, handler)
	response := csrfRequest(t, handler, http.MethodPost, "/en/sign-out", url.Values{"_csrf": {csrf.Value}}, csrf, &http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en/sign-in" {
		t.Fatalf("sign-out response = %d %q, want redirect to sign-in", response.Code, response.Header().Get("Location"))
	}
	if !signOut.called {
		t.Fatal("sign-out did not revoke current session")
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == middleware.DefaultSessionCookieName && cookie.MaxAge >= 0 {
			t.Fatalf("session cookie was not expired: %+v", cookie)
		}
	}
}

func TestAuthenticatedHomeRedirectsAnonymousVisitor(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	handler := NewRouterWithServices(dependencies)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/en/home", nil))

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/en/sign-in" {
		t.Fatalf("anonymous home response = %d %q, want sign-in redirect", response.Code, response.Header().Get("Location"))
	}
}

func TestGuestWriteCreatesAccountOnlyOnPost(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	guest := service.User{ID: 73, IsGuest: true, Username: stringPointer("guest-73"), Theme: "light"}
	creator := &httpGuestCreatorStub{result: service.GuestSessionResult{User: guest, Session: service.Session{ID: "guest-session"}}}
	dependencies.GuestCreator = creator
	handler := NewRouterWithServices(dependencies)
	view := httptest.NewRecorder()
	handler.ServeHTTP(view, httptest.NewRequest(http.MethodGet, "/en/guest/write", nil))
	if view.Code != http.StatusOK || strings.Contains(view.Body.String(), "data-guest-banner") {
		t.Fatalf("guest page view created or displayed guest account: %d %s", view.Code, view.Body.String())
	}
	csrf := csrfCookie(t, handler)
	response := csrfRequest(t, handler, http.MethodPost, "/en/guest/write", url.Values{"_csrf": {csrf.Value}, "action": {"write"}}, csrf)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "data-guest-banner") || !strings.Contains(response.Body.String(), "data-guest-owned-state") {
		t.Fatalf("guest write response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "guest-session") {
		t.Fatal("guest write response rendered session credential")
	}
	if creator.lastTheme == "" {
		t.Fatal("guest creation without a theme did not fall back to a supported theme")
	}
}

func TestGuestTransferCodeOnlyAppearsAfterExplicitRequest(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	guest := service.User{ID: 73, IsGuest: true, Username: stringPointer("guest-73"), Theme: "light"}
	dependencies.Users = httpUserStub{user: guest}
	dependencies.Sessions = httpSessionStub{session: service.Session{ID: "opaque-session", UserID: 73, CreatedAt: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}}
	dependencies.GuestClaimer = httpGuestClaimerStub{claim: service.GuestClaimCode{UserID: 73, Code: "ABCDEFGHJK"}}
	handler := NewRouterWithServices(dependencies)
	csrf := csrfCookie(t, handler)
	session := &http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"}
	view := csrfRequest(t, handler, http.MethodGet, "/en/guest/transfer", nil, csrf, session)
	if strings.Contains(view.Body.String(), "ABCDEFGHJK") {
		t.Fatal("guest transfer page exposed code before request")
	}
	response := csrfRequest(t, handler, http.MethodPost, "/en/guest/transfer", url.Values{"_csrf": {csrf.Value}, "action": {"request"}}, csrf, session)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ABCDEFGHJK") {
		t.Fatalf("explicit transfer request response = %d %s", response.Code, response.Body.String())
	}
}

func TestGuestInvalidTransferCodeHasUniformSafeError(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	dependencies.GuestRedeemer = httpGuestRedeemerStub{err: service.ErrInvalidGuestClaimCode}
	handler := NewRouterWithServices(dependencies)
	csrf := csrfCookie(t, handler)
	response := csrfRequest(t, handler, http.MethodPost, "/en/guest/transfer", url.Values{
		"_csrf": {csrf.Value}, "action": {"redeem"}, "code": {"SECRET-CODE"},
	}, csrf)

	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "invalid or no longer available") {
		t.Fatalf("invalid guest code response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "SECRET-CODE") || strings.Contains(response.Body.String(), "session_id") {
		t.Fatal("invalid guest code response leaked credentials")
	}
}

func TestRouterHardensBrowserRequests(t *testing.T) {
	dependencies := newHTTPTestDependencies(t)
	handler := NewRouterWithServices(dependencies)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/en/sign-in", nil))
	csp := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want same-origin scripts only and no framing", csp)
	}
	for header, want := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "same-origin", "Cache-Control": "no-store"} {
		if got := page.Header().Get(header); got != want {
			t.Errorf("page %s = %q, want %q", header, got, want)
		}
	}
	script := httptest.NewRecorder()
	handler.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	if script.Code != http.StatusOK || !strings.Contains(script.Body.String(), `localStorage.getItem(storageKey)`) || script.Header().Get("Cache-Control") == "no-store" {
		t.Errorf("app.js = %d (cache %q), want cacheable page script", script.Code, script.Header().Get("Cache-Control"))
	}

	planted := httptest.NewRequest(http.MethodGet, "/en/sign-in", nil)
	planted.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "attacker-planted"})
	replaced := httptest.NewRecorder()
	handler.ServeHTTP(replaced, planted)
	if cookies := replaced.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != middleware.CSRFCookieName || len(cookies[0].Value) != 64 {
		t.Errorf("malformed CSRF cookie not replaced: %+v", cookies)
	}

	csrf := csrfCookie(t, handler)
	signOut := func(mutate func(*http.Request)) int {
		form := url.Values{"_csrf": {csrf.Value}}
		request := httptest.NewRequest(http.MethodPost, "/en/sign-out", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(csrf)
		mutate(request)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if code := signOut(func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }); code != http.StatusSeeOther {
		t.Errorf("same-origin sign-out = %d, want it to reach sign-out (303)", code)
	}
	for name, mutate := range map[string]func(*http.Request){
		"cross-site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"sibling origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.test") },
		"oversized body": func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Repeat("a=b&", maxRequestBodyBytes)))
		},
	} {
		if code := signOut(mutate); code != http.StatusForbidden {
			t.Errorf("%s sign-out = %d, want 403 before sign-out runs", name, code)
		}
	}

	secureCookie, err := middleware.NewSessionCookie(middleware.SessionCookieConfig{Secure: true, Lifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	dependencies.SessionCookie = secureCookie
	secureCSRF := csrfCookie(t, NewRouterWithServices(dependencies))
	if !secureCSRF.Secure {
		t.Error("CSRF cookie is not Secure when session cookies are, so it would travel over plain HTTP behind a TLS proxy")
	}
}

// The whole highlighted menu row must submit sign-out. When the button was
// nested in a <form>, daisyUI styled the form as the row and a click on its
// padding did nothing.
func TestAccountMenuSignOutIsTheMenuItem(t *testing.T) {
	handler := NewRouterWithServices(newHTTPTestDependencies(t))
	request := httptest.NewRequest(http.MethodGet, "/en/home", nil)
	request.AddCookie(&http.Cookie{Name: middleware.DefaultSessionCookieName, Value: "opaque-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	form := regexp.MustCompile(`<form id="sign-out-form" method="post" action="/en/sign-out" hidden>\s*<input type="hidden" name="_csrf" value="[^"]*">\s*</form>\s*<button type="submit" form="sign-out-form">Sign out</button>\s*</li>`)
	if !form.MatchString(body) {
		t.Fatalf("account menu does not render sign-out as the menu item submitting a separate form:\n%s", body)
	}
}
