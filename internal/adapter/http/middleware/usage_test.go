package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUsageRecordsNormalizedEvent(t *testing.T) {
	recorder := &usageRecorderStub{}
	handler, err := Chain(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	}), Usage(recorder, nil))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/en/groups/42?token=secret", nil)
	request.Pattern = "/en/groups/{id}"
	request.RemoteAddr = "203.0.113.9:54321"
	principal := Principal{ID: "7"}
	ctx := context.WithValue(request.Context(), principalKey{}, principal)
	ctx = context.WithValue(ctx, requestIDKey{}, "request-abc")
	request = request.WithContext(ctx)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if len(recorder.events) != 1 {
		t.Fatalf("events = %d, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Method != http.MethodPost || event.Status != http.StatusCreated || event.Route != "/en/groups/{id}" {
		t.Fatalf("event = %+v, want normalized route and status", event)
	}
	if event.RequestID != "request-abc" {
		t.Fatalf("event request id = %q, want request-abc", event.RequestID)
	}
	if event.IP != "203.0.113.9" {
		t.Fatalf("event IP = %q, want host without port", event.IP)
	}
	if event.UserID == nil || *event.UserID != 7 {
		t.Fatalf("event user = %v, want 7", event.UserID)
	}
}

func TestUsageFallsBackToPathWithoutPattern(t *testing.T) {
	recorder := &usageRecorderStub{}
	handler, err := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Usage(recorder, nil))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if recorder.events[0].Route != "/healthz" {
		t.Fatalf("route = %q, want path fallback", recorder.events[0].Route)
	}
}

func TestUsageRecorderFailureDoesNotChangeResponse(t *testing.T) {
	recorder := &usageRecorderStub{err: errors.New("queue full")}
	handler, err := Chain(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
		_, _ = writer.Write([]byte("short and stout"))
	}), Usage(recorder, nil))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/tea", nil))
	if response.Code != http.StatusTeapot || response.Body.String() != "short and stout" {
		t.Fatalf("response = %d %q, want unchanged 418", response.Code, response.Body.String())
	}
}

func TestUsageNilRecorderIsPassthrough(t *testing.T) {
	handler, err := Chain(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}), Usage(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestRequestOriginHostStripsPort(t *testing.T) {
	if got := requestOriginHost("198.51.100.4:1234"); got != "198.51.100.4" {
		t.Fatalf("requestOriginHost() = %q, want host only", got)
	}
	if got := requestOriginHost(""); got != "" {
		t.Fatalf("requestOriginHost(empty) = %q, want empty", got)
	}
}

type usageRecorderStub struct {
	events []UsageEvent
	err    error
}

func (s *usageRecorderStub) EnqueueUsage(_ context.Context, event UsageEvent) error {
	s.events = append(s.events, event)
	return s.err
}
