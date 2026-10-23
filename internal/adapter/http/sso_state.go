package httpadapter

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// oauthStateCookieName binds a single sign-on flow to the browser that
// started it, so a callback URL carrying someone else's state cannot complete
// here.
const oauthStateCookieName = "calbooker_oauth_state"

// setOAuthStateCookie uses SameSite=Lax because the issuer returns the
// browser through a cross-site top-level redirect.
func setOAuthStateCookie(writer http.ResponseWriter, dependencies Dependencies, state string) {
	http.SetCookie(writer, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   int(service.DefaultOAuthStateLifetime / time.Second),
		HttpOnly: true,
		Secure:   dependencies.SessionCookie.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearOAuthStateCookie(writer http.ResponseWriter, dependencies Dependencies) {
	http.SetCookie(writer, &http.Cookie{
		Name:     oauthStateCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   dependencies.SessionCookie.Secure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func oauthStateBound(request *http.Request, state string) bool {
	cookie, err := request.Cookie(oauthStateCookieName)
	return err == nil && state != "" && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) == 1
}

// recoveryLanguage resolves the page language for flows whose URL may not carry
// a language prefix, such as the issuer's callback.
func recoveryLanguage(request *http.Request) locale.Code {
	if language, _, ok := locale.ParsePath(request.URL.Path); ok {
		return language
	}
	return resolveRequestLanguage(request, "")
}
