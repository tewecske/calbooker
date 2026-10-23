package mail

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestDevelopmentSenderStoresValidatedMessage(t *testing.T) {
	sender := &DevelopmentSender{}
	input := service.Mail{
		To:       " User@Example.COM ",
		Subject:  "  Confirm account  ",
		TextBody: "Use this link once.",
	}

	if err := sender.Send(context.Background(), input); err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}

	messages := sender.Messages()
	if len(messages) != 1 {
		t.Fatalf("Messages() length = %d, want 1", len(messages))
	}
	want := service.Mail{
		To:       "user@example.com",
		Subject:  "Confirm account",
		TextBody: input.TextBody,
	}
	if messages[0] != want {
		t.Errorf("Messages()[0] = %#v, want %#v", messages[0], want)
	}

	messages[0].Subject = "changed"
	if got := sender.Messages()[0].Subject; got != want.Subject {
		t.Errorf("Messages() exposed internal state: subject = %q, want %q", got, want.Subject)
	}
}

func TestDevelopmentSenderRejectsInvalidMessages(t *testing.T) {
	tests := []struct {
		name    string
		message service.Mail
	}{
		{name: "empty recipient", message: service.Mail{Subject: "subject", TextBody: "body"}},
		{name: "display recipient", message: service.Mail{To: "User <user@example.com>", Subject: "subject", TextBody: "body"}},
		{name: "empty subject", message: service.Mail{To: "user@example.com", TextBody: "body"}},
		{name: "header subject", message: service.Mail{To: "user@example.com", Subject: "subject\r\nBcc: attacker@example.com", TextBody: "body"}},
		{name: "empty body", message: service.Mail{To: "user@example.com", Subject: "subject"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := (&DevelopmentSender{}).Send(context.Background(), test.message); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("Send() error = %v, want errors.Is(_, ErrInvalidMessage)", err)
			}
		})
	}
}

func TestDevelopmentSenderRejectsNilOrCanceledContext(t *testing.T) {
	sender := &DevelopmentSender{}
	message := service.Mail{To: "user@example.com", Subject: "subject", TextBody: "body"}

	if err := sender.Send(nil, message); !errors.Is(err, ErrNilContext) {
		t.Fatalf("Send(nil) error = %v, want errors.Is(_, ErrNilContext)", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sender.Send(ctx, message); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send(canceled) error = %v, want context.Canceled", err)
	}
	if len(sender.Messages()) != 0 {
		t.Error("canceled Send() stored a message")
	}
}

func TestDevelopmentSenderRejectsNilSender(t *testing.T) {
	var sender *DevelopmentSender
	if err := sender.Send(context.Background(), service.Mail{}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("Send() error = %v, want errors.Is(_, ErrInvalidMessage)", err)
	}
}
