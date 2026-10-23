package service

import (
	"net/url"
	"strings"
	"time"
)

// SystemInfoSettings contains the deployment facts assembled at startup. Every
// field is credential-free; connection addresses are sanitized before use.
type SystemInfoSettings struct {
	Environment          string
	PublicAddress        string
	PublicURL            string
	SecureSessionCookies bool
	MailConfigured       bool
	MailRelay            string
	SessionLifetime      time.Duration
	GuestRetention       time.Duration
	UsageRetention       time.Duration
	MaintenanceInterval  time.Duration
	TrustedProxy         string
	SSOIssuer            string
}

// SystemConfiguration is the safe configuration overview. It never carries
// passwords, hashes, tokens, provider secrets, mail credentials, database
// credentials, or query strings.
type SystemConfiguration struct {
	Environment          string
	PublicAddress        string
	PublicURL            string
	SecureSessionCookies bool
	MailConfigured       bool
	MailRelay            string
	SessionLifetime      time.Duration
	GuestRetention       time.Duration
	UsageRetention       time.Duration
	MaintenanceInterval  time.Duration
	TrustedProxy         string
	SSOIssuer            string
}

// SystemConfigurationProvider supplies the safe deployment summary.
type SystemConfigurationProvider interface {
	SystemConfiguration() SystemConfiguration
}

// SystemInfoService reports safe deployment configuration facts.
type SystemInfoService struct {
	settings SystemInfoSettings
}

// NewSystemInfoService constructs the configuration overview and sanitizes
// every connection address.
func NewSystemInfoService(settings SystemInfoSettings) *SystemInfoService {
	settings.PublicURL = sanitizeURL(settings.PublicURL)
	settings.MailRelay = sanitizeURL(settings.MailRelay)
	settings.SSOIssuer = sanitizeURL(settings.SSOIssuer)
	settings.TrustedProxy = strings.TrimSpace(settings.TrustedProxy)
	return &SystemInfoService{settings: settings}
}

// SystemConfiguration returns a copy of the safe configuration overview.
func (s *SystemInfoService) SystemConfiguration() SystemConfiguration {
	if s == nil {
		return SystemConfiguration{}
	}
	return SystemConfiguration{
		Environment:          s.settings.Environment,
		PublicAddress:        s.settings.PublicAddress,
		PublicURL:            s.settings.PublicURL,
		SecureSessionCookies: s.settings.SecureSessionCookies,
		MailConfigured:       s.settings.MailConfigured,
		MailRelay:            s.settings.MailRelay,
		SessionLifetime:      s.settings.SessionLifetime,
		GuestRetention:       s.settings.GuestRetention,
		UsageRetention:       s.settings.UsageRetention,
		MaintenanceInterval:  s.settings.MaintenanceInterval,
		TrustedProxy:         s.settings.TrustedProxy,
		SSOIssuer:            s.settings.SSOIssuer,
	}
}

// sanitizeURL reduces a connection address to scheme and host. Credentials,
// paths, queries, and fragments are removed so nothing sensitive is displayed.
func sanitizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}
