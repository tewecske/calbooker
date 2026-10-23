package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

const smtpTimeout = 15 * time.Second

// ErrInvalidRelay identifies an unusable relay URL or sender address.
var ErrInvalidRelay = errors.New("mail: invalid relay")

// SMTPSender delivers messages through a credential-free SMTP relay.
// smtp:// relays upgrade with STARTTLS when the server offers it; smtps://
// relays use implicit TLS. Message bodies are never logged.
type SMTPSender struct {
	address  string
	host     string
	implicit bool
	from     string
}

var _ service.MailSender = (*SMTPSender)(nil)

// NewSMTPSender parses relay (smtp://host:port or smtps://host:port) and the
// From address used for every message.
func NewSMTPSender(relay, from string) (*SMTPSender, error) {
	parsed, err := url.Parse(strings.TrimSpace(relay))
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return nil, ErrInvalidRelay
	}
	sender := &SMTPSender{host: parsed.Hostname()}
	port := parsed.Port()
	switch parsed.Scheme {
	case "smtp":
		if port == "" {
			port = "25"
		}
	case "smtps":
		sender.implicit = true
		if port == "" {
			port = "465"
		}
	default:
		return nil, ErrInvalidRelay
	}
	address, err := mail.ParseAddress(strings.TrimSpace(from))
	if err != nil || strings.ContainsAny(address.Address, "\r\n") {
		return nil, ErrInvalidRelay
	}
	sender.address = net.JoinHostPort(sender.host, port)
	sender.from = address.Address
	return sender, nil
}

// Send delivers one plain-text message, bounded by ctx and a fixed timeout.
func (s *SMTPSender) Send(ctx context.Context, message service.Mail) error {
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
	body, err := s.compose(normalized, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	conn, err := s.dial(ctx)
	if err != nil {
		return fmt.Errorf("mail: connect relay: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("mail: relay greeting: %w", err)
	}
	defer func() { _ = client.Close() }()
	if !s.implicit {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		}
	}
	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("mail: relay rejected sender: %w", err)
	}
	if err := client.Rcpt(normalized.To); err != nil {
		return fmt.Errorf("mail: relay rejected recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: relay data: %w", err)
	}
	if _, err := writer.Write(body); err != nil {
		_ = writer.Close()
		return fmt.Errorf("mail: write message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("mail: relay refused message: %w", err)
	}
	return client.Quit()
}

func (s *SMTPSender) dial(ctx context.Context) (net.Conn, error) {
	if s.implicit {
		dialer := &tls.Dialer{Config: &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}}
		return dialer.DialContext(ctx, "tcp", s.address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", s.address)
}

// compose renders RFC 5322 headers and a quoted-printable UTF-8 body. The
// recipient and subject are validated by normalizeMessage, so they cannot
// inject headers.
func (s *SMTPSender) compose(message service.Mail, now time.Time) ([]byte, error) {
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "From: %s\r\n", s.from)
	fmt.Fprintf(&buffer, "To: %s\r\n", message.To)
	fmt.Fprintf(&buffer, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", message.Subject))
	fmt.Fprintf(&buffer, "Date: %s\r\n", now.UTC().Format(time.RFC1123Z))
	buffer.WriteString("MIME-Version: 1.0\r\n")
	buffer.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buffer.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	// In text mode the encoder writes every line ending as CRLF.
	encoder := quotedprintable.NewWriter(&buffer)
	if _, err := encoder.Write([]byte(message.TextBody)); err != nil {
		return nil, ErrInvalidMessage
	}
	if err := encoder.Close(); err != nil {
		return nil, ErrInvalidMessage
	}
	return buffer.Bytes(), nil
}
