package httpadapter

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/locale"
)

// The week grid is a reusable, server-rendered calendar week: seven ISO
// weekday columns (optionally split into resource columns), time rows of a
// fixed step, and items laid out in side-by-side lanes where they overlap.
//
// A feature handler builds one like this:
//
//	grid, err := BuildWeekGrid(WeekGridConfig{
//		ID: "plan-grid", StartMinute: 7 * 60, EndMinute: 21 * 60, Step: 15,
//		MoveURL: moveURL, CSRFToken: token,
//	}, items, renderer.WeekGridTranslator(language))
//
// and renders it with {{template "weekgrid" .}} where the dot is the
// *WeekGridView. Features only describe items (WeekGridItem); rows, lanes,
// and CSS placement classes are this file's business. Placement uses the
// generated wg-* classes from static/src/app.css because the
// Content-Security-Policy forbids style attributes.
//
// Editing: a grid with MoveURL makes draggable items movable (drag the item)
// and resizable (drag its bottom edge); a grid with CreateURL lets people add
// an item by clicking an empty slot. Both also get a keyboard form. Each
// request is an HTMX POST (CSRF header included) with the form fields
// item, weekday, start, end, resource, and week; ParseWeekGridChange
// validates and snaps them. The handler applies the change and responds with
// the re-rendered grid (outer swap of the grid element) plus out-of-band
// alerts; a rejected change answers 422 with the unchanged grid and an alert.
// The server is the source of truth: the browser never keeps a change the
// server did not render.

const (
	// weekGridMaxRows is the largest number of time rows a grid can have;
	// static/src/app.css generates row classes up to this number.
	weekGridMaxRows = 96
	// weekGridLaneColumns is the number of CSS grid columns inside each day
	// (or resource) column; lanes divide it between overlapping items.
	weekGridLaneColumns = 12
	// WeekGridMaxLanes is the most side-by-side lanes a column shows. Items
	// beyond it share the last lane (and overlap visually).
	WeekGridMaxLanes = weekGridLaneColumns
	// WeekGridMaxResources is the most resource columns inside one day.
	WeekGridMaxResources = 8
	minutesPerDay        = 24 * 60
)

// weekGridSteps are the supported row steps in minutes. Each has a
// wg-step-<n> class in static/src/app.css.
var weekGridSteps = []int{5, 10, 15, 20, 30, 60}

var (
	// ErrInvalidWeekGrid identifies a grid configuration or item that cannot
	// be laid out. It is a programming error, not a user error.
	ErrInvalidWeekGrid = errors.New("http: invalid week grid")
	// ErrInvalidWeekGridChange identifies a move, resize, or create request
	// that does not fit the grid. Answer it with 422 and the unchanged grid.
	ErrInvalidWeekGridChange = errors.New("http: invalid week grid change")
)

// WeekGridVariant names an item color. Unknown variants render as neutral.
type WeekGridVariant string

const (
	WeekGridNeutral   WeekGridVariant = "neutral"
	WeekGridPrimary   WeekGridVariant = "primary"
	WeekGridSecondary WeekGridVariant = "secondary"
	WeekGridAccent    WeekGridVariant = "accent"
	WeekGridInfo      WeekGridVariant = "info"
	WeekGridSuccess   WeekGridVariant = "success"
	WeekGridWarning   WeekGridVariant = "warning"
	WeekGridError     WeekGridVariant = "error"
)

func (v WeekGridVariant) class() string {
	switch v {
	case WeekGridPrimary, WeekGridSecondary, WeekGridAccent, WeekGridInfo, WeekGridSuccess, WeekGridWarning, WeekGridError:
		return "wg-item-" + string(v)
	default:
		return "wg-item-neutral"
	}
}

// WeekGridResource is one column inside each day, for example a court.
type WeekGridResource struct {
	Key   string
	Label string
}

// WeekGridConfig describes the grid's frame. Zero values select defaults
// where noted.
type WeekGridConfig struct {
	// ID is the grid element's id, unique on the page: a lowercase letter
	// followed by lowercase letters, digits, or hyphens.
	ID string
	// StartMinute and EndMinute bound the visible time range in minutes
	// after midnight; both are multiples of Step and the range has at most
	// 96 rows.
	StartMinute int
	EndMinute   int
	// Step is the row height in minutes: 5, 10, 15, 20, 30, or 60.
	Step int
	// Weekdays lists the ISO weekdays (1 = Monday) to show, in order.
	// Empty shows Monday to Sunday.
	Weekdays []int
	// Resources splits every day into these columns. Empty keeps one
	// column per day; items then leave Resource empty.
	Resources []WeekGridResource
	// FirstDay, when set, makes a dated view: it is the date of the first
	// shown weekday's week (any day of that ISO week works) and day headers
	// show dates.
	FirstDay time.Time
	// PreviousURL and NextURL link to the neighboring weeks of a dated view.
	PreviousURL string
	NextURL     string
	// Week is the current week of an alternating plan ("A" or "B"), sent
	// back with every change. WeekAURL and WeekBURL show the A/B switch.
	Week     string
	WeekAURL string
	WeekBURL string
	// MoveURL receives moves and resizes; CreateURL receives new items.
	// Both empty make the grid read only.
	MoveURL   string
	CreateURL string
	// DefaultDuration is the length of an item created by clicking a slot
	// (default 60 minutes).
	DefaultDuration int
	// CSRFToken goes into the keyboard forms for requests without HTMX.
	CSRFToken string
	// DragHint replaces the standard hint under an editable grid, for grids
	// that do not support every gesture.
	DragHint string
}

// WeekGridItem is one thing on the grid. Features fill it from their own
// records; IDs are opaque strings echoed back in change requests.
type WeekGridItem struct {
	ID          string
	Weekday     int
	StartMinute int
	EndMinute   int
	// Resource is the WeekGridResource.Key of the item's column when the
	// grid has resources.
	Resource string
	Label    string
	// Detail is an optional second line, for example a coach name.
	Detail  string
	Variant WeekGridVariant
	// Draggable items can be moved and resized when the grid has a MoveURL.
	Draggable bool
	// Warning marks an item that breaks a rule; WarningText explains it.
	Warning     bool
	WarningText string
	// URL, when set, turns the item's label into a link that covers the
	// item (read-only grids, for example to an event page).
	URL string
	// Muted shows the item faded with its label struck through, for
	// example a cancelled event.
	Muted bool
	// Badges are short texts shown as small badges on the item, for example
	// "Cancelled" or the viewer's sign-up status.
	Badges []string
	// Background items are drawn behind the other items of their column at
	// the full column width, take no lane, and cannot be moved or clicked
	// through, for example the resource slots a training plan fills.
	Background bool
}

// WeekGridView is the render-ready grid for the "weekgrid" template.
type WeekGridView struct {
	ID              string
	StartMinute     int
	EndMinute       int
	Step            int
	Rows            int
	DefaultDuration int
	Week            string
	MoveURL         string
	CreateURL       string
	CSRFToken       string
	Editable        bool
	HasResources    bool
	// BoardClass and RowsClass carry the generated column and row classes.
	BoardClass   string
	RowsClass    string
	DaySpanClass string
	StepClass    string
	// HourClass shifts the column's hour lines when the range starts off
	// the hour; empty otherwise.
	HourClass   string
	RangeLabel  string
	PreviousURL string
	NextURL     string
	WeekTabs    []WeekGridTab
	Days        []WeekGridDayView
	TimeLabels  []WeekGridTimeLabel
	// Hidden lists items that fall outside the shown days, resources, or
	// hours, so nothing silently disappears.
	Hidden []WeekGridItemView
	// MovableItems are the placed items the keyboard move form offers.
	MovableItems []WeekGridItemView
	Options      WeekGridOptions
	// MoveFields and CreateFields feed the keyboard forms' shared fields.
	MoveFields   WeekGridFieldSet
	CreateFields WeekGridFieldSet
	Labels       WeekGridLabels
	// DragHint is the hint under an editable grid.
	DragHint string
}

// WeekGridFieldSet is the dot of the weekgrid-target-fields template.
type WeekGridFieldSet struct {
	Prefix string
	Grid   *WeekGridView
}

// WeekGridTab is one entry of the A/B week switch.
type WeekGridTab struct {
	Label   string
	URL     string
	Current bool
}

// WeekGridDayView is one shown weekday.
type WeekGridDayView struct {
	Weekday   int
	Name      string
	ShortName string
	DateLabel string
	Columns   []WeekGridColumnView
}

// WeekGridColumnView is one day column, or one resource inside a day.
type WeekGridColumnView struct {
	Weekday       int
	Resource      string
	ResourceLabel string
	Label         string
	Items         []WeekGridItemView
}

// WeekGridTimeLabel is one time label in the gutter.
type WeekGridTimeLabel struct {
	Label    string
	RowClass string
}

// WeekGridItemView is a placed item.
type WeekGridItemView struct {
	WeekGridItem
	DayName        string
	TimeLabel      string
	ResourceLabel  string
	PlacementClass string
	VariantClass   string
	// Movable is Draggable on a grid that accepts moves.
	Movable      bool
	ClippedStart bool
	ClippedEnd   bool
	Lane         int
	Lanes        int
	Row          int
	RowSpan      int
}

// WeekGridOptions are the choices of the keyboard forms.
type WeekGridOptions struct {
	Days      []WeekGridOption
	Resources []WeekGridOption
	Starts    []WeekGridOption
	Ends      []WeekGridOption
}

// WeekGridOption is one select option.
type WeekGridOption struct {
	Value string
	Label string
}

// WeekGridLabels are the grid's localized texts.
type WeekGridLabels struct {
	Region       string
	Time         string
	WeekSwitch   string
	Previous     string
	Next         string
	Warning      string
	Hidden       string
	StartsBefore string
	EndsAfter    string
	DragHint     string
	Keyboard     string
	MoveHeading  string
	CreateHead   string
	Item         string
	Day          string
	Resource     string
	Start        string
	End          string
	MoveSubmit   string
	CreateSubmit string
	ResizeHandle string
}

// WeekGridTranslator resolves a weekgrid catalog message.
type WeekGridTranslator func(id string, values map[string]string) (string, error)

// WeekGridTranslator returns the catalog lookup BuildWeekGrid needs.
func (r *PageRenderer) WeekGridTranslator(language locale.Code) WeekGridTranslator {
	return func(id string, values map[string]string) (string, error) {
		if r == nil || r.catalogs == nil {
			return "", fmt.Errorf("%w: renderer is nil", ErrInvalidPage)
		}
		return r.catalogs.Translate(language, id, values)
	}
}

// WeekGridChange is a validated move, resize, or create request. Minutes are
// snapped to the grid step.
type WeekGridChange struct {
	ItemID      string
	Weekday     int
	StartMinute int
	EndMinute   int
	Resource    string
	Week        string
}

// ParseWeekGridChange reads and validates a change request against the grid
// the client was shown. requireItem is true for moves and resizes. Start and
// end snap to the nearest step; the start must be inside the visible range
// and the end after it (an end past the visible range is allowed, up to
// midnight).
func ParseWeekGridChange(request *http.Request, config WeekGridConfig, requireItem bool) (WeekGridChange, error) {
	if err := config.validate(); err != nil {
		return WeekGridChange{}, err
	}
	if err := request.ParseForm(); err != nil {
		return WeekGridChange{}, fmt.Errorf("%w: unreadable form", ErrInvalidWeekGridChange)
	}
	form := request.PostForm
	change := WeekGridChange{
		ItemID:   strings.TrimSpace(form.Get("item")),
		Resource: strings.TrimSpace(form.Get("resource")),
		Week:     strings.TrimSpace(form.Get("week")),
	}
	if requireItem && (change.ItemID == "" || len(change.ItemID) > 64) {
		return WeekGridChange{}, fmt.Errorf("%w: item", ErrInvalidWeekGridChange)
	}
	if !requireItem {
		change.ItemID = ""
	}
	weekday, err := strconv.Atoi(form.Get("weekday"))
	if err != nil || !slices.Contains(config.weekdays(), weekday) {
		return WeekGridChange{}, fmt.Errorf("%w: weekday", ErrInvalidWeekGridChange)
	}
	change.Weekday = weekday
	if len(config.Resources) == 0 {
		if change.Resource != "" {
			return WeekGridChange{}, fmt.Errorf("%w: resource", ErrInvalidWeekGridChange)
		}
	} else if !slices.ContainsFunc(config.Resources, func(r WeekGridResource) bool { return r.Key == change.Resource }) {
		return WeekGridChange{}, fmt.Errorf("%w: resource", ErrInvalidWeekGridChange)
	}
	if change.Week != config.Week {
		return WeekGridChange{}, fmt.Errorf("%w: week", ErrInvalidWeekGridChange)
	}
	start, err := strconv.Atoi(form.Get("start"))
	if err != nil {
		return WeekGridChange{}, fmt.Errorf("%w: start", ErrInvalidWeekGridChange)
	}
	end, err := strconv.Atoi(form.Get("end"))
	if err != nil {
		return WeekGridChange{}, fmt.Errorf("%w: end", ErrInvalidWeekGridChange)
	}
	change.StartMinute = config.snap(start)
	change.EndMinute = config.snap(end)
	if change.StartMinute < config.StartMinute || change.StartMinute >= config.EndMinute {
		return WeekGridChange{}, fmt.Errorf("%w: start outside the grid", ErrInvalidWeekGridChange)
	}
	if change.EndMinute <= change.StartMinute || change.EndMinute > minutesPerDay {
		return WeekGridChange{}, fmt.Errorf("%w: end", ErrInvalidWeekGridChange)
	}
	return change, nil
}

// snap rounds minute to the nearest step boundary of the grid.
func (c WeekGridConfig) snap(minute int) int {
	offset := minute - c.StartMinute
	steps := offset / c.Step
	if remainder := offset % c.Step; remainder*2 >= c.Step {
		steps++
	} else if remainder < 0 && -remainder*2 > c.Step {
		steps--
	}
	return c.StartMinute + steps*c.Step
}

func (c WeekGridConfig) weekdays() []int {
	if len(c.Weekdays) == 0 {
		return []int{1, 2, 3, 4, 5, 6, 7}
	}
	return c.Weekdays
}

func (c WeekGridConfig) validate() error {
	if !validWeekGridID(c.ID) {
		return fmt.Errorf("%w: id %q", ErrInvalidWeekGrid, c.ID)
	}
	if !slices.Contains(weekGridSteps, c.Step) {
		return fmt.Errorf("%w: step %d", ErrInvalidWeekGrid, c.Step)
	}
	if c.StartMinute < 0 || c.EndMinute > minutesPerDay || c.StartMinute >= c.EndMinute ||
		c.StartMinute%c.Step != 0 || c.EndMinute%c.Step != 0 {
		return fmt.Errorf("%w: range %d-%d", ErrInvalidWeekGrid, c.StartMinute, c.EndMinute)
	}
	if (c.EndMinute-c.StartMinute)/c.Step > weekGridMaxRows {
		return fmt.Errorf("%w: more than %d rows", ErrInvalidWeekGrid, weekGridMaxRows)
	}
	seenDays := map[int]bool{}
	for _, day := range c.weekdays() {
		if day < 1 || day > 7 || seenDays[day] {
			return fmt.Errorf("%w: weekday %d", ErrInvalidWeekGrid, day)
		}
		seenDays[day] = true
	}
	if len(c.Resources) > WeekGridMaxResources {
		return fmt.Errorf("%w: more than %d resources", ErrInvalidWeekGrid, WeekGridMaxResources)
	}
	seenResources := map[string]bool{}
	for _, resource := range c.Resources {
		if resource.Key == "" || seenResources[resource.Key] {
			return fmt.Errorf("%w: resource %q", ErrInvalidWeekGrid, resource.Key)
		}
		seenResources[resource.Key] = true
	}
	if c.Week != "" && c.Week != "A" && c.Week != "B" {
		return fmt.Errorf("%w: week %q", ErrInvalidWeekGrid, c.Week)
	}
	if c.DefaultDuration < 0 {
		return fmt.Errorf("%w: default duration", ErrInvalidWeekGrid)
	}
	return nil
}

func validWeekGridID(id string) bool {
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, character := range id {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

// BuildWeekGrid validates the configuration and items and lays the items out.
// Items outside the shown days, resources, or hours go to Hidden; items that
// cross the visible range are clipped and flagged.
func BuildWeekGrid(config WeekGridConfig, items []WeekGridItem, translate WeekGridTranslator) (*WeekGridView, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if translate == nil {
		return nil, fmt.Errorf("%w: translator is nil", ErrInvalidWeekGrid)
	}
	seenItems := map[string]bool{}
	for _, item := range items {
		if item.ID == "" || seenItems[item.ID] {
			return nil, fmt.Errorf("%w: item id %q", ErrInvalidWeekGrid, item.ID)
		}
		seenItems[item.ID] = true
		if item.Weekday < 1 || item.Weekday > 7 || item.StartMinute < 0 || item.EndMinute > minutesPerDay ||
			item.StartMinute >= item.EndMinute {
			return nil, fmt.Errorf("%w: item %q times", ErrInvalidWeekGrid, item.ID)
		}
	}
	text, err := newWeekGridText(translate)
	if err != nil {
		return nil, err
	}

	rows := (config.EndMinute - config.StartMinute) / config.Step
	resources := config.Resources
	hasResources := len(resources) > 0
	if !hasResources {
		resources = []WeekGridResource{{}}
	}
	weekdays := config.weekdays()
	view := &WeekGridView{
		ID:              config.ID,
		StartMinute:     config.StartMinute,
		EndMinute:       config.EndMinute,
		Step:            config.Step,
		Rows:            rows,
		DefaultDuration: cmp.Or(config.DefaultDuration, 60),
		Week:            config.Week,
		MoveURL:         config.MoveURL,
		CreateURL:       config.CreateURL,
		CSRFToken:       config.CSRFToken,
		Editable:        config.MoveURL != "" || config.CreateURL != "",
		HasResources:    hasResources,
		BoardClass:      fmt.Sprintf("wg-cols-%d", len(weekdays)*len(resources)),
		RowsClass:       fmt.Sprintf("wg-rows-%d", rows),
		DaySpanClass:    fmt.Sprintf("wg-cspan-%d", len(resources)),
		StepClass:       fmt.Sprintf("wg-step-%d", config.Step),
		HourClass:       weekGridHourClass(config),
		PreviousURL:     config.PreviousURL,
		NextURL:         config.NextURL,
		Labels:          text.labels,
		DragHint:        cmp.Or(config.DragHint, text.labels.DragHint),
	}
	if config.WeekAURL != "" || config.WeekBURL != "" {
		view.WeekTabs = []WeekGridTab{
			{Label: text.weekA, URL: config.WeekAURL, Current: config.Week == "A"},
			{Label: text.weekB, URL: config.WeekBURL, Current: config.Week == "B"},
		}
	}
	for minute := config.StartMinute; minute < config.EndMinute; minute += config.Step {
		if minute%60 == 0 || minute == config.StartMinute {
			view.TimeLabels = append(view.TimeLabels, WeekGridTimeLabel{
				Label:    formatWeekGridMinute(minute),
				RowClass: fmt.Sprintf("wg-row-%d", (minute-config.StartMinute)/config.Step+1),
			})
		}
	}

	var monday time.Time
	if !config.FirstDay.IsZero() {
		monday = isoMonday(config.FirstDay)
		label, err := text.rangeLabel(monday, monday.AddDate(0, 0, 6))
		if err != nil {
			return nil, err
		}
		view.RangeLabel = label
	}

	type columnKey struct {
		weekday  int
		resource string
	}
	columns := map[columnKey]*WeekGridColumnView{}
	resourceLabels := map[string]string{}
	for _, resource := range config.Resources {
		resourceLabels[resource.Key] = resource.Label
	}
	for _, weekday := range weekdays {
		day := WeekGridDayView{Weekday: weekday, Name: text.days[weekday], ShortName: text.shortDays[weekday]}
		if !monday.IsZero() {
			label, err := text.dateLabel(monday.AddDate(0, 0, weekday-1))
			if err != nil {
				return nil, err
			}
			day.DateLabel = label
		}
		for _, resource := range resources {
			label := day.Name
			if hasResources {
				label = day.Name + ", " + resource.Label
			}
			day.Columns = append(day.Columns, WeekGridColumnView{
				Weekday:       weekday,
				Resource:      resource.Key,
				ResourceLabel: resource.Label,
				Label:         label,
			})
		}
		view.Days = append(view.Days, day)
	}
	for dayIndex := range view.Days {
		for columnIndex := range view.Days[dayIndex].Columns {
			column := &view.Days[dayIndex].Columns[columnIndex]
			columns[columnKey{column.Weekday, column.Resource}] = column
		}
	}

	grouped := map[columnKey][]WeekGridItemView{}
	for _, item := range items {
		placed := WeekGridItemView{
			WeekGridItem:  item,
			DayName:       text.days[item.Weekday],
			TimeLabel:     formatWeekGridMinute(item.StartMinute) + "–" + formatWeekGridMinute(item.EndMinute),
			ResourceLabel: resourceLabels[item.Resource],
			VariantClass:  item.Variant.class(),
			Movable:       item.Draggable && !item.Background && config.MoveURL != "",
		}
		if !hasResources {
			placed.Resource = ""
		}
		key := columnKey{item.Weekday, placed.Resource}
		if _, ok := columns[key]; !ok || item.EndMinute <= config.StartMinute || item.StartMinute >= config.EndMinute {
			view.Hidden = append(view.Hidden, placed)
			continue
		}
		grouped[key] = append(grouped[key], placed)
	}
	for key, columnItems := range grouped {
		laid := layoutWeekGridColumn(columnItems, config)
		columns[key].Items = laid
		for _, item := range laid {
			if item.Movable {
				view.MovableItems = append(view.MovableItems, item)
			}
		}
	}
	slices.SortFunc(view.MovableItems, compareWeekGridItems)
	slices.SortFunc(view.Hidden, compareWeekGridItems)
	if view.Editable {
		view.Options = weekGridOptions(config, view.Days)
		view.MoveFields = WeekGridFieldSet{Prefix: config.ID + "-move", Grid: view}
		view.CreateFields = WeekGridFieldSet{Prefix: config.ID + "-create", Grid: view}
	}
	return view, nil
}

func compareWeekGridItems(a, b WeekGridItemView) int {
	return cmp.Or(
		cmp.Compare(a.Weekday, b.Weekday),
		cmp.Compare(a.StartMinute, b.StartMinute),
		cmp.Compare(a.Resource, b.Resource),
		cmp.Compare(a.ID, b.ID),
	)
}

// layoutWeekGridColumn places one column's items on rows and lanes. Rows are
// the step slots an item touches, clipped to the visible range. Items whose
// row spans overlap (directly or through a chain of overlaps) form a cluster;
// each cluster gets as many lanes as it needs, and every item takes the
// first lane that is free at its start row. Items that only touch (one ends
// on the row the next starts) share a lane.
func layoutWeekGridColumn(items []WeekGridItemView, config WeekGridConfig) []WeekGridItemView {
	var background []WeekGridItemView
	laid := make([]WeekGridItemView, 0, len(items))
	for _, item := range items {
		if item.Background {
			background = append(background, item)
		} else {
			laid = append(laid, item)
		}
	}
	rowsOf := func(item *WeekGridItemView) {
		start := max(item.StartMinute, config.StartMinute)
		end := min(item.EndMinute, config.EndMinute)
		item.ClippedStart = item.StartMinute < config.StartMinute
		item.ClippedEnd = item.EndMinute > config.EndMinute
		item.Row = (start - config.StartMinute) / config.Step
		endRow := (end - config.StartMinute + config.Step - 1) / config.Step
		item.RowSpan = max(endRow-item.Row, 1)
	}
	for index := range laid {
		rowsOf(&laid[index])
	}
	slices.SortStableFunc(laid, func(a, b WeekGridItemView) int {
		return cmp.Or(
			cmp.Compare(a.Row, b.Row),
			cmp.Compare(b.RowSpan, a.RowSpan),
			cmp.Compare(a.ID, b.ID),
		)
	})

	clusterStart, clusterEnd := 0, -1
	var laneEnds []int
	finish := func(end int) {
		for index := clusterStart; index < end; index++ {
			laid[index].Lanes = min(len(laneEnds), WeekGridMaxLanes)
		}
	}
	for index := range laid {
		item := &laid[index]
		if item.Row >= clusterEnd {
			finish(index)
			clusterStart, clusterEnd, laneEnds = index, item.Row, laneEnds[:0]
		}
		lane := slices.IndexFunc(laneEnds, func(end int) bool { return end <= item.Row })
		if lane < 0 {
			lane = len(laneEnds)
			laneEnds = append(laneEnds, 0)
		}
		laneEnds[lane] = item.Row + item.RowSpan
		item.Lane = min(lane, WeekGridMaxLanes-1)
		clusterEnd = max(clusterEnd, item.Row+item.RowSpan)
	}
	finish(len(laid))

	for index := range laid {
		item := &laid[index]
		firstColumn := item.Lane*weekGridLaneColumns/item.Lanes + 1
		nextColumn := (item.Lane+1)*weekGridLaneColumns/item.Lanes + 1
		item.PlacementClass = fmt.Sprintf("wg-row-%d wg-rspan-%d wg-col-%d wg-cspan-%d",
			item.Row+1, item.RowSpan, firstColumn, nextColumn-firstColumn)
	}
	// Background items come first, so the others paint over them.
	slices.SortStableFunc(background, compareWeekGridItems)
	for index := range background {
		item := &background[index]
		rowsOf(item)
		item.Lane, item.Lanes = 0, 1
		item.PlacementClass = fmt.Sprintf("wg-row-%d wg-rspan-%d wg-col-1 wg-cspan-%d", item.Row+1, item.RowSpan, weekGridLaneColumns)
	}
	return append(background, laid...)
}

func weekGridOptions(config WeekGridConfig, days []WeekGridDayView) WeekGridOptions {
	var options WeekGridOptions
	for _, day := range days {
		options.Days = append(options.Days, WeekGridOption{Value: strconv.Itoa(day.Weekday), Label: day.Name})
	}
	for _, resource := range config.Resources {
		options.Resources = append(options.Resources, WeekGridOption{Value: resource.Key, Label: resource.Label})
	}
	for minute := config.StartMinute; minute <= config.EndMinute; minute += config.Step {
		option := WeekGridOption{Value: strconv.Itoa(minute), Label: formatWeekGridMinute(minute)}
		if minute < config.EndMinute {
			options.Starts = append(options.Starts, option)
		}
		if minute > config.StartMinute {
			options.Ends = append(options.Ends, option)
		}
	}
	return options
}

// weekGridHourClass returns the wg-hour-at-<rows> class that moves the hour
// lines to the first full hour, or "" when the range starts on one.
func weekGridHourClass(config WeekGridConfig) string {
	if offset := config.StartMinute % 60; offset != 0 {
		return fmt.Sprintf("wg-hour-at-%d", (60-offset)/config.Step)
	}
	return ""
}

func formatWeekGridMinute(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

func isoMonday(day time.Time) time.Time {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	offset := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -offset)
}

type weekGridText struct {
	translate WeekGridTranslator
	days      [8]string
	shortDays [8]string
	months    [13]string
	weekA     string
	weekB     string
	labels    WeekGridLabels
}

func newWeekGridText(translate WeekGridTranslator) (weekGridText, error) {
	text := weekGridText{translate: translate}
	var failure error
	get := func(id string) string {
		value, err := translate(id, nil)
		if err != nil && failure == nil {
			failure = fmt.Errorf("translate %s: %w", id, err)
		}
		return value
	}
	for day := 1; day <= 7; day++ {
		text.days[day] = get(fmt.Sprintf("weekgrid.day.%d", day))
		text.shortDays[day] = get(fmt.Sprintf("weekgrid.day_short.%d", day))
	}
	for month := 1; month <= 12; month++ {
		text.months[month] = get(fmt.Sprintf("weekgrid.month.%d", month))
	}
	text.weekA = get("weekgrid.week_a")
	text.weekB = get("weekgrid.week_b")
	text.labels = WeekGridLabels{
		Region:       get("weekgrid.region"),
		Time:         get("weekgrid.time"),
		WeekSwitch:   get("weekgrid.week_switch"),
		Previous:     get("weekgrid.previous"),
		Next:         get("weekgrid.next"),
		Warning:      get("weekgrid.warning"),
		Hidden:       get("weekgrid.hidden"),
		StartsBefore: get("weekgrid.starts_before"),
		EndsAfter:    get("weekgrid.ends_after"),
		DragHint:     get("weekgrid.drag_hint"),
		Keyboard:     get("weekgrid.keyboard"),
		MoveHeading:  get("weekgrid.move_heading"),
		CreateHead:   get("weekgrid.create_heading"),
		Item:         get("weekgrid.item"),
		Day:          get("weekgrid.day"),
		Resource:     get("weekgrid.resource"),
		Start:        get("weekgrid.start"),
		End:          get("weekgrid.end"),
		MoveSubmit:   get("weekgrid.move_submit"),
		CreateSubmit: get("weekgrid.create_submit"),
		ResizeHandle: get("weekgrid.resize_handle"),
	}
	return text, failure
}

func (t weekGridText) dateLabel(day time.Time) (string, error) {
	return t.translate("weekgrid.date", map[string]string{
		"day":   strconv.Itoa(day.Day()),
		"month": t.months[day.Month()],
	})
}

func (t weekGridText) rangeLabel(from, to time.Time) (string, error) {
	fromLabel, err := t.dateLabel(from)
	if err != nil {
		return "", err
	}
	toLabel, err := t.dateLabel(to)
	if err != nil {
		return "", err
	}
	return t.translate("weekgrid.range", map[string]string{
		"from": fromLabel,
		"to":   toLabel,
		"year": strconv.Itoa(to.Year()),
	})
}
