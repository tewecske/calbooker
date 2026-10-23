package httpadapter

import (
	"net/http"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
)

// about renders the public about page. It credits bundled third-party assets
// and reproduces their license notices.
func about(renderer *PageRenderer, auth *authHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		language := resolveRequestLanguage(request, "")
		messageIDs := []string{
			"about.title",
			"about.heading",
			"about.description",
			"about.icons_heading",
			"about.icons_credit",
			"about.license_summary",
		}
		messages := make(map[string]string, len(messageIDs))
		for _, id := range messageIDs {
			text, err := renderer.Translate(language, id)
			if err != nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}
			messages[id] = text
		}
		page := PageData{
			Language:         string(language),
			Title:            messages["about.title"],
			Heading:          messages["about.heading"],
			Description:      messages["about.description"],
			Kind:             "about",
			Template:         "about",
			FragmentTemplate: "about-fragment",
			SignInURL:        mustLocalePath(language, "/sign-in"),
			Labels: map[string]string{
				"about_icons_heading":   messages["about.icons_heading"],
				"about_icons_credit":    messages["about.icons_credit"],
				"about_license_summary": messages["about.license_summary"],
			},
		}
		page.CSRFToken, _ = middleware.CSRFTokenFromContext(request.Context())
		applySessionState(renderer, auth, request, language, &page)
		if err := renderer.RenderRequest(writer, request, page); err != nil {
			http.Error(writer, "internal server error", http.StatusInternalServerError)
		}
	}
}
