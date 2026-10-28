package demoseed

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrRefused means the seed will not wipe the configured database.
var ErrRefused = errors.New("demo seed refused")

// CheckTarget refuses production and non-local databases and returns the
// database name that will be wiped.
func CheckTarget(databaseURL, environment string) (string, error) {
	if strings.EqualFold(environment, "production") {
		return "", fmt.Errorf("%w: CALBOOKER_ENV is production", ErrRefused)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: the database URL is not a URL with a host", ErrRefused)
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return "", fmt.Errorf("%w: database host %q is not local (localhost or 127.0.0.1)", ErrRefused, parsed.Hostname())
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && !ip.IsLoopback() {
		return "", fmt.Errorf("%w: database host %q is not loopback", ErrRefused, parsed.Hostname())
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" {
		return "", fmt.Errorf("%w: the database URL names no database", ErrRefused)
	}
	if name == "goweb" {
		return "", fmt.Errorf("%w: refusing to wipe goweb's own database", ErrRefused)
	}
	return name, nil
}

// GowebDatabaseURL returns databaseURL pointing at goweb's database on the
// same server.
func GowebDatabaseURL(databaseURL string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = "/goweb"
	return parsed.String(), nil
}
