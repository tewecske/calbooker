package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultUsageQueueCapacity bounds how many pending events are held.
	DefaultUsageQueueCapacity = 1024
	// DefaultUsageRetryAttempts bounds retries for one storage failure.
	DefaultUsageRetryAttempts = 3
	// DefaultUsageRetryBackoff is the delay between storage retries.
	DefaultUsageRetryBackoff = 50 * time.Millisecond
	// MaxUsageRouteLength bounds a normalized route.
	MaxUsageRouteLength = 255
	// MaxUsageMethodLength bounds a request method.
	MaxUsageMethodLength = 8
	// MaxUsageOriginLength bounds a request origin.
	MaxUsageOriginLength = 64
	// MaxUsageRequestIDLength bounds the linked request identifier.
	MaxUsageRequestIDLength = 64
)

var (
	// ErrNilUsageRepository identifies missing usage-event persistence.
	ErrNilUsageRepository = errors.New("service: nil usage repository")
	// ErrInvalidUsageConfig identifies unusable queue settings.
	ErrInvalidUsageConfig = errors.New("service: invalid usage config")
	// ErrInvalidUsageEvent identifies an event that cannot be stored safely.
	ErrInvalidUsageEvent = errors.New("service: invalid usage event")
	// ErrUsageQueueFull identifies backpressure that exceeded its bounded wait.
	ErrUsageQueueFull = errors.New("service: usage queue full")
)

// UsageEvent is one normalized request observation. It never carries a query
// string, credential, or bearer value. RequestID links the asynchronous record
// back to the request that caused it.
type UsageEvent struct {
	CreatedAt int64
	Method    string
	Route     string
	Status    int
	RequestID string
	TraceID   string
	UserID    *int64
	IP        *string
}

// UsageEventRepository stores normalized request events.
type UsageEventRepository interface {
	RecordUsageEvent(context.Context, UsageEvent) error
}

// UsageQueueStats reports live queue health without exposing event contents.
type UsageQueueStats struct {
	Capacity int
	Pending  int
	Recorded uint64
	Failed   uint64
	Dropped  uint64
}

// UsageQueue buffers normalized request events and records them outside the
// request's main work. A full queue applies backpressure instead of dropping
// silently, and a storage failure never fails the user request.
type UsageQueue struct {
	repository UsageEventRepository
	events     chan UsageEvent
	logger     *slog.Logger
	now        func() time.Time
	retries    int
	backoff    time.Duration

	recorded atomic.Uint64
	failed   atomic.Uint64
	dropped  atomic.Uint64

	mu     sync.Mutex
	closed bool
}

// NewUsageQueue constructs a bounded usage-event queue.
func NewUsageQueue(repository UsageEventRepository, capacity int) (*UsageQueue, error) {
	if repository == nil {
		return nil, ErrNilUsageRepository
	}
	if capacity <= 0 {
		return nil, ErrInvalidUsageConfig
	}
	return &UsageQueue{
		repository: repository,
		events:     make(chan UsageEvent, capacity),
		now:        time.Now,
		retries:    DefaultUsageRetryAttempts,
		backoff:    DefaultUsageRetryBackoff,
	}, nil
}

// SetLogger attaches a structured logger for queue health events.
func (q *UsageQueue) SetLogger(logger *slog.Logger) {
	if q == nil {
		return
	}
	q.logger = logger
}

// EnqueueUsage adds one event to the queue. When the queue is full it blocks
// until space is available or ctx is done, applying backpressure. It never
// fails the caller's request.
func (q *UsageQueue) EnqueueUsage(ctx context.Context, event UsageEvent) error {
	if q == nil || q.repository == nil || q.now == nil {
		return ErrInvalidUsageConfig
	}
	if ctx == nil {
		return errors.New("service: nil usage context")
	}
	normalized, err := normalizeUsageEvent(event, q.now().Unix())
	if err != nil {
		return err
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrInvalidUsageConfig
	}
	events := q.events
	q.mu.Unlock()

	select {
	case events <- normalized:
		return nil
	case <-ctx.Done():
		q.dropped.Add(1)
		if q.logger != nil {
			q.logger.Warn("usage event dropped under backpressure", "reason", ctx.Err())
		}
		return ErrUsageQueueFull
	}
}

// Run records queued events until ctx is cancelled. It stays alive after an
// individual storage failure.
func (q *UsageQueue) Run(ctx context.Context) error {
	if q == nil || q.repository == nil {
		return ErrInvalidUsageConfig
	}
	if ctx == nil {
		return errors.New("service: nil usage context")
	}
	defer q.close()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-q.events:
			q.persist(ctx, event)
		}
	}
}

func (q *UsageQueue) persist(ctx context.Context, event UsageEvent) {
	attempts := q.retries
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if err := q.repository.RecordUsageEvent(ctx, event); err == nil {
			q.recorded.Add(1)
			return
		}
		if ctx.Err() != nil {
			break
		}
		if attempt < attempts-1 && q.backoff > 0 {
			timer := time.NewTimer(q.backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				break
			case <-timer.C:
			}
		}
	}
	q.failed.Add(1)
	if q.logger != nil {
		q.logger.Error("usage event storage failed after retries", "job", "usage_event", "request_id", event.RequestID)
	}
}

// Stats returns a live snapshot of queue health.
func (q *UsageQueue) Stats() UsageQueueStats {
	if q == nil {
		return UsageQueueStats{}
	}
	return UsageQueueStats{
		Capacity: cap(q.events),
		Pending:  len(q.events),
		Recorded: q.recorded.Load(),
		Failed:   q.failed.Load(),
		Dropped:  q.dropped.Load(),
	}
}

func (q *UsageQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.events)
}

func normalizeUsageEvent(event UsageEvent, now int64) (UsageEvent, error) {
	if now <= 0 {
		return UsageEvent{}, ErrInvalidUsageEvent
	}
	method := strings.TrimSpace(event.Method)
	if method == "" || len(method) > MaxUsageMethodLength || !printable(method) {
		return UsageEvent{}, ErrInvalidUsageEvent
	}
	route := strings.TrimSpace(event.Route)
	if route == "" || len(route) > MaxUsageRouteLength || strings.ContainsAny(route, "?\r\n") || !printable(route) {
		return UsageEvent{}, ErrInvalidUsageEvent
	}
	if event.Status < 100 || event.Status > 599 {
		return UsageEvent{}, ErrInvalidUsageEvent
	}
	normalized := UsageEvent{
		CreatedAt: now,
		Method:    method,
		Route:     route,
		Status:    event.Status,
	}
	requestID := strings.TrimSpace(event.RequestID)
	if len(requestID) > MaxUsageRequestIDLength || (requestID != "" && !printable(requestID)) {
		return UsageEvent{}, ErrInvalidUsageEvent
	}
	normalized.RequestID = requestID
	traceID := strings.TrimSpace(event.TraceID)
	if len(traceID) > MaxUsageRequestIDLength || (traceID != "" && !printable(traceID)) {
		return UsageEvent{}, ErrInvalidUsageEvent
	}
	normalized.TraceID = traceID
	if event.UserID != nil && *event.UserID > 0 {
		userID := *event.UserID
		normalized.UserID = &userID
	}
	if event.IP != nil {
		origin := strings.TrimSpace(*event.IP)
		if origin != "" && len(origin) <= MaxUsageOriginLength && printable(origin) {
			normalized.IP = &origin
		}
	}
	return normalized, nil
}
