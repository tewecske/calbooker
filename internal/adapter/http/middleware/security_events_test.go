package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityEventsEmitsOnlyForDeniedStatuses(t *testing.T) {
	logger := &securityLoggerStub{}
	for _, testCase := range []struct {
		status int
		want   int
	}{
		{status: http.StatusOK, want: 0},
		{status: http.StatusUnauthorized, want: 1},
		{status: http.StatusForbidden, want: 2},
		{status: http.StatusTooManyRequests, want: 3},
		{status: http.StatusNotFound, want: 3},
	} {
		handler, err := Chain(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(testCase.status)
		}), SecurityEvents(logger, func(*http.Request) string { return "/{language}/admin" }))
		if err != nil {
			t.Fatal(err)
		}
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/en/admin", nil))
		if len(logger.events) != testCase.want {
			t.Fatalf("status %d produced %d security events, want %d", testCase.status, len(logger.events), testCase.want)
		}
	}
	last := logger.events[len(logger.events)-1]
	if last.Status != http.StatusTooManyRequests || last.Route != "/{language}/admin" || last.Method != http.MethodGet {
		t.Fatalf("event = %+v, want denied request fields", last)
	}
}

func TestSecurityEventsNilLoggerIsPassthrough(t *testing.T) {
	handler, err := Chain(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}), SecurityEvents(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

type securityLoggerStub struct {
	events []SecurityRequestEvent
}

func (s *securityLoggerStub) LogSecurityRequest(_ context.Context, event SecurityRequestEvent) {
	s.events = append(s.events, event)
}
