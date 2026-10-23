package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type groupJoinServiceStub struct {
	membership GroupMembership
	err        error
	calls      int
}

func (s *groupJoinServiceStub) Join(context.Context, int64, string) (GroupMembership, error) {
	s.calls++
	return s.membership, s.err
}

func TestRateLimitedGroupJoinerAllowsThenRejects(t *testing.T) {
	limiter, err := NewRateLimiter(RateLimitConfig{Limit: 1, Window: time.Minute, MaxKeys: 10})
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}
	inner := &groupJoinServiceStub{membership: GroupMembership{GroupID: 4, UserID: 7, Role: GroupRoleMember}}
	service, err := NewRateLimitedGroupJoiner(inner, limiter)
	if err != nil {
		t.Fatalf("NewRateLimitedGroupJoiner() error = %v", err)
	}

	first, err := service.Join(context.Background(), 7, "ABCDEFGHJKLM")
	if err != nil {
		t.Fatalf("Join() first error = %v", err)
	}
	if first.GroupID != 4 {
		t.Fatalf("Join() = %+v, want delegated membership", first)
	}
	if _, err := service.Join(context.Background(), 7, "ABCDEFGHJKLM"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Join() second error = %v, want ErrRateLimited", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner join calls = %d, want 1", inner.calls)
	}
}

func TestRateLimitedGroupJoinerRejectsNilDependencies(t *testing.T) {
	if _, err := NewRateLimitedGroupJoiner(nil, &RateLimiter{}); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("NewRateLimitedGroupJoiner(nil joiner) error = %v, want ErrInvalidGroup", err)
	}
}
