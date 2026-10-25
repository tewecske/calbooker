// Package httpadapter provides the HTTP transport adapter.
package httpadapter

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
	staticassets "github.com/tewecske/calbooker/static"
)

// NewHandler builds the HTTP handler for public foundation routes.
func NewHandler() http.Handler {
	return NewRouter()
}

// NewRouter builds public routes and applies shared transport boundaries.
func NewRouter(extra ...middleware.Middleware) http.Handler {
	return newRouter(slog.Default(), nil, extra...)
}

// NewRouterWithServices builds public routes while retaining application
// dependencies for future feature handlers.
func NewRouterWithServices(services any, extra ...middleware.Middleware) http.Handler {
	return newRouterWithServices(slog.Default(), nil, services, extra...)
}

// NewLoggedRouter builds routes with structured request logging.
func NewLoggedRouter(logger *Logger, extra ...middleware.Middleware) http.Handler {
	return NewLoggedRouterWithServices(logger, nil, extra...)
}

// NewLoggedRouterWithServices retains the application graph at HTTP
// composition time. Feature handlers will consume its ports as routes land;
// this foundation still exposes only public routes.
func NewLoggedRouterWithServices(logger *Logger, services any, extra ...middleware.Middleware) http.Handler {
	if logger == nil {
		return NewRouterWithServices(services, extra...)
	}
	return newRouterWithServices(logger.Application(), logger, services, extra...)
}

func newRouter(recoveryLogger *slog.Logger, requestLogger middleware.RequestLogger, extra ...middleware.Middleware) http.Handler {
	return newRouterWithServices(recoveryLogger, requestLogger, nil, extra...)
}

func newRouterWithServices(recoveryLogger *slog.Logger, requestLogger middleware.RequestLogger, services any, extra ...middleware.Middleware) http.Handler {
	mux := http.NewServeMux()
	dependencies, _ := services.(Dependencies)
	renderer, err := NewPageRenderer()
	if err != nil {
		panic(err)
	}
	auth := newAuthHandler(renderer, dependencies)
	// One call per area. Each register<Area>Routes function lives next to
	// the area's handlers, so feature branches add routes in their own files.
	registerCoreRoutes(mux, renderer, auth)
	registerAboutRoutes(mux, renderer, auth)
	registerAuthRoutes(mux, auth)
	registerGuestRoutes(mux, auth)
	registerSettingsRoutes(mux, newSettingsHandler(renderer, dependencies))
	registerGroupRoutes(mux, newGroupHandler(renderer, dependencies))
	registerAdminRoutes(mux, newAdminHandler(renderer, dependencies))
	registerWeekGridDemoRoutes(mux, renderer, dependencies.DevelopmentRoutes)

	shared := []middleware.Middleware{
		middleware.WithRequestID(nil),
		middleware.WithRequestInfo(),
		middleware.ClientIP(dependencies.TrustedProxy),
		middleware.SecurityHeaders(),
		middleware.Recover(recoveryLogger),
	}
	if dependencies.Tracer != nil {
		shared = append(shared, middleware.Span(dependencies.Tracer, NormalizeUsageRoute))
	}
	if dependencies.SecurityLogger != nil {
		shared = append(shared, middleware.SecurityEvents(dependencies.SecurityLogger, NormalizeUsageRoute))
	}
	if dependencies.Usage != nil {
		shared = append(shared, middleware.Usage(dependencies.Usage, NormalizeUsageRoute))
	}
	// Forms are small; the cap bounds memory for form and multipart parsing.
	shared = append(shared, middleware.LimitRequestBody(maxRequestBodyBytes))
	// Reject cross-origin state-changing browser requests (Sec-Fetch-Site and
	// Origin checks) before the CSRF token check. This also blocks sibling
	// subdomains, which SameSite treats as same-site.
	shared = append(shared, http.NewCrossOriginProtection().Handler)
	// Hydra posts signed logout tokens server to server; the token signature,
	// not a browser token, authenticates the request.
	shared = append(shared, middleware.CSRF(
		middleware.WithCSRFSecure(dependencies.SessionCookie.Secure()),
		middleware.WithCSRFExemptPath(ssoBackChannelLogoutPath),
	))
	if requestLogger != nil {
		shared = append(shared, middleware.Log(requestLogger))
	}
	shared = append(shared, extra...)
	handler, err := middleware.Chain(mux, shared...)
	if err != nil {
		panic(err)
	}
	return handler
}

// registerCoreRoutes registers the language redirect, the localized home and
// not-found fallbacks, static assets, and the health check.
func registerCoreRoutes(mux *http.ServeMux, renderer *PageRenderer, auth *authHandler) {
	mux.HandleFunc("GET /", defaultLocale)
	mux.HandleFunc("GET /{language}", home(renderer, auth))
	mux.HandleFunc("GET /{language}/{path...}", localized(renderer, auth))
	mux.Handle("GET /static/{path...}", http.StripPrefix("/static/", http.FileServer(http.FS(staticassets.Files()))))
	mux.HandleFunc("GET /healthz", health)
}

// localizedPrefixes returns the path prefix ("/en", "/hu", ...) of every
// supported language. Areas register each localized route once per prefix.
func localizedPrefixes() []string {
	codes := locale.Codes()
	prefixes := make([]string, 0, len(codes))
	for _, language := range codes {
		prefixes = append(prefixes, "/"+string(language))
	}
	return prefixes
}

func defaultLocale(writer http.ResponseWriter, request *http.Request) {
	language := resolveRequestLanguage(request, "")
	path, err := locale.Path(language, "/")
	if err != nil {
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, path, http.StatusFound)
}

const localeCookieName = "calbooker_locale"

// maxRequestBodyBytes bounds every request body; the largest forms are a few
// kilobytes.
const maxRequestBodyBytes = 256 << 10

func resolveRequestLanguage(request *http.Request, accountLanguage locale.Code) locale.Code {
	var browserLanguage locale.Code
	if cookie, err := request.Cookie(localeCookieName); err == nil {
		browserLanguage = locale.Code(cookie.Value)
	}
	var urlLanguage locale.Code
	if language, _, ok := locale.ParsePath(request.URL.Path); ok {
		urlLanguage = language
	}
	language, _ := locale.Resolve(locale.Preferences{
		URL:            urlLanguage,
		Account:        accountLanguage,
		Browser:        browserLanguage,
		AcceptLanguage: request.Header.Get("Accept-Language"),
	})
	return language
}

func home(renderer *PageRenderer, auth *authHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		language, route, ok := locale.ParsePath(request.URL.Path)
		if !ok || route != "/" {
			renderNotFound(renderer, writer, request)
			return
		}
		if !strings.HasSuffix(request.URL.Path, "/") {
			path, err := locale.Path(language, "/")
			if err != nil {
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}
			http.Redirect(writer, request, path, http.StatusFound)
			return
		}

		page := PageData{
			Language:         string(language),
			Title:            "Calbooker",
			Heading:          "Welcome to Calbooker",
			Kind:             "home",
			Template:         "home",
			FragmentTemplate: "home-fragment",
			SignInURL:        mustLocalePath(language, "/sign-in"),
			Labels:           auth.formLabels(language),
			Description:      translateOrEmpty(renderer, language, "home.description"),
		}
		page.CSRFToken, _ = middleware.CSRFTokenFromContext(request.Context())
		if user, ok := applySessionState(renderer, auth, request, language, &page); ok {
			page.Home = homeView(renderer, auth.dependencies, request, language, user)
		}
		if err := renderer.RenderRequest(writer, request, page); err != nil {
			http.Error(writer, "internal server error", http.StatusInternalServerError)
		}
	}
}

// applySessionState fills the shared header state so public pages reflect the
// visitor's session instead of always advertising anonymous navigation.
func applySessionState(renderer *PageRenderer, auth *authHandler, request *http.Request, language locale.Code, page *PageData) (service.User, bool) {
	if user, ok := auth.currentUser(request); ok {
		page.Account = settingsAccountMenu(language, user)
		page.Navigation = primaryNavigation(renderer, language, true)
		return user, true
	}
	page.Navigation = primaryNavigation(renderer, language, false)
	return service.User{}, false
}

// primaryNavigation builds the localized primary navigation. Authenticated
// clients see their account destinations; anonymous clients see sign-in.
func primaryNavigation(renderer *PageRenderer, language locale.Code, authenticated bool) []NavigationItem {
	defs := []navigationDefinition{
		{messageID: "nav.home", route: "/"},
		{messageID: "nav.about", route: "/about"},
	}
	if authenticated {
		defs = append(defs, navigationDefinition{messageID: "groups.nav", route: "/groups"})
	}
	navigation := make([]NavigationItem, 0, len(defs))
	for _, def := range defs {
		label, err := renderer.Translate(language, def.messageID)
		if err != nil {
			continue
		}
		navigation = append(navigation, NavigationItem{Label: label, URL: mustLocalePath(language, def.route)})
	}
	return navigation
}

// navbarLanguageOptions builds the site-wide language picker, linking each
// supported language to the equivalent URL for the current page. Query
// strings are dropped rather than carried over: some carry one-time secrets
// (confirmation and password-reset tokens, invite codes) that must never be
// echoed into a link on the rendered page.
func navbarLanguageOptions(current locale.Code, request *http.Request) []LanguageOption {
	_, route, ok := locale.ParsePath(request.URL.Path)
	if !ok {
		route = "/"
	}
	options := make([]LanguageOption, 0, len(locale.Codes()))
	for _, code := range locale.Codes() {
		path, err := locale.Path(code, route)
		if err != nil {
			continue
		}
		options = append(options, LanguageOption{
			Code:    string(code),
			Label:   languageLabel(code),
			URL:     path,
			Current: code == current,
		})
	}
	return options
}

type navigationDefinition struct {
	messageID string
	route     string
}

func mustLocalePath(language locale.Code, route string) string {
	path, _ := locale.Path(language, route)
	return path
}

func localized(renderer *PageRenderer, auth *authHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		_, route, ok := locale.ParsePath(request.URL.Path)
		if ok && route == "/" {
			home(renderer, auth).ServeHTTP(writer, request)
			return
		}
		renderNotFound(renderer, writer, request)
	}
}

func renderNotFound(renderer *PageRenderer, writer http.ResponseWriter, request *http.Request) {
	language, _, ok := locale.ParsePath(request.URL.Path)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	if err := renderer.RenderState(writer, request, language, PageStateNotFound); err != nil {
		http.Error(writer, "internal server error", http.StatusInternalServerError)
	}
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
