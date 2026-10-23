package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRateLimiterActionsAndRedactedBuckets(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 5, Window: time.Minute, MaxKeys: 100})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := limiter.Allow("signin.identifier", "sensitive@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := limiter.Allow("signin.origin", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}

	actions := limiter.Actions()
	if len(actions) != 2 {
		t.Fatalf("Actions() = %+v, want two actions", actions)
	}
	buckets, err := limiter.Buckets("signin.identifier")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Count != 2 || buckets[0].Limit != 5 {
		t.Fatalf("buckets = %+v, want one bucket with count 2", buckets)
	}
	if buckets[0].KeyHint == "sensitive@example.test" || strings.Contains(buckets[0].KeyHint, "sensitive") {
		t.Fatalf("key hint leaks the key: %q", buckets[0].KeyHint)
	}
	if !strings.HasPrefix(buckets[0].KeyHint, "s") || !strings.HasSuffix(buckets[0].KeyHint, "t") {
		t.Fatalf("key hint = %q, want redacted first and last rune", buckets[0].KeyHint)
	}
}

func TestRateLimitAdminServiceClearsAndAudits(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 5, Window: time.Minute, MaxKeys: 100})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := limiter.Allow("signin.identifier", "a@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := limiter.Allow("guest.create", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	auditor := &auditRecorderStub{}
	service, err := NewRateLimitAdminService(auditor, limiter)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := service.ClearAction(context.Background(), AdminActionContext{ActorID: 1, Origin: "10.0.0.1"}, "signin.identifier")
	if err != nil {
		t.Fatalf("ClearAction() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("ClearAction() removed = %d, want 1", removed)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionRateLimitCleared {
		t.Fatalf("audit records = %+v, want rate-limit cleared", auditor.records)
	}
	if auditor.records[0].TargetID != "signin.identifier" {
		t.Fatalf("audit target = %q, want action name", auditor.records[0].TargetID)
	}

	removed, err = service.ClearAll(context.Background(), AdminActionContext{ActorID: 1})
	if err != nil {
		t.Fatalf("ClearAll() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("ClearAll() removed = %d, want 1 remaining", removed)
	}
	if len(limiter.Actions()) != 0 {
		t.Fatalf("actions after clear = %+v, want none", limiter.Actions())
	}
	if len(auditor.records) != 2 {
		t.Fatalf("audit records = %d, want 2", len(auditor.records))
	}
}

func TestRateLimitAdminServiceRejectsUnknownAction(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 5, Window: time.Minute, MaxKeys: 100})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRateLimitAdminService(&auditRecorderStub{}, limiter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClearAction(context.Background(), AdminActionContext{ActorID: 1}, "missing.action"); !errors.Is(err, ErrUnknownRateLimitAction) {
		t.Fatalf("ClearAction(unknown) error = %v, want %v", err, ErrUnknownRateLimitAction)
	}
	if _, err := service.Buckets("missing.action"); !errors.Is(err, ErrUnknownRateLimitAction) {
		t.Fatalf("Buckets(unknown) error = %v, want %v", err, ErrUnknownRateLimitAction)
	}
}

func TestNewRateLimitAdminServiceValidatesInputs(t *testing.T) {
	if _, err := NewRateLimitAdminService(nil); !errors.Is(err, ErrNilRateLimitAdmin) {
		t.Fatalf("NewRateLimitAdminService() error = %v, want %v", err, ErrNilRateLimitAdmin)
	}
	if _, err := NewRateLimitAdminService(nil, nil); !errors.Is(err, ErrNilRateLimitAdmin) {
		t.Fatalf("NewRateLimitAdminService(nil limiter) error = %v, want %v", err, ErrNilRateLimitAdmin)
	}
}

func TestRateLimitAdminServiceOverviewDeduplicates(t *testing.T) {
	first, _ := NewRateLimiter(RateLimitConfig{Limit: 5, Window: time.Minute, MaxKeys: 100})
	second, _ := NewRateLimiter(RateLimitConfig{Limit: 7, Window: time.Hour, MaxKeys: 100})
	for range 1 {
		_, _ = first.Allow("shared.action", "one")
		_, _ = second.Allow("shared.action", "two")
	}
	service, err := NewRateLimitAdminService(nil, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if actions := service.Overview(); len(actions) != 1 || actions[0].Action != "shared.action" {
		t.Fatalf("Overview() = %+v, want one deduplicated action", actions)
	}
}
