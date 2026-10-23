package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChain(t *testing.T) {
	called := ""
	first := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			called += "first "
			next.ServeHTTP(writer, request)
		})
	}
	second := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			called += "second "
			next.ServeHTTP(writer, request)
		})
	}

	handler, err := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called += "handler" }), first, second)
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if called != "first second handler" {
		t.Errorf("middleware order = %q, want %q", called, "first second handler")
	}

	for _, test := range []struct {
		name        string
		handler     http.Handler
		middlewares []Middleware
		wantErr     error
	}{
		{name: "nil handler", wantErr: ErrNilHandler},
		{name: "nil middleware", handler: http.NotFoundHandler(), middlewares: []Middleware{nil}, wantErr: ErrNilMiddleware},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Chain(test.handler, test.middlewares...)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Chain() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestWithRequestInfo(t *testing.T) {
	var got RequestInfo
	handler := WithRequestInfo()(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		var ok bool
		got, ok = RequestInfoFromContext(request.Context())
		if !ok {
			t.Error("RequestInfoFromContext() found no request info")
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got.StartedAt.IsZero() {
		t.Error("RequestInfo.StartedAt is zero")
	}
}

func TestWithRequestID(t *testing.T) {
	handler := WithRequestID(func() (string, error) { return "request-123", nil })(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID, ok := RequestIDFromContext(request.Context())
		if !ok || requestID != "request-123" {
			t.Errorf("RequestIDFromContext() = %q, %t; want request-123, true", requestID, ok)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if response.Header().Get(RequestIDHeader) != "request-123" {
		t.Errorf("request id header = %q, want request-123", response.Header().Get(RequestIDHeader))
	}
	if _, err := NewRequestID(); err != nil {
		t.Fatalf("NewRequestID() error = %v, want nil", err)
	}

	for _, test := range []struct {
		name      string
		generator RequestIDGenerator
	}{
		{name: "generator failure", generator: func() (string, error) { return "", ErrRequestIDGeneration }},
		{name: "empty id", generator: func() (string, error) { return "", nil }},
		{name: "unsafe id", generator: func() (string, error) { return "request\nid", nil }},
		{name: "long id", generator: func() (string, error) { return strings.Repeat("x", 129), nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			WithRequestID(test.generator)(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want %d", response.Code, http.StatusInternalServerError)
			}
			if response.Header().Get(RequestIDHeader) != "" {
				t.Error("request id header set after generation failure")
			}
		})
	}
}

func TestRecover(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("private value must not enter logs")
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if response.Body.String() != "internal server error\n" {
		t.Errorf("body = %q, want generic error", response.Body.String())
	}
}

func TestLog(t *testing.T) {
	logger := &testRequestLogger{}
	handler := WithRequestID(func() (string, error) { return "request-123", nil })(Log(logger)(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/private?token=secret", nil))

	if logger.event.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", logger.event.Method)
	}
	if logger.event.Status != http.StatusNoContent {
		t.Errorf("status = %d, want %d", logger.event.Status, http.StatusNoContent)
	}
	if logger.event.RequestID != "request-123" {
		t.Errorf("request id = %q, want request-123", logger.event.RequestID)
	}
	if logger.event.Duration < 0 {
		t.Errorf("duration = %s, want non-negative", logger.event.Duration)
	}
}

func TestTrace(t *testing.T) {
	tracer := &testTracer{}
	handler := Trace(tracer)(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if request.Context().Value(traceContextKey{}) != "trace" {
			t.Error("trace context value missing")
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if tracer.method != http.MethodGet {
		t.Errorf("trace method = %q, want GET", tracer.method)
	}
	if !tracer.finished {
		t.Error("trace finish was not called")
	}
}

func TestRequireAuth(t *testing.T) {
	tests := []struct {
		name           string
		authenticator  Authenticator
		expectedStatus int
	}{
		{name: "missing authenticator", expectedStatus: http.StatusInternalServerError},
		{name: "unauthenticated", authenticator: authFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{}, ErrUnauthenticated }), expectedStatus: http.StatusUnauthorized},
		{name: "authenticator failure", authenticator: authFunc(func(context.Context, *http.Request) (Principal, error) {
			return Principal{}, errors.New("backend failure")
		}), expectedStatus: http.StatusInternalServerError},
		{name: "authenticated", authenticator: authFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{ID: "user-1"}, nil }), expectedStatus: http.StatusNoContent},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := RequireAuth(test.authenticator)(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				principal, ok := PrincipalFromContext(request.Context())
				if !ok || principal.ID != "user-1" {
					t.Error("authenticated principal missing from context")
				}
				writer.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

			if response.Code != test.expectedStatus {
				t.Errorf("status = %d, want %d", response.Code, test.expectedStatus)
			}
		})
	}
}

func TestRequireAnonymous(t *testing.T) {
	tests := []struct {
		name           string
		authenticator  Authenticator
		redirectPath   string
		expectedStatus int
		location       string
	}{
		{
			name:           "anonymous proceeds",
			authenticator:  authFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{}, ErrUnauthenticated }),
			redirectPath:   "/home",
			expectedStatus: http.StatusNoContent,
		},
		{
			name:           "authenticated redirects",
			authenticator:  authFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{ID: "user-1"}, nil }),
			redirectPath:   "/home",
			expectedStatus: http.StatusFound,
			location:       "/home",
		},
		{
			name: "backend failure",
			authenticator: authFunc(func(context.Context, *http.Request) (Principal, error) {
				return Principal{}, errors.New("backend failure")
			}),
			redirectPath:   "/home",
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "invalid redirect",
			authenticator:  authFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{ID: "user-1"}, nil }),
			redirectPath:   "https://example.com",
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := RequireAnonymous(test.authenticator, test.redirectPath)(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				called = true
				writer.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

			if response.Code != test.expectedStatus {
				t.Errorf("status = %d, want %d", response.Code, test.expectedStatus)
			}
			if test.location != "" && response.Header().Get("Location") != test.location {
				t.Errorf("location = %q, want %q", response.Header().Get("Location"), test.location)
			}
			if test.expectedStatus == http.StatusNoContent && !called {
				t.Error("anonymous request did not reach handler")
			}
		})
	}
}

func TestRequireAuthenticated(t *testing.T) {
	tests := []struct {
		name           string
		authenticator  Authenticator
		expectedStatus int
	}{
		{
			name:           "missing session redirects",
			authenticator:  authFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{}, ErrUnauthenticated }),
			expectedStatus: http.StatusFound,
		},
		{
			name:           "authenticated proceeds",
			authenticator:  authFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{ID: "user-1"}, nil }),
			expectedStatus: http.StatusNoContent,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := RequireAuthenticated(test.authenticator, "/sign-in")(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				principal, ok := PrincipalFromContext(request.Context())
				if !ok || principal.ID != "user-1" {
					t.Error("principal missing from authenticated request")
				}
				writer.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/private", nil))

			if response.Code != test.expectedStatus {
				t.Errorf("status = %d, want %d", response.Code, test.expectedStatus)
			}
		})
	}
}

func TestRequireAdmin(t *testing.T) {
	tests := []struct {
		name           string
		principal      Principal
		authError      error
		expectedStatus int
	}{
		{
			name:           "missing session redirects",
			authError:      ErrUnauthenticated,
			expectedStatus: http.StatusFound,
		},
		{
			name:           "member denied",
			principal:      Principal{ID: "user-1"},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "administrator proceeds",
			principal:      Principal{ID: "admin-1", IsAdmin: true},
			expectedStatus: http.StatusNoContent,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := RequireAdmin(authFunc(func(context.Context, *http.Request) (Principal, error) {
				return test.principal, test.authError
			}), "/sign-in")(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin", nil))

			if response.Code != test.expectedStatus {
				t.Errorf("status = %d, want %d", response.Code, test.expectedStatus)
			}
		})
	}
}

type authFunc func(context.Context, *http.Request) (Principal, error)

func (f authFunc) Authenticate(ctx context.Context, request *http.Request) (Principal, error) {
	return f(ctx, request)
}

type testRequestLogger struct {
	event RequestEvent
}

func (l *testRequestLogger) LogRequest(_ context.Context, event RequestEvent) {
	l.event = event
}

type traceContextKey struct{}

type testTracer struct {
	method   string
	finished bool
}

func (t *testTracer) Start(ctx context.Context, method string) (context.Context, func()) {
	t.method = method
	return context.WithValue(ctx, traceContextKey{}, "trace"), func() { t.finished = true }
}
