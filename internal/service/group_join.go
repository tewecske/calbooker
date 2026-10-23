package service

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const groupInviteJoinAction = "group.invite.join"

// GroupJoiner is the join operation consumed by group rate limiting.
type GroupJoiner interface {
	Join(context.Context, int64, string) (GroupMembership, error)
}

// RateLimitedGroupJoiner applies an invitation-redemption budget without
// including the bearer invite code in a limiter key.
type RateLimitedGroupJoiner struct {
	joiner  GroupJoiner
	limiter *RateLimiter
}

// NewRateLimitedGroupJoiner constructs invite redemption with its own budget.
func NewRateLimitedGroupJoiner(joiner GroupJoiner, limiter *RateLimiter) (*RateLimitedGroupJoiner, error) {
	if joiner == nil {
		return nil, ErrInvalidGroup
	}
	if limiter == nil {
		return nil, ErrInvalidRateLimitConfig
	}
	return &RateLimitedGroupJoiner{joiner: joiner, limiter: limiter}, nil
}

// Join applies the redemption budget for the account before attempting to
// redeem an invite code.
func (s *RateLimitedGroupJoiner) Join(ctx context.Context, userID int64, code string) (GroupMembership, error) {
	if s == nil || s.joiner == nil || s.limiter == nil {
		return GroupMembership{}, ErrInvalidRateLimitConfig
	}
	decision, err := s.limiter.Allow(groupInviteJoinAction, strconv.FormatInt(userID, 10))
	if err != nil {
		return GroupMembership{}, fmt.Errorf("allow group join: %w", err)
	}
	if !decision.Allowed {
		return GroupMembership{}, RateLimitError{RetryAfter: decision.RetryAfter}
	}
	return s.joiner.Join(ctx, userID, code)
}

// groupJoinDuration keeps the join budget window explicit for wiring.
const groupJoinDuration = time.Minute
