package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionCookieSetAndRead(t *testing.T) {
	createdAt := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	cookie, err := NewSessionCookie(SessionCookieConfig{
		Secure:   true,
		Lifetime: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSessionCookie() error = %v, want nil", err)
	}

	response := httptest.NewRecorder()
	if err := cookie.Set(response, "session-token", createdAt); err != nil {
		t.Fatalf("Set() error = %v, want nil", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Cookie", response.Header().Get("Set-Cookie"))
	got, ok := cookie.Read(request)
	if !ok || got != "session-token" {
		t.Fatalf("Read() = %q, %t; want session-token, true", got, ok)
	}

	parsed := response.Result().Cookies()[0]
	if parsed.Name != DefaultSessionCookieName || parsed.Path != "/" || !parsed.HttpOnly || !parsed.Secure {
		t.Fatalf("cookie attributes = %#v, want default name/path and HttpOnly/Secure", parsed)
	}
	if parsed.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", parsed.SameSite)
	}
	if parsed.MaxAge != int(2*time.Hour/time.Second) {
		t.Errorf("MaxAge = %d, want %d", parsed.MaxAge, 2*time.Hour/time.Second)
	}
	if !parsed.Expires.Equal(createdAt.Add(2 * time.Hour)) {
		t.Errorf("Expires = %s, want %s", parsed.Expires, createdAt.Add(2*time.Hour))
	}
}

func TestSessionCookieClear(t *testing.T) {
	cookie, err := NewSessionCookie(SessionCookieConfig{Lifetime: time.Hour})
	if err != nil {
		t.Fatalf("NewSessionCookie() error = %v, want nil", err)
	}
	response := httptest.NewRecorder()
	if err := cookie.Clear(response, time.Now()); err != nil {
		t.Fatalf("Clear() error = %v, want nil", err)
	}
	parsed := response.Result().Cookies()[0]
	if parsed.MaxAge != -1 || parsed.Value != "" || !parsed.HttpOnly {
		t.Fatalf("clear cookie = %#v, want expired empty HttpOnly cookie", parsed)
	}
}

func TestNewSessionCookieRejectsInvalidConfigAndValue(t *testing.T) {
	for _, test := range []struct {
		name   string
		config SessionCookieConfig
	}{
		{name: "short lifetime", config: SessionCookieConfig{Lifetime: time.Millisecond}},
		{name: "unsafe name", config: SessionCookieConfig{Name: "session name", Lifetime: time.Hour}},
		{name: "unsafe path", config: SessionCookieConfig{Path: "relative", Lifetime: time.Hour}},
		{name: "unsupported samesite", config: SessionCookieConfig{Lifetime: time.Hour, SameSite: http.SameSite(99)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSessionCookie(test.config); !errors.Is(err, ErrInvalidSessionCookieConfig) {
				t.Fatalf("NewSessionCookie() error = %v, want %v", err, ErrInvalidSessionCookieConfig)
			}
		})
	}

	cookie, err := NewSessionCookie(SessionCookieConfig{Lifetime: time.Hour})
	if err != nil {
		t.Fatalf("NewSessionCookie() error = %v, want nil", err)
	}
	response := httptest.NewRecorder()
	if err := cookie.Set(response, "bad value", time.Now()); !errors.Is(err, ErrInvalidSessionCookieValue) {
		t.Fatalf("Set(invalid value) error = %v, want %v", err, ErrInvalidSessionCookieValue)
	}
	if _, ok := cookie.Read(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Error("Read(missing cookie) = true, want false")
	}
}
