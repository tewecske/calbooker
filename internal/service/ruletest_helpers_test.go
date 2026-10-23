package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ruleClock is a deterministic, controllable time source shared by rule tests.
// Services expose an injectable func() time.Time so tests can freeze, set, and
// advance time without sleeping.
type ruleClock struct {
	mu  sync.Mutex
	now time.Time
}

// newRuleClock returns a clock frozen at at.
func newRuleClock(at time.Time) *ruleClock {
	return &ruleClock{now: at}
}

// Now reports the frozen current time.
func (c *ruleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Func returns a time source suitable for a service clock field.
func (c *ruleClock) Func() func() time.Time {
	return c.Now
}

// Set moves the clock to an absolute time.
func (c *ruleClock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

// Advance moves the clock forward by d. Negative durations are ignored so a
// test cannot accidentally time-travel into the past.
func (c *ruleClock) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ruleMailSender is an isolated mail transport for rule tests. It captures
// messages in memory only and never forwards them, so tests cannot leak
// credentials through a real transport.
type ruleMailSender struct {
	mu       sync.Mutex
	messages []Mail
	err      error
}

// Send captures a message unless a failure has been configured.
func (s *ruleMailSender) Send(_ context.Context, message Mail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.messages = append(s.messages, message)
	return nil
}

// SetError configures a delivery failure for all subsequent sends.
func (s *ruleMailSender) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Messages returns a copy of the captured messages.
func (s *ruleMailSender) Messages() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.messages...)
}

// Count reports how many messages were captured.
func (s *ruleMailSender) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// Last returns the most recent captured message.
func (s *ruleMailSender) Last() (Mail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return Mail{}, false
	}
	return s.messages[len(s.messages)-1], true
}

// ruleSessionRepository is an in-memory SessionRepository for rule tests.
// Missing sessions yield the zero value so SessionService reports not-found.
type ruleSessionRepository struct {
	mu        sync.Mutex
	sessions  map[string]Session
	createErr error
	findErr   error
	revokeErr error
}

// newRuleSessionRepository returns an empty in-memory session store.
func newRuleSessionRepository() *ruleSessionRepository {
	return &ruleSessionRepository{sessions: make(map[string]Session)}
}

// Seed inserts an existing session without going through CreateSession.
func (r *ruleSessionRepository) Seed(session Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = make(map[string]Session)
	}
	r.sessions[session.ID] = session
}

// Session returns the stored session for id.
func (r *ruleSessionRepository) Session(id string) (Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id]
	return session, ok
}

// CreateSession stores a session keyed by its opaque identifier.
func (r *ruleSessionRepository) CreateSession(_ context.Context, session Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	if r.sessions == nil {
		r.sessions = make(map[string]Session)
	}
	r.sessions[session.ID] = session
	return nil
}

// FindSession returns the stored session or the zero value when it is absent.
func (r *ruleSessionRepository) FindSession(_ context.Context, id string) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findErr != nil {
		return Session{}, r.findErr
	}
	return r.sessions[id], nil
}

// RevokeSession records a revocation timestamp on a stored session.
func (r *ruleSessionRepository) RevokeSession(_ context.Context, id string, revokedAt int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revokeErr != nil {
		return r.revokeErr
	}
	session, ok := r.sessions[id]
	if !ok {
		return nil
	}
	session.RevokedAt = &revokedAt
	r.sessions[id] = session
	return nil
}

// RevokeUserSessions revokes every stored session owned by userID.
func (r *ruleSessionRepository) RevokeUserSessions(_ context.Context, userID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revokeErr != nil {
		return r.revokeErr
	}
	revokedAt := time.Now().Unix()
	for id, session := range r.sessions {
		if session.UserID != userID || session.RevokedAt != nil {
			continue
		}
		session.RevokedAt = &revokedAt
		r.sessions[id] = session
	}
	return nil
}

// newRuleRateLimiter builds a real limiter driven by a deterministic clock so
// fixed-window behavior can be proven without sleeping.
func newRuleRateLimiter(t *testing.T, clock *ruleClock, config RateLimitConfig) *RateLimiter {
	t.Helper()
	if clock == nil {
		t.Fatal("newRuleRateLimiter: nil clock")
	}
	limiter, err := NewRateLimiter(config)
	if err != nil {
		t.Fatalf("NewRateLimiter(%+v) error = %v, want nil", config, err)
	}
	limiter.now = clock.Func()
	return limiter
}

// ruleRandomReader produces a deterministic, monotonically increasing byte
// stream. It is safe for concurrent reads and never blocks.
type ruleRandomReader struct {
	mu   sync.Mutex
	next byte
}

// Read fills p with sequential bytes starting from the reader's cursor.
func (r *ruleRandomReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		p[i] = r.next
		r.next++
	}
	return len(p), nil
}

// useRuleRandomness replaces crypto/rand.Reader with a deterministic reader for
// the duration of the test and restores the previous reader on cleanup. It must
// only be used by tests that do not call t.Parallel.
func useRuleRandomness(t *testing.T, start byte) *ruleRandomReader {
	t.Helper()
	previous := rand.Reader
	reader := &ruleRandomReader{next: start}
	rand.Reader = reader
	t.Cleanup(func() {
		rand.Reader = previous
	})
	return reader
}

// ruleSessionToken returns the deterministic session credential a fresh
// useRuleRandomness test would produce, so tests can assert exact values.
func ruleSessionToken(start byte) string {
	value := make([]byte, sessionIDBytes)
	for i := range value {
		value[i] = start + byte(i)
	}
	return hex.EncodeToString(value)
}

func TestRuleClockFreezesAndAdvances(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	clock := newRuleClock(base)

	if got := clock.Now(); !got.Equal(base) {
		t.Fatalf("Now() = %v, want %v", got, base)
	}
	clock.Advance(0)
	if got := clock.Now(); !got.Equal(base) {
		t.Fatalf("Advance(0) moved clock to %v, want %v", got, base)
	}
	clock.Advance(-time.Hour)
	if got := clock.Now(); !got.Equal(base) {
		t.Fatalf("Advance(negative) moved clock to %v, want %v", got, base)
	}
	clock.Advance(90 * time.Second)
	if got, want := clock.Now(), base.Add(90*time.Second); !got.Equal(want) {
		t.Fatalf("Advance(90s) = %v, want %v", got, want)
	}

	next := base.Add(time.Hour)
	clock.Set(next)
	if got := clock.Func()(); !got.Equal(next) {
		t.Fatalf("Func()() = %v, want %v", got, next)
	}
}

func TestRuleClockControlsRateLimitWindow(t *testing.T) {
	clock := newRuleClock(time.Unix(1_000, 0))
	limiter := newRuleRateLimiter(t, clock, RateLimitConfig{Limit: 2, Window: time.Minute, MaxKeys: 10})

	for attempt := 0; attempt < 2; attempt++ {
		decision, err := limiter.Allow("signin.identifier", "user@example.test")
		if err != nil {
			t.Fatalf("Allow() attempt %d error = %v, want nil", attempt, err)
		}
		if !decision.Allowed {
			t.Fatalf("Allow() attempt %d = denied, want allowed", attempt)
		}
	}

	decision, err := limiter.Allow("signin.identifier", "user@example.test")
	if err != nil {
		t.Fatalf("Allow() over budget error = %v, want nil", err)
	}
	if decision.Allowed {
		t.Fatal("Allow() over budget = allowed, want denied")
	}
	if decision.RetryAfter <= 0 || decision.RetryAfter > time.Minute {
		t.Fatalf("RetryAfter = %v, want within (0, 1m]", decision.RetryAfter)
	}

	clock.Advance(30 * time.Second)
	if decision, err = limiter.Allow("signin.identifier", "user@example.test"); err != nil {
		t.Fatalf("Allow() mid-window error = %v, want nil", err)
	} else if decision.Allowed {
		t.Fatal("Allow() mid-window = allowed, want still denied")
	}

	clock.Advance(30 * time.Second)
	if decision, err = limiter.Allow("signin.identifier", "user@example.test"); err != nil {
		t.Fatalf("Allow() new window error = %v, want nil", err)
	} else if !decision.Allowed {
		t.Fatal("Allow() new window = denied, want allowed")
	}

	if locked := limiter.LockedCount(); locked != 0 {
		t.Fatalf("LockedCount() = %d, want 0 after window rollover", locked)
	}
}

func TestRuleMailSenderCapturesAndFails(t *testing.T) {
	sender := &ruleMailSender{}
	message := Mail{To: "user@example.test", Subject: "Confirm", TextBody: "link"}

	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}
	if sender.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", sender.Count())
	}
	got, ok := sender.Last()
	if !ok || got != message {
		t.Fatalf("Last() = %+v/%t, want %+v/true", got, ok, message)
	}
	messages := sender.Messages()
	messages[0].Subject = "mutated"
	if again, _ := sender.Last(); again.Subject != message.Subject {
		t.Fatal("Messages() exposed internal storage")
	}

	deliveryErr := errors.New("mail delivery failed")
	sender.SetError(deliveryErr)
	if err := sender.Send(context.Background(), message); !errors.Is(err, deliveryErr) {
		t.Fatalf("Send() error = %v, want %v", err, deliveryErr)
	}
	if sender.Count() != 1 {
		t.Fatalf("Count() after failure = %d, want 1", sender.Count())
	}

	empty := &ruleMailSender{}
	if _, ok := empty.Last(); ok {
		t.Fatal("Last() on empty sender = ok, want false")
	}
}

func TestRuleSessionRepositoryLifecycle(t *testing.T) {
	repository := newRuleSessionRepository()
	service, err := NewSessionService(repository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}

	session, err := service.Create(context.Background(), 42)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	found, err := service.Lookup(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("Lookup() error = %v, want nil", err)
	}
	if found.UserID != 42 {
		t.Fatalf("Lookup() user = %d, want 42", found.UserID)
	}

	if err := service.Revoke(context.Background(), session.ID); err != nil {
		t.Fatalf("Revoke() error = %v, want nil", err)
	}
	if _, err := service.Lookup(context.Background(), session.ID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("Lookup() after revoke error = %v, want %v", err, ErrSessionRevoked)
	}

	if _, err := service.Lookup(context.Background(), strings.Repeat("a", maxSessionID)); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Lookup() missing error = %v, want %v", err, ErrSessionNotFound)
	}
}

func TestRuleSessionRepositoryExpiry(t *testing.T) {
	repository := newRuleSessionRepository()
	service, err := NewSessionService(repository, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	now := time.Now().Unix()
	repository.Seed(Session{ID: "expiring", UserID: 7, CreatedAt: now - 10, ExpiresAt: now + 10})
	repository.Seed(Session{ID: "expired", UserID: 7, CreatedAt: now - 20, ExpiresAt: now})

	if _, err := service.Lookup(context.Background(), "expiring"); err != nil {
		t.Fatalf("Lookup(active) error = %v, want nil", err)
	}
	if _, err := service.Lookup(context.Background(), "expired"); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Lookup(expired) error = %v, want %v", err, ErrSessionExpired)
	}
}

func TestRuleRandomnessIsDeterministic(t *testing.T) {
	const start = 0x40
	useRuleRandomness(t, start)

	service, err := NewSessionService(&sessionRepositoryStub{}, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionService() error = %v, want nil", err)
	}
	session, err := service.Create(context.Background(), 1)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if want := ruleSessionToken(start); session.ID != want {
		t.Fatalf("session ID = %q, want deterministic %q", session.ID, want)
	}

	next := new(ruleRandomReader)
	next.next = start
	buffer := make([]byte, 4)
	if _, err := next.Read(buffer); err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if buffer[0] != start || buffer[1] != start+1 || buffer[3] != start+3 {
		t.Fatalf("deterministic bytes = %v, want sequential from %#x", buffer, start)
	}
}

func TestRuleRandomnessRestoresGlobalReader(t *testing.T) {
	original := rand.Reader
	t.Run("installs deterministic reader", func(t *testing.T) {
		useRuleRandomness(t, 0)
		if rand.Reader == original {
			t.Fatal("useRuleRandomness did not install a deterministic reader")
		}
	})
	if rand.Reader != original {
		t.Fatal("useRuleRandomness did not restore crypto/rand.Reader")
	}
}
