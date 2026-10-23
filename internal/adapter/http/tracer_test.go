package httpadapter

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
)

func TestSpanTracerContinuesTrustedIncomingTrace(t *testing.T) {
	tracer := NewSpanTracer(SpanTracerConfig{TrustedProxy: "10.0.0.0/8"})
	request := httptest.NewRequest(http.MethodGet, "/en/", nil)
	request.RemoteAddr = "10.1.2.3:5555"
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	ctx, _ := tracer.StartSpan(context.Background(), request)
	trace, ok := middleware.TraceContextFromContext(ctx)
	if !ok {
		t.Fatal("trace context missing")
	}
	if trace.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || trace.ParentSpanID != "00f067aa0ba902b7" || !trace.Sampled {
		t.Fatalf("trace = %+v, want continued parent context", trace)
	}
	if trace.SpanID == trace.ParentSpanID || len(trace.SpanID) != 16 {
		t.Fatalf("span id = %q, want a fresh 16-character span", trace.SpanID)
	}
}

func TestSpanTracerStartsFreshTraceForUntrustedCaller(t *testing.T) {
	tracer := NewSpanTracer(SpanTracerConfig{TrustedProxy: "10.0.0.0/8"})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.9:1111"
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	ctx, _ := tracer.StartSpan(context.Background(), request)
	trace, _ := middleware.TraceContextFromContext(ctx)
	if trace.TraceID == "4bf92f3577b34da6a3ce929d0e0e4736" || trace.ParentSpanID != "" {
		t.Fatalf("trace = %+v, want untrusted caller to start a fresh trace", trace)
	}
	if !validHex(trace.TraceID, 32) {
		t.Fatalf("generated trace id = %q, want 32 hex characters", trace.TraceID)
	}
}

func TestSpanTracerRejectsMalformedIncomingTrace(t *testing.T) {
	tracer := NewSpanTracer(SpanTracerConfig{TrustedProxy: "10.0.0.0/8"})
	for _, header := range []string{
		"",
		"00-nothex-nothex-01",
		"00-00000000000000000000000000000000-0000000000000000-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = "10.0.0.1:1234"
		if header != "" {
			request.Header.Set("traceparent", header)
		}
		ctx, _ := tracer.StartSpan(context.Background(), request)
		trace, _ := middleware.TraceContextFromContext(ctx)
		if trace.ParentSpanID != "" {
			t.Fatalf("header %q continued an invalid trace: %+v", header, trace)
		}
	}
}

func TestSpanTracerLogsStableRouteWithoutQuery(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	tracer := NewSpanTracer(SpanTracerConfig{Logger: logger})
	request := httptest.NewRequest(http.MethodGet, "/en/groups/1?token=secret", nil)
	request.Pattern = "/en/groups/{id}"

	_, finish := tracer.StartSpan(context.Background(), request)
	finish("/{language}/groups/{id}", http.StatusOK)

	logged := buffer.String()
	for _, want := range []string{"server span", "/{language}/groups/{id}", `"status":200`, `"trace_id"`} {
		if !strings.Contains(logged, want) {
			t.Errorf("span log missing %q: %s", want, logged)
		}
	}
	if strings.Contains(logged, "secret") || strings.Contains(logged, "?") {
		t.Errorf("span log leaked query data: %s", logged)
	}
}

func TestSpanTracerHandlesNilInputs(t *testing.T) {
	var tracer *SpanTracer
	if ctx, finish := tracer.StartSpan(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil)); ctx == nil || finish != nil {
		t.Fatalf("nil tracer StartSpan = %v/%v", ctx, finish)
	}
	valid := NewSpanTracer(SpanTracerConfig{})
	ctx, finish := valid.StartSpan(nil, httptest.NewRequest(http.MethodGet, "/", nil))
	if ctx != nil || finish != nil {
		t.Fatalf("nil context StartSpan = %v/%v", ctx, finish)
	}
}
