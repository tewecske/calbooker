package middleware

import (
	"net/http"
	"strings"
)

// contentSecurityPolicy allows only same-origin scripts, styles, and requests.
// Inline scripts and styles are forbidden, so an injected <script> does not
// run; frame-ancestors blocks clickjacking. data: images cover CSS icons.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// SecurityHeaders sets browser hardening headers on every response. Dynamic
// responses are marked no-store because they carry per-user content and CSRF
// tokens; embedded static assets stay cacheable.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			header := writer.Header()
			header.Set("Content-Security-Policy", contentSecurityPolicy)
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("X-Frame-Options", "DENY")
			// Confirmation and reset links carry tokens in the URL; never send
			// them to another origin.
			header.Set("Referrer-Policy", "same-origin")
			header.Set("Cross-Origin-Opener-Policy", "same-origin")
			if !strings.HasPrefix(request.URL.Path, "/static/") {
				header.Set("Cache-Control", "no-store")
			}
			next.ServeHTTP(writer, request)
		})
	}
}

// LimitRequestBody caps request bodies so oversized form posts fail instead of
// being buffered in memory.
func LimitRequestBody(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Body != nil {
				request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
			}
			next.ServeHTTP(writer, request)
		})
	}
}
