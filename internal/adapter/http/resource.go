package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// Resource page sections.
const (
	groupSectionResources = "resources"
	groupSectionResource  = "resource"
)

// ResourceUseCases aggregates the resource operations consumed by HTTP
// handlers.
type ResourceUseCases interface {
	List(ctx context.Context, access *service.GroupAccess) ([]service.Resource, error)
	Find(ctx context.Context, access *service.GroupAccess, resourceID int64) (service.Resource, error)
	Create(ctx context.Context, actor service.GroupActor, input service.ResourceInput) (service.Resource, error)
	Update(ctx context.Context, actor service.GroupActor, resourceID, expectedVersion int64, input service.ResourceInput) (service.Resource, error)
	SetArchived(ctx context.Context, actor service.GroupActor, resourceID, expectedVersion int64, archived bool) (service.Resource, error)
}

// ResourcesView is a group's resource list. CreateForm is nil unless the
// account may manage resources.
type ResourcesView struct {
	CreateForm *ResourceFormView
	Active     ResourceTableView
	Archived   ResourceTableView
}

// ResourceTableView is one resource table: active or archived rows.
type ResourceTableView struct {
	L      map[string]string
	TestID string
	Rows   []ResourceRowView
}

// ResourceRowView is one resource list row.
type ResourceRowView struct {
	ID          int64
	Name        string
	Kind        string
	KindLabel   string
	Description string
	Capacity    string
	URL         string
}

// ResourceDetailView is one resource with the forms the account may use.
type ResourceDetailView struct {
	ResourceRowView
	Archived   bool
	Version    int64
	CanEdit    bool
	ListURL    string
	ArchiveURL string
	EditForm   *ResourceFormView
}

// ResourceFormView holds the values and errors of the resource create or
// edit form.
type ResourceFormView struct {
	L                map[string]string
	Prefix           string
	ActionURL        string
	Name             string
	Description      string
	MaxTeams         string
	Kinds            []SelectOptionView
	NameError        string
	KindError        string
	DescriptionError string
	MaxTeamsError    string
}

// registerResourceRoutes registers each group's resource list and pages.
func registerResourceRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/groups/{id}/resources", groups.resources)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/resources", groups.createResource)
		mux.HandleFunc("GET "+prefix+"/groups/{id}/resources/{resourceID}", groups.resource)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/resources/{resourceID}", groups.updateResource)
		mux.HandleFunc("POST "+prefix+"/groups/{id}/resources/{resourceID}/archive", groups.archiveResource)
	}
}

func (h *groupHandler) resources(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.resourceRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionResources, 0, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) createResource(writer http.ResponseWriter, request *http.Request) {
	current, ok := h.resourceRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := resourceForm(request)
	if _, err := h.dependencies.Resources.Create(request.Context(), current.actor(), input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionResources, 0, err, h.resourceFormErrors(current.language, form, err))
		return
	}
	h.renderSection(writer, request, current, groupSectionResources, 0, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "resources.created")}}, http.StatusOK)
}

func (h *groupHandler) resource(writer http.ResponseWriter, request *http.Request) {
	current, resourceID, ok := h.resourcePathRequest(writer, request)
	if !ok {
		return
	}
	h.renderSection(writer, request, current, groupSectionResource, resourceID, FormData{}, nil, http.StatusOK)
}

func (h *groupHandler) updateResource(writer http.ResponseWriter, request *http.Request) {
	current, resourceID, ok := h.resourcePathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	input, form := resourceForm(request)
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionResource, resourceID, service.ErrOptimisticLockConflict, form)
		return
	}
	if _, err := h.dependencies.Resources.Update(request.Context(), current.actor(), resourceID, version, input); err != nil {
		h.renderWriteError(writer, request, current, groupSectionResource, resourceID, err, h.resourceFormErrors(current.language, form, err))
		return
	}
	h.renderSection(writer, request, current, groupSectionResource, resourceID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "resources.saved")}}, http.StatusOK)
}

func (h *groupHandler) archiveResource(writer http.ResponseWriter, request *http.Request) {
	current, resourceID, ok := h.resourcePathRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	version, err := parseVersion(request.PostFormValue("version"))
	if err != nil {
		h.renderWriteError(writer, request, current, groupSectionResource, resourceID, service.ErrOptimisticLockConflict, FormData{})
		return
	}
	archived := request.PostFormValue("archived") == "true"
	if _, err := h.dependencies.Resources.SetArchived(request.Context(), current.actor(), resourceID, version, archived); err != nil {
		h.renderWriteError(writer, request, current, groupSectionResource, resourceID, err, FormData{})
		return
	}
	message := "resources.restored"
	if archived {
		message = "resources.archived_done"
	}
	h.renderSection(writer, request, current, groupSectionResource, resourceID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, message)}}, http.StatusOK)
}

// resourceRequest authorizes a resource page: outsiders get 404 from
// groupRequest, members who may not see resources get access denied.
func (h *groupHandler) resourceRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, bool) {
	current, ok := h.groupRequest(writer, request)
	if !ok {
		return groupRequest{}, false
	}
	if h.dependencies.Resources == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, false
	}
	if !current.access.CanViewResources() {
		h.renderAccessDenied(writer, request, current.language)
		return groupRequest{}, false
	}
	return current, true
}

func (h *groupHandler) resourcePathRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, ok := h.resourceRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	resourceID, err := pathPositiveID(request, "resourceID")
	if err != nil {
		h.renderGroupNotFound(writer, request, current.language)
		return groupRequest{}, 0, false
	}
	return current, resourceID, true
}

func (h *groupHandler) loadResources(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Resources == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	resources, err := h.dependencies.Resources.List(request.Context(), current.access)
	if err != nil {
		h.renderResourceLoadError(writer, request, current.language, err)
		return false
	}
	labels := h.labels[current.language]
	list := &ResourcesView{
		Active:   ResourceTableView{L: labels, TestID: "resources-active"},
		Archived: ResourceTableView{L: labels, TestID: "resources-archived"},
	}
	if current.access.CanManageResources() {
		list.CreateForm = h.resourceFormView(current.language, "resource-new", h.groupPath(current.language, groupID, "/resources"), nil, form)
	}
	for _, resource := range resources {
		row := h.resourceRow(current.language, groupID, resource)
		if resource.Archived() {
			list.Archived.Rows = append(list.Archived.Rows, row)
		} else {
			list.Active.Rows = append(list.Active.Rows, row)
		}
	}
	view.Resources = list
	return true
}

func (h *groupHandler) loadResource(writer http.ResponseWriter, request *http.Request, current groupRequest, groupID, resourceID int64, form FormData, view *GroupDetailView) bool {
	if h.dependencies.Resources == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return false
	}
	resource, err := h.dependencies.Resources.Find(request.Context(), current.access, resourceID)
	if err != nil {
		h.renderResourceLoadError(writer, request, current.language, err)
		return false
	}
	row := h.resourceRow(current.language, groupID, resource)
	detail := &ResourceDetailView{
		ResourceRowView: row,
		Archived:        resource.Archived(),
		Version:         resource.Version,
		CanEdit:         current.access.CanManageResources(),
		ListURL:         h.groupPath(current.language, groupID, "/resources"),
		ArchiveURL:      row.URL + "/archive",
	}
	if detail.CanEdit {
		detail.EditForm = h.resourceFormView(current.language, "resource-edit", row.URL, &resource, form)
	}
	view.Resource = detail
	return true
}

func (h *groupHandler) renderResourceLoadError(writer http.ResponseWriter, request *http.Request, language locale.Code, err error) {
	switch {
	case errors.Is(err, service.ErrResourceNotFound):
		h.renderGroupNotFound(writer, request, language)
	case errors.Is(err, service.ErrGroupForbidden):
		h.renderAccessDenied(writer, request, language)
	default:
		h.renderLoadError(writer, request, language, err)
	}
}

func (h *groupHandler) resourceRow(language locale.Code, groupID int64, resource service.Resource) ResourceRowView {
	row := ResourceRowView{
		ID:        resource.ID,
		Name:      resource.Name,
		Kind:      resource.Kind,
		KindLabel: h.t(language, "resources.kind."+resource.Kind),
		URL:       h.groupPath(language, groupID, "/resources/"+strconv.FormatInt(resource.ID, 10)),
	}
	if resource.Description != nil {
		row.Description = *resource.Description
	}
	if resource.MaxConcurrentTeams != nil {
		row.Capacity = strconv.Itoa(*resource.MaxConcurrentTeams)
	} else {
		row.Capacity = h.t(language, "resources.capacity.none")
	}
	return row
}

// resourceFormView fills the resource form from submitted values after a
// failed write, else from the stored resource (nil for a new resource).
func (h *groupHandler) resourceFormView(language locale.Code, prefix, actionURL string, resource *service.Resource, form FormData) *ResourceFormView {
	view := &ResourceFormView{L: h.labels[language], Prefix: prefix, ActionURL: actionURL}
	kind := service.ResourceKindField
	switch {
	case form.Submitted:
		view.Name = form.Value("name")
		kind = form.Value("kind")
		view.Description = form.Value("description")
		view.MaxTeams = form.Value("max_concurrent_teams")
		view.NameError = form.Error("name")
		view.KindError = form.Error("kind")
		view.DescriptionError = form.Error("description")
		view.MaxTeamsError = form.Error("max_concurrent_teams")
	case resource != nil:
		view.Name = resource.Name
		kind = resource.Kind
		if resource.Description != nil {
			view.Description = *resource.Description
		}
		if resource.MaxConcurrentTeams != nil {
			view.MaxTeams = strconv.Itoa(*resource.MaxConcurrentTeams)
		}
	}
	for _, option := range service.ResourceKinds() {
		view.Kinds = append(view.Kinds, SelectOptionView{Value: option, Label: h.t(language, "resources.kind."+option), Selected: option == kind})
	}
	return view
}

// resourceFormErrors attaches the field error for a resource validation
// failure.
func (h *groupHandler) resourceFormErrors(language locale.Code, form FormData, err error) FormData {
	field, message := resourceErrorField(err)
	if field != "" {
		form.Errors = []FieldError{{Field: field, Message: h.t(language, message)}}
	}
	return form
}

// resourceErrorField maps a resource validation error to its form field
// and message; it returns empty strings for other errors.
func resourceErrorField(err error) (string, string) {
	switch {
	case errors.Is(err, service.ErrInvalidResourceName):
		return "name", "resources.error.invalid_name"
	case errors.Is(err, service.ErrResourceNameTaken):
		return "name", "resources.error.name_taken"
	case errors.Is(err, service.ErrInvalidResourceKind):
		return "kind", "resources.error.invalid_kind"
	case errors.Is(err, service.ErrInvalidResourceDescription):
		return "description", "resources.error.invalid_description"
	case errors.Is(err, service.ErrInvalidResourceCapacity):
		return "max_concurrent_teams", "resources.error.invalid_capacity"
	}
	return "", ""
}

// resourceForm reads resource input. An empty limit is no limit; an
// unparseable one becomes -1, which validation refuses.
func resourceForm(request *http.Request) (service.ResourceInput, FormData) {
	values := map[string]string{
		"name":                 request.PostFormValue("name"),
		"kind":                 strings.TrimSpace(request.PostFormValue("kind")),
		"description":          request.PostFormValue("description"),
		"max_concurrent_teams": strings.TrimSpace(request.PostFormValue("max_concurrent_teams")),
	}
	limit := 0
	if raw := values["max_concurrent_teams"]; raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			parsed = -1
		}
		limit = parsed
	}
	input := service.ResourceInput{Name: values["name"], Kind: values["kind"], Description: values["description"], MaxConcurrentTeams: limit}
	return input, FormData{Submitted: true, Values: values}
}
