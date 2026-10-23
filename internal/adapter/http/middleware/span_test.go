package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpanStartsAndFinishesWithRouteAndStatus(t *testing.T) {
	tracer := &spanTracerStub{}
	handler, err := Chain(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, ok := TraceContextFromContext(request.Context()); !ok {
			t.Error("trace context missing inside handler")
		}
		writer.WriteHeader(http.StatusCreated)
	}), Span(tracer, func(*http.Request) string { return "/{language}/groups/{id}" }))
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/en/groups/1", nil))

	if tracer.route != "/{language}/groups/{id}" || tracer.status != http.StatusCreated {
		t.Fatalf("finish = %q/%d, want normalized route and status", tracer.route, tracer.status)
	}
}

func TestSpanFinishesWithOKWhenHandlerWritesNothing(t *testing.T) {
	tracer := &spanTracerStub{}
	handler, err := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Span(tracer, nil))
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if tracer.status != http.StatusOK {
		t.Fatalf("status = %d, want %d", tracer.status, http.StatusOK)
	}
}

func TestSpanNilTracerIsPassthrough(t *testing.T) {
	handler, err := Chain(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}), Span(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

type spanTracerStub struct {
	route  string
	status int
}

func (s *spanTracerStub) StartSpan(ctx context.Context, _ *http.Request) (context.Context, SpanFinisher) {
	ctx = WithTraceContext(ctx, TraceContext{TraceID: "trace", SpanID: "span"})
	return ctx, func(route string, status int) {
		s.route = route
		s.status = status
	}
}
