package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AuditActionEventsGenerated records a generation or regeneration of events
// from the training plan: the part, the weeks, and counts only.
const AuditActionEventsGenerated = "group.plan.events_generated"

// MaxGenerationWeeks is how many consecutive weeks one generation covers.
const MaxGenerationWeeks = 2

var (
	// ErrInvalidGenerationWeek identifies a week that does not start on a
	// Monday, a week count outside 1..MaxGenerationWeeks, or a week with no
	// day inside the season part.
	ErrInvalidGenerationWeek = errors.New("service: invalid generation week")
	// ErrNilEventGenerationRepository identifies missing persistence.
	ErrNilEventGenerationRepository = errors.New("service: nil event generation repository")
	// ErrNilEventGenerationSource identifies a missing season, resource, or
	// team source.
	ErrNilEventGenerationSource = errors.New("service: nil event generation source")
)

// Generation item actions.
const (
	// GenerationCreate: the event does not exist yet and will be created.
	GenerationCreate = "create"
	// GenerationUnchanged: the generated event already matches the plan.
	GenerationUnchanged = "unchanged"
	// GenerationRebuild: the generated event differs from the plan, was not
	// edited by hand, and has no participants; it is replaced.
	GenerationRebuild = "rebuild"
	// GenerationRemove: the plan no longer has the training for that week;
	// its untouched event is removed.
	GenerationRemove = "remove"
	// GenerationKeep: the event was edited by hand or has participants and
	// is left alone; the admin fixes it by hand.
	GenerationKeep = "keep"
	// GenerationSkip: the plan's training cannot be generated.
	GenerationSkip = "skip"
)

// Generation reasons.
const (
	// GenerationReasonEdited: the event was edited or cancelled by hand.
	GenerationReasonEdited = "edited"
	// GenerationReasonParticipants: somebody answered or attended.
	GenerationReasonParticipants = "participants"
	// GenerationReasonArchivedTeam: the training's team is archived.
	GenerationReasonArchivedTeam = "archived_team"
	// GenerationReasonArchivedResource: the training's resource is archived.
	GenerationReasonArchivedResource = "archived_resource"
	// GenerationReasonTimeSkipped: the start or end does not exist on that
	// day because of a daylight-saving change.
	GenerationReasonTimeSkipped = "time_skipped"
)

// GeneratedEvent is an event generated earlier, with whether anybody has a
// participant row on it.
type GeneratedEvent struct {
	Event           Event
	HasParticipants bool
}

// GenerationState is what the generation decides on: the group's time zone,
// the part's saved trainings, and the events generated for the chosen weeks
// (of every part; PlanGeneration keeps this part's).
type GenerationState struct {
	TimeZone  string
	Trainings []PlanTraining
	Existing  []GeneratedEvent
}

// GenerationPlan is what to write: events to create and event IDs to
// delete.
type GenerationPlan struct {
	Create []Event
	Delete []int64
}

// EventGenerationRepository reads and writes generated events. Every method
// is scoped to one group.
type EventGenerationRepository interface {
	// Generate reads the state of the part for the weeks (Mondays) and calls
	// plan with it. With apply it first locks the season part row and the
	// generated events (so participant writes and other generations wait),
	// then deletes and creates what plan returns in one transaction; without
	// it nothing is locked or written. It returns ErrSeasonNotFound when the
	// part is gone, an error from plan unchanged (nothing written), and
	// ErrTeamNotFound or ErrResourceNotFound from foreign keys.
	Generate(ctx context.Context, groupID, partID int64, weeks []time.Time, apply bool, plan func(GenerationState) (GenerationPlan, error)) error
}

// GenerationRequest chooses the weeks: Weeks consecutive weeks starting
// with the week whose Monday is Week (0 means one).
type GenerationRequest struct {
	PartID int64
	Week   time.Time
	Weeks  int
}

// GenerationItem is one line of a preview or result.
type GenerationItem struct {
	Action string
	Reason string
	// Week is the Monday of the week.
	Week         time.Time
	TrainingID   int64
	EventID      int64
	TeamID       int64
	TeamName     string
	ResourceName string
	// StartsAt and EndsAt are instants; show them in the group's time zone.
	StartsAt time.Time
	EndsAt   time.Time
}

// GenerationCounts counts items by action.
type GenerationCounts struct {
	Create, Unchanged, Rebuild, Remove, Keep, Skip int
}

// Changed reports whether anything is written.
func (c GenerationCounts) Changed() bool {
	return c.Create+c.Rebuild+c.Remove > 0
}

// GenerationPreview is what a generation does (or did).
type GenerationPreview struct {
	Season   Season
	Part     SeasonPart
	TimeZone string
	Weeks    []time.Time
	Items    []GenerationItem
	Counts   GenerationCounts
}

// GenerationOptions is the choice the generation page offers.
type GenerationOptions struct {
	// Seasons are the group's seasons, latest first, for the part picker.
	Seasons []Season
	Found   bool
	Season  Season
	Part    SeasonPart
	// Weeks are the Mondays of the weeks that touch the part; DefaultWeek is
	// next week's Monday, clamped into them.
	Weeks       []time.Time
	DefaultWeek time.Time
}

// EventGenerationService generates the real events of a week from the
// weekly training plan. Admins, managers, and site administrators only.
type EventGenerationService struct {
	events    EventGenerationRepository
	seasons   PlanSeasonSource
	resources PlanResourceSource
	teams     TrainingTeamSource
	users     UserRepository
	auditor   AuditRecorder
	now       func() time.Time
}

// NewEventGenerationService constructs event generation with explicit
// dependencies.
func NewEventGenerationService(events EventGenerationRepository, seasons PlanSeasonSource, resources PlanResourceSource, teams TrainingTeamSource, users UserRepository) (*EventGenerationService, error) {
	switch {
	case events == nil:
		return nil, ErrNilEventGenerationRepository
	case seasons == nil, resources == nil, teams == nil:
		return nil, ErrNilEventGenerationSource
	case users == nil:
		return nil, ErrNilUserRepository
	}
	return &EventGenerationService{events: events, seasons: seasons, resources: resources, teams: teams, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *EventGenerationService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Options returns the seasons and the weeks one part offers. partID 0 picks
// the default part like the plan pages do.
func (s *EventGenerationService) Options(ctx context.Context, access *GroupAccess, partID int64) (GenerationOptions, error) {
	if err := s.authorize(ctx, access); err != nil {
		return GenerationOptions{}, err
	}
	seasons, err := s.seasons.List(ctx, access)
	if err != nil {
		return GenerationOptions{}, fmt.Errorf("list seasons: %w", err)
	}
	options := GenerationOptions{Seasons: seasons}
	today, err := s.seasons.Today(ctx, access)
	if err != nil {
		return GenerationOptions{}, fmt.Errorf("find today: %w", err)
	}
	if partID == 0 {
		season, part, ok := defaultPlanPart(seasons, today)
		if !ok {
			return options, nil
		}
		options.Season, options.Part = season, part
	} else {
		season, part, err := s.seasons.FindPart(ctx, access, partID)
		if err != nil {
			return GenerationOptions{}, err
		}
		options.Season, options.Part = season, part
	}
	options.Found = true
	options.Weeks = GenerationWeeks(options.Part)
	if len(options.Weeks) > 0 {
		next := mondayOf(today).AddDate(0, 0, 7)
		options.DefaultWeek = options.Weeks[0]
		for _, week := range options.Weeks {
			if !week.After(next) {
				options.DefaultWeek = week
			}
		}
	}
	return options, nil
}

// GenerationWeeks returns the Mondays of the weeks that have a day inside
// the part.
func GenerationWeeks(part SeasonPart) []time.Time {
	var weeks []time.Time
	for week := mondayOf(part.StartsOn); !week.After(part.EndsOn); week = week.AddDate(0, 0, 7) {
		weeks = append(weeks, week)
	}
	return weeks
}

// Preview shows what Generate would do, without writing or locking.
func (s *EventGenerationService) Preview(ctx context.Context, access *GroupAccess, request GenerationRequest) (GenerationPreview, error) {
	if err := s.authorize(ctx, access); err != nil {
		return GenerationPreview{}, err
	}
	return s.run(ctx, access, request, false)
}

// Generate creates the training events of the chosen weeks from the plan.
// It creates events that are missing, rebuilds generated events that differ
// from the plan unless they were edited by hand or have participants, and
// removes untouched generated events of trainings the plan no longer has;
// the others are kept and listed. Running it again without changes writes
// nothing. It runs in one transaction with the season part row locked, and
// is audited with counts when it wrote something.
func (s *EventGenerationService) Generate(ctx context.Context, actor GroupActor, request GenerationRequest) (GenerationPreview, error) {
	if err := s.authorize(ctx, actor.Access); err != nil {
		return GenerationPreview{}, err
	}
	result, err := s.run(ctx, actor.Access, request, true)
	if err != nil {
		return GenerationPreview{}, err
	}
	if result.Counts.Changed() {
		weeks := make([]string, 0, len(result.Weeks))
		for _, week := range result.Weeks {
			weeks = append(weeks, FormatSeasonDate(week))
		}
		detail := "part " + strconv.FormatInt(result.Part.ID, 10) + "; weeks " + strings.Join(weeks, ",") +
			"; created " + strconv.Itoa(result.Counts.Create) + "; rebuilt " + strconv.Itoa(result.Counts.Rebuild) +
			"; removed " + strconv.Itoa(result.Counts.Remove) + "; kept " + strconv.Itoa(result.Counts.Keep) +
			"; unchanged " + strconv.Itoa(result.Counts.Unchanged)
		if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
			detail += " (site administrator)"
		}
		recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, actor.Access.GroupID(), AuditActionEventsGenerated, detail)
	}
	return result, nil
}

func (s *EventGenerationService) run(ctx context.Context, access *GroupAccess, request GenerationRequest, apply bool) (GenerationPreview, error) {
	if request.PartID <= 0 {
		return GenerationPreview{}, ErrSeasonNotFound
	}
	season, part, err := s.seasons.FindPart(ctx, access, request.PartID)
	if err != nil {
		return GenerationPreview{}, err
	}
	weeks, err := generationWeeks(part, request)
	if err != nil {
		return GenerationPreview{}, err
	}
	teams, err := s.teams.ListTeams(ctx, access.GroupID())
	if err != nil {
		return GenerationPreview{}, fmt.Errorf("list teams: %w", err)
	}
	resources, err := s.resources.List(ctx, access)
	if err != nil {
		return GenerationPreview{}, fmt.Errorf("list resources: %w", err)
	}
	teamByID := make(map[int64]Team, len(teams))
	for _, summary := range teams {
		teamByID[summary.Team.ID] = summary.Team
	}
	resourceByID := make(map[int64]Resource, len(resources))
	for _, resource := range resources {
		resourceByID[resource.ID] = resource
	}
	result := GenerationPreview{Season: season, Part: part, Weeks: weeks}
	createdAt := s.now().Unix()
	err = s.events.Generate(ctx, access.GroupID(), part.ID, weeks, apply, func(state GenerationState) (GenerationPlan, error) {
		outcome, err := PlanGeneration(GenerationInput{
			GroupID: access.GroupID(), Part: part, Weeks: weeks, State: state, Teams: teamByID, Resources: resourceByID, CreatedAt: createdAt,
		})
		if err != nil {
			return GenerationPlan{}, err
		}
		result.TimeZone, result.Items, result.Counts = state.TimeZone, outcome.Items, outcome.Counts
		return outcome.Plan, nil
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrSeasonNotFound), errors.Is(err, ErrRecordNotFound):
		return GenerationPreview{}, ErrSeasonNotFound
	case errors.Is(err, ErrGroupNotFound), errors.Is(err, ErrInvalidTimeZone), errors.Is(err, ErrTeamNotFound), errors.Is(err, ErrResourceNotFound):
		return GenerationPreview{}, err
	default:
		return GenerationPreview{}, fmt.Errorf("generate events: %w", err)
	}
	return result, nil
}

// generationWeeks validates the request against the part: Mondays only, one
// or two consecutive weeks, each with a day inside the part.
func generationWeeks(part SeasonPart, request GenerationRequest) ([]time.Time, error) {
	count := cmp.Or(request.Weeks, 1)
	first := SeasonDate(request.Week)
	if count < 1 || count > MaxGenerationWeeks || first.IsZero() || first.Weekday() != time.Monday {
		return nil, ErrInvalidGenerationWeek
	}
	weeks := make([]time.Time, 0, count)
	for index := range count {
		week := first.AddDate(0, 0, 7*index)
		if week.After(part.EndsOn) || week.AddDate(0, 0, 6).Before(part.StartsOn) {
			return nil, ErrInvalidGenerationWeek
		}
		weeks = append(weeks, week)
	}
	return weeks, nil
}

// GenerationInput is everything PlanGeneration decides on.
type GenerationInput struct {
	GroupID   int64
	Part      SeasonPart
	Weeks     []time.Time
	State     GenerationState
	Teams     map[int64]Team
	Resources map[int64]Resource
	CreatedAt int64
}

// GenerationOutcome is the decision: the items to show and the writes.
type GenerationOutcome struct {
	Items  []GenerationItem
	Counts GenerationCounts
	Plan   GenerationPlan
}

type generationKey struct {
	training int64
	week     time.Time
}

// PlanGeneration decides what generating the weeks does. A training makes
// one event in a week when its week (A, B, or all) is the part's week on
// that day and the day is inside the part. Local start and end minutes are
// turned into instants as wall-clock times in the group's time zone, so a
// training keeps its clock time across daylight-saving changes (a time the
// clocks skip is not generated). The event is titled with the team's name,
// has the training's team and resource, kind training, and open sign-up.
func PlanGeneration(in GenerationInput) (GenerationOutcome, error) {
	zone := in.State.TimeZone
	if _, err := eventLocation(zone); err != nil {
		return GenerationOutcome{}, err
	}
	var outcome GenerationOutcome
	add := func(item GenerationItem) {
		outcome.Items = append(outcome.Items, item)
		switch item.Action {
		case GenerationCreate:
			outcome.Counts.Create++
		case GenerationUnchanged:
			outcome.Counts.Unchanged++
		case GenerationRebuild:
			outcome.Counts.Rebuild++
		case GenerationRemove:
			outcome.Counts.Remove++
		case GenerationKeep:
			outcome.Counts.Keep++
		case GenerationSkip:
			outcome.Counts.Skip++
		}
	}

	partTrainings := make(map[int64]bool, len(in.State.Trainings))
	for _, training := range in.State.Trainings {
		partTrainings[training.ID] = true
	}
	// This part's generated events by (training, week). Events whose
	// training is gone (link cleared) belong to the part when they lie
	// inside its dates.
	existing := make(map[generationKey]GeneratedEvent)
	var unlinked []GeneratedEvent
	for _, generated := range in.State.Existing {
		event := generated.Event
		if event.GeneratedForWeek == nil {
			continue
		}
		week := SeasonDate(*event.GeneratedForWeek)
		switch {
		case event.PlanTrainingID != nil && partTrainings[*event.PlanTrainingID]:
			existing[generationKey{*event.PlanTrainingID, week}] = generated
		case event.PlanTrainingID == nil:
			if local, err := LocalDate(event.StartsAt, zone); err == nil && in.Part.Contains(local) {
				unlinked = append(unlinked, generated)
			}
		}
	}

	itemOf := func(action, reason string, week time.Time, event Event) GenerationItem {
		item := GenerationItem{Action: action, Reason: reason, Week: week, EventID: event.ID, StartsAt: event.StartsAt, EndsAt: event.EndsAt}
		if event.PlanTrainingID != nil {
			item.TrainingID = *event.PlanTrainingID
		}
		if len(event.Teams) > 0 {
			item.TeamID, item.TeamName = event.Teams[0].ID, event.Teams[0].Name
		}
		if event.ResourceName != nil {
			item.ResourceName = *event.ResourceName
		}
		return item
	}
	protectReason := func(generated GeneratedEvent) string {
		switch {
		case generated.Event.EditedAt != nil:
			return GenerationReasonEdited
		case generated.HasParticipants:
			return GenerationReasonParticipants
		}
		return ""
	}

	handled := make(map[generationKey]bool)
	for _, week := range in.Weeks {
		for _, training := range in.State.Trainings {
			day := week.AddDate(0, 0, training.Weekday-1)
			plannedWeek := in.Part.WeekOn(day)
			if plannedWeek == "" || (training.Week != SeasonWeekAll && training.Week != plannedWeek) {
				continue
			}
			key := generationKey{training.ID, week}
			handled[key] = true
			team, resource := in.Teams[training.TeamID], in.Resources[training.ResourceID]
			base := GenerationItem{Week: week, TrainingID: training.ID, TeamID: team.ID, TeamName: team.Name, ResourceName: resource.Name}
			current, exists := existing[key]
			if exists {
				if reason := protectReason(current); reason != "" {
					add(itemOf(GenerationKeep, reason, week, current.Event))
					continue
				}
			}
			skip := func(reason string) {
				base.Action, base.Reason = GenerationSkip, reason
				add(base)
				if exists {
					outcome.Plan.Delete = append(outcome.Plan.Delete, current.Event.ID)
					add(itemOf(GenerationRemove, "", week, current.Event))
				}
			}
			switch {
			case team.ID == 0 || team.Archived():
				skip(GenerationReasonArchivedTeam)
				continue
			case resource.ID == 0 || resource.Archived():
				skip(GenerationReasonArchivedResource)
				continue
			}
			starts, errStart := EventInstant(time.Date(day.Year(), day.Month(), day.Day(), 0, training.StartMinute, 0, 0, time.UTC), zone)
			ends, errEnd := EventInstant(time.Date(day.Year(), day.Month(), day.Day(), 0, training.EndMinute, 0, 0, time.UTC), zone)
			if err := errors.Join(errStart, errEnd); err != nil {
				if errors.Is(err, ErrEventTimeSkipped) {
					skip(GenerationReasonTimeSkipped)
					continue
				}
				return GenerationOutcome{}, err
			}
			base.StartsAt, base.EndsAt = starts, ends
			resourceID, weekValue, trainingID := resource.ID, week, training.ID
			fresh := Event{
				GroupID: in.GroupID, Kind: EventKindTraining, Title: team.Name, StartsAt: starts, EndsAt: ends, ResourceID: &resourceID,
				SignupMode: EventSignupOpen, Teams: []EventTeam{{ID: team.ID, Name: team.Name}},
				PlanTrainingID: &trainingID, GeneratedForWeek: &weekValue, CreatedAt: in.CreatedAt,
			}
			switch {
			case !exists:
				base.Action = GenerationCreate
				outcome.Plan.Create = append(outcome.Plan.Create, fresh)
			case generatedMatches(current.Event, fresh):
				add(itemOf(GenerationUnchanged, "", week, current.Event))
				continue
			default:
				outcome.Plan.Delete = append(outcome.Plan.Delete, current.Event.ID)
				outcome.Plan.Create = append(outcome.Plan.Create, fresh)
				base.Action, base.EventID = GenerationRebuild, current.Event.ID
			}
			add(base)
		}
	}
	// Generated events the plan no longer wants: the training is gone (its
	// link cleared by the foreign key), or it does not apply to that week
	// any more. Untouched ones are removed.
	removable := func(generated GeneratedEvent, week time.Time) {
		if reason := protectReason(generated); reason != "" {
			add(itemOf(GenerationKeep, reason, week, generated.Event))
			return
		}
		outcome.Plan.Delete = append(outcome.Plan.Delete, generated.Event.ID)
		add(itemOf(GenerationRemove, "", week, generated.Event))
	}
	for key, generated := range existing {
		if !handled[key] && slices.ContainsFunc(in.Weeks, func(week time.Time) bool { return week.Equal(key.week) }) {
			removable(generated, key.week)
		}
	}
	for _, generated := range unlinked {
		removable(generated, SeasonDate(*generated.Event.GeneratedForWeek))
	}
	slices.SortStableFunc(outcome.Items, func(a, b GenerationItem) int {
		return cmp.Or(a.StartsAt.Compare(b.StartsAt), cmp.Compare(a.TeamName, b.TeamName), cmp.Compare(a.EventID, b.EventID), cmp.Compare(a.TrainingID, b.TrainingID))
	})
	return outcome, nil
}

// generatedMatches reports whether a generated event already is what the
// plan says.
func generatedMatches(current, fresh Event) bool {
	return current.Kind == fresh.Kind && current.Title == fresh.Title && current.StartsAt.Equal(fresh.StartsAt) && current.EndsAt.Equal(fresh.EndsAt) &&
		current.SignupMode == fresh.SignupMode && current.ResourceID != nil && fresh.ResourceID != nil && *current.ResourceID == *fresh.ResourceID &&
		slices.Equal(current.TeamIDs(), fresh.TeamIDs()) && current.CancelledAt == nil
}

func (s *EventGenerationService) authorize(ctx context.Context, access *GroupAccess) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	if access == nil || !access.CanView() {
		return ErrGroupNotFound
	}
	if !access.CanManagePlan() {
		return ErrGroupForbidden
	}
	return nil
}

func (s *EventGenerationService) validate(ctx context.Context) error {
	if s == nil || s.events == nil || s.seasons == nil || s.resources == nil || s.teams == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil event generation context")
	}
	return nil
}
