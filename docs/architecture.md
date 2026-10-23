# Architecture

Package responsibilities are listed in `AGENTS.md`; the booking design is in
`design/booking.md`. This file describes shared building blocks that several
features use.

## Week grid

A server-rendered calendar week used by coach availability (#7), the weekly
calendars (#10), the resource slot plan (#13), and the training plan board
(#14). Code: `internal/adapter/http/weekgrid.go` (view model, layout, change
parsing), `templates/weekgrid.html` (the `weekgrid` template), the week grid
section of `static/src/app.css`, the week grid block of `static/app.js`, and
messages in `internal/locale/catalogs/<lang>/weekgrid.json`.

### Feeding it

A feature handler describes the frame and the items; it never deals with
rows, lanes, or CSS:

```go
grid, err := BuildWeekGrid(WeekGridConfig{
	ID:          "plan-grid",          // DOM id, [a-z][a-z0-9-]*
	StartMinute: 7 * 60,               // visible range, multiples of Step
	EndMinute:   21 * 60,
	Step:        15,                   // 5, 10, 15, 20, 30, or 60; at most 96 rows
	Weekdays:    nil,                  // ISO weekdays to show; nil = Monday..Sunday
	Resources:   []WeekGridResource{{Key: "7", Label: "Court 1"}}, // optional columns per day
	Week:        "A",                  // alternating plans: "A" or "B"
	WeekAURL:    urlA, WeekBURL: urlB, // show the A/B switch
	FirstDay:    monday,               // dated views: day headers show dates
	PreviousURL: prev, NextURL: next,  // dated views: week navigation
	MoveURL:     moveURL,              // enables moving and resizing
	CreateURL:   createURL,            // enables adding (click an empty slot)
	CSRFToken:   token,                // for the keyboard forms without HTMX
}, items, renderer.WeekGridTranslator(language))
```

Each `WeekGridItem` has an opaque `ID`, `Weekday` (1 = Monday),
`StartMinute`/`EndMinute` (minutes after local midnight, as in the data
model), optional `Resource` (a resource key), `Label`, optional `Detail`,
`Variant` (`neutral`, `primary`, `secondary`, `accent`, `info`, `success`,
`warning`, `error`), `Draggable`, and `Warning` with optional `WarningText`.
Render the result with `{{template "weekgrid" .Grid}}`.

### Layout

Each day (or resource inside a day) is a CSS grid column with one row per
step. Items occupy every step slot they touch, clipped to the visible range
(clipped items are flagged and marked). Items whose rows overlap, directly or
through a chain, form a cluster; the cluster gets as many lanes as it needs
and each item takes the first lane free at its start row, so items that only
touch share a lane. Lanes divide twelve sub-columns; at most 12 lanes show,
further items share the last one. Items outside the shown days, resources, or
hours are listed under the grid instead of disappearing.

The Content-Security-Policy forbids `style` attributes, so placement uses
classes (`wg-row-N`, `wg-rspan-N`, `wg-col-N`, `wg-cspan-N`, `wg-rows-N`,
`wg-cols-N`, `wg-step-N`, `wg-hour-at-N`) that `static/src/app.css` generates
with Tailwind `@utility` and `@source inline`. A test checks that every class
the view model can emit exists in the built stylesheet.

### Changes

The browser only proposes changes. Dropping an item (SortableJS, also on
touch after a short hold), dragging its bottom edge, clicking an empty slot,
or submitting the keyboard form posts `item`, `weekday`, `start`, `end`,
`resource`, and `week` with HTMX and the CSRF header. The handler calls
`ParseWeekGridChange(request, config, requireItem)`, which validates the
values against the grid the user saw and snaps times to the step, then
authorizes and applies the change and answers with the re-rendered grid
(outer swap of the grid element) plus out-of-band alerts. A rejected change
answers 422 with the unchanged grid and an error alert; a plain form post
redirects back (303).

### Demo

`/{language}/dev/weekgrid` (`weekgrid_demo.go`) exercises every variant with
in-memory items. It is registered only when `Dependencies.DevelopmentRoutes`
is true, which `cmd/web` sets from `Config.DevelopmentRoutes()` (only
`CALBOOKER_ENV=development`).
