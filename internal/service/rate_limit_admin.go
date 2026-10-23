package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// ErrNilRateLimitAdmin identifies missing rate-limit inspection wiring.
var ErrNilRateLimitAdmin = errors.New("service: nil rate limit admin")

// RateLimitInspector exposes safe live rate-limit state for administrators.
type RateLimitInspector interface {
	Actions() []RateLimitActionInfo
	Buckets(string) ([]RateLimitBucketInfo, error)
	ClearAction(string) (int, error)
	ClearAll() (int, error)
}

// RateLimitAdminService lists and clears live rate-limit budgets. Every clear
// is a separate, audited administrator action.
type RateLimitAdminService struct {
	limiters []RateLimitInspector
	auditor  AuditRecorder
}

// NewRateLimitAdminService constructs rate-limit administration over every
// registered limiter.
func NewRateLimitAdminService(auditor AuditRecorder, limiters ...RateLimitInspector) (*RateLimitAdminService, error) {
	if len(limiters) == 0 {
		return nil, ErrNilRateLimitAdmin
	}
	for _, limiter := range limiters {
		if limiter == nil {
			return nil, ErrNilRateLimitAdmin
		}
	}
	return &RateLimitAdminService{limiters: limiters, auditor: auditor}, nil
}

// Overview merges live action summaries across every limiter.
func (s *RateLimitAdminService) Overview() []RateLimitActionInfo {
	if s == nil {
		return nil
	}
	seen := make(map[string]struct{})
	overview := make([]RateLimitActionInfo, 0)
	for _, limiter := range s.limiters {
		for _, action := range limiter.Actions() {
			if _, exists := seen[action.Action]; exists {
				continue
			}
			seen[action.Action] = struct{}{}
			overview = append(overview, action)
		}
	}
	return overview
}

// Buckets returns redacted budgets for one action across every limiter.
func (s *RateLimitAdminService) Buckets(action string) ([]RateLimitBucketInfo, error) {
	if s == nil {
		return nil, ErrNilRateLimitAdmin
	}
	for _, limiter := range s.limiters {
		for _, candidate := range limiter.Actions() {
			if candidate.Action == action {
				return limiter.Buckets(action)
			}
		}
	}
	return nil, ErrUnknownRateLimitAction
}

// ClearAction removes every budget for one action and records the action.
// Clearing an unknown action is refused.
func (s *RateLimitAdminService) ClearAction(ctx context.Context, actor AdminActionContext, action string) (int, error) {
	if s == nil {
		return 0, ErrNilRateLimitAdmin
	}
	if ctx == nil {
		return 0, errors.New("service: nil rate limit context")
	}
	if actor.ActorID <= 0 || action == "" {
		return 0, ErrInvalidAdminQuery
	}
	removed := 0
	matched := false
	for _, limiter := range s.limiters {
		for _, candidate := range limiter.Actions() {
			if candidate.Action != action {
				continue
			}
			matched = true
			count, err := limiter.ClearAction(action)
			if err != nil {
				return removed, err
			}
			removed += count
		}
	}
	if !matched {
		return 0, ErrUnknownRateLimitAction
	}
	s.record(ctx, actor, action, removed)
	return removed, nil
}

// ClearAll removes every live budget and records the action.
func (s *RateLimitAdminService) ClearAll(ctx context.Context, actor AdminActionContext) (int, error) {
	if s == nil {
		return 0, ErrNilRateLimitAdmin
	}
	if ctx == nil {
		return 0, errors.New("service: nil rate limit context")
	}
	if actor.ActorID <= 0 {
		return 0, ErrInvalidAdminQuery
	}
	removed := 0
	for _, limiter := range s.limiters {
		count, err := limiter.ClearAll()
		removed += count
		if err != nil {
			s.record(ctx, actor, "all", removed)
			return removed, fmt.Errorf("clear rate limits: %w", err)
		}
	}
	s.record(ctx, actor, "all", removed)
	return removed, nil
}

func (s *RateLimitAdminService) record(ctx context.Context, actor AdminActionContext, target string, removed int) {
	if s.auditor == nil {
		return
	}
	_ = s.auditor.Record(ctx, AuditRecord{
		ActorUserID: actor.ActorID,
		Action:      AuditActionRateLimitCleared,
		TargetType:  "ratelimit",
		TargetID:    target,
		Detail:      "removed=" + strconv.Itoa(removed),
		IP:          actor.Origin,
	})
}
