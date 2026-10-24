package httpadapter

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/locale"
	staticassets "github.com/tewecske/calbooker/static"
)

func testWeekGridConfig() WeekGridConfig {
	return WeekGridConfig{ID: "test-grid", StartMinute: 8 * 60, EndMinute: 12 * 60, Step: 15}
}

func testWeekGridTranslator(t *testing.T, language locale.Code) WeekGridTranslator {
	t.Helper()
	renderer, err := NewPageRenderer()
	if err != nil {
		t.Fatalf("NewPageRenderer() error = %v", err)
	}
	return renderer.WeekGridTranslator(language)
}

func gridItem(id string, start, end int) WeekGridItemView {
	return WeekGridItemView{WeekGridItem: WeekGridItem{ID: id, Weekday: 1, StartMinute: start, EndMinute: end}}
}

func TestLayoutWeekGridColumn(t *testing.T) {
	at := func(hour, minute int) int { return hour*60 + minute }
	type placement struct {
		class        string
		lanes        int
		clippedStart bool
		clippedEnd   bool
	}
	tests := []struct {
		name  string
		items []WeekGridItemView
		want  map[string]placement
	}{
		{
			name:  "single item fills the column",
			items: []WeekGridItemView{gridItem("a", at(9, 0), at(10, 0))},
			want:  map[string]placement{"a": {class: "wg-row-5 wg-rspan-4 wg-col-1 wg-cspan-12", lanes: 1}},
		},
		{
			name:  "two overlapping items split the column",
			items: []WeekGridItemView{gridItem("b", at(9, 30), at(10, 30)), gridItem("a", at(9, 0), at(10, 0))},
			want: map[string]placement{
				"a": {class: "wg-row-5 wg-rspan-4 wg-col-1 wg-cspan-6", lanes: 2},
				"b": {class: "wg-row-7 wg-rspan-4 wg-col-7 wg-cspan-6", lanes: 2},
			},
		},
		{
			name:  "touching items share one lane",
			items: []WeekGridItemView{gridItem("a", at(9, 0), at(10, 0)), gridItem("b", at(10, 0), at(11, 0))},
			want: map[string]placement{
				"a": {class: "wg-row-5 wg-rspan-4 wg-col-1 wg-cspan-12", lanes: 1},
				"b": {class: "wg-row-9 wg-rspan-4 wg-col-1 wg-cspan-12", lanes: 1},
			},
		},
		{
			name: "a chain of overlaps forms one cluster and reuses a freed lane",
			items: []WeekGridItemView{
				gridItem("a", at(8, 0), at(9, 30)),
				gridItem("b", at(8, 30), at(10, 0)),
				gridItem("c", at(9, 30), at(10, 30)),
			},
			want: map[string]placement{
				"a": {class: "wg-row-1 wg-rspan-6 wg-col-1 wg-cspan-6", lanes: 2},
				"b": {class: "wg-row-3 wg-rspan-6 wg-col-7 wg-cspan-6", lanes: 2},
				"c": {class: "wg-row-7 wg-rspan-4 wg-col-1 wg-cspan-6", lanes: 2},
			},
		},
		{
			name: "separate clusters get their own lane counts",
			items: []WeekGridItemView{
				gridItem("a", at(8, 0), at(9, 0)),
				gridItem("b", at(8, 0), at(9, 0)),
				gridItem("c", at(8, 0), at(9, 0)),
				gridItem("d", at(10, 0), at(11, 0)),
			},
			want: map[string]placement{
				"a": {class: "wg-row-1 wg-rspan-4 wg-col-1 wg-cspan-4", lanes: 3},
				"b": {class: "wg-row-1 wg-rspan-4 wg-col-5 wg-cspan-4", lanes: 3},
				"c": {class: "wg-row-1 wg-rspan-4 wg-col-9 wg-cspan-4", lanes: 3},
				"d": {class: "wg-row-9 wg-rspan-4 wg-col-1 wg-cspan-12", lanes: 1},
			},
		},
		{
			name: "five lanes divide twelve columns unevenly but completely",
			items: []WeekGridItemView{
				gridItem("a", at(8, 0), at(9, 0)), gridItem("b", at(8, 0), at(9, 0)), gridItem("c", at(8, 0), at(9, 0)),
				gridItem("d", at(8, 0), at(9, 0)), gridItem("e", at(8, 0), at(9, 0)),
			},
			want: map[string]placement{
				"a": {class: "wg-row-1 wg-rspan-4 wg-col-1 wg-cspan-2", lanes: 5},
				"b": {class: "wg-row-1 wg-rspan-4 wg-col-3 wg-cspan-2", lanes: 5},
				"c": {class: "wg-row-1 wg-rspan-4 wg-col-5 wg-cspan-3", lanes: 5},
				"d": {class: "wg-row-1 wg-rspan-4 wg-col-8 wg-cspan-2", lanes: 5},
				"e": {class: "wg-row-1 wg-rspan-4 wg-col-10 wg-cspan-3", lanes: 5},
			},
		},
		{
			name: "items crossing the visible range are clipped and flagged",
			items: []WeekGridItemView{
				gridItem("early", at(7, 0), at(8, 30)),
				gridItem("late", at(11, 30), at(13, 0)),
				gridItem("all", at(6, 0), at(14, 0)),
			},
			want: map[string]placement{
				"all":   {class: "wg-row-1 wg-rspan-16 wg-col-1 wg-cspan-6", lanes: 2, clippedStart: true, clippedEnd: true},
				"early": {class: "wg-row-1 wg-rspan-2 wg-col-7 wg-cspan-6", lanes: 2, clippedStart: true},
				"late":  {class: "wg-row-15 wg-rspan-2 wg-col-7 wg-cspan-6", lanes: 2, clippedEnd: true},
			},
		},
		{
			name:  "off-step times cover every slot they touch",
			items: []WeekGridItemView{gridItem("a", at(9, 5), at(9, 20))},
			want:  map[string]placement{"a": {class: "wg-row-5 wg-rspan-2 wg-col-1 wg-cspan-12", lanes: 1}},
		},
		{
			name:  "items touching in minutes but sharing a slot get separate lanes",
			items: []WeekGridItemView{gridItem("a", at(9, 0), at(9, 10)), gridItem("b", at(9, 10), at(9, 30))},
			want: map[string]placement{
				// On the same start row the longer item takes the first lane.
				"a": {class: "wg-row-5 wg-rspan-1 wg-col-7 wg-cspan-6", lanes: 2},
				"b": {class: "wg-row-5 wg-rspan-2 wg-col-1 wg-cspan-6", lanes: 2},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			laid := layoutWeekGridColumn(test.items, testWeekGridConfig())
			if len(laid) != len(test.want) {
				t.Fatalf("laid out %d items, want %d", len(laid), len(test.want))
			}
			for _, item := range laid {
				want, ok := test.want[item.ID]
				if !ok {
					t.Fatalf("unexpected item %q", item.ID)
				}
				got := placement{class: item.PlacementClass, lanes: item.Lanes, clippedStart: item.ClippedStart, clippedEnd: item.ClippedEnd}
				if got != want {
					t.Errorf("item %q = %+v, want %+v", item.ID, got, want)
				}
			}
		})
	}
}

func TestLayoutWeekGridColumnCapsLanes(t *testing.T) {
	var items []WeekGridItemView
	for index := range WeekGridMaxLanes + 3 {
		items = append(items, gridItem(fmt.Sprintf("item-%02d", index), 9*60, 10*60))
	}
	for _, item := range layoutWeekGridColumn(items, testWeekGridConfig()) {
		if item.Lanes != WeekGridMaxLanes || item.Lane >= WeekGridMaxLanes {
			t.Errorf("item %q lane %d of %d, want at most %d lanes", item.ID, item.Lane, item.Lanes, WeekGridMaxLanes)
		}
		if !strings.Contains(item.PlacementClass, "wg-cspan-1") {
			t.Errorf("item %q placement %q, want one lane column", item.ID, item.PlacementClass)
		}
	}
}

func TestBuildWeekGridPlacesItemsAndListsHidden(t *testing.T) {
	config := testWeekGridConfig()
	config.Weekdays = []int{1, 2, 3, 4, 5}
	config.Resources = []WeekGridResource{{Key: "c1", Label: "Court 1"}, {Key: "c2", Label: "Court 2"}}
	config.MoveURL = "/en/test/move"
	items := []WeekGridItem{
		{ID: "shown", Weekday: 2, StartMinute: 9 * 60, EndMinute: 10 * 60, Resource: "c2", Label: "Shown", Draggable: true, Variant: WeekGridAccent},
		{ID: "fixed", Weekday: 2, StartMinute: 9 * 60, EndMinute: 10 * 60, Resource: "c1", Label: "Fixed"},
		{ID: "weekend", Weekday: 6, StartMinute: 9 * 60, EndMinute: 10 * 60, Resource: "c1", Label: "Weekend"},
		{ID: "evening", Weekday: 1, StartMinute: 18 * 60, EndMinute: 19 * 60, Resource: "c1", Label: "Evening"},
		{ID: "elsewhere", Weekday: 1, StartMinute: 9 * 60, EndMinute: 10 * 60, Resource: "c9", Label: "Elsewhere"},
		{ID: "ends-at-start", Weekday: 1, StartMinute: 7 * 60, EndMinute: 8 * 60, Resource: "c1", Label: "Before"},
	}
	view, err := BuildWeekGrid(config, items, testWeekGridTranslator(t, locale.English))
	if err != nil {
		t.Fatalf("BuildWeekGrid() error = %v", err)
	}
	if view.BoardClass != "wg-cols-10" || view.RowsClass != "wg-rows-16" || view.DaySpanClass != "wg-cspan-2" || view.StepClass != "wg-step-15" {
		t.Errorf("classes = %q %q %q %q", view.BoardClass, view.RowsClass, view.DaySpanClass, view.StepClass)
	}
	if !view.Editable || !view.HasResources || len(view.Days) != 5 || len(view.Days[1].Columns) != 2 {
		t.Fatalf("view shape: editable=%t resources=%t days=%d", view.Editable, view.HasResources, len(view.Days))
	}
	tuesday := view.Days[1]
	if tuesday.Name != "Tuesday" || tuesday.ShortName != "Tue" || tuesday.Columns[1].Label != "Tuesday, Court 2" {
		t.Errorf("tuesday = %+v", tuesday)
	}
	shown := tuesday.Columns[1].Items
	if len(shown) != 1 || shown[0].ID != "shown" || !shown[0].Movable || shown[0].VariantClass != "wg-item-accent" ||
		shown[0].TimeLabel != "09:00–10:00" || shown[0].ResourceLabel != "Court 2" {
		t.Errorf("tuesday court 2 items = %+v", shown)
	}
	if fixed := tuesday.Columns[0].Items; len(fixed) != 1 || fixed[0].Movable || fixed[0].Lanes != 1 {
		t.Errorf("fixed item = %+v, want an unmovable item alone in its column", fixed)
	}
	var hidden []string
	for _, item := range view.Hidden {
		hidden = append(hidden, item.ID)
	}
	if got, want := strings.Join(hidden, ","), "ends-at-start,elsewhere,evening,weekend"; got != want {
		t.Errorf("hidden = %s, want %s", got, want)
	}
	if len(view.MovableItems) != 1 || view.MovableItems[0].ID != "shown" {
		t.Errorf("movable items = %+v", view.MovableItems)
	}
	if len(view.Options.Starts) != 16 || view.Options.Starts[0].Label != "08:00" || view.Options.Ends[15].Value != "720" {
		t.Errorf("time options = %+v / %+v", view.Options.Starts, view.Options.Ends)
	}
	if len(view.TimeLabels) != 4 || view.TimeLabels[1].Label != "09:00" || view.TimeLabels[1].RowClass != "wg-row-5" {
		t.Errorf("time labels = %+v", view.TimeLabels)
	}
}

func TestBuildWeekGridWithoutResourcesIgnoresItemResource(t *testing.T) {
	items := []WeekGridItem{{ID: "a", Weekday: 7, StartMinute: 8 * 60, EndMinute: 9 * 60, Resource: "c1", Label: "A"}}
	view, err := BuildWeekGrid(testWeekGridConfig(), items, testWeekGridTranslator(t, locale.English))
	if err != nil {
		t.Fatalf("BuildWeekGrid() error = %v", err)
	}
	if view.BoardClass != "wg-cols-7" || view.DaySpanClass != "wg-cspan-1" || view.Editable {
		t.Errorf("classes = %q %q editable=%t", view.BoardClass, view.DaySpanClass, view.Editable)
	}
	if got := view.Days[6].Columns[0].Items; len(got) != 1 || got[0].Resource != "" {
		t.Errorf("sunday items = %+v", got)
	}
}

func TestBuildWeekGridWeekSwitchAndDatedHeaders(t *testing.T) {
	config := testWeekGridConfig()
	config.Week = "B"
	config.WeekAURL, config.WeekBURL = "/a", "/b"
	view, err := BuildWeekGrid(config, nil, testWeekGridTranslator(t, locale.Hungarian))
	if err != nil {
		t.Fatalf("BuildWeekGrid() error = %v", err)
	}
	if len(view.WeekTabs) != 2 || view.WeekTabs[0].Current || !view.WeekTabs[1].Current || view.WeekTabs[1].Label != "B hét" {
		t.Errorf("week tabs = %+v", view.WeekTabs)
	}

	dated := testWeekGridConfig()
	dated.FirstDay = time.Date(2026, time.October, 22, 15, 0, 0, 0, time.UTC) // a Thursday
	dated.PreviousURL, dated.NextURL = "/prev", "/next"
	for _, test := range []struct {
		language          locale.Code
		rangeLabel, first string
	}{
		{locale.English, "Oct 19 – Oct 25, 2026", "Oct 19"},
		{locale.Hungarian, "2026. okt. 19. – okt. 25.", "okt. 19."},
	} {
		view, err := BuildWeekGrid(dated, nil, testWeekGridTranslator(t, test.language))
		if err != nil {
			t.Fatalf("BuildWeekGrid(%s) error = %v", test.language, err)
		}
		if view.RangeLabel != test.rangeLabel || view.Days[0].DateLabel != test.first || view.WeekTabs != nil {
			t.Errorf("%s: range %q, first day %q, tabs %v", test.language, view.RangeLabel, view.Days[0].DateLabel, view.WeekTabs)
		}
	}
}

func TestBuildWeekGridRejectsInvalidInput(t *testing.T) {
	valid := testWeekGridConfig()
	tests := []struct {
		name   string
		config func(*WeekGridConfig)
		items  []WeekGridItem
	}{
		{name: "empty id", config: func(c *WeekGridConfig) { c.ID = "" }},
		{name: "id with spaces", config: func(c *WeekGridConfig) { c.ID = "my grid" }},
		{name: "unsupported step", config: func(c *WeekGridConfig) { c.Step = 7 }},
		{name: "range off step", config: func(c *WeekGridConfig) { c.StartMinute = 8*60 + 5 }},
		{name: "empty range", config: func(c *WeekGridConfig) { c.EndMinute = c.StartMinute }},
		{name: "past midnight", config: func(c *WeekGridConfig) { c.EndMinute = 25 * 60 }},
		{name: "too many rows", config: func(c *WeekGridConfig) { c.Step, c.StartMinute, c.EndMinute = 5, 0, 9*60 }},
		{name: "duplicate weekday", config: func(c *WeekGridConfig) { c.Weekdays = []int{1, 1} }},
		{name: "weekday out of range", config: func(c *WeekGridConfig) { c.Weekdays = []int{0} }},
		{name: "duplicate resource", config: func(c *WeekGridConfig) { c.Resources = []WeekGridResource{{Key: "a"}, {Key: "a"}} }},
		{name: "unknown week", config: func(c *WeekGridConfig) { c.Week = "C" }},
		{name: "duplicate item", items: []WeekGridItem{{ID: "x", Weekday: 1, StartMinute: 0, EndMinute: 60}, {ID: "x", Weekday: 1, StartMinute: 0, EndMinute: 60}}},
		{name: "item without id", items: []WeekGridItem{{Weekday: 1, StartMinute: 0, EndMinute: 60}}},
		{name: "item ends before it starts", items: []WeekGridItem{{ID: "x", Weekday: 1, StartMinute: 60, EndMinute: 60}}},
		{name: "item weekday", items: []WeekGridItem{{ID: "x", Weekday: 8, StartMinute: 0, EndMinute: 60}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			if test.config != nil {
				test.config(&config)
			}
			if _, err := BuildWeekGrid(config, test.items, testWeekGridTranslator(t, locale.English)); !errors.Is(err, ErrInvalidWeekGrid) {
				t.Errorf("BuildWeekGrid() error = %v, want ErrInvalidWeekGrid", err)
			}
		})
	}
	if _, err := BuildWeekGrid(valid, nil, nil); !errors.Is(err, ErrInvalidWeekGrid) {
		t.Errorf("BuildWeekGrid(nil translator) error = %v", err)
	}
}

func TestParseWeekGridChange(t *testing.T) {
	config := testWeekGridConfig()
	config.Week = "A"
	config.Weekdays = []int{1, 2, 3, 4, 5}
	config.Resources = []WeekGridResource{{Key: "c1"}, {Key: "c2"}}
	valid := url.Values{"item": {"u8"}, "weekday": {"3"}, "start": {"547"}, "end": {"608"}, "resource": {"c2"}, "week": {"A"}}
	request := func(values url.Values) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/en/test/move", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request
	}

	change, err := ParseWeekGridChange(request(valid), config, true)
	if err != nil {
		t.Fatalf("ParseWeekGridChange() error = %v", err)
	}
	want := WeekGridChange{ItemID: "u8", Weekday: 3, StartMinute: 540, EndMinute: 615, Resource: "c2", Week: "A"}
	if change != want {
		t.Errorf("change = %+v, want %+v (snapped to 15 minutes)", change, want)
	}

	created, err := ParseWeekGridChange(request(valid), config, false)
	if err != nil || created.ItemID != "" {
		t.Errorf("create change = %+v, %v; want no item ID", created, err)
	}

	pastVisibleEnd := cloneValues(valid)
	pastVisibleEnd.Set("end", "780")
	if change, err := ParseWeekGridChange(request(pastVisibleEnd), config, true); err != nil || change.EndMinute != 780 {
		t.Errorf("end after the visible range = %+v, %v; want accepted", change, err)
	}

	for _, test := range []struct{ field, value string }{
		{"item", ""},
		{"item", strings.Repeat("x", 65)},
		{"weekday", "6"},
		{"weekday", "monday"},
		{"resource", "c9"},
		{"resource", ""},
		{"week", "B"},
		{"start", "420"},
		{"start", "720"},
		{"start", "nine"},
		{"end", "540"},
		{"end", "1500"},
		{"end", ""},
	} {
		t.Run(test.field+"="+test.value, func(t *testing.T) {
			values := cloneValues(valid)
			values.Set(test.field, test.value)
			if _, err := ParseWeekGridChange(request(values), config, true); !errors.Is(err, ErrInvalidWeekGridChange) {
				t.Errorf("error = %v, want ErrInvalidWeekGridChange", err)
			}
		})
	}

	noResources := testWeekGridConfig()
	values := url.Values{"item": {"u8"}, "weekday": {"1"}, "start": {"480"}, "end": {"540"}, "resource": {"c1"}}
	if _, err := ParseWeekGridChange(request(values), noResources, true); !errors.Is(err, ErrInvalidWeekGridChange) {
		t.Errorf("resource on a grid without resources: error = %v", err)
	}
	broken := noResources
	broken.Step = 7
	if _, err := ParseWeekGridChange(request(values), broken, true); !errors.Is(err, ErrInvalidWeekGrid) {
		t.Errorf("invalid config: error = %v", err)
	}
}

func cloneValues(values url.Values) url.Values {
	clone := url.Values{}
	for key, value := range values {
		clone[key] = append([]string(nil), value...)
	}
	return clone
}

func TestWeekGridHourClass(t *testing.T) {
	for _, test := range []struct {
		start, step int
		want        string
	}{
		{start: 8 * 60, step: 15, want: ""},
		{start: 8*60 + 30, step: 15, want: "wg-hour-at-2"},
		{start: 8*60 + 5, step: 5, want: "wg-hour-at-11"},
		{start: 8*60 + 20, step: 20, want: "wg-hour-at-2"},
	} {
		config := WeekGridConfig{StartMinute: test.start, Step: test.step}
		if got := weekGridHourClass(config); got != test.want {
			t.Errorf("weekGridHourClass(%d, %d) = %q, want %q", test.start, test.step, got, test.want)
		}
	}
}

func TestWeekGridConfigSnap(t *testing.T) {
	config := testWeekGridConfig()
	for minute, want := range map[int]int{480: 480, 487: 480, 488: 495, 502: 495, 473: 480, 472: 465, 721: 720} {
		if got := config.snap(minute); got != want {
			t.Errorf("snap(%d) = %d, want %d", minute, got, want)
		}
	}
}

// TestWeekGridStepClassesExist ties the Go limits to the generated
// stylesheet: every class the view model can emit must exist in app.css.
func TestWeekGridStepClassesExist(t *testing.T) {
	stylesheet, err := fs.ReadFile(staticassets.Files(), "app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(stylesheet)
	var want []string
	for _, step := range weekGridSteps {
		want = append(want, fmt.Sprintf(".wg-step-%d", step))
	}
	want = append(want,
		fmt.Sprintf(".wg-rows-%d{", weekGridMaxRows),
		fmt.Sprintf(".wg-row-%d{", weekGridMaxRows),
		fmt.Sprintf(".wg-rspan-%d{", weekGridMaxRows),
		fmt.Sprintf(".wg-cols-%d{", 7*WeekGridMaxResources),
		fmt.Sprintf(".wg-col-%d{", weekGridLaneColumns),
		fmt.Sprintf(".wg-cspan-%d{", weekGridLaneColumns),
		".wg-hour-at-11{",
		".wg-item-muted", ".wg-item-link", ".wg-item-badges",
	)
	for _, variant := range []WeekGridVariant{WeekGridNeutral, WeekGridPrimary, WeekGridSecondary, WeekGridAccent, WeekGridInfo, WeekGridSuccess, WeekGridWarning, WeekGridError} {
		want = append(want, "."+variant.class())
	}
	for _, class := range want {
		if !strings.Contains(css, class) {
			t.Errorf("app.css is missing %q; run make assets", class)
		}
	}
	if got := WeekGridVariant("purple").class(); got != "wg-item-neutral" {
		t.Errorf("unknown variant class = %q", got)
	}
}
