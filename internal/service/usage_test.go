package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNewUsageQueueValidatesConfig(t *testing.T) {
	if _, err := NewUsageQueue(nil, 10); !errors.Is(err, ErrNilUsageRepository) {
		t.Fatalf("NewUsageQueue(nil repo) error = %v, want %v", err, ErrNilUsageRepository)
	}
	if _, err := NewUsageQueue(&usageRepositoryStub{}, 0); !errors.Is(err, ErrInvalidUsageConfig) {
		t.Fatalf("NewUsageQueue(zero capacity) error = %v, want %v", err, ErrInvalidUsageConfig)
	}
}

func TestUsageQueueRecordsEventsInOrder(t *testing.T) {
	repository := &usageRepositoryStub{}
	queue, err := NewUsageQueue(repository, 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- queue.Run(ctx) }()

	for index := 0; index < 3; index++ {
		if err := queue.EnqueueUsage(context.Background(), UsageEvent{Method: "GET", Route: "/{language}/", Status: 200}); err != nil {
			t.Fatalf("EnqueueUsage() error = %v", err)
		}
	}
	waitFor(t, func() bool { return repository.count() == 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stats := queue.Stats(); stats.Recorded != 3 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want recorded 3", stats)
	}
}

func TestUsageQueueAppliesBackpressure(t *testing.T) {
	repository := &usageRepositoryStub{}
	queue, err := NewUsageQueue(repository, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueUsage(context.Background(), UsageEvent{Method: "GET", Route: "/a", Status: 200}); err != nil {
		t.Fatalf("first EnqueueUsage() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = queue.EnqueueUsage(ctx, UsageEvent{Method: "GET", Route: "/b", Status: 200})
	if !errors.Is(err, ErrUsageQueueFull) {
		t.Fatalf("second EnqueueUsage() error = %v, want %v", err, ErrUsageQueueFull)
	}
	stats := queue.Stats()
	if stats.Dropped != 1 || stats.Pending != 1 {
		t.Fatalf("stats = %+v, want one dropped and one pending", stats)
	}
}

func TestUsageQueueSurvivesStorageFailure(t *testing.T) {
	repository := &usageRepositoryStub{}
	repository.err = errors.New("storage down")
	queue, err := NewUsageQueue(repository, 4)
	if err != nil {
		t.Fatal(err)
	}
	queue.backoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- queue.Run(ctx) }()
	if err := queue.EnqueueUsage(context.Background(), UsageEvent{Method: "GET", Route: "/a", Status: 200}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return queue.Stats().Failed == 1 })

	repository.setError(nil)
	if err := queue.EnqueueUsage(context.Background(), UsageEvent{Method: "POST", Route: "/b", Status: 201}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return queue.Stats().Recorded == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestUsageQueueNormalizesEvents(t *testing.T) {
	repository := &usageRepositoryStub{}
	queue, err := NewUsageQueue(repository, 4)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []UsageEvent{
		{Method: "", Route: "/a", Status: 200},
		{Method: "GET", Route: "/a?token=secret", Status: 200},
		{Method: "GET", Route: "/a", Status: 700},
	}
	for _, event := range invalid {
		if err := queue.EnqueueUsage(context.Background(), event); !errors.Is(err, ErrInvalidUsageEvent) {
			t.Fatalf("EnqueueUsage(%+v) error = %v, want %v", event, err, ErrInvalidUsageEvent)
		}
	}
}

func TestUsageQueueRejectsNilContext(t *testing.T) {
	queue, err := NewUsageQueue(&usageRepositoryStub{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueUsage(nil, UsageEvent{Method: "GET", Route: "/a", Status: 200}); err == nil {
		t.Fatal("EnqueueUsage(nil) error = nil, want failure")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

type usageRepositoryStub struct {
	mu                sync.Mutex
	events            []UsageEvent
	err               error
	remainingFailures int
	callCount         int
}

func (r *usageRepositoryStub) RecordUsageEvent(_ context.Context, event UsageEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callCount++
	if r.remainingFailures > 0 {
		r.remainingFailures--
		return errors.New("transient storage failure")
	}
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

func (r *usageRepositoryStub) attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callCount
}

func (r *usageRepositoryStub) last() UsageEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return UsageEvent{}
	}
	return r.events[len(r.events)-1]
}

func (r *usageRepositoryStub) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *usageRepositoryStub) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func TestUsageQueueRetriesTransientFailures(t *testing.T) {
	repository := &usageRepositoryStub{remainingFailures: 2}
	queue, err := NewUsageQueue(repository, 4)
	if err != nil {
		t.Fatal(err)
	}
	queue.backoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- queue.Run(ctx) }()
	if err := queue.EnqueueUsage(context.Background(), UsageEvent{Method: "GET", Route: "/a", Status: 200}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return queue.Stats().Recorded == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stats := queue.Stats(); stats.Failed != 0 || repository.attempts() != 3 {
		t.Fatalf("stats = %+v attempts = %d, want success after 3 attempts", stats, repository.attempts())
	}
}

func TestUsageQueuePreservesRequestLink(t *testing.T) {
	repository := &usageRepositoryStub{}
	queue, err := NewUsageQueue(repository, 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- queue.Run(ctx) }()
	if err := queue.EnqueueUsage(context.Background(), UsageEvent{Method: "GET", Route: "/a", Status: 200, RequestID: "request-123", TraceID: "trace-456"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return repository.count() == 1 })
	cancel()
	<-done
	stored := repository.last()
	if stored.RequestID != "request-123" || stored.TraceID != "trace-456" {
		t.Fatalf("stored link = %q/%q, want request-123/trace-456", stored.RequestID, stored.TraceID)
	}
}

func TestUsageQueueRejectsUnsafeRequestID(t *testing.T) {
	queue, err := NewUsageQueue(&usageRepositoryStub{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueUsage(context.Background(), UsageEvent{Method: "GET", Route: "/a", Status: 200, RequestID: "bad\nvalue"}); !errors.Is(err, ErrInvalidUsageEvent) {
		t.Fatalf("EnqueueUsage(unsafe request id) error = %v, want %v", err, ErrInvalidUsageEvent)
	}
}
