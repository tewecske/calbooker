package mail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

// fakeRelay accepts one SMTP session and records its envelope and data.
type fakeRelay struct {
	listener net.Listener
	envelope chan []string
	data     chan string
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	relay := &fakeRelay{listener: listener, envelope: make(chan []string, 1), data: make(chan string, 1)}
	go relay.serve()
	return relay
}

func (r *fakeRelay) serve() {
	conn, err := r.listener.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	reply := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	reply("220 fake ESMTP")
	var envelope []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimRight(line, "\r\n")
		switch upper := strings.ToUpper(command); {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			reply("250 fake")
		case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"):
			envelope = append(envelope, command)
			reply("250 ok")
		case upper == "DATA":
			reply("354 go ahead")
			var data strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				data.WriteString(dataLine)
			}
			r.envelope <- envelope
			r.data <- data.String()
			reply("250 queued")
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 unsupported")
		}
	}
}

func TestSMTPSenderDeliversEncodedPlainTextMessage(t *testing.T) {
	relay := newFakeRelay(t)
	sender, err := NewSMTPSender("smtp://"+relay.listener.Addr().String(), "no-reply@example.test")
	if err != nil {
		t.Fatalf("NewSMTPSender() error = %v", err)
	}
	body := "Szia!\nOpen this link:\n.hidden line starts with a dot\nhttps://example.test/en/confirm-email?token=abc"
	if err := sender.Send(context.Background(), service.Mail{To: "User@Example.Test", Subject: "Erősítse meg az e-mail címét", TextBody: body}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	envelope := <-relay.envelope
	if len(envelope) != 2 || envelope[0] != "MAIL FROM:<no-reply@example.test>" || envelope[1] != "RCPT TO:<user@example.test>" {
		t.Fatalf("envelope = %q, want normalized sender and recipient", envelope)
	}
	// The SMTP client dot-stuffs lines starting with "."; undo it as a server would.
	raw := strings.ReplaceAll(<-relay.data, "\r\n..", "\r\n.")
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("relay received an unparsable message: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || subject != "Erősítse meg az e-mail címét" {
		t.Fatalf("Subject = %q (%v), want UTF-8 subject round trip", subject, err)
	}
	if message.Header.Get("From") != "no-reply@example.test" || message.Header.Get("To") != "user@example.test" {
		t.Fatalf("From/To = %q/%q", message.Header.Get("From"), message.Header.Get("To"))
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// SMTP terminates the data with a line break before the closing dot.
	if got := strings.TrimSuffix(strings.ReplaceAll(string(decoded), "\r\n", "\n"), "\n"); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestSMTPSenderRejectsUnsafeConfigurationAndUnreachableRelay(t *testing.T) {
	for _, relay := range []string{"http://mail.example.test", "smtp://user:secret@mail.example.test", "smtp://"} {
		if _, err := NewSMTPSender(relay, "no-reply@example.test"); !errors.Is(err, ErrInvalidRelay) {
			t.Fatalf("NewSMTPSender(%q) error = %v, want ErrInvalidRelay", relay, err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	sender, err := NewSMTPSender("smtp://"+address, "no-reply@example.test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sender.Send(ctx, service.Mail{To: "user@example.test", Subject: "Hi", TextBody: "body"}); err == nil {
		t.Fatal("Send() to a closed relay succeeded, want error")
	}
}

func TestDevelopmentSenderKeepsOnlyRecentMessages(t *testing.T) {
	sender := &DevelopmentSender{}
	for i := range maxDevelopmentMessages + 5 {
		if err := sender.Send(context.Background(), service.Mail{To: "user@example.test", Subject: "message", TextBody: string(rune('a' + i%26))}); err != nil {
			t.Fatal(err)
		}
	}
	messages := sender.Messages()
	if len(messages) != maxDevelopmentMessages || messages[len(messages)-1].TextBody != string(rune('a'+(maxDevelopmentMessages+4)%26)) {
		t.Fatalf("retained %d messages ending %q, want the newest %d", len(messages), messages[len(messages)-1].TextBody, maxDevelopmentMessages)
	}
}
