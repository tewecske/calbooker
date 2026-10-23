package middleware

import (
	"context"
	"net/http"
)

// TraceContext is the validated distributed-trace context for one request.
// TraceID and SpanID are opaque hex identifiers with no credentials.
type TraceContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Sampled      bool
}

type traceContextValueKey struct{}

// WithTraceContext stores the request trace context.
func WithTraceContext(ctx context.Context, trace TraceContext) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, traceContextValueKey{}, trace)
}

// TraceContextFromContext returns the request trace context when present.
func TraceContextFromContext(ctx context.Context) (TraceContext, bool) {
	if ctx == nil {
		return TraceContext{}, false
	}
	trace, ok := ctx.Value(traceContextValueKey{}).(TraceContext)
	return trace, ok
}

// SpanFinisher completes one server span with its stable route and status.
type SpanFinisher func(route string, status int)

// SpanTracer starts one server span per request and may continue a trusted
// incoming distributed trace.
type SpanTracer interface {
	StartSpan(context.Context, *http.Request) (context.Context, SpanFinisher)
}

// Span starts one server span for every request. It records the stable route
// name and response status when the request completes.
func Span(tracer SpanTracer, normalize RouteNormalizer) Middleware {
	if tracer == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	if normalize == nil {
		normalize = DefaultRouteNormalizer
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			ctx, finish := tracer.StartSpan(request.Context(), request)
			if ctx == nil {
				ctx = request.Context()
			}
			request = request.WithContext(ctx)
			response := &statusWriter{ResponseWriter: writer}
			if finish != nil {
				defer func() {
					status := response.status
					if status == 0 {
						status = http.StatusOK
					}
					finish(normalize(request), status)
				}()
			}
			next.ServeHTTP(response, request)
		})
	}
}
