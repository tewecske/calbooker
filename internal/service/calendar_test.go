package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func calendarDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func TestNewCalendarWeekBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		day      time.Time
		zone     string
		monday   time.Time
		from, to time.Time
		hours    float64
	}{
		{
			name: "autumn change", day: calendarDate(2026, 10, 25), zone: "Europe/Budapest", monday: calendarDate(2026, 10, 19),
			from: time.Date(2026, 10, 18, 22, 0, 0, 0, time.UTC), to: time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC), hours: 169,
		},
		{
			name: "spring change", day: calendarDate(2026, 3, 23), zone: "Europe/Budapest", monday: calendarDate(2026, 3, 23),
			from: time.Date(2026, 3, 22, 23, 0, 0, 0, time.UTC), to: time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC), hours: 167,
		},
		{
			name: "ordinary week, mid-week day", day: calendarDate(2026, 11, 4), zone: "Europe/Budapest", monday: calendarDate(2026, 11, 2),
			from: time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC), hours: 168,
		},
		{
			name: "other zone", day: calendarDate(2026, 11, 1), zone: "America/New_York", monday: calendarDate(2026, 10, 26),
			from: time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC), hours: 169,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			week, err := NewCalendarWeek(test.day, test.zone)
			if err != nil {
				t.Fatalf("NewCalendarWeek() error = %v", err)
			}
			if !week.Monday.Equal(test.monday) || !week.From.Equal(test.from) || !week.To.Equal(test.to) || week.To.Sub(week.From).Hours() != test.hours {
				t.Fatalf("week = %s, %s .. %s", week.Monday, week.From.UTC(), week.To.UTC())
			}
			if !week.Contains(test.monday.AddDate(0, 0, 6)) || week.Contains(test.monday.AddDate(0, 0, 7)) || week.Contains(test.monday.AddDate(0, 0, -1)) {
				t.Fatal("Contains")
			}
			if !week.Previous().Equal(test.monday.AddDate(0, 0, -7)) || !week.Next().Equal(test.monday.AddDate(0, 0, 7)) || !week.Day(7).Equal(test.monday.AddDate(0, 0, 6)) {
				t.Fatal("neighbors")
			}
		})
	}
	if _, err := NewCalendarWeek(calendarDate(2026, 1, 1), "Mars/Base"); !errors.Is(err, ErrInvalidTimeZone) {
		t.Fatalf("bad zone error = %v", err)
	}
	if _, err := NewCalendarWeek(time.Time{}, "Europe/Budapest"); !errors.Is(err, ErrInvalidCalendarDate) {
		t.Fatalf("zero day error = %v", err)
	}
	if _, err := CalendarToday(time.Now(), ""); !errors.Is(err, ErrInvalidTimeZone) {
		t.Fatalf("today without zone error = %v", err)
	}
	// Sunday 23:30 UTC is already Monday in Budapest.
	if today, err := CalendarToday(time.Date(2026, 10, 25, 23, 30, 0, 0, time.UTC), "Europe/Budapest"); err != nil || !today.Equal(calendarDate(2026, 10, 26)) {
		t.Fatalf("today = %s, %v", today, err)
	}
}

func TestParseCalendarDate(t *testing.T) {
	if got := ParseCalendarDate(" 2026-10-21 "); !got.Equal(calendarDate(2026, 10, 21)) {
		t.Fatalf("parsed = %s", got)
	}
	for _, raw := range []string{"", "2026-13-01", "21.10.2026", "1969-12-31", "2201-01-01", "2026-10-21T10:00"} {
		if got := ParseCalendarDate(raw); !got.IsZero() {
			t.Errorf("ParseCalendarDate(%q) = %s", raw, got)
		}
	}
}

func TestCalendarSegments(t *testing.T) {
	week, err := NewCalendarWeek(calendarDate(2026, 10, 19), "Europe/Budapest")
	if err != nil {
		t.Fatal(err)
	}
	span := func(starts, ends time.Time) Event {
		return Event{ID: 1, StartsAt: starts, EndsAt: ends}
	}
	tests := []struct {
		name  string
		event Event
		want  []CalendarSegment
	}{
		{name: "evening", event: span(at("2026-10-19T17:00"), at("2026-10-19T18:30")), want: []CalendarSegment{{Weekday: 1, StartMinute: 1020, EndMinute: 1110}}},
		{
			// 01:30 summer time to 03:00 winter time on the last Sunday of
			// October: wall-clock minutes 90 to 180, two and a half hours.
			name:  "night the clocks go back",
			event: span(at("2026-10-25T01:30"), time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)),
			want:  []CalendarSegment{{Weekday: 7, StartMinute: 90, EndMinute: 180}},
		},
		{
			// 02:30 summer time to 02:15 winter time: the end reads earlier
			// than the start, so the segment gets the shortest length.
			name:  "repeated hour",
			event: span(time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), time.Date(2026, 10, 25, 1, 15, 0, 0, time.UTC)),
			want:  []CalendarSegment{{Weekday: 7, StartMinute: 150, EndMinute: 165}},
		},
		{
			name:  "across midnight",
			event: span(at("2026-10-23T22:00"), at("2026-10-24T02:00")),
			want:  []CalendarSegment{{Weekday: 5, StartMinute: 1320, EndMinute: 1440, ContinuesAfter: true}, {Weekday: 6, StartMinute: 0, EndMinute: 120, ContinuesBefore: true}},
		},
		{
			name:  "ends at midnight",
			event: span(at("2026-10-20T23:00"), at("2026-10-21T00:00")),
			want:  []CalendarSegment{{Weekday: 2, StartMinute: 1380, EndMinute: 1440}},
		},
		{
			name:  "into the next week",
			event: span(at("2026-10-25T23:00"), at("2026-10-26T01:00")),
			want:  []CalendarSegment{{Weekday: 7, StartMinute: 1380, EndMinute: 1440, ContinuesAfter: true}},
		},
		{
			name:  "from the previous week, several days",
			event: span(at("2026-10-17T09:00"), at("2026-10-20T12:00")),
			want: []CalendarSegment{
				{Weekday: 1, StartMinute: 0, EndMinute: 1440, ContinuesBefore: true, ContinuesAfter: true},
				{Weekday: 2, StartMinute: 0, EndMinute: 720, ContinuesBefore: true},
			},
		},
		{name: "next week", event: span(at("2026-10-26T00:00"), at("2026-10-26T01:00"))},
		{name: "previous week, ending at midnight", event: span(at("2026-10-18T22:00"), at("2026-10-19T00:00"))},
		{name: "no duration", event: span(at("2026-10-20T10:00"), at("2026-10-20T10:00"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := week.Segments(test.event); !slices.Equal(got, test.want) {
				t.Fatalf("segments = %+v, want %+v", got, test.want)
			}
		})
	}
	if got := (CalendarWeek{}).Segments(span(at("2026-10-20T10:00"), at("2026-10-20T11:00"))); got != nil {
		t.Fatalf("zero week segments = %+v", got)
	}
}

func TestCalendarFilterAndResources(t *testing.T) {
	court, hall := int64(31), int64(32)
	courtName, hallName := "court 1", "Hall"
	events := []Event{
		{ID: 1, Kind: EventKindTraining, Teams: eventTeams(1), ResourceID: &court, ResourceName: &courtName},
		{ID: 2, Kind: EventKindMatch, Teams: eventTeams(2)},
		{ID: 3, Kind: EventKindTraining, Teams: eventTeams(1, 2), ResourceID: &hall, ResourceName: &hallName},
		{ID: 4, Kind: EventKindOther, Teams: eventTeams(3), ResourceID: &court, ResourceName: &courtName},
	}
	ids := func(list []Event) []int64 {
		out := make([]int64, 0, len(list))
		for _, event := range list {
			out = append(out, event.ID)
		}
		return out
	}
	tests := []struct {
		filter CalendarFilter
		want   []int64
	}{
		{filter: CalendarFilter{}, want: []int64{1, 2, 3, 4}},
		{filter: CalendarFilter{TeamID: 1}, want: []int64{1, 3}},
		{filter: CalendarFilter{ResourceID: 31}, want: []int64{1, 4}},
		{filter: CalendarFilter{Kind: EventKindTraining}, want: []int64{1, 3}},
		{filter: CalendarFilter{TeamID: 2, Kind: EventKindTraining}, want: []int64{3}},
		{filter: CalendarFilter{TeamID: 9}, want: []int64{}},
	}
	for _, test := range tests {
		if got := ids(FilterCalendarEvents(events, test.filter)); !slices.Equal(got, test.want) {
			t.Errorf("filter %+v = %v, want %v", test.filter, got, test.want)
		}
	}
	if got := CalendarEventResources(events); !slices.Equal(got, []CalendarResource{{ID: 31, Name: "court 1"}, {ID: 32, Name: "Hall"}}) {
		t.Fatalf("resources = %+v", got)
	}
	if got := CalendarEventResources(nil); got == nil || len(got) != 0 {
		t.Fatalf("no resources = %#v", got)
	}
}

// TestCalendarVisibilityPerRole runs the calendar's query and filters
// through EventService, so each role sees exactly its events and filters
// only narrow them.
func TestCalendarVisibilityPerRole(t *testing.T) {
	lions := storedEvent(1, 1)
	tigers := storedEvent(2, 2)
	tigers.Kind = EventKindMatch
	extra := storedEvent(3, 3)
	extra.Coaches = []EventCoach{{UserID: 8}}
	both := storedEvent(4, 1, 2)
	both.StartsAt, both.EndsAt = at("2026-11-08T01:30"), at("2026-11-08T03:00")
	nextWeek := storedEvent(5, 1)
	nextWeek.StartsAt, nextWeek.EndsAt = at("2026-11-09T00:00"), at("2026-11-09T01:00")
	week, err := NewCalendarWeek(calendarDate(2026, 11, 4), "Europe/Budapest")
	if err != nil {
		t.Fatal(err)
	}
	extraCoach := GroupActor{Access: NewGroupAccess(5, 8, GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{2}})}
	// A parent on team 2 whose child plays on team 3.
	parent := actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRoleParent}}, ChildTeamIDs: []int64{3}, ChildUserIDs: []int64{20}})
	tests := []struct {
		name   string
		actor  GroupActor
		filter CalendarFilter
		want   []int64
	}{
		{name: "admin", actor: adminActor(), want: []int64{1, 2, 3, 4}},
		{name: "manager", actor: actorWith(memberWith(GroupRoleManager)), want: []int64{1, 2, 3, 4}},
		{name: "site administrator", actor: siteAdminActor(), want: []int64{1, 2, 3, 4}},
		{name: "coach and extra coach", actor: extraCoach, want: []int64{2, 3, 4}},
		{name: "coach filtered by a team they do not see", actor: extraCoach, filter: CalendarFilter{TeamID: 1}, want: []int64{4}},
		{name: "player", actor: playerOn(1), want: []int64{1, 4}},
		{name: "player filtered by kind", actor: playerOn(1), filter: CalendarFilter{Kind: EventKindMatch}, want: []int64{}},
		{name: "parent with a child in another team", actor: parent, want: []int64{2, 3, 4}},
		{name: "parent filtered by the child's team", actor: parent, filter: CalendarFilter{TeamID: 3}, want: []int64{3}},
		{name: "member without teams", actor: actorWith(memberWith(GroupRolePlayer)), want: []int64{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := newTestEventService(t, newEventStub(lions, tigers, extra, both, nextWeek))
			listed, err := setup.service.ListBetween(context.Background(), test.actor.Access, week.From, week.To)
			if err != nil {
				t.Fatalf("ListBetween() error = %v", err)
			}
			got := make([]int64, 0)
			for _, event := range FilterCalendarEvents(listed, test.filter) {
				got = append(got, event.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, test.want) {
				t.Fatalf("shown = %v, want %v", got, test.want)
			}
		})
	}
	setup := newTestEventService(t, newEventStub(lions))
	if _, err := setup.service.ListBetween(context.Background(), actorWith(GroupAccessGrants{}).Access, week.From, week.To); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("non-member error = %v", err)
	}
}
