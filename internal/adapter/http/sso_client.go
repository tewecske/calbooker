package httpadapter

import (
	"errors"
	"net/http"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// ssoBackChannelLogoutPath is registered with the issuer as this client's
// back-channel logout URL. It is fixed and not localized.
const ssoBackChannelLogoutPath = "/oauth/sso/backchannel-logout"

// ssoSignIn renders the client-mode sign-in page: one button that starts the
// issuer's flow. It never shows a password form.
func (h *authHandler) ssoSignIn(writer http.ResponseWriter, request *http.Request) {
	language, ok := requestLanguage(request)
	if !ok {
		h.renderError(writer, request, http.StatusNotFound)
		return
	}
	if h.authenticator != nil {
		if _, err := h.authenticator.Authenticate(request.Context(), request); err == nil {
			h.redirectLocalized(writer, request, language, "/home")
			return
		}
	}
	page := PageData{
		Language:         string(language),
		Title:            h.translate(language, "auth.sign_in.title"),
		Heading:          h.translate(language, "auth.sign_in.heading"),
		Description:      h.translate(language, "auth.sso.description"),
		Kind:             "sign-in",
		CSRFToken:        csrfToken(request),
		SubmitLabel:      h.translate(language, "auth.sso.submit"),
		SignInURL:        h.path(language, "/sign-in"),
		SSOSignInURL:     h.path(language, "/oauth/sso"),
		Navigation:       primaryNavigation(h.renderer, language, false),
		Template:         "auth",
		FragmentTemplate: "auth-fragment",
		Labels:           h.formLabels(language),
	}
	switch request.URL.Query().Get("sso") {
	case "error":
		page.Alerts = append(page.Alerts, Alert{Level: "error", Message: h.translate(language, "sso.error.generic")})
	case "expired":
		page.Alerts = append(page.Alerts, Alert{Level: "error", Message: h.translate(language, "sso.error.expired")})
	}
	h.renderPage(writer, request, page)
}

// ssoStart begins the issuer's authorization flow and binds its state to this
// browser.
func (h *authHandler) ssoStart(writer http.ResponseWriter, request *http.Request) {
	if h.dependencies.SessionCookie == nil || h.dependencies.SSOClient == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	target, state, err := h.dependencies.SSOClient.Start(request.Context())
	if err != nil {
		h.ssoClientFailure(writer, request)
		return
	}
	setOAuthStateCookie(writer, h.dependencies, state)
	http.Redirect(writer, request, target, http.StatusFound)
}

// ssoCallback completes the issuer's flow. Only the browser that started it,
// proven by the state cookie, can finish it.
func (h *authHandler) ssoCallback(writer http.ResponseWriter, request *http.Request) {
	if h.dependencies.SessionCookie == nil || h.dependencies.SSOClient == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	query := request.URL.Query()
	state := query.Get("state")
	bound := oauthStateBound(request, state)
	clearOAuthStateCookie(writer, h.dependencies)
	// A guest reaches the issuer only through the upgrade flow, so failures
	// for a guest browser return to the upgrade page.
	actor, signedIn := h.currentUser(request)
	guest := signedIn && actor.IsGuest()
	if !bound || query.Get("error") != "" {
		h.ssoCallbackFailure(writer, request, guest, nil)
		return
	}
	result, err := h.dependencies.SSOClient.Callback(request.Context(), actor.ID, query.Get("code"), state)
	if err != nil {
		h.ssoCallbackFailure(writer, request, guest, err)
		return
	}
	h.finishSignIn(writer, request, recoveryLanguage(request), result.Session.ID, "/home")
}

// ssoGuestUpgrade offers a signed-in guest one button that signs in at the
// issuer and turns this guest account into that account, keeping its data.
// Starting is a CSRF-protected POST so another site cannot start it.
func (h *authHandler) ssoGuestUpgrade(writer http.ResponseWriter, request *http.Request) {
	language := recoveryLanguage(request)
	user, authenticated := h.currentUser(request)
	isGuest := authenticated && user.IsGuest()
	if request.Method == http.MethodGet {
		var alerts []Alert
		switch request.URL.Query().Get("sso") {
		case "taken":
			alerts = append(alerts, Alert{Level: "error", Message: h.translate(language, "guest.error.sso_taken")})
		case "conflict":
			alerts = append(alerts, Alert{Level: "error", Message: h.translate(language, "guest.error.duplicate_email")})
		case "error":
			alerts = append(alerts, Alert{Level: "error", Message: h.translate(language, "sso.error.generic")})
		}
		h.renderGuestWithStatus(writer, request, "guest-upgrade", user, isGuest, false, "", FormData{}, http.StatusOK, alerts...)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !isGuest || h.dependencies.SessionCookie == nil || h.dependencies.SSOClient == nil {
		h.renderGuestAlert(writer, request, "guest-upgrade", language, formFailureStatus(request, http.StatusUnauthorized), FormData{}, "guest.error.invalid")
		return
	}
	target, state, err := h.dependencies.SSOClient.StartUpgrade(request.Context(), user.ID)
	if err != nil {
		h.redirectLocalized(writer, request, language, "/guest/upgrade?sso=error")
		return
	}
	setOAuthStateCookie(writer, h.dependencies, state)
	http.Redirect(writer, request, target, http.StatusSeeOther)
}

func (h *authHandler) ssoCallbackFailure(writer http.ResponseWriter, request *http.Request, guest bool, err error) {
	if !guest {
		h.ssoClientFailure(writer, request)
		return
	}
	reason := "error"
	switch {
	case errors.Is(err, service.ErrSSOGuestIdentityTaken):
		reason = "taken"
	case errors.Is(err, service.ErrOAuthEmailConflict):
		reason = "conflict"
	}
	h.redirectLocalized(writer, request, recoveryLanguage(request), "/guest/upgrade?sso="+reason)
}

// ssoBackChannelLogout receives the issuer's signed logout token and revokes
// the local sessions bound to its sid. The token signature authenticates the
// request, so this route is exempt from the browser CSRF check.
func (h *authHandler) ssoBackChannelLogout(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if h.dependencies.SSOClient == nil {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if err := request.ParseForm(); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := h.dependencies.SSOClient.BackChannelLogout(request.Context(), request.PostForm.Get("logout_token")); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	writer.WriteHeader(http.StatusOK)
}

func (h *authHandler) ssoClientFailure(writer http.ResponseWriter, request *http.Request) {
	path, err := locale.Path(recoveryLanguage(request), "/sign-in")
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, path+"?sso=error", http.StatusSeeOther)
}
