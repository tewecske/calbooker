package mail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

const testResendKey = "re_test_1234567890abcdef"

type resendCapture struct {
	method, path, auth, contentType string
	body                            map[string]any
	calls                           int
}

func newResendServer(t *testing.T, status int, response string) (*ResendSender, *resendCapture) {
	t.Helper()
	capture := &resendCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.calls++
		capture.method, capture.path = r.Method, r.URL.Path
		capture.auth, capture.contentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &capture.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	sender, err := NewResendSender(testResendKey, "no-reply@example.test")
	if err != nil {
		t.Fatal(err)
	}
	sender.endpoint = server.URL + "/emails"
	return sender, capture
}

func TestResendSenderPostsTheMessageWithTheKeyInTheAuthorizationHeader(t *testing.T) {
	sender, capture := newResendServer(t, http.StatusOK, `{"id":"abc"}`)
	err := sender.Send(context.Background(), service.Mail{To: "ada@example.test", Subject: "Confirm your email", TextBody: "Open https://app.example.test/confirm?token=t\nThanks"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if capture.method != http.MethodPost || capture.path != "/emails" || capture.auth != "Bearer "+testResendKey || capture.contentType != "application/json" {
		t.Fatalf("request = %s %s auth=%q type=%q", capture.method, capture.path, capture.auth, capture.contentType)
	}
	to, _ := capture.body["to"].([]any)
	if capture.body["from"] != "no-reply@example.test" || len(to) != 1 || to[0] != "ada@example.test" ||
		capture.body["subject"] != "Confirm your email" || capture.body["text"] != "Open https://app.example.test/confirm?token=t\nThanks" {
		t.Fatalf("body = %v", capture.body)
	}
	if strings.Contains(string(mustJSON(t, capture.body)), testResendKey) {
		t.Fatal("the API key was sent in the message body")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestResendSenderRejectionNamesTheCauseButLeaksNeitherKeyNorMessage(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		response string
		want     string
	}{
		{"validation", http.StatusUnprocessableEntity, `{"statusCode":422,"name":"validation_error","message":"The from domain secret-domain.test is not verified"}`, "status 422, validation_error"},
		{"bad key", http.StatusUnauthorized, `{"name":"invalid_api_key","message":"API key is invalid"}`, "status 401, invalid_api_key"},
		{"rate limited", http.StatusTooManyRequests, `{"name":"rate_limit_exceeded","message":"slow down"}`, "status 429, rate_limit_exceeded"},
		{"server error with an html body", http.StatusBadGateway, `<html>upstream said ada@example.test</html>`, "status 502)"},
		{"odd error name is dropped", http.StatusBadRequest, `{"name":"Bad Name; drop table","message":"x"}`, "status 400)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sender, _ := newResendServer(t, test.status, test.response)
			err := sender.Send(context.Background(), service.Mail{To: "ada@example.test", Subject: "Hi", TextBody: "reset token SECRET-TOKEN"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Send() error = %v, want it to contain %q", err, test.want)
			}
			for _, leaked := range []string{testResendKey, "SECRET-TOKEN", "ada@example.test", "secret-domain.test"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error %q leaks %q", err, leaked)
				}
			}
		})
	}
}

func TestResendSenderNeverFollowsRedirectsWithTheKey(t *testing.T) {
	other := &resendCapture{}
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.calls++
		other.auth = r.Header.Get("Authorization")
	}))
	defer elsewhere.Close()
	sender, _ := newResendServer(t, http.StatusOK, "")
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	sender.endpoint = redirector.URL
	if err := sender.Send(context.Background(), service.Mail{To: "ada@example.test", Subject: "Hi", TextBody: "x"}); err == nil {
		t.Fatal("Send() followed a redirect and reported success")
	}
	if other.calls != 0 {
		t.Fatalf("the redirect target received %d requests (auth %q), want none", other.calls, other.auth)
	}
}

func TestResendSenderValidatesMessagesBeforeAnyRequest(t *testing.T) {
	sender, capture := newResendServer(t, http.StatusOK, "")
	for name, message := range map[string]service.Mail{
		"header injection in the recipient": {To: "ada@example.test\r\nBcc: eve@example.test", Subject: "Hi", TextBody: "x"},
		"newline in the subject":            {To: "ada@example.test", Subject: "Hi\r\nBcc: eve@example.test", TextBody: "x"},
		"not an address":                    {To: "not an address", Subject: "Hi", TextBody: "x"},
	} {
		if err := sender.Send(context.Background(), message); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("Send(%s) error = %v, want %v", name, err, ErrInvalidMessage)
		}
	}
	if capture.calls != 0 {
		t.Fatalf("invalid messages reached the API %d times", capture.calls)
	}
}

func TestResendSenderStopsWhenTheContextIsCanceled(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	sender, _ := newResendServer(t, http.StatusOK, "")
	sender.endpoint = slow.URL
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := sender.Send(ctx, service.Mail{To: "ada@example.test", Subject: "Hi", TextBody: "x"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("Send() = %v after %s, want a prompt deadline error", err, time.Since(start))
	}
	if strings.Contains(err.Error(), testResendKey) {
		t.Fatal("error leaks the API key")
	}
}

func TestNewResendSenderValidatesItsInputs(t *testing.T) {
	for name, test := range map[string]struct{ key, from string }{
		"empty key":          {"", "no-reply@example.test"},
		"key with a space":   {"re_abc def", "no-reply@example.test"},
		"key with a newline": {"re_abc\ndef", "no-reply@example.test"},
		"oversized key":      {"re_" + strings.Repeat("a", 300), "no-reply@example.test"},
		"display name":       {testResendKey, "App <no-reply@example.test>"},
		"not an address":     {testResendKey, "not an address"},
	} {
		if _, err := NewResendSender(test.key, test.from); !errors.Is(err, ErrInvalidResendConfig) {
			t.Fatalf("NewResendSender(%s) error = %v, want %v", name, err, ErrInvalidResendConfig)
		}
	}
	if sender, err := NewResendSender("  "+testResendKey+"\n", " no-reply@example.test "); err != nil || sender.apiKey != testResendKey || sender.from != "no-reply@example.test" {
		t.Fatalf("NewResendSender(padded) = %+v, %v; want trimmed values", sender, err)
	}
}
