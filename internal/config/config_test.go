package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	testDatabaseURL  = "postgres://user:password@example.test/app"
	testClientSecret = "client-secret-value"
)

// requiredValues are the settings Load always needs: the database and the
// registered single sign-on client.
func requiredValues() map[string]string {
	return map[string]string{
		DatabaseURLEnv:     testDatabaseURL,
		SSOIssuerURLEnv:    "http://127.0.0.1:4444",
		SSOClientIDEnv:     "calbooker",
		SSOClientSecretEnv: testClientSecret,
	}
}

// loadWith loads requiredValues with overrides applied; an empty override
// removes the setting.
func loadWith(overrides map[string]string) (Config, error) {
	values := requiredValues()
	for name, value := range overrides {
		if value == "" {
			delete(values, name)
			continue
		}
		values[name] = value
	}
	return loadValues(values)
}

func TestLoadDefaults(t *testing.T) {
	config, err := loadWith(nil)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if config.Environment != EnvironmentDevelopment {
		t.Errorf("Environment = %q, want %q", config.Environment, EnvironmentDevelopment)
	}
	if config.HTTPAddress != DefaultHTTPAddress {
		t.Errorf("HTTPAddress = %q, want %q", config.HTTPAddress, DefaultHTTPAddress)
	}
	if config.PublicURL.String() != DefaultPublicURL {
		t.Errorf("PublicURL = %q, want %q", config.PublicURL.String(), DefaultPublicURL)
	}
	if config.SessionCookieSecure {
		t.Error("SessionCookieSecure = true, want false in development")
	}
	if config.SessionLifetime != DefaultSessionLifetime {
		t.Errorf("SessionLifetime = %s, want %s", config.SessionLifetime, DefaultSessionLifetime)
	}
	if config.SessionSecret.IsSet() {
		t.Error("default session secret is configured, want unset")
	}
	if config.SSORedirectURL != DefaultPublicURL+"/en/oauth/sso/callback" {
		t.Errorf("SSORedirectURL = %q, want the callback derived from the default public URL", config.SSORedirectURL)
	}
}

func TestLoadConfiguredValues(t *testing.T) {
	const sessionSecret = "01234567890123456789012345678901"
	config, err := loadWith(map[string]string{
		EnvironmentEnv:         " test ",
		HTTPAddressEnv:         "127.0.0.1:9090",
		PublicURLEnv:           "https://example.test/app",
		SessionCookieSecureEnv: "false",
		SessionLifetimeEnv:     "2h",
		SessionSecretEnv:       sessionSecret,
	})
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if config.Environment != EnvironmentTest {
		t.Errorf("Environment = %q, want %q", config.Environment, EnvironmentTest)
	}
	if config.HTTPAddress != "127.0.0.1:9090" {
		t.Errorf("HTTPAddress = %q, want 127.0.0.1:9090", config.HTTPAddress)
	}
	if config.PublicURL.String() != "https://example.test/app" {
		t.Errorf("PublicURL = %q, want https://example.test/app", config.PublicURL.String())
	}
	if config.SessionCookieSecure {
		t.Error("SessionCookieSecure = true, want false")
	}
	if config.SessionLifetime != 2*time.Hour {
		t.Errorf("SessionLifetime = %s, want 2h", config.SessionLifetime)
	}
	if !config.DatabaseURL.IsSet() || !config.SessionSecret.IsSet() {
		t.Error("configured secrets are not marked as set")
	}
	if config.SessionSecret.Reveal() != sessionSecret {
		t.Error("SessionSecret.Reveal() does not return configured value")
	}
}

func TestLoadRequiresDatabase(t *testing.T) {
	config, err := loadWith(map[string]string{DatabaseURLEnv: ""})
	if !errors.Is(err, ErrMissingDatabase) {
		t.Fatalf("Load(no database) error = %v, want %v", err, ErrMissingDatabase)
	}
	if config != (Config{}) {
		t.Fatal("Load() returned non-zero config on error")
	}
}

func TestLoadProductionCookieDefault(t *testing.T) {
	config, err := loadWith(map[string]string{
		EnvironmentEnv:   string(EnvironmentProduction),
		PublicURLEnv:     "https://example.test",
		SessionSecretEnv: "01234567890123456789012345678901",
		MailRelayEnv:     "smtp://mail.example.test:587",
		SSOIssuerURLEnv:  "https://id.example.test",
	})
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !config.SessionCookieSecure {
		t.Error("SessionCookieSecure = false, want true in production")
	}
}

func TestLoadSecureCookieDefaultFollowsPublicURLScheme(t *testing.T) {
	for publicURL, want := range map[string]bool{"https://staging.example.test": true, "http://localhost:8082": false} {
		config, err := loadWith(map[string]string{PublicURLEnv: publicURL})
		if err != nil {
			t.Fatalf("Load(%s) error = %v", publicURL, err)
		}
		if config.SessionCookieSecure != want {
			t.Errorf("development SessionCookieSecure with %s = %t, want %t", publicURL, config.SessionCookieSecure, want)
		}
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		value     string
		expected  error
		wantNoRaw bool
	}{
		{name: "environment", env: EnvironmentEnv, value: "staging", expected: ErrInvalidEnvironment},
		{name: "http address", env: HTTPAddressEnv, value: "localhost", expected: ErrInvalidHTTPAddress},
		{name: "http port", env: HTTPAddressEnv, value: ":65536", expected: ErrInvalidHTTPAddress},
		{name: "public url scheme", env: PublicURLEnv, value: "ftp://example.test", expected: ErrInvalidPublicURL},
		{name: "public url credentials", env: PublicURLEnv, value: "https://user:password@example.test", expected: ErrInvalidPublicURL, wantNoRaw: true},
		{name: "cookie boolean", env: SessionCookieSecureEnv, value: "sometimes", expected: ErrInvalidBoolean},
		{name: "session duration", env: SessionLifetimeEnv, value: "forever", expected: ErrInvalidDuration},
		{name: "weak session secret", env: SessionSecretEnv, value: "too-short", expected: ErrWeakSecret, wantNoRaw: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := loadWith(map[string]string{test.env: test.value})
			if !errors.Is(err, test.expected) {
				t.Fatalf("Load() error = %v, want errors.Is(_, %v)", err, test.expected)
			}
			if config != (Config{}) {
				t.Fatal("Load() returned non-zero config on error")
			}
			if test.wantNoRaw && strings.Contains(err.Error(), test.value) {
				t.Error("Load() error contains raw configured value")
			}
		})
	}
}

func TestSecretRedaction(t *testing.T) {
	const secretValue = "super-secret-value"
	secret := Secret{value: secretValue}

	if secret.String() == secretValue || strings.Contains(secret.String(), secretValue) {
		t.Error("Secret.String() exposes configured value")
	}
	if !strings.Contains(fmt.Sprintf("%#v", secret), "configured:true") {
		t.Errorf("Secret GoString() = %#v, want configured marker", secret)
	}

	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("json.Marshal(secret) error = %v", err)
	}
	if strings.Contains(string(encoded), secretValue) {
		t.Error("JSON encoding exposes configured value")
	}
}

func TestConfigDiagnosticsRedactSecrets(t *testing.T) {
	const secretValue = "01234567890123456789012345678901"
	config, err := loadWith(map[string]string{SessionSecretEnv: secretValue, DatabaseURLEnv: secretValue})
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	for _, diagnostic := range []string{
		config.String(),
		fmt.Sprintf("%+v", config),
		fmt.Sprintf("%#v", config),
	} {
		if strings.Contains(diagnostic, secretValue) || strings.Contains(diagnostic, testClientSecret) {
			t.Errorf("diagnostic contains secret: %q", diagnostic)
		}
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}
	if strings.Contains(string(encoded), secretValue) || strings.Contains(string(encoded), testClientSecret) {
		t.Error("JSON encoding config exposes secret")
	}
}

func TestLoadNilEnvironmentLookup(t *testing.T) {
	_, err := Load(nil)
	if err == nil {
		t.Fatal("Load(nil) error = nil, want error")
	}
}

func TestLoadRetentionDefaults(t *testing.T) {
	config, err := loadWith(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.GuestRetention != DefaultGuestRetention || config.UsageRetention != DefaultUsageRetention {
		t.Fatalf("retention defaults = %s/%s", config.GuestRetention, config.UsageRetention)
	}
}

func TestLoadConfiguredRetention(t *testing.T) {
	config, err := loadWith(map[string]string{
		GuestRetentionEnv: "48h",
		UsageRetentionEnv: "168h",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.GuestRetention != 48*time.Hour || config.UsageRetention != 168*time.Hour {
		t.Fatalf("retention = %s/%s", config.GuestRetention, config.UsageRetention)
	}
}

func TestLoadRejectsInvalidRetention(t *testing.T) {
	if _, err := loadWith(map[string]string{UsageRetentionEnv: "-1h"}); !errors.Is(err, ErrInvalidDuration) {
		t.Fatalf("Load(invalid retention) error = %v, want %v", err, ErrInvalidDuration)
	}
}

func TestLoadRejectsCredentialBearingMailRelay(t *testing.T) {
	if _, err := loadWith(map[string]string{MailRelayEnv: "smtp://user:password@mail.example.test"}); !errors.Is(err, ErrInvalidMailRelay) {
		t.Fatalf("Load(credential relay) error = %v, want %v", err, ErrInvalidMailRelay)
	}
	config, err := loadWith(map[string]string{MailRelayEnv: "smtp://mail.example.test:587"})
	if err != nil || !config.MailRelay.IsSet() {
		t.Fatalf("Load(valid relay) = %+v, %v", config, err)
	}
	if strings.Contains(config.String(), "mail.example.test") {
		t.Fatalf("config summary leaked relay address: %s", config.String())
	}
	if config.MailFrom != "no-reply@127.0.0.1" {
		t.Fatalf("default MailFrom = %q, want no-reply@ the public URL host", config.MailFrom)
	}
	for name, values := range map[string]map[string]string{
		"non-smtp relay":  {MailRelayEnv: "http://mail.example.test"},
		"display name":    {MailFromEnv: "App <app@example.test>"},
		"invalid address": {MailFromEnv: "not an address"},
	} {
		if _, err := loadWith(values); !errors.Is(err, ErrInvalidMailRelay) {
			t.Fatalf("Load(%s) error = %v, want %v", name, err, ErrInvalidMailRelay)
		}
	}
	ipv6Host, err := loadWith(map[string]string{PublicURLEnv: "http://[::1]:8082"})
	if err != nil || ipv6Host.MailFrom != "no-reply@localhost" {
		t.Fatalf("Load(IPv6 public URL) MailFrom = %q, %v; want fallback sender", ipv6Host.MailFrom, err)
	}
}

func TestLoadValidatesTrustedProxy(t *testing.T) {
	if _, err := loadWith(map[string]string{TrustedProxyEnv: "not-an-address"}); !errors.Is(err, ErrInvalidTrustedProxy) {
		t.Fatalf("Load(invalid proxy) error = %v, want %v", err, ErrInvalidTrustedProxy)
	}
	for _, valid := range []string{"10.0.0.1", "10.0.0.0/8", "2001:db8::/32"} {
		config, err := loadWith(map[string]string{TrustedProxyEnv: valid})
		if err != nil || config.TrustedProxy != valid {
			t.Fatalf("Load(proxy=%q) = %q, %v", valid, config.TrustedProxy, err)
		}
	}
}

func TestLoadSSOSettings(t *testing.T) {
	type ssoSettings struct {
		Issuer, ClientID, Redirect, Account string
	}
	tests := []struct {
		name      string
		overrides map[string]string
		want      ssoSettings
	}{
		{
			name:      "derives the callback from the public URL",
			overrides: map[string]string{PublicURLEnv: "http://127.0.0.1:8082/"},
			want: ssoSettings{
				Issuer:   "http://127.0.0.1:4444",
				ClientID: "calbooker",
				Redirect: "http://127.0.0.1:8082/en/oauth/sso/callback",
			},
		},
		{
			name: "keeps explicit callback and account page",
			overrides: map[string]string{
				SSOIssuerURLEnv:   "https://id.example.test",
				SSORedirectURLEnv: "https://app.example.test/hu/oauth/sso/callback",
				SSOAccountURLEnv:  "https://accounts.example.test/en/account/settings",
			},
			want: ssoSettings{
				Issuer:   "https://id.example.test",
				ClientID: "calbooker",
				Redirect: "https://app.example.test/hu/oauth/sso/callback",
				Account:  "https://accounts.example.test/en/account/settings",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded, err := loadWith(test.overrides)
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			got := ssoSettings{loaded.SSOIssuerURL, loaded.SSOClientID, loaded.SSORedirectURL, loaded.SSOAccountURL}
			if got != test.want {
				t.Fatalf("sso settings = %+v, want %+v", got, test.want)
			}
			if loaded.SSOClientSecret.Reveal() != testClientSecret {
				t.Fatal("SSOClientSecret does not hold the configured secret")
			}
		})
	}
}

func TestLoadRejectsInvalidSSOSettings(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
	}{
		{name: "without secret", overrides: map[string]string{SSOClientSecretEnv: ""}},
		{name: "without client id", overrides: map[string]string{SSOClientIDEnv: ""}},
		{name: "without issuer", overrides: map[string]string{SSOIssuerURLEnv: ""}},
		{name: "issuer with query", overrides: map[string]string{SSOIssuerURLEnv: "https://id.example.test/?realm=x"}},
		{name: "issuer with credentials", overrides: map[string]string{SSOIssuerURLEnv: "https://admin:password@id.example.test"}},
		{name: "relative redirect", overrides: map[string]string{SSORedirectURLEnv: "/en/oauth/sso/callback"}},
		{name: "non-http account page", overrides: map[string]string{SSOAccountURLEnv: "ftp://accounts.example.test"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded, err := loadWith(test.overrides)
			if !errors.Is(err, ErrInvalidSSO) {
				t.Fatalf("Load() error = %v, want errors.Is(_, %v)", err, ErrInvalidSSO)
			}
			if loaded != (Config{}) {
				t.Fatal("Load() returned a non-zero config for invalid single sign-on settings")
			}
			for _, secret := range []string{testClientSecret, "password"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("sso error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestResendAPIKeyReplacesTheRelayAndStaysSecret(t *testing.T) {
	const key = "re_test_1234567890abcdef"
	production := map[string]string{
		EnvironmentEnv:   string(EnvironmentProduction),
		PublicURLEnv:     "https://calbooker.example.test",
		SessionSecretEnv: "01234567890123456789012345678901",
		SSOIssuerURLEnv:  "https://id.example.test",
	}
	load := func(extra map[string]string) (Config, error) {
		values := map[string]string{}
		for name, value := range production {
			values[name] = value
		}
		for name, value := range extra {
			values[name] = value
		}
		return loadWith(values)
	}

	// A Resend key satisfies the production mail requirement, given an
	// explicit sender on a verified domain.
	config, err := load(map[string]string{ResendAPIKeyEnv: " " + key + "\n", MailFromEnv: "no-reply@example.test"})
	if err != nil {
		t.Fatalf("Load(production with Resend) error = %v", err)
	}
	if !config.MailConfigured() || config.MailRelay.IsSet() || config.ResendAPIKey.Reveal() != key {
		t.Fatalf("mail = relay %t, resend %q; want only the trimmed Resend key", config.MailRelay.IsSet(), config.ResendAPIKey.Reveal())
	}
	for _, text := range []string{config.String(), fmt.Sprintf("%v", config.ResendAPIKey), fmt.Sprintf("%+v", config.ResendAPIKey)} {
		if strings.Contains(text, key) {
			t.Fatalf("diagnostic text leaks the Resend key: %s", text)
		}
	}

	// The derived default sender is on the app's own host, which Resend would
	// refuse, so production demands an explicit one.
	if _, err := load(map[string]string{ResendAPIKeyEnv: key}); !errors.Is(err, ErrUnsafeProductionConfig) || !strings.Contains(err.Error(), MailFromEnv) {
		t.Fatalf("Load(production, Resend without sender) error = %v, want %v mentioning %s", err, ErrUnsafeProductionConfig, MailFromEnv)
	}
	// Neither mail setting is still unsafe, and the message names both.
	if _, err := load(nil); !errors.Is(err, ErrUnsafeProductionConfig) || !strings.Contains(err.Error(), ResendAPIKeyEnv) {
		t.Fatalf("Load(production, no mail) error = %v, want %v naming both settings", err, ErrUnsafeProductionConfig)
	}
	// One provider at a time, and no key that could break a header.
	for name, values := range map[string]map[string]string{
		"relay and key":  {MailRelayEnv: "smtp://mail.example.test:587", ResendAPIKeyEnv: key, MailFromEnv: "no-reply@example.test"},
		"key with space": {ResendAPIKeyEnv: "re_abc def", MailFromEnv: "no-reply@example.test"},
		"oversized key":  {ResendAPIKeyEnv: "re_" + strings.Repeat("a", 300), MailFromEnv: "no-reply@example.test"},
	} {
		if _, err := load(values); !errors.Is(err, ErrInvalidMailRelay) {
			t.Fatalf("Load(%s) error = %v, want %v", name, err, ErrInvalidMailRelay)
		}
	}
	// Development needs neither.
	if _, err := loadWith(map[string]string{ResendAPIKeyEnv: key}); err != nil {
		t.Fatalf("Load(development with Resend) error = %v", err)
	}
}
