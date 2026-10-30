package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// KindScope says which kind list a call works on: the event kinds (#8) or
// the resource kinds (#4) of one group.
type KindScope string

const (
	// KindScopeEvent selects a group's event kinds.
	KindScopeEvent KindScope = "event"
	// KindScopeResource selects a group's resource kinds.
	KindScopeResource KindScope = "resource"
)

// Valid reports whether the scope is a known kind list.
func (s KindScope) Valid() bool {
	return s == KindScopeEvent || s == KindScopeResource
}

// Event kind system codes. A system code is a stable identifier of a seeded
// kind; administrators can rename such a kind but its code stays. Only
// EventKindTraining carries behaviour (event generation, #15 creates it), so
// it can be neither deleted nor archived. The other seeded kinds (and every
// seeded resource kind) are ordinary rows that keep their code for the
// calendar colours and the demo data.
const (
	EventKindTraining     = "training"
	EventKindMatch        = "match"
	EventKindChampionship = "championship"
	EventKindOther        = "other"
)

const (
	// MaxKindNameLength bounds a kind name in either language.
	MaxKindNameLength = 48
	// MaxKindsPerScope bounds the kinds of one group and scope.
	MaxKindsPerScope = 50
)

// Stable kind audit action codes; the group is the target.
const (
	AuditActionEventKindCreated    = "group.event_kind.created"
	AuditActionEventKindRenamed    = "group.event_kind.renamed"
	AuditActionEventKindArchived   = "group.event_kind.archived"
	AuditActionEventKindRestored   = "group.event_kind.restored"
	AuditActionEventKindMoved      = "group.event_kind.moved"
	AuditActionEventKindDeleted    = "group.event_kind.deleted"
	AuditActionResourceKindCreated = "group.resource_kind.created"
	AuditActionResourceKindRenamed = "group.resource_kind.renamed"
	// The remaining resource kind codes.
	AuditActionResourceKindArchived = "group.resource_kind.archived"
	AuditActionResourceKindRestored = "group.resource_kind.restored"
	AuditActionResourceKindMoved    = "group.resource_kind.moved"
	AuditActionResourceKindDeleted  = "group.resource_kind.deleted"
)

var (
	// ErrKindNotFound identifies a kind that does not exist in the group.
	ErrKindNotFound = errors.New("service: kind not found")
	// ErrKindArchived identifies an archived kind offered for a new item.
	ErrKindArchived = errors.New("service: kind archived")
	// ErrInvalidKindName identifies a name outside storage limits.
	ErrInvalidKindName = errors.New("service: invalid kind name")
	// ErrKindNameTaken identifies a name used by another active kind.
	ErrKindNameTaken = errors.New("service: kind name taken")
	// ErrKindInUse identifies a delete of a kind that events or resources
	// still use.
	ErrKindInUse = errors.New("service: kind in use")
	// ErrKindProtected identifies a delete or archive of a kind with
	// behaviour (the training event kind).
	ErrKindProtected = errors.New("service: kind protected")
	// ErrTooManyKinds identifies a group at MaxKindsPerScope.
	ErrTooManyKinds = errors.New("service: too many kinds")
	// ErrInvalidKindScope identifies an unknown kind list.
	ErrInvalidKindScope = errors.New("service: invalid kind scope")
	// ErrNilKindRepository identifies missing kind persistence.
	ErrNilKindRepository = errors.New("service: nil kind repository")
)

// Kind is one event kind or resource kind of a group. SystemCode is empty
// for kinds an administrator added.
type Kind struct {
	ID         int64
	GroupID    int64
	SystemCode string
	NameEN     string
	// NameHU is empty when the Hungarian name is the English one.
	NameHU     string
	Position   int
	ArchivedAt *int64
	CreatedAt  int64
	Version    int64
	// Uses counts the events or resources that use the kind; it is filled
	// on list reads.
	Uses int
}

// Name returns the name for a language code, falling back to English.
func (k Kind) Name(language string) string {
	if language == "hu" && k.NameHU != "" {
		return k.NameHU
	}
	return k.NameEN
}

// Archived reports whether the kind is archived.
func (k Kind) Archived() bool {
	return k.ArchivedAt != nil
}

// Protected reports whether the kind has behaviour and so can be renamed
// and moved but neither archived nor deleted.
func (k Kind) Protected(scope KindScope) bool {
	return scope == KindScopeEvent && k.SystemCode == EventKindTraining
}

// KindInput is the editable name of a kind. NameHU may be empty.
type KindInput struct {
	NameEN string
	NameHU string
}

// DefaultKind is one row a new group starts with.
type DefaultKind struct {
	Code   string
	NameEN string
	NameHU string
}

// DefaultEventKinds returns the event kinds every new group starts with, in
// display order. Keep migration 000014 in step.
func DefaultEventKinds() []DefaultKind {
	return []DefaultKind{
		{Code: EventKindTraining, NameEN: "Training", NameHU: "Edzés"},
		{Code: EventKindMatch, NameEN: "Match", NameHU: "Mérkőzés"},
		{Code: EventKindChampionship, NameEN: "Championship", NameHU: "Bajnokság"},
		{Code: EventKindOther, NameEN: "Other", NameHU: "Egyéb"},
	}
}

// DefaultResourceKinds returns the resource kinds every new group starts
// with, in display order. Keep migration 000014 in step.
func DefaultResourceKinds() []DefaultKind {
	return []DefaultKind{
		{Code: "field", NameEN: "Field", NameHU: "Füves pálya"},
		{Code: "court", NameEN: "Court", NameHU: "Pálya"},
		{Code: "hall", NameEN: "Sports hall", NameHU: "Sportcsarnok"},
		{Code: "pool", NameEN: "Pool", NameHU: "Uszoda"},
		{Code: "room", NameEN: "Room", NameHU: "Terem"},
		{Code: "other", NameEN: "Other", NameHU: "Egyéb"},
	}
}

// DefaultKinds returns the defaults of a scope.
func DefaultKinds(scope KindScope) []DefaultKind {
	if scope == KindScopeResource {
		return DefaultResourceKinds()
	}
	return DefaultEventKinds()
}

// KindRepository persists event kinds and resource kinds. Every method is
// scoped to one group and one KindScope.
type KindRepository interface {
	// ListKinds returns the group's kinds by position, with Uses filled.
	ListKinds(ctx context.Context, scope KindScope, groupID int64) ([]Kind, error)
	// FindKind returns ErrKindNotFound for a kind outside the group.
	FindKind(ctx context.Context, scope KindScope, groupID, kindID int64) (Kind, error)
	// CreateKind appends the kind (the repository sets Position) and returns
	// ErrKindNameTaken for a duplicate active name.
	CreateKind(ctx context.Context, scope KindScope, kind Kind) (Kind, error)
	// UpdateKind writes names and archive state at the expected revision;
	// stale writes classify through ClassifyStaleWrite.
	UpdateKind(ctx context.Context, scope KindScope, kind Kind, expectedVersion int64) (Kind, error)
	// MoveKind swaps the kind with its neighbour (offset -1 up, +1 down).
	// Moving past either end changes nothing.
	MoveKind(ctx context.Context, scope KindScope, groupID, kindID int64, offset int) error
	// DeleteKind removes the kind at the expected revision; a kind that
	// events or resources use is ErrKindInUse.
	DeleteKind(ctx context.Context, scope KindScope, groupID, kindID, expectedVersion int64) error
	// SeedDefaultKinds inserts the default rows of both scopes for a group.
	// It is idempotent: rows with an existing system code are kept.
	SeedDefaultKinds(ctx context.Context, groupID int64, createdAt int64) error
}

// KindService owns kind use cases. Authorization comes from the caller's
// GroupAccess.
type KindService struct {
	kinds   KindRepository
	users   UserRepository
	auditor AuditRecorder
	now     func() time.Time
}

// NewKindService constructs kind use cases with explicit dependencies.
func NewKindService(kinds KindRepository, users UserRepository) (*KindService, error) {
	if kinds == nil {
		return nil, ErrNilKindRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &KindService{kinds: kinds, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *KindService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// All returns every kind of the group, archived ones included, by position.
// Every account that sees the group may read it: kind names are shown with
// the events and resources that use them, and back the filters.
func (s *KindService) All(ctx context.Context, access *GroupAccess, scope KindScope) ([]Kind, error) {
	if err := s.readable(ctx, access, scope); err != nil {
		return nil, err
	}
	kinds, err := s.kinds.ListKinds(ctx, scope, access.GroupID())
	if err != nil {
		return nil, fmt.Errorf("list %s kinds: %w", scope, err)
	}
	return kinds, nil
}

// Active returns the kinds a new item may use (not archived), by position.
func (s *KindService) Active(ctx context.Context, access *GroupAccess, scope KindScope) ([]Kind, error) {
	kinds, err := s.All(ctx, access, scope)
	if err != nil {
		return nil, err
	}
	active := kinds[:0:0]
	for _, kind := range kinds {
		if !kind.Archived() {
			active = append(active, kind)
		}
	}
	return active, nil
}

// Find returns one kind of the group, archived or not. A kind of another
// group is ErrKindNotFound.
func (s *KindService) Find(ctx context.Context, access *GroupAccess, scope KindScope, kindID int64) (Kind, error) {
	if err := s.readable(ctx, access, scope); err != nil {
		return Kind{}, err
	}
	if kindID <= 0 {
		return Kind{}, ErrKindNotFound
	}
	return s.kinds.FindKind(ctx, scope, access.GroupID(), kindID)
}

// Usable returns a kind that a new item may reference: it belongs to the
// group and is not archived (ErrKindArchived). Callers authorize their own
// write; this only checks the kind.
func (s *KindService) Usable(ctx context.Context, access *GroupAccess, scope KindScope, kindID int64) (Kind, error) {
	kind, err := s.Find(ctx, access, scope, kindID)
	if err != nil {
		return Kind{}, err
	}
	if kind.Archived() {
		return Kind{}, ErrKindArchived
	}
	return kind, nil
}

// Create appends a kind. Admins, managers, and site administrators.
func (s *KindService) Create(ctx context.Context, actor GroupActor, scope KindScope, input KindInput) (Kind, error) {
	if err := s.manageable(ctx, actor, scope); err != nil {
		return Kind{}, err
	}
	kind, err := buildKind(input)
	if err != nil {
		return Kind{}, err
	}
	existing, err := s.kinds.ListKinds(ctx, scope, actor.Access.GroupID())
	if err != nil {
		return Kind{}, fmt.Errorf("list %s kinds: %w", scope, err)
	}
	if len(existing) >= MaxKindsPerScope {
		return Kind{}, ErrTooManyKinds
	}
	kind.GroupID = actor.Access.GroupID()
	kind.CreatedAt = s.now().Unix()
	created, err := s.kinds.CreateKind(ctx, scope, kind)
	if err != nil {
		if errors.Is(err, ErrKindNameTaken) {
			return Kind{}, err
		}
		return Kind{}, fmt.Errorf("create %s kind: %w", scope, err)
	}
	s.record(ctx, actor, scope, "created", created)
	return created, nil
}

// Rename changes the names of a kind at the expected revision. Protected
// kinds can be renamed too.
func (s *KindService) Rename(ctx context.Context, actor GroupActor, scope KindScope, kindID, expectedVersion int64, input KindInput) (Kind, error) {
	kind, err := s.findEditable(ctx, actor, scope, kindID, expectedVersion)
	if err != nil {
		return Kind{}, err
	}
	changed, err := buildKind(input)
	if err != nil {
		return Kind{}, err
	}
	kind.NameEN, kind.NameHU = changed.NameEN, changed.NameHU
	updated, err := s.update(ctx, scope, kind, expectedVersion)
	if err != nil {
		return Kind{}, err
	}
	s.record(ctx, actor, scope, "renamed", updated)
	return updated, nil
}

// SetArchived archives or restores a kind at the expected revision. A
// protected kind cannot be archived (ErrKindProtected). Restoring fails with
// ErrKindNameTaken when an active kind took the name meanwhile.
func (s *KindService) SetArchived(ctx context.Context, actor GroupActor, scope KindScope, kindID, expectedVersion int64, archived bool) (Kind, error) {
	kind, err := s.findEditable(ctx, actor, scope, kindID, expectedVersion)
	if err != nil {
		return Kind{}, err
	}
	if kind.Archived() == archived {
		return kind, nil
	}
	verb := "restored"
	kind.ArchivedAt = nil
	if archived {
		if kind.Protected(scope) {
			return Kind{}, ErrKindProtected
		}
		now := s.now().Unix()
		kind.ArchivedAt = &now
		verb = "archived"
	}
	updated, err := s.update(ctx, scope, kind, expectedVersion)
	if err != nil {
		return Kind{}, err
	}
	s.record(ctx, actor, scope, verb, updated)
	return updated, nil
}

// Move shifts a kind one place up (offset -1) or down (offset +1) in the
// group's display order.
func (s *KindService) Move(ctx context.Context, actor GroupActor, scope KindScope, kindID int64, offset int) error {
	kind, err := s.findEditable(ctx, actor, scope, kindID, 0)
	if err != nil {
		return err
	}
	if offset != -1 && offset != 1 {
		return ErrInvalidGroup
	}
	if err := s.kinds.MoveKind(ctx, scope, actor.Access.GroupID(), kindID, offset); err != nil {
		if errors.Is(err, ErrKindNotFound) || errors.Is(err, ErrRecordNotFound) {
			return ErrKindNotFound
		}
		return fmt.Errorf("move %s kind: %w", scope, err)
	}
	s.record(ctx, actor, scope, "moved", kind)
	return nil
}

// Delete removes a kind that nothing uses. Protected kinds and kinds in use
// are refused (ErrKindProtected, ErrKindInUse).
func (s *KindService) Delete(ctx context.Context, actor GroupActor, scope KindScope, kindID, expectedVersion int64) error {
	kind, err := s.findEditable(ctx, actor, scope, kindID, expectedVersion)
	if err != nil {
		return err
	}
	if kind.Protected(scope) {
		return ErrKindProtected
	}
	if err := s.kinds.DeleteKind(ctx, scope, actor.Access.GroupID(), kindID, expectedVersion); err != nil {
		switch {
		case errors.Is(err, ErrKindInUse), errors.Is(err, ErrOptimisticLockConflict):
			return err
		case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrKindNotFound):
			// A kind that vanished is not-found, never a conflict.
			return ErrKindNotFound
		}
		return fmt.Errorf("delete %s kind: %w", scope, err)
	}
	s.record(ctx, actor, scope, "deleted", kind)
	return nil
}

// SeedDefaults gives a new group the default kinds. It is idempotent.
func (s *KindService) SeedDefaults(ctx context.Context, groupID int64) error {
	if s == nil || s.kinds == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if groupID <= 0 {
		return ErrGroupNotFound
	}
	if err := s.kinds.SeedDefaultKinds(ctx, groupID, s.now().Unix()); err != nil {
		return fmt.Errorf("seed default kinds: %w", err)
	}
	return nil
}

func (s *KindService) readable(ctx context.Context, access *GroupAccess, scope KindScope) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if !scope.Valid() {
		return ErrInvalidKindScope
	}
	if !access.CanView() {
		return ErrGroupNotFound
	}
	return nil
}

func (s *KindService) manageable(ctx context.Context, actor GroupActor, scope KindScope) error {
	if err := s.readable(ctx, actor.Access, scope); err != nil {
		return err
	}
	return authorizeGroupAction(actor, actor.Access.CanManageKinds())
}

func (s *KindService) findEditable(ctx context.Context, actor GroupActor, scope KindScope, kindID, expectedVersion int64) (Kind, error) {
	if err := s.manageable(ctx, actor, scope); err != nil {
		return Kind{}, err
	}
	if kindID <= 0 {
		return Kind{}, ErrKindNotFound
	}
	if expectedVersion < 0 {
		return Kind{}, ErrInvalidGroup
	}
	return s.kinds.FindKind(ctx, scope, actor.Access.GroupID(), kindID)
}

func (s *KindService) update(ctx context.Context, scope KindScope, kind Kind, expectedVersion int64) (Kind, error) {
	updated, err := s.kinds.UpdateKind(ctx, scope, kind, expectedVersion)
	switch {
	case err == nil:
		return updated, nil
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrKindNotFound):
		// A kind that vanished is not-found, never a conflict.
		return Kind{}, ErrKindNotFound
	case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrKindNameTaken):
		return Kind{}, err
	default:
		return Kind{}, fmt.Errorf("update %s kind: %w", scope, err)
	}
}

func (s *KindService) record(ctx context.Context, actor GroupActor, scope KindScope, verb string, kind Kind) {
	detail := string(scope) + " kind " + strconv.FormatInt(kind.ID, 10) + ": " + kind.NameEN
	if kind.SystemCode != "" {
		detail += "; code " + kind.SystemCode
	}
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), "group."+string(scope)+"_kind."+verb, detail)
}

func (s *KindService) validate(ctx context.Context) error {
	if s == nil || s.kinds == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil kind context")
	}
	return nil
}

// buildKind validates kind input. Names are trimmed; the English name is
// required and the Hungarian one optional.
func buildKind(input KindInput) (Kind, error) {
	en := strings.TrimSpace(input.NameEN)
	hu := strings.TrimSpace(input.NameHU)
	if en == "" || utf8.RuneCountInString(en) > MaxKindNameLength || hasControl(en) {
		return Kind{}, ErrInvalidKindName
	}
	if utf8.RuneCountInString(hu) > MaxKindNameLength || hasControl(hu) {
		return Kind{}, ErrInvalidKindName
	}
	return Kind{NameEN: en, NameHU: hu}, nil
}

// KindSource is the part of KindService events and resources use to check
// the kind they reference.
type KindSource interface {
	Usable(ctx context.Context, access *GroupAccess, scope KindScope, kindID int64) (Kind, error)
}
