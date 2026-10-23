package httpadapter

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/tewecske/calbooker/internal/service"
)

// Admin list URL parameter names. Keeping them in one place lets handlers and
// templates preserve the same state.
const (
	adminParamSearch    = "q"
	adminParamAdmin     = "admin"
	adminParamGuest     = "guest"
	adminParamConfirmed = "confirmed"
	adminParamSort      = "sort"
	adminParamDirection = "dir"
	adminParamPage      = "page"
	adminParamSize      = "size"
)

// adminListState holds the normalized account-list query and converts it to and
// from URL parameters. Every value read from the URL is clamped, so an invalid
// page, size, sort, or filter never reaches the repository.
type adminListState struct {
	Query service.AdminUserQuery
}

// parseAdminListState reads bounded list state from the request query string.
func parseAdminListState(request *http.Request) adminListState {
	if request == nil {
		return adminListState{Query: service.AdminUserQuery{}.Normalize()}
	}
	values := request.URL.Query()
	page, _ := strconv.Atoi(values.Get(adminParamPage))
	size, _ := strconv.Atoi(values.Get(adminParamSize))
	query := service.AdminUserQuery{
		Search:    values.Get(adminParamSearch),
		Admin:     values.Get(adminParamAdmin),
		Guest:     values.Get(adminParamGuest),
		Confirmed: values.Get(adminParamConfirmed),
		Sort:      values.Get(adminParamSort),
		Direction: values.Get(adminParamDirection),
		Page:      page,
		Size:      size,
	}
	return adminListState{Query: query.Normalize()}
}

// Values renders the normalized state as URL parameters.
func (s adminListState) Values() url.Values {
	query := s.Query.Normalize()
	values := url.Values{}
	if query.Search != "" {
		values.Set(adminParamSearch, query.Search)
	}
	if query.Admin != service.AdminFilterAll {
		values.Set(adminParamAdmin, query.Admin)
	}
	if query.Guest != service.AdminFilterAll {
		values.Set(adminParamGuest, query.Guest)
	}
	if query.Confirmed != service.AdminFilterAll {
		values.Set(adminParamConfirmed, query.Confirmed)
	}
	if query.Sort != service.AdminSortCreatedAt {
		values.Set(adminParamSort, query.Sort)
	}
	if query.Direction != defaultAdminDirection(query.Sort) {
		values.Set(adminParamDirection, query.Direction)
	}
	values.Set(adminParamPage, strconv.Itoa(query.Page))
	values.Set(adminParamSize, strconv.Itoa(query.Size))
	return values
}

// URL renders the state relative to one administrator list base path.
func (s adminListState) URL(base string) string {
	encoded := s.Values().Encode()
	if encoded == "" {
		return base
	}
	return base + "?" + encoded
}

// WithPage returns the state for another one-based page.
func (s adminListState) WithPage(page int) adminListState {
	s.Query.Page = page
	return s.normalized()
}

// WithFilter returns the state with one tri-state filter applied. An empty
// value clears the filter.
func (s adminListState) WithFilter(name, value string) adminListState {
	switch name {
	case adminParamAdmin:
		s.Query.Admin = value
	case adminParamGuest:
		s.Query.Guest = value
	case adminParamConfirmed:
		s.Query.Confirmed = value
	}
	s.Query.Page = 1
	return s.normalized()
}

// WithSort returns the state sorted by key. Selecting the active key flips the
// direction; selecting another key uses its natural default direction.
func (s adminListState) WithSort(key string) adminListState {
	current := s.Query.Normalize()
	next := service.AdminUserQuery{Sort: key, Direction: ""}
	normalizedNext := next.Normalize()
	if current.Sort == normalizedNext.Sort {
		switch current.Direction {
		case service.AdminDirectionAsc:
			s.Query.Direction = service.AdminDirectionDesc
		default:
			s.Query.Direction = service.AdminDirectionAsc
		}
	} else {
		s.Query.Direction = ""
	}
	s.Query.Sort = key
	s.Query.Page = 1
	return s.normalized()
}

func (s adminListState) normalized() adminListState {
	return adminListState{Query: s.Query.Normalize()}
}

// defaultAdminDirection mirrors the service default so the URL omits a
// direction that carries no extra information.
func defaultAdminDirection(sort string) string {
	if sort == service.AdminSortCreatedAt || sort == service.AdminSortID {
		return service.AdminDirectionDesc
	}
	return service.AdminDirectionAsc
}

// Audit list URL parameter names.
const (
	auditParamAction = "action"
	auditParamActor  = "actor"
	auditParamTarget = "target"
)

// auditListState holds the normalized audit-list query and converts it to and
// from URL parameters.
type auditListState struct {
	Query service.AuditQuery
}

// parseAuditListState reads bounded audit-list state from the request query.
func parseAuditListState(request *http.Request) auditListState {
	if request == nil {
		return auditListState{Query: service.AuditQuery{}.Normalize()}
	}
	values := request.URL.Query()
	page, _ := strconv.Atoi(values.Get(adminParamPage))
	size, _ := strconv.Atoi(values.Get(adminParamSize))
	query := service.AuditQuery{
		Action: values.Get(auditParamAction),
		Actor:  values.Get(auditParamActor),
		Target: values.Get(auditParamTarget),
		Page:   page,
		Size:   size,
	}
	return auditListState{Query: query.Normalize()}
}

// Values renders the normalized state as URL parameters.
func (s auditListState) Values() url.Values {
	query := s.Query.Normalize()
	values := url.Values{}
	if query.Action != "" {
		values.Set(auditParamAction, query.Action)
	}
	if query.Actor != "" {
		values.Set(auditParamActor, query.Actor)
	}
	if query.Target != "" {
		values.Set(auditParamTarget, query.Target)
	}
	values.Set(adminParamPage, strconv.Itoa(query.Page))
	values.Set(adminParamSize, strconv.Itoa(query.Size))
	return values
}

// URL renders the state relative to the audit list base path.
func (s auditListState) URL(base string) string {
	encoded := s.Values().Encode()
	if encoded == "" {
		return base
	}
	return base + "?" + encoded
}

// WithPage returns the state for another one-based page.
func (s auditListState) WithPage(page int) auditListState {
	s.Query.Page = page
	return auditListState{Query: s.Query.Normalize()}
}
