// Package app owns explicit application dependency composition.
package app

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mailadapter "github.com/tewecske/calbooker/internal/adapter/mail"
	oidcadapter "github.com/tewecske/calbooker/internal/adapter/oidc"
	"github.com/tewecske/calbooker/internal/config"
	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
	postgresstore "github.com/tewecske/calbooker/internal/store/postgres"
)

var ErrNilDatabase = errors.New("app: nil database")
var ErrMailDeliveryNotConfigured = errors.New("app: mail delivery not configured")

// Graph contains concrete PostgreSQL repositories and all currently
// implemented service use cases. It does not own Database; the server lifecycle
// closes that shared resource after HTTP shutdown.
type Graph struct {
	Database *sql.DB

	Users                 *postgresstore.UserRepository
	Sessions              *postgresstore.SessionRepository
	GuestClaimRepository  *postgresstore.GuestClaimCodeRepository
	OAuthStates           *postgresstore.OAuthStateRepository
	OAuthIdentities       *postgresstore.OAuthIdentityRepository
	Groups                *postgresstore.GroupRepository
	GroupMemberships      *postgresstore.GroupMembershipRepository
	AdminUsers            *postgresstore.AdminUserRepository
	AuditLogs             *postgresstore.AuditLogRepository
	Retention             *postgresstore.RetentionRepository
	DatastoreStats        *postgresstore.DatastoreStatsRepository
	UsageEvents           *postgresstore.UsageEventRepository
	UsageReport           *postgresstore.UsageReportRepository
	Suspicious            *postgresstore.SuspiciousAccountRepository
	SessionService        *service.SessionService
	Mail                  service.MailSender
	GuestSession          *service.GuestSessionService
	GuestClaim            *service.GuestClaimService
	GuestRedemption       *service.GuestRedemptionService
	GuestRateLimited      *service.RateLimitedGuestService
	GuestCleanup          *service.GuestCleanupService
	Theme                 *service.ThemeService
	LocaleSettings        *service.LocaleSettingsService
	Group                 *service.GroupService
	GroupJoin             *service.RateLimitedGroupJoiner
	AdminAccounts         *service.AdminAccountService
	AdminDiagnostics      *service.AdminDiagnosticsService
	Audit                 *service.AuditService
	Maintenance           *service.MaintenanceWorker
	RetentionService      *service.RetentionService
	SystemInfo            *service.SystemInfoService
	RuntimeInfo           *service.RuntimeInfoService
	DatastoreStatsService *service.DatastoreStatsService
	RateLimits            *service.RateLimitAdminService
	Usage                 *service.UsageQueue
	UsageReportService    *service.UsageReportService
	SuspiciousService     *service.SuspiciousAccountService
	SSOClient             *service.SSOClientService
}

// newSSOClient builds this app's OpenID Connect relying party and
// sign-in use cases. It performs no network I/O; discovery happens on first use.
func newSSOClient(appConfig config.Config, states service.OAuthStateRepository, identities service.OAuthIdentityRepository, users *postgresstore.UserRepository, sessions *service.SessionService, ssoSessions service.SSOClientSessionRepository) (*service.SSOClientService, error) {
	relyingParty, err := oidcadapter.New(oidcadapter.Config{
		Issuer:       appConfig.SSOIssuerURL,
		ClientID:     appConfig.SSOClientID,
		ClientSecret: appConfig.SSOClientSecret.Reveal(),
		RedirectURL:  appConfig.SSORedirectURL,
	})
	if err != nil {
		return nil, fmt.Errorf("construct oidc client: %w", err)
	}
	// The issuer returns here after sign-out; it must match the registered
	// post-logout redirect URI exactly.
	signInPath, err := locale.Path(locale.Default, "/sign-in")
	if err != nil {
		return nil, fmt.Errorf("derive post-logout redirect: %w", err)
	}
	postLogoutURL := strings.TrimRight(appConfig.PublicURL.String(), "/") + signInPath
	client, err := service.NewSSOClientService(service.SSOClientDependencies{
		RelyingParty:  relyingParty,
		States:        states,
		Identities:    identities,
		Users:         users,
		Sessions:      sessions,
		SSOSessions:   ssoSessions,
		GuestUpgrades: users,
		StateLifetime: service.DefaultOAuthStateLifetime,
		PostLogoutURL: postLogoutURL,
	})
	if err != nil {
		return nil, fmt.Errorf("construct sso client service: %w", err)
	}
	return client, nil
}

// New constructs the service graph over one migrated database. It performs no
// database I/O; startup owns opening, migration, and closing Database.
func New(database *sql.DB, appConfig config.Config) (*Graph, error) {
	if database == nil {
		return nil, ErrNilDatabase
	}
	if appConfig.Environment == config.EnvironmentProduction && !appConfig.MailConfigured() {
		return nil, ErrMailDeliveryNotConfigured
	}
	appConfig = withRetentionDefaults(appConfig)

	users, err := postgresstore.NewUserRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct user repository: %w", err)
	}
	sessionsRepository, err := postgresstore.NewSessionRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct session repository: %w", err)
	}
	guestClaimsRepository, err := postgresstore.NewGuestClaimCodeRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct guest claim repository: %w", err)
	}
	oauthStates, err := postgresstore.NewOAuthStateRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct oauth state repository: %w", err)
	}
	oauthIdentities, err := postgresstore.NewOAuthIdentityRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct oauth identity repository: %w", err)
	}
	groupsRepository, err := postgresstore.NewGroupRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct group repository: %w", err)
	}
	groupMembershipsRepository, err := postgresstore.NewGroupMembershipRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct group membership repository: %w", err)
	}
	adminUsersRepository, err := postgresstore.NewAdminUserRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct admin user repository: %w", err)
	}
	auditLogRepository, err := postgresstore.NewAuditLogRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct audit log repository: %w", err)
	}
	retentionRepository, err := postgresstore.NewRetentionRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct retention repository: %w", err)
	}
	datastoreStatsRepository, err := postgresstore.NewDatastoreStatsRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct datastore stats repository: %w", err)
	}
	usageEventRepository, err := postgresstore.NewUsageEventRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct usage event repository: %w", err)
	}
	usageReportRepository, err := postgresstore.NewUsageReportRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct usage report repository: %w", err)
	}
	suspiciousRepository, err := postgresstore.NewSuspiciousAccountRepository(database)
	if err != nil {
		return nil, fmt.Errorf("construct suspicious account repository: %w", err)
	}

	sessionService, err := service.NewSessionService(sessionsRepository, appConfig.SessionLifetime)
	if err != nil {
		return nil, fmt.Errorf("construct session service: %w", err)
	}
	// Without a relay, development keeps mail in memory; set CALBOOKER_MAIL_RELAY
	// to a local catcher such as Mailpit to read messages.
	var mailSender service.MailSender = &mailadapter.DevelopmentSender{}
	switch {
	case appConfig.ResendAPIKey.IsSet():
		resendSender, err := mailadapter.NewResendSender(appConfig.ResendAPIKey.Reveal(), appConfig.MailFrom)
		if err != nil {
			return nil, fmt.Errorf("construct resend sender: %w", err)
		}
		mailSender = resendSender
	case appConfig.MailRelay.IsSet():
		smtpSender, err := mailadapter.NewSMTPSender(appConfig.MailRelay.Reveal(), appConfig.MailFrom)
		if err != nil {
			return nil, fmt.Errorf("construct smtp sender: %w", err)
		}
		mailSender = smtpSender
	}
	ssoClient, err := newSSOClient(appConfig, oauthStates, oauthIdentities, users, sessionService, sessionsRepository)
	if err != nil {
		return nil, err
	}
	guestSession, err := service.NewGuestSessionService(users, sessionService)
	if err != nil {
		return nil, fmt.Errorf("construct guest session service: %w", err)
	}
	guestClaims, err := service.NewGuestClaimService(users, guestClaimsRepository)
	if err != nil {
		return nil, fmt.Errorf("construct guest claim service: %w", err)
	}
	guestRedemption, err := service.NewGuestRedemptionService(users, guestClaimsRepository, sessionService)
	if err != nil {
		return nil, fmt.Errorf("construct guest redemption service: %w", err)
	}
	guestLimiter, rateLimitStore, err := newSharedRateLimiter(database, "guest", service.RateLimitConfig{Limit: 10, Window: time.Minute})
	if err != nil {
		return nil, fmt.Errorf("construct guest rate limiter: %w", err)
	}
	guestRateLimited, err := service.NewRateLimitedGuestService(guestSession, guestRedemption, guestLimiter)
	if err != nil {
		return nil, fmt.Errorf("construct rate limited guest service: %w", err)
	}
	guestCleanup, err := service.NewGuestCleanupService(guestClaimsRepository, appConfig.GuestRetention)
	if err != nil {
		return nil, fmt.Errorf("construct guest cleanup service: %w", err)
	}
	theme, err := service.NewThemeService(users)
	if err != nil {
		return nil, fmt.Errorf("construct theme service: %w", err)
	}
	localeSettings, err := service.NewLocaleSettingsService(users)
	if err != nil {
		return nil, fmt.Errorf("construct locale settings service: %w", err)
	}
	groupService, err := service.NewGroupService(groupsRepository, groupMembershipsRepository, users)
	if err != nil {
		return nil, fmt.Errorf("construct group service: %w", err)
	}
	groupJoinLimiter, _, err := newSharedRateLimiter(database, "group-join", service.RateLimitConfig{Limit: 20, Window: time.Minute})
	if err != nil {
		return nil, fmt.Errorf("construct group join rate limiter: %w", err)
	}
	groupJoinService, err := service.NewRateLimitedGroupJoiner(groupService, groupJoinLimiter)
	if err != nil {
		return nil, fmt.Errorf("construct group join service: %w", err)
	}
	auditService, err := service.NewAuditService(auditLogRepository)
	if err != nil {
		return nil, fmt.Errorf("construct audit service: %w", err)
	}
	groupService.SetAuditor(auditService)
	adminAccounts, err := service.NewAdminAccountService(users, adminUsersRepository, auditService, sessionService)
	if err != nil {
		return nil, fmt.Errorf("construct admin account service: %w", err)
	}
	adminDiagnostics, err := service.NewAdminDiagnosticsService(users, sessionsRepository, oauthIdentities)
	if err != nil {
		return nil, fmt.Errorf("construct admin diagnostics service: %w", err)
	}
	retentionService, err := service.NewRetentionService(retentionRepository, appConfig.UsageRetention)
	if err != nil {
		return nil, fmt.Errorf("construct retention service: %w", err)
	}
	datastoreStats, err := service.NewDatastoreStatsService(datastoreStatsRepository, service.DefaultDatastoreStatsCacheTTL)
	if err != nil {
		return nil, fmt.Errorf("construct datastore stats service: %w", err)
	}
	rateLimits, err := service.NewRateLimitAdminService(auditService, guestLimiter, groupJoinLimiter)
	if err != nil {
		return nil, fmt.Errorf("construct rate limit admin service: %w", err)
	}
	usageQueue, err := service.NewUsageQueue(usageEventRepository, service.DefaultUsageQueueCapacity)
	if err != nil {
		return nil, fmt.Errorf("construct usage queue: %w", err)
	}
	usageReportService, err := service.NewUsageReportService(usageReportRepository, usageQueue)
	if err != nil {
		return nil, fmt.Errorf("construct usage report service: %w", err)
	}
	suspiciousService, err := service.NewSuspiciousAccountService(suspiciousRepository)
	if err != nil {
		return nil, fmt.Errorf("construct suspicious account service: %w", err)
	}
	maintenanceWorker, err := service.NewMaintenanceWorker(
		service.DefaultMaintenanceInterval,
		service.MaintenanceJob{Name: service.MaintenanceJobGuestCleanup, Run: guestCleanup.Cleanup},
		service.MaintenanceJob{Name: service.MaintenanceJobTokenRetention, Run: retentionService.CleanupTokens},
		service.MaintenanceJob{Name: service.MaintenanceJobUsageRetention, Run: retentionService.CleanupUsageEvents},
		service.MaintenanceJob{Name: service.MaintenanceJobRateLimitRetention, Run: rateLimitStore.DeleteExpired},
	)
	if err != nil {
		return nil, fmt.Errorf("construct maintenance worker: %w", err)
	}
	runtimeInfo := service.NewRuntimeInfoService(time.Now())
	systemInfo := service.NewSystemInfoService(service.SystemInfoSettings{
		Environment:          string(appConfig.Environment),
		PublicAddress:        appConfig.HTTPAddress,
		PublicURL:            appConfig.PublicURL.String(),
		SecureSessionCookies: appConfig.SessionCookieSecure,
		MailConfigured:       appConfig.MailConfigured(),
		MailRelay:            mailRelayLabel(appConfig),
		SessionLifetime:      appConfig.SessionLifetime,
		GuestRetention:       appConfig.GuestRetention,
		UsageRetention:       appConfig.UsageRetention,
		MaintenanceInterval:  maintenanceWorker.Interval(),
		TrustedProxy:         appConfig.TrustedProxy,
		SSOIssuer:            appConfig.SSOIssuerURL,
	})

	return &Graph{
		Database:              database,
		Users:                 users,
		Sessions:              sessionsRepository,
		GuestClaimRepository:  guestClaimsRepository,
		OAuthStates:           oauthStates,
		OAuthIdentities:       oauthIdentities,
		Groups:                groupsRepository,
		GroupMemberships:      groupMembershipsRepository,
		AdminUsers:            adminUsersRepository,
		AuditLogs:             auditLogRepository,
		Retention:             retentionRepository,
		DatastoreStats:        datastoreStatsRepository,
		UsageEvents:           usageEventRepository,
		UsageReport:           usageReportRepository,
		Suspicious:            suspiciousRepository,
		SessionService:        sessionService,
		Mail:                  mailSender,
		GuestSession:          guestSession,
		GuestClaim:            guestClaims,
		GuestRedemption:       guestRedemption,
		GuestRateLimited:      guestRateLimited,
		GuestCleanup:          guestCleanup,
		Theme:                 theme,
		LocaleSettings:        localeSettings,
		Group:                 groupService,
		GroupJoin:             groupJoinService,
		AdminAccounts:         adminAccounts,
		AdminDiagnostics:      adminDiagnostics,
		Audit:                 auditService,
		Maintenance:           maintenanceWorker,
		RetentionService:      retentionService,
		SystemInfo:            systemInfo,
		RuntimeInfo:           runtimeInfo,
		DatastoreStatsService: datastoreStats,
		RateLimits:            rateLimits,
		Usage:                 usageQueue,
		UsageReportService:    usageReportService,
		SuspiciousService:     suspiciousService,
		SSOClient:             ssoClient,
	}, nil
}

// withRetentionDefaults keeps graph construction usable when a caller provides
// a partial configuration in tests or tooling.
func withRetentionDefaults(appConfig config.Config) config.Config {
	if appConfig.GuestRetention <= 0 {
		appConfig.GuestRetention = config.DefaultGuestRetention
	}
	if appConfig.UsageRetention <= 0 {
		appConfig.UsageRetention = config.DefaultUsageRetention
	}
	return appConfig
}

// newSharedRateLimiter builds a limiter whose counters live in the database,
// so every instance of the app shares them. The returned store also owns the
// table-wide cleanup of expired windows.
func newSharedRateLimiter(database *sql.DB, scope string, config service.RateLimitConfig) (*service.RateLimiter, *postgresstore.RateLimitStore, error) {
	store, err := postgresstore.NewRateLimitStore(database, scope)
	if err != nil {
		return nil, nil, err
	}
	limiter, err := service.NewRateLimiterWithStore(config, store)
	if err != nil {
		return nil, nil, err
	}
	return limiter, store, nil
}

// mailRelayLabel names where mail goes for the administrator system page: the
// SMTP relay's host, or the Resend API. It never includes a credential.
func mailRelayLabel(appConfig config.Config) string {
	if appConfig.ResendAPIKey.IsSet() {
		return "https://api.resend.com"
	}
	return appConfig.MailRelay.Reveal()
}
