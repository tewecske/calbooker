package httpadapter

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

const (
	secretPassword = "sentinel-password-9f3a"
	secretToken    = "sentinel-token-6b2c"
	secretSession  = "sentinel-session-1d4e"
	secretSubject  = "sentinel-provider-subject-7a8b"
	secretHash     = "sentinel-argon2-hash-2c5d"
)

func secretSentinels() []string {
	return []string{secretPassword, secretToken, secretSession, secretSubject, secretHash}
}

func assertNoSecrets(t *testing.T, label, output string) {
	t.Helper()
	for _, secret := range secretSentinels() {
		if strings.Contains(output, secret) {
			t.Errorf("%s leaked secret %q: %s", label, secret, output)
		}
	}
}

type secretUsageRecorder struct {
	events []middleware.UsageEvent
}

func (r *secretUsageRecorder) EnqueueUsage(_ context.Context, event middleware.UsageEvent) error {
	r.events = append(r.events, event)
	return nil
}

func TestRouterDiagnosticsNeverContainRequestSecrets(t *testing.T) {
	var applicationOutput, securityOutput, spanOutput bytes.Buffer
	logger := NewLogger(
		slog.New(slog.NewJSONHandler(&applicationOutput, nil)),
		slog.New(slog.NewJSONHandler(&securityOutput, nil)),
	)
	tracer := NewSpanTracer(SpanTracerConfig{Logger: slog.New(slog.NewJSONHandler(&spanOutput, nil))})
	usage := &secretUsageRecorder{}

	dependencies := settingsStubDependencies(t)
	dependencies.Tracer = tracer
	dependencies.Usage = usage
	dependencies.SecurityLogger = logger
	handler := NewLoggedRouterWithServices(logger, dependencies)

	form := url.Values{"email": {"user@example.test"}, "password": {secretPassword}}

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/en/sign-in?token="+secretToken+"&password="+secretPassword, nil),
		httptest.NewRequest(http.MethodGet, "/en/account/settings?secret="+secretToken, nil),
		httptest.NewRequest(http.MethodPost, "/en/sign-up", strings.NewReader(form.Encode())),
		httptest.NewRequest(http.MethodGet, "/en/admin?subject="+secretSubject, nil),
	}
	requests[2].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, request := range requests {
		request.AddCookie(&http.Cookie{Name: middleware.DefaultSessionCookieName, Value: secretSession})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		assertNoSecrets(t, "response body", recorder.Body.String())
		assertNoSecrets(t, "response headers", fmt.Sprint(recorder.Header()))
	}

	assertNoSecrets(t, "application log", applicationOutput.String())
	assertNoSecrets(t, "security log", securityOutput.String())
	assertNoSecrets(t, "span log", spanOutput.String())
	assertNoSecrets(t, "span output", spanOutput.String())

	if len(usage.events) == 0 {
		t.Fatal("no usage events were recorded")
	}
	for _, event := range usage.events {
		assertNoSecrets(t, "usage route", event.Route)
		assertNoSecrets(t, "usage request id", event.RequestID)
		if strings.Contains(event.Route, "?") {
			t.Errorf("usage route contains a query string: %q", event.Route)
		}
	}
	if !strings.Contains(spanOutput.String(), `"route":"/{language}/sign-in"`) {
		t.Errorf("span log did not use a normalized route: %s", spanOutput.String())
	}
}

func TestRenderedPagesNeverContainCredentialsOrProviderSubjects(t *testing.T) {
	dependencies := settingsStubDependencies(t)
	dependencies.Users = testUserFinder{user: service.User{ID: 7, Version: 1}}
	handler, _ := newSettingsTestHandler(t, dependencies)

	full := httptest.NewRecorder()
	handler.page(full, authenticatedRequest(http.MethodGet, "/en/account/settings", 7, ""))
	assertNoSecrets(t, "settings full page", full.Body.String())

	fragment := httptest.NewRecorder()
	handler.page(fragment, authenticatedRequest(http.MethodGet, "/en/account/settings", 7, ""))
	if strings.Contains(fragment.Body.String(), secretHash) {
		t.Errorf("settings page leaked the stored password hash: %s", fragment.Body.String())
	}
	if strings.Contains(fragment.Body.String(), secretSubject) {
		t.Errorf("settings page leaked an external provider subject: %s", fragment.Body.String())
	}
}
