package httpadapter

import (
	"testing"

	"github.com/tewecske/calbooker/internal/locale"
)

func TestWeekGridBackgroundItemsTakeNoLane(t *testing.T) {
	config := testWeekGridConfig()
	config.MoveURL = "/move"
	items := []WeekGridItem{
		{ID: "a", Weekday: 1, StartMinute: 9 * 60, EndMinute: 10 * 60, Draggable: true},
		{ID: "bg", Weekday: 1, StartMinute: 8 * 60, EndMinute: 11 * 60, Background: true, Draggable: true},
	}
	grid, err := BuildWeekGrid(config, items, testWeekGridTranslator(t, locale.English))
	if err != nil {
		t.Fatalf("BuildWeekGrid() error = %v", err)
	}
	column := grid.Days[0].Columns[0].Items
	if len(column) != 2 || column[0].ID != "bg" || column[1].ID != "a" {
		t.Fatalf("column = %+v, want the background item first", column)
	}
	if column[0].PlacementClass != "wg-row-1 wg-rspan-12 wg-col-1 wg-cspan-12" || column[0].Movable {
		t.Errorf("background = %q movable %v, want the full column and not movable", column[0].PlacementClass, column[0].Movable)
	}
	if column[1].PlacementClass != "wg-row-5 wg-rspan-4 wg-col-1 wg-cspan-12" {
		t.Errorf("foreground = %q, want the full column (the background takes no lane)", column[1].PlacementClass)
	}
	if len(grid.MovableItems) != 1 {
		t.Errorf("movable items = %d, want 1", len(grid.MovableItems))
	}
}
