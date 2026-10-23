package middleware

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultSessionCookieName is the opaque browser session cookie name.
	DefaultSessionCookieName = "calbooker_session"
	// DefaultSessionCookiePath scopes the session cookie to this application.
	DefaultSessionCookiePath = "/"
)

var (
	// ErrInvalidSessionCookieConfig identifies unsafe or incomplete cookie settings.
	ErrInvalidSessionCookieConfig = errors.New("middleware: invalid session cookie config")
	// ErrInvalidSessionCookieValue identifies a value that cannot be safely sent as a cookie.
	ErrInvalidSessionCookieValue = errors.New("middleware: invalid session cookie value")
)

// SessionCookieConfig controls browser session-cookie attributes.
type SessionCookieConfig struct {
	Name     string
	Path     string
	Secure   bool
	Lifetime time.Duration
	SameSite http.SameSite
}

// SessionCookie writes and reads secure session cookies. HttpOnly is always
// enabled because browser scripts must never read session credentials.
type SessionCookie struct {
	name     string
	path     string
	secure   bool
	lifetime time.Duration
	sameSite http.SameSite
}

// NewSessionCookie validates cookie settings and applies safe defaults for
// name, path, and SameSite.
func NewSessionCookie(config SessionCookieConfig) (*SessionCookie, error) {
	if config.Name == "" {
		config.Name = DefaultSessionCookieName
	}
	if config.Path == "" {
		config.Path = DefaultSessionCookiePath
	}
	if config.SameSite == 0 || config.SameSite == http.SameSiteDefaultMode {
		config.SameSite = http.SameSiteLaxMode
	}
	if config.Lifetime < time.Second || !validCookieName(config.Name) || !validCookiePath(config.Path) || !validSameSite(config.SameSite) {
		return nil, ErrInvalidSessionCookieConfig
	}
	return &SessionCookie{
		name:     config.Name,
		path:     config.Path,
		secure:   config.Secure,
		lifetime: config.Lifetime,
		sameSite: config.SameSite,
	}, nil
}

// Set writes an HttpOnly session cookie with fixed expiry attributes.
func (c *SessionCookie) Set(writer http.ResponseWriter, sessionID string, now time.Time) error {
	if c == nil || writer == nil || !validSessionCookieValue(sessionID) || now.IsZero() {
		return ErrInvalidSessionCookieValue
	}
	cookie := c.cookie(sessionID, now.Add(c.lifetime))
	if err := cookie.Valid(); err != nil {
		return ErrInvalidSessionCookieValue
	}
	http.SetCookie(writer, &cookie)
	return nil
}

// Read returns the session credential when request contains this cookie.
func (c *SessionCookie) Read(request *http.Request) (string, bool) {
	if c == nil || request == nil {
		return "", false
	}
	cookie, err := request.Cookie(c.name)
	if err != nil || !validSessionCookieValue(cookie.Value) {
		return "", false
	}
	return cookie.Value, true
}

// Secure reports whether cookies for this deployment require HTTPS.
func (c *SessionCookie) Secure() bool {
	return c != nil && c.secure
}

// Clear expires the browser session cookie immediately.
func (c *SessionCookie) Clear(writer http.ResponseWriter, now time.Time) error {
	if c == nil || writer == nil || now.IsZero() {
		return ErrInvalidSessionCookieValue
	}
	cookie := c.cookie("", time.Unix(1, 0))
	cookie.MaxAge = -1
	http.SetCookie(writer, &cookie)
	return nil
}

func (c *SessionCookie) cookie(value string, expires time.Time) http.Cookie {
	return http.Cookie{
		Name:     c.name,
		Value:    value,
		Path:     c.path,
		Expires:  expires,
		MaxAge:   int(c.lifetime / time.Second),
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: c.sameSite,
	}
}

func validCookieName(name string) bool {
	if name == "" {
		return false
	}
	cookie := http.Cookie{Name: name}
	return cookie.Valid() == nil
}

func validCookiePath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.ContainsAny(path, ";\r\n")
}

func validSessionCookieValue(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] >= 0x7f || value[index] == ';' || value[index] == '\\' || value[index] == '"' {
			return false
		}
	}
	return true
}

func validSameSite(sameSite http.SameSite) bool {
	return sameSite == http.SameSiteLaxMode || sameSite == http.SameSiteStrictMode || sameSite == http.SameSiteNoneMode
}
