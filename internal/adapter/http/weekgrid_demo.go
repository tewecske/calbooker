package httpadapter

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/tewecske/calbooker/internal/adapter/http/middleware"
	"github.com/tewecske/calbooker/internal/locale"
)

// The week grid demo exercises the component in a browser (and in the
// Playwright spec) before any feature uses it. It keeps its items in memory,
// needs no sign-in, and is registered only when Dependencies.DevelopmentRoutes
// is set, which cmd/web does only for CALBOOKER_ENV=development.

const weekGridDemoRoute = "/dev/weekgrid"

// registerWeekGridDemoRoutes registers the development-only demo. It adds
// nothing unless enabled, so production never serves it.
func registerWeekGridDemoRoutes(mux *http.ServeMux, renderer *PageRenderer, enabled bool) {
	if !enabled {
		return
	}
	demo := newWeekGridDemo(renderer)
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("GET "+prefix+weekGridDemoRoute, demo.page)
		mux.HandleFunc("POST "+prefix+weekGridDemoRoute+"/move", demo.move)
		mux.HandleFunc("POST "+prefix+weekGridDemoRoute+"/create", demo.create)
		mux.HandleFunc("POST "+prefix+weekGridDemoRoute+"/reset", demo.reset)
	}
}

// WeekGridDemoView is the demo page state.
type WeekGridDemoView struct {
	Grid     *WeekGridView
	Views    []WeekGridTab
	ResetURL string
}

type weekGridDemoItem struct {
	WeekGridItem
	// Week is "A", "B", or "all", as in the weekly plan.
	Week string
}

type weekGridDemo struct {
	renderer *PageRenderer
	mu       sync.Mutex
	items    []weekGridDemoItem
	created  int
}

func newWeekGridDemo(renderer *PageRenderer) *weekGridDemo {
	return &weekGridDemo{renderer: renderer, items: weekGridDemoSeed()}
}

// weekGridDemoSeed covers the layout cases: an overlapping cluster with a
// touching pair, items crossing the visible range, a warning, a fixed item,
// A/B week items, and one outside the shown hours.
func weekGridDemoSeed() []weekGridDemoItem {
	item := func(id string, weekday, start, end int, resource, label string, variant WeekGridVariant, week string) weekGridDemoItem {
		return weekGridDemoItem{WeekGridItem: WeekGridItem{
			ID: id, Weekday: weekday, StartMinute: start, EndMinute: end, Resource: resource,
			Label: label, Variant: variant, Draggable: true,
		}, Week: week}
	}
	items := []weekGridDemoItem{
		item("u8", 1, 16*60, 17*60+30, "court-1", "U8 Lions", WeekGridPrimary, "all"),
		item("u10", 1, 16*60+30, 18*60, "court-1", "U10 Tigers", WeekGridSecondary, "all"),
		item("u12", 1, 17*60+30, 19*60, "court-2", "U12 Eagles", WeekGridAccent, "all"),
		item("early", 2, 6*60, 8*60, "court-1", "Early swim", WeekGridInfo, "all"),
		item("late", 3, 20*60, 22*60, "court-2", "Late league", WeekGridSuccess, "all"),
		item("warn", 4, 17*60, 18*60+30, "court-1", "U14 Wolves", WeekGridWarning, "all"),
		item("fixed", 5, 18*60, 19*60+30, "court-2", "Club meeting", WeekGridNeutral, "all"),
		item("week-a", 6, 9*60, 10*60+30, "court-1", "Week A camp", WeekGridPrimary, "A"),
		item("week-b", 6, 11*60, 12*60+30, "court-1", "Week B camp", WeekGridSecondary, "B"),
		item("night", 7, 22*60, 23*60, "court-1", "Night run", WeekGridError, "all"),
	}
	items[5].Warning = true
	items[6].Draggable = false
	return items
}

// weekGridDemoOptions is the view chosen through the query string.
type weekGridDemoOptions struct {
	week      string
	firstDay  time.Time
	resources bool
	readOnly  bool
}

func parseWeekGridDemoOptions(request *http.Request) weekGridDemoOptions {
	query := request.URL.Query()
	options := weekGridDemoOptions{week: "A", resources: query.Get("resources") == "1", readOnly: query.Get("readonly") == "1"}
	if query.Get("week") == "B" {
		options.week = "B"
	}
	if from := query.Get("from"); from != "" {
		if day, err := time.Parse(time.DateOnly, from); err == nil {
			options.firstDay = isoMonday(day)
			options.week = ""
		}
	}
	return options
}

func (o weekGridDemoOptions) query() url.Values {
	values := url.Values{}
	if !o.firstDay.IsZero() {
		values.Set("from", o.firstDay.Format(time.DateOnly))
	} else if o.week != "" {
		values.Set("week", o.week)
	}
	if o.resources {
		values.Set("resources", "1")
	}
	if o.readOnly {
		values.Set("readonly", "1")
	}
	return values
}

func weekGridDemoURL(language locale.Code, suffix string, values url.Values) string {
	path := mustLocalePath(language, weekGridDemoRoute+suffix)
	if encoded := values.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func (o weekGridDemoOptions) config(language locale.Code, csrfToken string) WeekGridConfig {
	config := WeekGridConfig{
		ID:          "demo-grid",
		StartMinute: 7 * 60,
		EndMinute:   21 * 60,
		Step:        15,
		Week:        o.week,
		CSRFToken:   csrfToken,
	}
	if o.resources {
		config.Resources = []WeekGridResource{{Key: "court-1", Label: "Court 1"}, {Key: "court-2", Label: "Court 2"}}
	}
	if !o.firstDay.IsZero() {
		config.FirstDay = o.firstDay
		previous, next := o, o
		previous.firstDay = o.firstDay.AddDate(0, 0, -7)
		next.firstDay = o.firstDay.AddDate(0, 0, 7)
		config.PreviousURL = weekGridDemoURL(language, "", previous.query())
		config.NextURL = weekGridDemoURL(language, "", next.query())
	} else {
		weekA, weekB := o, o
		weekA.week, weekB.week = "A", "B"
		config.WeekAURL = weekGridDemoURL(language, "", weekA.query())
		config.WeekBURL = weekGridDemoURL(language, "", weekB.query())
	}
	if !o.readOnly {
		config.MoveURL = weekGridDemoURL(language, "/move", o.query())
		config.CreateURL = weekGridDemoURL(language, "/create", o.query())
	}
	return config
}

// visibleItems returns the items of the chosen week. A dated view shows the
// week-A plan, like the first week of a season part.
func (d *weekGridDemo) visibleItems(language locale.Code, options weekGridDemoOptions) []WeekGridItem {
	week := options.week
	if week == "" {
		week = "A"
	}
	warning, _ := d.renderer.Translate(language, "weekgrid.demo.warning_example")
	var items []WeekGridItem
	for _, item := range d.items {
		if item.Week != "all" && item.Week != week {
			continue
		}
		if item.Warning {
			item.WarningText = warning
		}
		items = append(items, item.WeekGridItem)
	}
	return items
}

func (d *weekGridDemo) page(writer http.ResponseWriter, request *http.Request) {
	language := resolveRequestLanguage(request, "")
	d.respond(writer, request, language, parseWeekGridDemoOptions(request), nil, http.StatusOK)
}

func (d *weekGridDemo) move(writer http.ResponseWriter, request *http.Request) {
	language := resolveRequestLanguage(request, "")
	options := parseWeekGridDemoOptions(request)
	config := options.config(language, "")
	change, err := ParseWeekGridChange(request, config, true)
	if err != nil {
		d.fail(writer, request, language, options, "weekgrid.demo.invalid")
		return
	}
	d.mu.Lock()
	index := slices.IndexFunc(d.items, func(item weekGridDemoItem) bool { return item.ID == change.ItemID })
	if index < 0 || !d.items[index].Draggable {
		d.mu.Unlock()
		d.fail(writer, request, language, options, "weekgrid.demo.not_found")
		return
	}
	item := &d.items[index]
	item.Weekday, item.StartMinute, item.EndMinute = change.Weekday, change.StartMinute, change.EndMinute
	if options.resources {
		item.Resource = change.Resource
	}
	d.mu.Unlock()
	d.succeed(writer, request, language, options, "weekgrid.demo.moved")
}

func (d *weekGridDemo) create(writer http.ResponseWriter, request *http.Request) {
	language := resolveRequestLanguage(request, "")
	options := parseWeekGridDemoOptions(request)
	change, err := ParseWeekGridChange(request, options.config(language, ""), false)
	if err != nil {
		d.fail(writer, request, language, options, "weekgrid.demo.invalid")
		return
	}
	label, err := d.renderer.Translate(language, "weekgrid.demo.new_item")
	if err != nil {
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}
	resource := change.Resource
	if !options.resources {
		resource = "court-1"
	}
	week := demoItemWeek(options.week)
	d.mu.Lock()
	d.created++
	d.items = append(d.items, weekGridDemoItem{WeekGridItem: WeekGridItem{
		ID:          "new-" + strconv.Itoa(d.created),
		Weekday:     change.Weekday,
		StartMinute: change.StartMinute,
		EndMinute:   change.EndMinute,
		Resource:    resource,
		Label:       label + " " + strconv.Itoa(d.created),
		Variant:     WeekGridInfo,
		Draggable:   true,
	}, Week: week})
	d.mu.Unlock()
	d.succeed(writer, request, language, options, "weekgrid.demo.created")
}

func demoItemWeek(week string) string {
	if week == "" {
		return "all"
	}
	return week
}

func (d *weekGridDemo) reset(writer http.ResponseWriter, request *http.Request) {
	language := resolveRequestLanguage(request, "")
	d.mu.Lock()
	d.items, d.created = weekGridDemoSeed(), 0
	d.mu.Unlock()
	d.succeed(writer, request, language, parseWeekGridDemoOptions(request), "weekgrid.demo.reset_done")
}

// succeed re-renders the grid for HTMX and redirects a plain form post back
// to the page.
func (d *weekGridDemo) succeed(writer http.ResponseWriter, request *http.Request, language locale.Code, options weekGridDemoOptions, messageID string) {
	if !IsHTMX(request) {
		http.Redirect(writer, request, weekGridDemoURL(language, "", options.query()), http.StatusSeeOther)
		return
	}
	message, _ := d.renderer.Translate(language, messageID)
	d.respond(writer, request, language, options, []Alert{{Level: "success", Message: message}}, http.StatusOK)
}

// fail answers with the unchanged grid and an error alert (422, which the
// layout's HTMX configuration swaps).
func (d *weekGridDemo) fail(writer http.ResponseWriter, request *http.Request, language locale.Code, options weekGridDemoOptions, messageID string) {
	message, _ := d.renderer.Translate(language, messageID)
	d.respond(writer, request, language, options, []Alert{{Level: "error", Message: message}}, http.StatusUnprocessableEntity)
}

var errWeekGridDemoRender = errors.New("http: week grid demo render failed")

func (d *weekGridDemo) respond(writer http.ResponseWriter, request *http.Request, language locale.Code, options weekGridDemoOptions, alerts []Alert, status int) {
	page, err := d.pageData(request, language, options, alerts)
	if err == nil {
		// A change answers with the grid alone (outer swap of the grid
		// element); page navigation gets the usual full page or fragment.
		if IsHTMX(request) && request.Method == http.MethodPost {
			writer.Header().Add("Vary", "HX-Request")
			err = d.renderer.renderStatus(writer, "weekgrid-demo-swap", page, status)
		} else {
			err = d.renderer.RenderRequestStatus(writer, request, page, status)
		}
	}
	if err != nil {
		http.Error(writer, "internal server error", http.StatusInternalServerError)
	}
}

func (d *weekGridDemo) pageData(request *http.Request, language locale.Code, options weekGridDemoOptions, alerts []Alert) (PageData, error) {
	ids := []string{
		"weekgrid.demo.title", "weekgrid.demo.heading", "weekgrid.demo.description", "weekgrid.demo.views",
		"weekgrid.demo.template_view", "weekgrid.demo.dated_view", "weekgrid.demo.resources",
		"weekgrid.demo.read_only", "weekgrid.demo.reset",
	}
	messages := make(map[string]string, len(ids))
	for _, id := range ids {
		text, err := d.renderer.Translate(language, id)
		if err != nil {
			return PageData{}, errors.Join(errWeekGridDemoRender, err)
		}
		messages[id] = text
	}
	csrfToken, _ := middleware.CSRFTokenFromContext(request.Context())
	d.mu.Lock()
	items := d.visibleItems(language, options)
	d.mu.Unlock()
	grid, err := BuildWeekGrid(options.config(language, csrfToken), items, d.renderer.WeekGridTranslator(language))
	if err != nil {
		return PageData{}, errors.Join(errWeekGridDemoRender, err)
	}

	template, dated, resources, readOnly := options, options, options, options
	template.firstDay, template.week = time.Time{}, demoTemplateWeek(options.week)
	dated.firstDay = isoMonday(time.Now())
	resources.resources = !options.resources
	readOnly.readOnly = !options.readOnly
	page := PageData{
		Language:         string(language),
		Title:            messages["weekgrid.demo.title"],
		Heading:          messages["weekgrid.demo.heading"],
		Description:      messages["weekgrid.demo.description"],
		Kind:             "weekgrid-demo",
		Template:         "weekgrid-demo",
		FragmentTemplate: "weekgrid-demo-fragment",
		SignInURL:        mustLocalePath(language, "/sign-in"),
		CSRFToken:        csrfToken,
		Alerts:           alerts,
		Labels:           map[string]string{"views": messages["weekgrid.demo.views"]},
		WeekGridDemo: &WeekGridDemoView{
			Grid: grid,
			Views: []WeekGridTab{
				{Label: messages["weekgrid.demo.template_view"], URL: weekGridDemoURL(language, "", template.query()), Current: options.firstDay.IsZero()},
				{Label: messages["weekgrid.demo.dated_view"], URL: weekGridDemoURL(language, "", dated.query()), Current: !options.firstDay.IsZero()},
				{Label: messages["weekgrid.demo.resources"], URL: weekGridDemoURL(language, "", resources.query()), Current: options.resources},
				{Label: messages["weekgrid.demo.read_only"], URL: weekGridDemoURL(language, "", readOnly.query()), Current: options.readOnly},
			},
			ResetURL: weekGridDemoURL(language, "/reset", options.query()),
		},
	}
	page.Labels["reset"] = messages["weekgrid.demo.reset"]
	page.Navigation = primaryNavigation(d.renderer, language, false)
	return page, nil
}

func demoTemplateWeek(week string) string {
	if week == "" {
		return "A"
	}
	return week
}
