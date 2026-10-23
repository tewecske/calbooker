// Package config loads and validates deployment configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tewecske/calbooker/internal/locale"
)

const (
	// EnvironmentEnv names the environment classification variable.
	EnvironmentEnv = "CALBOOKER_ENV"
	// HTTPAddressEnv names the environment variable for the HTTP listen address.
	HTTPAddressEnv = "CALBOOKER_HTTP_ADDR"
	// PublicURLEnv names the externally visible application URL.
	PublicURLEnv = "CALBOOKER_PUBLIC_URL"
	// SessionCookieSecureEnv controls the Secure cookie attribute.
	SessionCookieSecureEnv = "CALBOOKER_SESSION_COOKIE_SECURE"
	// SessionLifetimeEnv names the session lifetime duration.
	SessionLifetimeEnv = "CALBOOKER_SESSION_LIFETIME"
	// GuestRetentionEnv names how long an empty abandoned guest is retained.
	GuestRetentionEnv = "CALBOOKER_GUEST_RETENTION"
	// UsageRetentionEnv names how long usage events are retained.
	UsageRetentionEnv = "CALBOOKER_USAGE_RETENTION"
	// MailRelayEnv names the optional outgoing mail relay.
	MailRelayEnv = "CALBOOKER_MAIL_RELAY"
	// ResendAPIKeyEnv names the optional Resend API key. Setting it delivers
	// mail through the Resend HTTP API instead of an SMTP relay.
	ResendAPIKeyEnv = "CALBOOKER_RESEND_API_KEY"
	// MailFromEnv names the optional sender address for outgoing mail.
	MailFromEnv = "CALBOOKER_MAIL_FROM"
	// TrustedProxyEnv names the optional trusted proxy address or network.
	TrustedProxyEnv = "CALBOOKER_TRUSTED_PROXY"
	// DatabaseURLEnv names the required database connection URL.
	DatabaseURLEnv = "CALBOOKER_DATABASE_URL"
	// SessionSecretEnv names the optional session-signing secret.
	SessionSecretEnv = "CALBOOKER_SESSION_SECRET"
	// SSOIssuerURLEnv names the OpenID Connect issuer (Hydra's public URL).
	SSOIssuerURLEnv = "CALBOOKER_SSO_ISSUER_URL"
	// SSOClientIDEnv names this app's client identifier registered in Hydra.
	SSOClientIDEnv = "CALBOOKER_SSO_CLIENT_ID"
	// SSOClientSecretEnv names this app's client secret registered in Hydra.
	SSOClientSecretEnv = "CALBOOKER_SSO_CLIENT_SECRET"
	// SSORedirectURLEnv overrides the derived single sign-on callback URL.
	SSORedirectURLEnv = "CALBOOKER_SSO_REDIRECT_URL"
	// SSOAccountURLEnv names the provider's account settings page linked from
	// this app.
	SSOAccountURLEnv = "CALBOOKER_SSO_ACCOUNT_URL"

	// DefaultEnvironment is used when EnvironmentEnv is not set.
	DefaultEnvironment Environment = EnvironmentDevelopment
	// DefaultHTTPAddress is used when HTTPAddressEnv is not set.
	DefaultHTTPAddress = "127.0.0.1:8082"
	// DefaultPublicURL is used when PublicURLEnv is not set. It uses
	// 127.0.0.1, not localhost, so its cookies never mix with the development
	// provider's on localhost.
	DefaultPublicURL = "http://127.0.0.1:8082"
	// DefaultSessionLifetime is used when SessionLifetimeEnv is not set.
	DefaultSessionLifetime = 24 * time.Hour
	// DefaultGuestRetention is used when GuestRetentionEnv is not set.
	DefaultGuestRetention = 30 * 24 * time.Hour
	// DefaultUsageRetention is used when UsageRetentionEnv is not set.
	DefaultUsageRetention = 90 * 24 * time.Hour
	// MinimumSessionSecretLength prevents weak configured session secrets.
	MinimumSessionSecretLength = 32
)

var (
	// ErrInvalidEnvironment identifies an unsupported environment value.
	ErrInvalidEnvironment = errors.New("config: invalid environment")
	// ErrInvalidHTTPAddress identifies an invalid HTTP listen address.
	ErrInvalidHTTPAddress = errors.New("config: invalid http address")
	// ErrInvalidPublicURL identifies an unsafe or malformed public URL.
	ErrInvalidPublicURL = errors.New("config: invalid public url")
	// ErrInvalidBoolean identifies a malformed boolean setting.
	ErrInvalidBoolean = errors.New("config: invalid boolean")
	// ErrInvalidDuration identifies a malformed or out-of-range duration.
	ErrInvalidDuration = errors.New("config: invalid duration")
	// ErrWeakSecret identifies a configured secret that is too short.
	ErrWeakSecret = errors.New("config: weak secret")
	// ErrInvalidMailRelay identifies a malformed outgoing mail relay.
	ErrInvalidMailRelay = errors.New("config: invalid mail relay")
	// ErrInvalidTrustedProxy identifies an unsupported trusted proxy value.
	ErrInvalidTrustedProxy = errors.New("config: invalid trusted proxy")
	// ErrMissingDatabase identifies a configuration without a database URL.
	ErrMissingDatabase = errors.New("config: database url not configured")
	// ErrInvalidSSO identifies missing or malformed single sign-on settings.
	ErrInvalidSSO = errors.New("config: invalid single sign-on configuration")
	// ErrUnsafeProductionConfig identifies deployment settings that are unsafe
	// to start with in production.
	ErrUnsafeProductionConfig = errors.New("config: unsafe production configuration")
)

// Environment identifies deployment behavior that may vary by environment.
type Environment string

const (
	// EnvironmentDevelopment is the local-development environment.
	EnvironmentDevelopment Environment = "development"
	// EnvironmentTest is the isolated-test environment.
	EnvironmentTest Environment = "test"
	// EnvironmentProduction is the deployed environment.
	EnvironmentProduction Environment = "production"
)

// Secret stores sensitive configuration without exposing its value through
// formatting or JSON encoding.
type Secret struct {
	value string
}

// IsSet reports whether a secret was configured.
func (s Secret) IsSet() bool {
	return s.value != ""
}

// Reveal returns the configured value for code that must use the secret.
// Callers must not log, serialize, or include the returned value in responses.
func (s Secret) Reveal() string {
	return s.value
}

// String redacts configured secret values.
func (s Secret) String() string {
	if !s.IsSet() {
		return ""
	}
	return "[redacted]"
}

// GoString keeps %#v diagnostics redacted too.
func (s Secret) GoString() string {
	return fmt.Sprintf("config.Secret{configured:%t}", s.IsSet())
}

// MarshalJSON emits only a redacted marker for configured secrets.
func (s Secret) MarshalJSON() ([]byte, error) {
	if !s.IsSet() {
		return []byte("null"), nil
	}
	return json.Marshal("[redacted]")
}

// Config contains validated deployment configuration.
type Config struct {
	Environment         Environment
	HTTPAddress         string
	PublicURL           url.URL
	SessionCookieSecure bool
	SessionLifetime     time.Duration
	GuestRetention      time.Duration
	UsageRetention      time.Duration
	DatabaseURL         Secret
	SessionSecret       Secret
	MailRelay           Secret
	ResendAPIKey        Secret
	MailFrom            string
	// mailFromConfigured records that MailFrom was set explicitly rather than
	// derived from the public URL.
	mailFromConfigured bool
	TrustedProxy       string
	SSOIssuerURL       string
	SSOClientID        string
	SSOClientSecret    Secret
	SSORedirectURL     string
	SSOAccountURL      string
}

// MailConfigured reports whether outgoing mail is delivered, through an SMTP
// relay or the Resend API.
func (c Config) MailConfigured() bool {
	return c.MailRelay.IsSet() || c.ResendAPIKey.IsSet()
}

// String returns a safe diagnostic summary without secret values.
func (c Config) String() string {
	return fmt.Sprintf(
		"environment=%s http_address=%s public_url=%s session_cookie_secure=%t session_lifetime=%s guest_retention=%s usage_retention=%s database_configured=%t session_secret_configured=%t mail_relay_configured=%t resend_configured=%t trusted_proxy_configured=%t sso_client_configured=%t",
		c.Environment,
		c.HTTPAddress,
		c.PublicURL.String(),
		c.SessionCookieSecure,
		c.SessionLifetime,
		c.GuestRetention,
		c.UsageRetention,
		c.DatabaseURL.IsSet(),
		c.SessionSecret.IsSet(),
		c.MailRelay.IsSet(),
		c.ResendAPIKey.IsSet(),
		c.TrustedProxy != "",
		c.SSOClientSecret.IsSet(),
	)
}

// Load reads configuration through getenv, applies typed defaults, and rejects
// malformed values. Passing getenv explicitly keeps configuration deterministic
// in tests and avoids hidden process-global state.
func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("config: nil environment lookup")
	}

	environment, err := parseEnvironment(getenv(EnvironmentEnv))
	if err != nil {
		return Config{}, err
	}
	httpAddress, err := parseHTTPAddress(valueOrDefault(getenv(HTTPAddressEnv), DefaultHTTPAddress))
	if err != nil {
		return Config{}, err
	}
	publicURL, err := parsePublicURL(valueOrDefault(getenv(PublicURLEnv), DefaultPublicURL))
	if err != nil {
		return Config{}, err
	}
	// An HTTPS public URL means browsers reach the app over TLS, so cookies
	// default to Secure there in every environment; plain-HTTP local
	// development keeps working.
	cookieSecure, err := parseBool(SessionCookieSecureEnv, getenv(SessionCookieSecureEnv), environment == EnvironmentProduction || publicURL.Scheme == "https")
	if err != nil {
		return Config{}, err
	}
	sessionLifetime, err := parseSessionLifetime(getenv(SessionLifetimeEnv))
	if err != nil {
		return Config{}, err
	}
	guestRetention, err := parseRetention(GuestRetentionEnv, getenv(GuestRetentionEnv), DefaultGuestRetention)
	if err != nil {
		return Config{}, err
	}
	usageRetention, err := parseRetention(UsageRetentionEnv, getenv(UsageRetentionEnv), DefaultUsageRetention)
	if err != nil {
		return Config{}, err
	}
	sessionSecret := Secret{value: getenv(SessionSecretEnv)}
	if sessionSecret.IsSet() && len(sessionSecret.value) < MinimumSessionSecretLength {
		return Config{}, fmt.Errorf("%w: %s must contain at least %d characters", ErrWeakSecret, SessionSecretEnv, MinimumSessionSecretLength)
	}
	mailRelay := Secret{value: getenv(MailRelayEnv)}
	if mailRelay.IsSet() {
		parsed, err := url.Parse(strings.TrimSpace(mailRelay.value))
		if err != nil || (parsed.Scheme != "smtp" && parsed.Scheme != "smtps") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("%w: %s must be an smtp:// or smtps:// host address without credentials", ErrInvalidMailRelay, MailRelayEnv)
		}
	}
	resendAPIKey := Secret{value: strings.TrimSpace(getenv(ResendAPIKeyEnv))}
	if resendAPIKey.IsSet() {
		if mailRelay.IsSet() {
			return Config{}, fmt.Errorf("%w: set only one of %s and %s", ErrInvalidMailRelay, MailRelayEnv, ResendAPIKeyEnv)
		}
		if len(resendAPIKey.value) > 256 || strings.ContainsFunc(resendAPIKey.value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return Config{}, fmt.Errorf("%w: %s must be a single token without whitespace", ErrInvalidMailRelay, ResendAPIKeyEnv)
		}
	}
	mailFrom := strings.TrimSpace(getenv(MailFromEnv))
	mailFromConfigured := mailFrom != ""
	if mailFrom == "" {
		mailFrom = "no-reply@" + publicURL.Hostname()
		if !plainEmailAddress(mailFrom) {
			mailFrom = "no-reply@localhost"
		}
	} else if !plainEmailAddress(mailFrom) {
		return Config{}, fmt.Errorf("%w: %s must be a plain email address", ErrInvalidMailRelay, MailFromEnv)
	}
	trustedProxy := strings.TrimSpace(getenv(TrustedProxyEnv))
	if trustedProxy != "" && !validProxyAddress(trustedProxy) {
		return Config{}, fmt.Errorf("%w: %s must be an IP address or CIDR network", ErrInvalidTrustedProxy, TrustedProxyEnv)
	}
	databaseURL := Secret{value: getenv(DatabaseURLEnv)}
	if !databaseURL.IsSet() {
		return Config{}, fmt.Errorf("%w: %s must be set", ErrMissingDatabase, DatabaseURLEnv)
	}

	loaded := Config{
		Environment:         environment,
		HTTPAddress:         httpAddress,
		PublicURL:           publicURL,
		SessionCookieSecure: cookieSecure,
		SessionLifetime:     sessionLifetime,
		GuestRetention:      guestRetention,
		UsageRetention:      usageRetention,
		DatabaseURL:         databaseURL,
		SessionSecret:       sessionSecret,
		MailRelay:           mailRelay,
		ResendAPIKey:        resendAPIKey,
		MailFrom:            mailFrom,
		mailFromConfigured:  mailFromConfigured,
		TrustedProxy:        trustedProxy,
	}
	if err := applySSO(&loaded, getenv); err != nil {
		return Config{}, err
	}
	if err := validateProduction(loaded); err != nil {
		return Config{}, err
	}
	return loaded, nil
}

// DevelopmentRoutes reports whether development-only pages (the week grid
// demo) are served. Only the development environment serves them; test and
// production never do.
func (c Config) DevelopmentRoutes() bool {
	return c.Environment == EnvironmentDevelopment
}

// validateProduction rejects deployment settings that are unsafe to serve with
// in production. It reports every problem at once without echoing values.
func validateProduction(cfg Config) error {
	if cfg.Environment != EnvironmentProduction {
		return nil
	}
	var problems []string
	if !cfg.SessionCookieSecure {
		problems = append(problems, SessionCookieSecureEnv+" must be true")
	}
	if cfg.PublicURL.Scheme != "https" {
		problems = append(problems, PublicURLEnv+" must use https")
	}
	if !cfg.SessionSecret.IsSet() {
		problems = append(problems, SessionSecretEnv+" must be set")
	}
	if !cfg.MailConfigured() {
		problems = append(problems, MailRelayEnv+" or "+ResendAPIKeyEnv+" must be set so mail is delivered")
	}
	// Resend only sends from a verified domain, so the derived default sender
	// (no-reply@ the app's host) would be refused at send time.
	if cfg.ResendAPIKey.IsSet() && !cfg.mailFromConfigured {
		problems = append(problems, MailFromEnv+" must be set to an address on a domain verified in Resend")
	}
	// Everything a browser or the token exchange crosses must use TLS.
	for _, setting := range cfg.ssoURLs() {
		if parsed, err := url.Parse(setting.value); setting.value != "" && (err != nil || parsed.Scheme != "https") {
			problems = append(problems, setting.name+" must use https")
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsafeProductionConfig, strings.Join(problems, "; "))
}

func parseEnvironment(raw string) (Environment, error) {
	environment := Environment(strings.ToLower(strings.TrimSpace(raw)))
	if environment == "" {
		return DefaultEnvironment, nil
	}
	if environment != EnvironmentDevelopment && environment != EnvironmentTest && environment != EnvironmentProduction {
		return "", fmt.Errorf("%w: %s", ErrInvalidEnvironment, EnvironmentEnv)
	}
	return environment, nil
}

func parseHTTPAddress(address string) (string, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("%w: value must include host and port", ErrInvalidHTTPAddress)
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidHTTPAddress)
	}

	return address, nil
}

func parsePublicURL(raw string) (url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return url.URL{}, fmt.Errorf("%w: value must be an http or https URL without credentials or query data", ErrInvalidPublicURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return url.URL{}, fmt.Errorf("%w: scheme must be http or https", ErrInvalidPublicURL)
	}
	return *parsed, nil
}

func parseBool(name, raw string, defaultValue bool) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%w: %s must be true or false", ErrInvalidBoolean, name)
	}
	return value, nil
}

func parseSessionLifetime(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultSessionLifetime, nil
	}
	lifetime, err := time.ParseDuration(raw)
	if err != nil || lifetime <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive duration", ErrInvalidDuration, SessionLifetimeEnv)
	}
	return lifetime, nil
}

func parseRetention(name, raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	period, err := time.ParseDuration(raw)
	if err != nil || period <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive duration", ErrInvalidDuration, name)
	}
	return period, nil
}

// validateHTTPURL requires an absolute http or https URL without credentials,
// query, or fragment, reporting the variable name but never its value.
func validateHTTPURL(kind error, name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: %s must be an absolute http or https URL without credentials or query data", kind, name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s scheme must be http or https", kind, name)
	}
	return nil
}

// applySSO loads the single sign-on settings. This app always signs people in
// through the provider's Hydra, so the issuer and the registered client are
// required.
func applySSO(cfg *Config, getenv func(string) string) error {
	cfg.SSOIssuerURL = strings.TrimSpace(getenv(SSOIssuerURLEnv))
	cfg.SSOClientID = strings.TrimSpace(getenv(SSOClientIDEnv))
	cfg.SSOClientSecret = Secret{value: getenv(SSOClientSecretEnv)}
	cfg.SSORedirectURL = strings.TrimSpace(getenv(SSORedirectURLEnv))
	cfg.SSOAccountURL = strings.TrimSpace(getenv(SSOAccountURLEnv))

	if cfg.SSOIssuerURL == "" || cfg.SSOClientID == "" || !cfg.SSOClientSecret.IsSet() {
		return fmt.Errorf("%w: %s, %s, and %s must be set", ErrInvalidSSO, SSOIssuerURLEnv, SSOClientIDEnv, SSOClientSecretEnv)
	}
	if cfg.SSORedirectURL == "" {
		redirectPath, err := locale.Path(locale.Default, "/oauth/sso/callback")
		if err != nil {
			return fmt.Errorf("%w: cannot derive %s", ErrInvalidSSO, SSORedirectURLEnv)
		}
		cfg.SSORedirectURL = strings.TrimRight(cfg.PublicURL.String(), "/") + redirectPath
	}
	for _, setting := range cfg.ssoURLs() {
		if setting.value == "" {
			continue
		}
		if err := validateHTTPURL(ErrInvalidSSO, setting.name, setting.value); err != nil {
			return err
		}
	}
	return nil
}

type namedURL struct {
	name  string
	value string
}

// ssoURLs lists the single sign-on URLs in a stable order for validation.
func (c Config) ssoURLs() []namedURL {
	return []namedURL{
		{SSOIssuerURLEnv, c.SSOIssuerURL},
		{SSORedirectURLEnv, c.SSORedirectURL},
		{SSOAccountURLEnv, c.SSOAccountURL},
	}
}

func validProxyAddress(raw string) bool {
	if net.ParseIP(raw) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(raw)
	return err == nil
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func plainEmailAddress(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value
}
