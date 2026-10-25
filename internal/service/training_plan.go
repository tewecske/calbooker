package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AuditActionTrainingPlanSaved records a save of the training plan board:
// the part and how many trainings were added, moved, or removed. Counts
// only; teams and resources are not named.
const AuditActionTrainingPlanSaved = "group.plan.trainings_saved"

// MaxTrainingsPerPart bounds the trainings one season part's plan, board
// draft, or save may hold.
const MaxTrainingsPerPart = 1000

var (
	// ErrInvalidTrainingTime identifies a weekday outside 1..7 or a time
	// range that is not inside one day with its start before its end.
	ErrInvalidTrainingTime = errors.New("service: invalid training time")
	// ErrInvalidTrainingWeek identifies a week other than A, B, or all in a
	// part with alternating weeks.
	ErrInvalidTrainingWeek = errors.New("service: invalid training week")
	// ErrTrainingPlanTooLarge identifies a save or draft with more than
	// MaxTrainingsPerPart trainings.
	ErrTrainingPlanTooLarge = errors.New("service: too many trainings")
	// ErrNilTrainingPlanRepository identifies missing training persistence.
	ErrNilTrainingPlanRepository = errors.New("service: nil training plan repository")
	// ErrNilTrainingPlanSource identifies a missing season, slot, resource,
	// team, or availability source.
	ErrNilTrainingPlanSource = errors.New("service: nil training plan source")
)

// Training plan board suggestion modes.
const (
	// TrainingSuggestNone shows the draft as it is.
	TrainingSuggestNone = ""
	// TrainingSuggestFill keeps the draft and suggests trainings for active
	// teams that have none.
	TrainingSuggestFill = "fill"
	// TrainingSuggestReplace drops the draft and starts from the suggestion.
	TrainingSuggestReplace = "replace"
)

// Training suggestion reasons.
const (
	// TrainingSuggestionPreviousSeason: the team trained there in the
	// previous season's matching part.
	TrainingSuggestionPreviousSeason = "previous_season"
	// TrainingSuggestionSlot: the team was fitted into a resource slot.
	TrainingSuggestionSlot = "slot"
)

// Training warning kinds. Warnings never block a save.
const (
	// TrainingWarningTeamArchived: the team is archived.
	TrainingWarningTeamArchived = "team_archived"
	// TrainingWarningResourceArchived: the resource is archived.
	TrainingWarningResourceArchived = "resource_archived"
	// TrainingWarningNoCoach: the team has no coach this season.
	TrainingWarningNoCoach = "no_coach"
	// TrainingWarningNoSlot: no resource slot overlaps the training.
	TrainingWarningNoSlot = "no_slot"
	// TrainingWarningOutsideSlot: the training reaches beyond its slot.
	TrainingWarningOutsideSlot = "outside_slot"
	// TrainingWarningSlotAges: the team's ages are outside the slot's.
	TrainingWarningSlotAges = "slot_ages"
	// TrainingWarningSlotFull: more teams train in the slot at once than it
	// allows.
	TrainingWarningSlotFull = "slot_full"
	// TrainingWarningResourceFull: more teams use the resource at once than
	// its limit.
	TrainingWarningResourceFull = "resource_full"
	// TrainingWarningTeamOverlap: the team trains twice at the same time.
	TrainingWarningTeamOverlap = "team_overlap"
	// TrainingWarningCoachUnavailable: one of the team's coaches has no
	// availability for the whole training.
	TrainingWarningCoachUnavailable = "coach_unavailable"
	// TrainingWarningCoachBusy: one of the team's coaches trains another
	// team at the same time.
	TrainingWarningCoachBusy = "coach_busy"
)

// PlanTraining is one weekly training of a team in a season part's plan: a
// team on a resource, optionally in a resource slot (PlanSlotID 0 is none),
// with its own start and end. Times are minutes after local midnight;
// Weekday is ISO (1 = Monday); Week is SeasonWeekA, SeasonWeekB, or
// SeasonWeekAll.
type PlanTraining struct {
	ID           int64
	GroupID      int64
	SeasonPartID int64
	TeamID       int64
	ResourceID   int64
	PlanSlotID   int64
	Week         string
	Weekday      int
	StartMinute  int
	EndMinute    int
	CreatedAt    int64
}

// trainingKey is what makes two trainings the same: everything but identity
// and slot.
type trainingKey struct {
	team, resource      int64
	week                string
	weekday, start, end int
}

func (t PlanTraining) key() trainingKey {
	return trainingKey{t.TeamID, t.ResourceID, t.Week, t.Weekday, t.StartMinute, t.EndMinute}
}

// weeksOverlap reports whether trainings of weeks a and b can happen in the
// same week of the part.
func weeksOverlap(a, b string, alternating bool) bool {
	return !alternating || a == SeasonWeekAll || b == SeasonWeekAll || a == b
}

// overlaps reports whether two trainings can happen at the same time.
func (t PlanTraining) overlaps(other PlanTraining, alternating bool) bool {
	return t.Weekday == other.Weekday && t.StartMinute < other.EndMinute && other.StartMinute < t.EndMinute &&
		weeksOverlap(t.Week, other.Week, alternating)
}

// TrainingChanges counts what a board save changed.
type TrainingChanges struct {
	Added   int
	Moved   int
	Removed int
}

// Total is the number of changed trainings.
func (c TrainingChanges) Total() int {
	return c.Added + c.Moved + c.Removed
}

// PlanTrainingDiff is what turns one training set of a part into another.
// Trainings keep their identity (and with it the events generated from
// them) when they are moved: Update holds trainings that exist with another
// team, resource, week, day, time, or slot.
type PlanTrainingDiff struct {
	Delete  []PlanTraining
	Update  []PlanTraining
	Insert  []PlanTraining
	Changes TrainingChanges
}

// DiffPlanTrainings compares two training sets of one part. Desired
// trainings with the ID of a current training update it; others are new; the
// current trainings missing from desired are deleted. A slot-only difference
// updates the row without counting as a change.
func DiffPlanTrainings(current, desired []PlanTraining) PlanTrainingDiff {
	byID := make(map[int64]PlanTraining, len(current))
	for _, training := range current {
		byID[training.ID] = training
	}
	kept := make(map[int64]bool, len(desired))
	var diff PlanTrainingDiff
	for _, training := range desired {
		existing, ok := byID[training.ID]
		if training.ID <= 0 || !ok || kept[training.ID] {
			training.ID = 0
			diff.Insert = append(diff.Insert, training)
			diff.Changes.Added++
			continue
		}
		kept[training.ID] = true
		switch {
		case existing.key() != training.key():
			diff.Update = append(diff.Update, training)
			diff.Changes.Moved++
		case existing.PlanSlotID != training.PlanSlotID:
			diff.Update = append(diff.Update, training)
		}
	}
	for _, training := range current {
		if !kept[training.ID] {
			diff.Delete = append(diff.Delete, training)
			diff.Changes.Removed++
		}
	}
	return diff
}

// TrainingRevision is the revision token of a part's trainings: a hash of
// the sorted (ID, team, resource, week, day, start, end) set. The board
// sends it back on save; a different current set is a stale save. It names
// no account and is not secret.
func TrainingRevision(trainings []PlanTraining) string {
	lines := make([]string, 0, len(trainings))
	for _, t := range trainings {
		lines = append(lines, strings.Join([]string{
			strconv.FormatInt(t.ID, 10), strconv.FormatInt(t.TeamID, 10), strconv.FormatInt(t.ResourceID, 10),
			t.Week, strconv.Itoa(t.Weekday), strconv.Itoa(t.StartMinute), strconv.Itoa(t.EndMinute),
		}, ":"))
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:16])
}

// TrainingDraftItem is a training of the board draft with the explanation
// of the suggestion that added it (empty for hand-made and saved ones).
type TrainingDraftItem struct {
	PlanTraining
	Reason string
}

// TrainingDraft is the board state the page sends back: the trainings the
// user is editing, in board order, and the revision of the saved set it
// started from.
type TrainingDraft struct {
	Revision string
	Items    []TrainingDraftItem
}

// TrainingBoardRequest selects what the board shows: a season part (0 picks
// the default part, see PlanSlotService.Board), and the saved trainings
// (Draft nil) or a draft, optionally with a suggestion (Suggest is one of
// TrainingSuggestNone, TrainingSuggestFill, TrainingSuggestReplace).
type TrainingBoardRequest struct {
	PartID  int64
	Draft   *TrainingDraft
	Suggest string
}

// TrainingWarning is one broken rule of one training. Index is the
// training's position on the board; Other is the other training of a
// conflict (-1 when there is none).
type TrainingWarning struct {
	Kind        string
	Index       int
	Other       int
	CoachUserID int64
	// Teams and Limit are the number of teams at once and what is allowed
	// (slot_full, resource_full).
	Teams int
	Limit int
	// TeamAges and SlotAges are set for slot_ages; SlotAges holds 0 for an
	// open end.
	TeamAges AgeRange
	SlotAges AgeRange
}

// TrainingBoardCoach is a coach of a team this season.
type TrainingBoardCoach struct {
	UserID      int64
	DisplayName *string
	Username    *string
	Head        bool
}

// TrainingBoardTeam is a team on the board with its ages and coaches.
type TrainingBoardTeam struct {
	Team    Team
	Ages    AgeRange
	Coaches []TrainingBoardCoach
	// Trainings is how many trainings the team has on the shown board.
	Trainings int
}

// BoardTraining is one training on the board.
type BoardTraining struct {
	// Index is the position in the draft; the page names trainings by it.
	Index    int
	Training PlanTraining
	Slot     PlanSlot
	HasSlot  bool
	// Reason explains a suggested training that is not saved.
	Reason string
	// Saved is true when the training is saved as shown.
	Saved    bool
	Warnings []TrainingWarning
}

// TrainingBoard is the training plan board of one season part.
type TrainingBoard struct {
	// Seasons are the group's seasons, latest first, for the part picker.
	Seasons []Season
	// Found is false when the group has no season part yet.
	Found  bool
	Season Season
	Part   SeasonPart
	// CanEdit is true for accounts that manage the plan; coaches see the
	// saved trainings read only, without warnings.
	CanEdit bool
	// PreviousSeason is the season the suggestion takes trainings from.
	PreviousSeason Season
	HasPrevious    bool
	// Revision is the revision the shown draft started from; SavedRevision
	// is the current one. They differ when the plan changed since the board
	// was loaded.
	Revision      string
	SavedRevision string
	// Changed is true when the shown trainings differ from the saved ones.
	Changed bool
	// Suggested is how many trainings this request's suggestion added;
	// Unplaced how many active teams it found no slot for.
	Suggested int
	Unplaced  int
	Slots     []PlanSlot
	Resources []Resource
	Teams     []TrainingBoardTeam
	Trainings []BoardTraining
	Warnings  []TrainingWarning
}

// Stale reports whether the saved trainings changed since the shown draft
// was loaded; saving it would be a conflict.
func (b TrainingBoard) Stale() bool {
	return b.Revision != b.SavedRevision
}

// TrainingPlanRepository persists the trainings of weekly plans. Every
// method is scoped to one group.
type TrainingPlanRepository interface {
	// ListPlanTrainings returns a part's trainings by weekday, start, team,
	// and ID.
	ListPlanTrainings(ctx context.Context, groupID, partID int64) ([]PlanTraining, error)
	// ReplacePlanTrainings locks the part, reads its trainings, and applies
	// what edit returns for them (DiffPlanTrainings) in one transaction. It
	// returns ErrSeasonNotFound when the part is gone, an error from edit
	// unchanged (nothing written), and ErrTeamNotFound or
	// ErrResourceNotFound from foreign keys.
	ReplacePlanTrainings(ctx context.Context, groupID, partID, createdAt int64, edit func([]PlanTraining) ([]PlanTraining, error)) (TrainingChanges, error)
}

// TrainingSeasonSource is the season lookup the training plan needs
// (SeasonService implements it).
type TrainingSeasonSource interface {
	PlanSeasonSource
	SeasonCoaches(ctx context.Context, access *GroupAccess, seasonID int64) ([]SeasonCoach, error)
}

// TrainingSlotSource is the slot lookup the training plan needs
// (PlanSlotService implements it).
type TrainingSlotSource interface {
	Slots(ctx context.Context, access *GroupAccess, partID int64) ([]PlanSlot, error)
}

// TrainingTeamSource is the team lookup the training plan needs
// (TeamRepository implements it).
type TrainingTeamSource interface {
	ListTeams(ctx context.Context, groupID int64) ([]TeamSummary, error)
}

// TrainingAvailabilitySource is the availability lookup the training plan
// needs (CoachService implements it).
type TrainingAvailabilitySource interface {
	Availability(ctx context.Context, access *GroupAccess, partID int64) (PartAvailability, error)
}

// TrainingPlanService owns the training plan board: the board view with
// suggestion and warnings, and saving the whole training set of a season
// part. Admins, managers, and site administrators edit; coaches read.
type TrainingPlanService struct {
	trainings    TrainingPlanRepository
	seasons      TrainingSeasonSource
	slots        TrainingSlotSource
	resources    PlanResourceSource
	teams        TrainingTeamSource
	availability TrainingAvailabilitySource
	users        UserRepository
	auditor      AuditRecorder
	now          func() time.Time
}

// NewTrainingPlanService constructs training plan use cases with explicit
// dependencies.
func NewTrainingPlanService(trainings TrainingPlanRepository, seasons TrainingSeasonSource, slots TrainingSlotSource, resources PlanResourceSource, teams TrainingTeamSource, availability TrainingAvailabilitySource, users UserRepository) (*TrainingPlanService, error) {
	switch {
	case trainings == nil:
		return nil, ErrNilTrainingPlanRepository
	case seasons == nil, slots == nil, resources == nil, teams == nil, availability == nil:
		return nil, ErrNilTrainingPlanSource
	case users == nil:
		return nil, ErrNilUserRepository
	}
	return &TrainingPlanService{trainings: trainings, seasons: seasons, slots: slots, resources: resources, teams: teams, availability: availability, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *TrainingPlanService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Trainings returns the saved trainings of a season part by weekday, start,
// team, and ID, each with its team, resource, slot (0 for none), week,
// weekday, start, and end. Admins, managers, site administrators, and
// coaches. Event generation (#15) reads the plan through it.
func (s *TrainingPlanService) Trainings(ctx context.Context, access *GroupAccess, partID int64) ([]PlanTraining, error) {
	if err := s.viewable(ctx, access); err != nil {
		return nil, err
	}
	if _, _, err := s.seasons.FindPart(ctx, access, partID); err != nil {
		return nil, err
	}
	return s.list(ctx, access.GroupID(), partID)
}

// Board returns the training plan board of a season part. Staff get the
// draft they sent (or the saved trainings), optionally with a suggestion,
// and the warnings. Coaches get the saved trainings read only, without
// warnings (they would show other coaches' availability). Players and
// parents are forbidden; non-members get ErrGroupNotFound.
func (s *TrainingPlanService) Board(ctx context.Context, access *GroupAccess, request TrainingBoardRequest) (TrainingBoard, error) {
	if err := s.viewable(ctx, access); err != nil {
		return TrainingBoard{}, err
	}
	seasons, err := s.seasons.List(ctx, access)
	if err != nil {
		return TrainingBoard{}, fmt.Errorf("list seasons: %w", err)
	}
	board := TrainingBoard{Seasons: seasons, CanEdit: access.CanManagePlan()}
	if request.PartID == 0 {
		today, err := s.seasons.Today(ctx, access)
		if err != nil {
			return TrainingBoard{}, fmt.Errorf("find today: %w", err)
		}
		season, part, ok := defaultPlanPart(seasons, today)
		if !ok {
			return board, nil
		}
		board.Season, board.Part = season, part
	} else {
		season, part, err := s.seasons.FindPart(ctx, access, request.PartID)
		if err != nil {
			return TrainingBoard{}, err
		}
		board.Season, board.Part = season, part
	}
	board.Found = true
	groupID := access.GroupID()
	season, part := board.Season, board.Part

	saved, err := s.list(ctx, groupID, part.ID)
	if err != nil {
		return TrainingBoard{}, err
	}
	if board.Slots, err = s.slots.Slots(ctx, access, part.ID); err != nil {
		return TrainingBoard{}, err
	}
	if board.Resources, err = s.resources.List(ctx, access); err != nil {
		return TrainingBoard{}, fmt.Errorf("list resources: %w", err)
	}
	summaries, err := s.teams.ListTeams(ctx, groupID)
	if err != nil {
		return TrainingBoard{}, fmt.Errorf("list teams: %w", err)
	}
	seasonCoaches, err := s.seasons.SeasonCoaches(ctx, access, season.ID)
	if err != nil {
		return TrainingBoard{}, err
	}
	board.SavedRevision = TrainingRevision(saved)
	board.Revision = board.SavedRevision

	teamByID := make(map[int64]Team, len(summaries))
	teams := make([]Team, 0, len(summaries))
	for _, summary := range summaries {
		teamByID[summary.Team.ID] = summary.Team
		teams = append(teams, summary.Team)
	}
	resourceByID := make(map[int64]Resource, len(board.Resources))
	for _, resource := range board.Resources {
		resourceByID[resource.ID] = resource
	}
	coachesOf := coachesByTeam(seasonCoaches)

	items := draftItems(saved)
	if !board.CanEdit {
		board.Teams, board.Trainings = s.boardParts(board, teams, season, coachesOf, items, saved, nil)
		return board, nil
	}

	if request.Draft != nil {
		if len(request.Draft.Items) > MaxTrainingsPerPart {
			return TrainingBoard{}, ErrTrainingPlanTooLarge
		}
		normalized, err := normalizeTrainingItems(part, request.Draft.Items)
		if err != nil {
			return TrainingBoard{}, err
		}
		// The draft comes from the browser: keep only teams and resources of
		// the group.
		items = slices.DeleteFunc(normalized, func(item TrainingDraftItem) bool {
			_, team := teamByID[item.TeamID]
			_, resource := resourceByID[item.ResourceID]
			return !team || !resource
		})
		if request.Draft.Revision != "" {
			board.Revision = request.Draft.Revision
		}
	}
	if request.Suggest == TrainingSuggestReplace {
		items = nil
	}
	availability, err := s.availability.Availability(ctx, access, part.ID)
	if err != nil {
		return TrainingBoard{}, err
	}
	board.PreviousSeason, board.HasPrevious = PreviousSeason(seasons, season)
	if request.Suggest == TrainingSuggestFill || request.Suggest == TrainingSuggestReplace {
		var previous []PlanTraining
		if board.HasPrevious {
			if previous, err = s.previousTrainings(ctx, groupID, board.PreviousSeason, part); err != nil {
				return TrainingBoard{}, err
			}
		}
		var active []Team
		for _, team := range teams {
			if !team.Archived() {
				active = append(active, team)
			}
		}
		added, unplaced := SuggestTrainings(TrainingSuggestionInput{
			Season: season, Part: part, Teams: active, Coaches: coachIDsByTeam(coachesOf), Slots: board.Slots,
			Resources: board.Resources, Availability: &availability, Previous: previous, Current: trainingsOf(items),
		})
		for _, suggested := range added {
			items = append(items, TrainingDraftItem{PlanTraining: suggested.Training, Reason: suggested.Reason})
		}
		board.Suggested, board.Unplaced = len(added), unplaced
	}
	for index := range items {
		items[index].PlanSlotID = 0
		if slot, ok := bestSlot(items[index].PlanTraining, board.Slots, part.AlternatingWeeks); ok {
			items[index].PlanSlotID = slot.ID
		}
	}
	board.Changed = TrainingRevision(trainingsOf(items)) != board.SavedRevision

	coachIDs := coachIDsByTeam(coachesOf)
	warnings := TrainingWarnings(TrainingWarningInput{
		Season: season, Part: part, Trainings: trainingsOf(items), Slots: board.Slots, Resources: board.Resources,
		Teams: teamByID, Coaches: coachIDs, Availability: &availability,
	})
	board.Teams, board.Trainings = s.boardParts(board, teams, season, coachesOf, items, saved, warnings)
	board.Warnings = warnings
	return board, nil
}

// previousTrainings returns the trainings of the previous season's part at
// the same position as part.
func (s *TrainingPlanService) previousTrainings(ctx context.Context, groupID int64, previous Season, part SeasonPart) ([]PlanTraining, error) {
	for _, candidate := range previous.Parts {
		if candidate.Position == part.Position {
			return s.list(ctx, groupID, candidate.ID)
		}
	}
	return nil, nil
}

// boardParts builds the team columns and the training rows of a board.
func (s *TrainingPlanService) boardParts(board TrainingBoard, teams []Team, season Season, coachesOf map[int64][]TrainingBoardCoach, items []TrainingDraftItem, saved []PlanTraining, warnings []TrainingWarning) ([]TrainingBoardTeam, []BoardTraining) {
	savedByID := make(map[int64]PlanTraining, len(saved))
	for _, training := range saved {
		savedByID[training.ID] = training
	}
	slotByID := make(map[int64]PlanSlot, len(board.Slots))
	for _, slot := range board.Slots {
		slotByID[slot.ID] = slot
	}
	rows := make([]BoardTraining, 0, len(items))
	count := make(map[int64]int)
	for index, item := range items {
		row := BoardTraining{Index: index, Training: item.PlanTraining, Reason: item.Reason}
		if existing, ok := savedByID[item.ID]; ok && item.ID > 0 && existing.key() == item.key() {
			row.Saved = true
		}
		if row.Saved {
			row.Reason = ""
		}
		row.Slot, row.HasSlot = slotByID[item.PlanSlotID]
		for _, warning := range warnings {
			if warning.Index == index {
				row.Warnings = append(row.Warnings, warning)
			}
		}
		rows = append(rows, row)
		count[item.TeamID]++
	}
	columns := make([]TrainingBoardTeam, 0, len(teams))
	for _, team := range teams {
		// Archived teams show only while they hold trainings.
		if team.Archived() && count[team.ID] == 0 {
			continue
		}
		columns = append(columns, TrainingBoardTeam{Team: team, Ages: TeamAgeRange(team, season), Coaches: coachesOf[team.ID], Trainings: count[team.ID]})
	}
	return columns, rows
}

// Save replaces a season part's trainings with trainings, at the revision
// the board was loaded with (TrainingRevision). Trainings that carry the ID
// of a saved training update it (and keep the events generated from it);
// the rest are new; saved trainings missing from the set are removed. Every
// team and resource must belong to the group. A different current set is
// ErrOptimisticLockConflict; a part that is gone is ErrSeasonNotFound.
// Admins, managers, and site administrators; audited when something
// changed.
func (s *TrainingPlanService) Save(ctx context.Context, actor GroupActor, partID int64, revision string, trainings []PlanTraining) (TrainingChanges, error) {
	if err := s.validate(ctx); err != nil {
		return TrainingChanges{}, err
	}
	if err := authorizeGroupAction(actor, actor.Access.CanManagePlan()); err != nil {
		return TrainingChanges{}, err
	}
	if partID <= 0 {
		return TrainingChanges{}, ErrSeasonNotFound
	}
	if len(trainings) > MaxTrainingsPerPart {
		return TrainingChanges{}, ErrTrainingPlanTooLarge
	}
	_, part, err := s.seasons.FindPart(ctx, actor.Access, partID)
	if err != nil {
		return TrainingChanges{}, err
	}
	groupID := actor.Access.GroupID()
	items := make([]TrainingDraftItem, 0, len(trainings))
	for _, training := range trainings {
		items = append(items, TrainingDraftItem{PlanTraining: training})
	}
	normalized, err := normalizeTrainingItems(part, items)
	if err != nil {
		return TrainingChanges{}, err
	}
	desired := trainingsOf(normalized)
	if err := s.checkReferences(ctx, actor.Access, desired); err != nil {
		return TrainingChanges{}, err
	}
	slots, err := s.slots.Slots(ctx, actor.Access, part.ID)
	if err != nil {
		return TrainingChanges{}, err
	}
	for index := range desired {
		desired[index].PlanSlotID = 0
		desired[index].GroupID, desired[index].SeasonPartID = groupID, part.ID
		if slot, ok := bestSlot(desired[index], slots, part.AlternatingWeeks); ok {
			desired[index].PlanSlotID = slot.ID
		}
	}
	changes, err := s.trainings.ReplacePlanTrainings(ctx, groupID, part.ID, s.now().Unix(), func(current []PlanTraining) ([]PlanTraining, error) {
		if TrainingRevision(current) != revision {
			return nil, ErrOptimisticLockConflict
		}
		return desired, nil
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrSeasonNotFound), errors.Is(err, ErrRecordNotFound):
		// A part that vanished is not-found, never a conflict.
		return TrainingChanges{}, ErrSeasonNotFound
	case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrTeamNotFound), errors.Is(err, ErrResourceNotFound):
		return TrainingChanges{}, err
	default:
		return TrainingChanges{}, fmt.Errorf("save plan trainings: %w", err)
	}
	if changes.Total() > 0 {
		detail := "part " + strconv.FormatInt(part.ID, 10) + "; added " + strconv.Itoa(changes.Added) +
			"; moved " + strconv.Itoa(changes.Moved) + "; removed " + strconv.Itoa(changes.Removed) +
			"; trainings " + strconv.Itoa(len(desired))
		if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
			detail += " (site administrator)"
		}
		recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, groupID, AuditActionTrainingPlanSaved, detail)
	}
	return changes, nil
}

// checkReferences refuses trainings of teams or resources outside the group.
func (s *TrainingPlanService) checkReferences(ctx context.Context, access *GroupAccess, trainings []PlanTraining) error {
	teams, err := s.teams.ListTeams(ctx, access.GroupID())
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}
	resources, err := s.resources.List(ctx, access)
	if err != nil {
		return fmt.Errorf("list resources: %w", err)
	}
	for _, training := range trainings {
		if !slices.ContainsFunc(teams, func(team TeamSummary) bool { return team.Team.ID == training.TeamID }) {
			return ErrTeamNotFound
		}
		if !slices.ContainsFunc(resources, func(resource Resource) bool { return resource.ID == training.ResourceID }) {
			return ErrResourceNotFound
		}
	}
	return nil
}

func (s *TrainingPlanService) list(ctx context.Context, groupID, partID int64) ([]PlanTraining, error) {
	trainings, err := s.trainings.ListPlanTrainings(ctx, groupID, partID)
	if err != nil {
		return nil, fmt.Errorf("list plan trainings: %w", err)
	}
	return trainings, nil
}

func (s *TrainingPlanService) viewable(ctx context.Context, access *GroupAccess) error {
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

func (s *TrainingPlanService) validate(ctx context.Context) error {
	if s == nil || s.trainings == nil || s.seasons == nil || s.slots == nil || s.resources == nil || s.teams == nil || s.availability == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil training plan context")
	}
	return nil
}

func draftItems(trainings []PlanTraining) []TrainingDraftItem {
	items := make([]TrainingDraftItem, 0, len(trainings))
	for _, training := range trainings {
		items = append(items, TrainingDraftItem{PlanTraining: training})
	}
	return items
}

func trainingsOf(items []TrainingDraftItem) []PlanTraining {
	trainings := make([]PlanTraining, 0, len(items))
	for _, item := range items {
		trainings = append(trainings, item.PlanTraining)
	}
	return trainings
}

// normalizeTrainingItems validates times and weeks for part and drops
// duplicates (the first of equal trainings stays) and repeated IDs. Order is
// kept. In a part without alternating weeks every training is an every-week
// one.
func normalizeTrainingItems(part SeasonPart, items []TrainingDraftItem) ([]TrainingDraftItem, error) {
	normalized := make([]TrainingDraftItem, 0, len(items))
	seen := make(map[trainingKey]bool, len(items))
	ids := make(map[int64]bool, len(items))
	for _, item := range items {
		if item.Weekday < 1 || item.Weekday > 7 || item.StartMinute < 0 || item.StartMinute >= item.EndMinute || item.EndMinute > minutesInDay {
			return nil, ErrInvalidTrainingTime
		}
		switch {
		case !part.AlternatingWeeks:
			item.Week = SeasonWeekAll
		case item.Week == "":
			item.Week = SeasonWeekAll
		case item.Week != SeasonWeekAll && item.Week != SeasonWeekA && item.Week != SeasonWeekB:
			return nil, ErrInvalidTrainingWeek
		}
		if seen[item.key()] {
			continue
		}
		seen[item.key()] = true
		if item.ID > 0 && ids[item.ID] {
			item.ID = 0
		}
		ids[item.ID] = true
		normalized = append(normalized, item)
	}
	return normalized, nil
}

// slotCoversWeek reports whether a slot is open in every week the training
// happens in.
func slotCoversWeek(slot PlanSlot, training PlanTraining, alternating bool) bool {
	return !alternating || slot.Week == SeasonWeekAll || slot.Week == training.Week
}

// bestSlot finds the slot a training belongs to: a slot of the training's
// resource, day, and weeks that overlaps it. A slot that contains the whole
// training wins (the shortest first), else the one with the largest overlap;
// ties go to the lowest ID.
func bestSlot(training PlanTraining, slots []PlanSlot, alternating bool) (PlanSlot, bool) {
	var (
		best                  PlanSlot
		found, bestContains   bool
		bestOverlap, bestSize int
	)
	for _, slot := range slots {
		if slot.ResourceID != training.ResourceID || slot.Weekday != training.Weekday || !slotCoversWeek(slot, training, alternating) {
			continue
		}
		overlap := min(slot.EndMinute, training.EndMinute) - max(slot.StartMinute, training.StartMinute)
		if overlap <= 0 {
			continue
		}
		contains := slot.StartMinute <= training.StartMinute && training.EndMinute <= slot.EndMinute
		size := slot.EndMinute - slot.StartMinute
		better := false
		switch {
		case !found:
			better = true
		case contains != bestContains:
			better = contains
		case contains:
			better = size < bestSize || (size == bestSize && slot.ID < best.ID)
		default:
			better = overlap > bestOverlap || (overlap == bestOverlap && slot.ID < best.ID)
		}
		if better {
			best, found, bestContains, bestOverlap, bestSize = slot, true, contains, overlap, size
		}
	}
	return best, found
}

// peakLoad returns the most trainings of group (target included) that run at
// once while target runs.
func peakLoad(target PlanTraining, group []PlanTraining, alternating bool) int {
	var related []PlanTraining
	for _, other := range group {
		if target.overlaps(other, alternating) {
			related = append(related, other)
		}
	}
	points := []int{target.StartMinute, target.EndMinute}
	for _, other := range related {
		points = append(points, max(other.StartMinute, target.StartMinute), min(other.EndMinute, target.EndMinute))
	}
	slices.Sort(points)
	points = slices.Compact(points)
	peak := 0
	for index := 0; index+1 < len(points); index++ {
		from, to := points[index], points[index+1]
		load := 0
		for _, other := range related {
			if other.StartMinute < to && from < other.EndMinute {
				load++
			}
		}
		peak = max(peak, load)
	}
	return peak
}

// coachesByTeam groups season coaches by team, head coach first, then by
// name.
func coachesByTeam(coaches []SeasonCoach) map[int64][]TrainingBoardCoach {
	byTeam := make(map[int64][]TrainingBoardCoach)
	for _, coach := range coaches {
		byTeam[coach.TeamID] = append(byTeam[coach.TeamID], TrainingBoardCoach{UserID: coach.CoachUserID, DisplayName: coach.DisplayName, Username: coach.Username, Head: coach.IsHead})
	}
	for _, list := range byTeam {
		slices.SortStableFunc(list, func(a, b TrainingBoardCoach) int {
			if a.Head != b.Head {
				if a.Head {
					return -1
				}
				return 1
			}
			return cmp.Or(strings.Compare(coachSortName(a.DisplayName, a.Username), coachSortName(b.DisplayName, b.Username)), cmp.Compare(a.UserID, b.UserID))
		})
	}
	return byTeam
}

func coachIDsByTeam(coaches map[int64][]TrainingBoardCoach) map[int64][]int64 {
	ids := make(map[int64][]int64, len(coaches))
	for team, list := range coaches {
		for _, coach := range list {
			ids[team] = append(ids[team], coach.UserID)
		}
	}
	return ids
}

// TrainingWarningInput is what TrainingWarnings checks.
type TrainingWarningInput struct {
	Season Season
	Part   SeasonPart
	// Trainings are the board's trainings in board order, with PlanSlotID
	// set; warning indexes refer to them.
	Trainings []PlanTraining
	Slots     []PlanSlot
	Resources []Resource
	Teams     map[int64]Team
	// Coaches are the coaches of each team in the season.
	Coaches map[int64][]int64
	// Availability, when set, is checked for every coach of a training's
	// team.
	Availability *PartAvailability
}

// TrainingWarnings returns the broken rules of the trainings, by training
// and in a fixed order per training: archived team or resource, no coach,
// slot (none, partly outside, ages, full), resource full, the team twice at
// once, then per coach not available and busy with another team.
func TrainingWarnings(in TrainingWarningInput) []TrainingWarning {
	alternating := in.Part.AlternatingWeeks
	slots := make(map[int64]PlanSlot, len(in.Slots))
	for _, slot := range in.Slots {
		slots[slot.ID] = slot
	}
	resources := make(map[int64]Resource, len(in.Resources))
	for _, resource := range in.Resources {
		resources[resource.ID] = resource
	}
	var warnings []TrainingWarning
	for index, training := range in.Trainings {
		warn := func(kind string) TrainingWarning {
			return TrainingWarning{Kind: kind, Index: index, Other: -1}
		}
		team, teamKnown := in.Teams[training.TeamID]
		if teamKnown && team.Archived() {
			warnings = append(warnings, warn(TrainingWarningTeamArchived))
		}
		resource := resources[training.ResourceID]
		if resource.ID != 0 && resource.Archived() {
			warnings = append(warnings, warn(TrainingWarningResourceArchived))
		}
		coaches := in.Coaches[training.TeamID]
		if len(coaches) == 0 {
			warnings = append(warnings, warn(TrainingWarningNoCoach))
		}
		slot, hasSlot := slots[training.PlanSlotID]
		switch {
		case !hasSlot:
			warnings = append(warnings, warn(TrainingWarningNoSlot))
		default:
			if training.StartMinute < slot.StartMinute || training.EndMinute > slot.EndMinute {
				warnings = append(warnings, warn(TrainingWarningOutsideSlot))
			}
			if teamKnown {
				ages := TeamAgeRange(team, in.Season)
				if !slotAcceptsAges(slot, ages) {
					w := warn(TrainingWarningSlotAges)
					w.TeamAges, w.SlotAges = ages, AgeRange{Min: optionalValue(slot.MinAge), Max: optionalValue(slot.MaxAge)}
					warnings = append(warnings, w)
				}
			}
			var inSlot []PlanTraining
			for _, other := range in.Trainings {
				if other.PlanSlotID == slot.ID {
					inSlot = append(inSlot, other)
				}
			}
			if peak := peakLoad(training, inSlot, alternating); peak > slot.teams() {
				w := warn(TrainingWarningSlotFull)
				w.Teams, w.Limit = peak, slot.teams()
				warnings = append(warnings, w)
			}
		}
		if resource.MaxConcurrentTeams != nil {
			var onResource []PlanTraining
			for _, other := range in.Trainings {
				if other.ResourceID == training.ResourceID {
					onResource = append(onResource, other)
				}
			}
			if peak := peakLoad(training, onResource, alternating); peak > *resource.MaxConcurrentTeams {
				w := warn(TrainingWarningResourceFull)
				w.Teams, w.Limit = peak, *resource.MaxConcurrentTeams
				warnings = append(warnings, w)
			}
		}
		for otherIndex, other := range in.Trainings {
			if otherIndex != index && other.TeamID == training.TeamID && training.overlaps(other, alternating) {
				w := warn(TrainingWarningTeamOverlap)
				w.Other = otherIndex
				warnings = append(warnings, w)
			}
		}
		for _, coach := range coaches {
			if in.Availability != nil && !in.Availability.Covers(coach, training.Week, training.Weekday, training.StartMinute, training.EndMinute) {
				w := warn(TrainingWarningCoachUnavailable)
				w.CoachUserID = coach
				warnings = append(warnings, w)
			}
			for otherIndex, other := range in.Trainings {
				if otherIndex == index || other.TeamID == training.TeamID || !training.overlaps(other, alternating) || !slices.Contains(in.Coaches[other.TeamID], coach) {
					continue
				}
				w := warn(TrainingWarningCoachBusy)
				w.CoachUserID, w.Other = coach, otherIndex
				warnings = append(warnings, w)
			}
		}
	}
	return warnings
}

// slotAcceptsAges reports whether the team's whole age range is inside the
// slot's (an open end accepts any age).
func slotAcceptsAges(slot PlanSlot, ages AgeRange) bool {
	return (slot.MinAge == nil || *slot.MinAge <= ages.Min) && (slot.MaxAge == nil || ages.Max <= *slot.MaxAge)
}

// SuggestedTraining is a training the suggestion added, with its
// explanation.
type SuggestedTraining struct {
	Training PlanTraining
	Reason   string
}

// TrainingSuggestionInput is what SuggestTrainings works from.
type TrainingSuggestionInput struct {
	Season Season
	Part   SeasonPart
	// Teams are the active teams to fill, in board order. A team that already
	// has a training in Current is left as it is.
	Teams []Team
	// Coaches are the coaches of each team in the season.
	Coaches   map[int64][]int64
	Slots     []PlanSlot
	Resources []Resource
	// Availability, when set, must cover every coach of a team.
	Availability *PartAvailability
	// Previous are the trainings of the previous season's matching part.
	Previous []PlanTraining
	// Current is the draft the suggestion adds to.
	Current []PlanTraining
}

// SuggestTrainings proposes trainings for teams without one, never removing
// or changing what Current holds:
//
//  1. a team gets the trainings it had in the previous season's matching
//     part, on the same active resource at the same day and time, even if
//     they now break a rule (the board then warns);
//  2. a team still without a training gets one in the first resource slot
//     where the team's ages are inside the slot's ages (slots with an age
//     range before open ones), all of the team's coaches are available for
//     the slot's whole time, the slot and its resource have room, no coach
//     trains another team at the same time, and the team is not already
//     training then. The training takes the slot's time. In a part with
//     alternating weeks an every-week slot is tried as an every-week
//     training first, then for week A and week B;
//  3. a team no slot fits stays without a training; the number of such
//     teams is returned.
//
// Teams are filled in the given order and every pick counts for the next
// team, so the result is deterministic.
func SuggestTrainings(in TrainingSuggestionInput) ([]SuggestedTraining, int) {
	alternating := in.Part.AlternatingWeeks
	resources := make(map[int64]Resource, len(in.Resources))
	for _, resource := range in.Resources {
		resources[resource.ID] = resource
	}
	all := slices.Clone(in.Current)
	for index := range all {
		all[index].PlanSlotID = 0
		if slot, ok := bestSlot(all[index], in.Slots, alternating); ok {
			all[index].PlanSlotID = slot.ID
		}
	}
	has := make(map[int64]bool)
	for _, training := range in.Current {
		has[training.TeamID] = true
	}
	var suggested []SuggestedTraining
	add := func(training PlanTraining, reason string) {
		training.ID, training.PlanSlotID = 0, 0
		if slot, ok := bestSlot(training, in.Slots, alternating); ok {
			training.PlanSlotID = slot.ID
		}
		all = append(all, training)
		has[training.TeamID] = true
		suggested = append(suggested, SuggestedTraining{Training: training, Reason: reason})
	}
	for _, team := range in.Teams {
		if has[team.ID] {
			continue
		}
		var taken []PlanTraining
		for _, previous := range in.Previous {
			if previous.TeamID != team.ID {
				continue
			}
			if resource, ok := resources[previous.ResourceID]; !ok || resource.Archived() {
				continue
			}
			training := previous
			if !alternating {
				training.Week = SeasonWeekAll
			}
			if slices.ContainsFunc(taken, func(other PlanTraining) bool { return other.key() == training.key() }) {
				continue
			}
			taken = append(taken, training)
		}
		for _, training := range taken {
			add(training, TrainingSuggestionPreviousSeason)
		}
	}
	// Slots with an age range come first; the sort is stable, so each group
	// keeps the slot order (weekday, start, resource, ID).
	slots := slices.Clone(in.Slots)
	slices.SortStableFunc(slots, func(a, b PlanSlot) int {
		return cmp.Compare(boolInt(b.MinAge != nil || b.MaxAge != nil), boolInt(a.MinAge != nil || a.MaxAge != nil))
	})
	unplaced := 0
	for _, team := range in.Teams {
		if has[team.ID] {
			continue
		}
		ages := TeamAgeRange(team, in.Season)
		placed := false
	search:
		for _, slot := range slots {
			resource, ok := resources[slot.ResourceID]
			if !ok || resource.Archived() || !slotAcceptsAges(slot, ages) {
				continue
			}
			weeks := []string{SeasonWeekAll}
			if alternating {
				weeks = []string{slot.Week}
				if slot.Week == SeasonWeekAll {
					weeks = []string{SeasonWeekAll, SeasonWeekA, SeasonWeekB}
				}
			}
			for _, week := range weeks {
				candidate := PlanTraining{
					TeamID: team.ID, ResourceID: slot.ResourceID, PlanSlotID: slot.ID, Week: week,
					Weekday: slot.Weekday, StartMinute: slot.StartMinute, EndMinute: slot.EndMinute,
				}
				if trainingFits(in, slot, resource, candidate, all) {
					add(candidate, TrainingSuggestionSlot)
					placed = true
					break search
				}
			}
		}
		if !placed {
			unplaced++
		}
	}
	return suggested, unplaced
}

// trainingFits reports whether candidate, in slot, breaks none of the rules
// the suggestion keeps, next to the trainings already planned.
func trainingFits(in TrainingSuggestionInput, slot PlanSlot, resource Resource, candidate PlanTraining, planned []PlanTraining) bool {
	alternating := in.Part.AlternatingWeeks
	coaches := in.Coaches[candidate.TeamID]
	if in.Availability != nil {
		for _, coach := range coaches {
			if !in.Availability.Covers(coach, candidate.Week, candidate.Weekday, candidate.StartMinute, candidate.EndMinute) {
				return false
			}
		}
	}
	var inSlot, onResource []PlanTraining
	for _, other := range planned {
		if candidate.overlaps(other, alternating) {
			if other.TeamID == candidate.TeamID {
				return false
			}
			for _, coach := range coaches {
				if slices.Contains(in.Coaches[other.TeamID], coach) {
					return false
				}
			}
		}
		if other.PlanSlotID == slot.ID {
			inSlot = append(inSlot, other)
		}
		if other.ResourceID == candidate.ResourceID {
			onResource = append(onResource, other)
		}
	}
	if peakLoad(candidate, append(inSlot, candidate), alternating) > slot.teams() {
		return false
	}
	if resource.MaxConcurrentTeams != nil && peakLoad(candidate, append(onResource, candidate), alternating) > *resource.MaxConcurrentTeams {
		return false
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
