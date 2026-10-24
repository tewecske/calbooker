package app

import (
	"database/sql"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/config"
)

func loadTestConfig(t *testing.T) config.Config {
	t.Helper()
	loaded, err := config.Load(func(name string) string {
		return map[string]string{
			config.DatabaseURLEnv:     "postgres://user:pw@127.0.0.1/app",
			config.PublicURLEnv:       "http://127.0.0.1:8082",
			config.SSOIssuerURLEnv:    "http://127.0.0.1:4444",
			config.SSOClientIDEnv:     "calbooker",
			config.SSOClientSecretEnv: "client-secret",
		}[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestNewRejectsNilDatabase(t *testing.T) {
	if _, err := New(nil, config.Config{}); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("New(nil) error = %v, want %v", err, ErrNilDatabase)
	}
}

func TestNewRejectsProductionWithoutMailDelivery(t *testing.T) {
	appConfig := config.Config{
		Environment:     config.EnvironmentProduction,
		PublicURL:       url.URL{Scheme: "https", Host: "example.test"},
		SessionLifetime: time.Hour,
	}
	if _, err := New(&sql.DB{}, appConfig); !errors.Is(err, ErrMailDeliveryNotConfigured) {
		t.Fatalf("New(production) error = %v, want %v", err, ErrMailDeliveryNotConfigured)
	}
}

func TestNewRejectsIncompleteSSOClient(t *testing.T) {
	appConfig := config.Config{
		Environment:     config.EnvironmentDevelopment,
		PublicURL:       url.URL{Scheme: "http", Host: "127.0.0.1:8082"},
		SessionLifetime: time.Hour,
		SSOIssuerURL:    "http://127.0.0.1:4444",
		SSOClientID:     "calbooker",
		SSORedirectURL:  "http://127.0.0.1:8082/en/oauth/sso/callback",
	}
	if _, err := New(&sql.DB{}, appConfig); err == nil {
		t.Fatal("New() without a client secret succeeded")
	}
}

func TestNewBuildsCompleteGraphWithoutDatabaseIO(t *testing.T) {
	database := &sql.DB{}
	graph, err := New(database, loadTestConfig(t))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if graph.Database != database {
		t.Fatal("graph does not retain injected database")
	}
	for name, value := range map[string]any{
		"users": graph.Users, "sessions": graph.Sessions, "guest claims": graph.GuestClaimRepository,
		"oauth states": graph.OAuthStates, "oauth identities": graph.OAuthIdentities,
		"session service": graph.SessionService, "mail": graph.Mail, "sso client": graph.SSOClient,
		"guest session": graph.GuestSession, "guest claims service": graph.GuestClaim,
		"guest redemption": graph.GuestRedemption, "guest rate limited": graph.GuestRateLimited,
		"guest cleanup": graph.GuestCleanup, "theme": graph.Theme, "locale settings": graph.LocaleSettings,
		"groups": graph.Group, "group join": graph.GroupJoin, "events": graph.Event, "admin accounts": graph.AdminAccounts,
		"admin diagnostics": graph.AdminDiagnostics, "audit": graph.Audit, "maintenance": graph.Maintenance,
		"retention": graph.RetentionService, "rate limits": graph.RateLimits, "usage": graph.Usage,
	} {
		if value == nil {
			t.Fatalf("graph field %s is nil", name)
		}
	}
	if issuer := graph.SystemInfo.SystemConfiguration().SSOIssuer; issuer != "http://127.0.0.1:4444" {
		t.Fatalf("system SSOIssuer = %q, want the configured issuer", issuer)
	}
}

func TestNewMigrationStatusProviderRejectsNilRunner(t *testing.T) {
	if provider := NewMigrationStatusProvider(nil); provider != nil {
		t.Fatalf("NewMigrationStatusProvider(nil) = %v, want nil", provider)
	}
}
