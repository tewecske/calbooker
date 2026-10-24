package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// MaxAvailabilityWindows is the most windows a coach keeps in one season
// part (after merging).
const MaxAvailabilityWindows = 100

// availabilityMinuteStep is the granularity of window times.
const availabilityMinuteStep = 5

var (
	// ErrInvalidAvailability identifies a window with a weekday outside
	// 1..7, or times that are not 5-minute steps with a start before the
	// end inside one day.
	ErrInvalidAvailability = errors.New("service: invalid availability window")
	// ErrAvailabilityWeek identifies a week other than "all", "A", or "B",
	// or an A/B week in a part without alternating weeks.
	ErrAvailabilityWeek = errors.New("service: invalid availability week")
	// ErrAvailabilityNotFound identifies a window that does not exist (any
	// more) among the coach's windows of the part.
	ErrAvailabilityNotFound = errors.New("service: availability window not found")
	// ErrTooManyAvailabilityWindows identifies a change that would leave more
	// than MaxAvailabilityWindows windows.
	ErrTooManyAvailabilityWindows = errors.New("service: too many availability windows")
	// ErrNoPreviousSeasonPart identifies a copy into the group's first part.
	ErrNoPreviousSeasonPart = errors.New("service: no previous season part")
	// ErrNothingToCopy identifies a copy from a part where the coach has no
	// windows that fit the target part; nothing is replaced.
	ErrNothingToCopy = errors.New("service: no availability to copy")
)

// AvailabilityWindow is a weekly time window in which a coach is available
// during a season part. Week is SeasonWeekAll, or SeasonWeekA or
// SeasonWeekB in parts with alternating weeks. Minutes are after local
// midnight; weekday 1 is Monday.
type AvailabilityWindow struct {
	ID           int64
	GroupID      int64
	SeasonPartID int64
	CoachUserID  int64
	Week         string
	Weekday      int
	StartMinute  int
	EndMinute    int
}

// AvailabilityInput is a new or moved window.
type AvailabilityInput struct {
	Week        string
	Weekday     int
	StartMinute int
	EndMinute   int
}

// PartAvailability is the availability of one or every coach in a season
// part.
type PartAvailability struct {
	Season  Season
	Part    SeasonPart
	Windows []AvailabilityWindow
}

// Applies reports whether a window counts in the part: every "all" window,
// and A/B windows only while the part alternates. A/B windows stay stored
// when a part stops alternating, but no longer count.
func (p PartAvailability) Applies(window AvailabilityWindow) bool {
	return window.Week == SeasonWeekAll || (p.Part.AlternatingWeeks && (window.Week == SeasonWeekA || window.Week == SeasonWeekB))
}

// ForCoach returns the windows of one coach.
func (p PartAvailability) ForCoach(coachUserID int64) []AvailabilityWindow {
	windows := make([]AvailabilityWindow, 0)
	for _, window := range p.Windows {
		if window.CoachUserID == coachUserID {
			windows = append(windows, window)
		}
	}
	return windows
}

// Covers reports whether the coach is available for the whole of a weekly
// slot: week is the slot's plan week ("A", "B", or "all"), weekday 1..7,
// and minutes after local midnight. In a part with alternating weeks an
// "all" slot needs the coach in both weeks; an A or B slot counts the
// coach's windows of that week plus their every-week windows.
func (p PartAvailability) Covers(coachUserID int64, week string, weekday, startMinute, endMinute int) bool {
	if startMinute >= endMinute {
		return false
	}
	weeks := []string{SeasonWeekAll}
	if p.Part.AlternatingWeeks {
		switch week {
		case SeasonWeekA, SeasonWeekB:
			weeks = []string{week}
		default:
			weeks = []string{SeasonWeekA, SeasonWeekB}
		}
	}
	for _, plan := range weeks {
		var intervals []AvailabilityWindow
		for _, window := range p.Windows {
			if window.CoachUserID == coachUserID && window.Weekday == weekday && p.Applies(window) && (window.Week == SeasonWeekAll || window.Week == plan) {
				intervals = append(intervals, AvailabilityWindow{Week: SeasonWeekAll, Weekday: weekday, StartMinute: window.StartMinute, EndMinute: window.EndMinute})
			}
		}
		covered := false
		for _, merged := range NormalizeAvailability(intervals) {
			if merged.StartMinute <= startMinute && endMinute <= merged.EndMinute {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// NormalizeAvailability sorts windows by week, weekday, and start and merges
// windows of the same week and weekday that overlap or touch (16:00-17:00
// and 17:00-18:00 become 16:00-18:00). A merged window keeps the first
// window's identifiers.
func NormalizeAvailability(windows []AvailabilityWindow) []AvailabilityWindow {
	sorted := slices.Clone(windows)
	slices.SortStableFunc(sorted, func(a, b AvailabilityWindow) int {
		return cmp.Or(
			cmp.Compare(a.CoachUserID, b.CoachUserID),
			cmp.Compare(weekOrder(a.Week), weekOrder(b.Week)),
			cmp.Compare(a.Weekday, b.Weekday),
			cmp.Compare(a.StartMinute, b.StartMinute),
			cmp.Compare(a.EndMinute, b.EndMinute),
		)
	})
	merged := make([]AvailabilityWindow, 0, len(sorted))
	for _, window := range sorted {
		if last := len(merged) - 1; last >= 0 {
			previous := &merged[last]
			if previous.CoachUserID == window.CoachUserID && previous.Week == window.Week && previous.Weekday == window.Weekday && window.StartMinute <= previous.EndMinute {
				previous.EndMinute = max(previous.EndMinute, window.EndMinute)
				continue
			}
		}
		merged = append(merged, window)
	}
	return merged
}

func weekOrder(week string) int {
	switch week {
	case SeasonWeekAll:
		return 0
	case SeasonWeekA:
		return 1
	case SeasonWeekB:
		return 2
	default:
		return 3
	}
}

// DefaultSeasonPart picks the part availability pages open with: the part
// containing today, else the next part to start, else the part that ended
// last. False when the group has no seasons.
func DefaultSeasonPart(seasons []Season, today time.Time) (Season, SeasonPart, bool) {
	var (
		next, last             SeasonPart
		nextSeason, lastSeason Season
		hasNext, hasLast       bool
	)
	today = SeasonDate(today)
	for _, season := range seasons {
		for _, part := range season.Parts {
			switch {
			case part.Contains(today):
				return season, part, true
			case part.StartsOn.After(today):
				if !hasNext || part.StartsOn.Before(next.StartsOn) {
					next, nextSeason, hasNext = part, season, true
				}
			default:
				if !hasLast || part.EndsOn.After(last.EndsOn) {
					last, lastSeason, hasLast = part, season, true
				}
			}
		}
	}
	if hasNext {
		return nextSeason, next, true
	}
	return lastSeason, last, hasLast
}

// PreviousSeasonPart returns the part before partID in time: part 1 of the
// same season for a part 2, else the last part of the previous season.
func PreviousSeasonPart(seasons []Season, partID int64) (Season, SeasonPart, bool) {
	type entry struct {
		season Season
		part   SeasonPart
	}
	var parts []entry
	for _, season := range seasons {
		for _, part := range season.Parts {
			parts = append(parts, entry{season, part})
		}
	}
	slices.SortFunc(parts, func(a, b entry) int { return a.part.StartsOn.Compare(b.part.StartsOn) })
	index := slices.IndexFunc(parts, func(e entry) bool { return e.part.ID == partID })
	if index <= 0 {
		return Season{}, SeasonPart{}, false
	}
	return parts[index-1].season, parts[index-1].part, true
}

// AvailabilityParts lists the seasons whose parts carry availability and
// the part the availability pages open with.
type AvailabilityParts struct {
	// Seasons are the group's seasons, latest first.
	Seasons []Season
	// Default is DefaultSeasonPart for today in the group's time zone;
	// HasDefault is false when the group has no seasons.
	DefaultSeason Season
	Default       SeasonPart
	HasDefault    bool
}

// Parts returns the seasons and the default part for the availability
// pages. Staff and coaches.
func (s *CoachService) Parts(ctx context.Context, access *GroupAccess) (AvailabilityParts, error) {
	if err := s.validate(ctx); err != nil {
		return AvailabilityParts{}, err
	}
	if !access.CanView() {
		return AvailabilityParts{}, ErrGroupNotFound
	}
	if !access.CanViewCoaches() {
		return AvailabilityParts{}, ErrGroupForbidden
	}
	seasons, err := s.seasons.List(ctx, access)
	if err != nil {
		return AvailabilityParts{}, err
	}
	today, err := s.seasons.Today(ctx, access)
	if err != nil {
		return AvailabilityParts{}, err
	}
	parts := AvailabilityParts{Seasons: seasons}
	parts.DefaultSeason, parts.Default, parts.HasDefault = DefaultSeasonPart(seasons, today)
	return parts, nil
}

// Availability returns every coach's windows of a part, for the side-by-side
// view and the planning boards. Staff only.
func (s *CoachService) Availability(ctx context.Context, access *GroupAccess, partID int64) (PartAvailability, error) {
	if err := s.validate(ctx); err != nil {
		return PartAvailability{}, err
	}
	if !access.CanView() {
		return PartAvailability{}, ErrGroupNotFound
	}
	if !access.CanViewAllAvailability() {
		return PartAvailability{}, ErrGroupForbidden
	}
	return s.partAvailability(ctx, access, partID, 0)
}

// CoachAvailability returns one coach's windows of a part. Staff and the
// coach themself.
func (s *CoachService) CoachAvailability(ctx context.Context, access *GroupAccess, partID, coachUserID int64) (PartAvailability, error) {
	if err := s.authorizeAvailability(ctx, access, coachUserID, access.CanViewAvailability(coachUserID)); err != nil {
		return PartAvailability{}, err
	}
	return s.partAvailability(ctx, access, partID, coachUserID)
}

// PreviousPart returns the part before partID, the source of
// CopyPreviousAvailability; false for the group's first part.
func (s *CoachService) PreviousPart(ctx context.Context, access *GroupAccess, partID int64) (Season, SeasonPart, bool, error) {
	if err := s.validate(ctx); err != nil {
		return Season{}, SeasonPart{}, false, err
	}
	if !access.CanView() {
		return Season{}, SeasonPart{}, false, ErrGroupNotFound
	}
	seasons, err := s.seasons.List(ctx, access)
	if err != nil {
		return Season{}, SeasonPart{}, false, err
	}
	if !slices.ContainsFunc(seasons, func(season Season) bool { _, ok := season.Part(partID); return ok }) {
		return Season{}, SeasonPart{}, false, ErrSeasonNotFound
	}
	season, part, ok := PreviousSeasonPart(seasons, partID)
	return season, part, ok, nil
}

// AddAvailability adds a window; it merges with the coach's windows of the
// same week and weekday that it overlaps or touches. Staff and the coach
// themself; staff changes to another coach are audited.
func (s *CoachService) AddAvailability(ctx context.Context, actor GroupActor, partID, coachUserID int64, input AvailabilityInput) error {
	part, err := s.editablePart(ctx, actor, partID, coachUserID)
	if err != nil {
		return err
	}
	window, err := buildAvailabilityWindow(part, coachUserID, input)
	if err != nil {
		return err
	}
	err = s.edit(ctx, actor, part, coachUserID, func(current []AvailabilityWindow) ([]AvailabilityWindow, error) {
		return append(current, window), nil
	})
	if err != nil {
		return err
	}
	s.recordAvailability(ctx, actor, coachUserID, part, "added "+windowDetail(window))
	return nil
}

// MoveAvailability moves or resizes a window, merging it as AddAvailability
// does.
func (s *CoachService) MoveAvailability(ctx context.Context, actor GroupActor, partID, coachUserID, windowID int64, input AvailabilityInput) error {
	part, err := s.editablePart(ctx, actor, partID, coachUserID)
	if err != nil {
		return err
	}
	window, err := buildAvailabilityWindow(part, coachUserID, input)
	if err != nil {
		return err
	}
	err = s.edit(ctx, actor, part, coachUserID, func(current []AvailabilityWindow) ([]AvailabilityWindow, error) {
		index := slices.IndexFunc(current, func(w AvailabilityWindow) bool { return w.ID == windowID })
		if windowID <= 0 || index < 0 {
			return nil, ErrAvailabilityNotFound
		}
		window.ID = current[index].ID
		current[index] = window
		return current, nil
	})
	if err != nil {
		return err
	}
	s.recordAvailability(ctx, actor, coachUserID, part, "moved window "+strconv.FormatInt(windowID, 10)+" to "+windowDetail(window))
	return nil
}

// DeleteAvailability removes a window.
func (s *CoachService) DeleteAvailability(ctx context.Context, actor GroupActor, partID, coachUserID, windowID int64) error {
	part, err := s.editablePart(ctx, actor, partID, coachUserID)
	if err != nil {
		return err
	}
	var removed AvailabilityWindow
	err = s.edit(ctx, actor, part, coachUserID, func(current []AvailabilityWindow) ([]AvailabilityWindow, error) {
		index := slices.IndexFunc(current, func(w AvailabilityWindow) bool { return w.ID == windowID })
		if windowID <= 0 || index < 0 {
			return nil, ErrAvailabilityNotFound
		}
		removed = current[index]
		return slices.Delete(current, index, index+1), nil
	})
	if err != nil {
		return err
	}
	s.recordAvailability(ctx, actor, coachUserID, part, "removed "+windowDetail(removed))
	return nil
}

// CopyPreviousAvailability replaces the coach's windows of a part with
// their windows of the previous part (PreviousSeasonPart) and returns how
// many windows the part now has. Into a part without alternating weeks only
// every-week windows are copied; A/B windows count only in alternating
// parts. When nothing fits, nothing is replaced (ErrNothingToCopy).
func (s *CoachService) CopyPreviousAvailability(ctx context.Context, actor GroupActor, partID, coachUserID int64) (int, error) {
	part, err := s.editablePart(ctx, actor, partID, coachUserID)
	if err != nil {
		return 0, err
	}
	sourceSeason, sourcePart, ok, err := s.PreviousPart(ctx, actor.Access, partID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNoPreviousSeasonPart
	}
	source, err := s.coaches.ListAvailability(ctx, actor.Access.GroupID(), sourcePart.ID, coachUserID)
	if err != nil {
		return 0, fmt.Errorf("list availability: %w", err)
	}
	sourceView := PartAvailability{Season: sourceSeason, Part: sourcePart}
	target := PartAvailability{Part: part}
	copied := make([]AvailabilityWindow, 0, len(source))
	for _, window := range source {
		if !sourceView.Applies(window) {
			continue
		}
		window.ID, window.SeasonPartID = 0, part.ID
		if target.Applies(window) {
			copied = append(copied, window)
		}
	}
	if len(copied) == 0 {
		return 0, ErrNothingToCopy
	}
	copied = NormalizeAvailability(copied)
	err = s.edit(ctx, actor, part, coachUserID, func([]AvailabilityWindow) ([]AvailabilityWindow, error) {
		return copied, nil
	})
	if err != nil {
		return 0, err
	}
	if actor.Access.UserID() != coachUserID {
		s.record(ctx, actor, AuditActionCoachAvailabilityCopied, "coach "+strconv.FormatInt(coachUserID, 10)+"; part "+strconv.FormatInt(part.ID, 10)+"; from part "+strconv.FormatInt(sourcePart.ID, 10)+"; "+strconv.Itoa(len(copied))+" windows")
	}
	return len(copied), nil
}

func (s *CoachService) authorizeAvailability(ctx context.Context, access *GroupAccess, coachUserID int64, allowed bool) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if !access.CanView() {
		return ErrGroupNotFound
	}
	if !allowed {
		return ErrGroupForbidden
	}
	_, err := s.findCoach(ctx, access, coachUserID)
	return err
}

func (s *CoachService) editablePart(ctx context.Context, actor GroupActor, partID, coachUserID int64) (SeasonPart, error) {
	if err := s.authorizeAvailability(ctx, actor.Access, coachUserID, actor.Access.CanEditAvailability(coachUserID)); err != nil {
		return SeasonPart{}, err
	}
	_, part, err := s.seasons.FindPart(ctx, actor.Access, partID)
	if err != nil {
		return SeasonPart{}, err
	}
	return part, nil
}

func (s *CoachService) partAvailability(ctx context.Context, access *GroupAccess, partID, coachUserID int64) (PartAvailability, error) {
	season, part, err := s.seasons.FindPart(ctx, access, partID)
	if err != nil {
		return PartAvailability{}, err
	}
	windows, err := s.coaches.ListAvailability(ctx, access.GroupID(), part.ID, coachUserID)
	if err != nil {
		return PartAvailability{}, fmt.Errorf("list availability: %w", err)
	}
	return PartAvailability{Season: season, Part: part, Windows: windows}, nil
}

// edit applies change to the coach's windows of the part, normalizes the
// result, and stores it.
func (s *CoachService) edit(ctx context.Context, actor GroupActor, part SeasonPart, coachUserID int64, change func([]AvailabilityWindow) ([]AvailabilityWindow, error)) error {
	err := s.coaches.EditAvailability(ctx, actor.Access.GroupID(), part.ID, coachUserID, s.now().Unix(), func(current []AvailabilityWindow) ([]AvailabilityWindow, error) {
		changed, err := change(slices.Clone(current))
		if err != nil {
			return nil, err
		}
		normalized := NormalizeAvailability(changed)
		if len(normalized) > MaxAvailabilityWindows {
			return nil, ErrTooManyAvailabilityWindows
		}
		return normalized, nil
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCoachNotFound), errors.Is(err, ErrSeasonNotFound), errors.Is(err, ErrAvailabilityNotFound), errors.Is(err, ErrTooManyAvailabilityWindows):
		return err
	default:
		return fmt.Errorf("edit availability: %w", err)
	}
}

func (s *CoachService) recordAvailability(ctx context.Context, actor GroupActor, coachUserID int64, part SeasonPart, change string) {
	if actor.Access.UserID() == coachUserID {
		return
	}
	s.record(ctx, actor, AuditActionCoachAvailabilityChanged, "coach "+strconv.FormatInt(coachUserID, 10)+"; part "+strconv.FormatInt(part.ID, 10)+"; "+change)
}

// buildAvailabilityWindow validates a window for the part.
func buildAvailabilityWindow(part SeasonPart, coachUserID int64, input AvailabilityInput) (AvailabilityWindow, error) {
	switch input.Week {
	case SeasonWeekAll:
	case SeasonWeekA, SeasonWeekB:
		if !part.AlternatingWeeks {
			return AvailabilityWindow{}, ErrAvailabilityWeek
		}
	default:
		return AvailabilityWindow{}, ErrAvailabilityWeek
	}
	if input.Weekday < 1 || input.Weekday > 7 || input.StartMinute < 0 || input.StartMinute >= input.EndMinute || input.EndMinute > minutesInDay ||
		input.StartMinute%availabilityMinuteStep != 0 || input.EndMinute%availabilityMinuteStep != 0 {
		return AvailabilityWindow{}, ErrInvalidAvailability
	}
	return AvailabilityWindow{
		GroupID: part.GroupID, SeasonPartID: part.ID, CoachUserID: coachUserID,
		Week: input.Week, Weekday: input.Weekday, StartMinute: input.StartMinute, EndMinute: input.EndMinute,
	}, nil
}

const minutesInDay = 24 * 60

// FormatMinute writes minutes after midnight as HH:MM.
func FormatMinute(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

func windowDetail(window AvailabilityWindow) string {
	return "week " + window.Week + " day " + strconv.Itoa(window.Weekday) + " " + FormatMinute(window.StartMinute) + "-" + FormatMinute(window.EndMinute)
}
