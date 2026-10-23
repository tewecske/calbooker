package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testCSRFToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCSRF(t *testing.T) {
	handler := CSRF(
		WithCSRFTokenGenerator(func() (string, error) { return testCSRFToken, nil }),
	)(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if token, ok := CSRFTokenFromContext(request.Context()); !ok || token != testCSRFToken {
			t.Errorf("CSRFTokenFromContext() = %q, %t; want test token, true", token, ok)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if getResponse.Code != http.StatusNoContent {
		t.Fatalf("GET status = %d, want %d", getResponse.Code, http.StatusNoContent)
	}
	cookie := getResponse.Result().Cookies()
	if len(cookie) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookie))
	}
	if cookie[0].Name != CSRFCookieName || cookie[0].Value != testCSRFToken || !cookie[0].HttpOnly || cookie[0].Secure || cookie[0].SameSite != http.SameSiteStrictMode {
		t.Errorf("csrf cookie = %#v", cookie[0])
	}

	tests := []struct {
		name           string
		method         string
		cookie         bool
		formToken      string
		headerToken    string
		expectedStatus int
	}{
		{name: "missing cookie", method: http.MethodPost, expectedStatus: http.StatusForbidden},
		{name: "missing request token", method: http.MethodPost, cookie: true, expectedStatus: http.StatusForbidden},
		{name: "wrong form token", method: http.MethodPost, cookie: true, formToken: "bad", expectedStatus: http.StatusForbidden},
		{name: "valid form token", method: http.MethodPost, cookie: true, formToken: testCSRFToken, expectedStatus: http.StatusNoContent},
		{name: "valid htmx header", method: http.MethodPost, cookie: true, headerToken: testCSRFToken, expectedStatus: http.StatusNoContent},
		{name: "valid put token", method: http.MethodPut, cookie: true, formToken: testCSRFToken, expectedStatus: http.StatusNoContent},
		{name: "options is safe", method: http.MethodOptions, expectedStatus: http.StatusNoContent},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/", strings.NewReader(""))
			if test.cookie {
				request.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: testCSRFToken})
			}
			if test.formToken != "" {
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				request.Body = http.NoBody
				request.PostForm = map[string][]string{CSRFFieldName: {test.formToken}}
			}
			if test.headerToken != "" {
				request.Header.Set(CSRFHeaderName, test.headerToken)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.expectedStatus {
				t.Errorf("status = %d, want %d", response.Code, test.expectedStatus)
			}
		})
	}
}

func TestCSRFGeneratorFailure(t *testing.T) {
	handler := CSRF(WithCSRFTokenGenerator(func() (string, error) { return "", errors.New("random failure") }))(http.NotFoundHandler())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "random failure") {
		t.Errorf("response = %d, %q", response.Code, response.Body.String())
	}
}

func TestCSRFHelpers(t *testing.T) {
	if token, ok := CSRFTokenFromContext(context.Background()); ok || token != "" {
		t.Errorf("CSRFTokenFromContext(background) = %q, %t; want empty, false", token, ok)
	}
	if _, err := NewCSRFToken(); err != nil {
		t.Fatalf("NewCSRFToken() error = %v, want nil", err)
	}
}

func TestCSRFExemptPathIsExact(t *testing.T) {
	handler := CSRF(WithCSRFExemptPath("/oauth/sso/backchannel-logout"))(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	for path, want := range map[string]int{
		"/oauth/sso/backchannel-logout":       http.StatusNoContent,
		"/oauth/sso/backchannel-logout/extra": http.StatusForbidden,
		"/oauth/sso/backchannel":              http.StatusForbidden,
		"/en/sign-out":                        http.StatusForbidden,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader("logout_token=x")))
		if response.Code != want {
			t.Errorf("POST %s without a token = %d, want %d", path, response.Code, want)
		}
	}
}
