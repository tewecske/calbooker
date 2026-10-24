package httpadapter

import (
	"net/http"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

type authHandler struct {
	renderer      *PageRenderer
	dependencies  Dependencies
	authenticator *SessionAuthenticator
}

// registerAuthRoutes registers the signed-in home, sign-out, and single
// sign-on routes. Credentials live at the single sign-on provider; this app
// only starts and finishes the issuer's sign-in flow.
func registerAuthRoutes(mux *http.ServeMux, auth *authHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/home", auth.home)
		mux.HandleFunc("POST "+prefix+"/sign-out", auth.signOut)
		mux.HandleFunc("GET "+prefix+"/sign-in", auth.ssoSignIn)
		mux.HandleFunc("GET "+prefix+"/oauth/sso", auth.ssoStart)
		mux.HandleFunc("GET "+prefix+"/oauth/sso/callback", auth.ssoCallback)
		mux.HandleFunc("GET "+prefix+"/guest/upgrade", auth.ssoGuestUpgrade)
		mux.HandleFunc("POST "+prefix+"/guest/upgrade", auth.ssoGuestUpgrade)
	}
	mux.HandleFunc("POST "+ssoBackChannelLogoutPath, auth.ssoBackChannelLogout)
}

func newAuthHandler(renderer *PageRenderer, dependencies Dependencies) *authHandler {
	authenticator, _ := NewSessionAuthenticator(dependencies.Users, dependencies.Sessions, dependencies.SessionCookie)
	return &authHandler{renderer: renderer, dependencies: dependencies, authenticator: authenticator}
}

func (h *authHandler) home(writer http.ResponseWriter, request *http.Request) {
	language, ok := requestLanguage(request)
	if !ok || h.authenticator == nil {
		h.renderError(writer, request, http.StatusUnauthorized)
		return
	}
	user, ok := h.currentUser(request)
	if !ok {
		h.redirectLocalized(writer, request, language, "/sign-in")
		return
	}
	page := PageData{
		Language:         string(language),
		Title:            "Calbooker",
		Heading:          "Your home",
		Kind:             "home",
		Template:         "home",
		FragmentTemplate: "home-fragment",
		CSRFToken:        csrfToken(request),
		Labels:           h.formLabels(language),
		Account: &AccountMenu{
			Label:       accountLabel(user),
			SettingsURL: h.path(language, "/account/settings"),
			SignOutURL:  h.path(language, "/sign-out"),
			Theme:       user.Theme,
			ThemeURL:    h.path(language, "/account/theme"),
			IsAdmin:     user.IsAdmin,
			AdminURL:    h.path(language, "/admin"),
		},
	}
	page.Navigation = primaryNavigation(h.renderer, language, true)
	h.renderPage(writer, request, page)
}

func (h *authHandler) signOut(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.dependencies.SessionCookie == nil || h.dependencies.SessionAdmin == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	issuerLogout := ""
	if sessionID, ok := h.dependencies.SessionCookie.Read(request); ok {
		if h.dependencies.SSOClient != nil {
			// Read the issuer logout URL (it needs this session's ID token
			// hint) before the session is revoked.
			issuerLogout, _ = h.dependencies.SSOClient.SignOutURL(request.Context(), sessionID)
		}
		if err := h.dependencies.SessionAdmin.SignOut(request.Context(), sessionID); err != nil {
			h.renderError(writer, request, http.StatusInternalServerError)
			return
		}
	}
	if err := h.dependencies.SessionCookie.Clear(writer, time.Now()); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	if issuerLogout != "" {
		// Sign-out also ends the single sign-on session, which logs the
		// browser out of the provider and every other client app.
		http.Redirect(writer, request, issuerLogout, http.StatusSeeOther)
		return
	}
	language, ok := requestLanguage(request)
	if !ok {
		language = locale.Default
	}
	h.redirectLocalized(writer, request, language, "/sign-in")
}

func (h *authHandler) formLabels(language locale.Code) map[string]string {
	labels := make(map[string]string, 20)
	for name, id := range map[string]string{
		"transfer_code": "form.transfer_code", "save": "form.save", "request_transfer": "form.request_transfer",
		"redeem": "form.redeem", "sign_in": "form.sign_in", "continue": "form.continue",
		"settings_preferences": "settings.preferences.heading", "settings_language": "settings.preferences.language",
		"settings_theme": "settings.preferences.theme", "settings_theme_toggle": "settings.preferences.theme_toggle",
		"settings_sso_heading": "settings.sso.heading", "settings_sso_help": "settings.sso.help", "settings_sso_manage": "settings.sso.manage",
		"guest_upgrade_sso_help": "guest.upgrade.sso_help", "guest_upgrade_sso_submit": "guest.upgrade.sso_submit",
	} {
		labels[name] = h.translate(language, id)
	}
	return labels
}

// replaceSession ends the browser's previous session, if any, before setting
// the new one, so a signed-in browser never keeps two live sessions and a
// session planted before sign-in (fixation) or a guest session left behind
// cannot be reused. A failed revocation does not block the new sign-in.
func replaceSession(writer http.ResponseWriter, request *http.Request, dependencies Dependencies, sessionID string) error {
	if previous, ok := dependencies.SessionCookie.Read(request); ok && previous != sessionID && dependencies.SessionAdmin != nil {
		_ = dependencies.SessionAdmin.SignOut(request.Context(), previous)
	}
	return dependencies.SessionCookie.Set(writer, sessionID, time.Now())
}

func (h *authHandler) finishSignIn(writer http.ResponseWriter, request *http.Request, language locale.Code, sessionID, destination string) {
	if err := replaceSession(writer, request, h.dependencies, sessionID); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	if IsHTMX(request) {
		// A full browser navigation (not an htmx swap) is required so the
		// header reflects the new session immediately: account menu, avatar,
		// and the authenticated navigation items.
		writer.Header().Set("HX-Redirect", localizedPath(language, destination))
		writer.WriteHeader(http.StatusOK)
		return
	}
	h.redirectLocalized(writer, request, language, destination)
}

func (h *authHandler) renderPage(writer http.ResponseWriter, request *http.Request, page PageData) {
	h.renderPageWithStatus(writer, request, page, http.StatusOK)
}

func (h *authHandler) renderPageWithStatus(writer http.ResponseWriter, request *http.Request, page PageData, status int) {
	if err := h.renderer.RenderRequestStatus(writer, request, page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *authHandler) renderError(writer http.ResponseWriter, request *http.Request, status int) {
	http.Error(writer, http.StatusText(status), status)
}

func (h *authHandler) redirectLocalized(writer http.ResponseWriter, request *http.Request, language locale.Code, route string) {
	path, err := locale.Path(language, route)
	if err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, path, http.StatusSeeOther)
}

func (h *authHandler) path(language locale.Code, route string) string {
	path, _ := locale.Path(language, route)
	return path
}

func (h *authHandler) translate(language locale.Code, id string) string {
	value, err := h.renderer.Translate(language, id)
	if err != nil {
		return id
	}
	return value
}

func requestLanguage(request *http.Request) (locale.Code, bool) {
	language, _, ok := locale.ParsePath(request.URL.Path)
	return language, ok
}

func csrfToken(request *http.Request) string {
	token, _ := middleware.CSRFTokenFromContext(request.Context())
	return token
}

// requestOrigin is the client address used for rate limits, sign-in history,
// and audit records. It never includes the source port, which changes per
// connection and would give every connection a fresh rate-limit budget.
func requestOrigin(request *http.Request) string {
	origin, ok := middleware.ClientIPFromContext(request.Context())
	if !ok {
		origin = middleware.PeerIP(request.RemoteAddr)
	}
	if origin == "" {
		return "unknown"
	}
	return origin
}

func accountLabel(user service.User) string {
	if user.DisplayName != nil && strings.TrimSpace(*user.DisplayName) != "" {
		return *user.DisplayName
	}
	if user.Username != nil && strings.TrimSpace(*user.Username) != "" {
		return *user.Username
	}
	if user.Email != nil {
		return *user.Email
	}
	return "Account"
}
