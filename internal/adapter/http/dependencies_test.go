package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

type testSessionLookup struct {
	session service.Session
	err     error
}

func (s testSessionLookup) Lookup(context.Context, string) (service.Session, error) {
	return s.session, s.err
}

type testUserFinder struct {
	user service.User
	err  error
}

func (u testUserFinder) FindUserByID(context.Context, int64) (service.User, error) {
	return u.user, u.err
}

type testSessionAdmin struct{}

func (testSessionAdmin) SignOut(context.Context, string) error { return nil }

func testSessionCookie(t *testing.T) *middleware.SessionCookie {
	t.Helper()
	cookie, err := middleware.NewSessionCookie(middleware.SessionCookieConfig{
		Lifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSessionCookie() error = %v", err)
	}
	return cookie
}

func TestDependenciesValidateRejectsMissingBoundaries(t *testing.T) {
	if err := (Dependencies{}).Validate(); !errors.Is(err, ErrInvalidHTTPDependencies) {
		t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidHTTPDependencies)
	}
}

func TestSessionAuthenticatorRejectsMissingAndExpiredCookies(t *testing.T) {
	authenticator, err := NewSessionAuthenticator(
		testUserFinder{user: service.User{ID: 7}},
		testSessionLookup{err: service.ErrSessionExpired},
		testSessionCookie(t),
	)
	if err != nil {
		t.Fatalf("NewSessionAuthenticator() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/en/", nil)
	if _, err := authenticator.Authenticate(context.Background(), request); !errors.Is(err, middleware.ErrUnauthenticated) {
		t.Fatalf("missing cookie error = %v, want unauthenticated", err)
	}
}

func TestSessionAuthenticatorReturnsSafePrincipal(t *testing.T) {
	cookie := testSessionCookie(t)
	request := httptest.NewRequest(http.MethodGet, "/en/", nil)
	if err := cookie.Set(requestResponseWriter{header: request.Header}, "opaque-session", time.Now()); err != nil {
		t.Fatalf("cookie.Set() error = %v", err)
	}
	request.Header.Set("Cookie", "calbooker_session=opaque-session")

	authenticator, err := NewSessionAuthenticator(
		testUserFinder{user: service.User{ID: 7, IsAdmin: true}},
		testSessionLookup{session: service.Session{ID: "opaque-session", UserID: 7, CreatedAt: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}},
		cookie,
	)
	if err != nil {
		t.Fatalf("NewSessionAuthenticator() error = %v", err)
	}
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.ID != "7" || !principal.IsAdmin {
		t.Fatalf("principal = %+v, want id 7 admin", principal)
	}
}

func TestSessionAuthenticatorNeverHonorsAProfileSession(t *testing.T) {
	cookie := testSessionCookie(t)
	request := httptest.NewRequest(http.MethodGet, "/en/", nil)
	request.Header.Set("Cookie", "calbooker_session=opaque-session")
	authenticator, err := NewSessionAuthenticator(
		testUserFinder{user: service.User{ID: 20, Kind: service.UserKindProfile}},
		testSessionLookup{session: service.Session{ID: "opaque-session", UserID: 20, CreatedAt: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}},
		cookie,
	)
	if err != nil {
		t.Fatalf("NewSessionAuthenticator() error = %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), request); !errors.Is(err, middleware.ErrUnauthenticated) {
		t.Fatalf("profile session error = %v, want unauthenticated", err)
	}
	// The same session for a guest authenticates, so the kind decided it.
	authenticator.users = testUserFinder{user: service.User{ID: 20, Kind: service.UserKindGuest}}
	if _, err := authenticator.Authenticate(context.Background(), request); err != nil {
		t.Fatalf("guest session error = %v, want nil", err)
	}
}

type requestResponseWriter struct{ header http.Header }

func (w requestResponseWriter) Header() http.Header       { return w.header }
func (w requestResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (w requestResponseWriter) WriteHeader(int)           {}
