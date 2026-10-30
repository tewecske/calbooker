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

const (
	maxResourceNameLength        = 64
	maxResourceDescriptionLength = 255
	// MaxResourceConcurrentTeams bounds the optional limit of teams that may
	// use a resource at the same time.
	MaxResourceConcurrentTeams = 99
)

// Stable resource audit action codes; the group is the target.
const (
	AuditActionResourceCreated  = "group.resource.created"
	AuditActionResourceUpdated  = "group.resource.updated"
	AuditActionResourceArchived = "group.resource.archived"
	AuditActionResourceRestored = "group.resource.restored"
)

var (
	// ErrResourceNotFound identifies a resource that does not exist in the
	// group or that the account may not see.
	ErrResourceNotFound = errors.New("service: resource not found")
	// ErrResourceArchived identifies an archived resource offered for a new
	// plan slot or event.
	ErrResourceArchived = errors.New("service: resource archived")
	// ErrInvalidResourceName identifies a resource name outside storage limits.
	ErrInvalidResourceName = errors.New("service: invalid resource name")
	// ErrResourceNameTaken identifies a name already used by another active
	// resource of the group.
	ErrResourceNameTaken = errors.New("service: resource name taken")
	// ErrResourceKindArchived identifies an archived kind chosen for a new
	// or changed resource kind.
	ErrResourceKindArchived = errors.New("service: resource kind archived")
	// ErrInvalidResourceKind identifies a kind that is not one of the group's
	// resource kinds.
	ErrInvalidResourceKind = errors.New("service: invalid resource kind")
	// ErrInvalidResourceDescription identifies a description outside storage
	// limits.
	ErrInvalidResourceDescription = errors.New("service: invalid resource description")
	// ErrInvalidResourceCapacity identifies a concurrent-team limit outside
	// 1..MaxResourceConcurrentTeams.
	ErrInvalidResourceCapacity = errors.New("service: invalid resource capacity")
	// ErrNilResourceRepository identifies missing resource persistence.
	ErrNilResourceRepository = errors.New("service: nil resource repository")
)

// Resource is a field, court, hall, pool, or room a group books. Archived
// resources stay loadable for the plans and events that reference them.
type Resource struct {
	ID      int64
	GroupID int64
	Name    string
	// KindID is the group's resource kind. KindCode (the system code, empty
	// for kinds an administrator added) and the names are filled on reads.
	KindID      int64
	KindCode    string
	KindNameEN  string
	KindNameHU  string
	Description *string
	// MaxConcurrentTeams limits how many teams may use the resource at the
	// same time; nil is no limit.
	MaxConcurrentTeams *int
	ArchivedAt         *int64
	CreatedAt          int64
	Version            int64
}

// KindName returns the resource kind's name for a language code.
func (r Resource) KindName(language string) string {
	return Kind{NameEN: r.KindNameEN, NameHU: r.KindNameHU}.Name(language)
}

// Archived reports whether the resource is archived.
func (r Resource) Archived() bool {
	return r.ArchivedAt != nil
}

// ResourceInput is the editable part of a resource. MaxConcurrentTeams 0 is
// no limit.
type ResourceInput struct {
	Name               string
	KindID             int64
	Description        string
	MaxConcurrentTeams int
}

// ResourceRepository persists resources. Every method is scoped to one group.
type ResourceRepository interface {
	// CreateResource returns ErrResourceNameTaken for a duplicate active name.
	CreateResource(context.Context, Resource) (Resource, error)
	// FindResource returns ErrResourceNotFound for a resource outside the
	// group. Archived resources are returned.
	FindResource(ctx context.Context, groupID, resourceID int64) (Resource, error)
	// ListResources returns active resources first, then archived ones.
	ListResources(ctx context.Context, groupID int64, includeArchived bool) ([]Resource, error)
	// UpdateResource writes the editable fields and archive state at the
	// expected revision; stale writes classify through ClassifyStaleWrite.
	UpdateResource(ctx context.Context, resource Resource, expectedVersion int64) (Resource, error)
}

// ResourceService owns resource use cases. Authorization comes from the
// caller's GroupAccess.
type ResourceService struct {
	resources ResourceRepository
	users     UserRepository
	auditor   AuditRecorder
	kinds     KindSource
	now       func() time.Time
}

// NewResourceService constructs resource use cases with explicit dependencies.
func NewResourceService(resources ResourceRepository, users UserRepository) (*ResourceService, error) {
	if resources == nil {
		return nil, ErrNilResourceRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &ResourceService{resources: resources, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *ResourceService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// SetKinds attaches the group's resource kinds, which resources check. It is
// intended to be called once during startup.
func (s *ResourceService) SetKinds(kinds KindSource) {
	if s != nil {
		s.kinds = kinds
	}
}

// checkKind validates the chosen kind: it must be one of the group's
// resource kinds, and not archived unless it is the resource's existing kind.
func (s *ResourceService) checkKind(ctx context.Context, access *GroupAccess, chosen, existing int64) error {
	if s.kinds == nil {
		return nil
	}
	_, err := s.kinds.Usable(ctx, access, KindScopeResource, chosen)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrKindArchived) && chosen == existing:
		return nil
	case errors.Is(err, ErrKindNotFound):
		return ErrInvalidResourceKind
	case errors.Is(err, ErrKindArchived):
		return ErrResourceKindArchived
	default:
		return fmt.Errorf("find resource kind: %w", err)
	}
}

// List returns every resource of the group, active ones first, for the
// resource pages. Admins, managers, site administrators, and coaches.
func (s *ResourceService) List(ctx context.Context, access *GroupAccess) ([]Resource, error) {
	if err := s.viewable(ctx, access); err != nil {
		return nil, err
	}
	resources, err := s.resources.ListResources(ctx, access.GroupID(), true)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	return resources, nil
}

// ActiveResources returns the resources that new plan slots and events may
// use (not archived). It backs resource pickers; the same accounts as List.
func (s *ResourceService) ActiveResources(ctx context.Context, access *GroupAccess) ([]Resource, error) {
	if err := s.viewable(ctx, access); err != nil {
		return nil, err
	}
	resources, err := s.resources.ListResources(ctx, access.GroupID(), false)
	if err != nil {
		return nil, fmt.Errorf("list active resources: %w", err)
	}
	return resources, nil
}

// Find returns one resource of the group, archived or not, so existing
// plans and events can show the resource they reference. Every account that
// sees the group may load it; the resource pages also need CanViewResources.
func (s *ResourceService) Find(ctx context.Context, access *GroupAccess, resourceID int64) (Resource, error) {
	if err := s.validate(ctx); err != nil {
		return Resource{}, err
	}
	if !access.CanView() {
		return Resource{}, ErrGroupNotFound
	}
	if resourceID <= 0 {
		return Resource{}, ErrResourceNotFound
	}
	return s.resources.FindResource(ctx, access.GroupID(), resourceID)
}

// Usable returns a resource that a new plan slot or event may reference:
// it must belong to the group and not be archived (ErrResourceArchived).
// Callers authorize their own write; this only checks the resource.
func (s *ResourceService) Usable(ctx context.Context, access *GroupAccess, resourceID int64) (Resource, error) {
	resource, err := s.Find(ctx, access, resourceID)
	if err != nil {
		return Resource{}, err
	}
	if resource.Archived() {
		return Resource{}, ErrResourceArchived
	}
	return resource, nil
}

// Create adds a resource. Admins, managers, and site administrators.
func (s *ResourceService) Create(ctx context.Context, actor GroupActor, input ResourceInput) (Resource, error) {
	if err := s.validate(ctx); err != nil {
		return Resource{}, err
	}
	if err := authorizeGroupAction(actor, actor.Access.CanManageResources()); err != nil {
		return Resource{}, err
	}
	resource, err := buildResource(input)
	if err != nil {
		return Resource{}, err
	}
	if err := s.checkKind(ctx, actor.Access, resource.KindID, 0); err != nil {
		return Resource{}, err
	}
	resource.GroupID = actor.Access.GroupID()
	resource.CreatedAt = s.now().Unix()
	created, err := s.resources.CreateResource(ctx, resource)
	if err != nil {
		if errors.Is(err, ErrResourceNameTaken) {
			return Resource{}, err
		}
		return Resource{}, fmt.Errorf("create resource: %w", err)
	}
	s.record(ctx, actor, AuditActionResourceCreated, created)
	return created, nil
}

// Update changes a resource's name, kind, description, and concurrent-team
// limit at the expected revision. Archived resources can be edited too.
func (s *ResourceService) Update(ctx context.Context, actor GroupActor, resourceID, expectedVersion int64, input ResourceInput) (Resource, error) {
	resource, err := s.findEditable(ctx, actor, resourceID, expectedVersion)
	if err != nil {
		return Resource{}, err
	}
	changed, err := buildResource(input)
	if err != nil {
		return Resource{}, err
	}
	if err := s.checkKind(ctx, actor.Access, changed.KindID, resource.KindID); err != nil {
		return Resource{}, err
	}
	resource.Name, resource.KindID, resource.Description, resource.MaxConcurrentTeams = changed.Name, changed.KindID, changed.Description, changed.MaxConcurrentTeams
	updated, err := s.update(ctx, resource, expectedVersion)
	if err != nil {
		return Resource{}, err
	}
	s.record(ctx, actor, AuditActionResourceUpdated, updated)
	return updated, nil
}

// SetArchived archives or restores a resource at the expected revision.
// Restoring fails with ErrResourceNameTaken when an active resource took the
// name meanwhile.
func (s *ResourceService) SetArchived(ctx context.Context, actor GroupActor, resourceID, expectedVersion int64, archived bool) (Resource, error) {
	resource, err := s.findEditable(ctx, actor, resourceID, expectedVersion)
	if err != nil {
		return Resource{}, err
	}
	if resource.Archived() == archived {
		return resource, nil
	}
	action := AuditActionResourceRestored
	resource.ArchivedAt = nil
	if archived {
		now := s.now().Unix()
		resource.ArchivedAt = &now
		action = AuditActionResourceArchived
	}
	updated, err := s.update(ctx, resource, expectedVersion)
	if err != nil {
		return Resource{}, err
	}
	s.record(ctx, actor, action, updated)
	return updated, nil
}

func (s *ResourceService) viewable(ctx context.Context, access *GroupAccess) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if !access.CanView() {
		return ErrGroupNotFound
	}
	if !access.CanViewResources() {
		return ErrGroupForbidden
	}
	return nil
}

func (s *ResourceService) findEditable(ctx context.Context, actor GroupActor, resourceID, expectedVersion int64) (Resource, error) {
	if err := s.validate(ctx); err != nil {
		return Resource{}, err
	}
	if !actor.Access.CanView() {
		return Resource{}, ErrGroupNotFound
	}
	if resourceID <= 0 {
		return Resource{}, ErrResourceNotFound
	}
	if !actor.Access.CanManageResources() {
		return Resource{}, ErrGroupForbidden
	}
	if expectedVersion < 0 {
		return Resource{}, ErrInvalidGroup
	}
	return s.resources.FindResource(ctx, actor.Access.GroupID(), resourceID)
}

func (s *ResourceService) update(ctx context.Context, resource Resource, expectedVersion int64) (Resource, error) {
	updated, err := s.resources.UpdateResource(ctx, resource, expectedVersion)
	switch {
	case err == nil:
		return updated, nil
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrResourceNotFound):
		// A resource that vanished is not-found, never a conflict.
		return Resource{}, ErrResourceNotFound
	case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrResourceNameTaken):
		return Resource{}, err
	default:
		return Resource{}, fmt.Errorf("update resource: %w", err)
	}
}

func (s *ResourceService) record(ctx context.Context, actor GroupActor, action string, resource Resource) {
	detail := "resource " + strconv.FormatInt(resource.ID, 10) + ": " + resource.Name
	if action == AuditActionResourceCreated || action == AuditActionResourceUpdated {
		detail += "; kind " + resource.KindNameEN
		if resource.MaxConcurrentTeams != nil {
			detail += "; max teams " + strconv.Itoa(*resource.MaxConcurrentTeams)
		}
	}
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func (s *ResourceService) validate(ctx context.Context) error {
	if s == nil || s.resources == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil resource context")
	}
	return nil
}

// buildResource validates resource input. Names and descriptions are
// trimmed; an empty description and a zero limit are stored as none.
func buildResource(input ResourceInput) (Resource, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > maxResourceNameLength || hasControl(name) {
		return Resource{}, ErrInvalidResourceName
	}
	if input.KindID <= 0 {
		return Resource{}, ErrInvalidResourceKind
	}
	resource := Resource{Name: name, KindID: input.KindID}
	description := strings.TrimSpace(input.Description)
	if description != "" {
		if utf8.RuneCountInString(description) > maxResourceDescriptionLength || hasControl(description) {
			return Resource{}, ErrInvalidResourceDescription
		}
		resource.Description = &description
	}
	switch {
	case input.MaxConcurrentTeams == 0:
	case input.MaxConcurrentTeams < 1 || input.MaxConcurrentTeams > MaxResourceConcurrentTeams:
		return Resource{}, ErrInvalidResourceCapacity
	default:
		limit := input.MaxConcurrentTeams
		resource.MaxConcurrentTeams = &limit
	}
	return resource, nil
}
