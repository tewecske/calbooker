package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxAuditActionLength = 64
	maxAuditTargetType   = 32
	maxAuditTargetID     = 64
	maxAuditDetailLength = 1000
	maxAuditActorEmail   = 255
	maxAuditOrigin       = 64
)

// Stable administrator action codes recorded in the audit trail.
const (
	AuditActionAccountUpdated   = "admin.account.updated"
	AuditActionAccountDeleted   = "admin.account.deleted"
	AuditActionSessionsRevoked  = "admin.sessions.revoked"
	AuditActionMaintenanceRun   = "admin.maintenance.run"
	AuditActionRateLimitCleared = "admin.ratelimit.cleared"
	// Privilege and credential changes get their own codes so they are not
	// hidden among ordinary account edits.
	AuditActionAdminGranted = "admin.account.admin_granted"
	AuditActionAdminRevoked = "admin.account.admin_revoked"
)

// Audit sink outcomes.
const (
	AuditOutcomeRecorded = "recorded"
	AuditOutcomeFailed   = "failed"
)

var (
	// ErrInvalidAuditEntry identifies unusable administrator action input.
	ErrInvalidAuditEntry = errors.New("service: invalid audit entry")
	// ErrNilAuditRepository identifies missing audit persistence.
	ErrNilAuditRepository = errors.New("service: nil audit repository")
)

// AuditEntry is one durable administrator action record. Nullable columns use
// pointers so a deleted actor or target leaves a historical snapshot intact.
type AuditEntry struct {
	ID          int64
	OccurredAt  int64
	ActorUserID *int64
	ActorEmail  *string
	Action      string
	TargetType  *string
	TargetID    *string
	Detail      *string
	IP          *string
}

// AuditRepository stores administrator action history. Rows are never
// cascade-deleted when an actor or target account is removed.
type AuditRepository interface {
	CreateAuditEntry(context.Context, AuditEntry) error
	ListAuditEntries(context.Context, AuditQuery) (AuditPage, error)
}

const (
	// DefaultAuditPageSize is used when a request omits a page size.
	DefaultAuditPageSize = 20
	// MaxAuditPageSize bounds every audit list request.
	MaxAuditPageSize = 100
)

// AuditQuery is the bounded, server-validated audit-list query.
type AuditQuery struct {
	Action string
	Actor  string
	Target string
	Page   int
	Size   int
}

// AuditPage is one page of audit entries plus its total count.
type AuditPage struct {
	Entries []AuditEntry
	Total   int
	Page    int
	Size    int
	Pages   int
}

// Normalize returns a copy with every field bounded to a supported value.
func (q AuditQuery) Normalize() AuditQuery {
	q.Action = normalizeAdminSearch(q.Action)
	q.Actor = normalizeAdminSearch(q.Actor)
	q.Target = normalizeAdminSearch(q.Target)
	q.Page = min(max(q.Page, 1), MaxAdminPage)
	if q.Size < 1 {
		q.Size = DefaultAuditPageSize
	}
	if q.Size > MaxAuditPageSize {
		q.Size = MaxAuditPageSize
	}
	return q
}

// Offset returns the zero-based row offset for the normalized page.
func (q AuditQuery) Offset() int {
	if q.Page < 1 {
		return 0
	}
	return (min(q.Page, MaxAdminPage) - 1) * q.Size
}

// AuditRecord is the safe, validated input for one administrator action. It
// must never carry passwords, session credentials, tokens, or provider secrets.
type AuditRecord struct {
	ActorUserID int64
	ActorEmail  string
	Action      string
	TargetType  string
	TargetID    string
	Detail      string
	IP          string
}

// AuditRecorder records administrator actions for a use case.
type AuditRecorder interface {
	Record(context.Context, AuditRecord) error
}

// AuditSink receives every administrator action so it can be emitted to the
// separately identifiable security log stream, with outcome AuditOutcomeFailed
// when the durable record could not be written. Sinks must never block or fail
// an already completed action.
type AuditSink interface {
	AuditRecorded(ctx context.Context, record AuditRecord, outcome string)
}

// AuditService validates and stores administrator action records. A storage
// failure is returned to the caller so it can be logged without undoing an
// already completed account action.
type AuditService struct {
	repository AuditRepository
	sink       AuditSink
	now        func() time.Time
}

// NewAuditService constructs audit recording with its persistence port.
func NewAuditService(repository AuditRepository) (*AuditService, error) {
	if repository == nil {
		return nil, ErrNilAuditRepository
	}
	return &AuditService{repository: repository, now: time.Now}, nil
}

// SetSink attaches the security-log sink. It is intended to be called once
// during startup before the server begins serving requests.
func (s *AuditService) SetSink(sink AuditSink) {
	if s == nil {
		return
	}
	s.sink = sink
}

// Record validates and stores one administrator action, then emits it to the
// security log sink. A sink failure cannot affect the stored record.
func (s *AuditService) Record(ctx context.Context, record AuditRecord) error {
	if s == nil || s.repository == nil || s.now == nil {
		return ErrInvalidAuditEntry
	}
	if ctx == nil {
		return errors.New("service: nil audit context")
	}
	entry, err := buildAuditEntry(record, s.now().Unix())
	if err == nil {
		if createErr := s.repository.CreateAuditEntry(ctx, entry); createErr != nil {
			err = fmt.Errorf("create audit entry: %w", createErr)
		}
	}
	// Callers cannot undo a completed action, so a lost audit record must at
	// least reach the security log stream.
	if s.sink != nil {
		outcome := AuditOutcomeRecorded
		if err != nil {
			outcome = AuditOutcomeFailed
		}
		s.sink.AuditRecorded(ctx, record, outcome)
	}
	return err
}

// List returns one bounded page of administrator action history, newest first.
func (s *AuditService) List(ctx context.Context, query AuditQuery) (AuditPage, error) {
	if s == nil || s.repository == nil {
		return AuditPage{}, ErrInvalidAuditEntry
	}
	if ctx == nil {
		return AuditPage{}, errors.New("service: nil audit context")
	}
	normalized := query.Normalize()
	page, err := s.repository.ListAuditEntries(ctx, normalized)
	if err != nil {
		return AuditPage{}, err
	}
	if page.Size < 1 {
		page.Size = normalized.Size
	}
	if page.Page < 1 {
		page.Page = normalized.Page
	}
	page.Pages = adminPageCount(page.Total, page.Size)
	return page, nil
}

func buildAuditEntry(record AuditRecord, occurredAt int64) (AuditEntry, error) {
	if record.ActorUserID <= 0 || occurredAt <= 0 {
		return AuditEntry{}, ErrInvalidAuditEntry
	}
	action := strings.TrimSpace(record.Action)
	if action == "" || len(action) > maxAuditActionLength || !printable(action) {
		return AuditEntry{}, ErrInvalidAuditEntry
	}
	actorEmail := strings.TrimSpace(record.ActorEmail)
	if utf8.RuneCountInString(actorEmail) > maxAuditActorEmail || !printable(actorEmail) {
		return AuditEntry{}, ErrInvalidAuditEntry
	}
	targetType := strings.TrimSpace(record.TargetType)
	if len(targetType) > maxAuditTargetType || !printable(targetType) {
		return AuditEntry{}, ErrInvalidAuditEntry
	}
	targetID := strings.TrimSpace(record.TargetID)
	if len(targetID) > maxAuditTargetID || !printable(targetID) {
		return AuditEntry{}, ErrInvalidAuditEntry
	}
	detail := strings.TrimSpace(record.Detail)
	if utf8.RuneCountInString(detail) > maxAuditDetailLength || !printable(detail) {
		return AuditEntry{}, ErrInvalidAuditEntry
	}
	ip := strings.TrimSpace(record.IP)
	if len(ip) > maxAuditOrigin || !printable(ip) {
		return AuditEntry{}, ErrInvalidAuditEntry
	}
	actorID := record.ActorUserID
	entry := AuditEntry{
		OccurredAt:  occurredAt,
		ActorUserID: &actorID,
		Action:      action,
	}
	entry.ActorEmail = optionalString(actorEmail)
	entry.TargetType = optionalString(targetType)
	entry.TargetID = optionalString(targetID)
	entry.Detail = optionalString(detail)
	entry.IP = optionalString(ip)
	return entry, nil
}

func printable(value string) bool {
	for _, character := range value {
		if character == 0 || (character < 0x20 && character != '\t') || character == 0x7f {
			return false
		}
	}
	return true
}
