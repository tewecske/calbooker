package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPUsesForwardedForOnlyFromTrustedProxy(t *testing.T) {
	tests := []struct {
		name         string
		trustedProxy string
		remoteAddr   string
		forwardedFor []string
		wantClientIP string
	}{
		{name: "direct peer drops port", remoteAddr: "203.0.113.5:51234", wantClientIP: "203.0.113.5"},
		{name: "direct IPv6 peer drops port", remoteAddr: "[2001:db8::7]:443", wantClientIP: "2001:db8::7"},
		{name: "untrusted peer cannot spoof", trustedProxy: "10.0.0.1", remoteAddr: "203.0.113.5:1", forwardedFor: []string{"198.51.100.9"}, wantClientIP: "203.0.113.5"},
		{name: "no trusted proxy configured ignores header", remoteAddr: "10.0.0.1:1", forwardedFor: []string{"198.51.100.9"}, wantClientIP: "10.0.0.1"},
		{name: "trusted proxy uses appended client hop", trustedProxy: "10.0.0.1", remoteAddr: "10.0.0.1:9000", forwardedFor: []string{"198.51.100.66, 203.0.113.5"}, wantClientIP: "203.0.113.5"},
		{name: "chained proxies in trusted network are skipped", trustedProxy: "10.0.0.0/8", remoteAddr: "10.0.0.1:9000", forwardedFor: []string{"198.51.100.66", "203.0.113.5, 10.2.3.4"}, wantClientIP: "203.0.113.5"},
		{name: "malformed hop stops at the proxy", trustedProxy: "10.0.0.1", remoteAddr: "10.0.0.1:9000", forwardedFor: []string{"198.51.100.66, not-an-ip"}, wantClientIP: "10.0.0.1"},
		{name: "trusted proxy without header", trustedProxy: "10.0.0.1", remoteAddr: "10.0.0.1:9000", wantClientIP: "10.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got string
			handler := ClientIP(test.trustedProxy)(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				got, _ = ClientIPFromContext(request.Context())
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = test.remoteAddr
			for _, value := range test.forwardedFor {
				request.Header.Add("X-Forwarded-For", value)
			}
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if got != test.wantClientIP {
				t.Fatalf("client IP = %q, want %q", got, test.wantClientIP)
			}
		})
	}
}
