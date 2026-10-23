package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/tewecske/calbooker/internal/service"
)

const (
	resendEndpoint         = "https://api.resend.com/emails"
	resendTimeout          = 15 * time.Second
	maxResendResponseBytes = 64 << 10
)

// ErrInvalidResendConfig identifies an unusable Resend API key or sender.
var ErrInvalidResendConfig = errors.New("mail: invalid resend configuration")

// ResendSender delivers messages through the Resend HTTP API
// (https://resend.com/docs/api-reference/emails/send-email). The API key is
// sent only in the Authorization header; it and the message body are never
// logged or included in errors. The sender address must be on a domain
// verified in Resend.
type ResendSender struct {
	apiKey   string
	from     string
	endpoint string
	client   *http.Client
}

var _ service.MailSender = (*ResendSender)(nil)

// NewResendSender validates the API key and the From address used for every
// message.
func NewResendSender(apiKey, from string) (*ResendSender, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || len(apiKey) > 256 || strings.ContainsFunc(apiKey, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, ErrInvalidResendConfig
	}
	address, err := mail.ParseAddress(strings.TrimSpace(from))
	if err != nil || address.Name != "" || strings.ContainsAny(address.Address, "\r\n") {
		return nil, ErrInvalidResendConfig
	}
	return &ResendSender{
		apiKey:   apiKey,
		from:     address.Address,
		endpoint: resendEndpoint,
		client: &http.Client{
			Timeout: resendTimeout,
			// Never follow a redirect: the Authorization header must only ever
			// reach the Resend endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Send delivers one plain-text message, bounded by ctx and a fixed timeout.
func (s *ResendSender) Send(ctx context.Context, message service.Mail) error {
	if s == nil {
		return ErrInvalidMessage
	}
	if ctx == nil {
		return ErrNilContext
	}
	normalized, err := normalizeMessage(message)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"from":    s.from,
		"to":      []string{normalized.To},
		"subject": normalized.Subject,
		"text":    normalized.TextBody,
	})
	if err != nil {
		return ErrInvalidMessage
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("mail: build resend request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+s.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "calbooker")
	response, err := s.client.Do(request)
	if err != nil {
		// The URL error text can carry the endpoint but never the key; keep
		// only the cause class.
		if ctx.Err() != nil {
			return fmt.Errorf("mail: resend request: %w", ctx.Err())
		}
		return errors.New("mail: resend request failed")
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxResendResponseBytes))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("mail: resend rejected message (status %d%s)", response.StatusCode, resendErrorName(body))
}

// resendErrorName returns ", <name>" for Resend's short error code (for
// example validation_error or invalid_api_key) so operators can tell causes
// apart. The free-text message is dropped: it can echo message content.
func resendErrorName(body []byte) string {
	var payload struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Name == "" || len(payload.Name) > 64 {
		return ""
	}
	for _, r := range payload.Name {
		if (r < 'a' || r > 'z') && r != '_' {
			return ""
		}
	}
	return ", " + payload.Name
}
