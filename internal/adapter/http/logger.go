package httpadapter

import (
	"context"
	"log/slog"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

// Logger emits structured application and security events.
type Logger struct {
	application *slog.Logger
	security    *slog.Logger
}

// NewLogger creates a logger with independently identifiable output streams.
// A nil security logger reuses application output with a security stream field.
func NewLogger(application, security *slog.Logger) *Logger {
	if application == nil {
		application = slog.Default()
	}
	if security == nil {
		security = application
	}
	return &Logger{application: application, security: security}
}

// Application returns application logger for lifecycle events.
func (l *Logger) Application() *slog.Logger {
	return l.application
}

// LogRequest implements middleware.RequestLogger with safe stable fields.
func (l *Logger) LogRequest(ctx context.Context, event middleware.RequestEvent) {
	l.application.InfoContext(ctx, "http request",
		"request_id", event.RequestID,
		"method", event.Method,
		"status", event.Status,
		"duration_ms", event.Duration.Milliseconds(),
	)
}

// SecurityEvent contains stable security-event fields without credentials.
type SecurityEvent struct {
	RequestID   string
	Action      string
	Outcome     string
	ActorUserID int64
	TargetType  string
	TargetID    string
}

// LogSecurity emits an identifiable security event without sensitive values.
// Actor and target are internal numeric identifiers, never emails or
// provider subjects.
func (l *Logger) LogSecurity(ctx context.Context, event SecurityEvent) {
	l.security.WarnContext(ctx, "security event",
		"stream", "security",
		"request_id", event.RequestID,
		"action", event.Action,
		"outcome", event.Outcome,
		"actor_user_id", event.ActorUserID,
		"target_type", event.TargetType,
		"target_id", event.TargetID,
	)
}

// LogSecurityRequest implements middleware.SecurityRequestLogger. Denied and
// rate-limited requests are emitted only on the security stream.
func (l *Logger) LogSecurityRequest(ctx context.Context, event middleware.SecurityRequestEvent) {
	l.security.WarnContext(ctx, "security event",
		"stream", "security",
		"event", "http_denied",
		"request_id", event.RequestID,
		"method", event.Method,
		"route", event.Route,
		"status", event.Status,
	)
}

// AuditSecuritySink mirrors administrator actions to the security log stream
// with actor and target identifiers. It never includes emails, details,
// origins, credentials, or tokens.
type AuditSecuritySink struct {
	logger *Logger
}

// NewAuditSecuritySink constructs a security-log sink over logger.
func NewAuditSecuritySink(logger *Logger) *AuditSecuritySink {
	return &AuditSecuritySink{logger: logger}
}

// AuditRecorded implements service.AuditSink.
func (s *AuditSecuritySink) AuditRecorded(ctx context.Context, record service.AuditRecord, outcome string) {
	if s == nil || s.logger == nil {
		return
	}
	requestID := ""
	if id, ok := middleware.RequestIDFromContext(ctx); ok {
		requestID = id
	}
	s.logger.LogSecurity(ctx, SecurityEvent{
		RequestID:   requestID,
		Action:      record.Action,
		Outcome:     outcome,
		ActorUserID: record.ActorUserID,
		TargetType:  record.TargetType,
		TargetID:    record.TargetID,
	})
}

var _ service.AuditSink = (*AuditSecuritySink)(nil)

var _ middleware.RequestLogger = (*Logger)(nil)

var _ middleware.SecurityRequestLogger = (*Logger)(nil)
