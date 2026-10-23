// Package middleware contains reusable HTTP transport boundaries.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrNilHandler identifies a missing middleware chain handler.
	ErrNilHandler = errors.New("middleware: nil handler")
	// ErrNilMiddleware identifies a missing middleware function.
	ErrNilMiddleware = errors.New("middleware: nil middleware")
	// ErrUnauthenticated identifies a request without valid authentication.
	ErrUnauthenticated = errors.New("middleware: unauthenticated")
	// ErrRequestIDGeneration identifies failure to create a request ID.
	ErrRequestIDGeneration = errors.New("middleware: request id generation failed")
	// ErrInvalidRequestID identifies an unsafe request ID.
	ErrInvalidRequestID = errors.New("middleware: invalid request id")
	// ErrInvalidRedirectPath identifies a redirect outside this application.
	ErrInvalidRedirectPath = errors.New("middleware: invalid redirect path")
)

const (
	// RequestIDHeader is the response header containing the server-generated ID.
	RequestIDHeader = "X-Request-ID"
	requestIDBytes  = 16
	maxRequestIDLen = 128
)

// Middleware transforms one HTTP handler into another.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares in request order and validates every boundary.
func Chain(handler http.Handler, middlewares ...Middleware) (http.Handler, error) {
	if handler == nil {
		return nil, ErrNilHandler
	}
	for index := len(middlewares) - 1; index >= 0; index-- {
		if middlewares[index] == nil {
			return nil, ErrNilMiddleware
		}
		handler = middlewares[index](handler)
		if handler == nil {
			return nil, ErrNilHandler
		}
	}
	return handler, nil
}

type requestInfoKey struct{}

// RequestInfo contains request-scoped timing metadata.
type RequestInfo struct {
	StartedAt time.Time
}

type requestIDKey struct{}

// RequestIDGenerator creates a server-side request correlation ID.
type RequestIDGenerator func() (string, error)

// NewRequestID creates a cryptographically random, header-safe request ID.
func NewRequestID() (string, error) {
	value := make([]byte, requestIDBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("%w: random source unavailable", ErrRequestIDGeneration)
	}
	return hex.EncodeToString(value), nil
}

// WithRequestID adds a generated request ID to context and response headers.
func WithRequestID(generator RequestIDGenerator) Middleware {
	if generator == nil {
		generator = NewRequestID
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requestID, err := generator()
			if err != nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}
			if !validRequestID(requestID) {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}

			writer.Header().Set(RequestIDHeader, requestID)
			ctx := context.WithValue(request.Context(), requestIDKey{}, requestID)
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}

// RequestIDFromContext returns the server-generated request ID when present.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	requestID, ok := ctx.Value(requestIDKey{}).(string)
	return requestID, ok
}

// WithRequestInfo adds request-scoped metadata to the context.
func WithRequestInfo() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			info := RequestInfo{StartedAt: time.Now()}
			ctx := context.WithValue(request.Context(), requestInfoKey{}, info)
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}

// RequestInfoFromContext returns request timing metadata when present.
func RequestInfoFromContext(ctx context.Context) (RequestInfo, bool) {
	if ctx == nil {
		return RequestInfo{}, false
	}
	info, ok := ctx.Value(requestInfoKey{}).(RequestInfo)
	return info, ok
}

// Recover converts handler panics into generic server errors and logs only
// stable request fields. Panic values are intentionally not logged.
func Recover(logger *slog.Logger) Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			defer func() {
				if recover() == nil {
					return
				}
				logger.Error("http handler panic", "method", request.Method)
				http.Error(writer, "internal server error", http.StatusInternalServerError)
			}()
			next.ServeHTTP(writer, request)
		})
	}
}

// RequestEvent contains safe, low-cardinality request outcome fields.
type RequestEvent struct {
	RequestID string
	Method    string
	Status    int
	Duration  time.Duration
}

// RequestLogger receives request events without URL or credential fields.
type RequestLogger interface {
	LogRequest(context.Context, RequestEvent)
}

// Log records request method, status, and duration through logger.
func Log(logger RequestLogger) Middleware {
	return func(next http.Handler) http.Handler {
		if logger == nil {
			return next
		}
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			startedAt := time.Now()
			response := &statusWriter{ResponseWriter: writer}
			next.ServeHTTP(response, request)
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.LogRequest(request.Context(), RequestEvent{
				RequestID: requestID(request.Context()),
				Method:    request.Method,
				Status:    status,
				Duration:  time.Since(startedAt),
			})
		})
	}
}

// Tracer starts and finishes a request span using a consuming-side tracer.
type Tracer interface {
	Start(context.Context, string) (context.Context, func())
}

// Trace creates a low-cardinality span named by HTTP method.
func Trace(tracer Tracer) Middleware {
	return func(next http.Handler) http.Handler {
		if tracer == nil {
			return next
		}
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			ctx, finish := tracer.Start(request.Context(), request.Method)
			if finish != nil {
				defer finish()
			}
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}

// Principal identifies an authenticated account without exposing credentials.
type Principal struct {
	ID      string
	IsAdmin bool
}

// Authenticator resolves the authenticated principal for a request.
type Authenticator interface {
	Authenticate(context.Context, *http.Request) (Principal, error)
}

type principalKey struct{}

// RequireAuth protects routes with an injected authenticator.
func RequireAuth(authenticator Authenticator) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if authenticator == nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}

			principal, err := authenticator.Authenticate(request.Context(), request)
			if err != nil {
				if errors.Is(err, ErrUnauthenticated) {
					http.Error(writer, "authentication required", http.StatusUnauthorized)
					return
				}
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}

			ctx := context.WithValue(request.Context(), principalKey{}, principal)
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}

// RequireAnonymous protects pages intended only for visitors without a
// session. Authenticated visitors are redirected to redirectPath.
func RequireAnonymous(authenticator Authenticator, redirectPath string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if authenticator == nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}

			_, err := authenticator.Authenticate(request.Context(), request)
			if errors.Is(err, ErrUnauthenticated) {
				next.ServeHTTP(writer, request)
				return
			}
			if err != nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}
			redirect(writer, request, redirectPath)
		})
	}
}

// RequireAuthenticated protects page routes and redirects visitors without a
// valid session to redirectPath.
func RequireAuthenticated(authenticator Authenticator, redirectPath string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if authenticator == nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}

			principal, err := authenticator.Authenticate(request.Context(), request)
			if errors.Is(err, ErrUnauthenticated) {
				redirect(writer, request, redirectPath)
				return
			}
			if err != nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}
			withPrincipal(next, writer, request, principal)
		})
	}
}

// RequireAdmin protects administrator pages. Authenticated non-administrators
// receive access denied without revealing whether any other account exists.
func RequireAdmin(authenticator Authenticator, redirectPath string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if authenticator == nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}

			principal, err := authenticator.Authenticate(request.Context(), request)
			if errors.Is(err, ErrUnauthenticated) {
				redirect(writer, request, redirectPath)
				return
			}
			if err != nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}
			if !principal.IsAdmin {
				http.Error(writer, "access denied", http.StatusForbidden)
				return
			}
			withPrincipal(next, writer, request, principal)
		})
	}
}

// PrincipalFromContext returns the authenticated principal when present.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

func withPrincipal(next http.Handler, writer http.ResponseWriter, request *http.Request, principal Principal) {
	ctx := context.WithValue(request.Context(), principalKey{}, principal)
	next.ServeHTTP(writer, request.WithContext(ctx))
}

func redirect(writer http.ResponseWriter, request *http.Request, path string) {
	if !validRedirectPath(path) {
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, path, http.StatusFound)
}

func validRedirectPath(path string) bool {
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return false
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return false
	}
	return !strings.HasPrefix(path, "//")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func requestID(ctx context.Context) string {
	value, _ := RequestIDFromContext(ctx)
	return value
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLen {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}
