package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	// Embed the IANA time zone database so group time zones resolve in
	// minimal images without /usr/share/zoneinfo.
	_ "time/tzdata"

	httpadapter "github.com/tewecske/calbooker/internal/adapter/http"
	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/app"
	"github.com/tewecske/calbooker/internal/config"
	appserver "github.com/tewecske/calbooker/internal/server"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
)

func main() {
	appConfig, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	applicationLogger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	securityLogger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger := httpadapter.NewLogger(applicationLogger, securityLogger)

	startupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := store.Open(startupContext, appConfig.DatabaseURL.Reveal())
	if err != nil {
		cancel()
		log.Fatal(err)
	}
	migrator, err := store.NewMigrator(database, migrations.FS)
	if err != nil {
		cancel()
		_ = database.Close()
		log.Fatal(err)
	}
	err = migrator.Apply(startupContext)
	cancel()
	if err != nil {
		_ = database.Close()
		log.Fatal(err)
	}
	applicationGraph, err := app.New(database, appConfig)
	if err != nil {
		_ = database.Close()
		log.Fatal(err)
	}
	sessionCookie, err := middleware.NewSessionCookie(middleware.SessionCookieConfig{
		Secure:   appConfig.SessionCookieSecure,
		Lifetime: appConfig.SessionLifetime,
	})
	if err != nil {
		_ = database.Close()
		log.Fatal(err)
	}
	applicationGraph.Audit.SetSink(httpadapter.NewAuditSecuritySink(logger))
	applicationGraph.RuntimeInfo.SetMigrations(app.NewMigrationStatusProvider(migrator))
	spanTracer := httpadapter.NewSpanTracer(httpadapter.SpanTracerConfig{
		Logger:       applicationLogger,
		TrustedProxy: appConfig.TrustedProxy,
	})
	dependencies := httpadapter.Dependencies{
		Users:               applicationGraph.Users,
		Sessions:            applicationGraph.SessionService,
		SessionAdmin:        applicationGraph.SessionService,
		SessionCookie:       sessionCookie,
		GuestCreator:        applicationGraph.GuestRateLimited,
		GuestClaimer:        applicationGraph.GuestClaim,
		GuestRedeemer:       applicationGraph.GuestRateLimited,
		LocaleSettings:      applicationGraph.LocaleSettings,
		ThemeUpdater:        applicationGraph.Theme,
		Groups:              applicationGraph.Group,
		GroupAccess:         applicationGraph.GroupAccess,
		Teams:               applicationGraph.Team,
		Invites:             applicationGraph.Invite,
		Profiles:            applicationGraph.Profile,
		JoinGroup:           applicationGraph.GroupJoin,
		AdminAccounts:       applicationGraph.AdminAccounts,
		AdminAccountEditor:  applicationGraph.AdminAccounts,
		AdminUserDetailer:   applicationGraph.AdminDiagnostics,
		AdminSessionRevoker: applicationGraph.AdminAccounts,
		AdminAudit:          applicationGraph.Audit,
		AdminMaintenance:    applicationGraph.Maintenance,
		AdminAuditRecorder:  applicationGraph.Audit,
		AdminSystemConfig:   applicationGraph.SystemInfo,
		AdminRuntime:        applicationGraph.RuntimeInfo,
		AdminDatastoreStats: applicationGraph.DatastoreStatsService,
		AdminRateLimits:     applicationGraph.RateLimits,
		Usage:               httpadapter.NewUsageRecorder(applicationGraph.Usage),
		AdminUsage:          applicationGraph.UsageReportService,
		AdminSuspicious:     applicationGraph.SuspiciousService,
		Tracer:              spanTracer,
		SecurityLogger:      logger,
		SSOClient:           applicationGraph.SSOClient,
		SSOAccountURL:       appConfig.SSOAccountURL,
		TrustedProxy:        appConfig.TrustedProxy,
		DevelopmentRoutes:   appConfig.DevelopmentRoutes(),
	}
	router := httpadapter.NewLoggedRouterWithServices(logger, dependencies)

	server, err := appserver.New(appConfig.HTTPAddress, router, appserver.WithDependency(database))
	if err != nil {
		_ = database.Close()
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	applicationGraph.Maintenance.SetLogger(applicationLogger)
	go func() {
		if err := applicationGraph.Maintenance.Run(ctx); err != nil {
			applicationLogger.Error("maintenance worker stopped", "error", err)
		}
	}()
	applicationGraph.Usage.SetLogger(applicationLogger)
	go func() {
		if err := applicationGraph.Usage.Run(ctx); err != nil {
			applicationLogger.Error("usage queue stopped", "error", err)
		}
	}()

	logger.Application().Info("server listening", "address", appConfig.HTTPAddress)
	if err := server.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
