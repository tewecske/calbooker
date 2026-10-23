package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// CSRFCookieName stores the synchronizer token in the browser.
	CSRFCookieName = "calbooker_csrf"
	// CSRFFieldName is the form field accepted for state-changing requests.
	CSRFFieldName = "_csrf"
	// CSRFHeaderName is the header accepted by HTMX and other clients.
	CSRFHeaderName = "X-CSRF-Token"
	csrfTokenBytes = 32
)

var (
	// ErrCSRFTokenGeneration identifies unavailable secure randomness.
	ErrCSRFTokenGeneration = errors.New("csrf: token generation failed")
	// ErrCSRFTokenInvalid identifies a missing or mismatched request token.
	ErrCSRFTokenInvalid = errors.New("csrf: token invalid")
)

type csrfContextKey struct{}

// CSRFTokenGenerator creates a validated CSRF token.
type CSRFTokenGenerator func() (string, error)

type csrfConfig struct {
	secure    bool
	generator CSRFTokenGenerator
	exempt    map[string]bool
}

// WithCSRFExemptPath exempts one exact path from the token check. Use it only
// for server-to-server endpoints that authenticate every request themselves,
// such as a signed back-channel logout token; browsers never receive a form
// for them.
func WithCSRFExemptPath(path string) CSRFOption {
	return func(config *csrfConfig) {
		if config.exempt == nil {
			config.exempt = map[string]bool{}
		}
		config.exempt[path] = true
	}
}

// CSRFOption configures CSRF middleware.
type CSRFOption func(*csrfConfig)

// WithCSRFSecure controls the Secure cookie attribute. HTTPS requests are
// secure by default; this option is useful when TLS terminates before the app.
func WithCSRFSecure(secure bool) CSRFOption {
	return func(config *csrfConfig) { config.secure = secure }
}

// WithCSRFTokenGenerator injects deterministic randomness for tests.
func WithCSRFTokenGenerator(generator CSRFTokenGenerator) CSRFOption {
	return func(config *csrfConfig) { config.generator = generator }
}

// NewCSRFToken creates a cryptographically random, cookie-safe token.
func NewCSRFToken() (string, error) {
	value := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("%w: random source unavailable", ErrCSRFTokenGeneration)
	}
	return hex.EncodeToString(value), nil
}

// CSRF protects state-changing requests with a double-submit token.
func CSRF(options ...CSRFOption) Middleware {
	config := csrfConfig{generator: NewCSRFToken}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			token, err := requestToken(request)
			if isSafeMethod(request.Method) {
				// Replace a missing or malformed (for example planted) cookie,
				// which would otherwise break every form on the page.
				if err != nil || !validCSRFToken(token) {
					token, err = config.generator()
					if err != nil || !validCSRFToken(token) {
						http.Error(writer, "internal server error", http.StatusInternalServerError)
						return
					}
					setCSRFCookie(writer, token, config.secure || request.TLS != nil)
				}
				withCSRFToken(next, writer, request, token)
				return
			}
			if config.exempt[request.URL.Path] {
				next.ServeHTTP(writer, request)
				return
			}
			if err != nil || !validCSRFToken(token) || !requestHasCSRFToken(request, token) {
				http.Error(writer, "csrf validation failed", http.StatusForbidden)
				return
			}
			withCSRFToken(next, writer, request, token)
		})
	}
}

// CSRFTokenFromContext returns the token rendered into a same-origin form.
func CSRFTokenFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	token, ok := ctx.Value(csrfContextKey{}).(string)
	return token, ok
}

func requestToken(request *http.Request) (string, error) {
	cookie, err := request.Cookie(CSRFCookieName)
	if err != nil {
		return "", err
	}
	return cookie.Value, nil
}

func requestHasCSRFToken(request *http.Request, expected string) bool {
	provided := strings.TrimSpace(request.Header.Get(CSRFHeaderName))
	if provided == "" {
		if err := request.ParseForm(); err != nil {
			return false
		}
		provided = strings.TrimSpace(request.PostFormValue(CSRFFieldName))
	}
	if !validCSRFToken(provided) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func setCSRFCookie(writer http.ResponseWriter, token string, secure bool) {
	http.SetCookie(writer, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func withCSRFToken(next http.Handler, writer http.ResponseWriter, request *http.Request, token string) {
	ctx := context.WithValue(request.Context(), csrfContextKey{}, token)
	next.ServeHTTP(writer, request.WithContext(ctx))
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func validCSRFToken(token string) bool {
	if len(token) != csrfTokenBytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(token)
	return err == nil && len(decoded) == csrfTokenBytes
}
