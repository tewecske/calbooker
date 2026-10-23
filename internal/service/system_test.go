package service

import (
	"strings"
	"testing"
)

func TestSystemInfoServiceSanitizesConnectionAddresses(t *testing.T) {
	info := NewSystemInfoService(SystemInfoSettings{
		Environment:   "production",
		PublicAddress: ":8080",
		PublicURL:     "https://user:password@example.test/app?token=secret#frag",
		MailRelay:     "smtp://relay-user:relay-pass@mail.example.test:587/outbound?key=secret",
		SSOIssuer:     "https://issuer-user:issuer-pass@id.example.test/realm?client_secret=secret",
		TrustedProxy:  " 127.0.0.1 ",
	})
	configuration := info.SystemConfiguration()
	if configuration.SSOIssuer != "https://id.example.test" {
		t.Fatalf("SSOIssuer = %q, want credential-free host", configuration.SSOIssuer)
	}
	if configuration.PublicURL != "https://example.test" {
		t.Fatalf("PublicURL = %q, want credential-free host", configuration.PublicURL)
	}
	if configuration.MailRelay != "smtp://mail.example.test:587" {
		t.Fatalf("MailRelay = %q, want credential-free host", configuration.MailRelay)
	}
	if configuration.TrustedProxy != "127.0.0.1" {
		t.Fatalf("TrustedProxy = %q, want trimmed value", configuration.TrustedProxy)
	}
	summary := configuration.PublicURL + configuration.MailRelay + configuration.SSOIssuer
	for _, secret := range []string{"password", "secret", "relay-pass", "key=", "issuer-pass"} {
		if strings.Contains(summary, secret) {
			t.Fatalf("system configuration leaked %q: %s", secret, summary)
		}
	}
}

func TestSystemInfoServiceHandlesNilReceiver(t *testing.T) {
	var info *SystemInfoService
	if configuration := info.SystemConfiguration(); configuration != (SystemConfiguration{}) {
		t.Fatalf("nil receiver = %+v, want zero value", configuration)
	}
}
