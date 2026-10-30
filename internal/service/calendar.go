package service

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"time"
)

// The weekly calendars (#10) show the events an account sees (EventService
// List/ListBetween, which apply the Visibility rule) one Monday-based week at
// a time in the group's time zone. This file holds the calendar's pure
// rules: week boundaries, splitting events into per-day segments, and
// filters that only ever narrow what EventService returned.

// calendarMinYear and calendarMaxYear bound the weeks a calendar accepts;
// dates outside are read as missing.
const (
	calendarMinYear = 1970
	calendarMaxYear = 2200
	// calendarMinSegment is the shortest segment shown, for the rare event
	// whose wall-clock end is not after its start on a day the clocks go
	// back (02:30 summer time to 02:15 winter time).
	calendarMinSegment = 15
)

// ErrInvalidCalendarDate identifies a missing calendar day.
var ErrInvalidCalendarDate = errors.New("service: invalid calendar date")

// CalendarWeek is one Monday-based week in a group's time zone.
type CalendarWeek struct {
	// Monday is the week's first day as a calendar date (midnight UTC, the
	// SeasonDate form).
	Monday time.Time
	// From and To are the instants of local midnight on Monday and on the
	// following Monday; an event is in the week when it overlaps [From, To).
	From     time.Time
	To       time.Time
	TimeZone string
	location *time.Location
}

// NewCalendarWeek returns the week holding day (only its date counts) in the
// IANA time zone. A week across a daylight-saving change is 167 or 169 hours
// long; its days still start at local midnight.
func NewCalendarWeek(day time.Time, timeZone string) (CalendarWeek, error) {
	location, err := eventLocation(timeZone)
	if err != nil {
		return CalendarWeek{}, err
	}
	date := SeasonDate(day)
	if date.IsZero() {
		return CalendarWeek{}, ErrInvalidCalendarDate
	}
	monday := date.AddDate(0, 0, -((int(date.Weekday()) + 6) % 7))
	week := CalendarWeek{Monday: monday, TimeZone: timeZone, location: location}
	week.From = week.midnight(0)
	week.To = week.midnight(7)
	return week, nil
}

// CalendarToday returns today's date in the IANA time zone.
func CalendarToday(now time.Time, timeZone string) (time.Time, error) {
	if _, err := eventLocation(timeZone); err != nil {
		return time.Time{}, err
	}
	return LocalDate(now, timeZone)
}

// ParseCalendarDate reads the week parameter of a calendar URL
// (YYYY-MM-DD, any day of the wanted week). It returns the zero time for
// anything else, including years outside 1970..2200.
func ParseCalendarDate(raw string) time.Time {
	date := ParseSeasonDate(raw)
	if date.IsZero() || date.Year() < calendarMinYear || date.Year() > calendarMaxYear {
		return time.Time{}
	}
	return date
}

// midnight returns the instant of local midnight offset days after Monday.
// time.Date moves a midnight that a clock change skips to the first instant
// that exists.
func (w CalendarWeek) midnight(offset int) time.Time {
	day := w.Monday.AddDate(0, 0, offset)
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, w.location)
}

// Day returns the date of the ISO weekday (1 = Monday) of the week.
func (w CalendarWeek) Day(weekday int) time.Time {
	return w.Monday.AddDate(0, 0, weekday-1)
}

// Previous and Next return the Mondays of the neighboring weeks.
func (w CalendarWeek) Previous() time.Time { return w.Monday.AddDate(0, 0, -7) }

// Next returns the Monday of the following week.
func (w CalendarWeek) Next() time.Time { return w.Monday.AddDate(0, 0, 7) }

// Contains reports whether the calendar date day is in the week.
func (w CalendarWeek) Contains(day time.Time) bool {
	day = SeasonDate(day)
	return !day.IsZero() && !day.Before(w.Monday) && day.Before(w.Monday.AddDate(0, 0, 7))
}

// CalendarSegment is the part of an event on one day of a week, in
// wall-clock minutes after local midnight (EndMinute 1440 is midnight).
type CalendarSegment struct {
	Weekday     int
	StartMinute int
	EndMinute   int
	// ContinuesBefore and ContinuesAfter mark an event that started on an
	// earlier day or ends on a later day (or outside the week).
	ContinuesBefore bool
	ContinuesAfter  bool
}

// Segments splits an event into one segment per day of the week it covers.
// Times are wall-clock times in the week's zone, so an event at 01:30 on the
// night the clocks go back starts at minute 90 although that day has 25
// hours. Days the event does not touch, and events outside the week, give
// no segments.
func (w CalendarWeek) Segments(event Event) []CalendarSegment {
	if w.location == nil || !event.EndsAt.After(event.StartsAt) {
		return nil
	}
	var segments []CalendarSegment
	for offset := range 7 {
		dayStart, dayEnd := w.midnight(offset), w.midnight(offset+1)
		start, end := maxTime(event.StartsAt, dayStart), minTime(event.EndsAt, dayEnd)
		if !end.After(start) {
			continue
		}
		segment := CalendarSegment{
			Weekday:         offset + 1,
			StartMinute:     w.wallMinute(start),
			EndMinute:       minutesPerCalendarDay,
			ContinuesBefore: event.StartsAt.Before(dayStart),
			ContinuesAfter:  event.EndsAt.After(dayEnd),
		}
		if end.Before(dayEnd) {
			segment.EndMinute = w.wallMinute(end)
		}
		if segment.EndMinute <= segment.StartMinute {
			segment.EndMinute = min(segment.StartMinute+calendarMinSegment, minutesPerCalendarDay)
		}
		segments = append(segments, segment)
	}
	return segments
}

const minutesPerCalendarDay = 24 * 60

func (w CalendarWeek) wallMinute(instant time.Time) int {
	local := instant.In(w.location)
	return local.Hour()*60 + local.Minute()
}

// CalendarFilter narrows a calendar to one team, resource, or event kind. Zero
// values do not filter. A filter is applied to the events EventService
// already returned for the account, so it never shows more than that.
type CalendarFilter struct {
	TeamID     int64
	ResourceID int64
	KindID     int64
}

// Matches reports whether event passes the filter.
func (f CalendarFilter) Matches(event Event) bool {
	if f.TeamID > 0 && !slices.Contains(event.TeamIDs(), f.TeamID) {
		return false
	}
	if f.ResourceID > 0 && (event.ResourceID == nil || *event.ResourceID != f.ResourceID) {
		return false
	}
	return f.KindID <= 0 || event.KindID == f.KindID
}

// FilterCalendarEvents returns the events that pass filter, in order.
func FilterCalendarEvents(events []Event, filter CalendarFilter) []Event {
	kept := make([]Event, 0, len(events))
	for _, event := range events {
		if filter.Matches(event) {
			kept = append(kept, event)
		}
	}
	return kept
}

// CalendarResource is a resource offered by the calendar's resource filter.
type CalendarResource struct {
	ID   int64
	Name string
}

// CalendarEventResources lists the resources the events take place on, once
// each, by name. Players and parents, who do not see the resource list,
// filter by these.
func CalendarEventResources(events []Event) []CalendarResource {
	seen := make(map[int64]bool)
	resources := make([]CalendarResource, 0)
	for _, event := range events {
		if event.ResourceID == nil || seen[*event.ResourceID] {
			continue
		}
		seen[*event.ResourceID] = true
		name := ""
		if event.ResourceName != nil {
			name = *event.ResourceName
		}
		resources = append(resources, CalendarResource{ID: *event.ResourceID, Name: name})
	}
	slices.SortFunc(resources, func(a, b CalendarResource) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return resources
}
