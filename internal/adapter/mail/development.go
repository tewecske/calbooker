// Package mail provides mail delivery adapters.
package mail

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/tewecske/calbooker/internal/service"
)

var (
	// ErrInvalidMessage identifies a message that cannot be safely delivered.
	ErrInvalidMessage = errors.New("mail: invalid message")
	// ErrNilContext identifies a missing send context.
	ErrNilContext = errors.New("mail: nil context")
)

// maxDevelopmentMessages bounds DevelopmentSender memory; older messages are
// dropped first.
const maxDevelopmentMessages = 100

// DevelopmentSender retains the most recent messages in memory instead of
// delivering them. It is intended for local development and tests; it emits no
// logs.
type DevelopmentSender struct {
	mu       sync.RWMutex
	messages []service.Mail
}

var _ service.MailSender = (*DevelopmentSender)(nil)

// Send validates and stores one message unless ctx is canceled.
func (s *DevelopmentSender) Send(ctx context.Context, message service.Mail) error {
	if s == nil {
		return ErrInvalidMessage
	}
	if ctx == nil {
		return ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	normalized, err := normalizeMessage(message)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) >= maxDevelopmentMessages {
		s.messages = append(s.messages[:0], s.messages[len(s.messages)-maxDevelopmentMessages+1:]...)
	}
	s.messages = append(s.messages, normalized)
	return nil
}

// Messages returns a snapshot of messages accepted by Send.
func (s *DevelopmentSender) Messages() []service.Mail {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]service.Mail(nil), s.messages...)
}

func normalizeMessage(message service.Mail) (service.Mail, error) {
	recipient, err := service.NormalizeEmail(message.To)
	if err != nil || recipient == "" {
		return service.Mail{}, ErrInvalidMessage
	}
	subject := strings.TrimSpace(message.Subject)
	if subject == "" || strings.ContainsAny(subject, "\r\n") || message.TextBody == "" {
		return service.Mail{}, ErrInvalidMessage
	}
	message.To = recipient
	message.Subject = subject
	return message, nil
}
