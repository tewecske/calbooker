package service

import "context"

// Mail is a plain-text transactional message. Credentials and bearer tokens
// must not be included in logs or diagnostics containing a Mail value.
type Mail struct {
	To       string
	Subject  string
	TextBody string
}

// MailSender is the delivery port consumed by services that send messages.
type MailSender interface {
	Send(context.Context, Mail) error
}
