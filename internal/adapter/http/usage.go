package httpadapter

import (
	"context"
	"net/http"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// NewUsageRecorder adapts the usage queue to the middleware recorder port.
// A nil queue returns a nil recorder so usage recording stays optional.
func NewUsageRecorder(queue *service.UsageQueue) middleware.UsageRecorder {
	if queue == nil {
		return nil
	}
	return usageRecorder{queue: queue}
}

type usageRecorder struct {
	queue *service.UsageQueue
}

// EnqueueUsage converts the middleware event into the persistence model.
func (r usageRecorder) EnqueueUsage(ctx context.Context, event middleware.UsageEvent) error {
	converted := service.UsageEvent{
		Method:    event.Method,
		Route:     event.Route,
		Status:    event.Status,
		RequestID: event.RequestID,
		TraceID:   event.TraceID,
		UserID:    event.UserID,
	}
	if event.IP != "" {
		origin := event.IP
		converted.IP = &origin
	}
	return r.queue.EnqueueUsage(ctx, converted)
}

// NormalizeUsageRoute replaces the language prefix with a stable placeholder so
// one feature aggregates across every supported language.
func NormalizeUsageRoute(request *http.Request) string {
	route := middleware.DefaultRouteNormalizer(request)
	if _, rest, ok := locale.ParsePath(route); ok {
		return "/{language}" + rest
	}
	return route
}
