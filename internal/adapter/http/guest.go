package httpadapter

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// registerGuestRoutes registers the guest write and transfer pages. The guest
// upgrade belongs to the sign-on flow (registerAuthRoutes).
func registerGuestRoutes(mux *http.ServeMux, auth *authHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+"/guest/write", auth.guestWrite)
		mux.HandleFunc("POST "+prefix+"/guest/write", auth.guestWrite)
		mux.HandleFunc("GET "+prefix+"/guest/transfer", auth.guestTransfer)
		mux.HandleFunc("POST "+prefix+"/guest/transfer", auth.guestTransfer)
	}
}

func (h *authHandler) guestWrite(writer http.ResponseWriter, request *http.Request) {
	language := recoveryLanguage(request)
	user, authenticated := h.currentUser(request)
	if request.Method == http.MethodGet {
		h.renderGuest(writer, request, "guest-write", user, authenticated && user.IsGuest(), false, "", FormData{})
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if authenticated {
		h.renderGuest(writer, request, "guest-write", user, user.IsGuest(), true, "", FormData{})
		return
	}
	if h.dependencies.GuestCreator == nil || h.dependencies.SessionCookie == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	_ = request.ParseForm()
	theme := strings.TrimSpace(request.PostFormValue("theme"))
	if theme == "" {
		theme = "light"
	}
	result, err := h.dependencies.GuestCreator.Create(request.Context(), requestOrigin(request), theme)
	if err != nil {
		h.renderGuestAlert(writer, request, "guest-write", language, http.StatusUnprocessableEntity, FormData{}, "guest.error.create")
		return
	}
	if err := replaceSession(writer, request, h.dependencies, result.Session.ID); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return
	}
	h.renderGuest(writer, request, "guest-write", result.User, true, true, "", FormData{})
}

func (h *authHandler) guestTransfer(writer http.ResponseWriter, request *http.Request) {
	language := recoveryLanguage(request)
	user, authenticated := h.currentUser(request)
	isGuest := authenticated && user.IsGuest()
	if request.Method == http.MethodGet {
		h.renderGuest(writer, request, "guest-transfer", user, isGuest, false, "", FormData{})
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_ = request.ParseForm()
	action := request.PostFormValue("action")
	if action == "request" {
		if !isGuest || h.dependencies.GuestClaimer == nil {
			h.renderGuestAlert(writer, request, "guest-transfer", language, http.StatusUnprocessableEntity, FormData{}, "guest.error.invalid")
			return
		}
		claim, err := h.dependencies.GuestClaimer.Issue(request.Context(), user.ID)
		if err != nil {
			h.renderGuestAlert(writer, request, "guest-transfer", language, http.StatusUnprocessableEntity, FormData{}, "guest.error.invalid")
			return
		}
		h.renderGuest(writer, request, "guest-transfer", user, true, false, claim.Code, FormData{})
		return
	}
	if action == "redeem" {
		if h.dependencies.GuestRedeemer == nil || h.dependencies.SessionCookie == nil {
			h.renderError(writer, request, http.StatusInternalServerError)
			return
		}
		code := request.PostFormValue("code")
		result, err := h.dependencies.GuestRedeemer.Redeem(request.Context(), requestOrigin(request), code)
		if err != nil {
			form := FormData{Submitted: true, Errors: []FieldError{{Field: "code", Message: h.translate(language, "guest.error.invalid_code")}}}
			h.renderGuestWithStatus(writer, request, "guest-transfer", user, false, false, "", form, http.StatusUnprocessableEntity, Alert{Level: "error", Message: h.translate(language, "guest.error.invalid_code")})
			return
		}
		if err := replaceSession(writer, request, h.dependencies, result.Session.ID); err != nil {
			h.renderError(writer, request, http.StatusInternalServerError)
			return
		}
		h.renderGuest(writer, request, "guest-transfer", result.User, true, true, "", FormData{})
		return
	}
	h.renderGuestAlert(writer, request, "guest-transfer", language, http.StatusUnprocessableEntity, FormData{}, "guest.error.invalid")
}

func (h *authHandler) currentUser(request *http.Request) (service.User, bool) {
	if h.authenticator == nil || h.dependencies.Users == nil {
		return service.User{}, false
	}
	principal, err := h.authenticator.Authenticate(request.Context(), request)
	if err != nil {
		return service.User{}, false
	}
	userID, err := strconv.ParseInt(principal.ID, 10, 64)
	if err != nil {
		return service.User{}, false
	}
	user, err := h.dependencies.Users.FindUserByID(request.Context(), userID)
	return user, err == nil && user.ID == userID
}

func (h *authHandler) renderGuest(writer http.ResponseWriter, request *http.Request, kind string, user service.User, guest, owned bool, code string, form FormData) {
	h.renderGuestWithStatus(writer, request, kind, user, guest, owned, code, form, http.StatusOK)
}

func (h *authHandler) renderGuestWithStatus(writer http.ResponseWriter, request *http.Request, kind string, user service.User, guest, owned bool, code string, form FormData, status int, alerts ...Alert) {
	language := recoveryLanguage(request)
	page := PageData{
		Language:          string(language),
		Title:             h.translate(language, "guest."+strings.TrimPrefix(kind, "guest-")+".title"),
		Heading:           h.translate(language, "guest."+strings.TrimPrefix(kind, "guest-")+".heading"),
		Kind:              kind,
		CSRFToken:         csrfToken(request),
		Form:              form,
		FormAction:        mustLocalePath(language, "/"+strings.ReplaceAll(kind, "guest-", "guest/")),
		FormMethod:        http.MethodPost,
		Template:          "guest",
		FragmentTemplate:  "guest-fragment",
		GuestBanner:       guest,
		GuestOwned:        owned,
		GuestTransferCode: code,
		SignInURL:         mustLocalePath(language, "/sign-in"),
		Alerts:            alerts,
		Labels:            h.formLabels(language),
	}
	// The upgrade page offers single sign-on; this app keeps no passwords.
	page.SSOSignInURL = page.FormAction
	if user.ID > 0 {
		page.Account = &AccountMenu{Label: accountLabel(user), SettingsURL: mustLocalePath(language, "/account/settings"), SignOutURL: mustLocalePath(language, "/sign-out"), Theme: user.Theme, ThemeURL: mustLocalePath(language, "/account/theme")}
	}
	if err := h.renderer.RenderRequestStatus(writer, request, page, status); err != nil {
		h.renderError(writer, request, http.StatusInternalServerError)
	}
}

func (h *authHandler) renderGuestAlert(writer http.ResponseWriter, request *http.Request, kind string, language locale.Code, status int, form FormData, messageID string) {
	h.renderGuestWithStatus(writer, request, kind, service.User{}, false, false, "", form, status, Alert{Level: "error", Message: h.translate(language, messageID)})
}
