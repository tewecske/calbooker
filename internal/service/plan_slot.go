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

// Plan slot limits.
const (
	// MaxPlanSlotAge bounds the optional age range of a plan slot (years).
	MaxPlanSlotAge = 99
	// MaxPlanSlotTeams bounds the optional number of teams in a plan slot.
	MaxPlanSlotTeams = 99
	// MaxPlanSlotsPerPart bounds how many slots one season part's plan holds.
	MaxPlanSlotsPerPart = 500
)

// Stable plan audit action codes; the group is the target.
const (
	AuditActionPlanSlotCreated = "group.plan.slot_created"
	AuditActionPlanSlotUpdated = "group.plan.slot_updated"
	AuditActionPlanSlotDeleted = "group.plan.slot_deleted"
	AuditActionPlanCopied      = "group.plan.copied"
)

// Plan copy source kinds.
const (
	// PlanCopyPreviousSeason is the previous season's part at the same
	// position.
	PlanCopyPreviousSeason = "previous_season"
	// PlanCopyPreviousPart is the part before the target: part 1 of the same
	// season for part 2, the previous season's last part for part 1.
	PlanCopyPreviousPart = "previous_part"
)

var (
	// ErrPlanSlotNotFound identifies a plan slot that does not exist in the
	// group or that the account may not see.
	ErrPlanSlotNotFound = errors.New("service: plan slot not found")
	// ErrInvalidPlanSlotWeek identifies a week other than A, B, or all, or A
	// or B in a part without alternating weeks.
	ErrInvalidPlanSlotWeek = errors.New("service: invalid plan slot week")
	// ErrInvalidPlanSlotTime identifies a weekday outside 1..7 or a time
	// range that is not inside one day with its start before its end.
	ErrInvalidPlanSlotTime = errors.New("service: invalid plan slot time")
	// ErrInvalidPlanSlotAges identifies ages outside 1..MaxPlanSlotAge or a
	// minimum above the maximum.
	ErrInvalidPlanSlotAges = errors.New("service: invalid plan slot ages")
	// ErrInvalidPlanSlotTeams identifies a team limit outside
	// 1..MaxPlanSlotTeams.
	ErrInvalidPlanSlotTeams = errors.New("service: invalid plan slot team limit")
	// ErrPlanFull identifies a part whose plan already holds
	// MaxPlanSlotsPerPart slots.
	ErrPlanFull = errors.New("service: plan has too many slots")
	// ErrInvalidPlanCopy identifies a copy from the target part itself.
	ErrInvalidPlanCopy = errors.New("service: invalid plan copy source")
	// ErrPlanCopyNotConfirmed identifies a copy into a part that has slots
	// without confirming that they are replaced.
	ErrPlanCopyNotConfirmed = errors.New("service: plan copy would replace slots")
	// ErrNilPlanSlotRepository identifies missing plan slot persistence.
	ErrNilPlanSlotRepository = errors.New("service: nil plan slot repository")
)

// PlanSlot is one resource slot of a season part's weekly plan: when a
// resource is open for trainings, optionally for an age range and a number
// of teams. Times are minutes after local midnight; Weekday is ISO
// (1 = Monday); Week is SeasonWeekA, SeasonWeekB, or SeasonWeekAll.
type PlanSlot struct {
	ID           int64
	GroupID      int64
	SeasonPartID int64
	ResourceID   int64
	Week         string
	Weekday      int
	StartMinute  int
	EndMinute    int
	// MinAge, MaxAge, and MaxTeams are optional (nil).
	MinAge    *int
	MaxAge    *int
	MaxTeams  *int
	CreatedAt int64
	Version   int64
}

// InWeek reports whether the slot applies to week (SeasonWeekA or
// SeasonWeekB; SeasonWeekAll matches every slot).
func (s PlanSlot) InWeek(week string) bool {
	return week == SeasonWeekAll || s.Week == SeasonWeekAll || s.Week == week
}

// teams is how many teams the slot counts for in capacity warnings: its
// limit, or one when it has none.
func (s PlanSlot) teams() int {
	if s.MaxTeams != nil {
		return *s.MaxTeams
	}
	return 1
}

// sameContent reports whether two slots plan the same thing (everything but
// identity and revision).
func (s PlanSlot) sameContent(other PlanSlot) bool {
	return s.ResourceID == other.ResourceID && s.Week == other.Week && s.Weekday == other.Weekday &&
		s.StartMinute == other.StartMinute && s.EndMinute == other.EndMinute &&
		equalOptional(s.MinAge, other.MinAge) && equalOptional(s.MaxAge, other.MaxAge) && equalOptional(s.MaxTeams, other.MaxTeams)
}

func equalOptional(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// PlanSlotInput is the editable part of a plan slot. Week "" is
// SeasonWeekAll; MinAge, MaxAge, and MaxTeams 0 are none.
type PlanSlotInput struct {
	ResourceID  int64
	Week        string
	Weekday     int
	StartMinute int
	EndMinute   int
	MinAge      int
	MaxAge      int
	MaxTeams    int
}

// PlanSlotMove is a move or resize from the week grid: the slot keeps its
// week, ages, and team limit.
type PlanSlotMove struct {
	ResourceID  int64
	Weekday     int
	StartMinute int
	EndMinute   int
}

// PlanWarning is a stretch of time in which the slots of a resource allow
// more teams than the resource's concurrent-team limit. Slots without a
// team limit count as one team. Week is SeasonWeekA or SeasonWeekB in
// alternating parts (SeasonWeekAll when both weeks have the same
// warning) and SeasonWeekAll otherwise.
type PlanWarning struct {
	ResourceID  int64
	Week        string
	Weekday     int
	StartMinute int
	EndMinute   int
	// Teams is the most teams the slots allow at one moment of the stretch;
	// Limit is the resource's max_concurrent_teams.
	Teams   int
	Limit   int
	SlotIDs []int64
}

// PlanCopySource is a part whose slots can be copied into another part.
type PlanCopySource struct {
	Kind      string
	Season    Season
	Part      SeasonPart
	SlotCount int
}

// PlanCopyResult reports what a copy did.
type PlanCopyResult struct {
	// Copied slots were created in the target; Replaced slots were removed
	// from it first.
	Copied   int
	Replaced int
	// SkippedArchived slots were on archived resources and not copied.
	SkippedArchived int
	// Merged slots became duplicates of another copied slot (A and B slots
	// copied into a part without alternating weeks) and were copied once.
	Merged int
}

// PlanBoard is everything the plan page shows for one season part.
type PlanBoard struct {
	// Seasons are the group's seasons, latest first, for the part picker.
	Seasons []Season
	// Found is false when the group has no season part yet; the other
	// fields are then empty.
	Found  bool
	Season Season
	Part   SeasonPart
	Slots  []PlanSlot
	// Resources are every resource of the group (archived ones included,
	// for slots that still reference them), active ones first.
	Resources   []Resource
	Warnings    []PlanWarning
	CopySources []PlanCopySource
}

// PlanSeasonSource is the season lookup the plan needs
// (SeasonService implements it).
type PlanSeasonSource interface {
	List(ctx context.Context, access *GroupAccess) ([]Season, error)
	FindPart(ctx context.Context, access *GroupAccess, partID int64) (Season, SeasonPart, error)
	Today(ctx context.Context, access *GroupAccess) (time.Time, error)
}

// PlanResourceSource is the resource lookup the plan needs
// (ResourceService implements it).
type PlanResourceSource interface {
	List(ctx context.Context, access *GroupAccess) ([]Resource, error)
	Find(ctx context.Context, access *GroupAccess, resourceID int64) (Resource, error)
}

// PlanSlotRepository persists plan slots. Every method is scoped to one
// group.
type PlanSlotRepository interface {
	// ListPlanSlots returns a part's slots by weekday, start, resource, and
	// ID.
	ListPlanSlots(ctx context.Context, groupID, partID int64) ([]PlanSlot, error)
	// FindPlanSlot returns ErrPlanSlotNotFound for a slot outside the group.
	FindPlanSlot(ctx context.Context, groupID, slotID int64) (PlanSlot, error)
	// CreatePlanSlot returns ErrSeasonNotFound or ErrResourceNotFound from
	// foreign keys.
	CreatePlanSlot(context.Context, PlanSlot) (PlanSlot, error)
	// UpdatePlanSlot writes the slot at the expected revision; stale writes
	// classify through ClassifyStaleWrite.
	UpdatePlanSlot(ctx context.Context, slot PlanSlot, expectedVersion int64) (PlanSlot, error)
	// DeletePlanSlot removes the slot at the expected revision; stale
	// writes classify through ClassifyStaleWrite.
	DeletePlanSlot(ctx context.Context, groupID, slotID, expectedVersion int64) error
	// ReplacePlanSlots removes every slot of the part and inserts slots in
	// one transaction. It returns how many slots it removed.
	ReplacePlanSlots(ctx context.Context, groupID, partID int64, slots []PlanSlot) (int, error)
}

// PlanSlotService owns the resource slots of the weekly plans. Admins,
// managers, and site administrators edit; coaches read. Authorization comes
// from the caller's GroupAccess.
type PlanSlotService struct {
	slots     PlanSlotRepository
	seasons   PlanSeasonSource
	resources PlanResourceSource
	users     UserRepository
	auditor   AuditRecorder
	now       func() time.Time
}

// NewPlanSlotService constructs plan slot use cases with explicit
// dependencies.
func NewPlanSlotService(slots PlanSlotRepository, seasons PlanSeasonSource, resources PlanResourceSource, users UserRepository) (*PlanSlotService, error) {
	if slots == nil {
		return nil, ErrNilPlanSlotRepository
	}
	if seasons == nil {
		return nil, ErrNilSeasonRepository
	}
	if resources == nil {
		return nil, ErrNilResourceRepository
	}
	if users == nil {
		return nil, ErrNilUserRepository
	}
	return &PlanSlotService{slots: slots, seasons: seasons, resources: resources, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *PlanSlotService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Slots returns the slots of a season part, by weekday, start, resource,
// and ID. Admins, managers, site administrators, and coaches. The training
// plan (#14) builds on it.
func (s *PlanSlotService) Slots(ctx context.Context, access *GroupAccess, partID int64) ([]PlanSlot, error) {
	if err := s.viewable(ctx, access); err != nil {
		return nil, err
	}
	if _, _, err := s.seasons.FindPart(ctx, access, partID); err != nil {
		return nil, err
	}
	return s.list(ctx, access.GroupID(), partID)
}

// Slot returns one slot of the group. Same access as Slots.
func (s *PlanSlotService) Slot(ctx context.Context, access *GroupAccess, slotID int64) (PlanSlot, error) {
	if err := s.viewable(ctx, access); err != nil {
		return PlanSlot{}, err
	}
	if slotID <= 0 {
		return PlanSlot{}, ErrPlanSlotNotFound
	}
	return s.slots.FindPlanSlot(ctx, access.GroupID(), slotID)
}

// Board loads the plan page of a season part: the seasons for the picker,
// the part's slots, the group's resources, capacity warnings, and the parts
// its plan can be copied from. partID 0 picks the default part: the one
// containing today, else the first part of the season containing today,
// else the next part to start, else the latest part.
func (s *PlanSlotService) Board(ctx context.Context, access *GroupAccess, partID int64) (PlanBoard, error) {
	if err := s.viewable(ctx, access); err != nil {
		return PlanBoard{}, err
	}
	seasons, err := s.seasons.List(ctx, access)
	if err != nil {
		return PlanBoard{}, fmt.Errorf("list seasons: %w", err)
	}
	board := PlanBoard{Seasons: seasons}
	if partID == 0 {
		today, err := s.seasons.Today(ctx, access)
		if err != nil {
			return PlanBoard{}, fmt.Errorf("find today: %w", err)
		}
		season, part, ok := defaultPlanPart(seasons, today)
		if !ok {
			return board, nil
		}
		board.Season, board.Part = season, part
	} else {
		season, part, err := s.seasons.FindPart(ctx, access, partID)
		if err != nil {
			return PlanBoard{}, err
		}
		board.Season, board.Part = season, part
	}
	board.Found = true
	if board.Slots, err = s.list(ctx, access.GroupID(), board.Part.ID); err != nil {
		return PlanBoard{}, err
	}
	if board.Resources, err = s.resources.List(ctx, access); err != nil {
		return PlanBoard{}, fmt.Errorf("list resources: %w", err)
	}
	board.Warnings = PlanWarnings(board.Part, board.Slots, board.Resources)
	for _, source := range planCopyCandidates(seasons, board.Season, board.Part) {
		slots, err := s.list(ctx, access.GroupID(), source.Part.ID)
		if err != nil {
			return PlanBoard{}, err
		}
		source.SlotCount = len(slots)
		board.CopySources = append(board.CopySources, source)
	}
	return board, nil
}

// Create adds a slot to a season part. Admins, managers, and site
// administrators. The resource must be active.
func (s *PlanSlotService) Create(ctx context.Context, actor GroupActor, partID int64, input PlanSlotInput) (PlanSlot, error) {
	if err := s.editable(ctx, actor); err != nil {
		return PlanSlot{}, err
	}
	_, part, err := s.seasons.FindPart(ctx, actor.Access, partID)
	if err != nil {
		return PlanSlot{}, err
	}
	slot, err := buildPlanSlot(part, input)
	if err != nil {
		return PlanSlot{}, err
	}
	if err := s.usableResource(ctx, actor.Access, slot.ResourceID); err != nil {
		return PlanSlot{}, err
	}
	existing, err := s.list(ctx, actor.Access.GroupID(), part.ID)
	if err != nil {
		return PlanSlot{}, err
	}
	if len(existing) >= MaxPlanSlotsPerPart {
		return PlanSlot{}, ErrPlanFull
	}
	slot.GroupID = actor.Access.GroupID()
	slot.CreatedAt = s.now().Unix()
	created, err := s.slots.CreatePlanSlot(ctx, slot)
	if err != nil {
		if errors.Is(err, ErrSeasonNotFound) || errors.Is(err, ErrResourceNotFound) {
			return PlanSlot{}, err
		}
		return PlanSlot{}, fmt.Errorf("create plan slot: %w", err)
	}
	s.record(ctx, actor, AuditActionPlanSlotCreated, planSlotDetail(created))
	return created, nil
}

// Update replaces a slot's resource, week, time, ages, and team limit at
// the expected revision. A slot may stay on an archived resource; moving it
// to another resource needs an active one.
func (s *PlanSlotService) Update(ctx context.Context, actor GroupActor, slotID, expectedVersion int64, input PlanSlotInput) (PlanSlot, error) {
	current, part, err := s.findEditable(ctx, actor, slotID, expectedVersion)
	if err != nil {
		return PlanSlot{}, err
	}
	changed, err := buildPlanSlot(part, input)
	if err != nil {
		return PlanSlot{}, err
	}
	return s.write(ctx, actor, current, changed, expectedVersion)
}

// Move changes a slot's resource, weekday, and time from the week grid at
// the expected revision, keeping its ages and team limit. In a part that no
// longer alternates weeks, a moved A or B slot becomes an every-week slot.
func (s *PlanSlotService) Move(ctx context.Context, actor GroupActor, slotID, expectedVersion int64, move PlanSlotMove) (PlanSlot, error) {
	current, part, err := s.findEditable(ctx, actor, slotID, expectedVersion)
	if err != nil {
		return PlanSlot{}, err
	}
	week := current.Week
	if !part.AlternatingWeeks {
		week = SeasonWeekAll
	}
	changed, err := buildPlanSlot(part, PlanSlotInput{
		ResourceID: move.ResourceID, Week: week, Weekday: move.Weekday, StartMinute: move.StartMinute, EndMinute: move.EndMinute,
		MinAge: optionalValue(current.MinAge), MaxAge: optionalValue(current.MaxAge), MaxTeams: optionalValue(current.MaxTeams),
	})
	if err != nil {
		return PlanSlot{}, err
	}
	return s.write(ctx, actor, current, changed, expectedVersion)
}

// Delete removes a slot at the expected revision.
func (s *PlanSlotService) Delete(ctx context.Context, actor GroupActor, slotID, expectedVersion int64) error {
	current, _, err := s.findEditable(ctx, actor, slotID, expectedVersion)
	if err != nil {
		return err
	}
	if err := s.slots.DeletePlanSlot(ctx, actor.Access.GroupID(), slotID, expectedVersion); err != nil {
		return classifyPlanSlotWrite(err, "delete plan slot")
	}
	s.record(ctx, actor, AuditActionPlanSlotDeleted, planSlotDetail(current))
	return nil
}

// Copy replaces the slots of the target part with copies of the source
// part's slots. Copies keep resource, weekday, time, ages, and team limit.
// Weeks are kept when the target alternates weeks; otherwise every copy is
// an every-week slot and duplicates are copied once. Slots on archived
// resources are skipped. A target with slots needs replace (the user
// confirmed); copying again redoes the copy.
func (s *PlanSlotService) Copy(ctx context.Context, actor GroupActor, targetPartID, sourcePartID int64, replace bool) (PlanCopyResult, error) {
	if err := s.editable(ctx, actor); err != nil {
		return PlanCopyResult{}, err
	}
	if targetPartID <= 0 || sourcePartID <= 0 {
		return PlanCopyResult{}, ErrSeasonNotFound
	}
	if targetPartID == sourcePartID {
		return PlanCopyResult{}, ErrInvalidPlanCopy
	}
	_, target, err := s.seasons.FindPart(ctx, actor.Access, targetPartID)
	if err != nil {
		return PlanCopyResult{}, err
	}
	_, source, err := s.seasons.FindPart(ctx, actor.Access, sourcePartID)
	if err != nil {
		return PlanCopyResult{}, err
	}
	groupID := actor.Access.GroupID()
	existing, err := s.list(ctx, groupID, target.ID)
	if err != nil {
		return PlanCopyResult{}, err
	}
	if len(existing) > 0 && !replace {
		return PlanCopyResult{}, ErrPlanCopyNotConfirmed
	}
	sourceSlots, err := s.list(ctx, groupID, source.ID)
	if err != nil {
		return PlanCopyResult{}, err
	}
	resources, err := s.resources.List(ctx, actor.Access)
	if err != nil {
		return PlanCopyResult{}, fmt.Errorf("list resources: %w", err)
	}
	copies, result := CopyPlanSlots(sourceSlots, target, resources, s.now().Unix())
	removed, err := s.slots.ReplacePlanSlots(ctx, groupID, target.ID, copies)
	if err != nil {
		if errors.Is(err, ErrSeasonNotFound) || errors.Is(err, ErrResourceNotFound) {
			return PlanCopyResult{}, err
		}
		return PlanCopyResult{}, fmt.Errorf("copy plan slots: %w", err)
	}
	result.Replaced = removed
	s.record(ctx, actor, AuditActionPlanCopied, "part "+strconv.FormatInt(target.ID, 10)+" from part "+strconv.FormatInt(source.ID, 10)+
		"; copied "+strconv.Itoa(result.Copied)+"; replaced "+strconv.Itoa(result.Replaced)+
		"; skipped archived "+strconv.Itoa(result.SkippedArchived)+"; merged "+strconv.Itoa(result.Merged))
	return result, nil
}

// CopyPlanSlots maps source slots into the target part (see
// PlanSlotService.Copy) and returns the new slots, without IDs, and the
// counts. resources must hold every resource the slots reference; unknown
// resources count as archived.
func CopyPlanSlots(source []PlanSlot, target SeasonPart, resources []Resource, createdAt int64) ([]PlanSlot, PlanCopyResult) {
	active := make(map[int64]bool, len(resources))
	for _, resource := range resources {
		active[resource.ID] = !resource.Archived()
	}
	var result PlanCopyResult
	copies := make([]PlanSlot, 0, len(source))
	for _, slot := range source {
		if !active[slot.ResourceID] {
			result.SkippedArchived++
			continue
		}
		copied := PlanSlot{
			GroupID:      target.GroupID,
			SeasonPartID: target.ID,
			ResourceID:   slot.ResourceID,
			Week:         slot.Week,
			Weekday:      slot.Weekday,
			StartMinute:  slot.StartMinute,
			EndMinute:    slot.EndMinute,
			MinAge:       cloneOptional(slot.MinAge),
			MaxAge:       cloneOptional(slot.MaxAge),
			MaxTeams:     cloneOptional(slot.MaxTeams),
			CreatedAt:    createdAt,
		}
		if !target.AlternatingWeeks {
			copied.Week = SeasonWeekAll
		}
		if slices.ContainsFunc(copies, copied.sameContent) {
			result.Merged++
			continue
		}
		copies = append(copies, copied)
	}
	result.Copied = len(copies)
	return copies, result
}

// PlanWarnings finds the stretches in which a part's slots allow more
// teams on a resource than its concurrent-team limit. Slots without a team
// limit count as one team; resources without a limit never warn. In a part
// with alternating weeks, weeks A and B are checked separately (every-week
// slots count in both) and a warning both weeks share is reported once as
// SeasonWeekAll.
func PlanWarnings(part SeasonPart, slots []PlanSlot, resources []Resource) []PlanWarning {
	limits := make(map[int64]int, len(resources))
	for _, resource := range resources {
		if resource.MaxConcurrentTeams != nil {
			limits[resource.ID] = *resource.MaxConcurrentTeams
		}
	}
	type dayKey struct {
		resourceID int64
		weekday    int
	}
	days := map[dayKey][]PlanSlot{}
	for _, slot := range slots {
		if _, limited := limits[slot.ResourceID]; limited {
			key := dayKey{slot.ResourceID, slot.Weekday}
			days[key] = append(days[key], slot)
		}
	}
	weeks := []string{SeasonWeekAll}
	if part.AlternatingWeeks {
		weeks = []string{SeasonWeekA, SeasonWeekB}
	}
	var warnings []PlanWarning
	for key, daySlots := range days {
		var perWeek [][]PlanWarning
		for _, week := range weeks {
			var weekSlots []PlanSlot
			for _, slot := range daySlots {
				if !part.AlternatingWeeks || slot.InWeek(week) {
					weekSlots = append(weekSlots, slot)
				}
			}
			perWeek = append(perWeek, capacityWarnings(weekSlots, key.resourceID, key.weekday, week, limits[key.resourceID]))
		}
		if len(perWeek) == 2 {
			for _, inA := range perWeek[0] {
				if index := slices.IndexFunc(perWeek[1], func(inB PlanWarning) bool { return sameWarning(inA, inB) }); index >= 0 {
					inA.Week = SeasonWeekAll
					perWeek[1] = slices.Delete(perWeek[1], index, index+1)
				}
				warnings = append(warnings, inA)
			}
			warnings = append(warnings, perWeek[1]...)
			continue
		}
		warnings = append(warnings, perWeek[0]...)
	}
	slices.SortFunc(warnings, func(a, b PlanWarning) int {
		return cmp.Or(
			cmp.Compare(a.Weekday, b.Weekday),
			cmp.Compare(a.StartMinute, b.StartMinute),
			cmp.Compare(a.ResourceID, b.ResourceID),
			cmp.Compare(a.Week, b.Week),
		)
	})
	return warnings
}

func sameWarning(a, b PlanWarning) bool {
	return a.ResourceID == b.ResourceID && a.Weekday == b.Weekday && a.StartMinute == b.StartMinute &&
		a.EndMinute == b.EndMinute && a.Teams == b.Teams && slices.Equal(a.SlotIDs, b.SlotIDs)
}

// capacityWarnings sweeps one resource's slots of one day and week and
// returns the merged stretches above limit.
func capacityWarnings(slots []PlanSlot, resourceID int64, weekday int, week string, limit int) []PlanWarning {
	if len(slots) == 0 {
		return nil
	}
	points := make([]int, 0, 2*len(slots))
	for _, slot := range slots {
		points = append(points, slot.StartMinute, slot.EndMinute)
	}
	slices.Sort(points)
	points = slices.Compact(points)
	var warnings []PlanWarning
	var current *PlanWarning
	for index := 0; index+1 < len(points); index++ {
		from, to := points[index], points[index+1]
		teams := 0
		var ids []int64
		for _, slot := range slots {
			if slot.StartMinute < to && from < slot.EndMinute {
				teams += slot.teams()
				ids = append(ids, slot.ID)
			}
		}
		if teams <= limit {
			current = nil
			continue
		}
		if current != nil && current.EndMinute == from {
			current.EndMinute = to
			current.Teams = max(current.Teams, teams)
			for _, id := range ids {
				if !slices.Contains(current.SlotIDs, id) {
					current.SlotIDs = append(current.SlotIDs, id)
				}
			}
			continue
		}
		warnings = append(warnings, PlanWarning{
			ResourceID: resourceID, Week: week, Weekday: weekday, StartMinute: from, EndMinute: to,
			Teams: teams, Limit: limit, SlotIDs: ids,
		})
		current = &warnings[len(warnings)-1]
	}
	for index := range warnings {
		slices.Sort(warnings[index].SlotIDs)
	}
	return warnings
}

// defaultPlanPart picks the part a plan page opens with (see Board).
func defaultPlanPart(seasons []Season, today time.Time) (Season, SeasonPart, bool) {
	for _, season := range seasons {
		if part, ok := season.PartOn(today); ok {
			return season, part, true
		}
	}
	for _, season := range seasons {
		if season.Contains(today) && len(season.Parts) > 0 {
			for _, part := range season.Parts {
				if part.StartsOn.After(today) {
					return season, part, true
				}
			}
			return season, season.Parts[len(season.Parts)-1], true
		}
	}
	var (
		next       SeasonPart
		nextSeason Season
		found      bool
	)
	for _, season := range seasons {
		for _, part := range season.Parts {
			if part.StartsOn.After(today) && (!found || part.StartsOn.Before(next.StartsOn)) {
				next, nextSeason, found = part, season, true
			}
		}
	}
	if found {
		return nextSeason, next, true
	}
	// Seasons are latest first: the latest part is the last part of the
	// first season that has parts.
	for _, season := range seasons {
		if len(season.Parts) > 0 {
			return season, season.Parts[len(season.Parts)-1], true
		}
	}
	return Season{}, SeasonPart{}, false
}

// planCopyCandidates returns the parts a target part's plan is usually
// copied from: the previous season's part at the same position, and the
// part before the target.
func planCopyCandidates(seasons []Season, season Season, part SeasonPart) []PlanCopySource {
	var previous *Season
	for index := range seasons {
		candidate := &seasons[index]
		if candidate.ID != season.ID && candidate.StartsOn.Before(season.StartsOn) && (previous == nil || candidate.StartsOn.After(previous.StartsOn)) {
			previous = candidate
		}
	}
	var sources []PlanCopySource
	if previous != nil {
		for _, candidate := range previous.Parts {
			if candidate.Position == part.Position {
				sources = append(sources, PlanCopySource{Kind: PlanCopyPreviousSeason, Season: *previous, Part: candidate})
			}
		}
	}
	var before *PlanCopySource
	for _, candidate := range season.Parts {
		if candidate.Position == part.Position-1 {
			before = &PlanCopySource{Kind: PlanCopyPreviousPart, Season: season, Part: candidate}
		}
	}
	if before == nil && previous != nil && len(previous.Parts) > 0 {
		last := previous.Parts[len(previous.Parts)-1]
		before = &PlanCopySource{Kind: PlanCopyPreviousPart, Season: *previous, Part: last}
	}
	if before != nil && !slices.ContainsFunc(sources, func(source PlanCopySource) bool { return source.Part.ID == before.Part.ID }) {
		sources = append(sources, *before)
	}
	return sources
}

func (s *PlanSlotService) write(ctx context.Context, actor GroupActor, current, changed PlanSlot, expectedVersion int64) (PlanSlot, error) {
	if changed.ResourceID != current.ResourceID {
		if err := s.usableResource(ctx, actor.Access, changed.ResourceID); err != nil {
			return PlanSlot{}, err
		}
	}
	changed.ID, changed.GroupID, changed.SeasonPartID, changed.CreatedAt, changed.Version = current.ID, current.GroupID, current.SeasonPartID, current.CreatedAt, current.Version
	updated, err := s.slots.UpdatePlanSlot(ctx, changed, expectedVersion)
	if err != nil {
		return PlanSlot{}, classifyPlanSlotWrite(err, "update plan slot")
	}
	s.record(ctx, actor, AuditActionPlanSlotUpdated, planSlotDetail(updated))
	return updated, nil
}

func classifyPlanSlotWrite(err error, operation string) error {
	switch {
	case errors.Is(err, ErrRecordNotFound), errors.Is(err, ErrPlanSlotNotFound):
		// A slot that vanished is not-found, never a conflict.
		return ErrPlanSlotNotFound
	case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrResourceNotFound):
		return err
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func (s *PlanSlotService) usableResource(ctx context.Context, access *GroupAccess, resourceID int64) error {
	resource, err := s.resources.Find(ctx, access, resourceID)
	if err != nil {
		return err
	}
	if resource.Archived() {
		return ErrResourceArchived
	}
	return nil
}

func (s *PlanSlotService) list(ctx context.Context, groupID, partID int64) ([]PlanSlot, error) {
	slots, err := s.slots.ListPlanSlots(ctx, groupID, partID)
	if err != nil {
		return nil, fmt.Errorf("list plan slots: %w", err)
	}
	return slots, nil
}

func (s *PlanSlotService) viewable(ctx context.Context, access *GroupAccess) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if !access.CanView() {
		return ErrGroupNotFound
	}
	if !access.CanViewPlan() {
		return ErrGroupForbidden
	}
	return nil
}

func (s *PlanSlotService) editable(ctx context.Context, actor GroupActor) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	return authorizeGroupAction(actor, actor.Access.CanManagePlan())
}

func (s *PlanSlotService) findEditable(ctx context.Context, actor GroupActor, slotID, expectedVersion int64) (PlanSlot, SeasonPart, error) {
	if err := s.editable(ctx, actor); err != nil {
		return PlanSlot{}, SeasonPart{}, err
	}
	if slotID <= 0 {
		return PlanSlot{}, SeasonPart{}, ErrPlanSlotNotFound
	}
	if expectedVersion < 0 {
		return PlanSlot{}, SeasonPart{}, ErrInvalidGroup
	}
	slot, err := s.slots.FindPlanSlot(ctx, actor.Access.GroupID(), slotID)
	if err != nil {
		return PlanSlot{}, SeasonPart{}, err
	}
	_, part, err := s.seasons.FindPart(ctx, actor.Access, slot.SeasonPartID)
	if err != nil {
		return PlanSlot{}, SeasonPart{}, err
	}
	return slot, part, nil
}

func (s *PlanSlotService) record(ctx context.Context, actor GroupActor, action, detail string) {
	if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
		detail += " (site administrator)"
	}
	recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), action, detail)
}

func (s *PlanSlotService) validate(ctx context.Context) error {
	if s == nil || s.slots == nil || s.seasons == nil || s.resources == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil plan context")
	}
	return nil
}

func planSlotDetail(slot PlanSlot) string {
	detail := "part " + strconv.FormatInt(slot.SeasonPartID, 10) + "; slot " + strconv.FormatInt(slot.ID, 10) +
		"; resource " + strconv.FormatInt(slot.ResourceID, 10) + "; week " + slot.Week +
		"; day " + strconv.Itoa(slot.Weekday) + " " + FormatPlanMinute(slot.StartMinute) + "-" + FormatPlanMinute(slot.EndMinute)
	if slot.MinAge != nil || slot.MaxAge != nil {
		detail += "; ages " + optionalText(slot.MinAge) + "-" + optionalText(slot.MaxAge)
	}
	if slot.MaxTeams != nil {
		detail += "; max teams " + strconv.Itoa(*slot.MaxTeams)
	}
	return detail
}

// FormatPlanMinute formats minutes after midnight as HH:MM.
func FormatPlanMinute(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

func optionalText(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func optionalValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func cloneOptional(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// buildPlanSlot validates slot input for part. Ages and the team limit 0
// are none.
func buildPlanSlot(part SeasonPart, input PlanSlotInput) (PlanSlot, error) {
	if input.ResourceID <= 0 {
		return PlanSlot{}, ErrResourceNotFound
	}
	week := input.Week
	if week == "" {
		week = SeasonWeekAll
	}
	switch week {
	case SeasonWeekAll:
	case SeasonWeekA, SeasonWeekB:
		if !part.AlternatingWeeks {
			return PlanSlot{}, ErrInvalidPlanSlotWeek
		}
	default:
		return PlanSlot{}, ErrInvalidPlanSlotWeek
	}
	if input.Weekday < 1 || input.Weekday > 7 || input.StartMinute < 0 || input.StartMinute >= input.EndMinute || input.EndMinute > minutesInDay {
		return PlanSlot{}, ErrInvalidPlanSlotTime
	}
	slot := PlanSlot{
		SeasonPartID: part.ID,
		ResourceID:   input.ResourceID,
		Week:         week,
		Weekday:      input.Weekday,
		StartMinute:  input.StartMinute,
		EndMinute:    input.EndMinute,
	}
	for _, age := range []int{input.MinAge, input.MaxAge} {
		if age < 0 || age > MaxPlanSlotAge {
			return PlanSlot{}, ErrInvalidPlanSlotAges
		}
	}
	if input.MinAge > 0 && input.MaxAge > 0 && input.MinAge > input.MaxAge {
		return PlanSlot{}, ErrInvalidPlanSlotAges
	}
	if input.MaxTeams < 0 || input.MaxTeams > MaxPlanSlotTeams {
		return PlanSlot{}, ErrInvalidPlanSlotTeams
	}
	slot.MinAge = positiveOptional(input.MinAge)
	slot.MaxAge = positiveOptional(input.MaxAge)
	slot.MaxTeams = positiveOptional(input.MaxTeams)
	return slot, nil
}

const minutesInDay = 24 * 60

func positiveOptional(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}
