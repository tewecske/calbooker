package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// LocaleSettingsUpdater persists an account language preference.
type LocaleSettingsUpdater interface {
	Update(context.Context, int64, string) (service.User, error)
}

// AccountThemeUpdater persists an account visual theme.
type AccountThemeUpdater interface {
	UpdateTheme(context.Context, int64, string) error
}

// SettingsRenderer renders localized settings pages and fragments.
type SettingsRenderer struct {
	renderer     *PageRenderer
	dependencies Dependencies
}

// NewSettingsRenderer constructs settings rendering with its page dependencies.
func NewSettingsRenderer(renderer *PageRenderer, dependencies Dependencies) *SettingsRenderer {
	return &SettingsRenderer{renderer: renderer, dependencies: dependencies}
}

// Render writes one settings page or fragment with the given status.
func (r *SettingsRenderer) Translate(language locale.Code, id string) (string, error) {
	if r == nil || r.renderer == nil {
		return "", ErrInvalidPage
	}
	return r.renderer.Translate(language, id)
}

func (r *SettingsRenderer) Render(writer http.ResponseWriter, request *http.Request, page PageData, status int) error {
	if r == nil || r.renderer == nil {
		return ErrInvalidPage
	}
	return r.renderer.RenderRequestStatus(writer, request, page, status)
}

type settingsHandler struct {
	auth          *authHandler
	settings      *SettingsRenderer
	dependencies  Dependencies
	authenticator *SessionAuthenticator
}

// registerSettingsRoutes registers the account settings, language, and theme
// routes.
func registerSettingsRoutes(mux *http.ServeMux, settings *settingsHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/account/settings", settings.page)
		mux.HandleFunc("POST "+prefix+"/account/settings/locale", settings.updateLocale)
		mux.HandleFunc("POST "+prefix+"/account/theme", settings.updateTheme)
	}
}

func newSettingsHandler(renderer *PageRenderer, dependencies Dependencies) *settingsHandler {
	authenticator, _ := NewSessionAuthenticator(dependencies.Users, dependencies.Sessions, dependencies.SessionCookie)
	return &settingsHandler{auth: newAuthHandler(renderer, dependencies), settings: NewSettingsRenderer(renderer, dependencies), dependencies: dependencies, authenticator: authenticator}
}

func (h *settingsHandler) page(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	h.renderSettings(writer, request, language, user, FormData{}, nil, http.StatusOK)
}

func (h *settingsHandler) updateLocale(writer http.ResponseWriter, request *http.Request) {
	user, language, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_ = request.ParseForm()
	if h.dependencies.LocaleSettings == nil {
		h.renderSettings(writer, request, language, user, FormData{}, []Alert{{Level: "error", Message: settingsTranslate(h.settings, language, "settings.error.generic")}}, http.StatusInternalServerError)
		return
	}
	selected := strings.TrimSpace(request.PostFormValue("locale"))
	if _, err := h.dependencies.LocaleSettings.Update(request.Context(), user.ID, selected); err != nil {
		form := FormData{Submitted: true}
		message := "settings.error.generic"
		status := http.StatusUnprocessableEntity
		switch {
		case errors.Is(err, service.ErrInvalidLocale):
			form.Errors = []FieldError{{Field: "locale", Message: settingsTranslate(h.settings, language, "settings.error.invalid_locale")}}
			message = "settings.error.invalid_locale"
		case errors.Is(err, service.ErrOptimisticLockConflict):
			message = "settings.error.conflict"
		case errors.Is(err, service.ErrRecordNotFound):
			message = "settings.error.not_found"
			status = http.StatusNotFound
		}
		h.renderSettings(writer, request, language, user, form, []Alert{{Level: "error", Message: settingsTranslate(h.settings, language, message)}}, status)
		return
	}
	destination, err := locale.Path(locale.Code(selected), "/account/settings")
	if err != nil {
		h.renderSettings(writer, request, language, user, FormData{}, []Alert{{Level: "error", Message: settingsTranslate(h.settings, language, "settings.error.generic")}}, http.StatusInternalServerError)
		return
	}
	h.redirectWithStatus(writer, request, destination, http.StatusSeeOther)
}

func (h *settingsHandler) updateTheme(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.dependencies.ThemeUpdater == nil {
		renderSimpleError(writer, request, http.StatusInternalServerError)
		return
	}
	user, _, ok := h.authenticatedUser(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	if err := h.dependencies.ThemeUpdater.UpdateTheme(request.Context(), user.ID, request.PostFormValue("theme")); err != nil {
		if errors.Is(err, service.ErrInvalidTheme) {
			renderSimpleError(writer, request, http.StatusUnprocessableEntity)
			return
		}
		renderSimpleError(writer, request, http.StatusInternalServerError)
		return
	}
	writer.Header().Set("HX-Trigger", "theme-updated")
	writer.WriteHeader(http.StatusNoContent)
}

func (h *settingsHandler) authenticatedUser(writer http.ResponseWriter, request *http.Request) (service.User, locale.Code, bool) {
	return authenticatePageUser(writer, request, h.authenticator, h.dependencies.Users)
}

func (h *settingsHandler) renderSettings(writer http.ResponseWriter, request *http.Request, language locale.Code, user service.User, form FormData, alerts []Alert, status int) {
	page := PageData{
		Language:         string(language),
		Title:            settingsTranslate(h.settings, language, "settings.title"),
		Heading:          settingsTranslate(h.settings, language, "settings.heading"),
		Kind:             "settings",
		CSRFToken:        csrfToken(request),
		Form:             form,
		Template:         "settings",
		FragmentTemplate: "settings-fragment",
		Alerts:           alerts,
		Labels:           h.auth.formLabels(language),
		Account:          settingsAccountMenu(language, user),
		Navigation:       primaryNavigation(h.auth.renderer, language, true),
		Settings: &SettingsView{
			AccountURL:   h.dependencies.SSOAccountURL,
			Email:        optionalValueString(user.Email),
			Locale:       user.Locale,
			Theme:        user.Theme,
			Languages:    languageOptions(language),
			LocaleAction: localizedPath(language, "/account/settings/locale"),
			ThemeURL:     localizedPath(language, "/account/theme"),
		},
	}
	h.settings.Render(writer, request, page, status)
}

func (h *settingsHandler) redirectWithStatus(writer http.ResponseWriter, request *http.Request, destination string, status int) {
	if IsHTMX(request) {
		writer.Header().Set("HX-Redirect", destination)
		writer.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(writer, request, destination, status)
}

func settingsAccountMenu(language locale.Code, user service.User) *AccountMenu {
	return &AccountMenu{
		Label:       accountLabel(user),
		SettingsURL: mustLocalePath(language, "/account/settings"),
		SignOutURL:  mustLocalePath(language, "/sign-out"),
		Theme:       user.Theme,
		ThemeURL:    mustLocalePath(language, "/account/theme"),
		IsAdmin:     user.IsAdmin,
		AdminURL:    mustLocalePath(language, "/admin"),
	}
}

func languageOptions(current locale.Code) []LanguageOption {
	options := make([]LanguageOption, 0, len(locale.Codes()))
	for _, code := range locale.Codes() {
		options = append(options, LanguageOption{
			Code:    string(code),
			Label:   languageLabel(code),
			Current: code == current,
		})
	}
	return options
}

func languageLabel(code locale.Code) string {
	switch code {
	case locale.Hungarian:
		return "Magyar"
	case locale.English:
		return "English"
	default:
		return string(code)
	}
}

func optionalValueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func settingsTranslate(renderer *SettingsRenderer, language locale.Code, id string) string {
	if renderer == nil {
		return id
	}
	value, err := renderer.Translate(language, id)
	if err != nil {
		return id
	}
	return value
}

func authenticatePageUser(writer http.ResponseWriter, request *http.Request, authenticator *SessionAuthenticator, users UserFinder) (service.User, locale.Code, bool) {
	language := recoveryLanguage(request)
	if authenticator == nil || users == nil {
		renderSimpleError(writer, request, http.StatusInternalServerError)
		return service.User{}, language, false
	}
	principal, err := authenticator.Authenticate(request.Context(), request)
	if errors.Is(err, middleware.ErrUnauthenticated) {
		if IsHTMX(request) {
			writer.Header().Set("HX-Redirect", localizedPath(language, "/sign-in"))
			writer.WriteHeader(http.StatusOK)
			return service.User{}, language, false
		}
		redirectLocalized(writer, request, language, "/sign-in")
		return service.User{}, language, false
	}
	if err != nil {
		renderSimpleError(writer, request, http.StatusInternalServerError)
		return service.User{}, language, false
	}
	userID, err := strconv.ParseInt(principal.ID, 10, 64)
	if err != nil {
		renderSimpleError(writer, request, http.StatusInternalServerError)
		return service.User{}, language, false
	}
	user, err := users.FindUserByID(request.Context(), userID)
	if err != nil || user.ID != userID {
		renderSimpleError(writer, request, http.StatusInternalServerError)
		return service.User{}, language, false
	}
	return user, language, true
}

func localizedPath(language locale.Code, route string) string {
	path, _ := locale.Path(language, route)
	return path
}

func renderSimpleError(writer http.ResponseWriter, _ *http.Request, status int) {
	http.Error(writer, http.StatusText(status), status)
}

func redirectLocalized(writer http.ResponseWriter, request *http.Request, language locale.Code, route string) {
	path, err := locale.Path(language, route)
	if err != nil {
		renderSimpleError(writer, request, http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, path, http.StatusSeeOther)
}
