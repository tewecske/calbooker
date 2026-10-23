package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultAdminPageSize is used when a request omits a page size.
	DefaultAdminPageSize = 20
	// MaxAdminPageSize bounds every administrator list request.
	MaxAdminPageSize = 100
	// MaxAdminPage bounds requested page numbers so the row offset cannot
	// overflow into a negative SQL OFFSET.
	MaxAdminPage         = 1_000_000
	maxAdminSearchLength = 255
)

// Tri-state administrator list filters. The empty value keeps every row.
const (
	AdminFilterAll = ""
	AdminFilterYes = "yes"
	AdminFilterNo  = "no"
)

// Supported administrator list sort keys.
const (
	AdminSortCreatedAt   = "created_at"
	AdminSortID          = "id"
	AdminSortEmail       = "email"
	AdminSortUsername    = "username"
	AdminSortDisplayName = "display_name"
)

// Sort directions accepted from the URL.
const (
	AdminDirectionAsc  = "asc"
	AdminDirectionDesc = "desc"
)

var (
	// ErrInvalidAdminQuery identifies unusable administrator list input.
	ErrInvalidAdminQuery = errors.New("service: invalid admin query")
	// ErrNilAdminUserRepository identifies missing administrator list persistence.
	ErrNilAdminUserRepository = errors.New("service: nil admin user repository")
	// ErrAdminSelfAction identifies an administrator action against their own account that is refused.
	ErrAdminSelfAction = errors.New("service: administrator self-protection")
	// ErrLastAdministrator refuses a demotion or deletion that would leave no
	// administrator.
	ErrLastAdministrator = errors.New("service: last administrator")
)

// AdminUserQuery is the bounded, server-validated account-list query. Page,
// size, sorting, and filter values are clamped by Normalize so a hostile URL
// cannot request unbounded work.
type AdminUserQuery struct {
	Search    string
	Admin     string
	Guest     string
	Confirmed string
	Sort      string
	Direction string
	Page      int
	Size      int
}

// AdminUserPage is one page of accounts plus its total count.
type AdminUserPage struct {
	Users []User
	Total int
	Page  int
	Size  int
	Pages int
}

// Normalize returns a copy with every field bounded to a supported value.
// Invalid values fall back to safe defaults instead of failing the request.
func (q AdminUserQuery) Normalize() AdminUserQuery {
	q.Search = normalizeAdminSearch(q.Search)
	q.Admin = normalizeAdminTriState(q.Admin)
	q.Guest = normalizeAdminTriState(q.Guest)
	q.Confirmed = normalizeAdminTriState(q.Confirmed)
	q.Sort = normalizeAdminSort(q.Sort)
	q.Direction = normalizeAdminDirection(q.Direction, q.Sort)
	q.Page = min(max(q.Page, 1), MaxAdminPage)
	if q.Size < 1 {
		q.Size = DefaultAdminPageSize
	}
	if q.Size > MaxAdminPageSize {
		q.Size = MaxAdminPageSize
	}
	return q
}

// Offset returns the zero-based row offset for the normalized page.
func (q AdminUserQuery) Offset() int {
	if q.Page < 1 {
		return 0
	}
	return (min(q.Page, MaxAdminPage) - 1) * q.Size
}

// AdminUserRepository lists accounts for the administrator area. Implementations
// must apply search, filters, sorting, and pagination in the data store.
type AdminUserRepository interface {
	ListUsers(context.Context, AdminUserQuery) (AdminUserPage, error)
}

// AdminActionContext identifies the acting administrator and request origin
// for one audited action. Origin is a safe client identifier, never a URL.
type AdminActionContext struct {
	ActorID int64
	Origin  string
}

// SessionRevoker ends every active session for an account.
type SessionRevoker interface {
	RevokeUser(context.Context, int64) error
}

// AdminAccountService owns administrator account use cases. It applies server
// side validation before every repository call.
type AdminAccountService struct {
	users      UserRepository
	adminUsers AdminUserRepository
	auditor    AuditRecorder
	sessions   SessionRevoker
}

// NewAdminAccountService constructs administrator account use cases with
// explicit account, list, audit, and session dependencies. The auditor is
// optional and never blocks an account action on a recording failure.
func NewAdminAccountService(users UserRepository, adminUsers AdminUserRepository, auditor AuditRecorder, sessions SessionRevoker) (*AdminAccountService, error) {
	if users == nil {
		return nil, ErrNilUserRepository
	}
	if adminUsers == nil {
		return nil, ErrNilAdminUserRepository
	}
	return &AdminAccountService{users: users, adminUsers: adminUsers, auditor: auditor, sessions: sessions}, nil
}

// RevokeSessions ends every active session for an account and records the
// action. A missing account is reported as not-found.
func (s *AdminAccountService) RevokeSessions(ctx context.Context, actor AdminActionContext, targetID int64) error {
	if s == nil || s.users == nil || s.adminUsers == nil || s.sessions == nil {
		return ErrInvalidAdminQuery
	}
	if ctx == nil {
		return errors.New("service: nil admin session context")
	}
	if actor.ActorID <= 0 || targetID <= 0 {
		return ErrInvalidAdminQuery
	}
	target, err := s.users.FindUserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return ErrRecordNotFound
		}
		return err
	}
	if err := s.sessions.RevokeUser(ctx, targetID); err != nil {
		return err
	}
	s.record(ctx, actor, AuditActionSessionsRevoked, target, adminAccountDetail(target))
	return nil
}

// AdminAccountUpdateInput contains the editable fields for one account. The
// issuer owns email and name, so an administrator changes only the role here.
type AdminAccountUpdateInput struct {
	IsAdmin bool
	Version int64
}

// Find returns one account's safe detail for the administrator area.
func (s *AdminAccountService) Find(ctx context.Context, targetID int64) (User, error) {
	if s == nil || s.users == nil || s.adminUsers == nil {
		return User{}, ErrInvalidAdminQuery
	}
	if ctx == nil {
		return User{}, errors.New("service: nil admin find context")
	}
	if targetID <= 0 {
		return User{}, ErrInvalidAdminQuery
	}
	user, err := s.users.FindUserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrRecordNotFound
		}
		return User{}, err
	}
	return user, nil
}

// Update atomically changes an account's administrator status using the
// caller's expected revision. A stale revision is a conflict; a vanished
// account is not-found.
func (s *AdminAccountService) Update(ctx context.Context, actor AdminActionContext, targetID int64, input AdminAccountUpdateInput) (User, error) {
	if s == nil || s.users == nil || s.adminUsers == nil {
		return User{}, ErrInvalidAdminQuery
	}
	if ctx == nil {
		return User{}, errors.New("service: nil admin update context")
	}
	if actor.ActorID <= 0 || targetID <= 0 || input.Version < 0 {
		return User{}, ErrInvalidAdminQuery
	}
	if actor.ActorID == targetID && !input.IsAdmin {
		return User{}, ErrAdminSelfAction
	}
	current, err := s.users.FindUserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrRecordNotFound
		}
		return User{}, err
	}
	wasAdmin := current.IsAdmin
	current.IsAdmin = input.IsAdmin
	updated, err := s.users.UpdateUser(ctx, current, input.Version)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return User{}, ErrRecordNotFound
		}
		return User{}, err
	}
	detail := adminAccountDetail(updated)
	s.record(ctx, actor, AuditActionAccountUpdated, updated, detail)
	if updated.IsAdmin != wasAdmin {
		action := AuditActionAdminRevoked
		if updated.IsAdmin {
			action = AuditActionAdminGranted
		}
		s.record(ctx, actor, action, updated, detail)
	}
	return updated, nil
}

// Delete removes an account and its dependent credentials after an explicit
// confirmation. Owned records cascade at the data store, while historical
// audit and sign-in records survive with nullable references. An administrator
// can never delete their own account.
func (s *AdminAccountService) Delete(ctx context.Context, actor AdminActionContext, targetID, expectedVersion int64) error {
	if s == nil || s.users == nil || s.adminUsers == nil {
		return ErrInvalidAdminQuery
	}
	if ctx == nil {
		return errors.New("service: nil admin delete context")
	}
	if actor.ActorID <= 0 || targetID <= 0 || expectedVersion < 0 {
		return ErrInvalidAdminQuery
	}
	if actor.ActorID == targetID {
		return ErrAdminSelfAction
	}
	target, err := s.users.FindUserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return ErrRecordNotFound
		}
		return err
	}
	// Deletion cascades away the account's session records, including the
	// single sign-on session IDs, so revoke first while they still exist.
	if s.sessions != nil {
		if err := s.sessions.RevokeUser(ctx, targetID); err != nil {
			return err
		}
	}
	if err := s.users.DeleteUser(ctx, targetID, expectedVersion); err != nil {
		switch {
		case errors.Is(err, ErrUserNotFound):
			return ErrRecordNotFound
		case errors.Is(err, ErrOptimisticLockConflict):
			return err
		default:
			return err
		}
	}
	s.record(ctx, actor, AuditActionAccountDeleted, target, adminAccountDetail(target))
	return nil
}

func adminAccountDetail(user User) string {
	if user.Email != nil {
		return *user.Email
	}
	if user.Username != nil {
		return *user.Username
	}
	return strconv.FormatInt(user.ID, 10)
}

// record stores one administrator action as a best-effort audit event. A
// recording failure must not undo the completed account action.
func (s *AdminAccountService) record(ctx context.Context, actor AdminActionContext, action string, target User, detail string) {
	if s == nil || s.auditor == nil {
		return
	}
	record := AuditRecord{
		ActorUserID: actor.ActorID,
		Action:      action,
		TargetType:  "user",
		TargetID:    strconv.FormatInt(target.ID, 10),
		Detail:      detail,
		IP:          actor.Origin,
	}
	if actorUser, err := s.users.FindUserByID(ctx, actor.ActorID); err == nil && actorUser.Email != nil {
		record.ActorEmail = *actorUser.Email
	}
	_ = s.auditor.Record(ctx, record)
}

// List returns one bounded page of accounts.
func (s *AdminAccountService) List(ctx context.Context, query AdminUserQuery) (AdminUserPage, error) {
	if s == nil || s.users == nil || s.adminUsers == nil {
		return AdminUserPage{}, ErrInvalidAdminQuery
	}
	if ctx == nil {
		return AdminUserPage{}, errors.New("service: nil admin query context")
	}
	normalized := query.Normalize()
	page, err := s.adminUsers.ListUsers(ctx, normalized)
	if err != nil {
		return AdminUserPage{}, err
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

func adminPageCount(total, size int) int {
	if total <= 0 || size <= 0 {
		return 0
	}
	return (total + size - 1) / size
}

func normalizeAdminSearch(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.ReplaceAll(trimmed, "\x00", "")
	if utf8.RuneCountInString(trimmed) <= maxAdminSearchLength {
		return trimmed
	}
	runes := []rune(trimmed)
	return string(runes[:maxAdminSearchLength])
}

func normalizeAdminTriState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case AdminFilterYes:
		return AdminFilterYes
	case AdminFilterNo:
		return AdminFilterNo
	default:
		return AdminFilterAll
	}
}

func normalizeAdminSort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case AdminSortID:
		return AdminSortID
	case AdminSortEmail:
		return AdminSortEmail
	case AdminSortUsername:
		return AdminSortUsername
	case AdminSortDisplayName:
		return AdminSortDisplayName
	default:
		return AdminSortCreatedAt
	}
}

func normalizeAdminDirection(raw, sort string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case AdminDirectionAsc:
		return AdminDirectionAsc
	case AdminDirectionDesc:
		return AdminDirectionDesc
	default:
		if sort == AdminSortCreatedAt || sort == AdminSortID {
			return AdminDirectionDesc
		}
		return AdminDirectionAsc
	}
}

// AdminSearchPattern escapes LIKE wildcards and wraps the term for a
// case-insensitive substring match.
func AdminSearchPattern(search string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
	return "%" + escaped + "%"
}
