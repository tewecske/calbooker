package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/tewecske/calbooker/internal/service"
)

// groupSectionKinds is the page where admins and managers manage the group's
// event kinds and resource kinds (#43). It sits under the settings tab.
const groupSectionKinds = "kinds"

// KindUseCases aggregates the kind operations consumed by HTTP handlers.
type KindUseCases interface {
	All(ctx context.Context, access *service.GroupAccess, scope service.KindScope) ([]service.Kind, error)
	Active(ctx context.Context, access *service.GroupAccess, scope service.KindScope) ([]service.Kind, error)
	Find(ctx context.Context, access *service.GroupAccess, scope service.KindScope, kindID int64) (service.Kind, error)
	Create(ctx context.Context, actor service.GroupActor, scope service.KindScope, input service.KindInput) (service.Kind, error)
	Rename(ctx context.Context, actor service.GroupActor, scope service.KindScope, kindID, expectedVersion int64, input service.KindInput) (service.Kind, error)
	SetArchived(ctx context.Context, actor service.GroupActor, scope service.KindScope, kindID, expectedVersion int64, archived bool) (service.Kind, error)
	Move(ctx context.Context, actor service.GroupActor, scope service.KindScope, kindID int64, offset int) error
	Delete(ctx context.Context, actor service.GroupActor, scope service.KindScope, kindID, expectedVersion int64) error
}

// KindsView holds both kind lists of a group.
type KindsView struct {
	SettingsURL string
	Lists       []KindListView
}

// KindListView is one kind list: the event kinds or the resource kinds.
type KindListView struct {
	// Segment is the URL segment of the list: events or resources.
	Segment   string
	Heading   string
	Intro     string
	CreateURL string
	Rows      []KindRowView
}

// KindRowView is one kind with its action URLs.
type KindRowView struct {
	ID          int64
	Version     int64
	NameEN      string
	NameHU      string
	DisplayName string
	// Code is the system code, empty for kinds an administrator added.
	Code      string
	Archived  bool
	Protected bool
	Uses      int
	CanDelete bool
	First     bool
	Last      bool
	RenameURL string
	// ArchiveURL, MoveURL, and DeleteURL post to the kind.
	ArchiveURL string
	MoveURL    string
	DeleteURL  string
}

// kindSegments maps the URL segment of a list to its scope.
var kindSegments = map[string]service.KindScope{
	"events":    service.KindScopeEvent,
	"resources": service.KindScopeResource,
}

func kindSegment(scope service.KindScope) string {
	if scope == service.KindScopeResource {
		return "resources"
	}
	return "events"
}

// registerKindRoutes registers the kind management page and its actions.
func registerKindRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{group}/kinds", groups.kinds)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/kinds/{list}", groups.createKind)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/kinds/{list}/{kindID}", groups.renameKind)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/kinds/{list}/{kindID}/archive", groups.archiveKind)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/kinds/{list}/{kindID}/move", groups.moveKind)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/kinds/{list}/{kindID}/delete", groups.deleteKind)
	}
}

func (h *groupHandler) kinds(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.kindRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionKinds, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createKind(writer http.ResponseWriter, request *http.Request) {
	current, scope, ok := h.kindListRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input := kindInput(request)
	if _, err := h.dependencies.Kinds.Create(request.Context(), current.actor(), scope, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionKinds, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "kinds.created")}}, http.StatusOK)
}

func (h *groupHandler) renameKind(writer http.ResponseWriter, request *http.Request) {
	current, scope, kindID, ok := h.kindPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if _, err := h.dependencies.Kinds.Rename(request.Context(), current.actor(), scope, kindID, version, kindInput(request)); err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionKinds, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "kinds.saved")}}, http.StatusOK)
}

func (h *groupHandler) archiveKind(writer http.ResponseWriter, request *http.Request) {
	current, scope, kindID, ok := h.kindPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	archived := request.PostFormValue("archived") == "true"
	if _, err := h.dependencies.Kinds.SetArchived(request.Context(), current.actor(), scope, kindID, version, archived); err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, err, FormData{})
		return
	}
	message := "kinds.restored"
	if archived {
		message = "kinds.archived_done"
	}
	h.renderSection(writer, request, current, groupSectionKinds, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

func (h *groupHandler) moveKind(writer http.ResponseWriter, request *http.Request) {
	current, scope, kindID, ok := h.kindPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	offset := 0
	switch request.PostFormValue("direction") {
	case "up":
		offset = -1
	case "down":
		offset = 1
	}
	if err := h.dependencies.Kinds.Move(request.Context(), current.actor(), scope, kindID, offset); err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionKinds, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "kinds.moved")}}, http.StatusOK)
}

func (h *groupHandler) deleteKind(writer http.ResponseWriter, request *http.Request) {
	current, scope, kindID, ok := h.kindPathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	if err := h.dependencies.Kinds.Delete(request.Context(), current.actor(), scope, kindID, version); err != nil {
		h.renderWriteError(writer, request, current, groupSectionKinds, 0, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionKinds, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "kinds.deleted")}}, http.StatusOK)
}

// kindRequest authorizes the kind page: outsiders get 404 from groupRequest,
// members who may not manage kinds get access denied.
func (h *groupHandler) kindRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Kinds == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanManageKinds() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) kindListRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, service.KindScope, bool) {
	current, ok := h.kindRequest(writer, request)
	if !ok {
		return groupRequest{}, "", false
	}
	scope, known := kindSegments[request.PathValue("list")]
	if !known {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, "", false
	}
	return current, scope, true
}

func (h *groupHandler) kindPathRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, service.KindScope, int64, bool) {
	current, scope, ok := h.kindListRequest(writer, request)
	if !ok {
		return groupRequest{}, "", 0, false
	}
	kindID, err := pathPositiveID(request, "kindID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, "", 0, false
	}
	return current, scope, kindID, true
}

func kindInput(request *http.Request) service.KindInput {
	return service.KindInput{NameEN: request.PostFormValue("name_en"), NameHU: request.PostFormValue("name_hu")}
}

func (h *groupHandler) loadKinds(writer http.ResponseWriter, request *http.Request, current groupRequest, view *GroupDetailView) bool {
	if h.dependencies.Kinds == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	lists := make([]KindListView, 0, 2)
	for _, scope := range []service.KindScope{service.KindScopeEvent, service.KindScopeResource} {
		kinds, err := h.dependencies.Kinds.All(request.Context(), current.access, scope)
		if err != nil {
			h.renderLoadError(writer, request, current.language, err)
			return false
		}
		segment := kindSegment(scope)
		base := h.groupPath(current.language, current.slug, "/kinds/"+segment)
		list := KindListView{
			Segment:   segment,
			Heading:   h.t(current.language, "kinds."+segment+".heading"),
			Intro:     h.t(current.language, "kinds."+segment+".intro"),
			CreateURL: base,
		}
		for i, kind := range kinds {
			rowURL := base + "/" + strconv.FormatInt(kind.ID, 10)
			protected := kind.Protected(scope)
			list.Rows = append(list.Rows, KindRowView{
				ID: kind.ID, Version: kind.Version, NameEN: kind.NameEN, NameHU: kind.NameHU,
				DisplayName: kind.Name(string(current.language)), Code: kind.SystemCode,
				Archived: kind.Archived(), Protected: protected, Uses: kind.Uses,
				CanDelete: !protected && kind.Uses == 0,
				First:     i == 0, Last: i == len(kinds)-1,
				RenameURL: rowURL, ArchiveURL: rowURL + "/archive", MoveURL: rowURL + "/move", DeleteURL: rowURL + "/delete",
			})
		}
		lists = append(lists, list)
	}
	view.Kinds = &KindsView{SettingsURL: h.groupPath(current.language, current.slug, "/settings"), Lists: lists}
	return true
}

// kindChoices lists the kinds a form offers: the active ones plus the kind
// the edited item already has (0 for a new item). Without kind use cases the
// list is empty.
func (h *groupHandler) kindChoices(writer http.ResponseWriter, request *http.Request, current groupRequest, scope service.KindScope, own int64) ([]service.Kind, bool) {
	if h.dependencies.Kinds == nil {
		return nil, true
	}
	kinds, err := h.dependencies.Kinds.Active(request.Context(), current.access, scope)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return nil, false
	}
	if own > 0 {
		found := false
		for _, kind := range kinds {
			found = found || kind.ID == own
		}
		if !found {
			if kind, err := h.dependencies.Kinds.Find(request.Context(), current.access, scope, own); err == nil {
				kinds = append(kinds, kind)
			}
		}
	}
	return kinds, true
}

// calendarKinds lists the group's event kinds for the calendar filter.
func (h *groupHandler) calendarKinds(writer http.ResponseWriter, request *http.Request, current groupRequest) ([]service.Kind, bool) {
	if h.dependencies.Kinds == nil {
		return nil, true
	}
	kinds, err := h.dependencies.Kinds.All(request.Context(), current.access, service.KindScopeEvent)
	if err != nil {
		h.renderLoadError(writer, request, current.language, err)
		return nil, false
	}
	return kinds, true
}

// kindErrorMessage maps a kind failure to its catalog message; it returns an
// empty string for other errors.
func kindErrorMessage(err error) string {
	switch {
	case errors.Is(err, service.ErrInvalidKindName):
		return "kinds.error.invalid_name"
	case errors.Is(err, service.ErrKindNameTaken):
		return "kinds.error.name_taken"
	case errors.Is(err, service.ErrKindInUse):
		return "kinds.error.in_use"
	case errors.Is(err, service.ErrKindProtected):
		return "kinds.error.protected"
	case errors.Is(err, service.ErrTooManyKinds):
		return "kinds.error.too_many"
	}
	return ""
}
