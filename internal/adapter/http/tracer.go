package httpadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
)

const traceparentHeader = "traceparent"

// SpanTracerConfig configures the in-house server tracer.
type SpanTracerConfig struct {
	// Logger receives completed spans. A nil logger disables span output.
	Logger *slog.Logger
	// TrustedProxy is an IP address or CIDR network. Only requests from a
	// trusted proxy may continue an incoming distributed trace.
	TrustedProxy string
}

// SpanTracer starts one server span per request and continues a trusted
// incoming W3C trace context. It never logs query strings or credentials.
type SpanTracer struct {
	logger       *slog.Logger
	trustedProxy *net.IPNet
	trustedIP    net.IP
	now          func() time.Time
}

// NewSpanTracer constructs the server tracer.
func NewSpanTracer(config SpanTracerConfig) *SpanTracer {
	tracer := &SpanTracer{logger: config.Logger, now: time.Now}
	trusted := strings.TrimSpace(config.TrustedProxy)
	if trusted != "" {
		if _, network, err := net.ParseCIDR(trusted); err == nil {
			tracer.trustedProxy = network
		} else if ip := net.ParseIP(trusted); ip != nil {
			tracer.trustedIP = ip
		}
	}
	return tracer
}

// StartSpan starts a server span and returns a finisher using the stable route
// and status. Only a trusted caller's valid trace context is continued.
func (t *SpanTracer) StartSpan(ctx context.Context, request *http.Request) (context.Context, middleware.SpanFinisher) {
	if t == nil || ctx == nil || request == nil {
		return ctx, nil
	}
	trace := t.newTraceContext(request)
	ctx = middleware.WithTraceContext(ctx, trace)
	startedAt := t.now()
	method := request.Method
	return ctx, func(route string, status int) {
		if t.logger == nil {
			return
		}
		t.logger.InfoContext(ctx, "server span",
			"trace_id", trace.TraceID,
			"span_id", trace.SpanID,
			"parent_span_id", trace.ParentSpanID,
			"method", method,
			"route", route,
			"status", status,
			"duration_ms", t.now().Sub(startedAt).Milliseconds(),
		)
	}
}

func (t *SpanTracer) newTraceContext(request *http.Request) middleware.TraceContext {
	if trace, ok := t.continueTrace(request); ok {
		return trace
	}
	return middleware.TraceContext{
		TraceID: randomHex(16),
		SpanID:  randomHex(8),
		Sampled: true,
	}
}

func (t *SpanTracer) continueTrace(request *http.Request) (middleware.TraceContext, bool) {
	if t == nil || !t.isTrusted(request.RemoteAddr) {
		return middleware.TraceContext{}, false
	}
	header := strings.TrimSpace(request.Header.Get(traceparentHeader))
	traceID, parentSpanID, sampled, ok := parseTraceparent(header)
	if !ok {
		return middleware.TraceContext{}, false
	}
	return middleware.TraceContext{
		TraceID:      traceID,
		SpanID:       randomHex(8),
		ParentSpanID: parentSpanID,
		Sampled:      sampled,
	}, true
}

func (t *SpanTracer) isTrusted(remoteAddr string) bool {
	host := remoteAddr
	if parsedHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = parsedHost
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	if t.trustedProxy != nil && t.trustedProxy.Contains(ip) {
		return true
	}
	return t.trustedIP != nil && t.trustedIP.Equal(ip)
}

func parseTraceparent(header string) (traceID, spanID string, sampled, ok bool) {
	if len(header) != 55 || header[2] != '-' || header[35] != '-' || header[52] != '-' {
		return "", "", false, false
	}
	if header[0:2] != "00" {
		return "", "", false, false
	}
	traceID = header[3:35]
	spanID = header[36:52]
	flags := header[53:55]
	if !validHex(traceID, 32) || !validHex(spanID, 16) || !validHex(flags, 2) {
		return "", "", false, false
	}
	if allZero(traceID) || allZero(spanID) {
		return "", "", false, false
	}
	parsedFlags, err := strconv.ParseUint(flags, 16, 8)
	if err != nil {
		return "", "", false, false
	}
	return traceID, spanID, parsedFlags&1 == 1, true
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func allZero(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] != '0' {
			return false
		}
	}
	return true
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return strings.Repeat("0", bytes*2)
	}
	return hex.EncodeToString(buffer)
}
