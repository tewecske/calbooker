package service

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const maxRateLimitKeyLength = 255

var (
	// ErrInvalidRateLimitConfig identifies unusable limiter settings.
	ErrInvalidRateLimitConfig = errors.New("service: invalid rate limit config")
	// ErrInvalidRateLimitKey identifies an empty or oversized limiter dimension.
	ErrInvalidRateLimitKey = errors.New("service: invalid rate limit key")
	// ErrRateLimitCapacity identifies a limiter at its configured key bound.
	ErrRateLimitCapacity = errors.New("service: rate limit capacity reached")
	// ErrRateLimited identifies a caller over an authentication budget.
	ErrRateLimited = errors.New("service: rate limited")
	// ErrUnknownRateLimitAction identifies an inspect request for an action
	// with no live budgets.
	ErrUnknownRateLimitAction = errors.New("service: unknown rate limit action")
)

// RateLimitConfig controls one fixed-window budget shared by independent keys.
type RateLimitConfig struct {
	Limit   int
	Window  time.Duration
	MaxKeys int
}

// DefaultAuthenticationRateLimitConfig is a bounded baseline for auth actions.
var DefaultAuthenticationRateLimitConfig = RateLimitConfig{
	Limit:   10,
	Window:  time.Minute,
	MaxKeys: 10_000,
}

// RateLimitDecision reports the result and remaining budget for one key.
type RateLimitDecision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

// RateLimitError carries retry timing without exposing the limited key.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e RateLimitError) Error() string {
	return ErrRateLimited.Error()
}

func (e RateLimitError) Unwrap() error {
	return ErrRateLimited
}

// RateLimitStore keeps fixed-window attempt counters. The in-memory store
// serves one process; a database store is shared by every instance of an app,
// so a restart or a second instance does not reset or multiply the budgets.
// Every method takes the time so tests control the clock.
type RateLimitStore interface {
	// Hit counts one attempt in the action/key window, opening a window of the
	// given length when none is live. The count includes this attempt and can
	// exceed the limit; the limiter compares it. hint is the redacted key.
	Hit(ctx context.Context, action, key, hint string, window time.Duration, now time.Time) (RateLimitHit, error)
	// Get returns the live window for action/key, or the zero value.
	Get(ctx context.Context, action, key string, now time.Time) (RateLimitHit, error)
	// Clear removes one action/key window.
	Clear(ctx context.Context, action, key string) error
	// List returns every live window.
	List(ctx context.Context, now time.Time) ([]RateLimitStoredBucket, error)
	// ClearAction removes every window of one action and returns the count.
	ClearAction(ctx context.Context, action string) (int, error)
	// ClearAll removes every window and returns the count.
	ClearAll(ctx context.Context) (int, error)
}

// RateLimitHit is the live window of one action/key. The zero value means no
// live window.
type RateLimitHit struct {
	Count   int
	Started time.Time
}

// RateLimitStoredBucket is one live window with its redacted key hint.
type RateLimitStoredBucket struct {
	Action  string
	KeyHint string
	Count   int
	Started time.Time
}

// rateLimitStoreTimeout bounds one store call, because limiter methods carry
// no context of their own.
const rateLimitStoreTimeout = 5 * time.Second

// RateLimiter implements bounded, concurrent fixed-window budgets over a
// RateLimitStore. Action is part of every key, so sign-in and other
// authentication actions stay separate.
type RateLimiter struct {
	config RateLimitConfig
	now    func() time.Time
	store  RateLimitStore
}

// NewRateLimiter constructs a bounded in-memory limiter.
func NewRateLimiter(config RateLimitConfig) (*RateLimiter, error) {
	if config.Limit <= 0 || config.Window <= 0 || config.MaxKeys <= 0 {
		return nil, ErrInvalidRateLimitConfig
	}
	return &RateLimiter{config: config, now: time.Now, store: newMemoryRateLimitStore(config)}, nil
}

// NewRateLimiterWithStore constructs a limiter over a shared store. MaxKeys
// bounds only the in-memory store, so it is not required here.
func NewRateLimiterWithStore(config RateLimitConfig, store RateLimitStore) (*RateLimiter, error) {
	if config.Limit <= 0 || config.Window <= 0 || store == nil {
		return nil, ErrInvalidRateLimitConfig
	}
	return &RateLimiter{config: config, now: time.Now, store: store}, nil
}

func (l *RateLimiter) usable() bool {
	return l != nil && l.now != nil && l.store != nil
}

func storeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), rateLimitStoreTimeout)
}

// retryAfter is the time left in a window that started at started.
func (l *RateLimiter) retryAfter(started, now time.Time) time.Duration {
	return l.config.Window - now.Sub(started)
}

// Allow consumes one attempt from action/key budget.
func (l *RateLimiter) Allow(action, key string) (RateLimitDecision, error) {
	if !l.usable() {
		return RateLimitDecision{}, ErrInvalidRateLimitConfig
	}
	if err := validateRateLimitKey(action); err != nil {
		return RateLimitDecision{}, err
	}
	if err := validateRateLimitKey(key); err != nil {
		return RateLimitDecision{}, err
	}
	now := l.now()
	ctx, cancel := storeContext()
	defer cancel()
	hit, err := l.store.Hit(ctx, action, key, redactRateLimitKey(key), l.config.Window, now)
	if err != nil {
		return RateLimitDecision{}, err
	}
	if hit.Count > l.config.Limit {
		return RateLimitDecision{Allowed: false, Remaining: 0, RetryAfter: l.retryAfter(hit.Started, now)}, nil
	}
	return RateLimitDecision{Allowed: true, Remaining: l.config.Limit - hit.Count}, nil
}

// Clear removes one action/key budget, allowing successful authentication to
// reset relevant failed-attempt state.
func (l *RateLimiter) Clear(action, key string) error {
	if !l.usable() {
		return ErrInvalidRateLimitConfig
	}
	if err := validateRateLimitKey(action); err != nil {
		return err
	}
	if err := validateRateLimitKey(key); err != nil {
		return err
	}
	ctx, cancel := storeContext()
	defer cancel()
	return l.store.Clear(ctx, action, key)
}

// live lists the live windows for the read-only views. A store failure yields
// no windows rather than a guess.
func (l *RateLimiter) live() []RateLimitStoredBucket {
	if !l.usable() {
		return nil
	}
	ctx, cancel := storeContext()
	defer cancel()
	buckets, err := l.store.List(ctx, l.now())
	if err != nil {
		return nil
	}
	return buckets
}

// LockedCount reports how many keys are currently at their fixed-window
// limit. It never exposes the limited keys themselves.
func (l *RateLimiter) LockedCount() int {
	locked := 0
	for _, bucket := range l.live() {
		if bucket.Count >= l.config.Limit {
			locked++
		}
	}
	return locked
}

// RateLimitActionInfo is one action's live budget summary. It never carries
// limiter keys.
type RateLimitActionInfo struct {
	Action  string
	Limit   int
	Window  time.Duration
	Buckets int
	Locked  int
}

// RateLimitBucketInfo is one live budget with a redacted key hint.
type RateLimitBucketInfo struct {
	Action     string
	KeyHint    string
	Count      int
	Limit      int
	RetryAfter time.Duration
}

// Actions returns one summary per action currently held by the limiter.
func (l *RateLimiter) Actions() []RateLimitActionInfo {
	index := make(map[string]int)
	actions := make([]RateLimitActionInfo, 0)
	for _, bucket := range l.live() {
		position, exists := index[bucket.Action]
		if !exists {
			position = len(actions)
			index[bucket.Action] = position
			actions = append(actions, RateLimitActionInfo{
				Action: bucket.Action,
				Limit:  l.config.Limit,
				Window: l.config.Window,
			})
		}
		actions[position].Buckets++
		if bucket.Count >= l.config.Limit {
			actions[position].Locked++
		}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Action < actions[j].Action })
	return actions
}

// Buckets returns redacted live budgets for one action.
func (l *RateLimiter) Buckets(action string) ([]RateLimitBucketInfo, error) {
	if !l.usable() {
		return nil, ErrInvalidRateLimitConfig
	}
	if err := validateRateLimitKey(action); err != nil {
		return nil, err
	}
	now := l.now()
	ctx, cancel := storeContext()
	defer cancel()
	stored, err := l.store.List(ctx, now)
	if err != nil {
		return nil, err
	}
	buckets := make([]RateLimitBucketInfo, 0)
	for _, bucket := range stored {
		if bucket.Action != action {
			continue
		}
		buckets = append(buckets, RateLimitBucketInfo{
			Action:     bucket.Action,
			KeyHint:    bucket.KeyHint,
			Count:      min(bucket.Count, l.config.Limit),
			Limit:      l.config.Limit,
			RetryAfter: l.retryAfter(bucket.Started, now),
		})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Count > buckets[j].Count })
	return buckets, nil
}

// ClearAction removes every budget for one action and returns the removed
// count. Clearing an unknown action is a successful no-op.
func (l *RateLimiter) ClearAction(action string) (int, error) {
	if !l.usable() {
		return 0, ErrInvalidRateLimitConfig
	}
	if err := validateRateLimitKey(action); err != nil {
		return 0, err
	}
	ctx, cancel := storeContext()
	defer cancel()
	return l.store.ClearAction(ctx, action)
}

// ClearAll removes every live budget and returns the removed count.
func (l *RateLimiter) ClearAll() (int, error) {
	if !l.usable() {
		return 0, ErrInvalidRateLimitConfig
	}
	ctx, cancel := storeContext()
	defer cancel()
	return l.store.ClearAll(ctx)
}

// redactRateLimitKey keeps a short, non-reversible hint so administrators can
// distinguish budgets without exposing an email address or request origin.
func redactRateLimitKey(key string) string {
	runes := []rune(key)
	if len(runes) <= 2 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[0]) + strings.Repeat("*", len(runes)-2) + string(runes[len(runes)-1])
}

// RateLimitSnapshot is the read-only state of one action/key budget used for
// administrator lockout diagnostics.
type RateLimitSnapshot struct {
	Count      int
	Limit      int
	RetryAfter time.Duration
}

// Snapshot returns the current budget for action/key without consuming it. It
// is read-only so administrators can diagnose live lockout state.
func (l *RateLimiter) Snapshot(action, key string) (RateLimitSnapshot, error) {
	if !l.usable() {
		return RateLimitSnapshot{}, ErrInvalidRateLimitConfig
	}
	if err := validateRateLimitKey(action); err != nil {
		return RateLimitSnapshot{}, err
	}
	if err := validateRateLimitKey(key); err != nil {
		return RateLimitSnapshot{}, err
	}
	now := l.now()
	ctx, cancel := storeContext()
	defer cancel()
	hit, err := l.store.Get(ctx, action, key, now)
	if err != nil {
		return RateLimitSnapshot{}, err
	}
	snapshot := RateLimitSnapshot{Limit: l.config.Limit}
	if hit.Count > 0 {
		snapshot.Count = min(hit.Count, l.config.Limit)
		snapshot.RetryAfter = l.retryAfter(hit.Started, now)
	}
	return snapshot, nil
}

// originRateLimitKey groups IPv6 clients by /64 prefix, which one subscriber
// usually controls in full, so rotating addresses inside it does not yield
// fresh budgets. IPv4 and unparseable origins are used unchanged.
func originRateLimitKey(origin string) string {
	addr, err := netip.ParseAddr(origin)
	if err != nil || addr.Zone() != "" {
		return origin
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return origin
	}
	return prefix.String()
}

func validateRateLimitKey(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > maxRateLimitKeyLength || strings.ContainsAny(value, "\x00\r\n") {
		return ErrInvalidRateLimitKey
	}
	return nil
}
