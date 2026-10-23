package middleware

import (
	"context"
	"net/http"
)

// SecurityRequestEvent is a separately identifiable security event for a
// denied or rate-limited request. It carries no URL, query string, or
// credential.
type SecurityRequestEvent struct {
	RequestID string
	Method    string
	Route     string
	Status    int
}

// SecurityRequestLogger emits security events on a separate log stream.
type SecurityRequestLogger interface {
	LogSecurityRequest(context.Context, SecurityRequestEvent)
}

// SecurityEvents emits a security event after a request is denied or rate
// limited. Ordinary requests are not emitted here.
func SecurityEvents(logger SecurityRequestLogger, normalize RouteNormalizer) Middleware {
	if logger == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	if normalize == nil {
		normalize = DefaultRouteNormalizer
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			response := &statusWriter{ResponseWriter: writer}
			next.ServeHTTP(response, request)
			status := response.status
			if !isSecurityStatus(status) {
				return
			}
			logger.LogSecurityRequest(request.Context(), SecurityRequestEvent{
				RequestID: requestID(request.Context()),
				Method:    request.Method,
				Route:     normalize(request),
				Status:    status,
			})
		})
	}
}

func isSecurityStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}
