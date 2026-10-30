package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

var (
	ErrInvalidHTTPDependencies = errors.New("http: invalid service dependencies")
	ErrSessionAuthentication   = errors.New("http: session authentication failed")
)

// Dependencies contains consuming-side ports used by HTTP handlers. Concrete
// repositories and services are assembled by the application composition
// package and passed here by process startup.
type Dependencies struct {
	Users         UserFinder
	Sessions      SessionLookup
	SessionAdmin  SessionSignOut
	SessionCookie *middleware.SessionCookie

	GuestCreator  GuestCreator
	GuestClaimer  GuestClaimer
	GuestRedeemer GuestRedeemer

	LocaleSettings LocaleSettingsUpdater
	ThemeUpdater   AccountThemeUpdater

	Groups      GroupUseCases
	GroupAccess GroupAccessProvider
	Teams       TeamUseCases
	Invites     InviteUseCases
	Profiles    ProfileUseCases
	Resources   ResourceUseCases
	// Kinds are the group's event kinds and resource kinds (#43).
	Kinds   KindUseCases
	Seasons SeasonUseCases
	Events  EventUseCases
	Coaches CoachUseCases
	Plan    PlanUseCases
	// TrainingPlan is the training plan board (#14).
	TrainingPlan TrainingPlanUseCases
	// EventGeneration generates events from the training plan (#15).
	EventGeneration EventGenerationUseCases
	// Participation is sign-up and attendance on event pages (#9).
	Participation ParticipationUseCases
	CoachBoard    CoachBoardUseCases
	JoinGroup     JoinRateLimitedGroupJoiner
	// Statistics is participation statistics per period (#11).
	Statistics StatisticsUseCases

	AdminAccounts       AdminAccountLister
	AdminAccountEditor  AdminAccountEditor
	AdminUserDetailer   AdminUserDetailer
	AdminSessionRevoker AdminSessionRevoker
	AdminAudit          AdminAuditReader
	AdminMaintenance    AdminMaintenanceRunner
	AdminAuditRecorder  AdminActionRecorder
	AdminSystemConfig   service.SystemConfigurationProvider
	AdminRuntime        AdminRuntimeProvider
	AdminDatastoreStats AdminDatastoreStatsReader
	AdminRateLimits     AdminRateLimitManager

	Usage middleware.UsageRecorder

	AdminUsage      AdminUsageReporter
	AdminSuspicious AdminSuspiciousReporter

	Tracer         middleware.SpanTracer
	SecurityLogger middleware.SecurityRequestLogger

	// SSOClient signs visitors in through the issuer. Without it the sign-in
	// routes fail closed; this app has no local credential routes.
	SSOClient SSOClientUseCases
	// SSOAccountURL is the issuer-side account page linked from settings.
	SSOAccountURL string

	// TrustedProxy is the reverse proxy address or network whose
	// X-Forwarded-For header identifies the client for rate limits and audit.
	TrustedProxy string

	// DevelopmentRoutes registers development-only pages such as the week
	// grid demo. cmd/web sets it only for CALBOOKER_ENV=development.
	DevelopmentRoutes bool
}

// AdminAccountLister lists accounts for the administrator area.
type AdminAccountLister interface {
	List(context.Context, service.AdminUserQuery) (service.AdminUserPage, error)
}

// AdminAccountEditor reads, atomically updates, and deletes an account for an
// administrator.
type AdminAccountEditor interface {
	Find(context.Context, int64) (service.User, error)
	Update(context.Context, service.AdminActionContext, int64, service.AdminAccountUpdateInput) (service.User, error)
	Delete(context.Context, service.AdminActionContext, int64, int64) error
}

// AdminUserDetailer aggregates safe account diagnostics for an administrator.
type AdminUserDetailer interface {
	Detail(context.Context, int64) (service.AdminUserDetail, error)
}

// AdminSessionRevoker ends every active session for an account.
type AdminSessionRevoker interface {
	RevokeSessions(context.Context, service.AdminActionContext, int64) error
}

// AdminAuditReader lists administrator action history.
type AdminAuditReader interface {
	List(context.Context, service.AuditQuery) (service.AuditPage, error)
}

// AdminActionRecorder stores a completed administrator action in the audit
// trail without exposing credentials.
type AdminActionRecorder interface {
	Record(context.Context, service.AuditRecord) error
}

// AdminMaintenanceRunner inspects and runs scheduled cleanup jobs.
type AdminMaintenanceRunner interface {
	RunOnce(context.Context) ([]service.MaintenanceRun, error)
	RunJob(context.Context, string) (service.MaintenanceRun, error)
	Status() []service.MaintenanceStatus
}

// AdminRuntimeProvider reports live runtime and migration facts.
type AdminRuntimeProvider interface {
	RuntimeInfo() service.RuntimeInfo
	Migrations(context.Context) ([]service.MigrationInfo, error)
}

// AdminDatastoreStatsReader reports safe aggregate data-store counts.
type AdminDatastoreStatsReader interface {
	Counts(context.Context) (service.DatastoreCounts, error)
}

// AdminRateLimitManager lists and clears live rate-limit budgets.
type AdminRateLimitManager interface {
	Overview() []service.RateLimitActionInfo
	Buckets(string) ([]service.RateLimitBucketInfo, error)
	ClearAction(context.Context, service.AdminActionContext, string) (int, error)
	ClearAll(context.Context, service.AdminActionContext) (int, error)
}

// AdminUsageReporter builds route usage reports for administrators.
type AdminUsageReporter interface {
	Report(context.Context, string, bool) (service.UsageReport, error)
}

// AdminSuspiciousReporter builds suspicious-account reports.
type AdminSuspiciousReporter interface {
	Report(context.Context, string) (service.SuspiciousReport, error)
}

// SSOClientUseCases signs visitors in through the issuer and
// upgrades guests into issuer accounts.
type SSOClientUseCases interface {
	Start(ctx context.Context) (string, string, error)
	StartUpgrade(ctx context.Context, guestID int64) (string, string, error)
	// Callback receives the account signed in to the browser (or zero) so an
	// upgrade completes only for the guest that started it.
	Callback(ctx context.Context, actorUserID int64, code, state string) (service.SSOCallbackResult, error)
	BackChannelLogout(ctx context.Context, logoutToken string) error
	SignOutURL(ctx context.Context, sessionID string) (string, error)
}

// UserFinder resolves an account for an authenticated session.
type UserFinder interface {
	FindUserByID(context.Context, int64) (service.User, error)
}

// SessionLookup validates an opaque session credential.
type SessionLookup interface {
	Lookup(context.Context, string) (service.Session, error)
}

// SessionSignOut revokes one opaque session credential.
type SessionSignOut interface {
	SignOut(context.Context, string) error
}

type GuestCreator interface {
	Create(context.Context, string, string) (service.GuestSessionResult, error)
}

type GuestClaimer interface {
	Issue(context.Context, int64) (service.GuestClaimCode, error)
}

type GuestRedeemer interface {
	Redeem(context.Context, string, string) (service.GuestSessionResult, error)
}

// Validate checks dependencies needed by session-aware routes.
func (d Dependencies) Validate() error {
	if d.Users == nil || d.Sessions == nil || d.SessionAdmin == nil || d.SessionCookie == nil {
		return ErrInvalidHTTPDependencies
	}
	return nil
}

// SessionAuthenticator resolves an authenticated account from its opaque
// browser cookie. It never exposes the cookie value through Principal.
type SessionAuthenticator struct {
	users    UserFinder
	sessions SessionLookup
	cookie   *middleware.SessionCookie
}

// NewSessionAuthenticator constructs request authentication from explicit
// session and account ports.
func NewSessionAuthenticator(users UserFinder, sessions SessionLookup, cookie *middleware.SessionCookie) (*SessionAuthenticator, error) {
	if users == nil || sessions == nil || cookie == nil {
		return nil, ErrInvalidHTTPDependencies
	}
	return &SessionAuthenticator{users: users, sessions: sessions, cookie: cookie}, nil
}

// Authenticate validates the current browser session and returns only safe
// account identity and authorization state.
func (a *SessionAuthenticator) Authenticate(ctx context.Context, request *http.Request) (middleware.Principal, error) {
	if a == nil || a.users == nil || a.sessions == nil || a.cookie == nil || ctx == nil || request == nil {
		return middleware.Principal{}, ErrSessionAuthentication
	}
	sessionID, ok := a.cookie.Read(request)
	if !ok {
		return middleware.Principal{}, middleware.ErrUnauthenticated
	}
	session, err := a.sessions.Lookup(ctx, sessionID)
	if err != nil {
		return middleware.Principal{}, middleware.ErrUnauthenticated
	}
	user, err := a.users.FindUserByID(ctx, session.UserID)
	if err != nil || user.ID != session.UserID {
		return middleware.Principal{}, ErrSessionAuthentication
	}
	// Child profiles never sign in; a session for one is never honored.
	if user.Kind == service.UserKindProfile {
		return middleware.Principal{}, middleware.ErrUnauthenticated
	}
	return middleware.Principal{ID: strconv.FormatInt(user.ID, 10), IsAdmin: user.IsAdmin}, nil
}
