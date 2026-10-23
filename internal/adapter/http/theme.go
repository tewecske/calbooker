package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/service"
)

const themeFieldName = "theme"

// ThemeUpdater is the consuming-side port for account theme persistence.
type ThemeUpdater interface {
	UpdateTheme(context.Context, int64, string) error
}

// ThemeUpdate handles an authenticated account theme mutation.
func ThemeUpdate(updater ThemeUpdater) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if updater == nil {
			http.Error(writer, "internal server error", http.StatusInternalServerError)
			return
		}
		principal, ok := middleware.PrincipalFromContext(request.Context())
		if !ok {
			http.Error(writer, "authentication required", http.StatusUnauthorized)
			return
		}
		userID, err := strconv.ParseInt(strings.TrimSpace(principal.ID), 10, 64)
		if err != nil || userID <= 0 {
			http.Error(writer, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := request.ParseForm(); err != nil {
			http.Error(writer, "validation failed", http.StatusUnprocessableEntity)
			return
		}
		if err := updater.UpdateTheme(request.Context(), userID, request.PostFormValue(themeFieldName)); err != nil {
			if errors.Is(err, service.ErrInvalidTheme) {
				http.Error(writer, "validation failed", http.StatusUnprocessableEntity)
				return
			}
			http.Error(writer, "internal server error", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("HX-Trigger", "theme-updated")
		writer.WriteHeader(http.StatusNoContent)
	})
}
