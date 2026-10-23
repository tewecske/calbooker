package middleware

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const usageEnqueueTimeout = 100 * time.Millisecond

// UsageEvent is one normalized request observation recorded for operations.
// It never carries a query string, credential, or bearer value. RequestID links
// the asynchronous record back to the request that caused it.
type UsageEvent struct {
	Method    string
	Route     string
	Status    int
	RequestID string
	TraceID   string
	UserID    *int64
	IP        string
}

// UsageRecorder enqueues one usage event. A queue-full or storage failure must
// never fail the user request.
type UsageRecorder interface {
	EnqueueUsage(context.Context, UsageEvent) error
}

// RouteNormalizer derives a low-cardinality route name from a request.
type RouteNormalizer func(*http.Request) string

// Usage records one normalized request event after the handler completes. A
// full queue applies bounded backpressure and never changes the response.
func Usage(recorder UsageRecorder, normalize RouteNormalizer) Middleware {
	if recorder == nil {
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
			if status == 0 {
				status = http.StatusOK
			}
			event := UsageEvent{
				Method:    request.Method,
				Route:     normalize(request),
				Status:    status,
				RequestID: requestID(request.Context()),
				IP:        requestOriginHost(request.RemoteAddr),
			}
			if trace, ok := TraceContextFromContext(request.Context()); ok {
				event.TraceID = trace.TraceID
			}
			if principal, ok := PrincipalFromContext(request.Context()); ok {
				if id, err := strconv.ParseInt(principal.ID, 10, 64); err == nil && id > 0 {
					event.UserID = &id
				}
			}
			ctx, cancel := context.WithTimeout(request.Context(), usageEnqueueTimeout)
			defer cancel()
			_ = recorder.EnqueueUsage(ctx, event)
		})
	}
}

// DefaultRouteNormalizer uses the matched server pattern, which already
// replaces record identifiers with placeholders. It falls back to the path.
func DefaultRouteNormalizer(request *http.Request) string {
	if request == nil {
		return ""
	}
	if request.Pattern != "" {
		return request.Pattern
	}
	if request.URL != nil {
		return request.URL.Path
	}
	return ""
}

func requestOriginHost(remoteAddr string) string {
	trimmed := strings.TrimSpace(remoteAddr)
	if trimmed == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		return host
	}
	return trimmed
}
