package httpadapter

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
	"github.com/tewecske/calbooker/templates"
)

var (
	// ErrInvalidPage identifies page data that cannot be rendered safely.
	ErrInvalidPage = errors.New("http: invalid page")
	// ErrPageNotFound identifies a template name that is not embedded.
	ErrPageNotFound = errors.New("http: page template not found")
	// ErrNilRequest identifies a missing request for response-mode selection.
	ErrNilRequest = errors.New("http: nil request")
	// ErrInvalidPageState identifies an unsupported localized state page.
	ErrInvalidPageState = errors.New("http: invalid page state")
)

// PageState identifies a localized non-success page.
type PageState string

const (
	PageStateNotFound       PageState = "not-found"
	PageStateAccessDenied   PageState = "access-denied"
	PageStateValidation     PageState = "validation"
	PageStateConflict       PageState = "conflict"
	PageStateSessionExpired PageState = "session-expired"
)

// PageData contains escaped, server-owned data for a full HTML document.
type PageData struct {
	Language         string
	Title            string
	Heading          string
	Kind             string
	Message          string
	Description      string
	CSRFToken        string
	Form             FormData
	FormAction       string
	FormMethod       string
	SubmitLabel      string
	Template         string
	FragmentTemplate string
	Navigation       []NavigationItem
	Languages        []LanguageOption
	Account          *AccountMenu
	SignInURL        string
	// SSOSignInURL starts the issuer's sign-in flow (or the guest upgrade).
	SSOSignInURL      string
	GuestBanner       bool
	GuestOwned        bool
	GuestTransferCode string
	Labels            map[string]string
	Alerts            []Alert
	Settings          *SettingsView
	Groups            *GroupsView
	Group             *GroupDetailView
	Admin             *AdminView
}

// AdminView contains administrator page data. URLs are empty when a section is
// not yet available, so navigation only advertises implemented routes.
type AdminView struct {
	IsAdmin    bool
	Page       string
	Sections   []AdminSectionView
	List       *AdminListView
	Detail     *AdminDetailView
	Audit      *AdminAuditView
	System     *AdminSystemView
	Limits     *AdminRateLimitView
	Usage      *AdminUsageView
	Suspicious *AdminSuspiciousView
}

// AdminSuspiciousView is the investigation-signal report page state.
type AdminSuspiciousView struct {
	BaseURL         string
	WindowKey       string
	Windows         []AdminUsageWindowView
	ActionThreshold int
	OriginThreshold int
	Accounts        []AdminSuspiciousAccountView
}

// AdminSuspiciousAccountView is one flagged account's safe aggregate.
type AdminSuspiciousAccountView struct {
	UserID          int64
	DetailURL       string
	Requests        int
	DistinctOrigins int
	ActionFlag      bool
	OriginFlag      bool
}

// AdminUsageView is the usage report page state with URL-persisted window and
// ordering.
type AdminUsageView struct {
	BaseURL   string
	WindowKey string
	Ascending bool
	Windows   []AdminUsageWindowView
	MostURL   string
	LeastURL  string
	Routes    []AdminUsageRouteView
	Queue     AdminUsageQueueView
}

// AdminUsageWindowView is one selectable report window.
type AdminUsageWindowView struct {
	Key     string
	Label   string
	URL     string
	Current bool
}

// AdminUsageRouteView is one route's request count.
type AdminUsageRouteView struct {
	Route    string
	Requests int
}

// AdminUsageQueueView is the live usage queue health.
type AdminUsageQueueView struct {
	Capacity int
	Pending  int
	Recorded uint64
	Failed   uint64
	Dropped  uint64
}

// AdminSystemView contains administrator system health data. It never carries
// credentials; each ticket adds one section as it lands.
type AdminSystemView struct {
	RunURL        string
	Jobs          []AdminMaintenanceJobView
	RunConfirm    string
	Configuration *AdminSystemConfigView
	Runtime       *AdminSystemRuntimeView
	Migrations    []AdminMigrationView
	Counts        *AdminDatastoreCountsView
}

// AdminRateLimitView is the live rate-limit page state.
type AdminRateLimitView struct {
	ClearURL     string
	ClearAllURL  string
	ClearConfirm string
	Actions      []AdminRateLimitActionView
}

// AdminRateLimitActionView is one live rate-limit action.
type AdminRateLimitActionView struct {
	Action   string
	Limit    int
	Window   string
	Buckets  int
	Locked   int
	ClearURL string
	Entries  []AdminRateLimitBucketView
}

// AdminRateLimitBucketView is one redacted live budget.
type AdminRateLimitBucketView struct {
	KeyHint string
	Count   int
	Limit   int
	Retry   string
}

// AdminDatastoreCountsView is the safe data-store health summary.
type AdminDatastoreCountsView struct {
	TotalAccounts         int
	GuestAccounts         int
	AdministratorAccounts int
	UnconfirmedAccounts   int
	ActiveSessions        int
	ExpiredSessions       int
	ExpiredOAuthStates    int
}

// AdminSystemRuntimeView contains live, credential-free process facts.
type AdminSystemRuntimeView struct {
	Version    string
	StartedAt  string
	Uptime     string
	GoVersion  string
	Platform   string
	CPUs       int
	GOMAXPROCS int
	Goroutines int
	HeapAlloc  string
	SysMemory  string
	GC         uint32
}

// AdminMigrationView is one migration's installation state.
type AdminMigrationView struct {
	Rank        int
	Version     int64
	Description string
	Installed   bool
	InstalledAt string
}

// AdminSystemConfigView contains credential-free deployment configuration.
type AdminSystemConfigView struct {
	Environment          string
	PublicAddress        string
	PublicURL            string
	SecureSessionCookies bool
	MailConfigured       bool
	MailRelay            string
	SessionLifetime      string
	GuestRetention       string
	UsageRetention       string
	MaintenanceInterval  string
	TrustedProxy         string
	SSOIssuer            string
}

// AdminMaintenanceJobView is one background job's retained last-run state.
type AdminMaintenanceJobView struct {
	Job          string
	Label        string
	Runs         int
	Failures     int
	LastRun      string
	LastDuration string
	LastDeleted  int
	LastError    string
	Failed       bool
}

// AdminAuditView contains one page of administrator action history.
type AdminAuditView struct {
	BaseURL      string
	ActionFilter string
	ActorFilter  string
	TargetFilter string
	Page         int
	Size         int
	Total        int
	Pages        int
	HasPrevious  bool
	HasNext      bool
	PreviousURL  string
	NextURL      string
	Entries      []AdminAuditEntryView
}

// AdminAuditEntryView is one audit row's safe fields.
type AdminAuditEntryView struct {
	Time   string
	Actor  string
	Action string
	Target string
	Detail string
	Origin string
}

// AdminDetailView contains safe account diagnostics for one account.
type AdminDetailView struct {
	UserID            int64
	Label             string
	Email             string
	Username          string
	DisplayName       string
	IsAdmin           bool
	IsGuest           bool
	Confirmed         bool
	Locale            string
	Theme             string
	CreatedAt         string
	Version           int64
	EditURL           string
	ListURL           string
	SessionsRevokeURL string
	SessionCount      int
	Sessions          []AdminSessionView
	Identities        []IdentityView
}

// AdminSessionView is one active session's safe timing.
type AdminSessionView struct {
	CreatedAt string
	ExpiresAt string
}

// AdminSectionView is one administrator navigation entry.
type AdminSectionView struct {
	ID      string
	Label   string
	URL     string
	Current bool
}

// AdminListView contains one page of accounts and every URL needed to change
// the list state without client-side JavaScript.
type AdminListView struct {
	BaseURL         string
	SearchURL       string
	SearchValue     string
	AdminFilter     string
	GuestFilter     string
	ConfirmedFilter string
	Sort            string
	Direction       string
	Page            int
	Size            int
	Total           int
	Pages           int
	HasPrevious     bool
	HasNext         bool
	PreviousURL     string
	NextURL         string
	Rows            []AdminUserRowView
	SortURLs        map[string]string
	FilterURLs      map[string]string
	Mode            string
	Form            *AdminAccountFormView
}

// AdminAccountFormView contains the create or edit account form state. Values
// and field errors come from the page-level FormData so submitted values are
// preserved after a validation failure.
type AdminAccountFormView struct {
	Heading     string
	ActionURL   string
	CancelURL   string
	Email       string
	IsAdmin     bool
	Version     int64
	SubmitLabel string
	DeleteURL   string
	IsSelf      bool
}

// AdminUserRowView is one account row in the administrator list.
type AdminUserRowView struct {
	ID        int64
	Label     string
	Email     string
	IsAdmin   bool
	IsGuest   bool
	Confirmed bool
	CreatedAt string
	DetailURL string
	EditURL   string
}

// GroupsView contains the group list and join/create affordances.
type GroupsView struct {
	ListURL     string
	CreateURL   string
	JoinURL     string
	Creating    bool
	Summaries   []GroupSummaryView
	CreateLabel string
	JoinLabel   string
	MineLabel   string
}

// GroupSummaryView is one group list row with a localized role label. A
// non-member label keeps superuser access from reading as a group role.
type GroupSummaryView struct {
	Group       service.Group
	MemberCount int
	RoleLabel   string
}

// GroupDetailView contains one group's detail, roster, and management URLs.
type GroupDetailView struct {
	GroupID     int64
	Name        string
	Version     int64
	MemberCount int
	IsAdmin     bool
	IsMember    bool
	InviteCode  string
	ListURL     string
	RenameURL   string
	InviteURL   string
	LeaveURL    string
	MembersURL  string
	Members     []GroupMemberView
}

// GroupMemberView contains one roster row and its available actions.
type GroupMemberView struct {
	UserID     int64
	Role       string
	Version    int64
	Label      string
	IsAdmin    bool
	IsSelf     bool
	RoleAction string
	RoleLabel  string
	PromoteURL string
	RemoveURL  string
}

// SettingsView contains server-owned account settings data for rendering.
type SettingsView struct {
	// AccountURL links to the provider's account page, which owns email,
	// name, and credentials.
	AccountURL   string
	Email        string
	Locale       string
	Theme        string
	Languages    []LanguageOption
	LocaleAction string
	ThemeURL     string
}

// LanguageOption is one selectable site language. URL is empty where a
// language option is not tied to a page (for example the account settings
// form, which persists the choice instead of navigating).
type LanguageOption struct {
	Code    string
	Label   string
	URL     string
	Current bool
}

// IdentityView is one linked external identity for display. The permanent
// provider subject is never part of this value.
type IdentityView struct {
	ID            int64
	Provider      string
	ProviderLabel string
	Label         string
}

// NavigationItem describes one internal navigation link.
type NavigationItem struct {
	Label string
	URL   string
}

// AccountMenu contains links shown to an authenticated account.
type AccountMenu struct {
	Label       string
	SettingsURL string
	SignOutURL  string
	Theme       string
	ThemeURL    string
	IsAdmin     bool
	AdminURL    string
}

// Alert is a user-facing status message. Level accepts info, success, warning,
// or error; unknown levels render as info.
type Alert struct {
	Level   string
	Message string
}

// FormData contains submitted values and server-side field errors.
type FormData struct {
	Submitted bool
	Values    map[string]string
	Errors    []FieldError
}

// FieldError associates one safe validation message with a form field.
type FieldError struct {
	Field   string
	Message string
}

// Value returns a submitted value after the form has been submitted.
func (f FormData) Value(field string) string {
	if !f.Submitted || f.Values == nil {
		return ""
	}
	return f.Values[field]
}

// Error returns a field error after the form has been submitted.
func (f FormData) Error(field string) string {
	if !f.Submitted {
		return ""
	}
	for _, fieldError := range f.Errors {
		if fieldError.Field == field {
			return fieldError.Message
		}
	}
	return ""
}

// HasErrors reports whether submitted form data contains validation errors.
func (f FormData) HasErrors() bool {
	return f.Submitted && len(f.Errors) > 0
}

// PageRenderer executes embedded HTML templates after buffering the output.
type PageRenderer struct {
	templates *template.Template
	catalogs  *locale.Catalogs
}

// NewPageRenderer parses the embedded application templates.
func NewPageRenderer() (*PageRenderer, error) {
	parsed, err := template.ParseFS(templates.FS, "*.html")
	if err != nil {
		return nil, fmt.Errorf("parse page templates: %w", err)
	}
	catalogs, err := locale.LoadEmbeddedCatalogs()
	if err != nil {
		return nil, fmt.Errorf("load page catalogs: %w", err)
	}
	if err := catalogs.ValidateCompleteness(); err != nil {
		return nil, fmt.Errorf("validate page catalogs: %w", err)
	}
	return &PageRenderer{templates: parsed, catalogs: catalogs}, nil
}

// Translate resolves one validated catalog message for a page handler.
func (r *PageRenderer) Translate(language locale.Code, id string) (string, error) {
	if r == nil || r.catalogs == nil {
		return "", fmt.Errorf("%w: renderer is nil", ErrInvalidPage)
	}
	return r.catalogs.Translate(language, id, nil)
}

// RenderState renders a localized error or recovery state in full-page or
// fragment mode according to request headers.
func (r *PageRenderer) RenderState(writer http.ResponseWriter, request *http.Request, language locale.Code, state PageState) error {
	if r == nil || r.catalogs == nil {
		return fmt.Errorf("%w: renderer is nil", ErrInvalidPageState)
	}
	if !locale.Supported(language) {
		return fmt.Errorf("%w: unsupported language %q", ErrInvalidPageState, language)
	}
	ids, ok := pageStateMessages[state]
	if !ok {
		return fmt.Errorf("%w: %q", ErrInvalidPageState, state)
	}
	title, err := r.catalogs.Translate(language, ids.title, nil)
	if err != nil {
		return fmt.Errorf("translate page title: %w", err)
	}
	heading, err := r.catalogs.Translate(language, ids.heading, nil)
	if err != nil {
		return fmt.Errorf("translate page heading: %w", err)
	}
	message, err := r.catalogs.Translate(language, ids.message, nil)
	if err != nil {
		return fmt.Errorf("translate page message: %w", err)
	}
	return r.RenderRequestStatus(writer, request, PageData{
		Language:         string(language),
		Title:            title,
		Heading:          heading,
		Kind:             "state",
		Message:          message,
		Template:         "state",
		FragmentTemplate: "state-fragment",
	}, stateStatus(state))
}

func stateStatus(state PageState) int {
	switch state {
	case PageStateNotFound:
		return http.StatusNotFound
	case PageStateAccessDenied:
		return http.StatusForbidden
	case PageStateConflict:
		return http.StatusConflict
	case PageStateSessionExpired:
		return http.StatusUnauthorized
	default:
		return http.StatusUnprocessableEntity
	}
}

// Render writes one complete HTML document. It buffers template execution so a
// template failure cannot leave a partially rendered response.
func (r *PageRenderer) Render(writer http.ResponseWriter, name string, page PageData) error {
	if r == nil || r.templates == nil {
		return fmt.Errorf("%w: renderer is nil", ErrInvalidPage)
	}
	if page.Language == "" || page.Title == "" || page.Heading == "" {
		return fmt.Errorf("%w: language, title, and heading are required", ErrInvalidPage)
	}
	if r.templates.Lookup(name) == nil {
		return fmt.Errorf("%w: %s", ErrPageNotFound, name)
	}

	var output bytes.Buffer
	if err := r.templates.ExecuteTemplate(&output, name, page); err != nil {
		return fmt.Errorf("execute page template %q: %w", name, err)
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, err := writer.Write(output.Bytes())
	return err
}

// RenderRequest serves a complete document for normal requests and an
// accessible fragment for HTMX requests.
func (r *PageRenderer) RenderRequest(writer http.ResponseWriter, request *http.Request, page PageData) error {
	return r.RenderRequestStatus(writer, request, page, http.StatusOK)
}

// RenderRequestStatus renders a full page or fragment with an explicit HTTP
// status after buffering template execution.
func (r *PageRenderer) RenderRequestStatus(writer http.ResponseWriter, request *http.Request, page PageData, status int) error {
	if request == nil {
		return ErrNilRequest
	}
	if page.Language == "" || page.Title == "" || page.Heading == "" {
		return fmt.Errorf("%w: language, title, and heading are required", ErrInvalidPage)
	}
	if page.Languages == nil {
		page.Languages = navbarLanguageOptions(locale.Code(page.Language), request)
	}
	name := page.Template
	if IsHTMX(request) {
		name = page.FragmentTemplate
	}
	if name == "" {
		return fmt.Errorf("%w: response template is required", ErrInvalidPage)
	}
	writer.Header().Add("Vary", "HX-Request")
	return r.renderStatus(writer, name, page, status)
}

// IsHTMX reports whether request asks for an HTMX response fragment.
func IsHTMX(request *http.Request) bool {
	if request == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(request.Header.Get("HX-Request")), "true")
}

// formFailureStatus returns the status for a form fragment that reports a
// failure to the user. The layout's htmx responseHandling swaps only 422
// among client errors, so HTMX requests get 422 and the alert is shown; full
// page posts keep the more specific status.
func formFailureStatus(request *http.Request, status int) int {
	if IsHTMX(request) {
		return http.StatusUnprocessableEntity
	}
	return status
}

func (r *PageRenderer) render(writer http.ResponseWriter, name string, page PageData) error {
	return r.renderStatus(writer, name, page, http.StatusOK)
}

func (r *PageRenderer) renderStatus(writer http.ResponseWriter, name string, page PageData, status int) error {
	if r == nil || r.templates == nil {
		return fmt.Errorf("%w: renderer is nil", ErrInvalidPage)
	}
	if r.templates.Lookup(name) == nil {
		return fmt.Errorf("%w: %s", ErrPageNotFound, name)
	}

	var output bytes.Buffer
	if err := r.templates.ExecuteTemplate(&output, name, page); err != nil {
		return fmt.Errorf("execute page template %q: %w", name, err)
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	_, err := writer.Write(output.Bytes())
	return err
}

type pageStateMessageIDs struct {
	title   string
	heading string
	message string
}

var pageStateMessages = map[PageState]pageStateMessageIDs{
	PageStateNotFound:       {title: "errors.not_found.title", heading: "errors.not_found.heading", message: "errors.not_found.message"},
	PageStateAccessDenied:   {title: "errors.access_denied.title", heading: "errors.access_denied.heading", message: "errors.access_denied.message"},
	PageStateValidation:     {title: "errors.validation.title", heading: "errors.validation.heading", message: "errors.validation.message"},
	PageStateConflict:       {title: "errors.conflict.title", heading: "errors.conflict.heading", message: "errors.conflict.message"},
	PageStateSessionExpired: {title: "errors.session_expired.title", heading: "errors.session_expired.heading", message: "errors.session_expired.message"},
}
